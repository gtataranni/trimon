package dns

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/gtataranni/trimon/pkg/types"
)

// startDNSServer starts a UDP DNS server on a loopback ephemeral port with the
// given handler and returns its "host:port".
func startDNSServer(t *testing.T, handler dns.HandlerFunc) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &dns.Server{PacketConn: pc, Handler: handler}
	started := make(chan struct{})
	srv.NotifyStartedFunc = func() { close(started) }
	go func() { _ = srv.ActivateAndServe() }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("dns server did not start")
	}
	t.Cleanup(func() { _ = srv.Shutdown() })
	return pc.LocalAddr().String()
}

// aReply answers every query with a single A record pointing at ip.
func aReply(ip string) dns.HandlerFunc {
	return func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		m.Answer = append(m.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: r.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
			A:   net.ParseIP(ip),
		})
		_ = w.WriteMsg(m)
	}
}

// txtReply answers every query with a single TXT record.
func txtReply(txt string) dns.HandlerFunc {
	return func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		m.Answer = append(m.Answer, &dns.TXT{
			Hdr: dns.RR_Header{Name: r.Question[0].Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 60},
			Txt: []string{txt},
		})
		_ = w.WriteMsg(m)
	}
}

// rcodeReply answers every query with the given Rcode and no answers.
func rcodeReply(rcode int) dns.HandlerFunc {
	return func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetRcode(r, rcode)
		_ = w.WriteMsg(m)
	}
}

// silent never replies, so queries time out.
func silent() dns.HandlerFunc {
	return func(w dns.ResponseWriter, r *dns.Msg) {}
}

func baseCfg(resolver string, overrides ...func(*types.ProbeConfig)) types.ProbeConfig {
	cfg := types.ProbeConfig{
		Name:           "test",
		Type:           "dns",
		Targets:        []string{"example.com"},
		Count:          3,
		PacketInterval: 10 * time.Millisecond,
		Timeout:        2 * time.Second,
		Interval:       30 * time.Second,
		DNS:            &types.DNSConfig{RecordType: "A", Resolver: resolver},
	}
	for _, fn := range overrides {
		fn(&cfg)
	}
	return cfg
}

func TestDNSSuccess(t *testing.T) {
	server := startDNSServer(t, aReply("1.2.3.4"))
	results := New(baseCfg(server)).Run(context.Background())
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	r := results[0]
	if r.Status != types.StatusSuccess {
		t.Errorf("Status: got %s, want StatusSuccess (error: %s)", r.Status, r.ErrorMsg)
	}
	if r.PacketsSent != 3 || r.PacketsReceived != 3 {
		t.Errorf("packets: sent=%d recv=%d, want 3/3", r.PacketsSent, r.PacketsReceived)
	}
	if r.RTTMeanMS <= 0 {
		t.Errorf("RTTMeanMS: got %f, want > 0", r.RTTMeanMS)
	}
}

func TestDNSExpectedAnswerMatch(t *testing.T) {
	server := startDNSServer(t, aReply("1.2.3.4"))
	cfg := baseCfg(server, func(c *types.ProbeConfig) {
		c.DNS.ExpectedAnswer = []string{"9.9.9.9", "1.2.3.4"}
	})
	r := New(cfg).Run(context.Background())[0]
	if r.Status != types.StatusSuccess {
		t.Errorf("Status: got %s, want StatusSuccess (error: %s)", r.Status, r.ErrorMsg)
	}
	if r.PacketsReceived != 3 {
		t.Errorf("PacketsReceived: got %d, want 3", r.PacketsReceived)
	}
}

func TestDNSExpectedAnswerMismatch(t *testing.T) {
	server := startDNSServer(t, aReply("1.2.3.4"))
	cfg := baseCfg(server, func(c *types.ProbeConfig) {
		c.DNS.ExpectedAnswer = []string{"9.9.9.9"}
	})
	r := New(cfg).Run(context.Background())[0]
	if r.Status != types.StatusFailure {
		t.Errorf("Status: got %s, want StatusFailure", r.Status)
	}
	if r.PacketsReceived != 0 {
		t.Errorf("PacketsReceived: got %d, want 0 (no matching answer)", r.PacketsReceived)
	}
}

