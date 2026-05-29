package tcp

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"net"
	"time"
)

// TCP control-bit flags.
const (
	flagFIN = 1 << 0
	flagSYN = 1 << 1
	flagRST = 1 << 2
	flagACK = 1 << 4
)

const tcpHeaderLen = 20 // no options

// synSegment is a parsed view of the fields we care about in a TCP segment.
type synSegment struct {
	srcPort uint16
	dstPort uint16
	seq     uint32
	ack     uint32
	flags   uint8
}

// buildSYN marshals a 20-byte TCP SYN segment with a checksum computed over the
// IPv4 pseudo-header. srcIP/dstIP must be 4-byte IPv4 addresses.
func buildSYN(srcIP, dstIP net.IP, srcPort, dstPort uint16, seq uint32) ([]byte, error) {
	s4, d4 := srcIP.To4(), dstIP.To4()
	if s4 == nil || d4 == nil {
		return nil, fmt.Errorf("syn: IPv4 source and destination required (src=%v dst=%v)", srcIP, dstIP)
	}

	hdr := make([]byte, tcpHeaderLen)
	binary.BigEndian.PutUint16(hdr[0:2], srcPort)
	binary.BigEndian.PutUint16(hdr[2:4], dstPort)
	binary.BigEndian.PutUint32(hdr[4:8], seq)
	// hdr[8:12] ack = 0
	hdr[12] = (tcpHeaderLen / 4) << 4 // data offset (words); reserved bits 0
	hdr[13] = flagSYN
	binary.BigEndian.PutUint16(hdr[14:16], 0xFFFF) // window
	// hdr[16:18] checksum filled below
	// hdr[18:20] urgent pointer = 0

	binary.BigEndian.PutUint16(hdr[16:18], tcpChecksum(s4, d4, hdr))
	return hdr, nil
}

