package stdout

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gtataranni/trimon/pkg/types"
)

func sampleResult() types.ProbeResult {
	return types.ProbeResult{
		Timestamp:       time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC),
		ProbeName:       "test-probe",
		Target:          "8.8.8.8",
		SourceIP:        "192.168.1.1",
		ProbeType:       "icmp",
		PacketsSent:     3,
		PacketsReceived: 3,
		RTTMinMS:        1.1,
		RTTMeanMS:       2.2,
		RTTMaxMS:        3.3,
		RTTStddevMS:     0.5,
		PacketLossRatio: 0.0,
		Status:          types.StatusSuccess,
		Labels:          map[string]string{"env": "prod"},
	}
}

func TestJSONOutput(t *testing.T) {
	var buf bytes.Buffer
	e := newWithWriter(&buf, "json")

	if err := e.Export(context.Background(), sampleResult()); err != nil {
		t.Fatalf("Export: %v", err)
	}

	var rec jsonRecord
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("json decode: %v\nraw: %s", err, buf.String())
	}

	if rec.Probe != "test-probe" {
		t.Errorf("probe: want test-probe, got %q", rec.Probe)
	}
	if rec.Status != "success" {
		t.Errorf("status: want success, got %q", rec.Status)
	}
	if rec.RTTMeanMS == nil || *rec.RTTMeanMS != 2.2 {
		t.Errorf("rtt_mean_ms: want 2.2, got %v", rec.RTTMeanMS)
	}
	if rec.PacketLoss == nil || *rec.PacketLoss != 0.0 {
		t.Errorf("packet_loss: want 0.0, got %v", rec.PacketLoss)
	}
	if rec.Labels["env"] != "prod" {
		t.Errorf("label env: want prod, got %q", rec.Labels["env"])
	}
	if rec.TS != "2024-06-01T12:00:00Z" {
		t.Errorf("ts: want 2024-06-01T12:00:00Z, got %q", rec.TS)
	}
}

func TestJSONErrorMsgOmitted(t *testing.T) {
	var buf bytes.Buffer
	e := newWithWriter(&buf, "json")

	r := sampleResult()
	r.Status = types.StatusSuccess
	r.ErrorMsg = ""
	_ = e.Export(context.Background(), r)

	if strings.Contains(buf.String(), "error_msg") {
		t.Error("error_msg should be omitted when empty")
	}
}

func TestJSONErrorMsgPresent(t *testing.T) {
	var buf bytes.Buffer
	e := newWithWriter(&buf, "json")

	r := sampleResult()
	r.Status = types.StatusError
	r.ErrorMsg = "socket error"
	_ = e.Export(context.Background(), r)

	var rec jsonRecord
	_ = json.Unmarshal(buf.Bytes(), &rec)
	if rec.ErrorMsg != "socket error" {
		t.Errorf("error_msg: want socket error, got %q", rec.ErrorMsg)
	}
}

func TestTextOutput(t *testing.T) {
	var buf bytes.Buffer
	e := newWithWriter(&buf, "text")

	if err := e.Export(context.Background(), sampleResult()); err != nil {
		t.Fatalf("Export: %v", err)
	}

	line := buf.String()
	if !strings.Contains(line, "probe=test-probe") {
		t.Errorf("text output missing probe name: %q", line)
	}
	if !strings.Contains(line, "status=success") {
		t.Errorf("text output missing status: %q", line)
	}
	if !strings.Contains(line, "loss=0%") {
		t.Errorf("text output missing loss: %q", line)
	}
}

func TestJSONRTTOmittedOnError(t *testing.T) {
	var buf bytes.Buffer
	e := newWithWriter(&buf, "json")

	r := sampleResult()
	r.Status = types.StatusError
	r.PacketsReceived = 0
	r.PacketsSent = 0
	r.RTTMinMS, r.RTTMeanMS, r.RTTMaxMS, r.RTTStddevMS = 0, 0, 0, 0
	r.PacketLossRatio = 0
	r.ErrorMsg = "socket error"
	_ = e.Export(context.Background(), r)

	raw := buf.String()
	for _, field := range []string{"rtt_min_ms", "rtt_mean_ms", "rtt_max_ms", "rtt_stddev_ms", "packet_loss"} {
		if strings.Contains(raw, field) {
			t.Errorf("field %q should be absent on status=error, got: %s", field, raw)
		}
	}
}

func TestNilLabelsBecomesEmpty(t *testing.T) {
	var buf bytes.Buffer
	e := newWithWriter(&buf, "json")

	r := sampleResult()
	r.Labels = nil
	_ = e.Export(context.Background(), r)

	var rec jsonRecord
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("json decode: %v", err)
	}
	if rec.Labels == nil {
		t.Error("labels should be {} not null")
	}
}
