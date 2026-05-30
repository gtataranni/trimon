package tcp

import (
	"context"
	"errors"
	"net"
	"strconv"
	"syscall"
	"time"

	"github.com/gtataranni/trimon/internal/probe"
	"github.com/gtataranni/trimon/pkg/types"
)

// portState classifies a single TCP attempt's outcome.
type portState int

const (
	stateUnreachable portState = iota // no reply (timeout, dropped, host down)
	stateClosed                       // RST received: host reachable, port shut
	stateOpen                         // SYN/ACK (or completed handshake): port open
)

// reachable reports whether the target answered at all (open or closed). Both
// count as network-reachable; only stateUnreachable is treated as packet loss.
func (s portState) reachable() bool { return s != stateUnreachable }

// Prober implements probe.Prober for TCP probes. It supports two modes:
//
//   - "connect": completes a full handshake via the kernel and closes it.
//   - "syn":     sends a raw half-open SYN and classifies the reply.
//
// In both modes a SYN/ACK (or completed handshake) is an open port, a RST is a
// closed-but-reachable port, and silence is unreachable. Count attempts are made
// per tick, PacketInterval apart; packet loss is the fraction that got no reply.
type Prober struct {
	cfg types.ProbeConfig
}

// New creates a new TCP Prober from cfg.
func New(cfg types.ProbeConfig) *Prober {
	return &Prober{cfg: cfg}
}

func (p *Prober) Name() string { return p.cfg.Name }
func (p *Prober) Type() string { return "tcp" }

// Run expands the probe's targets into individual IPs (resolving FQDNs at call
// time), then probes all IPs in parallel within ctx. One ProbeResult is
// returned per probed IP.
func (p *Prober) Run(ctx context.Context) []types.ProbeResult {
	return probe.RunWorkItems(ctx, p.cfg.Targets, p.cfg.MaxResolvedIPs, p.probeOne)
}

// probeOne runs Count TCP attempts against a single IP and returns a populated result.
func (p *Prober) probeOne(ctx context.Context, wi probe.WorkItem) types.ProbeResult {
	result, ok := probe.NewResult(p.cfg, wi, p.Type())
	if !ok {
		return result
	}

	attempt := p.attemptFn()
	var synAckGot bool

	live := probe.RunLoop(ctx, &result, p.cfg.Count, p.cfg.PacketInterval, func(ctx context.Context) probe.Attempt {
		rtt, state, err := attempt(ctx, wi.IP)
		if err != nil {
			// A real probe error (e.g. socket/permission failure) aborts the run.
			return probe.Attempt{Err: err}
		}
		if !state.reachable() {
			return probe.Attempt{}
		}
		if state == stateOpen {
			synAckGot = true
		}
		return probe.Attempt{RTT: rtt, Received: true}
	})

	// PortOpen is a tri-state: set it (true/false) for any non-error TCP result.
	if live {
		result.PortOpen = &synAckGot
	}

	return result
}

// attemptFunc runs a single TCP attempt against an IP, returning the round-trip
// time to the reply, the observed port state, and a non-nil error only when the
// probe itself could not run (socket/permission failure) — never for an
// unreachable or closed target.
type attemptFunc func(ctx context.Context, ip string) (time.Duration, portState, error)

// attemptFn selects the per-attempt function for the configured mode.
func (p *Prober) attemptFn() attemptFunc {
	port := p.cfg.TCP.Port
	src := p.cfg.SourceIP
	timeout := p.cfg.Timeout
	if p.cfg.TCP.Mode == types.TCPModeSYN {
		return func(ctx context.Context, ip string) (time.Duration, portState, error) {
			return synAttempt(ctx, src, ip, port, timeout)
		}
	}
	return func(ctx context.Context, ip string) (time.Duration, portState, error) {
		return connectAttempt(ctx, src, ip, port, timeout)
	}
}

// connectAttempt opens a full TCP connection (kernel handshake) and closes it.
// A completed handshake is an open port; ECONNREFUSED (RST) is a reachable,
// closed port; a timeout or unreachable error is loss. It returns an error only
// for failures that prevent the probe from running at all.
func connectAttempt(ctx context.Context, sourceIP, ip string, port int, timeout time.Duration) (time.Duration, portState, error) {
	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	d := net.Dialer{Timeout: timeout}
	if sourceIP != "" {
		d.LocalAddr = &net.TCPAddr{IP: net.ParseIP(sourceIP)}
	}

	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", addr)
	rtt := time.Since(start)
	if err == nil {
		_ = conn.Close()
		return rtt, stateOpen, nil
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		// Host answered with a RST: reachable, port closed.
		return rtt, stateClosed, nil
	}
	// Timeout / no route / host down: no reply.
	return 0, stateUnreachable, nil
}
