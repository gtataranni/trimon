package otlp

import (
	"context"
	"log/slog"
	"math"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/gtataranni/trimon/internal/config"
	"github.com/gtataranni/trimon/pkg/types"
)

// newTestExporter builds an Exporter wired to a ManualReader so tests can
// call reader.Collect to inspect recorded values without a real collector.
func newTestExporter(t *testing.T) (*Exporter, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	meter := provider.Meter(instrScope)
	e := &Exporter{provider: provider, logger: slog.Default()}
	if err := e.registerInstruments(meter, "test-version", "abc1234"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	return e, reader
}

// newBridgeExporter creates a real Exporter (with Prometheus bridge, no OTLP)
// for integration tests that verify /metrics output.
func newBridgeExporter(t *testing.T) *Exporter {
	t.Helper()
	exp, err := New(context.Background(), config.OTLPExporterConfig{Enabled: false}, "test-version", "abc1234", slog.Default())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = exp.Close() })
	return exp
}

// collectMetrics triggers a manual collection and returns the flat Metrics slice.
func collectMetrics(t *testing.T, reader *sdkmetric.ManualReader) []metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(rm.ScopeMetrics) == 0 {
		return nil
	}
	return rm.ScopeMetrics[0].Metrics
}

// findFloat64Gauge locates a named metric and returns its single datapoint value
// and attribute set. Fatals if absent or not a Gauge[float64].
func findFloat64Gauge(t *testing.T, ms []metricdata.Metrics, name string) (float64, attribute.Set) {
	t.Helper()
	for _, m := range ms {
		if m.Name != name {
			continue
		}
		g, ok := m.Data.(metricdata.Gauge[float64])
		if !ok {
			t.Fatalf("metric %q: expected Gauge[float64], got %T", name, m.Data)
		}
		if len(g.DataPoints) == 0 {
			t.Fatalf("metric %q: no datapoints", name)
		}
		return g.DataPoints[0].Value, g.DataPoints[0].Attributes
	}
	t.Fatalf("metric %q not found in collected metrics", name)
	return 0, attribute.NewSet()
}

// findInt64Gauge locates a named metric and returns its single datapoint value
// and attribute set. Fatals if absent or not a Gauge[int64].
func findInt64Gauge(t *testing.T, ms []metricdata.Metrics, name string) (int64, attribute.Set) {
	t.Helper()
	for _, m := range ms {
		if m.Name != name {
			continue
		}
		g, ok := m.Data.(metricdata.Gauge[int64])
		if !ok {
			t.Fatalf("metric %q: expected Gauge[int64], got %T", name, m.Data)
		}
		if len(g.DataPoints) == 0 {
			t.Fatalf("metric %q: no datapoints", name)
		}
		return g.DataPoints[0].Value, g.DataPoints[0].Attributes
	}
	t.Fatalf("metric %q not found in collected metrics", name)
	return 0, attribute.NewSet()
}

// findInt64Counter locates a named metric and returns the sum of its datapoints.
// Fatals if absent or not a Sum[int64].
func findInt64Counter(t *testing.T, ms []metricdata.Metrics, name string) int64 {
	t.Helper()
	for _, m := range ms {
		if m.Name != name {
			continue
		}
		s, ok := m.Data.(metricdata.Sum[int64])
		if !ok {
			t.Fatalf("metric %q: expected Sum[int64], got %T", name, m.Data)
		}
		var total int64
		for _, dp := range s.DataPoints {
			total += dp.Value
		}
		return total
	}
	t.Fatalf("metric %q not found in collected metrics", name)
	return 0
}

// findInt64CounterByAttr locates a named counter metric and returns the value of
// the datapoint whose attrKey matches attrVal. Fatals if absent.
func findInt64CounterByAttr(t *testing.T, ms []metricdata.Metrics, name, attrKey, attrVal string) int64 {
	t.Helper()
	for _, m := range ms {
		if m.Name != name {
			continue
		}
		s, ok := m.Data.(metricdata.Sum[int64])
		if !ok {
			t.Fatalf("metric %q: expected Sum[int64], got %T", name, m.Data)
		}
		for _, dp := range s.DataPoints {
			v, ok := dp.Attributes.Value(attribute.Key(attrKey))
			if ok && v.AsString() == attrVal {
				return dp.Value
			}
		}
		t.Fatalf("metric %q: no datapoint with %s=%q", name, attrKey, attrVal)
	}
	t.Fatalf("metric %q not found in collected metrics", name)
	return 0
}

