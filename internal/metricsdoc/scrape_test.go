package metricsdoc

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gtataranni/trimon/internal/config"
	"github.com/gtataranni/trimon/internal/exporter/otlp"
	"github.com/gtataranni/trimon/pkg/types"
)

// TestPrometheusNameMatchesScrape guards faithfulness: every Prometheus name we
// derive for an inventory instrument must actually appear in a real /metrics
// scrape from the OTel Prometheus bridge. If the bridge's translation strategy
// ever diverges from PrometheusName, this fails loudly.
func TestPrometheusNameMatchesScrape(t *testing.T) {
	exp, err := otlp.New(context.Background(), config.OTLPExporterConfig{Enabled: false},
		"test-version", "abc1234", slog.Default())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = exp.Close() })
	exp.SetGoroutinesGetter(func() int { return 0 })

	// Drive an observation on every probe-result instrument plus the
	// self-observability counters so each series appears in the scrape.
	ctx := context.Background()
	open := true
	results := []types.ProbeResult{
		{ProbeName: "p", ProbeType: types.ProbeTypeICMP, Target: "10.0.0.1",
			PacketsSent: 1, PacketsReceived: 1, Status: types.StatusSuccess},
		{ProbeName: "t", ProbeType: types.ProbeTypeTCP, Target: "10.0.0.1",
			PacketsSent: 1, PacketsReceived: 1, PortOpen: &open, Status: types.StatusSuccess},
		{ProbeName: "h", ProbeType: types.ProbeTypeHTTP, Target: "10.0.0.1",
			PacketsSent: 1, PacketsReceived: 1, DurationMS: 12, Status: types.StatusSuccess},
		{ProbeName: "e", ProbeType: types.ProbeTypeICMP, Target: "10.0.0.1",
			Status: types.StatusError, ErrorType: "socket"},
	}
	for _, r := range results {
		if err := exp.Export(ctx, r); err != nil {
			t.Fatalf("export: %v", err)
		}
	}
	exp.RecordConfigReload(ctx)
	exp.RecordDroppedResult(ctx, "p")
	exp.RecordExporterError(ctx, "otlp")

	series := scrapeSeries(t, exp)

	for _, in := range exp.Instruments() {
		want, err := PrometheusName(in)
		if err != nil {
			t.Fatalf("PrometheusName(%q): %v", in.Name, err)
		}
		if _, ok := series[want]; !ok {
			t.Errorf("instrument %q: derived name %q not present in scrape", in.Name, want)
		}
	}
}

// scrapeSeries returns the set of trimon_* series names present in a /metrics
// scrape, ignoring the synthetic _created series the bridge adds for counters.
func scrapeSeries(t *testing.T, exp *otlp.Exporter) map[string]struct{} {
	t.Helper()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rr := httptest.NewRecorder()
	exp.PrometheusHandler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("/metrics: want 200, got %d", rr.Code)
	}

	series := make(map[string]struct{})
	for _, line := range strings.Split(rr.Body.String(), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name := line
		if i := strings.IndexAny(name, "{ "); i >= 0 {
			name = name[:i]
		}
		if !strings.HasPrefix(name, "trimon_") || strings.HasSuffix(name, "_created") {
			continue
		}
		series[name] = struct{}{}
	}
	return series
}
