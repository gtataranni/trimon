package probe

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gtataranni/trimon/pkg/types"
)

func TestStatusFromLoss(t *testing.T) {
	tests := []struct {
		name string
		loss float64
		want types.Status
	}{
		{"no loss is success", 0, types.StatusSuccess},
		{"partial loss is partial", 0.5, types.StatusPartial},
		{"near-total loss is partial", 0.99, types.StatusPartial},
		{"total loss is failure", 1, types.StatusFailure},
		{"over-total loss is failure", 1.5, types.StatusFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StatusFromLoss(tt.loss); got != tt.want {
				t.Errorf("StatusFromLoss(%v) = %s, want %s", tt.loss, got, tt.want)
			}
		})
	}
}

func TestNewResult(t *testing.T) {
	cfg := types.ProbeConfig{
		Name:     "probe-a",
		SourceIP: "10.0.0.1",
		Labels:   map[string]string{"env": "prod"},
	}

	t.Run("bare IP resolves", func(t *testing.T) {
		r, ok := NewResult(cfg, WorkItem{IP: "192.0.2.5"}, "tcp")
		if !ok {
			t.Fatalf("ok = false, want true for a bare IP")
		}
		if r.Target != "192.0.2.5" || r.FQDN != "" {
			t.Errorf("Target/FQDN = %q/%q, want 192.0.2.5/\"\"", r.Target, r.FQDN)
		}
		if r.ProbeName != "probe-a" || r.SourceIP != "10.0.0.1" || r.ProbeType != "tcp" {
			t.Errorf("identifying fields not populated: %+v", r)
		}
		if r.Labels["env"] != "prod" {
			t.Errorf("Labels not passed through: %v", r.Labels)
		}
		if r.Status != "" {
			t.Errorf("Status = %q, want empty (caller sets it)", r.Status)
		}
	})

	t.Run("resolved FQDN proceeds", func(t *testing.T) {
		r, ok := NewResult(cfg, WorkItem{IP: "192.0.2.5", FQDN: "example.com"}, "icmp")
		if !ok {
			t.Fatalf("ok = false, want true when IP != FQDN")
		}
		if r.FQDN != "example.com" {
			t.Errorf("FQDN = %q, want example.com", r.FQDN)
		}
	})

	t.Run("unresolved FQDN is a resolve error", func(t *testing.T) {
		r, ok := NewResult(cfg, WorkItem{IP: "bad.invalid", FQDN: "bad.invalid"}, "udp")
		if ok {
			t.Fatalf("ok = true, want false for an unresolved FQDN")
		}
		if r.Status != types.StatusError || r.ErrorType != "resolve_error" {
			t.Errorf("Status/ErrorType = %s/%s, want error/resolve_error", r.Status, r.ErrorType)
		}
	})
}