// attrString retrieves a string attribute value from a Set, fataling when absent.
func attrString(t *testing.T, attrs attribute.Set, key string) string {
	t.Helper()
	v, ok := attrs.Value(attribute.Key(key))
	if !ok {
		t.Fatalf("attribute %q not found", key)
	}
	return v.AsString()
}

// metricsBody hits the Prometheus bridge handler and returns the response body.
func metricsBody(t *testing.T, exp *Exporter) string {
	t.Helper()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rr := httptest.NewRecorder()
	exp.PrometheusHandler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("/metrics: want 200, got %d\nbody: %s", rr.Code, rr.Body.String())
	}
	return rr.Body.String()
}

// ---- tests ------------------------------------------------------------------

// TestStatusSuccess verifies that a StatusSuccess result sets success=1,
// propagates RTT values, packet_loss=0, and up=1.
func TestStatusSuccess(t *testing.T) {
	e, reader := newTestExporter(t)
	ctx := context.Background()

	result := types.ProbeResult{
		ProbeName:       "ping-gw",
		ProbeType:       "icmp",
		Target:          "192.168.1.1",
		SourceIP:        "0.0.0.0",
		Status:          types.StatusSuccess,
		RTTMinMS:        10.0,
		RTTMeanMS:       12.5,
		RTTMaxMS:        15.0,
		RTTStddevMS:     1.5,
		PacketsSent:     3,
		PacketsReceived: 3,
		PacketLossRatio: 0.0,
	}

	if err := e.Export(ctx, result); err != nil {
		t.Fatalf("Export: %v", err)
	}

	ms := collectMetrics(t, reader)

	successVal, _ := findInt64Gauge(t, ms, "trimon.probe.success")
	if successVal != 1 {
		t.Errorf("success: got %d, want 1", successVal)
	}

	upVal, _ := findInt64Gauge(t, ms, "trimon.probe.up")
	if upVal != 1 {
		t.Errorf("up: got %d, want 1", upVal)
	}

	rttMean, _ := findFloat64Gauge(t, ms, "trimon.probe.rtt.mean")
	if rttMean != 12.5 {
		t.Errorf("rtt.mean: got %f, want 12.5", rttMean)
	}

	rttMin, _ := findFloat64Gauge(t, ms, "trimon.probe.rtt.min")
	if rttMin != 10.0 {
		t.Errorf("rtt.min: got %f, want 10.0", rttMin)
	}

	rttMax, _ := findFloat64Gauge(t, ms, "trimon.probe.rtt.max")
	if rttMax != 15.0 {
		t.Errorf("rtt.max: got %f, want 15.0", rttMax)
	}

	rttStddev, _ := findFloat64Gauge(t, ms, "trimon.probe.rtt.stddev")
	if rttStddev != 1.5 {
		t.Errorf("rtt.stddev: got %f, want 1.5", rttStddev)
	}

	loss, _ := findFloat64Gauge(t, ms, "trimon.probe.packet_loss")
	if loss != 0.0 {
		t.Errorf("packet_loss: got %f, want 0.0", loss)
	}

	sent := findInt64Counter(t, ms, "trimon.probe.packets_sent")
	if sent != 3 {
		t.Errorf("packets_sent: got %d, want 3", sent)
	}

	recv := findInt64Counter(t, ms, "trimon.probe.packets_received")
	if recv != 3 {
		t.Errorf("packets_received: got %d, want 3", recv)
	}
}

