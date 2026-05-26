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
- Use KISS and DRY principles, alongside Go best practices
- Updates to this file are allowed but need to be confirmed by user

### Core dependencies (keep this list small)
- `golang.org/x/net/icmp` — raw ICMP
- `gopkg.in/yaml.v3` — config parsing
- `go.opentelemetry.io/otel` — OTel SDK; single `MeterProvider` drives both `/metrics` (via bridge) and optional OTLP push
- `go.opentelemetry.io/otel/exporters/prometheus` — Prometheus bridge; converts OTel instruments to Prometheus text format at scrape time
- `github.com/prometheus/client_golang` — pulled in transitively by the bridge; **do not define instruments directly against it**

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
Exporter goroutine ──▶ stdout / OTLP (OTel SDK)
                              │
                              └──▶ Prometheus bridge ──▶ /metrics

+ HTTP Server: /healthz, /metrics, /config, /reload
```

### Load-bearing abstractions

Two interfaces drive the entire design. Stabilize them before adding implementations.

```go
// internal/probe/prober.go
type Prober interface {
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
│   │   ├── prober.go           # Prober interface
│   │   └── icmp/               # ICMP impl
│   ├── scheduler/              # per-probe ticker + lifecycle
│   ├── pipeline/               # buffered result channel, fan-in/out
│   ├── exporter/
│   │   ├── exporter.go         # Exporter interface
│   │   ├── otlp/               # OTLP exporter + OTel instrument definitions
│   │   └── stdout/             # stdout impl (json + text)
│   └── server/                 # HTTP /healthz, /metrics, /config, /reload
├── pkg/types/                  # ProbeResult, ProbeConfig, Status consts
├── docs/                       # design docs (metrics.md, etc.)
├── config.example.yaml
├── Dockerfile
├── Makefile
├── ROADMAP.md
├── TASKS.md
└── README.md
```

`internal/` is for implementation. `pkg/types/` holds anything an external consumer might reasonably import — keep it minimal.

## Common commands

```bash
make build       # build ./bin/trimon
make test        # go test ./... with race detector
make lint        # golangci-lint run
make container   # build container image (podman by default)
make release V=vX.Y.Z  # tag and build a release

./bin/trimon --config config.example.yaml --log-level debug
```

After build, grant raw socket capability for local runs (Linux only):
```bash
sudo setcap cap_net_raw+ep ./bin/trimon
```

## Conventions

- **Errors:** wrap with `fmt.Errorf("context: %w", err)`. No bare returns of upstream errors.
- **Logging:** structured (`log/slog`), JSON by default, text when `--log-format=text`.
- **Context:** every blocking call takes a `context.Context`. No `context.Background()` outside `main`.
- **Concurrency:** every goroutine has a clear owner and a shutdown path (context cancel or close channel). No fire-and-forget.
- **Tests:** table-driven. Required for `internal/config` (validation), `pkg/types`, `internal/scheduler` (lifecycle), `internal/exporter/stdout` (output shape).
- **No global state** outside `cmd/trimon/main.go`.
- **Commits:** conventional commits (`feat:`, `fix:`, `refactor:`, `docs:`, `test:`, `chore:`).
Use "Assisted-by: AGENT_NAME:MODEL_VERSION" instead of "Co-Authored-By: name email". Example: `Assisted-by: claude-code:claude-sonnet-4-6`.
- **Build**: use `bin/` as target dir, or use `make` commands

## Status semantics — get this right

A `ProbeResult.status` is one of:

| Status | Meaning |
|---|---|
| `success` | 0% packet loss |
| `partial` | 0% < loss < 100% |
| `failure` | 100% loss (host unreachable / all timeouts) |
| `error` | probe could not execute at all (socket error, invalid `source_ip`, etc.) |

`failure` and `error` are **different**. `failure` means the probe ran correctly and the target didn't respond. `error` means trimon itself couldn't run the probe. Do not collapse them.

## Metrics on `/metrics` (Prometheus text)

All instruments are defined once in `internal/exporter/otlp/otlp.go` via the OTel SDK.
The Prometheus bridge converts them to Prometheus text format at scrape time. The same
instruments also push to an OTel Collector when `exporters.otlp.enabled: true`.
Full design rationale: **[docs/metrics.md](docs/metrics.md)**

Probe attributes: `probe.name`, `probe.type`, `probe.target`, `probe.source_ip`, plus
any user-defined labels from the probe config. `probe.status` is **never** an attribute
(see docs/metrics.md for why).

**Probe result metrics** — the actual measurements trimon produces:
- `trimon_probe_rtt_min_milliseconds{...}` — gauge
- `trimon_probe_rtt_mean_milliseconds{...}` — gauge
- `trimon_probe_rtt_max_milliseconds{...}` — gauge
- `trimon_probe_rtt_stddev_milliseconds{...}` — gauge
- `trimon_probe_packet_loss_ratio{...}` — gauge, 0.0–1.0, NaN on status=error
- `trimon_probe_packets_sent_total{...}` — counter (not incremented on error)
- `trimon_probe_packets_received_total{...}` — counter (not incremented on error)
- `trimon_probe_success{...}` — gauge, 1 only if all packets replied (status=success)
- `trimon_probe_up{...}` — gauge, 1 if ≥1 reply received (status=success or partial)

**Operational self-observability** — about trimon itself:
- `trimon_build_info{version, commit, goversion}` — gauge, value 1
- `trimon_probe_runs_total{probe.name}` — counter
- `trimon_probe_errors_total{probe.name, error.type}` — counter
- `trimon_probe_results_dropped_total{probe.name}` — counter, incremented when the pipeline buffer is full and a result is dropped
- `trimon_scheduler_goroutines` — gauge
- `trimon_config_reloads_total` — counter

---

## Project phases & roadmap

See [ROADMAP.md](ROADMAP.md) for the full phase plan, per-phase checklists, and out-of-scope items.

Each phase ends in a tagged release. Do not start phase N+1 work in a phase N PR.

## When in doubt

1. Re-read the relevant interface in `internal/probe/prober.go` or `internal/exporter/exporter.go`.
2. Check the current phase's checklist in [ROADMAP.md](ROADMAP.md) — if the task isn't on it, it's probably out of scope for this iteration.
3. Prefer adding a comment that states an assumption over asking; flag the assumption in the PR description and in the summary at the end of agent cycles.