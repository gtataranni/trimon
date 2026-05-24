//go:build integration

package icmp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gtataranni/trimon/pkg/types"
)

// unreachableTarget is a documentation-range IP (RFC 5737, TEST-NET-1) that
// will never respond to ICMP echo. Any ICMP probe to it will time out cleanly.
const unreachableTarget = "192.0.2.1"

// newProbe is a convenience constructor for integration tests.
func newProbe(count int, packetInterval, timeout, interval time.Duration) *Prober {
	return New(types.ProbeConfig{
		Name:           "test",
		Type:           "icmp",
		Target:         unreachableTarget,
		Count:          count,
		PacketInterval: packetInterval,
		Timeout:        timeout,
		Interval:       interval,
	})
}

// requireSocket skips the test if the prober cannot open a raw socket.
// Run integration tests with: sudo go test -tags integration ./internal/probe/icmp/...
func requireSocket(t *testing.T) {
	t.Helper()
	p := newProbe(1, 100*time.Millisecond, 200*time.Millisecond, time.Second)
	r, _ := p.Run(context.Background())
	if r.Status == types.StatusError &&
		(strings.Contains(r.ErrorMsg, "operation not permitted") ||
			strings.Contains(r.ErrorMsg, "permission denied")) {
		t.Skip("raw socket unavailable (re-run with sudo or CAP_NET_RAW)")
	}
}

// TestTimeoutFiresBeforeAllPacketsSent validates behaviour when pinger.Timeout
// is shorter than count*packetInterval: pro-bing stops mid-sequence and reports
// only the packets it actually sent.
//
// Invariant under test:
//
//	count=10, packet_interval=500ms → full sequence takes ≥4.5s
//	timeout=2s                      → fires after ~4 packets
//
// Expected: PacketsSent < 10, PacketsReceived=0, StatusFailure (100% of sent lost).
func TestTimeoutFiresBeforeAllPacketsSent(t *testing.T) {
	requireSocket(t)
	p := newProbe(10, 500*time.Millisecond, 2*time.Second, 30*time.Second)

	ctx := context.Background()
	start := time.Now()
	result, err := p.Run(ctx)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Logf("elapsed=%v packets_sent=%d packets_received=%d loss=%.2f status=%s",
		elapsed, result.PacketsSent, result.PacketsReceived, result.PacketLossRatio, result.Status)

	if result.PacketsSent == 0 {
		t.Fatal("expected at least one packet sent before timeout")
	}
	if result.PacketsSent >= 10 {
		t.Errorf("expected fewer than 10 packets sent (timeout should have fired), got %d", result.PacketsSent)
	}
	if result.PacketsReceived != 0 {
		t.Errorf("expected 0 packets received from unreachable target, got %d", result.PacketsReceived)
	}
	if result.Status != types.StatusFailure {
		t.Errorf("expected StatusFailure, got %s", result.Status)
	}
	if result.PacketLossRatio != 1.0 {
		t.Errorf("expected 100%% loss, got %.4f", result.PacketLossRatio)
	}
	// Sanity: elapsed should be ≈ timeout, not count*packetInterval.
	if elapsed > 3*time.Second {
		t.Errorf("probe took %v, expected ≈ 2s (timeout budget)", elapsed)
	}
}

// TestTimeoutAfterAllPacketsSent validates behaviour when timeout > count*packetInterval:
// all packets are sent, then pro-bing waits for replies until timeout fires.
//
// Invariant under test:
//
//	count=3, packet_interval=200ms → full sequence sent in ~0.4s
//	timeout=2s                     → fires ~1.6s after last send
//
// Expected: PacketsSent=3, PacketsReceived=0, StatusFailure.
func TestTimeoutAfterAllPacketsSent(t *testing.T) {
	requireSocket(t)
	p := newProbe(3, 200*time.Millisecond, 2*time.Second, 30*time.Second)

	ctx := context.Background()
	start := time.Now()
	result, err := p.Run(ctx)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Logf("elapsed=%v packets_sent=%d packets_received=%d loss=%.2f status=%s",
		elapsed, result.PacketsSent, result.PacketsReceived, result.PacketLossRatio, result.Status)

	if result.PacketsSent != 3 {
		t.Errorf("expected all 3 packets sent, got %d", result.PacketsSent)
	}
	if result.PacketsReceived != 0 {
		t.Errorf("expected 0 packets received from unreachable target, got %d", result.PacketsReceived)
	}
	if result.Status != types.StatusFailure {
		t.Errorf("expected StatusFailure, got %s", result.Status)
	}
}

// TestContextCancelledBeforeProbeTimeout validates behaviour when the caller
// cancels the context before pinger.Timeout would fire.
//
// The current prober code swallows the RunWithContext error when ctx.Err()!=nil
// and falls through to collect whatever stats pro-bing accumulated.
//
// Invariant under test:
//
//	count=10, packet_interval=500ms, timeout=10s
//	ctx cancelled after 1.5s → fires after ~3 packets
//
// Expected: PacketsSent < 10, StatusFailure (or StatusError if pro-bing
// returns zero stats on context cancellation — this test documents which).
func TestContextCancelledBeforeProbeTimeout(t *testing.T) {
	requireSocket(t)
	p := newProbe(10, 500*time.Millisecond, 10*time.Second, 30*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	start := time.Now()
	result, err := p.Run(ctx)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Logf("elapsed=%v packets_sent=%d packets_received=%d loss=%.2f status=%s error_msg=%q",
		elapsed, result.PacketsSent, result.PacketsReceived, result.PacketLossRatio, result.Status, result.ErrorMsg)

	// Document whether pro-bing surfaces partial stats or zeros on ctx cancel.
	if result.Status == types.StatusError {
		t.Logf("pro-bing returned zero stats on context cancellation → StatusError path triggered (packets_sent=0 check)")
	} else {
		t.Logf("pro-bing returned partial stats on context cancellation → StatusFailure with %d/%d packets", result.PacketsReceived, result.PacketsSent)
	}

	// Sanity: probe should have stopped at ~1.5s, not run to 10s.
	if elapsed > 2500*time.Millisecond {
		t.Errorf("probe took %v, expected ≈ 1.5s (context deadline)", elapsed)
	}
}
