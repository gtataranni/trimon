// Package metricsdoc renders the trimon metrics reference table from the
// exporter's instrument inventory. It is the single source of truth for the
// generated table in docs/metrics.md: the generator (cmd/gen-metrics-docs)
// reads the live inventory, renders it here, and splices it into the doc.
package metricsdoc

import (
	"fmt"
	"sort"
	"strings"

	"github.com/prometheus/otlptranslator"

	"github.com/gtataranni/trimon/internal/exporter/otlp"
)

const (
	beginMarker = "<!-- BEGIN GENERATED: instruments -->"
	endMarker   = "<!-- END GENERATED: instruments -->"
)

// PrometheusName derives the Prometheus series name the OTel Prometheus bridge
// exposes for the given instrument. It applies the bridge's default translation
// strategy (UnderscoreEscapingWithSuffixes) so the rendered name stays faithful
// without running an HTTP scrape.
func PrometheusName(info otlp.InstrumentInfo) (string, error) {
	mt, err := metricType(info.Kind)
	if err != nil {
		return "", err
	}
	namer := otlptranslator.NewMetricNamer("", otlptranslator.UnderscoreEscapingWithSuffixes)
	name, err := namer.Build(otlptranslator.Metric{
		Name: info.Name,
		Unit: info.Unit,
		Type: mt,
	})
	if err != nil {
		return "", fmt.Errorf("derive prometheus name for %q: %w", info.Name, err)
	}
	return name, nil
}

// metricType maps an OTel instrument kind (as captured in the inventory) to the
// otlptranslator metric type that drives suffixing. Unknown kinds fail loudly so
// a newly introduced instrument type cannot silently produce a wrong name.
func metricType(kind string) (otlptranslator.MetricType, error) {
	switch kind {
	case "Int64Counter":
		return otlptranslator.MetricTypeMonotonicCounter, nil
	case "Float64Gauge", "Int64Gauge", "Int64ObservableGauge":
		return otlptranslator.MetricTypeGauge, nil
	default:
		return 0, fmt.Errorf("unknown instrument kind %q", kind)
	}
}

// RenderTable renders the full instrument inventory as a single flat, name-sorted
// Markdown table. The returned string ends with a trailing newline.
func RenderTable(inv []otlp.InstrumentInfo) (string, error) {
	rows := make([]otlp.InstrumentInfo, len(inv))
	copy(rows, inv)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

	var b strings.Builder
	b.WriteString("| OTel name | Kind | Unit | Prometheus name | Description |\n")
	b.WriteString("|-----------|------|------|-----------------|-------------|\n")
	for _, in := range rows {
		promName, err := PrometheusName(in)
		if err != nil {
			return "", err
		}
		unit := "—"
		if in.Unit != "" {
			unit = "`" + in.Unit + "`"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | `%s` | %s |\n",
			in.Name, in.Kind, unit, promName, in.Description)
	}
	return b.String(), nil
}

// Splice replaces the content between the generated-block markers in doc with
// table, preserving the surrounding prose and the markers themselves. It errors
// if either marker is missing or out of order.
func Splice(doc []byte, table string) ([]byte, error) {
	s := string(doc)
	bi := strings.Index(s, beginMarker)
	if bi < 0 {
		return nil, fmt.Errorf("begin marker %q not found", beginMarker)
	}
	ei := strings.Index(s, endMarker)
	if ei < 0 {
		return nil, fmt.Errorf("end marker %q not found", endMarker)
	}
	if ei < bi {
		return nil, fmt.Errorf("end marker precedes begin marker")
	}

	var b strings.Builder
	b.WriteString(s[:bi+len(beginMarker)])
	b.WriteString("\n")
	b.WriteString(table)
	b.WriteString(s[ei:])
	return []byte(b.String()), nil
}