// tcpChecksum computes the 16-bit ones-complement checksum over the IPv4
// pseudo-header followed by the TCP segment. The segment's checksum field must
// be zero on entry.
func tcpChecksum(srcIP, dstIP net.IP, seg []byte) uint16 {
	pseudo := make([]byte, 12)
	copy(pseudo[0:4], srcIP.To4())
	copy(pseudo[4:8], dstIP.To4())
	pseudo[8] = 0
	pseudo[9] = 6 // protocol: TCP
	binary.BigEndian.PutUint16(pseudo[10:12], uint16(len(seg)))

	var sum uint32
	addBytes := func(b []byte) {
		for i := 0; i+1 < len(b); i += 2 {
			sum += uint32(binary.BigEndian.Uint16(b[i : i+2]))
		}
		if len(b)%2 == 1 {
			sum += uint32(b[len(b)-1]) << 8
		}
	}
	addBytes(pseudo)
	addBytes(seg)

	for sum>>16 != 0 {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	return ^uint16(sum)
}

// parseTCPSegment extracts the header fields from a TCP segment. The input must
// start at the TCP header (IP header already stripped). It returns an error if
// the buffer is too short to contain a TCP header.
func parseTCPSegment(b []byte) (synSegment, error) {
	if len(b) < tcpHeaderLen {
		return synSegment{}, fmt.Errorf("syn: short TCP segment (%d bytes)", len(b))
	}
	return synSegment{
		srcPort: binary.BigEndian.Uint16(b[0:2]),
		dstPort: binary.BigEndian.Uint16(b[2:4]),
		seq:     binary.BigEndian.Uint32(b[4:8]),
		ack:     binary.BigEndian.Uint32(b[8:12]),
		flags:   b[13],
	}, nil
}

// stripIPv4Header returns the TCP payload of a received buffer. Raw IPv4 sockets
// may deliver packets with the IP header included; this detects that (version
// nibble 4) and skips IHL words, otherwise returns the buffer unchanged.
func stripIPv4Header(b []byte) []byte {
	if len(b) >= 20 && b[0]>>4 == 4 {
		ihl := int(b[0]&0x0F) * 4
		if ihl >= 20 && ihl <= len(b) {
			return b[ihl:]
		}
	}
	return b
}

// classifyReply maps a reply segment to a port state, given the probe's source
// port, target port, and expected acknowledgement number (our SYN seq + 1). It
// returns (state, true) when the segment belongs to this probe, or (_, false)
// when it is unrelated traffic and should be ignored.
func classifyReply(seg synSegment, srcPort, dstPort uint16, expectAck uint32) (portState, bool) {
	// Reply must come from the target port and be addressed to our source port.
	if seg.srcPort != dstPort || seg.dstPort != srcPort {
		return stateUnreachable, false
	}
	switch {
	case seg.flags&flagSYN != 0 && seg.flags&flagACK != 0 && seg.ack == expectAck:
		return stateOpen, true
	case seg.flags&flagRST != 0:
		return stateClosed, true
	default:
		return stateUnreachable, false
	}
}

// synAttempt sends a single raw half-open SYN to ip:port and classifies the
// reply without completing the handshake. A SYN/ACK is an open port, a RST is a
// closed-but-reachable port, and no reply within timeout is unreachable.
//
// It requires a raw socket (CAP_NET_RAW); a setup failure (e.g. permission
// denied) returns a non-nil error so the caller reports status=error.
func synAttempt(ctx context.Context, sourceIP, ip string, port int, timeout time.Duration) (time.Duration, portState, error) {
	dstIP := net.ParseIP(ip).To4()
	if dstIP == nil {
		return 0, stateUnreachable, fmt.Errorf("syn: target %q is not IPv4 (SYN mode is IPv4-only)", ip)
	}

	srcIP, err := resolveSourceIP(sourceIP, dstIP)
	if err != nil {
		return 0, stateUnreachable, err
	}

	conn, err := net.ListenPacket("ip4:tcp", srcIP.String())
	if err != nil {
		return 0, stateUnreachable, fmt.Errorf("syn: open raw socket (needs CAP_NET_RAW): %w", err)
	}
	defer func() { _ = conn.Close() }()

	srcPort := uint16(1024 + rand.IntN(65535-1024))
	seq := rand.Uint32()
	syn, err := buildSYN(srcIP, dstIP, srcPort, uint16(port), seq)
	if err != nil {
		return 0, stateUnreachable, err
	}

	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return 0, stateUnreachable, fmt.Errorf("syn: set deadline: %w", err)
	}

	start := time.Now()
	if _, err := conn.WriteTo(syn, &net.IPAddr{IP: dstIP}); err != nil {
		return 0, stateUnreachable, fmt.Errorf("syn: send: %w", err)
	}

	buf := make([]byte, 1500)
	for {
		n, raddr, err := conn.ReadFrom(buf)
		if err != nil {
			// Deadline reached with no matching reply: unreachable, not an error.
			return 0, stateUnreachable, nil
		}
		if raddr.String() != dstIP.String() {
			continue
		}
		seg, err := parseTCPSegment(stripIPv4Header(buf[:n]))
		if err != nil {
			continue
		}
		if state, ok := classifyReply(seg, srcPort, uint16(port), seq+1); ok {
			return time.Since(start), state, nil
		}
	}
}

// resolveSourceIP returns the IPv4 source address to send from. When sourceIP is
// configured it is used; otherwise the OS routing table picks the address for
// the route to dstIP (no packets are sent).
func resolveSourceIP(sourceIP string, dstIP net.IP) (net.IP, error) {
	if sourceIP != "" {
		ip := net.ParseIP(sourceIP).To4()
		if ip == nil {
			return nil, fmt.Errorf("syn: source_ip %q is not IPv4", sourceIP)
		}
		return ip, nil
	}
	c, err := net.Dial("udp", net.JoinHostPort(dstIP.String(), "80"))
	if err != nil {
		return nil, fmt.Errorf("syn: determine source IP for %v: %w", dstIP, err)
	}
	defer func() { _ = c.Close() }()
	ip := c.LocalAddr().(*net.UDPAddr).IP.To4()
	if ip == nil {
		return nil, fmt.Errorf("syn: no IPv4 source route to %v", dstIP)
	}
	return ip, nil
}
