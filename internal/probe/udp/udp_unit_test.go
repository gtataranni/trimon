package udp

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/gtataranni/trimon/pkg/types"
)

// listenUDPEcho starts a UDP echo server on a loopback ephemeral port and
// returns its port. It echoes every datagram back to its sender until the test
// cleans it up.
func listenUDPEcho(t *testing.T) int {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(buf[:n], addr)
		}
	}()
	return pc.LocalAddr().(*net.UDPAddr).Port
}

// listenUDPReply starts a UDP server that replies to every datagram with a
// fixed response, ignoring the request payload.
func listenUDPReply(t *testing.T, reply []byte) int {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 65535)
		for {
			_, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(reply, addr)
		}
	}()
	return pc.LocalAddr().(*net.UDPAddr).Port
}

// listenUDPSilent starts a UDP server that reads and discards datagrams without
// ever replying — a deterministic, local black hole for the timeout path.
func listenUDPSilent(t *testing.T) int {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 65535)
		for {
			if _, _, err := pc.ReadFrom(buf); err != nil {
				return
			}
		}
	}()
	return pc.LocalAddr().(*net.UDPAddr).Port
}

func baseCfg(port int, overrides ...func(*types.ProbeConfig)) types.ProbeConfig {
	cfg := types.ProbeConfig{
		Name:           "test",
		Type:           "udp",
		Targets:        []string{"127.0.0.1"},
		Count:          3,
		PacketInterval: 10 * time.Millisecond,
		Timeout:        2 * time.Second,
		Interval:       30 * time.Second,
		UDP:            &types.UDPConfig{Port: port, Payload: "trimon-ping"},
	}
	for _, fn := range overrides {
		fn(&cfg)
	}
	return cfg
}

func TestUDPSuccess(t *testing.T) {
	port := listenUDPEcho(t)
	results := New(baseCfg(port)).Run(context.Background())
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
	if r.PacketLossRatio != 0 {
		t.Errorf("PacketLossRatio: got %f, want 0", r.PacketLossRatio)
	}
	if r.RTTMeanMS <= 0 {
		t.Errorf("RTTMeanMS: got %f, want > 0", r.RTTMeanMS)
	}
	if r.PortOpen == nil || !*r.PortOpen {
		t.Errorf("PortOpen: got %v, want true (got a reply)", r.PortOpen)
	}
}

// A closed UDP port returns an ICMP port-unreachable (ECONNREFUSED on the
// connected socket): the host is reachable, so this is success with loss=0, but
// PortOpen is false to distinguish closed from open — mirroring a TCP RST.
func TestUDPClosedPortIsReachable(t *testing.T) {
	// Bind a UDP socket to claim a port, then close it so datagrams to it draw an
	// ICMP port-unreachable.
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := pc.LocalAddr().(*net.UDPAddr).Port
	_ = pc.Close()

	r := New(baseCfg(port)).Run(context.Background())[0]
	if r.Status != types.StatusSuccess {
		t.Errorf("Status: got %s, want StatusSuccess (ICMP unreachable = reachable; error: %s)", r.Status, r.ErrorMsg)
	}
	if r.PacketsReceived != 3 {
		t.Errorf("PacketsReceived: got %d, want 3 (ICMP unreachable counts as a reply)", r.PacketsReceived)
	}
	if r.PacketLossRatio != 0 {
		t.Errorf("PacketLossRatio: got %f, want 0", r.PacketLossRatio)
	}
	if r.PortOpen == nil || *r.PortOpen {
		t.Errorf("PortOpen: got %v, want false (closed port)", r.PortOpen)
	}
}

// An empty payload sends a zero-length datagram; an echo server replies with an
// empty datagram, which still counts as a reply.
func TestUDPEmptyPayloadSuccess(t *testing.T) {
	port := listenUDPEcho(t)
	cfg := baseCfg(port, func(c *types.ProbeConfig) {
		c.UDP = &types.UDPConfig{Port: port}
	})
	r := New(cfg).Run(context.Background())[0]
	if r.Status != types.StatusSuccess {
		t.Errorf("Status: got %s, want StatusSuccess (error: %s)", r.Status, r.ErrorMsg)
	}
	if r.PacketsReceived != 3 {
		t.Errorf("PacketsReceived: got %d, want 3", r.PacketsReceived)
	}
}

// A reply whose prefix matches expected_response counts as received.
func TestUDPExpectedResponseMatch(t *testing.T) {
	port := listenUDPReply(t, []byte("PONG v1"))
	cfg := baseCfg(port, func(c *types.ProbeConfig) {
		c.UDP = &types.UDPConfig{Port: port, Payload: "PING", ExpectedResponse: "PONG"}
	})
	r := New(cfg).Run(context.Background())[0]
	if r.Status != types.StatusSuccess {
		t.Errorf("Status: got %s, want StatusSuccess (error: %s)", r.Status, r.ErrorMsg)
	}
	if r.PacketsReceived != 3 {
		t.Errorf("PacketsReceived: got %d, want 3", r.PacketsReceived)
	}
}

// A reply that does not match expected_response is not a usable reply: total
// loss, down, but not an error (the host did respond).
func TestUDPExpectedResponseMismatch(t *testing.T) {
	port := listenUDPReply(t, []byte("NOPE"))
	cfg := baseCfg(port, func(c *types.ProbeConfig) {
		c.UDP = &types.UDPConfig{Port: port, Payload: "PING", ExpectedResponse: "PONG"}
	})
	r := New(cfg).Run(context.Background())[0]
	if r.Status != types.StatusFailure {
		t.Errorf("Status: got %s, want StatusFailure", r.Status)
	}
	if r.PacketsReceived != 0 {
		t.Errorf("PacketsReceived: got %d, want 0 (no matching reply)", r.PacketsReceived)
	}
	if r.PacketLossRatio != 1 {
		t.Errorf("PacketLossRatio: got %f, want 1", r.PacketLossRatio)
	}
	if r.PortOpen == nil || *r.PortOpen {
		t.Errorf("PortOpen: got %v, want false (non-matching reply is not open)", r.PortOpen)
	}
}

// A silent target never replies: every Read times out — total loss, down, not
// an error.
func TestUDPNoReplyIsFailure(t *testing.T) {
	port := listenUDPSilent(t)
	cfg := baseCfg(port, func(c *types.ProbeConfig) {
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
	// Silence is open|filtered/down: up=0, PortOpen false (not nil).
	if r.PortOpen == nil || *r.PortOpen {
		t.Errorf("PortOpen: got %v, want false (silence)", r.PortOpen)
	}
}

func TestUDPResolveError(t *testing.T) {
	cfg := baseCfg(53, func(c *types.ProbeConfig) {
		c.Targets = []string{"invalid..hostname"}
	})
	results := New(cfg).Run(context.Background())
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	r := results[0]
	if r.Status != types.StatusError {
		t.Errorf("Status: got %s, want StatusError", r.Status)
	}
	if r.ErrorType != "resolve_error" {
		t.Errorf("ErrorType: got %q, want resolve_error", r.ErrorType)
	}
	if r.PacketsSent != 0 {
		t.Errorf("PacketsSent must be 0 on resolve error, got %d", r.PacketsSent)
	}
	if r.PortOpen != nil {
		t.Errorf("PortOpen: got %v, want nil (not applicable on error)", r.PortOpen)
	}
}

func TestUDPProbeType(t *testing.T) {
	port := listenUDPEcho(t)
	results := New(baseCfg(port)).Run(context.Background())
	if results[0].ProbeType != "udp" {
		t.Errorf("ProbeType: got %q, want udp", results[0].ProbeType)
	}
}