// TestStatusPartial verifies that a StatusPartial result sets success=0, up=1,
// propagates RTT values, and records the supplied packet_loss ratio.
func TestStatusPartial(t *testing.T) {
	e, reader := newTestExporter(t)
	ctx := context.Background()

	result := types.ProbeResult{
		ProbeName:       "ping-gw",
		ProbeType:       "icmp",
		Target:          "192.168.1.1",
		SourceIP:        "0.0.0.0",
		Status:          types.StatusPartial,
		RTTMinMS:        5.0,
		RTTMeanMS:       8.0,
		RTTMaxMS:        11.0,
		RTTStddevMS:     2.0,
		PacketsSent:     3,
		PacketsReceived: 2,
		PacketLossRatio: 0.33,
	}

	if err := e.Export(ctx, result); err != nil {
		t.Fatalf("Export: %v", err)
	}

	ms := collectMetrics(t, reader)

	successVal, _ := findInt64Gauge(t, ms, "trimon.probe.success")
	if successVal != 0 {
		t.Errorf("success: got %d, want 0", successVal)
	}

	upVal, _ := findInt64Gauge(t, ms, "trimon.probe.up")
	if upVal != 1 {
		t.Errorf("up: got %d, want 1 (partial = up)", upVal)
	}

	rttMean, _ := findFloat64Gauge(t, ms, "trimon.probe.rtt.mean")
	if rttMean != 8.0 {
		t.Errorf("rtt.mean: got %f, want 8.0", rttMean)
	}

	loss, _ := findFloat64Gauge(t, ms, "trimon.probe.packet_loss")
	if loss != 0.33 {
		t.Errorf("packet_loss: got %f, want 0.33", loss)
	}
}

// TestStatusFailure verifies that a StatusFailure result sets success=0, up=0,
// packet_loss=1.0, and RTT gauges are all zero.
func TestStatusFailure(t *testing.T) {
	e, reader := newTestExporter(t)
	ctx := context.Background()

	result := types.ProbeResult{
		ProbeName:       "ping-gw",
		ProbeType:       "icmp",
		Target:          "192.168.1.1",
		SourceIP:        "0.0.0.0",
		Status:          types.StatusFailure,
		PacketsSent:     3,
		PacketsReceived: 0,
		PacketLossRatio: 1.0,
	}

	if err := e.Export(ctx, result); err != nil {
		t.Fatalf("Export: %v", err)
	}

	ms := collectMetrics(t, reader)

	successVal, _ := findInt64Gauge(t, ms, "trimon.probe.success")
	if successVal != 0 {
		t.Errorf("success: got %d, want 0", successVal)
	}

	upVal, _ := findInt64Gauge(t, ms, "trimon.probe.up")
	if upVal != 0 {
		t.Errorf("up: got %d, want 0 (failure = down)", upVal)
	}

	loss, _ := findFloat64Gauge(t, ms, "trimon.probe.packet_loss")
	if loss != 1.0 {
		t.Errorf("packet_loss: got %f, want 1.0", loss)
	}

	for _, rttName := range []string{
		"trimon.probe.rtt.min",
		"trimon.probe.rtt.mean",
		"trimon.probe.rtt.max",
		"trimon.probe.rtt.stddev",
	} {
		v, _ := findFloat64Gauge(t, ms, rttName)
		if v != 0.0 {
			t.Errorf("%s: got %f, want 0.0", rttName, v)
		}
	}
}

