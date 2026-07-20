# ADR-0003: Unmeasured RTT and loss emit NaN, not 0

- **Status:** accepted
- **Date:** 2026-07-20

## Context

When a probe cannot measure a value — RTT on `failure`/`error`, or `packet_loss` on
`error` — the instrument still has to emit *something*. The candidates are `0`, retaining
the last value, or `NaN`. A `0ms` RTT is physically impossible and would corrupt latency
alerting thresholds; retaining the last value hides that the measurement stopped. Encoding
`1.0` loss on `error` would collapse "target unreachable" and "trimon internal failure"
into the same value.

## Decision

RTT gauges emit `NaN` on `failure` and `error`. `probe.packet_loss` emits `NaN` on
`error` (and `1.0` on `failure`, which is a real measurement — 100% loss). NaN marks the
undefined state explicitly.

## Consequences

- Grafana breaks the graph line on NaN, making absence of measurement visually
  unambiguous; this matches OTel-ecosystem convention.
- Prometheus, VictoriaMetrics, and Grafana Mimir handle NaN natively.
- Alerting must use `probe.up` / `probe.success`, not RTT thresholds, to detect down
  targets — RTT is NaN, not a large number, when a target is down.
