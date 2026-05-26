# Observability pipeline latency

trimon exposes metrics via two paths. Each has a different latency profile; choose
the right one depending on whether you need fast alerting or rich dashboard labels.

---

## The two paths

```
trimon probe result
       │
       ├─── OTel gauge (updated in-process, always current)
       │         │
       │         ├─── Prometheus bridge → /metrics          [direct scrape]
       │         │
       │         └─── OTLP periodic reader ──► otelcol ──► otelcol /metrics  [OTLP path]
       │
       └─── stdout exporter (optional, no latency)
```

### Direct scrape (`job="trimon"`, `instance="trimon:8080"`)

Prometheus pulls `/metrics` from trimon directly. The OTel Prometheus bridge reads
the in-process gauge value at scrape time — no buffering. Latency is bounded by the
Prometheus scrape interval (default 15s).

**Worst-case staleness: 1 × scrape_interval** (e.g. 15s)

### OTLP path (`job="otelcol-prometheus"`, `instance="otelcol:8889"`)

trimon batches results and pushes to the OTel Collector on the `export_interval`
cadence. Prometheus then scrapes the collector's Prometheus exporter endpoint.
Two intervals stack.

**Worst-case staleness: export_interval + scrape_interval**

| Config | Value | Worst-case staleness |
|--------|-------|----------------------|
| `export_interval: 15s` + `scrape_interval: 15s` | default | ~30s |
| `export_interval: 5s` + `scrape_interval: 15s` | multiline-demo | ~20s |
| `export_interval: 5s` + `scrape_interval: 5s` | tightest practical | ~10s |

---

## Which path to use

| Use case | Recommended path | Reason |
|----------|-----------------|--------|
| Alerting rules (fast detection) | `job="trimon"` | Only one interval of staleness |
| Dashboards | `job="otelcol-prometheus"` | Carries OTel resource attributes not present on the direct scrape |
| Both simultaneously | Use `job="trimon"` for alerts, `job="otelcol-prometheus"` for panels | Complementary |

Avoid mixing the two paths in the same PromQL expression — they will briefly show
different values for the same probe and produce confusing aggregations.

---

## Deduplication

Running both scrape jobs produces duplicate `trimon_probe_*` time series unless one set
is dropped. The recommended approach (used in the bundled `prometheus.yml` files) is to
drop probe result metrics from the direct scrape job and keep only self-observability
metrics there:

```yaml
# in the job_name: trimon scrape config
metric_relabel_configs:
  - source_labels: [__name__]
    regex: 'trimon_probe_(rtt_.*|packet_loss_ratio|packets_sent_total|packets_received_total|success|up)'
    action: drop
```

Self-observability metrics (`trimon_probe_runs_total`, `trimon_probe_errors_total`,
`trimon_probe_results_dropped_total`, `trimon_scheduler_goroutines`, etc.) are **not**
pushed via OTLP and must be collected via the direct scrape.

---

## Tuning

`export_interval` in the ops config (`config.yaml`) is the primary knob:

```yaml
exporters:
  otlp:
    batch:
      export_interval: 5s   # default is 15s; lower = fresher OTLP data, more connections
      export_timeout: 5s    # must be < export_interval
```

`export_timeout` must stay below `export_interval`. Setting both to the same value
causes the batch to time out before it can flush.

Lowering `export_interval` below the probe cadence (`probe_every`) has diminishing
returns — an empty batch is flushed with no new data.