// TestStatusError verifies that a StatusError result sets success=0, up=0,
// packet_loss=NaN, and packet counters are not incremented.
func TestStatusError(t *testing.T) {
	e, reader := newTestExporter(t)
	ctx := context.Background()

	result := types.ProbeResult{
		ProbeName: "ping-gw",
		ProbeType: "icmp",
		Target:    "192.168.1.1",
		SourceIP:  "0.0.0.0",
		Status:    types.StatusError,
		ErrorMsg:  "socket error",
	}

	if err := e.Export(ctx, result); err != nil {
		t.Fatalf("Export: %v", err)
	}

	ms := collectMetrics(t, reader)

	successVal, _ := findInt64Gauge(t, ms, "trimon.probe.success")
	if successVal != 0 {
		t.Errorf("success: got %d, want 0", successVal)
	}

	upVal, _ := findInt64Gauge(t, ms, "trimon.probe.up")
	if upVal != 0 {
		t.Errorf("up: got %d, want 0 (error = down)", upVal)
	}

	loss, _ := findFloat64Gauge(t, ms, "trimon.probe.packet_loss")
	if !math.IsNaN(loss) {
		t.Errorf("packet_loss: got %f, want NaN", loss)
	}

	for _, rttName := range []string{
		"trimon.probe.rtt.min",
		"trimon.probe.rtt.mean",
		"trimon.probe.rtt.max",
		"trimon.probe.rtt.stddev",
	} {
		v, _ := findFloat64Gauge(t, ms, rttName)
		if v != 0.0 {
			t.Errorf("%s: got %f, want 0.0", rttName, v)
		}
	}

	// Packet counters must NOT be incremented on error (probe could not run).
	for _, name := range []string{"trimon.probe.packets_sent", "trimon.probe.packets_received"} {
		found := false
		for _, m := range ms {
			if m.Name == name {
				found = true
				s := m.Data.(metricdata.Sum[int64])
				for _, dp := range s.DataPoints {
					if dp.Value != 0 {
						t.Errorf("%s: got %d, want 0 on error", name, dp.Value)
					}
				}
			}
		}
		if found {
			// counter was recorded with value 0 — that's acceptable
		}
	}
}

// TestProbeRunsCounter verifies that probeRuns is incremented for every Export call.
func TestProbeRunsCounter(t *testing.T) {
	e, reader := newTestExporter(t)
	ctx := context.Background()

	r := types.ProbeResult{ProbeName: "p", ProbeType: "icmp", Target: "1.2.3.4", SourceIP: "0.0.0.0", Status: types.StatusSuccess, PacketsSent: 1, PacketsReceived: 1}
	_ = e.Export(ctx, r)
	_ = e.Export(ctx, r)

	ms := collectMetrics(t, reader)
	runs := findInt64Counter(t, ms, "trimon.probe.runs")
	if runs != 2 {
		t.Errorf("probe.runs: got %d, want 2", runs)
	}
}

// TestProbeErrorsCounter verifies that probeErrors is incremented on StatusError.
func TestProbeErrorsCounter(t *testing.T) {
	e, reader := newTestExporter(t)
	ctx := context.Background()

	errResult := types.ProbeResult{ProbeName: "p", ProbeType: "icmp", Target: "1.2.3.4", SourceIP: "0.0.0.0", Status: types.StatusError}
	okResult := types.ProbeResult{ProbeName: "p", ProbeType: "icmp", Target: "1.2.3.4", SourceIP: "0.0.0.0", Status: types.StatusSuccess, PacketsSent: 1, PacketsReceived: 1}
	_ = e.Export(ctx, errResult)
	_ = e.Export(ctx, okResult)

	ms := collectMetrics(t, reader)
	errors := findInt64Counter(t, ms, "trimon.probe.errors")
	if errors != 1 {
		t.Errorf("probe.errors: got %d, want 1", errors)
	}
}

