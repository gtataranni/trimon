package types

import (
	"encoding/json"
	"testing"
	"time"
)

func TestProbeResultJSONRoundtrip(t *testing.T) {
	r := ProbeResult{
		Timestamp:       time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		ProbeName:       "test",
		Target:          "8.8.8.8",
		SourceIP:        "192.168.1.1",
		ProbeType:       "icmp",
		PacketsSent:     3,
		PacketsReceived: 3,
		RTTMinMS:        1.0,
		RTTMeanMS:       2.0,
		RTTMaxMS:        3.0,
		RTTStddevMS:     0.5,
		PacketLossRatio: 0.0,
		Status:          StatusSuccess,
		Labels:          map[string]string{"env": "test"},
	}

	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out ProbeResult
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if out.ProbeName != r.ProbeName {
		t.Errorf("ProbeName: want %q, got %q", r.ProbeName, out.ProbeName)
	}
	if out.Status != StatusSuccess {
		t.Errorf("Status: want %q, got %q", StatusSuccess, out.Status)
	}
	if out.PacketLossRatio != 0.0 {
		t.Errorf("PacketLossRatio: want 0.0, got %f", out.PacketLossRatio)
	}
}

func TestStatusConstants(t *testing.T) {
	cases := []struct {
		s    Status
		want string
	}{
		{StatusSuccess, "success"},
		{StatusPartial, "partial"},
		{StatusFailure, "failure"},
		{StatusError, "error"},
	}
	for _, tc := range cases {
		if string(tc.s) != tc.want {
			t.Errorf("Status %v: want %q, got %q", tc.s, tc.want, string(tc.s))
		}
	}
}

func TestStatusHelpers(t *testing.T) {
	cases := []struct {
		s         Status
		isSuccess bool
		isUp      bool
		isError   bool
	}{
		{StatusSuccess, true, true, false},
		{StatusPartial, false, true, false},
		{StatusFailure, false, false, false},
		{StatusError, false, false, true},
	}
	for _, tc := range cases {
		if got := tc.s.IsSuccess(); got != tc.isSuccess {
			t.Errorf("%s.IsSuccess(): want %v, got %v", tc.s, tc.isSuccess, got)
		}
		if got := tc.s.IsUp(); got != tc.isUp {
			t.Errorf("%s.IsUp(): want %v, got %v", tc.s, tc.isUp, got)
		}
		if got := tc.s.IsError(); got != tc.isError {
			t.Errorf("%s.IsError(): want %v, got %v", tc.s, tc.isError, got)
		}
	}
}

func TestErrorMsgOmitEmpty(t *testing.T) {
	r := ProbeResult{Status: StatusSuccess}
	b, _ := json.Marshal(r)
	var m map[string]interface{}
	_ = json.Unmarshal(b, &m)
	if _, ok := m["error_msg"]; ok {
		t.Error("error_msg should be omitted when empty")
	}
}

