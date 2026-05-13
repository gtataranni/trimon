# trimon metrics design

This document records the design decisions behind trimon's Prometheus `/metrics` endpoint.
Read this before adding, removing, or renaming instruments.

---

## Metric surfaces

trimon exposes measurement data in two places with different purposes:

| Surface | Where | Content |
|---|---|---|
| `/metrics` (Prometheus text) | HTTP server | Probe result gauges/counters + trimon operational metrics |
| Exporter pipeline | stdout / OTLP | Full probe results — RTT distributions, all fields, user labels |

The `/metrics` endpoint carries **two categories** of instruments:

### 1. Probe result metrics

These are the actual measurements trimon produces. Packet loss and reachability are the
core output of trimon.

```
trimon_probe_up{probe_name}                       gauge, 0.0 or 1.0
trimon_probe_packet_loss_ratio{probe_name}        gauge, 0.0–1.0 or NaN
trimon_probe_packets_sent_total{probe_name}       counter
trimon_probe_packets_received_total{probe_name}   counter
```

### 2. Operational self-observability

These describe trimon's own behavior — scheduler health, run throughput, errors, build
metadata.

```
trimon_build_info{version, commit, goversion}     gauge, value=1
trimon_probe_runs_total{probe_name}               counter
trimon_probe_errors_total{probe_name,error_type}  counter
trimon_scheduler_goroutines                       gauge
trimon_config_reload_total                        counter
```

