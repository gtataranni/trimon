package stdout

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/gtataranni/trimon/pkg/types"
)

// jsonRecord is the NDJSON shape for JSON mode.
type jsonRecord struct {
	TS           string            `json:"ts"`
	Probe        string            `json:"probe"`
	Type         string            `json:"type"`
	Target       string            `json:"target"`
	SourceIP     string            `json:"source_ip"`
	Status       string            `json:"status"`
	RTTMeanMS    float64           `json:"rtt_mean_ms"`
	RTTMinMS     float64           `json:"rtt_min_ms"`
	RTTMaxMS     float64           `json:"rtt_max_ms"`
	RTTStddevMS  float64           `json:"rtt_stddev_ms"`
	PacketLoss   float64           `json:"packet_loss"`
	ErrorMsg     string            `json:"error_msg,omitempty"`
	ErrorType    string            `json:"error_type,omitempty"`
	Labels       map[string]string `json:"labels"`
}

// Exporter writes results to w in either JSON or text format.
type Exporter struct {
	w      io.Writer
	format string // "json" | "text"
	enc    *json.Encoder
}

// New creates a stdout Exporter. format must be "json" or "text".
func New(format string) *Exporter {
	return newWithWriter(os.Stdout, format)
}

func newWithWriter(w io.Writer, format string) *Exporter {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return &Exporter{w: w, format: format, enc: enc}
}

func (e *Exporter) Export(_ context.Context, r types.ProbeResult) error {
	if e.format == "json" {
		return e.writeJSON(r)
	}
	return e.writeText(r)
}

func (e *Exporter) Close() error { return nil }

func (e *Exporter) writeJSON(r types.ProbeResult) error {
	rec := jsonRecord{
		TS:          r.Timestamp.UTC().Format(time.RFC3339),
		Probe:       r.ProbeName,
		Type:        r.ProbeType,
		Target:      r.Target,
		SourceIP:    r.SourceIP,
		Status:      string(r.Status),
		RTTMeanMS:   r.RTTMeanMS,
		RTTMinMS:    r.RTTMinMS,
		RTTMaxMS:    r.RTTMaxMS,
		RTTStddevMS: r.RTTStddevMS,
		PacketLoss:  r.PacketLossRatio,
		ErrorMsg:    r.ErrorMsg,
		ErrorType:   r.ErrorType,
		Labels:      r.Labels,
	}
	if rec.Labels == nil {
		rec.Labels = map[string]string{}
	}
	return e.enc.Encode(rec)
}

func (e *Exporter) writeText(r types.ProbeResult) error {
	errPart := ""
	if r.ErrorMsg != "" {
		errPart = fmt.Sprintf(" error=%q", r.ErrorMsg)
	}
	_, err := fmt.Fprintf(e.w,
		"%s probe=%s type=%s target=%s src=%s status=%s loss=%.0f%% rtt_mean=%.2fms rtt_min=%.2fms rtt_max=%.2fms%s\n",
		r.Timestamp.UTC().Format(time.RFC3339),
		r.ProbeName,
		r.ProbeType,
		r.Target,
		r.SourceIP,
		r.Status,
		r.PacketLossRatio*100,
		r.RTTMeanMS,
		r.RTTMinMS,
		r.RTTMaxMS,
		errPart,
	)
	return err
}
