package metricsdoc

import (
	"strings"
	"testing"

	"github.com/gtataranni/trimon/internal/exporter/otlp"
)

func TestPrometheusName(t *testing.T) {
	tests := []struct {
		name string
		in   otlp.InstrumentInfo
		want string
	}{
		{
			name: "gauge with ms unit gets unit suffix",
			in:   otlp.InstrumentInfo{Name: "trimon.probe.rtt.min", Kind: "Float64Gauge", Unit: "ms"},
			want: "trimon_probe_rtt_min_milliseconds",
		},
		{
			name: "gauge with ratio unit",
			in:   otlp.InstrumentInfo{Name: "trimon.probe.packet_loss", Kind: "Float64Gauge", Unit: "ratio"},
			want: "trimon_probe_packet_loss_ratio",
		},
		{
			name: "unitless gauge",
			in:   otlp.InstrumentInfo{Name: "trimon.probe.port_open", Kind: "Float64Gauge"},
			want: "trimon_probe_port_open",
		},
		{
			name: "int gauge",
			in:   otlp.InstrumentInfo{Name: "trimon.probe.up", Kind: "Int64Gauge"},
			want: "trimon_probe_up",
		},
		{
			name: "counter gets total suffix",
			in:   otlp.InstrumentInfo{Name: "trimon.probe.packets_sent", Kind: "Int64Counter", Unit: "{packets}"},
			want: "trimon_probe_packets_sent_total",
		},
		{
			name: "observable gauge",
			in:   otlp.InstrumentInfo{Name: "trimon.build.info", Kind: "Int64ObservableGauge"},
			want: "trimon_build_info",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PrometheusName(tt.in)
			if err != nil {
				t.Fatalf("PrometheusName: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPrometheusNameUnknownKind(t *testing.T) {
	_, err := PrometheusName(otlp.InstrumentInfo{Name: "x", Kind: "Bogus"})
	if err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

func TestRenderTable(t *testing.T) {
	inv := []otlp.InstrumentInfo{
		{Name: "trimon.probe.up", Kind: "Int64Gauge", Description: "up gauge"},
		{Name: "trimon.probe.packets_sent", Kind: "Int64Counter", Unit: "{packets}", Description: "sent counter"},
		{Name: "trimon.probe.rtt.min", Kind: "Float64Gauge", Unit: "ms", Description: "min rtt"},
	}
	got, err := RenderTable(inv)
	if err != nil {
		t.Fatalf("RenderTable: %v", err)
	}

	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 5 { // header + separator + 3 rows
		t.Fatalf("expected 5 lines, got %d:\n%s", len(lines), got)
	}
	if !strings.HasPrefix(lines[0], "| OTel name | Kind | Unit | Prometheus name | Description |") {
		t.Errorf("unexpected header: %q", lines[0])
	}

	// Rows are sorted by OTel name.
	wantOrder := []string{"trimon.probe.packets_sent", "trimon.probe.rtt.min", "trimon.probe.up"}
	for i, name := range wantOrder {
		row := lines[2+i]
		if !strings.Contains(row, "`"+name+"`") {
			t.Errorf("row %d: expected name %q, got %q", i, name, row)
		}
	}

	// Unitless rendering and derived Prometheus names.
	if !strings.Contains(got, "`trimon_probe_packets_sent_total`") {
		t.Errorf("missing derived counter name:\n%s", got)
	}
	if !strings.Contains(lines[4], "| — |") { // trimon.probe.up has no unit
		t.Errorf("expected em-dash unit for unitless instrument, got %q", lines[4])
	}
}

func TestSplice(t *testing.T) {
	doc := []byte("intro\n" + beginMarker + "\nOLD TABLE\n" + endMarker + "\noutro\n")
	table := "| a |\n| - |\n| 1 |\n"

	out, err := Splice(doc, table)
	if err != nil {
		t.Fatalf("Splice: %v", err)
	}
	want := "intro\n" + beginMarker + "\n" + table + endMarker + "\noutro\n"
	if string(out) != want {
		t.Errorf("got:\n%q\nwant:\n%q", out, want)
	}

	// Idempotent: splicing the same table again is a no-op.
	out2, err := Splice(out, table)
	if err != nil {
		t.Fatalf("Splice (2nd): %v", err)
	}
	if string(out2) != string(out) {
		t.Errorf("splice not idempotent:\ngot:\n%q\nwant:\n%q", out2, out)
	}
}

func TestSpliceMissingMarker(t *testing.T) {
	if _, err := Splice([]byte("no markers here"), "table"); err == nil {
		t.Fatal("expected error when markers absent")
	}
}
