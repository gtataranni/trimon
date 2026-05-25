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
