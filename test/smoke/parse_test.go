package smoke

import (
	"math"
	"testing"
)

// TestParsePromText exercises the Prometheus text parser against representative
// trimon output: labelled series (with the OTel bridge's otel_scope_* labels), a
// NaN value (rtt_mean for HTTP, which is NaN by design), and an unlabelled
// self-observability metric. It is tag-free so it runs in `go test ./...`.
func TestParsePromText(t *testing.T) {
	body := `# HELP trimon_probe_up ...
# TYPE trimon_probe_up gauge
trimon_probe_up{otel_scope_name="github.com/gtataranni/trimon",probe_name="internet",probe_source_ip="0.0.0.0",probe_target="8.8.8.8",probe_type="icmp"} 1
trimon_probe_up{probe_name="http-example",probe_target="example.com",probe_type="http"} 1
trimon_probe_rtt_mean_milliseconds{probe_name="internet",probe_target="8.8.8.8",probe_type="icmp"} 12.5
trimon_probe_rtt_mean_milliseconds{probe_name="http-example",probe_target="example.com",probe_type="http"} NaN
trimon_probe_port_open{probe_name="tcp-https",probe_target="1.1.1.1",probe_type="tcp"} 1
trimon_probe_duration_milliseconds{probe_name="http-example",probe_target="example.com",probe_type="http"} 42.5
trimon_scheduler_goroutines 7
`
	s := parsePromText(body)

	if ups := ofType(s, "trimon_probe_up", "icmp"); !anyValue(ups, 1) {
		t.Errorf("icmp probe_up==1 not found, got %+v", ups)
	}
	if fps := ofType(s, "trimon_probe_rtt_mean_milliseconds", "icmp"); !anyNonNaN(fps) {
		t.Errorf("icmp rtt_mean should have a non-NaN value")
	}
	// rtt_mean is NaN by design for HTTP — must not register as a non-NaN value.
	if fps := ofType(s, "trimon_probe_rtt_mean_milliseconds", "http"); anyNonNaN(fps) {
		t.Errorf("http rtt_mean should be NaN, got %+v", fps)
	}
	if fps := ofType(s, "trimon_probe_duration_milliseconds", "http"); !anyNonNaN(fps) {
		t.Errorf("http duration should have a non-NaN value")
	}
	if fps := ofType(s, "trimon_probe_port_open", "tcp"); !anyValue(fps, 1) {
		t.Errorf("tcp port_open==1 not found")
	}

	// Label and value extraction on a fully-labelled series.
	icmpRTT := ofType(s, "trimon_probe_rtt_mean_milliseconds", "icmp")
	if len(icmpRTT) != 1 {
		t.Fatalf("expected 1 icmp rtt_mean series, got %d", len(icmpRTT))
	}
	if got := icmpRTT[0].labels["probe_target"]; got != "8.8.8.8" {
		t.Errorf("probe_target label = %q, want 8.8.8.8", got)
	}
	if got := icmpRTT[0].value; got != 12.5 {
		t.Errorf("value = %v, want 12.5", got)
	}

	// Unlabelled metric parses with the value in the right field.
	var sched bool
	for _, x := range s {
		if x.name == "trimon_scheduler_goroutines" && x.value == 7 {
			sched = true
		}
	}
	if !sched {
		t.Errorf("unlabelled metric trimon_scheduler_goroutines not parsed")
	}

	if !math.IsNaN(parseValue("NaN")) {
		t.Errorf("parseValue(\"NaN\") should be NaN")
	}
}
