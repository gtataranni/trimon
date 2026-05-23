# trimon

**T**arget **R**eachability **I**nspection and **MON**itoring

[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.22+-00ADD8.svg)](https://golang.org/)

trimon is an open-source, push-based multi-protocol IP target monitoring daemon.
It runs ICMP echo probes on a configurable schedule, streams results through a
pluggable exporter pipeline, and exposes self-observability metrics in Prometheus
text format. The OTel SDK is wired in for future OTLP export.

---

## Quickstart

### Local binary

```bash
make build
sudo setcap cap_net_raw+ep ./bin/trimon   # grant raw socket capability
./bin/trimon --config config.example.yaml
```

### Container

```bash
make container   # builds with podman by default; pass CONTAINER_RUNTIME=docker to use docker

podman run --rm \
  --name trimon \
  --cap-add NET_RAW \
  -p 8080:8080 \
  -v "$(pwd)/config.docker.yaml:/etc/trimon/config.yaml:ro" \
  trimon:dev
```

---

## Requirements

### CAP_NET_RAW (Linux)

ICMP probes require raw IP sockets. Run the binary as one of:

- `root`, or
- grant the capability: `sudo setcap cap_net_raw+ep ./bin/trimon`
- container: pass `--cap-add NET_RAW` to `docker run` / `podman run`

Without this, probes report `status: error` with
`"open raw socket (CAP_NET_RAW required): ..."`.

---

## CLI flags

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `trimon.yaml` | Path to the YAML config file |
| `--log-level` | `info` | Log verbosity: `debug`, `info`, `warn`, `error` |
| `--log-format` | `json` | Log format: `json`, `text` |

---

## Config reference

See [config.example.yaml](config.example.yaml).

---

## HTTP endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/healthz` | Returns `200 {"status":"ok"}` while the process is running |
| `GET` | `/metrics` | Prometheus text format, self-observability metrics |
| `GET` | `/config` | Active config as JSON (pass `Accept: application/x-yaml` for YAML) |
| `POST` | `/reload` | Reload config from disk without restarting |

### Prometheus metrics

All metrics are served via the OTel Prometheus bridge. Instruments are defined once in
`internal/exporter/otlp/otlp.go` and exported to both `/metrics` and an optional OTLP
collector simultaneously.

**Probe result metrics** (attributes: `probe.name`, `probe.type`, `probe.target`, `probe.source_ip`, user labels):

| Metric | Type | Notes |
|--------|------|-------|
| `trimon_probe_rtt_min_milliseconds` | Gauge | 0 on failure/error |
| `trimon_probe_rtt_mean_milliseconds` | Gauge | 0 on failure/error |
| `trimon_probe_rtt_max_milliseconds` | Gauge | 0 on failure/error |
| `trimon_probe_rtt_stddev_milliseconds` | Gauge | 0 on failure/error |
| `trimon_probe_packet_loss_ratio` | Gauge | 1.0 on failure, NaN on error |
| `trimon_probe_packets_sent_total` | Counter | not incremented on error |
| `trimon_probe_packets_received_total` | Counter | not incremented on error |
| `trimon_probe_success` | Gauge | 1 if all packets replied |
| `trimon_probe_up` | Gauge | 1 if ≥1 reply; use this for alerting |

**Self-observability metrics:**

| Metric | Type | Labels |
|--------|------|--------|
| `trimon_build_info` | Gauge | `version`, `commit`, `goversion` |
| `trimon_probe_runs_total` | Counter | `probe.name` |
| `trimon_probe_errors_total` | Counter | `probe.name`, `error.type` |
| `trimon_scheduler_goroutines` | Gauge | — |
| `trimon_config_reloads_total` | Counter | — |

---

## Development

```bash
make test      # run unit tests with race detector
make lint      # run golangci-lint
make build     # compile binary to ./bin/trimon
make container # build container image (podman by default)
```

### Releasing

```bash
make release V=v0.1.0
git push origin v0.1.0
```

### Adding a new probe type

1. Create `internal/probe/<type>/<type>.go` implementing `probe.Prober`.
2. Add a case to the factory switch in `cmd/trimon/main.go`.
3. Add the type name to `knownProbeTypes` in `internal/config/config.go`.

### Adding a new exporter

1. Create `internal/exporter/<name>/<name>.go` implementing `exporter.Exporter`.
2. Instantiate and append to the `exporters` slice in `buildExporters` in `cmd/trimon/main.go`.
3. Add configuration fields to `ExportersConfig` in `internal/config/config.go`.

---

## License

[Apache 2.0](LICENSE)