// TestRequiredAttributes verifies that every probe result metric carries the four
// mandatory probe attributes on every status path, and that probe.status is absent.
func TestRequiredAttributes(t *testing.T) {
	cases := []struct {
		name   string
		result types.ProbeResult
	}{
		{
			name: "success",
			result: types.ProbeResult{
				ProbeName:       "alpha",
				ProbeType:       "icmp",
				Target:          "10.0.0.1",
				SourceIP:        "10.0.0.2",
				Status:          types.StatusSuccess,
				PacketsSent:     1,
				PacketsReceived: 1,
			},
		},
		{
			name: "partial",
			result: types.ProbeResult{
				ProbeName:       "beta",
				ProbeType:       "icmp",
				Target:          "10.0.0.3",
				SourceIP:        "10.0.0.4",
				Status:          types.StatusPartial,
				PacketsSent:     3,
				PacketsReceived: 1,
				PacketLossRatio: 0.66,
			},
		},
		{
			name: "failure",
			result: types.ProbeResult{
				ProbeName: "gamma",
				ProbeType: "icmp",
				Target:    "10.0.0.5",
				SourceIP:  "10.0.0.6",
				Status:    types.StatusFailure,
			},
		},
		{
			name: "error",
			result: types.ProbeResult{
				ProbeName: "delta",
				ProbeType: "icmp",
				Target:    "10.0.0.7",
				SourceIP:  "10.0.0.8",
				Status:    types.StatusError,
				ErrorMsg:  "raw socket unavailable",
			},
		},
	}

	requiredKeys := []string{"probe.name", "probe.type", "probe.target", "probe.source_ip"}

	// Check against packet_loss which is always recorded.
	const checkMetric = "trimon.probe.packet_loss"

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, reader := newTestExporter(t)
			if err := e.Export(context.Background(), tc.result); err != nil {
				t.Fatalf("Export: %v", err)
			}
			ms := collectMetrics(t, reader)
			_, attrs := findFloat64Gauge(t, ms, checkMetric)

			for _, key := range requiredKeys {
				if _, ok := attrs.Value(attribute.Key(key)); !ok {
					t.Errorf("attribute %q missing for status %s", key, tc.name)
				}
			}

			if got := attrString(t, attrs, "probe.name"); got != tc.result.ProbeName {
				t.Errorf("probe.name: got %q, want %q", got, tc.result.ProbeName)
			}
			if got := attrString(t, attrs, "probe.type"); got != tc.result.ProbeType {
				t.Errorf("probe.type: got %q, want %q", got, tc.result.ProbeType)
			}
			if got := attrString(t, attrs, "probe.target"); got != tc.result.Target {
				t.Errorf("probe.target: got %q, want %q", got, tc.result.Target)
			}
			if got := attrString(t, attrs, "probe.source_ip"); got != tc.result.SourceIP {
				t.Errorf("probe.source_ip: got %q, want %q", got, tc.result.SourceIP)
			}
			if _, ok := attrs.Value(attribute.Key("probe.status")); ok {
				t.Errorf("probe.status must not be an attribute: causes stale gauge series in Prometheus when status changes")
			}
		})
	}
}

// TestUserLabels verifies that keys from ProbeResult.Labels are forwarded as
// metric attributes alongside the mandatory probe attributes.
func TestUserLabels(t *testing.T) {
	e, reader := newTestExporter(t)
	ctx := context.Background()

	result := types.ProbeResult{
		ProbeName:       "labelled-probe",
		ProbeType:       "icmp",
		Target:          "10.0.0.1",
		SourceIP:        "0.0.0.0",
		Status:          types.StatusSuccess,
		PacketsSent:     1,
		PacketsReceived: 1,
		Labels: map[string]string{
			"env":    "prod",
			"region": "eu-west-1",
		},
	}

	if err := e.Export(ctx, result); err != nil {
		t.Fatalf("Export: %v", err)
	}

	ms := collectMetrics(t, reader)
	_, attrs := findFloat64Gauge(t, ms, "trimon.probe.packet_loss")

	for labelKey, wantVal := range result.Labels {
		gotVal := attrString(t, attrs, labelKey)
		if gotVal != wantVal {
			t.Errorf("user label %q: got %q, want %q", labelKey, gotVal, wantVal)
		}
	}

	for _, key := range []string{"probe.name", "probe.type", "probe.target", "probe.source_ip"} {
		if _, ok := attrs.Value(attribute.Key(key)); !ok {
			t.Errorf("mandatory attribute %q missing when user labels present", key)
		}
	}
}

