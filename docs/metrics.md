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
| `trimon.probe.port_open` | Float64Gauge | — | `trimon_probe_port_open` | TCP & UDP; 1 open, 0 closed/no-reply, NaN for other probe types or error |
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

## `probe.port_open` semantics (TCP & UDP probes)

A Float64Gauge encoding port reachability, set by the TCP probe (both `connect` and
`syn` modes) and the UDP probe:

- `1` — port **open**: a TCP SYN/ACK (or completed connect handshake), or a matching UDP reply
- `0` — port **not open**: a TCP RST, a UDP ICMP port-unreachable, or no reply at all
- **`NaN`** — probe type without port semantics (ICMP, HTTP), or the probe could not run (status = error)

The operationally distinct states are recovered by **combining it with `probe.up`** —
deliberately, rather than encoding a 3-valued enum as an attribute. Per
[`probe.status` is not an attribute](#probestatus-is-not-an-attribute), trimon keeps
state in gauge *values*, not in mutable labels; this metric extends the existing
`probe.up`/`probe.success` binary-gauge idiom rather than introducing a `state=` label.

| `probe.up` | `port_open` | TCP meaning | UDP meaning |
|---|---|---|---|
| 1 | 1 | **OPEN** — SYN/ACK | **OPEN** — a (matching) reply |
| 1 | 0 | **CLOSED** — RST | **CLOSED** — ICMP port-unreachable |
| 0 | 0 | **FILTERED / DROPPED / DOWN** — no reply | **OPEN\|FILTERED / DOWN** — silence |
| 0 | 1 | impossible | impossible |

**A refused port is reachability, not loss.** network reachability is trimon's primary signal
and service availability a secondary one, so a host that actively refuses a port still counts as a
received reply: `probe.up = 1`, `packet_loss = 0`, status `success`, and `port_open = 0`
is what distinguishes a reachable closed port from an open one. For TCP this refusal is a RST
(connect-mode `ECONNREFUSED` is treated identically to a SYN-mode RST); for UDP it is an
ICMP port-unreachable, delivered as `ECONNREFUSED` on the connected socket. Only silence
(timeout) is packet loss.

**UDP's `open|filtered` ambiguity is inherent.** UDP has no handshake, so a quietly-open
service that never replies and a dropped/filtered packet are indistinguishable — both are
silence (`up = 0`, `port_open = 0`). UDP can therefore positively confirm only *open* (a
reply) or *closed* (ICMP unreachable); the third state is the union open|filtered, exactly
as in Nmap. Sending a protocol-appropriate `payload`/`expected_response` maximizes the
chance an open service answers, narrowing the ambiguity.

PromQL: open = `trimon_probe_port_open == 1`; closed = `trimon_probe_up == 1 and
trimon_probe_port_open == 0`; unreachable/filtered = `trimon_probe_up == 0`. Scope to one
protocol with the `probe_type` label, e.g. `trimon_probe_port_open{probe_type="udp"}`.

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
scope for current phases. See GH Milestones `v0.6.0` for the planned work.
