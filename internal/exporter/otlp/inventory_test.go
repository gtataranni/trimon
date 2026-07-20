package otlp

import (
	"context"
	"strings"
	"testing"

	"github.com/gtataranni/trimon/pkg/types"
)

// TestInstrumentInventory asserts the exporter reports a non-empty inventory and
// that every registered instrument carries a name, kind, and non-empty
// description. A future instrument added without a description fails here.
func TestInstrumentInventory(t *testing.T) {
	e, _ := newTestExporter(t)

	inv := e.Instruments()
	if len(inv) == 0 {
		t.Fatal("instrument inventory is empty")
	}

	for _, i := range inv {
		if i.Name == "" {
			t.Errorf("inventory entry with empty name: %+v", i)
		}
		if i.Kind == "" {
			t.Errorf("instrument %q: empty kind", i.Name)
		}
		if i.Description == "" {
			t.Errorf("instrument %q: empty description", i.Name)
		}
	}
}

// TestMetricsHaveHelp verifies the Prometheus bridge emits a # HELP line for
// every trimon_* series once instruments carry descriptions.
func TestMetricsHaveHelp(t *testing.T) {
	exp := newBridgeExporter(t)
	exp.SetGoroutinesGetter(func() int { return 0 })

	// Drive at least one observation on every result instrument so each series
	// appears in the scrape below.
	ctx := context.Background()
	open := true
	results := []types.ProbeResult{
		{ProbeName: "p", ProbeType: types.ProbeTypeICMP, Target: "10.0.0.1",
			PacketsSent: 1, PacketsReceived: 1, Status: types.StatusSuccess},
		{ProbeName: "p", ProbeType: types.ProbeTypeTCP, Target: "10.0.0.1",
			PacketsSent: 1, PacketsReceived: 1, PortOpen: &open, Status: types.StatusSuccess},
		{ProbeName: "h", ProbeType: types.ProbeTypeHTTP, Target: "10.0.0.1",
			PacketsSent: 1, PacketsReceived: 1, DurationMS: 12, Status: types.StatusSuccess},
		{ProbeName: "p", ProbeType: types.ProbeTypeICMP, Target: "10.0.0.1",
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

	body := metricsBody(t, exp)

	help := make(map[string]struct{})
	series := make(map[string]struct{})
	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(line, "# HELP "):
			rest := strings.TrimPrefix(line, "# HELP ")
			if i := strings.IndexByte(rest, ' '); i >= 0 {
				help[rest[:i]] = struct{}{}
			}
		case line == "" || strings.HasPrefix(line, "#"):
			// skip other comment/blank lines
		default:
			name := line
			if i := strings.IndexAny(name, "{ "); i >= 0 {
				name = name[:i]
			}
			if strings.HasPrefix(name, "trimon_") && !strings.HasSuffix(name, "_created") {
				series[name] = struct{}{}
			}
		}
	}

	if len(series) == 0 {
		t.Fatal("no trimon_ series present in scrape")
	}
	for name := range series {
		if _, ok := help[name]; !ok {
			t.Errorf("series %q has no # HELP line", name)
		}
	}
}
