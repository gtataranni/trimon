package tcp

import (
	"encoding/binary"
	"net"
	"testing"
)

func TestBuildSYNChecksum(t *testing.T) {
	src := net.IPv4(192, 168, 1, 10)
	dst := net.IPv4(93, 184, 216, 34)
	seg, err := buildSYN(src, dst, 40000, 443, 0xDEADBEEF)
	if err != nil {
		t.Fatalf("buildSYN: %v", err)
	}
	if len(seg) != tcpHeaderLen {
		t.Fatalf("segment length: got %d, want %d", len(seg), tcpHeaderLen)
	}
	// Recomputing the checksum over a segment that already carries the correct
	// checksum must yield 0 (the ones-complement self-check property).
	if got := tcpChecksum(src, dst, seg); got != 0 {
		t.Errorf("checksum self-check: got %#04x, want 0", got)
	}
	if seg[13] != flagSYN {
		t.Errorf("flags: got %#02x, want SYN %#02x", seg[13], flagSYN)
	}
	if binary.BigEndian.Uint32(seg[4:8]) != 0xDEADBEEF {
		t.Errorf("seq mismatch")
	}
}

func TestBuildSYNRejectsIPv6(t *testing.T) {
	if _, err := buildSYN(net.ParseIP("2001:db8::1"), net.IPv4(1, 1, 1, 1), 1, 2, 3); err == nil {
		t.Error("expected error for IPv6 source")
	}
}

func TestClassifyReply(t *testing.T) {
	const (
		srcPort   = uint16(40000)
		dstPort   = uint16(443)
		expectAck = uint32(1001)
	)
	tests := []struct {
		name      string
		seg       synSegment
		wantState portState
		wantOK    bool
	}{
		{"syn-ack open", synSegment{srcPort: dstPort, dstPort: srcPort, flags: flagSYN | flagACK, ack: expectAck}, stateOpen, true},
		{"rst closed", synSegment{srcPort: dstPort, dstPort: srcPort, flags: flagRST | flagACK}, stateClosed, true},
		{"syn-ack wrong ack ignored", synSegment{srcPort: dstPort, dstPort: srcPort, flags: flagSYN | flagACK, ack: 999}, stateUnreachable, false},
		{"wrong source port ignored", synSegment{srcPort: 80, dstPort: srcPort, flags: flagSYN | flagACK, ack: expectAck}, stateUnreachable, false},
		{"not addressed to us ignored", synSegment{srcPort: dstPort, dstPort: 12345, flags: flagRST}, stateUnreachable, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, ok := classifyReply(tt.seg, srcPort, dstPort, expectAck)
			if state != tt.wantState || ok != tt.wantOK {
				t.Errorf("got (%v, %v), want (%v, %v)", state, ok, tt.wantState, tt.wantOK)
			}
		})
	}
}

func TestParseTCPSegmentRoundTrip(t *testing.T) {
	src := net.IPv4(10, 0, 0, 1)
	dst := net.IPv4(10, 0, 0, 2)
	seg, err := buildSYN(src, dst, 1234, 5678, 42)
	if err != nil {
		t.Fatalf("buildSYN: %v", err)
	}
	got, err := parseTCPSegment(seg)
	if err != nil {
		t.Fatalf("parseTCPSegment: %v", err)
	}
	if got.srcPort != 1234 || got.dstPort != 5678 || got.seq != 42 || got.flags != flagSYN {
		t.Errorf("parsed %+v, unexpected fields", got)
	}
}

func TestParseTCPSegmentShort(t *testing.T) {
	if _, err := parseTCPSegment(make([]byte, 10)); err == nil {
		t.Error("expected error for short segment")
	}
}

func TestStripIPv4Header(t *testing.T) {
	tcp := make([]byte, tcpHeaderLen)
	tcp[0] = 0xAB // recognisable first TCP byte

	// Buffer with a 20-byte IPv4 header prepended.
	withIP := make([]byte, 20+len(tcp))
	withIP[0] = 0x45 // version 4, IHL 5 words
	copy(withIP[20:], tcp)

	if got := stripIPv4Header(withIP); len(got) != len(tcp) || got[0] != 0xAB {
		t.Errorf("with IP header: got %d bytes first=%#02x, want %d first=0xab", len(got), got[0], len(tcp))
	}
	if got := stripIPv4Header(tcp); len(got) != len(tcp) {
		t.Errorf("bare segment: got %d bytes, want %d unchanged", len(got), len(tcp))
	}
}