func TestRunLoop(t *testing.T) {
	const interval = time.Millisecond

	t.Run("all attempts received", func(t *testing.T) {
		var result types.ProbeResult
		ok := RunLoop(context.Background(), &result, 3, interval, func(context.Context) Attempt {
			return Attempt{RTT: 2 * time.Millisecond, Received: true}
		})
		if !ok {
			t.Fatalf("ok = false, want true")
		}
		if result.Status != types.StatusSuccess {
			t.Errorf("Status = %s, want success", result.Status)
		}
		if result.PacketsSent != 3 || result.PacketsReceived != 3 {
			t.Errorf("packets sent/recv = %d/%d, want 3/3", result.PacketsSent, result.PacketsReceived)
		}
		if result.PacketLossRatio != 0 {
			t.Errorf("PacketLossRatio = %v, want 0", result.PacketLossRatio)
		}
		if result.RTTMeanMS <= 0 {
			t.Errorf("RTTMeanMS = %v, want > 0", result.RTTMeanMS)
		}
	})

	t.Run("no attempts received is failure with no RTT", func(t *testing.T) {
		var result types.ProbeResult
		ok := RunLoop(context.Background(), &result, 3, interval, func(context.Context) Attempt {
			return Attempt{}
		})
		if !ok {
			t.Fatalf("ok = false, want true (failure is not an error)")
		}
		if result.Status != types.StatusFailure {
			t.Errorf("Status = %s, want failure", result.Status)
		}
		if result.PacketLossRatio != 1 {
			t.Errorf("PacketLossRatio = %v, want 1", result.PacketLossRatio)
		}
		if result.RTTMeanMS != 0 {
			t.Errorf("RTTMeanMS = %v, want 0 (no samples)", result.RTTMeanMS)
		}
	})

	t.Run("some received is partial", func(t *testing.T) {
		var result types.ProbeResult
		n := 0
		ok := RunLoop(context.Background(), &result, 4, interval, func(context.Context) Attempt {
			n++
			return Attempt{RTT: time.Millisecond, Received: n%2 == 0}
		})
		if !ok {
			t.Fatalf("ok = false, want true")
		}
		if result.Status != types.StatusPartial {
			t.Errorf("Status = %s, want partial", result.Status)
		}
		if result.PacketsReceived != 2 {
			t.Errorf("PacketsReceived = %d, want 2", result.PacketsReceived)
		}
	})

	t.Run("fatal error before any reply is a probe error", func(t *testing.T) {
		var result types.ProbeResult
		ok := RunLoop(context.Background(), &result, 3, interval, func(context.Context) Attempt {
			return Attempt{Err: errors.New("socket boom")}
		})
		if ok {
			t.Fatalf("ok = true, want false on fatal error")
		}
		if result.Status != types.StatusError || result.ErrorType != "probe_error" {
			t.Errorf("Status/ErrorType = %s/%s, want error/probe_error", result.Status, result.ErrorType)
		}
		if result.PacketsSent != 1 {
			t.Errorf("PacketsSent = %d, want 1 (loop aborts on the first fatal error)", result.PacketsSent)
		}
	})

	t.Run("fatal error after a reply keeps the partial measurement", func(t *testing.T) {
		var result types.ProbeResult
		n := 0
		ok := RunLoop(context.Background(), &result, 3, interval, func(context.Context) Attempt {
			n++
			if n == 1 {
				return Attempt{RTT: time.Millisecond, Received: true}
			}
			return Attempt{Err: errors.New("late boom")}
		})
		// One reply was received, so the run is a measurement (partial), not an error.
		if !ok {
			t.Fatalf("ok = false, want true once a reply was received")
		}
		if result.Status != types.StatusPartial {
			t.Errorf("Status = %s, want partial", result.Status)
		}
	})

	t.Run("count zero is cancelled", func(t *testing.T) {
		var result types.ProbeResult
		ok := RunLoop(context.Background(), &result, 0, interval, func(context.Context) Attempt {
			t.Fatal("attempt must not run when count is 0")
			return Attempt{}
		})
		if ok {
			t.Fatalf("ok = true, want false")
		}
		if result.Status != types.StatusError || result.ErrorType != "cancelled" {
			t.Errorf("Status/ErrorType = %s/%s, want error/cancelled", result.Status, result.ErrorType)
		}
	})
}

func TestRunWorkItems(t *testing.T) {
	t.Run("nil for no targets", func(t *testing.T) {
		got := RunWorkItems(context.Background(), nil, 0, func(context.Context, WorkItem) types.ProbeResult {
			t.Fatal("probeOne must not run with no targets")
			return types.ProbeResult{}
		})
		if got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})

	t.Run("one result per resolved IP", func(t *testing.T) {
		// Bare IPs bypass DNS, so ExpandTargets yields one WorkItem each.
		targets := []string{"192.0.2.1", "192.0.2.2"}
		results := RunWorkItems(context.Background(), targets, 0, func(_ context.Context, wi WorkItem) types.ProbeResult {
			return types.ProbeResult{Target: wi.IP, Status: types.StatusSuccess}
		})
		if len(results) != 2 {
			t.Fatalf("got %d results, want 2", len(results))
		}
		// Order must match the targets order (results are indexed, not appended).
		if results[0].Target != "192.0.2.1" || results[1].Target != "192.0.2.2" {
			t.Errorf("targets out of order: %q, %q", results[0].Target, results[1].Target)
		}
	})
}
