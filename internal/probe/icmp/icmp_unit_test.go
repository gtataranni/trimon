package icmp

import (
	"context"
	"testing"
	"time"

	probing "github.com/prometheus-community/pro-bing"
	"github.com/gtataranni/trimon/pkg/types"
)

func TestStatusDetermination(t *testing.T) {
	tests := []struct {
		name          string
		packetsSent   int
		packetsRecv   int
		packetLoss    float64 // pro-bing percentage 0–100
		wantStatus    types.Status
		wantLossRatio float64
	}{
		{"zero loss", 3, 3, 0, types.StatusSuccess, 0},
		{"partial loss", 4, 2, 50, types.StatusPartial, 0.5},
		{"total loss", 3, 0, 100, types.StatusFailure, 1.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := types.ProbeResult{}
			applyStats(&result, &probing.Statistics{
				PacketsSent: tt.packetsSent,
				PacketsRecv: tt.packetsRecv,
				PacketLoss:  tt.packetLoss,
			})
			if result.Status != tt.wantStatus {
				t.Errorf("Status: got %s, want %s", result.Status, tt.wantStatus)
			}
			if result.PacketLossRatio != tt.wantLossRatio {
				t.Errorf("PacketLossRatio: got %f, want %f", result.PacketLossRatio, tt.wantLossRatio)
			}
		})
	}
}

func TestRTTZeroWhenNoPacketsReceived(t *testing.T) {
	result := types.ProbeResult{}
	applyStats(&result, &probing.Statistics{
		PacketsSent: 3,
		PacketsRecv: 0,
		PacketLoss:  100,
		MinRtt:      5 * time.Millisecond,
		AvgRtt:      5 * time.Millisecond,
		MaxRtt:      5 * time.Millisecond,
	})
	if result.RTTMinMS != 0 || result.RTTMeanMS != 0 || result.RTTMaxMS != 0 || result.RTTStddevMS != 0 {
		t.Errorf("RTT fields must be zero when no packets received: min=%f mean=%f max=%f stddev=%f",
			result.RTTMinMS, result.RTTMeanMS, result.RTTMaxMS, result.RTTStddevMS)
	}
}

func TestFieldsOnError(t *testing.T) {
	// Empty addr causes NewPinger to return "addr cannot be empty" without any
	// network or socket access — exercises our DNS-failure error path.
	p := New(types.ProbeConfig{Name: "test", Type: "icmp", Target: ""})
	result, err := p.Run(context.Background())
	if err != nil {
		t.Fatalf("Run must never return a non-nil error: %v", err)
	}
	if result.Status != types.StatusError {
		t.Errorf("Status: got %s, want StatusError", result.Status)
	}
	if result.ErrorMsg == "" {
		t.Error("ErrorMsg must be non-empty on error")
	}
	if result.ErrorType != "init_error" {
		t.Errorf("ErrorType: got %q, want %q", result.ErrorType, "init_error")
	}
	if result.RTTMinMS != 0 || result.RTTMeanMS != 0 || result.RTTMaxMS != 0 || result.RTTStddevMS != 0 {
		t.Errorf("RTT fields must be zero on error: min=%f mean=%f max=%f stddev=%f",
			result.RTTMinMS, result.RTTMeanMS, result.RTTMaxMS, result.RTTStddevMS)
	}
}
