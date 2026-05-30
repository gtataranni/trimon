package udp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/gtataranni/trimon/internal/probe"
	"github.com/gtataranni/trimon/pkg/types"
)

// portState classifies a single UDP attempt's outcome. UDP has no handshake, so
// only two positive signals exist: a (matching) reply means the port is open,
// and an ICMP port-unreachable (delivered as ECONNREFUSED on a connected socket)
// means the host is reachable but the port is closed. Everything else — a
// timeout, a dropped packet, or a non-matching reply — is indistinguishable
// silence (Nmap's "open|filtered" ambiguity) and counts as unreachable.
type portState int

const (
	stateUnreachable portState = iota // no usable reply (timeout, drop, non-match)
	stateClosed                       // ICMP port-unreachable: host reachable, port shut
	stateOpen                         // matching UDP reply: port open
)

// reachable reports whether the host answered at all (open or closed). Both
// count as network-reachable; only stateUnreachable is treated as packet loss.
func (s portState) reachable() bool { return s != stateUnreachable }

// Prober implements probe.Prober for UDP probes. Each tick it sends the
// configured payload to a target port over a connected UDP socket and waits for
// a reply within Timeout. A reply (optionally matching ExpectedResponse) counts
// as received; a deadline expiry or non-matching reply is loss. Count attempts
// are made per tick, PacketInterval apart; packet loss is the fraction that got
// no usable reply.
//
// UDP is connectionless, so net.Dial always succeeds and carries no liveness
// signal — the real signal is whether Read returns before the deadline. A
// connected socket is used so the kernel surfaces ICMP errors (port/host
// unreachable) on the socket as ECONNREFUSED.
//
// That ECONNREFUSED yields a tri-state port classification, mirroring the TCP
// probe's PortOpen: an ICMP port-unreachable is a reachable-but-closed port
// (counts as a reply, not loss, with PortOpen=false), distinct from silence
// (loss, PortOpen=false, up=0). A matching reply is open (PortOpen=true). The
// open|filtered ambiguity is inherent to UDP — silence cannot tell a quietly
// open service from a dropped packet.
type Prober struct {
	cfg types.ProbeConfig
}

// New creates a new UDP Prober from cfg.
func New(cfg types.ProbeConfig) *Prober {
	return &Prober{cfg: cfg}
}

func (p *Prober) Name() string { return p.cfg.Name }
func (p *Prober) Type() string { return "udp" }

// Run expands the probe's targets into individual IPs (resolving FQDNs at call
// time), then probes all IPs in parallel within ctx. One ProbeResult is
// returned per probed IP.
func (p *Prober) Run(ctx context.Context) []types.ProbeResult {
	items := probe.ExpandTargets(ctx, p.cfg.Targets, p.cfg.MaxResolvedIPs)
	if len(items) == 0 {
		return nil
	}

	results := make([]types.ProbeResult, len(items))
	var wg sync.WaitGroup
	for i, item := range items {
		wg.Add(1)
		go func(idx int, wi probe.WorkItem) {
			defer wg.Done()
			results[idx] = p.probeOne(ctx, wi)
		}(i, item)
	}
	wg.Wait()
	return results
}

