package icmp

import (
	"context"
	"fmt"
	"time"

	probing "github.com/prometheus-community/pro-bing"

	"github.com/gtataranni/trimon/pkg/types"
)

// Prober implements probe.Prober for ICMP echo (ping).
type Prober struct {
	cfg types.ProbeConfig
}

// New creates a new ICMP Prober from cfg.
func New(cfg types.ProbeConfig) *Prober {
	return &Prober{cfg: cfg}
}

func (p *Prober) Name() string { return p.cfg.Name }
func (p *Prober) Type() string { return "icmp" }

// Run sends cfg.Count ICMP echo requests and returns aggregated statistics.
// RTT measurement, sequence/ID validation, and source-address filtering are
// handled by pro-bing internally, mirroring the approach used by blackbox_exporter.
func (p *Prober) Run(ctx context.Context) (types.ProbeResult, error) {
	result := types.ProbeResult{
		Timestamp: time.Now().UTC(),
		ProbeName: p.cfg.Name,
		Target:    p.cfg.Target,
		SourceIP:  p.cfg.SourceIP,
		ProbeType: "icmp",
		Labels:    p.cfg.Labels,
	}

	pinger, err := probing.NewPinger(p.cfg.Target)
	if err != nil {
		result.Status = types.StatusError
		result.ErrorType = "init_error"
		result.ErrorMsg = fmt.Sprintf("resolve target %q: %v", p.cfg.Target, err)
		return result, nil //nolint:nilerr
	}

	pinger.Count = p.cfg.Count
	pinger.Interval = p.cfg.PacketInterval
	pinger.Timeout = p.cfg.Timeout
	pinger.Source = p.cfg.SourceIP
	pinger.SetPrivileged(true)

	if runErr := pinger.RunWithContext(ctx); runErr != nil && ctx.Err() == nil {
		result.Status = types.StatusError
		result.ErrorType = "run_error"
		result.ErrorMsg = fmt.Sprintf("pinger run: %v", runErr)
		return result, nil //nolint:nilerr
	}

	stats := pinger.Statistics()
	if stats.PacketsSent == 0 {
		result.Status = types.StatusError
		result.ErrorMsg = "no packets sent"
		if ctx.Err() == context.DeadlineExceeded {
			result.ErrorType = "timeout"
		}
		return result, nil
	}

	applyStats(&result, stats)
	return result, nil
}

// applyStats populates result from pinger statistics.
// Caller must ensure stats.PacketsSent > 0.
func applyStats(result *types.ProbeResult, stats *probing.Statistics) {
	result.PacketsSent = stats.PacketsSent
	result.PacketsReceived = stats.PacketsRecv
	// PacketLoss from pro-bing is a percentage (0–100); convert to ratio (0.0–1.0).
	result.PacketLossRatio = stats.PacketLoss / 100

	switch {
	case result.PacketLossRatio == 0:
		result.Status = types.StatusSuccess
	case result.PacketLossRatio >= 1:
		result.Status = types.StatusFailure
	default:
		result.Status = types.StatusPartial
	}

	if stats.PacketsRecv > 0 {
		result.RTTMinMS = stats.MinRtt.Seconds() * 1000
		result.RTTMeanMS = stats.AvgRtt.Seconds() * 1000
		result.RTTMaxMS = stats.MaxRtt.Seconds() * 1000
		result.RTTStddevMS = stats.StdDevRtt.Seconds() * 1000
	}
}