The full probe result data (RTT stats, all packet fields, user labels) flows through the
Exporter pipeline. `/metrics` carries only the instruments above.
See [OTLP Exporter Metrics](#otlp-exporter-metrics) for the Exporter pipeline spec.

The distinction is not always obvious, and there may be overlap.
Contributors are welcome to suggest changes.

---

## OTLP exporter metrics (Exporter pipeline — Phase 2+)

All instruments are synchronous gauges recorded once per `Export()` call. Each data point
carries the attributes below — `probe.status` is an OTLP *attribute*, not a Prometheus
label, so cardinality is not a concern here.

| OTel metric name                | Unit        | Source field            | When `status=error` |
|---------------------------------|-------------|-------------------------|---------------------|
| `trimon.probe.rtt.min`          | `ms`        | `RTTMinMS`              | `0`                 |
| `trimon.probe.rtt.mean`         | `ms`        | `RTTMeanMS`             | `0`                 |
| `trimon.probe.rtt.max`          | `ms`        | `RTTMaxMS`              | `0`                 |
| `trimon.probe.rtt.stddev`       | `ms`        | `RTTStddevMS`           | `0`                 |
| `trimon.probe.packet_loss`      | `ratio`     | `PacketLossRatio`       | `1.0`               |
| `trimon.probe.packets_sent`     | `{packets}` | `PacketsSent`           | `0`                 |
| `trimon.probe.packets_received` | `{packets}` | `PacketsReceived`       | `0`                 |
| `trimon.probe.success`          | `1\|0`      | `1` if status=success   | `0`                 |

**Attributes on every data point:** `probe.name`, `probe.type`, `probe.target`,
`probe.source_ip`, `probe.status`, plus user-defined labels from config.

**Resource attributes:** `service.name="trimon"`, `service.version`, `host.name`.

### Intentional differences from the Prometheus surface

| Dimension | Prometheus `/metrics` | OTLP |
|---|---|---|
| `packet_loss` on error | `NaN` | `1.0` |
| "up" signal | `trimon_probe_up` = 1 if loss < 100% (includes partial) | `trimon.probe.success` = 1 only if status = success |

**`packet_loss` on error:** OTLP uses `1.0` because `probe.status=error` is present as an
attribute on every data point, so consumers can always filter the error case out. On the
Prometheus surface there is no such per-datapoint attribute, so `NaN` is used to make the
undefined state explicit and avoid conflating network loss with probe execution failure.

**`probe.success` vs `probe_up`:** The OTLP `probe.success` gauge is a strict
signal — it is `1` only when all packets replied. `trimon_probe_up` on the Prometheus
surface uses a looser definition (at least one reply) to produce a more useful alerting
signal for ICMP, where partial loss is a real and normal operational state. Both are
intentional; they serve different consumers.

---

## Design decisions

### Why `trimon_probe_runs_total` has no `status` label

A `status` label on a counter multiplies cardinality by the number of distinct status
values and makes simple sum queries awkward. More importantly, `success`/`partial`/`failure`
are different points on a continuous loss ratio — embedding them as counter labels encodes
trimon's threshold interpretation into the metric name, which couples monitoring consumers
to trimon internals.

Status is instead exposed as two continuous instruments: `trimon_probe_up` (health signal)
and `trimon_probe_packet_loss_ratio` (the actual measurement).

### `trimon_probe_up` semantics

`probe_up` is a binary health signal intended for alerting (`ALERT IF probe_up == 0`).

**Definition:** `probe_up = 1` if at least one probe packet received a reply in the last
run; `probe_up = 0` otherwise.

| status | probe_up |
|---|---|
| `success` (0% loss) | 1 |
| `partial` (0% < loss < 100%) | 1 |
| `failure` (100% loss) | 0 |
| `error` (probe could not run) | 0 |

**Protocol notes:**

- **ICMP:** `probe_up = 1` iff `PacketsReceived > 0`. Trimon cannot distinguish "target
  down" from "network partition" from "ICMP filtered" — missing echo replies are the only
  observable fact. `probe_up = 0` therefore means "no replies received or probe errored",
  not "target is down".

- **Future protocols (HTTP, TCP, DNS):** `probe_up` semantics will be defined
  per-protocol in this document as probers are added. HTTP, for example, can check
  response code ranges. The definition will always be documented here before code ships.

### `trimon_probe_packet_loss_ratio` semantics

A gauge in the range `[0.0, 1.0]` representing the fraction of packets that received no
reply in the last completed probe run.

- `0.0` — all packets replied (status = success)
- `0.0 < v < 1.0` — partial loss (status = partial)
- `1.0` — total loss (status = failure); trimon cannot distinguish host down from
  network-level drop for ICMP
- **`NaN`** — probe could not run (status = error); the gauge is not updated, making it
  `NaN` (or stale if never set)

**Why NaN on error, not 1.0:**  
Setting the gauge to `1.0` on error would collapse "target unreachable" (network loss) and
"probe errored" (trimon internal failure) into the same value, making it impossible to
distinguish them without checking `trimon_probe_errors_total`. NaN makes the undefined
state explicit. Consumers that need to handle the error case can filter with
`and trimon_probe_errors_total == 0` or use `trimon_probe_errors_total` directly.

PromQL tip: use `trimon_probe_packet_loss_ratio or on(probe_name) (trimon_probe_errors_total * NaN)`
to keep the metric visible in dashboards even during error windows if needed.

### `trimon_probe_packets_sent_total` and `trimon_probe_packets_received_total`

Cumulative counters incremented by the actual packet counts from each probe run. These
complement `trimon_probe_packet_loss_ratio` (which is a gauge of the last run only) by
making loss rate queryable over time:

```promql
1 - rate(trimon_probe_packets_received_total[5m])
    /
    rate(trimon_probe_packets_sent_total[5m])
```

On `status = error`, neither counter is incremented (no packets were sent).

### `trimon_probe_errors_total` label: `error_type`

The `error_type` label carries the category of execution failure (e.g. `probe_error`,
`socket_error`). It is the only label on an error counter because it carries genuine
diagnostic value — knowing *why* trimon failed to run a probe is actionable. Status-as-label
on run counters does not meet this bar.

---

---

## OTLP Exporter Metrics

These instruments are recorded through the Exporter pipeline (`internal/exporter/otlp`)
and shipped to an OTel Collector via gRPC or HTTP. All are synchronous gauges recorded per
`Export()` call; the OTel SDK periodic reader flushes them to the collector on its own
schedule.

| OTel metric name | Unit | Source field | `status=failure` | `status=error` |
|---|---|---|---|---|
| `trimon.probe.rtt.min` | `ms` | `RTTMinMS` | `0` | `0` |
| `trimon.probe.rtt.mean` | `ms` | `RTTMeanMS` | `0` | `0` |
| `trimon.probe.rtt.max` | `ms` | `RTTMaxMS` | `0` | `0` |
| `trimon.probe.rtt.stddev` | `ms` | `RTTStddevMS` | `0` | `0` |
| `trimon.probe.packet_loss` | `ratio` | `PacketLossRatio` | `1.0` | `NaN` |
| `trimon.probe.packets_sent` | `{packets}` | `PacketsSent` | `0` | `0` |
| `trimon.probe.packets_received` | `{packets}` | `PacketsReceived` | `0` | `0` |
| `trimon.probe.success` | *(none)* | `1` if success, `0` otherwise | `0` | `0` |

**Attributes on every data point:** `probe.name`, `probe.type`, `probe.target`,
`probe.source_ip`, `probe.status`, plus user-defined labels from the probe config.

Note: `probe.status` is an OTel *attribute* (a dimension on a data point), not a
Prometheus label. Cardinality impact is negligible and it carries diagnostic value for
trace/metric correlation — intentionally different from the `/metrics` surface where the
status label was dropped from counters.

**Resource attributes:** `service.name="trimon"`, `service.version`, `host.name`.

### NaN alignment between /metrics and OTLP

Both surfaces record `NaN` for `packet_loss` / `trimon_probe_packet_loss_ratio` when
`status=error`. This is a deliberate decision to use a single rule regardless of transport.

The OTel metrics data model uses IEEE 754 `double` (float64), so NaN is representable.
The OTel specification notes that special float values (NaN, Inf) are "not recommended"
but does not prohibit them. The `probe.status=error` attribute is always present and can
be used to filter or split error data points in any backend that does not handle NaN well.

Backend compatibility: Prometheus, VictoriaMetrics, and Grafana Mimir handle NaN
natively. Some commercial SaaS providers do not. Test your pipeline if you rely on NaN for
alerting.

---

## Trends and advanced metrics

Recording rules, long-window loss rates, and probe result history buffering are out of
scope for current phases. See [ROADMAP.md](../ROADMAP.md) Phase 6 for the planned work.