// probeOne runs Count UDP attempts against a single IP and returns a populated result.
func (p *Prober) probeOne(ctx context.Context, wi probe.WorkItem) types.ProbeResult {
	cfg := p.cfg
	result := types.ProbeResult{
		Timestamp: time.Now().UTC(),
		ProbeName: cfg.Name,
		Target:    wi.IP,
		FQDN:      wi.FQDN,
		SourceIP:  cfg.SourceIP,
		ProbeType: "udp",
		Labels:    cfg.Labels,
	}

	// If ExpandTargets could not resolve the FQDN (IP == FQDN), emit an error result.
	if wi.FQDN != "" && wi.IP == wi.FQDN {
		result.Status = types.StatusError
		result.ErrorType = "resolve_error"
		result.ErrorMsg = fmt.Sprintf("resolve target %q: lookup failed", wi.FQDN)
		return result
	}

	// Payload and ExpectedResponse are sent and matched as raw bytes. An empty
	// payload sends a zero-length datagram.
	payload := []byte(cfg.UDP.Payload)
	expected := []byte(cfg.UDP.ExpectedResponse)
	addr := net.JoinHostPort(wi.IP, strconv.Itoa(cfg.UDP.Port))

	var (
		samples []time.Duration
		lastErr error
		openGot bool
	)
	for i := 0; i < cfg.Count; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(cfg.PacketInterval):
			}
			if ctx.Err() != nil {
				break
			}
		}

		rtt, state, err := udpAttempt(ctx, cfg.SourceIP, addr, payload, expected, cfg.Timeout)
		result.PacketsSent++
		if err != nil {
			// A socket setup failure (e.g. bad source binding) aborts the run.
			lastErr = err
			break
		}
		if !state.reachable() {
			continue
		}
		// stateClosed (ICMP port-unreachable) counts as reachable, just like a
		// TCP RST: the host answered, so it is not packet loss.
		result.PacketsReceived++
		samples = append(samples, rtt)
		if state == stateOpen {
			openGot = true
		}
	}

	// A setup/socket error before any reply means the probe could not run.
	if lastErr != nil && result.PacketsReceived == 0 {
		result.Status = types.StatusError
		result.ErrorType = "probe_error"
		result.ErrorMsg = lastErr.Error()
		return result
	}

	if result.PacketsSent == 0 {
		// ctx was already done before the first attempt could be made.
		result.Status = types.StatusError
		result.ErrorType = "cancelled"
		result.ErrorMsg = "no attempts made before context cancellation"
		return result
	}

	result.PacketLossRatio = 1 - float64(result.PacketsReceived)/float64(result.PacketsSent)

	switch {
	case result.PacketLossRatio == 0:
		result.Status = types.StatusSuccess
	case result.PacketLossRatio >= 1:
		result.Status = types.StatusFailure
	default:
		result.Status = types.StatusPartial
	}

	if result.PacketsReceived > 0 {
		result.RTTMinMS, result.RTTMeanMS, result.RTTMaxMS, result.RTTStddevMS = probe.RTTStats(samples)
	}

	// PortOpen is a tri-state: set it (true/false) for any non-error UDP result.
	// true = a (matching) reply arrived; false = closed (ICMP unreachable) or only
	// silence. Combine with probe.up to tell closed (up=1) from open|filtered (up=0).
	result.PortOpen = &openGot

	return result
}

// udpAttempt sends payload to addr over a connected UDP socket and waits for a
// reply within timeout. It returns the round-trip time and the observed port
// state:
//
//   - stateOpen        a usable reply arrived (any reply when expected is empty,
//     otherwise one prefixed by expected)
//   - stateClosed      the host returned an ICMP port-unreachable, delivered as
//     ECONNREFUSED on the connected socket — reachable, port shut
//   - stateUnreachable a timeout, a dropped packet, or a non-matching reply
//
// The error is non-nil only when the socket could not be set up (Dial/deadline
// failure) — never for a timeout, an ICMP error, or a non-matching reply.
func udpAttempt(ctx context.Context, sourceIP, addr string, payload, expected []byte, timeout time.Duration) (time.Duration, portState, error) {
	d := net.Dialer{Timeout: timeout}
	if sourceIP != "" {
		d.LocalAddr = &net.UDPAddr{IP: net.ParseIP(sourceIP)}
	}

	conn, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return 0, stateUnreachable, err
	}
	defer func() { _ = conn.Close() }()

	// Set the deadline before any I/O. Do not call conn.File(): it disables
	// deadline support on the underlying socket.
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return 0, stateUnreachable, err
	}

	start := time.Now()
	// An ICMP port-unreachable from a prior datagram can surface as ECONNREFUSED
	// on the Write; that means the port is closed (host reachable). Any other
	// write error (or a short write) is indistinguishable silence.
	n, werr := conn.Write(payload)
	if werr != nil {
		if errors.Is(werr, syscall.ECONNREFUSED) {
			return time.Since(start), stateClosed, nil
		}
		return 0, stateUnreachable, nil
	}
	if n < len(payload) {
		return 0, stateUnreachable, nil
	}

	buf := make([]byte, 64*1024)
	n, rerr := conn.Read(buf)
	rtt := time.Since(start)
	if rerr != nil {
		// ICMP port-unreachable surfaces here as ECONNREFUSED: reachable, closed.
		if errors.Is(rerr, syscall.ECONNREFUSED) {
			return rtt, stateClosed, nil
		}
		// Deadline expiry or any other read error: indistinguishable silence.
		return 0, stateUnreachable, nil
	}
	if len(expected) == 0 || bytes.HasPrefix(buf[:n], expected) {
		return rtt, stateOpen, nil
	}
	// Got a reply, but it did not match the expected prefix.
	return 0, stateUnreachable, nil
}
