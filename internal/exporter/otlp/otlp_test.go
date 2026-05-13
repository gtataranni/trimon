package otlp

import (
	"context"
	"log/slog"
	"math"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

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
	if err := e.registerInstruments(meter); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	return e, reader
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

// findFloat64Gauge locates a named metric in ms and returns its single
// datapoint value and attribute set.  It fatals if the metric is absent or
// the data is not a Gauge[float64].
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
// and attribute set.  It fatals if absent or not a Gauge[int64].
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

// attrString retrieves a string attribute value from a Set, fataling when absent.
func attrString(t *testing.T, attrs attribute.Set, key string) string {
	t.Helper()
	v, ok := attrs.Value(attribute.Key(key))
	if !ok {
		t.Fatalf("attribute %q not found", key)
	}
	return v.AsString()
}

// ---- tests ------------------------------------------------------------------

// TestStatusSuccess verifies that a StatusSuccess result sets success=1,
// propagates RTT values, and packet_loss=0.
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

	sent, _ := findInt64Gauge(t, ms, "trimon.probe.packets_sent")
	if sent != 3 {
		t.Errorf("packets_sent: got %d, want 3", sent)
	}

	recv, _ := findInt64Gauge(t, ms, "trimon.probe.packets_received")
	if recv != 3 {
		t.Errorf("packets_received: got %d, want 3", recv)
	}
}

// TestStatusPartial verifies that a StatusPartial result sets success=0,
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

	rttMean, _ := findFloat64Gauge(t, ms, "trimon.probe.rtt.mean")
	if rttMean != 8.0 {
		t.Errorf("rtt.mean: got %f, want 8.0", rttMean)
	}

	loss, _ := findFloat64Gauge(t, ms, "trimon.probe.packet_loss")
	if loss != 0.33 {
		t.Errorf("packet_loss: got %f, want 0.33", loss)
	}
}

// TestStatusFailure verifies that a StatusFailure result sets success=0,
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

// TestStatusError verifies that a StatusError result sets success=0,
// packet_loss=NaN, and both RTT and packet count gauges are zero.
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

	sent, _ := findInt64Gauge(t, ms, "trimon.probe.packets_sent")
	if sent != 0 {
		t.Errorf("packets_sent: got %d, want 0", sent)
	}

	recv, _ := findInt64Gauge(t, ms, "trimon.probe.packets_received")
	if recv != 0 {
		t.Errorf("packets_received: got %d, want 0", recv)
	}
}

// TestRequiredAttributes verifies that every metric carries the five mandatory
// probe attributes on every status path.
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

	// Required attribute keys as defined in the spec.
	requiredKeys := []string{
		"probe.name",
		"probe.type",
		"probe.target",
		"probe.source_ip",
		"probe.status",
	}

	// Check against the packet_loss gauge which is always recorded.
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

			// Verify the values are correctly mapped from the ProbeResult fields.
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
			if got := attrString(t, attrs, "probe.status"); got != string(tc.result.Status) {
				t.Errorf("probe.status: got %q, want %q", got, tc.result.Status)
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

	// Mandatory attributes must still be present alongside user labels.
	for _, key := range []string{"probe.name", "probe.type", "probe.target", "probe.source_ip", "probe.status"} {
		if _, ok := attrs.Value(attribute.Key(key)); !ok {
			t.Errorf("mandatory attribute %q missing when user labels present", key)
		}
	}
}

// TestAllMetricsPresent verifies that every expected instrument name appears
// in the collected output after a single Export call.
func TestAllMetricsPresent(t *testing.T) {
	e, reader := newTestExporter(t)

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

	if err := e.Export(context.Background(), result); err != nil {
		t.Fatalf("Export: %v", err)
	}

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
	}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("expected metric %q not found in collected output", name)
		}
	}
}

// TestExportReturnsNilError confirms that Export always returns nil for all
// valid status values (the OTel SDK records synchronously and never errors for
// gauge instruments).
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
