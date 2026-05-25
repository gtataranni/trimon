# Roadmap

Each phase ends in a tagged release. Do not start phase N+1 work in a phase N PR.

## Phase 1 — v0.1.0: ICMP + stdout ✅

Goal: a runnable daemon that pings configured targets and emits NDJSON to stdout.

- [x] Module scaffold, Makefile, golangci-lint config, Dockerfile
- [x] `pkg/types` — `ProbeResult`, `ProbeConfig`, status constants
- [x] `internal/config` — YAML loader + validator (unique names, valid targets, source_ip resolution)
- [x] `internal/probe/probe.go` — `Probe` interface
- [x] `internal/exporter/exporter.go` — `Exporter` interface
- [x] `internal/probe/icmp` — raw ICMP, source_ip binding, count + timeout, RTT stats
- [x] `internal/scheduler` — per-probe goroutine + ticker, start/stop/reload diff
- [x] `internal/pipeline` — buffered fan-in channel, exporter fan-out
- [x] `internal/exporter/stdout` — JSON (NDJSON) and text formats
- [x] `internal/server` — `/healthz`, `/metrics` (self-observability)
- [x] `cmd/trimon/main.go` — flags, wiring, ~~SIGHUP reload~~, SIGTERM graceful shutdown
- [x] POST /reload for reload
- [x] `config.example.yaml`, `README.md`

**Done criteria:** `trimon --config config.example.yaml` pings 8.8.8.8 every 10s, prints NDJSON to stdout, `/healthz` returns 200, ~~SIGHUP~~ `POST /reload` reloads config without dropping in-flight probes, `SIGTERM` drains and exits cleanly.

---

## Phase 2 — v0.2.0: OTLP export

Goal: ship probe results to a real OTel Collector as metrics.

Metric spec: **[docs/metrics.md — OTLP exporter metrics](docs/metrics.md)**

- [x] `internal/exporter/otlp` — gRPC and HTTP variants, configurable batching/retry/TLS
- [x] OTel resource attributes (service.name, service.version, host.name)
- [x] Map `ProbeResult` → metric names per spec in docs/metrics.md
- [x] Multiple exporters simultaneously (stdout + OTLP)
- [x] `examples/local-stack/` — Docker Compose stack: OTel Collector + Prometheus + Grafana
- [x] Integration test: run trimon against local Collector (build tag `integration`)

---

## Phase 2.5 - v0.2.5: strengthen structure and add demo

- [ ] Finish up (most of) items in [TASKS.md](TASKS.md)
- [ ] Add advanced dashboard example and screenshot

---

## Phase 3 — v0.3.0: Target areas/groups

Goal: introduce target area as a way to group multiple targets.
An area can be "the internet", or "the web server subnet" or "the private DNS servers in Europe".
The area is considered reachable based on the probe result of all targets in the area.

- [ ] Extend concept of target into target area, where an area can be defined by multiple ip4/6 addresses. This is very similar to task SEC-10, and can be solved together.
- [ ] Results are available per IP target but also by target area

---

## Phase 4 — v0.4.0: Additional protocols

Goal: trimon becomes genuinely multi-protocol.

- [ ] TCP connect probe (`internal/probe/tcp`) — connect-time, source port label
- [ ] UDP probe (`internal/probe/udp`) — send/recv with expected response patterns
- [ ] DNS probe (`internal/probe/dns`) — A/AAAA/CNAME, resolver override, expected answer match
- [ ] HTTP/HTTPS probe (`internal/probe/http`) — status code match, response time, TLS expiry
- [ ] Per-protocol config schema additions, validated by the config loader
- [ ] Simple MQTT exporter (`internal/exporter/mqtt`) — publish probe results to an MQTT broker
- [ ] Consider implementing OPT-13 Unify probe timeout through context; remove pinger.Timeout from ICMP prober.

---

## Phase 5 — v0.5.0: Operability and management API

Goal: production-grade lifecycle and observability.

- [ ] Hot-reload via HTTP API (`POST /-/reload`) in addition to SIGHUP
- [ ] Config from a directory of files (merge), not just a single file
- [ ] Probe-level enable/disable without restart
- [ ] Per-probe rate limiting / global concurrency cap
- [ ] Traces emitted for probe runs (OTel spans alongside metrics)
- [ ] GET /api/probes/results — returns the last N results per probe as JSON
- [ ] GET /api/probes, POST /api/probes — list and create probes, persist config (validate → write YAML → reload scheduler)
- [ ] DELETE /api/probes/{name}
- [ ] PATCH /api/probes/{name} — modify probe interval/timeout/count

---

## Phase 6 — v0.6.0: Path & advanced

Goal: the things that needed everything else to be solid first.

- [ ] Traceroute / path discovery probe (TBD)
- [ ] MTR-style continuous path probing (TBD)
- [ ] Path-change detection events (TBD)
- [ ] Probe result history buffer for short retention without external storage
- [ ] Trends and advanced self-observability: recording rules guidance, long-window loss rates, anomaly baselines (TBD)

---

## Explicitly out of scope (all phases)

- Distributed/coordinated multi-instance deployment
- Built-in alerting (handled by OTel/Prometheus consumers downstream)
- Business-domain labels (customer, site, SLA tier — handled by downstream label enrichment)
- Storage backend / time-series DB
- Web UI

If a request lands here, push back and explain why it belongs in the consumer side of the OTel stack, not in trimon.
