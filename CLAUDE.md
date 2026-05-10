# CLAUDE.md

Guidance for Claude Code working on this repository.

## Project: trimon
**T**arget **R**eachability **I**nspection and **MON**itoring

`trimon` is an open-source, push-based multi-protocol IP target monitoring daemon that exports results to the OpenTelemetry stack.

It differs from `blackbox_exporter` in one key way: trimon runs an internal scheduler and pushes results out, rather than waiting to be scraped before triggering a measurement. Probe frequency is configured per-target; results are collected continuously and exported on the same cadence they're produced.

## Tech stack & hard constraints

- **Language:** Go 1.22+
- **No CGO** — pure Go only
- **Linux-first** — ICMP requires raw sockets and `CAP_NET_RAW`
- **Module path:** `github.com/<handle>/trimon` (set once, do not refactor)
- **License:** Apache 2.0

### Core dependencies (keep this list small)
- `golang.org/x/net/icmp` — raw ICMP
- `gopkg.in/yaml.v3` — config parsing
- `go.opentelemetry.io/otel` — OTel SDK (wired in from day 1, even with stdout-only export)
- `github.com/prometheus/client_golang` — self-observability `/metrics` endpoint

Do not add new dependencies without an explicit reason. Prefer the standard library.

## Architecture

```
config.yaml
    │
    ▼
Config Loader ──API call──▶ hot-reload
    │
    ▼
Scheduler (one goroutine + ticker per prober)
    │
    ▼
Probing Workers ──── bind to source_ip
    │
    ▼
Result Channel (buffered, fan-in)
    │
    ▼
Exporter goroutine ──▶ stdout (v1) / OTLP (later)

+ HTTP Server: /healthz, /metrics, some api features (TBD)
```

### Load-bearing abstractions

Two interfaces drive the entire design. Stabilize them before adding implementations.

```go
// internal/probe/probe.go
type Probe interface {
    Run(ctx context.Context) (types.ProbeResult, error)
    Name() string
    Type() string
}

// internal/exporter/exporter.go
type Exporter interface {
    Export(ctx context.Context, result types.ProbeResult) error
    Close() error
}
```

If a change requires modifying these, stop and flag it before proceeding.

## Project layout

```
trimon/
├── cmd/trimon/main.go          # flags, wiring, signal handling
├── internal/
│   ├── config/                 # YAML load, validate, hot-reload
│   ├── probe/
│   │   ├── probe.go            # Probe interface
│   │   └── icmp/               # ICMP impl
│   ├── scheduler/              # per-probe ticker + lifecycle
│   ├── pipeline/               # buffered result channel, fan-in/out
│   ├── exporter/
│   │   ├── exporter.go         # Exporter interface
│   │   └── stdout/             # stdout impl (json + text)
│   └── server/                 # HTTP /healthz + /metrics
├── pkg/types/                  # ProbeResult, ProbeConfig, Status consts
├── config.example.yaml
├── Dockerfile
├── Makefile
└── README.md
```

`internal/` is for implementation. `pkg/types/` holds anything an external consumer might reasonably import — keep it minimal.

## Common commands

```bash
make build       # build ./bin/trimon
make test        # go test ./... with race detector
make lint        # golangci-lint run
make docker      # build container image with CAP_NET_RAW

./bin/trimon --config config.example.yaml --log-level debug
```

After build, grant raw socket capability for local runs:
```bash
sudo setcap cap_net_raw+ep ./bin/trimon
```

## Conventions

- **Errors:** wrap with `fmt.Errorf("context: %w", err)`. No bare returns of upstream errors.
- **Logging:** structured (`log/slog`), JSON by default, text when `--log-format=text`, logfmt also supported.
- **Context:** every blocking call takes a `context.Context`. No `context.Background()` outside `main`.
- **Concurrency:** every goroutine has a clear owner and a shutdown path (context cancel or close channel). No fire-and-forget.
- **Tests:** table-driven. Required for `internal/config` (validation), `pkg/types`, `internal/scheduler` (lifecycle), `internal/exporter/stdout` (output shape).
- **No global state** outside `cmd/trimon/main.go`.
- **Commits:** conventional commits (`feat:`, `fix:`, `refactor:`, `docs:`, `test:`, `chore:`).

## Status semantics — get this right

A `ProbeResult.status` is one of:

| Status | Meaning |
|---|---|
| `success` | 0% packet loss |
| `partial` | 0% < loss < 100% |
| `failure` | 100% loss (host unreachable / all timeouts) |
| `error` | probe could not execute at all (socket error, invalid `source_ip`, etc.) |

`failure` and `error` are **different**. `failure` means the probe ran correctly and the target didn't respond. `error` means trimon itself couldn't run the probe. Do not collapse them.

## Self-observability metrics (Prometheus text on `/metrics`)

These are about trimon itself, not the probe results.

