# ADR-0002: `probe.status` is encoded in gauge values, never as a label

- **Status:** accepted
- **Date:** 2026-07-20

## Context

Each probe run produces a status: `success`, `partial`, `failure`, or `error`. The
tempting shortcut is to attach `probe.status` as a metric attribute/label. The Prometheus
data model is built around labels that identify *what* is measured (which probe, target,
type) — not the mutable *state* of that entity.

When a label value changes over time, Prometheus does not overwrite the old series; each
unique label set is an independent time series. For gauges the old series keeps its last
value until the staleness window expires (~5 min). A `probe.status` label therefore
creates **overlapping, contradictory series** for one logical entity, and any `max()` or
dashboard aggregation over them yields wrong results. It also adds a cardinality dimension
with no query value — nobody aggregates `probe_up{status="success"}` across probes.

## Decision

`probe.status` is **not** attached to any instrument. State is encoded entirely in gauge
*values*: `probe.up`, `probe.success`, `probe.packet_loss` (with `NaN` for error), and
`probe.port_open`.

## Consequences

- No mutable-label staleness or overlapping series; aggregations stay correct.
- Status must be recovered by combining gauge values (see ADR-0005, ADR-0006), which is a
  deliberate trade for a clean data model.
- Any new per-run state must extend the binary-gauge idiom, not introduce a `state=` label.
