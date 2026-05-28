package icmp

import (
	"context"
	"fmt"
	"net"
	"sync"
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

// workItem is a single resolved IP to probe, plus the FQDN it came from (empty
// for literal-IP targets).
type workItem struct {
	ip   string
	fqdn string
}

// Run expands the probe's targets list into individual IPs (resolving FQDNs at
// call time), then pings all IPs in parallel within ctx. One ProbeResult is
// returned per probed IP. Errors are embedded in each result.
func (p *Prober) Run(ctx context.Context) []types.ProbeResult {
	items := p.expandTargets(ctx)
	if len(items) == 0 {
		return nil
	}

	results := make([]types.ProbeResult, len(items))
	var wg sync.WaitGroup
	for i, item := range items {
		wg.Add(1)
		go func(idx int, wi workItem) {
			defer wg.Done()
			results[idx] = p.probeOne(ctx, wi)
		}(i, item)
	}
	wg.Wait()
	return results
}

// expandTargets resolves each entry in p.cfg.Targets to one or more workItems.
// FQDN entries are resolved using ctx's deadline; literal IPs are used as-is.
// DNS failures produce a StatusError workItem rather than being silently dropped.
func (p *Prober) expandTargets(ctx context.Context) []workItem {
	var items []workItem
	for _, entry := range p.cfg.Targets {
		if net.ParseIP(entry) != nil {
			items = append(items, workItem{ip: entry})
			continue
		}
		addrs, err := net.DefaultResolver.LookupHost(ctx, entry)
		if err != nil || len(addrs) == 0 {
			// Emit a StatusError result for this FQDN rather than silently skipping.
			items = append(items, workItem{ip: entry, fqdn: entry})
			continue
		}
		seen := make(map[string]bool, len(addrs))
		cap := p.cfg.MaxResolvedIPs
		for _, addr := range addrs {
			if seen[addr] {
				continue
			}
			seen[addr] = true
			items = append(items, workItem{ip: addr, fqdn: entry})
			if cap > 0 && len(seen) >= cap {
				break
			}
		}
	}
	return items
}

// probeOne pings a single IP and returns a populated ProbeResult.
func (p *Prober) probeOne(ctx context.Context, wi workItem) types.ProbeResult {
	result := types.ProbeResult{
		Timestamp: time.Now().UTC(),
		ProbeName: p.cfg.Name,
		Target:    wi.ip,
		FQDN:      wi.fqdn,
		SourceIP:  p.cfg.SourceIP,
		ProbeType: "icmp",
		Labels:    p.cfg.Labels,
	}

	// If expandTargets could not resolve the FQDN (ip == fqdn), emit an error result.
	if wi.fqdn != "" && wi.ip == wi.fqdn {
		result.Status = types.StatusError
		result.ErrorType = "resolve_error"
		result.ErrorMsg = fmt.Sprintf("resolve target %q: lookup failed", wi.fqdn)
		return result
	}

	pinger, err := probing.NewPinger(wi.ip)
	if err != nil {
		result.Status = types.StatusError
		result.ErrorType = "init_error"
		result.ErrorMsg = fmt.Sprintf("init pinger for %q: %v", wi.ip, err)
		return result
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
		return result
	}

	stats := pinger.Statistics()
	if stats.PacketsSent == 0 {
		result.Status = types.StatusError
		result.ErrorMsg = "no packets sent"
		if ctx.Err() == context.DeadlineExceeded {
			result.ErrorType = "timeout"
		}
		return result
	}

	applyStats(&result, stats)
	return result
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

	// RTT fields are only populated when at least one reply was received; they remain zero otherwise.
	if stats.PacketsRecv > 0 {
		result.RTTMinMS = stats.MinRtt.Seconds() * 1000
		result.RTTMeanMS = stats.AvgRtt.Seconds() * 1000
		result.RTTMaxMS = stats.MaxRtt.Seconds() * 1000
		result.RTTStddevMS = stats.StdDevRtt.Seconds() * 1000
	}
}
