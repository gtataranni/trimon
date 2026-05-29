package tcp

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/gtataranni/trimon/pkg/types"
)

// listenTCP starts an accepting TCP listener on a loopback ephemeral port and
// returns its port. The listener accepts and immediately closes connections
// until the test cleans it up.
func listenTCP(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func baseCfg(port int, overrides ...func(*types.ProbeConfig)) types.ProbeConfig {
	cfg := types.ProbeConfig{
		Name:           "test",
		Type:           "tcp",
		Targets:        []string{"127.0.0.1"},
		Count:          3,
		PacketInterval: 10 * time.Millisecond,
		Timeout:        2 * time.Second,
		Interval:       30 * time.Second,
		TCP:            &types.TCPConfig{Port: port},
	}
	for _, fn := range overrides {
		fn(&cfg)
	}
	return cfg
}

func TestTCPSuccess(t *testing.T) {
	port := listenTCP(t)
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
		t.Errorf("PortOpen: got %v, want true (open port)", r.PortOpen)
	}
}

// A refused connection (RST) means the host is reachable but the port is closed.
// It counts as a received reply, so status is success and probe is up; PortOpen
// is false to distinguish closed from open.
func TestTCPConnectionRefusedIsReachable(t *testing.T) {
	// Bind a listener to claim a port, then close it so connects are refused.
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	results := New(baseCfg(port)).Run(context.Background())
	r := results[0]
	if r.Status != types.StatusSuccess {
		t.Errorf("Status: got %s, want StatusSuccess (RST = reachable)", r.Status)
	}
	if r.PacketsReceived != 3 {
		t.Errorf("PacketsReceived: got %d, want 3 (RST counts as a reply)", r.PacketsReceived)
	}
	if r.PacketLossRatio != 0 {
		t.Errorf("PacketLossRatio: got %f, want 0", r.PacketLossRatio)
	}
	if r.PortOpen == nil || *r.PortOpen {
		t.Errorf("PortOpen: got %v, want false (closed port)", r.PortOpen)
	}
}

// An unrouteable / silent target yields no reply: total loss, down, not an error.
func TestTCPUnreachableIsFailure(t *testing.T) {
	// 192.0.2.0/24 (TEST-NET-1) is reserved and not routable; connects time out.
	cfg := baseCfg(80, func(c *types.ProbeConfig) {
		c.Targets = []string{"192.0.2.1"}
		c.Count = 1
		c.Timeout = 300 * time.Millisecond
	})
	r := New(cfg).Run(context.Background())[0]
	if r.Status != types.StatusFailure {
		t.Errorf("Status: got %s, want StatusFailure", r.Status)
	}
	if r.PortOpen == nil || *r.PortOpen {
		t.Errorf("PortOpen: got %v, want false (no reply)", r.PortOpen)
	}
}

func TestTCPResolveError(t *testing.T) {
	cfg := baseCfg(80, func(c *types.ProbeConfig) {
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
}

func TestTCPProbeType(t *testing.T) {
	port := listenTCP(t)
	results := New(baseCfg(port)).Run(context.Background())
	if results[0].ProbeType != "tcp" {
		t.Errorf("ProbeType: got %q, want tcp", results[0].ProbeType)
	}
}