func TestDNSTXTMatch(t *testing.T) {
	server := startDNSServer(t, txtReply("v=spf1 -all"))
	cfg := baseCfg(server, func(c *types.ProbeConfig) {
		c.DNS.RecordType = "TXT"
		c.DNS.ExpectedAnswer = []string{"v=spf1 -all"}
	})
	r := New(cfg).Run(context.Background())[0]
	if r.Status != types.StatusSuccess {
		t.Errorf("Status: got %s, want StatusSuccess (error: %s)", r.Status, r.ErrorMsg)
	}
}

// NXDOMAIN means the resolver is reachable; with no expected answer it is a
// usable reply (success), since the probe is verifying resolver liveness.
func TestDNSNXDOMAINReachable(t *testing.T) {
	server := startDNSServer(t, rcodeReply(dns.RcodeNameError))
	r := New(baseCfg(server)).Run(context.Background())[0]
	if r.Status != types.StatusSuccess {
		t.Errorf("Status: got %s, want StatusSuccess (NXDOMAIN = resolver reachable)", r.Status)
	}
	if r.PacketsReceived != 3 {
		t.Errorf("PacketsReceived: got %d, want 3", r.PacketsReceived)
	}
}

// NXDOMAIN cannot satisfy an expected answer, so it is a failure when one is set.
func TestDNSNXDOMAINWithExpectedIsFailure(t *testing.T) {
	server := startDNSServer(t, rcodeReply(dns.RcodeNameError))
	cfg := baseCfg(server, func(c *types.ProbeConfig) {
		c.DNS.ExpectedAnswer = []string{"1.2.3.4"}
	})
	r := New(cfg).Run(context.Background())[0]
	if r.Status != types.StatusFailure {
		t.Errorf("Status: got %s, want StatusFailure", r.Status)
	}
}

func TestDNSServfailIsFailure(t *testing.T) {
	server := startDNSServer(t, rcodeReply(dns.RcodeServerFailure))
	r := New(baseCfg(server)).Run(context.Background())[0]
	if r.Status != types.StatusFailure {
		t.Errorf("Status: got %s, want StatusFailure (SERVFAIL)", r.Status)
	}
	if r.PacketsReceived != 0 {
		t.Errorf("PacketsReceived: got %d, want 0", r.PacketsReceived)
	}
}

// A silent resolver yields timeouts on every attempt: total loss, down, but not
// an error (the probe ran fine).
func TestDNSNoReplyIsFailure(t *testing.T) {
	server := startDNSServer(t, silent())
	cfg := baseCfg(server, func(c *types.ProbeConfig) {
		c.Count = 1
		c.Timeout = 300 * time.Millisecond
	})
	r := New(cfg).Run(context.Background())[0]
	if r.Status != types.StatusFailure {
		t.Errorf("Status: got %s, want StatusFailure", r.Status)
	}
	if r.PacketsSent != 1 || r.PacketsReceived != 0 {
		t.Errorf("packets: sent=%d recv=%d, want 1/0", r.PacketsSent, r.PacketsReceived)
	}
}

// An unreachable resolver (nothing listening) is a query-time failure, not a
// StatusError — StatusError is reserved for the probe being unable to run.
func TestDNSUnreachableResolverIsFailure(t *testing.T) {
	cfg := baseCfg("127.0.0.1:1", func(c *types.ProbeConfig) {
		c.Count = 1
		c.Timeout = 300 * time.Millisecond
	})
	r := New(cfg).Run(context.Background())[0]
	if r.Status != types.StatusFailure {
		t.Errorf("Status: got %s, want StatusFailure (error: %s)", r.Status, r.ErrorMsg)
	}
}

func TestDNSProbeType(t *testing.T) {
	server := startDNSServer(t, aReply("1.2.3.4"))
	r := New(baseCfg(server)).Run(context.Background())[0]
	if r.ProbeType != "dns" {
		t.Errorf("ProbeType: got %q, want dns", r.ProbeType)
	}
	if r.Target != "example.com" {
		t.Errorf("Target: got %q, want the query name example.com", r.Target)
	}
}
