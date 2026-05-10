# trimon

**T**arget **R**eachability **I**nspection and **MON**itoring

trimon is an open-source, push-based multi-protocol IP target monitoring daemon.
It runs ICMP echo probes on a configurable schedule, streams results through a
pluggable exporter pipeline, and exposes self-observability metrics in Prometheus
text format. The OTel SDK is wired in for future OTLP export.

---

## Quickstart

TODO: under hot development


---

## CAP_NET_RAW

ICMP probes require a raw IP socket. On Linux this means either:

- running as `root`, or
- granting `CAP_NET_RAW` to the binary: `sudo setcap cap_net_raw+ep ./bin/trimon`
- passing `--cap-add NET_RAW` to `docker run`

Without this capability, probes will report `status: error` with the message
`"open raw socket (CAP_NET_RAW required): ..."`.

---

## CLI flags

TODO: under hot development

---

## Config reference

See [config.example.yaml](config.example.yaml)


---

## HTTP endpoints

| Path | Description |
|------|-------------|
| `GET /healthz` | Always returns `200 {"status":"ok"}` while the process runs |
| `GET /metrics` | Prometheus text format, self-observability metrics |

### Prometheus metrics

| Metric | Type | Labels |
|--------|------|--------|
| `trimon_build_info` | Gauge | `version`, `commit`, `goversion` |
| `trimon_probe_runs_total` | Counter | `probe_name`, `status` |
| `trimon_probe_errors_total` | Counter | `probe_name`, `error_type` |
| `trimon_scheduler_goroutines` | Gauge | — |
| `trimon_config_reload_total` | Counter | — |

---

## Architecture

Phase 1 architecture, subject to change.

```
                        ┌─────────────────────────────────────────┐
                        │                  trimon                  │
                        │                                          │
  SIGHUP ──────────────▶│  config loader ─────────────────────┐   │
  SIGTERM ─────────────▶│  signal handler                     │   │
                        │          │                           │   │
                        │          ▼                           │   │
                        │  ┌──────────────┐                   │   │
                        │  │  scheduler   │  one goroutine     │   │
                        │  │  ┌────────┐  │  + ticker per     │   │
                        │  │  │ prober │  │  prober            │   │
                        │  │  │ (icmp) │  │                   │   │
                        │  │  └────┬───┘  │                   │   │
                        │  └───────┼──────┘                   │   │
                        │          │ ProbeResult               │   │
                        │          ▼                           │   │
                        │  ┌──────────────┐  buffered(1000)   │   │
                        │  │   pipeline   │◀──────────────────┘   │
                        │  │  chan[1000]  │                        │
                        │  └──────┬───────┘                        │
                        │         │ fan-out                        │
                        │    ┌────┴────┐                           │
                        │    ▼         ▼                           │
                        │  stdout   metrics                        │
                        │ exporter  exporter                       │
                        │ (JSON/txt) (Prometheus                   │
                        │            counters)                     │
                        │                      ┌──────────────┐   │
                        │                      │  HTTP server │   │
                        │                      │  /healthz    │   │
                        │                      │  /metrics    │   │
                        │                      └──────────────┘   │
                        └─────────────────────────────────────────┘
```

---

## Development

```bash
make test    # run unit tests with race detector
make lint    # run golangci-lint
make build   # compile binary to ./bin/trimon
make docker  # build container image
```

### Adding a new probe type

1. Create `internal/probe/<type>/<type>.go` implementing `probe.Probe`.
2. Add a case to the factory switch in `cmd/trimon/main.go`.
3. Add the type name to `knownProbeTypes` in `internal/config/config.go`.

### Adding a new exporter

1. Create `internal/exporter/<name>/<name>.go` implementing `exporter.Exporter`.
2. Instantiate and append to the `exporters` slice in `buildExporters` in `cmd/trimon/main.go`.
3. Add configuration fields to `ExportersConfig` in `internal/config/config.go`.

---

## License

See [LICENSE](LICENSE).