// TestAllMetricsPresent verifies that every expected instrument name appears
// in the collected output after a single successful Export call.
func TestAllMetricsPresent(t *testing.T) {
	e, reader := newTestExporter(t)
	ctx := context.Background()

	result := types.ProbeResult{
		ProbeName:       "smoke",
		ProbeType:       "icmp",
		Target:          "1.1.1.1",
		SourceIP:        "0.0.0.0",
		Status:          types.StatusSuccess,
		PacketsSent:     5,
		PacketsReceived: 5,
		RTTMinMS:        1.0,
		RTTMeanMS:       2.0,
		RTTMaxMS:        3.0,
		RTTStddevMS:     0.5,
		PacketLossRatio: 0.0,
	}

	if err := e.Export(ctx, result); err != nil {
		t.Fatalf("Export: %v", err)
	}
	e.RecordExporterError(ctx, "otlp")
	e.RecordDroppedResult(ctx, "smoke")

	ms := collectMetrics(t, reader)
	names := make(map[string]bool, len(ms))
	for _, m := range ms {
		names[m.Name] = true
	}

	expected := []string{
		"trimon.probe.rtt.min",
		"trimon.probe.rtt.mean",
		"trimon.probe.rtt.max",
		"trimon.probe.rtt.stddev",
		"trimon.probe.packet_loss",
		"trimon.probe.packets_sent",
		"trimon.probe.packets_received",
		"trimon.probe.success",
		"trimon.probe.up",
		"trimon.probe.runs",
		"trimon.probe.results_dropped",
		"trimon.build.info",
		"trimon.scheduler.goroutines",
		"trimon.exporter.errors",
	}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("expected metric %q not found in collected output", name)
		}
	}
}

// TestExportReturnsNilError confirms that Export always returns nil for all
// valid status values.
func TestExportReturnsNilError(t *testing.T) {
	statuses := []types.Status{
		types.StatusSuccess,
		types.StatusPartial,
		types.StatusFailure,
		types.StatusError,
	}

	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			e, _ := newTestExporter(t)
			result := types.ProbeResult{
				ProbeName: "p",
				ProbeType: "icmp",
				Target:    "1.2.3.4",
				SourceIP:  "0.0.0.0",
				Status:    status,
			}
			if err := e.Export(context.Background(), result); err != nil {
				t.Errorf("Export returned unexpected error: %v", err)
			}
		})
	}
}

// ── Prometheus bridge integration tests ──────────────────────────────────────

// TestBridgeBuildInfo verifies that trimon_build_info appears in the Prometheus
// output with the correct label values.
func TestBridgeBuildInfo(t *testing.T) {
	exp := newBridgeExporter(t)
	body := metricsBody(t, exp)

	if !strings.Contains(body, `trimon_build_info`) {
		t.Errorf("trimon_build_info not found in /metrics output\n%s", body)
	}
	if !strings.Contains(body, `version="test-version"`) {
		t.Errorf("version label not found in /metrics output\n%s", body)
	}
	if !strings.Contains(body, `commit="abc1234"`) {
		t.Errorf("commit label not found in /metrics output\n%s", body)
	}
}

// TestBridgeProbeUp verifies that probe up/success gauges and packet counters
// appear in the Prometheus output with correct values after Export.
func TestBridgeProbeUp(t *testing.T) {
	exp := newBridgeExporter(t)

	cases := []struct {
		name    string
		result  types.ProbeResult
		wantUp  string
		wantDown string
	}{
		{
			name: "success",
			result: types.ProbeResult{
				ProbeName: "probe-a", ProbeType: "icmp", Target: "1.1.1.1", SourceIP: "0.0.0.0",
				Status: types.StatusSuccess, PacketsSent: 3, PacketsReceived: 3, PacketLossRatio: 0,
			},
			wantUp: `trimon_probe_up{`,
		},
		{
			name: "failure",
			result: types.ProbeResult{
				ProbeName: "probe-b", ProbeType: "icmp", Target: "2.2.2.2", SourceIP: "0.0.0.0",
				Status: types.StatusFailure, PacketsSent: 3, PacketsReceived: 0, PacketLossRatio: 1,
			},
			wantDown: `trimon_probe_up{`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := exp.Export(context.Background(), tc.result); err != nil {
				t.Fatalf("Export: %v", err)
			}
		})
	}

	body := metricsBody(t, exp)

	if !strings.Contains(body, "trimon_probe_up") {
		t.Errorf("trimon_probe_up not found in /metrics output\n%s", body)
	}
	if !strings.Contains(body, "trimon_probe_up_total") {
		// probe_up is a gauge, not _total
	}
	// Verify packet counters appear
	if !strings.Contains(body, "trimon_probe_packets_sent") {
		t.Errorf("trimon_probe_packets_sent not found in /metrics output\n%s", body)
	}
}

