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

Two interfaces drive the entire design — `internal/probe/prober.go` (`Prober`) and `internal/exporter/exporter.go` (`Exporter`). Stabilize them before adding implementations. If a change requires modifying these, stop and flag it before proceeding.

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
- **Build**: use `bin/` as target dir for binaries when not using container. When not on linux, use containers - see [Makefile](Makefile).

## Status semantics — get this right

A `ProbeResult.status` is one of:

| Status | Meaning |
|---|---|
| `success` | 0% packet loss |
| `partial` | 0% < loss < 100% |
| `failure` | 100% loss (host unreachable / all timeouts) |
| `error` | probe could not execute at all (socket error, invalid `source_ip`, etc.) |

`failure` and `error` are **different**. `failure` means the probe ran correctly and the target didn't respond. `error` means trimon itself couldn't run the probe. Do not collapse them.

## Multi-target / DNS resolution behavior (since v0.3.0)

Each probe config accepts `targets: [...]` (a list of IPs or FQDNs). On every scheduler tick, hostnames are **re-resolved** — each returned IP becomes its own `ProbeResult`. Key design decisions:

- `probe.target` = resolved IP address (never the hostname)
- `probe.fqdn` = original hostname (present only when the target was a hostname; absent for bare IPs)
- Area-level reachability: `max by (probe_name)(trimon_probe_up)` across all IPs in the probe
- The old `target:` (singular) YAML key is removed; use `targets:`

## Metrics on `/metrics` (Prometheus text)

All instruments are defined once in `internal/exporter/otlp/otlp.go` via the OTel SDK.
The Prometheus bridge converts them to Prometheus text format at scrape time. The same
instruments also push to an OTel Collector when `exporters.otlp.enabled: true`.
Full metric spec (names, attributes, types): **[docs/metrics.md](docs/metrics.md)**

Key invariants agents frequently get wrong:
- `probe.status` is **never** an attribute — see docs/metrics.md for the rationale.
- RTT gauges and `trimon_probe_packet_loss_ratio` emit **NaN** on `status=error`.
- Counters (`packets_sent`, `packets_received`) are **not incremented** on `status=error`.

### Label cardinality — never put per-run values in `ProbeResult.Labels`

Every entry in `ProbeResult.Labels` is turned into an OTel **metric attribute** by
`buildAttrs` in `internal/exporter/otlp/otlp.go` and attached to *all* per-result series
(`probe_up`, `success`, `packet_loss`, the RTT gauges, the packet counters, …). A label
whose value changes between probe runs therefore creates a brand-new time series on every
tick and orphans the previous one — an **unbounded cardinality leak** that degrades both
Prometheus and the collector.

A value may become a label **only if its domain is small and stable** — e.g. `probe.name`,
`probe.target`, `probe.source_ip`, or static user-defined config labels. **Never** put
per-run or high-churn values in `Labels`:

- ephemeral source ports, request/connection IDs, session tokens
- timestamps, durations, measured latencies
- monotonic or slowly-drifting counters (e.g. TLS days-until-expiry)

If such data is useful for debugging, log it with `slog` at debug level. If it genuinely
needs to be in metrics, expose it as a numeric **gauge value** with a bounded attribute set
(like the existing RTT gauges) — never as an attribute key/value. When in doubt, ask: *"how
many distinct values can this take over the daemon's lifetime?"* If the answer is "unbounded"
or "grows with time", it is not a label.

---

## Project phases & roadmap

**Current phase: Phase 4 — v0.4.0 (additional protocols).** Phases 1–3 are complete.

See [ROADMAP.md](ROADMAP.md) for the full phase plan, per-phase checklists, and out-of-scope items.
See [TASKS.md](TASKS.md) for the active task backlog — check it before starting any implementation work.

Each phase ends in a tagged release. Do not start phase N+1 work in a phase N PR.

## When in doubt

1. Re-read the relevant interface in `internal/probe/prober.go` or `internal/exporter/exporter.go`.
2. Check [TASKS.md](TASKS.md) for the active backlog; check [ROADMAP.md](ROADMAP.md) for phase scope — if the task isn't in the current phase, it's probably out of scope.
3. Research pros/cons, best practices and solutions from other popular project for an informed decision, then ask the user.