// TestMeasured is the canonical matrix for ProbeResult.Measured(): every
// status × probe type combination, asserting exactly which pointers are
// non-nil and their values. This is the single source of truth for the four
// predicates (RTT, Loss, Duration, PortOpen) — exporter tests should not
// re-derive them.
func TestMeasured(t *testing.T) {
	probeTypes := []string{ProbeTypeICMP, ProbeTypeTCP, ProbeTypeUDP, ProbeTypeDNS, ProbeTypeHTTP}

	type want struct {
		rtt      *RTT
		loss     *float64
		duration *float64
		portOpen *bool
	}

	f := func(v float64) *float64 { return &v }
	b := func(v bool) *bool { return &v }

	for _, pt := range probeTypes {
		t.Run(pt+"/success", func(t *testing.T) {
			r := ProbeResult{
				ProbeType:       pt,
				Status:          StatusSuccess,
				PacketsSent:     3,
				PacketsReceived: 3,
				RTTMinMS:        1, RTTMeanMS: 2, RTTMaxMS: 3, RTTStddevMS: 0.5,
				PacketLossRatio: 0,
				DurationMS:      42.5,
			}
			w := want{loss: f(0)}
			if pt != ProbeTypeHTTP {
				w.rtt = &RTT{MinMS: 1, MeanMS: 2, MaxMS: 3, StddevMS: 0.5}
			} else {
				w.duration = f(42.5)
			}
			assertMeasured(t, r, w)
		})

		t.Run(pt+"/partial", func(t *testing.T) {
			r := ProbeResult{
				ProbeType:       pt,
				Status:          StatusPartial,
				PacketsSent:     3,
				PacketsReceived: 2,
				RTTMinMS:        1, RTTMeanMS: 2, RTTMaxMS: 3, RTTStddevMS: 0.5,
				PacketLossRatio: 0.33,
			}
			w := want{loss: f(0.33)}
			if pt != ProbeTypeHTTP {
				w.rtt = &RTT{MinMS: 1, MeanMS: 2, MaxMS: 3, StddevMS: 0.5}
			}
			assertMeasured(t, r, w)
		})

		t.Run(pt+"/failure", func(t *testing.T) {
			r := ProbeResult{
				ProbeType:       pt,
				Status:          StatusFailure,
				PacketsSent:     3,
				PacketsReceived: 0,
				PacketLossRatio: 1.0,
			}
			w := want{loss: f(1.0)}
			assertMeasured(t, r, w)
		})

		t.Run(pt+"/error", func(t *testing.T) {
			r := ProbeResult{
				ProbeType: pt,
				Status:    StatusError,
				ErrorType: "probe_error",
			}
			w := want{}
			assertMeasured(t, r, w)
		})
	}

	t.Run("tcp/success with PortOpen true", func(t *testing.T) {
		r := ProbeResult{
			ProbeType:       ProbeTypeTCP,
			Status:          StatusSuccess,
			PacketsSent:     1,
			PacketsReceived: 1,
			PacketLossRatio: 0,
			PortOpen:        b(true),
		}
		w := want{loss: f(0), rtt: &RTT{}, portOpen: b(true)}
		assertMeasured(t, r, w)
	})

	t.Run("udp/failure with PortOpen false", func(t *testing.T) {
		r := ProbeResult{
			ProbeType:       ProbeTypeUDP,
			Status:          StatusFailure,
			PacketsSent:     1,
			PacketsReceived: 0,
			PacketLossRatio: 1.0,
			PortOpen:        b(false),
		}
		w := want{loss: f(1.0), portOpen: b(false)}
		assertMeasured(t, r, w)
	})

	t.Run("http/success with DurationMS == 0 (edge case)", func(t *testing.T) {
		r := ProbeResult{
			ProbeType:       ProbeTypeHTTP,
			Status:          StatusSuccess,
			PacketsSent:     1,
			PacketsReceived: 1,
			PacketLossRatio: 0,
			DurationMS:      0,
		}
		w := want{loss: f(0)}
		assertMeasured(t, r, w)
	})
}

func assertMeasured(t *testing.T, r ProbeResult, w struct {
	rtt      *RTT
	loss     *float64
	duration *float64
	portOpen *bool
}) {
	t.Helper()
	m := r.Measured()

	if (m.RTT == nil) != (w.rtt == nil) {
		t.Fatalf("RTT presence: got %v, want %v", m.RTT, w.rtt)
	}
	if m.RTT != nil && *m.RTT != *w.rtt {
		t.Errorf("RTT value: got %+v, want %+v", *m.RTT, *w.rtt)
	}

	if (m.Loss == nil) != (w.loss == nil) {
		t.Fatalf("Loss presence: got %v, want %v", m.Loss, w.loss)
	}
	if m.Loss != nil && *m.Loss != *w.loss {
		t.Errorf("Loss value: got %v, want %v", *m.Loss, *w.loss)
	}

	if (m.Duration == nil) != (w.duration == nil) {
		t.Fatalf("Duration presence: got %v, want %v", m.Duration, w.duration)
	}
	if m.Duration != nil && *m.Duration != *w.duration {
		t.Errorf("Duration value: got %v, want %v", *m.Duration, *w.duration)
	}

	if (m.PortOpen == nil) != (w.portOpen == nil) {
		t.Fatalf("PortOpen presence: got %v, want %v", m.PortOpen, w.portOpen)
	}
	if m.PortOpen != nil && *m.PortOpen != *w.portOpen {
		t.Errorf("PortOpen value: got %v, want %v", *m.PortOpen, *w.portOpen)
	}
}