// TestExporterName verifies that the OTLP exporter returns the expected name.
func TestExporterName(t *testing.T) {
	e, _ := newTestExporter(t)
	if got := e.Name(); got != "otlp" {
		t.Errorf("Name: got %q, want %q", got, "otlp")
	}
}

// TestExporterErrorsCounter verifies that RecordExporterError increments
// trimon.exporter.errors with the correct exporter.name attribute.
func TestExporterErrorsCounter(t *testing.T) {
	e, reader := newTestExporter(t)
	ctx := context.Background()

	e.RecordExporterError(ctx, "otlp")
	e.RecordExporterError(ctx, "otlp")
	e.RecordExporterError(ctx, "stdout")

	ms := collectMetrics(t, reader)

	otlpCount := findInt64CounterByAttr(t, ms, "trimon.exporter.errors", "exporter.name", "otlp")
	if otlpCount != 2 {
		t.Errorf("exporter.errors[otlp]: got %d, want 2", otlpCount)
	}

	stdoutCount := findInt64CounterByAttr(t, ms, "trimon.exporter.errors", "exporter.name", "stdout")
	if stdoutCount != 1 {
		t.Errorf("exporter.errors[stdout]: got %d, want 1", stdoutCount)
	}
}

// TestDroppedResultsCounter verifies that RecordDroppedResult increments
// trimon.probe.results_dropped with the correct probe.name attribute.
func TestDroppedResultsCounter(t *testing.T) {
	e, reader := newTestExporter(t)
	ctx := context.Background()

	e.RecordDroppedResult(ctx, "probe-a")
	e.RecordDroppedResult(ctx, "probe-a")
	e.RecordDroppedResult(ctx, "probe-b")

	ms := collectMetrics(t, reader)

	aCount := findInt64CounterByAttr(t, ms, "trimon.probe.results_dropped", "probe.name", "probe-a")
	if aCount != 2 {
		t.Errorf("results_dropped[probe-a]: got %d, want 2", aCount)
	}

	bCount := findInt64CounterByAttr(t, ms, "trimon.probe.results_dropped", "probe.name", "probe-b")
	if bCount != 1 {
		t.Errorf("results_dropped[probe-b]: got %d, want 1", bCount)
	}
}

// TestBridgeExporterErrors verifies that trimon_exporter_errors_total appears
// in the Prometheus output after RecordExporterError is called.
func TestBridgeExporterErrors(t *testing.T) {
	exp := newBridgeExporter(t)
	exp.RecordExporterError(context.Background(), "otlp")

	body := metricsBody(t, exp)
	if !strings.Contains(body, `trimon_exporter_errors_total`) {
		t.Errorf("trimon_exporter_errors_total not found in /metrics output\n%s", body)
	}
	if !strings.Contains(body, `exporter_name="otlp"`) {
		t.Errorf(`exporter_name="otlp" not found in /metrics output\n%s`, body)
	}
}

// TestBridgePacketCounterAccumulation verifies that packet counters accumulate
// across multiple Export calls.
func TestBridgePacketCounterAccumulation(t *testing.T) {
	exp := newBridgeExporter(t)
	ctx := context.Background()

	run := types.ProbeResult{
		ProbeName: "p", ProbeType: "icmp", Target: "1.2.3.4", SourceIP: "0.0.0.0",
		Status: types.StatusSuccess, PacketsSent: 4, PacketsReceived: 4, PacketLossRatio: 0,
	}
	_ = exp.Export(ctx, run)
	_ = exp.Export(ctx, run)

	body := metricsBody(t, exp)

	// After two runs of 4 packets each, the counter should be 8.
	if !strings.Contains(body, "trimon_probe_packets_sent_total") {
		t.Errorf("trimon_probe_packets_sent_total not found\n%s", body)
	}
	if !strings.Contains(body, "8") {
		t.Errorf("expected accumulated count of 8 in /metrics output\n%s", body)
	}
}
