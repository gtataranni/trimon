# trimon metrics design

This document records the design decisions behind trimon's metric instrumentation.
Read this before adding, removing, or renaming instruments.

---

## Architecture: unified OTel SDK

trimon uses a single OTel SDK `MeterProvider` with two readers:

| Reader | When active | Purpose |
|--------|-------------|---------|
| Prometheus bridge (`go.opentelemetry.io/otel/exporters/prometheus`) | Always | Serves `/metrics` in Prometheus text format |
| OTLP periodic reader | When `exporters.otlp.enabled: true` | Pushes to OTel Collector via gRPC or HTTP |

All instruments are defined once in `internal/exporter/otlp/otlp.go`. There is no
separate Prometheus client or second instrument set.

---

## Instrument inventory

All instruments use the prefix `trimon.` and the scope `github.com/gtataranni/trimon`.

### Probe result instruments

Recorded on every `Export()` call. Attributes: `probe.name`, `probe.type`,
`probe.target`, `probe.source_ip`, plus user-defined labels from the probe config.

| OTel name | Type | Unit | Prometheus name | Notes |
|-----------|------|------|-----------------|-------|
| `trimon.probe.rtt.min` | Float64Gauge | `ms` | `trimon_probe_rtt_min_milliseconds` | ICMP only; NaN on failure/error and for HTTP probes |
| `trimon.probe.rtt.mean` | Float64Gauge | `ms` | `trimon_probe_rtt_mean_milliseconds` | ICMP only; NaN on failure/error and for HTTP probes |
| `trimon.probe.rtt.max` | Float64Gauge | `ms` | `trimon_probe_rtt_max_milliseconds` | ICMP only; NaN on failure/error and for HTTP probes |
| `trimon.probe.rtt.stddev` | Float64Gauge | `ms` | `trimon_probe_rtt_stddev_milliseconds` | ICMP only; NaN on failure/error and for HTTP probes |
| `trimon.probe.duration` | Float64Gauge | `ms` | `trimon_probe_duration_milliseconds` | HTTP only; wall-clock from request start to body drain; NaN when no response received or for non-HTTP probes |
| `trimon.probe.packet_loss` | Float64Gauge | `ratio` | `trimon_probe_packet_loss_ratio` | 1.0 on failure, NaN on error |
| `trimon.probe.packets_sent` | Int64Counter | `{packets}` | `trimon_probe_packets_sent_total` | not incremented on error |
| `trimon.probe.packets_received` | Int64Counter | `{packets}` | `trimon_probe_packets_received_total` | not incremented on error |
| `trimon.probe.success` | Int64Gauge | — | `trimon_probe_success` | 1 only if status=success (all packets replied) |
| `trimon.probe.up` | Int64Gauge | — | `trimon_probe_up` | 1 if status=success or partial (≥1 reply) |

RTT gauges emit `NaN` on `failure` and `error` — rather than `0` or retaining the last value — because 0ms is physically impossible and would corrupt latency alerting thresholds, while NaN causes Grafana to break the graph line, making the absence of measurement visually unambiguous; this mirrors the OTel ecosystem convention and is analogous to how `probe.packet_loss` uses NaN on `error`.

**Why RTT and duration are separate instruments:** ICMP's min/mean/max/stddev describe network jitter across N packets (a statistical distribution). HTTP always sends one request per tick; repeating requests 2–N would reuse the TCP connection and produce a bimodal distribution that makes mean and stddev misleading. A single `duration` (DNS + TCP + TLS + TTFB + body) is the canonical HTTP latency measure, matching the approach of blackbox_exporter, Datadog Synthetics, and Checkly.

**`probe.success` vs `probe.up`:** intentional semantic difference.
`probe.up` is the alerting signal (`ALERT IF probe_up == 0`); it is 1 for any partial
reply so that intermittent ICMP filtering does not fire a page. `probe.success` is a
strict signal — 1 only when every sent packet received a reply.

### Self-observability instruments

| OTel name | Type | Unit | Prometheus name | Attributes |
|-----------|------|------|-----------------|------------|
| `trimon.probe.runs` | Int64Counter | `{runs}` | `trimon_probe_runs_total` | `probe.name` |
| `trimon.probe.errors` | Int64Counter | `{errors}` | `trimon_probe_errors_total` | `probe.name`, `error.type` |
| `trimon.build.info` | Int64ObservableGauge | — | `trimon_build_info` | `version`, `commit`, `goversion` |
| `trimon.scheduler.goroutines` | Int64ObservableGauge | `{goroutines}` | `trimon_scheduler_goroutines` | — |
| `trimon.config.reloads` | Int64Counter | `{reloads}` | `trimon_config_reloads_total` | — |

---

## `probe.status` is not an attribute

`probe.status` is intentionally **not** attached to any instrument.

The Prometheus data model is designed around labels that identify the *identity* of
what is being measured — which probe, which target, which type. Labels are not meant
to carry the *state* of that entity. When a label's value changes over time (a
"mutable label"), Prometheus does not overwrite the old series; each unique label set
is an independent time series. For Gauges, the old series keeps its last value until
the staleness window expires (~5 minutes by default). This creates **overlapping time
series**: contradictory values for the same logical entity coexist in the database,
and any `max()` or dashboard panel that aggregates over both will produce incorrect
results.

Using `probe.status` as a label would also add a cardinality dimension with no query
value: you would never aggregate `probe_up{status="success"}` across probes — it is
meaningless to sum successes and failures together. The status is fully encoded in the
gauge values (`probe.up`, `probe.success`, `probe.packet_loss`, NaN for error) without
needing a separate label dimension.

---

## `probe.up` semantics

`probe.up = 1` if at least one probe packet received a reply in the last run; 0 otherwise.

| status | probe.up |
|--------|----------|
| `success` (0% loss) | 1 |
| `partial` (0% < loss < 100%) | 1 |
| `failure` (100% loss) | 0 |
| `error` (probe could not run) | 0 |

---

## `probe.packet_loss` semantics

A Float64Gauge in `[0.0, 1.0]` representing the fraction of packets with no reply in
the last completed probe run.

- `0.0` — all packets replied (status = success)
- `0.0 < v < 1.0` — partial loss (status = partial)
- `1.0` — total loss (status = failure)
- **`NaN`** — probe could not run (status = error)

NaN makes the undefined state explicit. `1.0` on error would collapse "target
unreachable" and "trimon internal failure" into the same value. Prometheus, VictoriaMetrics,
and Grafana Mimir handle NaN natively.

---

## Why `probe.runs` and `probe.errors` use only `probe.name`

RTT and loss instruments use the full probe attribute set (`probe.name`, `probe.type`,
`probe.target`, `probe.source_ip`, user labels) because slicing by target or type is
useful for those measurements. Operational counters (`runs`, `errors`) are summarised
per probe name only — adding cardinality dimensions there provides no diagnostic value
and bloats the time-series count.

---

## Trends and advanced metrics

Recording rules, long-window loss rates, and probe result history buffering are out of
scope for current phases. See [ROADMAP.md](../ROADMAP.md) Phase 6 for the planned work.
