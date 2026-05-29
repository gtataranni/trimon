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
// Pointer fields use omitempty to distinguish "not measured" from a legitimate zero.
// This struct is write-only (only ever passed to Encode), so nil-checks are confined to tests.
type jsonRecord struct {
	TS          string            `json:"ts"`
	Probe       string            `json:"probe"`
	Type        string            `json:"type"`
	Target      string            `json:"target"`
	FQDN        string            `json:"fqdn,omitempty"`
	SourceIP    string            `json:"source_ip"`
	Status      string            `json:"status"`
	DurationMS  *float64          `json:"duration_ms,omitempty"` // HTTP probes
	RTTMeanMS   *float64          `json:"rtt_mean_ms,omitempty"` // multi-packet probes
	RTTMinMS    *float64          `json:"rtt_min_ms,omitempty"`
	RTTMaxMS    *float64          `json:"rtt_max_ms,omitempty"`
	RTTStddevMS *float64          `json:"rtt_stddev_ms,omitempty"`
	PacketLoss  *float64          `json:"packet_loss,omitempty"`
	ErrorMsg    string            `json:"error_msg,omitempty"`
	ErrorType   string            `json:"error_type,omitempty"`
	Labels      map[string]string `json:"labels"`
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

// Name returns the exporter identifier used in self-observability metrics.
func (e *Exporter) Name() string { return "stdout" }

func (e *Exporter) Export(_ context.Context, r types.ProbeResult) error {
	if e.format == "json" {
		return e.writeJSON(r)
	}
	return e.writeText(r)
}

func (e *Exporter) Close() error { return nil }

func (e *Exporter) writeJSON(r types.ProbeResult) error {
	rec := jsonRecord{
		TS:        r.Timestamp.UTC().Format(time.RFC3339),
		Probe:     r.ProbeName,
		Type:      r.ProbeType,
		Target:    r.Target,
		FQDN:      r.FQDN,
		SourceIP:  r.SourceIP,
		Status:    string(r.Status),
		ErrorMsg:  r.ErrorMsg,
		ErrorType: r.ErrorType,
		Labels:    r.Labels,
	}
	if r.Status != types.StatusError {
		rec.PacketLoss = &r.PacketLossRatio
	}
	if r.ProbeType == types.ProbeTypeHTTP {
		if r.DurationMS > 0 {
			rec.DurationMS = &r.DurationMS
		}
	} else if r.PacketsReceived > 0 {
		rec.RTTMeanMS = &r.RTTMeanMS
		rec.RTTMinMS = &r.RTTMinMS
		rec.RTTMaxMS = &r.RTTMaxMS
		rec.RTTStddevMS = &r.RTTStddevMS
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
	fqdnText := ""
	if r.FQDN != "" {
		fqdnText = fmt.Sprintf(" fqdn=%s", r.FQDN)
	}
	lossText := ""
	if r.Status != types.StatusError {
		lossText = fmt.Sprintf(" loss=%.0f%%", r.PacketLossRatio*100)
	}
	if r.ProbeType == types.ProbeTypeHTTP {
		_, err := fmt.Fprintf(e.w,
			"%s probe=%s type=%s target=%s%s src=%s status=%s%s duration=%.2fms%s\n",
			r.Timestamp.UTC().Format(time.RFC3339),
			r.ProbeName,
			r.ProbeType,
			r.Target,
			fqdnText,
			r.SourceIP,
			r.Status,
			lossText,
			r.DurationMS,
			errPart,
		)
		return err
	}
	_, err := fmt.Fprintf(e.w,
		"%s probe=%s type=%s target=%s%s src=%s status=%s%s rtt_mean=%.2fms rtt_min=%.2fms rtt_max=%.2fms%s\n",
		r.Timestamp.UTC().Format(time.RFC3339),
		r.ProbeName,
		r.ProbeType,
		r.Target,
		fqdnText,
		r.SourceIP,
		r.Status,
		lossText,
		r.RTTMeanMS,
		r.RTTMinMS,
		r.RTTMaxMS,
		errPart,
	)
	return err
}
