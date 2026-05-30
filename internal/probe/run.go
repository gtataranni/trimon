package probe

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gtataranni/trimon/pkg/types"
)

// RunWorkItems expands targets into individual IPs (resolving FQDNs at call
// time) and runs probeOne against each in parallel within ctx, returning one
// ProbeResult per resolved IP. It returns nil when there are no targets.
//
// This is the shared fan-out used by every host-oriented prober (ICMP, TCP,
// UDP, HTTP). The DNS prober queries names rather than connecting to hosts and
// does not use it.
func RunWorkItems(ctx context.Context, targets []string, maxResolvedIPs int, probeOne func(context.Context, WorkItem) types.ProbeResult) []types.ProbeResult {
	items := ExpandTargets(ctx, targets, maxResolvedIPs)
	if len(items) == 0 {
		return nil
	}

	results := make([]types.ProbeResult, len(items))
	var wg sync.WaitGroup
	for i, item := range items {
		wg.Add(1)
		go func(idx int, wi WorkItem) {
			defer wg.Done()
			results[idx] = probeOne(ctx, wi)
		}(i, item)
	}
	wg.Wait()
	return results
}

// NewResult builds the base ProbeResult for a work item, pre-populating the
// identifying fields (timestamp, name, target, FQDN, source, type, labels).
//
// ok is false when ExpandTargets could not resolve the item's FQDN (signalled
// by IP == FQDN): the returned result is already marked StatusError /
// resolve_error and the caller must return it immediately without probing.
func NewResult(cfg types.ProbeConfig, wi WorkItem, probeType string) (result types.ProbeResult, ok bool) {
	result = types.ProbeResult{
		Timestamp: time.Now().UTC(),
		ProbeName: cfg.Name,
		Target:    wi.IP,
		FQDN:      wi.FQDN,
		SourceIP:  cfg.SourceIP,
		ProbeType: probeType,
		Labels:    cfg.Labels,
	}
	if wi.FQDN != "" && wi.IP == wi.FQDN {
		result.Status = types.StatusError
		result.ErrorType = "resolve_error"
		result.ErrorMsg = fmt.Sprintf("resolve target %q: lookup failed", wi.FQDN)
		return result, false
	}
	return result, true
}

// StatusFromLoss maps a packet-loss ratio to a probe status: 0 loss is success,
// total loss is failure, anything in between is partial. StatusError is never
// returned here — it denotes a probe that could not run at all and is set by the
// caller. See docs/metrics.md ("probe.packet_loss semantics") for the boundary
// and how each status maps to the exported metrics.
func StatusFromLoss(lossRatio float64) types.Status {
	switch {
	case lossRatio == 0:
		return types.StatusSuccess
	case lossRatio >= 1:
		return types.StatusFailure
	default:
		return types.StatusPartial
	}
}

// Attempt is the outcome of a single probe attempt within RunLoop.
type Attempt struct {
	RTT      time.Duration // round-trip time; recorded only when Received is true
	Received bool          // the attempt produced a usable reply
	Err      error         // fatal: aborts the loop; marks the probe StatusError if nothing was received
}

// RunLoop executes count probe attempts against a single target, interval apart,
// invoking attempt for each. It accumulates PacketsSent, PacketsReceived, and
// RTT samples into result, then derives PacketLossRatio, the RTT summary stats,
// and Status:
//
//   - 0 loss   -> StatusSuccess
//   - 1 loss   -> StatusFailure
//   - else     -> StatusPartial
//
// Two StatusError cases short-circuit before that mapping (returning ok=false):
//
//   - an attempt returned a fatal Err and nothing was ever received -> "probe_error"
//   - ctx was cancelled before the first attempt (PacketsSent == 0)  -> "cancelled"
//
// ok reports whether the run produced a non-error status, so the caller may set
// status-dependent fields (e.g. PortOpen) only on a real measurement. RunLoop
// touches only the packet counters, RTT fields, PacketLossRatio, and the
// Status/ErrorType/ErrorMsg trio; the caller fills the rest.
func RunLoop(ctx context.Context, result *types.ProbeResult, count int, interval time.Duration, attempt func(context.Context) Attempt) (ok bool) {
	var (
		samples []time.Duration
		lastErr error
	)
	for i := 0; i < count; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(interval):
			}
			if ctx.Err() != nil {
				break
			}
		}

		a := attempt(ctx)
		result.PacketsSent++
		if a.Err != nil {
			lastErr = a.Err
			break
		}
		if !a.Received {
			continue
		}
		result.PacketsReceived++
		samples = append(samples, a.RTT)
	}

	// A setup/socket error before any reply means the probe could not run.
	if lastErr != nil && result.PacketsReceived == 0 {
		result.Status = types.StatusError
		result.ErrorType = "probe_error"
		result.ErrorMsg = lastErr.Error()
		return false
	}
	if result.PacketsSent == 0 {
		// ctx was already done before the first attempt could be made.
		result.Status = types.StatusError
		result.ErrorType = "cancelled"
		result.ErrorMsg = "no attempts made before context cancellation"
		return false
	}

	result.PacketLossRatio = 1 - float64(result.PacketsReceived)/float64(result.PacketsSent)
	result.Status = StatusFromLoss(result.PacketLossRatio)
	if result.PacketsReceived > 0 {
		result.RTTMinMS, result.RTTMeanMS, result.RTTMaxMS, result.RTTStddevMS = RTTStats(samples)
	}
	return true
}
