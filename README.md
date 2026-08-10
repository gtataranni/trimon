# trimon

*Push-based multi-line **T**arget **R**eachability **I**nspection and **MON**itoring for multi-homed networks*

[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.25+-00ADD8.svg)](https://golang.org/)
[![Platform](https://img.shields.io/badge/platform-Linux-orange.svg)](#requirements)

trimon is an open-source, push-based multi-protocol IP target monitoring daemon that exports results to the OpenTelemetry stack. It is particularly useful in multi-line environments and SD-WAN setups where routing agents need continuous per-interface latency signals — not scrape-triggered snapshots. Pull-based tools like blackbox_exporter only measure when scraped, creating gaps between scrape intervals. trimon pushes results continuously from each source IP, running one goroutine per probe on its own configurable schedule.

---

## Multi-line demo

→ **[examples/multiline-demo/](examples/multiline-demo/README.md)**

Spins up a Docker Compose stack with three simulated WAN lines (fiber / cable / VSAT) and a pre-built Grafana dashboard showing per-line RTT, packet loss, and jitter side by side.

```bash
cd examples/multiline-demo
docker compose up -d --build
# Grafana: http://localhost:3001  →  trimon | Multi-Line Quality
```

![trimon Multi-Line Quality dashboard](examples/multiline-demo/multiline-dashboard-banner.png)

---

## Key features

- **Push-based:** probes run on schedule, results export continuously — no scrape gaps
- **Multi-line `source_ip` per probe** — bind each probe to a specific interface IP
- Per-probe cadence override — run critical-path probes more frequently
- **OTel-native:** single MeterProvider feeds both `/metrics` (Prometheus bridge) and OTLP push
- Single static binary, no CGO, no runtime dependencies

---

## How it works

```
config inputs (--config file / --probes dir)
    │
    ▼
Scheduler  (one goroutine + ticker per probe)
    │
    ▼
Probers  ──── bind to source_ip ────▶ ICMP echo
    │
    ▼
Result pipeline  (buffered channel, fan-in)
    │
    ▼
Exporters ──▶ stdout (optional)
          └──▶ OTLP ──▶ OTel Collector ──▶ Prometheus ──▶ Grafana
                    └──▶ Prometheus bridge  (/metrics)
```

---

## Quickstart

### Multi-line demo

See [examples/multiline-demo/](examples/multiline-demo/README.md) — the fastest way to see trimon working end-to-end with a full observability stack.

### Local binary

```bash
make build
sudo setcap cap_net_raw+ep ./bin/trimon
./bin/trimon --config config.example.yaml --probes ./examples/probes.d
```

---

## Configuration reference

trimon takes two config inputs:

- **[config.example.yaml](config.example.yaml)** — ops config (`--config`): a single file holding exporters, server listen address, pipeline buffer. Intended for ops use; never exposed via HTTP.
- **[examples/probes.d/](examples/probes.d)** — probe config (`--probes`): a **directory** of `*.yaml` files holding global probe defaults and target lists. Safe to expose to unprivileged users; returned merged by `GET /config`.

Both examples are annotated field-by-field and are the canonical reference for probe
fields and probe types. See
[ADR-0007](docs/adr/0007-two-config-files.md) for the two-input split and its rationale.

### The probe config directory

`--probes` names a directory. Every `*.yaml` file directly inside it is merged, in lexical
order, at startup and on every `POST /reload`:

```
probes.d/
├── _global.yaml      # reserved: global defaults only
├── core-sites.yaml   # probes:
└── edge-sites.yaml   # probes:
```

Rules:

- `--probes` must be a directory — pointing it at a plain file is an error. A single-file
  setup is just a directory with one `*.yaml` in it.
- Only `*.yaml` **directly inside** the directory is read — non-recursive; dotfiles,
  subdirectories, and other extensions are ignored.
- `global:` is allowed **only** in `_global.yaml`, and that file must not declare `probes:`.
  Without `_global.yaml`, the built-in defaults apply (`probe_every: 30s`,
  `packet_interval: 1s`, `timeout: 5s`, `count: 3`).
- Probe names must be unique **across** all files.
- A directory with no `*.yaml` files is an error.
- Reload is all-or-nothing: any error in any file rejects the whole set and the daemon
  keeps its previous config. The logged `sha256` covers file names and contents, so adds,
  removes, and renames are all visible.
- The ops config (`--config`) remains a single file.

See [ADR-0008](docs/adr/0008-probe-config-directory.md) for the rationale.

---

## Metrics reference

All metrics are served via the OTel Prometheus bridge and, when enabled, pushed over OTLP.
Instruments are defined once in `internal/exporter/otlp/otlp.go` — that file plus
[docs/metrics.md](docs/metrics.md) are the source of truth for names, types, and semantics.
Key signals: `trimon_probe_up` (1 if ≥1 reply — use this for alerting), `trimon_probe_success`
(1 if all packets replied), `trimon_probe_rtt_*_milliseconds`, `trimon_probe_packet_loss_ratio`,
`trimon_probe_port_open`, and `trimon_probe_duration_milliseconds`. Design rationale lives in
the [ADRs](docs/adr/).

---

## HTTP endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/healthz` | Returns `200 {"status":"ok"}` while the process is running |
| `GET` | `/metrics` | Prometheus text format, self-observability metrics |
| `GET` | `/config` | Active config as JSON (pass `Accept: application/x-yaml` for YAML) |
| `POST` | `/reload` | Reload config from disk without restarting |

---

## Requirements

trimon runs on Linux only. macOS, Windows, and other platforms are out of scope for
now: ICMP and per-probe source-IP binding rely on Linux raw sockets and capabilities. You
can still develop on a non-Linux host by building and running in a container
(`make container`, `make dev-stack`), but the deployment target is Linux.

### CAP_NET_RAW (Linux)

ICMP probes require raw IP sockets. Run the binary as one of:

- `root`, or
- grant the capability: `sudo setcap cap_net_raw+ep ./bin/trimon`
- container: pass `--cap-add NET_RAW` to `docker run` / `podman run`

Without this, probes report `status: error` with
`"open raw socket (CAP_NET_RAW required): ..."`.

---

## Development

```bash
make test      # run unit tests with race detector
make lint      # run golangci-lint
make build     # compile binary to ./bin/trimon
make container # build container image
```

### Smoke test

`make smoke` runs an end-to-end check: it builds and starts the lean dev-stack
(the real Linux binary + OTel Collector) in containers, waits for the trimon HTTP
server, then runs the Go assertion layer in [test/smoke/](test/smoke/) (build tag
`smoke`). It verifies that every probe type (ICMP, TCP, UDP, DNS, HTTP) reports a
reachable target through `/metrics` and that results reach the collector over
OTLP, before tearing the stack down.

```bash
make smoke                 # build, run, assert, tear down
make smoke ARGS="--keep"   # leave the stack running for inspection
```

It needs a container runtime (docker by default; use e.g. `make smoke ARGS="--runtime podman"` to change)
and outbound network, since the demo probes hit public targets. The `smoke` tag
keeps these tests out of `make test`.

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
