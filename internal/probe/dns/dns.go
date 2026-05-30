package dns

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"

	"github.com/gtataranni/trimon/internal/probe"
	"github.com/gtataranni/trimon/pkg/types"
)

// Prober implements probe.Prober for DNS queries. Each tick it sends Count
// queries (PacketInterval apart) for each target query name to the configured
// resolver — or the system resolver from /etc/resolv.conf — over UDP, and
// measures the round-trip time reported by the resolver library.
//
// An attempt counts as received when the resolver answers before the timeout
// with an acceptable Rcode (NOERROR, or NXDOMAIN when no specific answer is
// expected) and, if ExpectedAnswer is set, at least one answer matches. A
// timeout, network error, SERVFAIL/REFUSED, or answer mismatch is loss. Packet
// loss across the attempts then maps to success/partial/failure, exactly like
// the other probers. StatusError is reserved for the probe being unable to run
// at all (no resolver could be determined).
//
// Targets are query names, not hosts to connect to, so they are used verbatim
// (no ExpandTargets / re-resolution). SourceIP is not used by DNS probes.
type Prober struct {
	cfg types.ProbeConfig
}

// New creates a new DNS Prober from cfg.
func New(cfg types.ProbeConfig) *Prober {
	return &Prober{cfg: cfg}
}

func (p *Prober) Name() string { return p.cfg.Name }
func (p *Prober) Type() string { return "dns" }

// Run queries all target names in parallel within ctx, returning one
// ProbeResult per target. The resolver address is determined once per run.
func (p *Prober) Run(ctx context.Context) []types.ProbeResult {
	if len(p.cfg.Targets) == 0 {
		return nil
	}

	server, serverErr := p.resolveServer()

	results := make([]types.ProbeResult, len(p.cfg.Targets))
	var wg sync.WaitGroup
	for i, qname := range p.cfg.Targets {
		wg.Add(1)
		go func(idx int, name string) {
			defer wg.Done()
			results[idx] = p.probeOne(ctx, name, server, serverErr)
		}(i, qname)
	}
	wg.Wait()
	return results
}

// resolveServer returns the "host:port" of the nameserver to query: the
// configured resolver if set, otherwise the first nameserver in
// /etc/resolv.conf. A non-nil error means no resolver could be determined and
// the probe cannot run.
func (p *Prober) resolveServer() (string, error) {
	if p.cfg.DNS.Resolver != "" {
		return p.cfg.DNS.Resolver, nil
	}
	conf, err := dns.ClientConfigFromFile("/etc/resolv.conf")
	if err != nil {
		return "", fmt.Errorf("read system resolver config: %w", err)
	}
	if len(conf.Servers) == 0 {
		return "", fmt.Errorf("no nameservers configured in /etc/resolv.conf")
	}
	return net.JoinHostPort(conf.Servers[0], conf.Port), nil
}

// probeOne runs Count DNS queries for a single name and returns a populated result.
func (p *Prober) probeOne(ctx context.Context, qname, server string, serverErr error) types.ProbeResult {
	cfg := p.cfg
	result := types.ProbeResult{
		Timestamp: time.Now().UTC(),
		ProbeName: cfg.Name,
		Target:    qname, // for DNS the target is the query name
		SourceIP:  cfg.SourceIP,
		ProbeType: "dns",
		Labels:    cfg.Labels,
	}

	if serverErr != nil {
		result.Status = types.StatusError
		result.ErrorType = "init_error"
		result.ErrorMsg = serverErr.Error()
		return result
	}

	qtype := qtypeFor(cfg.DNS.RecordType)
	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn(qname), qtype)
	// UDP client; truncated (TC=1) responses are not retried over TCP — answer
	// matching may then fail for very large record sets, which is rare.
	client := &dns.Client{Timeout: cfg.Timeout}

	// dnsAttempt never reports a fatal error: a query-time failure (timeout,
	// SERVFAIL, mismatch) is loss, not StatusError.
	probe.RunLoop(ctx, &result, cfg.Count, cfg.PacketInterval, func(ctx context.Context) probe.Attempt {
		rtt, ok := dnsAttempt(ctx, client, msg, server, qtype, cfg.DNS.ExpectedAnswer)
		return probe.Attempt{RTT: rtt, Received: ok}
	})

	return result
}

// dnsAttempt sends one query and reports the round-trip time and whether the
// response is usable. ok is true only when the resolver answered before the
// deadline with an acceptable Rcode and (if expected answers are configured) a
// matching answer. A timeout, network error, SERVFAIL/REFUSED, or mismatch
// yields ok == false — never an aborting error (query-time failures are loss).
func dnsAttempt(ctx context.Context, c *dns.Client, msg *dns.Msg, server string, qtype uint16, expected []string) (time.Duration, bool) {
	resp, rtt, err := c.ExchangeContext(ctx, msg, server)
	if err != nil {
		return 0, false // timeout or network error: no usable reply
	}

	switch resp.Rcode {
	case dns.RcodeSuccess:
		// proceed to answer matching below
	case dns.RcodeNameError:
		// NXDOMAIN: the resolver is reachable and answered. With no specific
		// answer expected this is a usable reply; otherwise it cannot match.
		if len(expected) == 0 {
			return rtt, true
		}
		return 0, false
	default:
		// SERVFAIL, REFUSED, etc.: the resolver could not serve the query.
		return 0, false
	}

	if len(expected) == 0 {
		return rtt, true
	}
	if matchAnswer(resp, qtype, expected) {
		return rtt, true
	}
	return 0, false
}

// matchAnswer reports whether any answer RR of the queried type equals one of
// the expected values (case-insensitive).
func matchAnswer(resp *dns.Msg, qtype uint16, expected []string) bool {
	for _, got := range extractAnswers(resp, qtype) {
		for _, want := range expected {
			if strings.EqualFold(got, want) {
				return true
			}
		}
	}
	return false
}

// extractAnswers returns the string form of each answer RR matching qtype.
// Trailing dots are trimmed from names so expected values can be written
// without them; TXT chunks are concatenated to reconstruct the full string.
func extractAnswers(resp *dns.Msg, qtype uint16) []string {
	var out []string
	for _, rr := range resp.Answer {
		switch v := rr.(type) {
		case *dns.A:
			if qtype == dns.TypeA {
				out = append(out, v.A.String())
			}
		case *dns.AAAA:
			if qtype == dns.TypeAAAA {
				out = append(out, v.AAAA.String())
			}
		case *dns.CNAME:
			if qtype == dns.TypeCNAME {
				out = append(out, strings.TrimSuffix(v.Target, "."))
			}
		case *dns.MX:
			if qtype == dns.TypeMX {
				out = append(out, strings.TrimSuffix(v.Mx, "."))
			}
		case *dns.TXT:
			if qtype == dns.TypeTXT {
				out = append(out, strings.Join(v.Txt, ""))
			}
		}
	}
	return out
}

// qtypeFor maps a validated, upper-cased record type to its dns type constant,
// defaulting to A.
func qtypeFor(recordType string) uint16 {
	switch recordType {
	case "AAAA":
		return dns.TypeAAAA
	case "CNAME":
		return dns.TypeCNAME
	case "MX":
		return dns.TypeMX
	case "TXT":
		return dns.TypeTXT
	default:
		return dns.TypeA
	}
}
