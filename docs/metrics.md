# trimon metrics reference

Instrument names, types, and semantics. The **source of truth** for what exists is the
instrument definitions in `internal/exporter/otlp/otlp.go`; the instrument table below is
**generated** from them — run `make gen-docs` to refresh it, do not edit it by hand. The
**rationale** for the design choices lives in the [ADRs](adr/) linked below — read those
before changing how a metric behaves.

Read this before adding, removing, or renaming instruments.

---

## Architecture

A single OTel SDK `MeterProvider` drives both `/metrics` (via the Prometheus bridge) and
optional OTLP push. All instruments are defined once in `internal/exporter/otlp/otlp.go`.
Rationale: [ADR-0001](adr/0001-unified-otel-meterprovider.md).

All instruments use the prefix `trimon.` and the scope `github.com/gtataranni/trimon`.

### Instruments

The table below is **generated** from the instrument definitions in
`internal/exporter/otlp/otlp.go` — run `make gen-docs` to refresh it. Do not edit it by
hand; CI fails if it drifts from the code.

Probe-result instruments (recorded on every `Export()` call) carry the attributes
`probe.name`, `probe.type`, `probe.target`, `probe.source_ip`, plus user-defined labels
from the probe config. Self-observability instruments carry only the attributes noted in
their descriptions.

<!-- BEGIN GENERATED: instruments -->
| OTel name | Kind | Unit | Prometheus name | Description |
|-----------|------|------|-----------------|-------------|
| `trimon.build.info` | Int64ObservableGauge | — | `trimon_build_info` | Build metadata as labels (version, commit, goversion); value is always 1 |
| `trimon.config.reloads` | Int64Counter | `{reloads}` | `trimon_config_reloads_total` | Total configuration reloads |
| `trimon.exporter.errors` | Int64Counter | `{errors}` | `trimon_exporter_errors_total` | Exporter failures to ship a result, labelled by exporter.name |
| `trimon.probe.duration` | Float64Gauge | `ms` | `trimon_probe_duration_milliseconds` | HTTP wall-clock duration from request start to body drain; NaN when no response received or for non-HTTP probes |
| `trimon.probe.errors` | Int64Counter | `{errors}` | `trimon_probe_errors_total` | Total probe errors, labelled by probe.name and error.type |
| `trimon.probe.packet_loss` | Float64Gauge | `ratio` | `trimon_probe_packet_loss_ratio` | Fraction of packets with no reply in the last run; 1.0 on failure, NaN on error |
| `trimon.probe.packets_received` | Int64Counter | `{packets}` | `trimon_probe_packets_received_total` | Packets received per probe run; not incremented on error |
| `trimon.probe.packets_sent` | Int64Counter | `{packets}` | `trimon_probe_packets_sent_total` | Packets sent per probe run; not incremented on error |
| `trimon.probe.port_open` | Float64Gauge | — | `trimon_probe_port_open` | TCP & UDP port reachability; 1 open, 0 closed/no-reply, NaN for other probe types or error |
| `trimon.probe.results_dropped` | Int64Counter | `{results}` | `trimon_probe_results_dropped_total` | Results dropped when the pipeline buffer is full, labelled by probe.name |
| `trimon.probe.rtt.max` | Float64Gauge | `ms` | `trimon_probe_rtt_max_milliseconds` | Maximum ICMP round-trip time over the last probe run; NaN on failure/error and for HTTP probes |
| `trimon.probe.rtt.mean` | Float64Gauge | `ms` | `trimon_probe_rtt_mean_milliseconds` | Mean ICMP round-trip time over the last probe run; NaN on failure/error and for HTTP probes |
| `trimon.probe.rtt.min` | Float64Gauge | `ms` | `trimon_probe_rtt_min_milliseconds` | Minimum ICMP round-trip time over the last probe run; NaN on failure/error and for HTTP probes |
| `trimon.probe.rtt.stddev` | Float64Gauge | `ms` | `trimon_probe_rtt_stddev_milliseconds` | Standard deviation of ICMP round-trip time over the last probe run; NaN on failure/error and for HTTP probes |
| `trimon.probe.runs` | Int64Counter | `{runs}` | `trimon_probe_runs_total` | Total probe runs, labelled by probe.name |
| `trimon.probe.success` | Int64Gauge | — | `trimon_probe_success` | 1 only when status=success (all packets replied), else 0 |
| `trimon.probe.up` | Int64Gauge | — | `trimon_probe_up` | 1 when status=success or partial (at least one reply), else 0 |
| `trimon.scheduler.goroutines` | Int64ObservableGauge | `{goroutines}` | `trimon_scheduler_goroutines` | Live scheduler goroutine count, observed on each scrape |
<!-- END GENERATED: instruments -->

Operational counters (`runs`, `errors`, `results_dropped`) carry only `probe.name` — no
target/type dimensions, which would add cardinality with no diagnostic value.

---

## Semantics — summary + rationale

Behaviour is summarised here; the *why* is in the ADRs.

- **`probe.status` is never a metric attribute.** State is encoded in gauge values, not
  mutable labels. → [ADR-0002](adr/0002-status-not-a-label.md)
- **RTT gauges and `packet_loss` emit NaN, not 0, when unmeasured** (RTT on
  failure/error; loss on error). → [ADR-0003](adr/0003-nan-not-zero-for-unmeasured.md)
- **ICMP has RTT stats; HTTP has a single `duration`.**
  → [ADR-0004](adr/0004-rtt-stats-vs-http-duration.md)
- **`probe.up` (≥1 reply, the alerting signal) vs `probe.success` (all replied, strict).**
  → [ADR-0005](adr/0005-up-vs-success.md)
- **`port_open` + `up` encode open / closed / filtered for TCP & UDP.** A refused port is
  reachability, not loss. → [ADR-0006](adr/0006-port-open-encoding.md)

### `probe.up` values

| status | probe.up |
|--------|----------|
| `success` (0% loss) | 1 |
| `partial` (0% < loss < 100%) | 1 |
| `failure` (100% loss) | 0 |
| `error` (probe could not run) | 0 |

### `probe.packet_loss` values

A Float64Gauge in `[0.0, 1.0]` — the fraction of packets with no reply in the last run:
`0.0` success, `0.0 < v < 1.0` partial, `1.0` failure, **`NaN`** error.

### `probe.port_open` values (TCP & UDP)

Combine with `probe.up` to recover the three states (see
[ADR-0006](adr/0006-port-open-encoding.md) for the full table and rationale):

| `probe.up` | `port_open` | meaning |
|---|---|---|
| 1 | 1 | OPEN |
| 1 | 0 | CLOSED (TCP RST / UDP ICMP unreachable) |
| 0 | 0 | FILTERED / DOWN (silence) |

PromQL: open = `trimon_probe_port_open == 1`; closed = `trimon_probe_up == 1 and
trimon_probe_port_open == 0`; unreachable/filtered = `trimon_probe_up == 0`. Scope to one
protocol with `probe_type`, e.g. `trimon_probe_port_open{probe_type="udp"}`.

---

## Trends and advanced metrics

Recording rules, long-window loss rates, and probe result history buffering are out of
scope for current phases. See GH Milestones `v0.6.0` for the planned work.
