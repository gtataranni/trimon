package otlp

import "go.opentelemetry.io/otel/metric"

// InstrumentInfo describes one registered metric instrument. The exporter builds
// an ordered inventory of these as a side effect of registration, so any
// instrument — present or future — is captured at birth. Consumers (e.g. the
// metrics-doc generator) read the inventory instead of maintaining a parallel
// list by hand.
type InstrumentInfo struct {
	Name        string // OTel instrument name, e.g. "trimon.probe.rtt.min"
	Kind        string // OTel instrument kind, e.g. "Float64Gauge"
	Unit        string // OTel unit, e.g. "ms"; "" when none
	Description string // human-facing semantics; also feeds the Prometheus # HELP line
}

// Instruments returns the ordered inventory of every instrument the exporter
// registered, in registration order. The slice is a copy; callers may not
// mutate the exporter's state through it.
func (e *Exporter) Instruments() []InstrumentInfo {
	out := make([]InstrumentInfo, len(e.instruments))
	copy(out, e.instruments)
	return out
}

// recordingMeter wraps a metric.Meter so that every instrument created through
// it is appended to inv at creation time. It overrides only the instrument
// constructors trimon actually uses; all other Meter methods delegate to the
// embedded Meter. Because registration flows through this wrapper, it is
// structurally impossible to register an instrument without it appearing in the
// inventory.
type recordingMeter struct {
	metric.Meter
	inv *[]InstrumentInfo
}

func (m *recordingMeter) Float64Gauge(name string, options ...metric.Float64GaugeOption) (metric.Float64Gauge, error) {
	inst, err := m.Meter.Float64Gauge(name, options...)
	if err == nil {
		c := metric.NewFloat64GaugeConfig(options...)
		m.append(name, "Float64Gauge", c.Unit(), c.Description())
	}
	return inst, err
}

func (m *recordingMeter) Int64Gauge(name string, options ...metric.Int64GaugeOption) (metric.Int64Gauge, error) {
	inst, err := m.Meter.Int64Gauge(name, options...)
	if err == nil {
		c := metric.NewInt64GaugeConfig(options...)
		m.append(name, "Int64Gauge", c.Unit(), c.Description())
	}
	return inst, err
}

func (m *recordingMeter) Int64Counter(name string, options ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	inst, err := m.Meter.Int64Counter(name, options...)
	if err == nil {
		c := metric.NewInt64CounterConfig(options...)
		m.append(name, "Int64Counter", c.Unit(), c.Description())
	}
	return inst, err
}

func (m *recordingMeter) Int64ObservableGauge(name string, options ...metric.Int64ObservableGaugeOption) (metric.Int64ObservableGauge, error) {
	inst, err := m.Meter.Int64ObservableGauge(name, options...)
	if err == nil {
		c := metric.NewInt64ObservableGaugeConfig(options...)
		m.append(name, "Int64ObservableGauge", c.Unit(), c.Description())
	}
	return inst, err
}

func (m *recordingMeter) append(name, kind, unit, description string) {
	*m.inv = append(*m.inv, InstrumentInfo{Name: name, Kind: kind, Unit: unit, Description: description})
}