- `trimon_build_info{version, commit, goversion}` — gauge, value 1
- `trimon_probe_runs_total{probe_name, status}` — counter
- `trimon_probe_errors_total{probe_name, error_type}` — counter
- `trimon_scheduler_goroutines` — gauge
- `trimon_config_reload_total` — counter

Probe results themselves go through the `Exporter` pipeline (stdout in v1), **not** through `/metrics`.

---

## Project phases

Each phase ends in a tagged release. Do not start phase N+1 work in a phase N PR.

### Phase 1 — v0.1.0: ICMP + stdout (current)

Goal: a runnable daemon that pings configured targets and emits NDJSON to stdout.

- [ ] Module scaffold, Makefile, golangci-lint config, Dockerfile
- [ ] `pkg/types` — `ProbeResult`, `ProbeConfig`, status constants
- [ ] `internal/config` — YAML loader + validator (unique names, valid targets, source_ip resolution)
- [ ] `internal/probe/probe.go` — `Probe` interface
- [ ] `internal/exporter/exporter.go` — `Exporter` interface
- [ ] `internal/probe/icmp` — raw ICMP, source_ip binding, count + timeout, RTT stats
- [ ] `internal/scheduler` — per-probe goroutine + ticker, start/stop/reload diff
- [ ] `internal/pipeline` — buffered fan-in channel, exporter fan-out
- [ ] `internal/exporter/stdout` — JSON (NDJSON) and text formats
- [ ] `internal/server` — `/healthz`, `/metrics` (self-observability)
- [ ] `cmd/trimon/main.go` — flags, wiring, SIGHUP reload, SIGTERM graceful shutdown
- [ ] `config.example.yaml`, `README.md`

**Done criteria:** `trimon --config config.example.yaml` pings 8.8.8.8 every 10s, prints NDJSON to stdout, `/healthz` returns 200, `SIGHUP` reloads config without dropping in-flight probes, `SIGTERM` drains and exits cleanly.

### Phase 2 — v0.2.0: OTLP export

Goal: ship to a real OTel Collector.

- [ ] `internal/exporter/otlp` — gRPC and HTTP variants
- [ ] OTel resource attributes (service.name, service.version, host.name)
- [ ] Map `ProbeResult` → metric names defined in spec (`trimon.probe.duration`, `trimon.probe.rtt.min`, etc.)
- [ ] Configurable batching, retry, TLS
- [ ] Multiple exporters can be enabled simultaneously (stdout + OTLP)
- [ ] Integration test: run trimon against a local Collector in CI

### Phase 3 - v0.3.0: Introduce target areas/groups

Goal: introduce target area as a way to group multiple targets.
An area can be "the internet", or "the web server subnet" or "the private DNS servers in Europe".
The Area is considered reachable based on the probe result of all targets in the area.

- [ ] Extend concept of target into target area, where an area can be defined by multiple ip4/6 addresses
- [ ] Results are available per ip target but also by target area

### Phase 4 — v0.4.0: Additional protocols

Goal: trimon becomes genuinely multi-protocol.

- [ ] TCP connect probe (`internal/probe/tcp`) — connect-time, source port label
- [ ] UDP probe (`internal/probe/udp`) — send/recv with expected response patterns
- [ ] DNS probe (`internal/probe/dns`) — A/AAAA/CNAME, resolver override, expected answer match
- [ ] HTTP/HTTPS probe (`internal/probe/http`) — status code match, response time, TLS expiry
- [ ] Per-protocol config schema additions, validated by the config loader

### Phase 5 — v0.5.0: Operability

Goal: production-grade lifecycle and observability.

- [ ] Hot-reload via HTTP API (`POST /-/reload`) in addition to SIGHUP
- [ ] Config from a directory of files (merge), not just a single file
- [ ] Probe-level enable/disable without restart
- [ ] Per-probe rate limiting / global concurrency cap
- [ ] Traces emitted for probe runs (OTel spans alongside metrics)

### Phase 6 — v0.6.0: Path & advanced

Goal: the things that needed everything else to be solid first.

- [ ] Traceroute / path discovery probe (TBD)
- [ ] MTR-style continuous path probing (TBD)
- [ ] Path-change detection events (TBD)
- [ ] Probe result history buffer for short retention without external storage

---

## Explicitly out of scope (all phases)

- Distributed/coordinated multi-instance deployment
- Built-in alerting (handled by OTel/Prometheus consumers downstream)
- Business-domain labels (customer, site, SLA tier — handled by downstream label enrichment)
- Storage backend / time-series DB
- Web UI

If a request lands here, push back and explain why it belongs in the consumer side of the OTel stack, not in trimon.

## When in doubt

1. Re-read the relevant interface in `internal/probe/probe.go` or `internal/exporter/exporter.go`.
2. Check the current phase's checklist above — if the task isn't on it, it's probably out of scope for this iteration.
3. Prefer adding a comment that states an assumption over asking; flag the assumption in the PR description and in the summary at the end of agent cycles.