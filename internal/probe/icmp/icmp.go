package icmp

import (
	"context"
	"fmt"
	"math"
	"net"
	"time"

	gicmp "golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"

	"github.com/gtataranni/trimon/pkg/types"
)

const interPacketInterval = 200 * time.Millisecond

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
func (p *Prober) Run(ctx context.Context) (types.ProbeResult, error) {
	result := types.ProbeResult{
		Timestamp: time.Now().UTC(),
		ProbeName: p.cfg.Name,
		Target:    p.cfg.Target,
		SourceIP:  p.cfg.SourceIP,
		ProbeType: "icmp",
		Labels:    p.cfg.Labels,
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(p.cfg.Timeout)
	}

	conn, err := gicmp.ListenPacket("ip4:icmp", p.cfg.SourceIP)
	if err != nil {
		result.Status = types.StatusError
		result.ErrorMsg = fmt.Sprintf("open raw socket (CAP_NET_RAW required): %v", err)
		return result, nil //nolint:nilerr // error is reported in result
	}
	defer conn.Close()

	if err := conn.SetDeadline(deadline); err != nil {
		result.Status = types.StatusError
		result.ErrorMsg = fmt.Sprintf("set deadline: %v", err)
		return result, nil
	}

	dstIP := net.ParseIP(p.cfg.Target)
	if dstIP == nil {
		addrs, lerr := net.LookupHost(p.cfg.Target)
		if lerr != nil || len(addrs) == 0 {
			result.Status = types.StatusError
			result.ErrorMsg = fmt.Sprintf("resolve target %q: %v", p.cfg.Target, lerr)
			return result, nil
		}
		dstIP = net.ParseIP(addrs[0])
	}
	dst := &net.IPAddr{IP: dstIP}

	rtts := make([]float64, 0, p.cfg.Count)
	sent := 0
	recv := 0

	for seq := 0; seq < p.cfg.Count; seq++ {
		if ctx.Err() != nil {
			break
		}

		msg := gicmp.Message{
			Type: ipv4.ICMPTypeEcho,
			Code: 0,
			Body: &gicmp.Echo{
				ID:   1,
				Seq:  seq + 1,
				Data: []byte("trimon"),
			},
		}
		wb, err := msg.Marshal(nil)
		if err != nil {
			result.Status = types.StatusError
			result.ErrorMsg = fmt.Sprintf("marshal ICMP: %v", err)
			return result, nil
		}

		sendAt := time.Now()
		if _, err := conn.WriteTo(wb, dst); err != nil {
			// single packet write failure; record and continue
			sent++
			continue
		}
		sent++

		rb := make([]byte, 1500)
		n, _, err := conn.ReadFrom(rb)
		if err != nil {
			// timeout or error on this packet — count as lost
			if seq < p.cfg.Count-1 {
				time.Sleep(interPacketInterval)
			}
			continue
		}
		rtt := time.Since(sendAt).Seconds() * 1000 // ms

		rm, err := gicmp.ParseMessage(1, rb[:n])
		if err != nil {
			continue
		}
		if rm.Type != ipv4.ICMPTypeEchoReply {
			continue
		}
		recv++
		rtts = append(rtts, rtt)

		if seq < p.cfg.Count-1 {
			time.Sleep(interPacketInterval)
		}
	}

	result.PacketsSent = sent
	result.PacketsReceived = recv

	if sent == 0 {
		result.Status = types.StatusError
		result.ErrorMsg = "no packets sent"
		return result, nil
	}

	loss := float64(sent-recv) / float64(sent)
	result.PacketLossRatio = loss

	switch {
	case loss == 0:
		result.Status = types.StatusSuccess
	case loss >= 1:
		result.Status = types.StatusFailure
	default:
		result.Status = types.StatusPartial
	}

	if len(rtts) > 0 {
		result.RTTMinMS, result.RTTMeanMS, result.RTTMaxMS, result.RTTStddevMS = rttStats(rtts)
	}

	return result, nil
}

func rttStats(rtts []float64) (min, mean, max, stddev float64) {
	min = rtts[0]
	max = rtts[0]
	sum := 0.0
	for _, v := range rtts {
		sum += v
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	mean = sum / float64(len(rtts))

	variance := 0.0
	for _, v := range rtts {
		d := v - mean
		variance += d * d
	}
	variance /= float64(len(rtts))
	stddev = math.Sqrt(variance)
	return
}
