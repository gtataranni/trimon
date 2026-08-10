# ADR-0007: Two config files, load-once, probe-only hot-reload

- **Status:** accepted (amended in part by [ADR-0008](0008-probe-config-directory.md),
  which lets `--probes` name a directory of merged `*.yaml` files)
- **Date:** 2026-07-20

## Context

trimon's configuration mixes two audiences with different security postures and change
cadences: (1) *what to probe* — targets, intervals, labels — which unprivileged users may
edit frequently and which is safe to expose; and (2) *deployment settings* — OTLP
endpoints (possibly carrying credentials), TLS paths, bind address, pipeline tuning —
owned by platform operators and never safe to expose over HTTP.

## Decision

- **Two files, two flags** (both required): `--probes` (probe config) and `--config` (ops
  config). trimon exits if either is missing.
- **Load once.** `config.Load(opsPath, probePath)` reads each file exactly once into a
  byte buffer before parsing — no second read anywhere in the load chain — to avoid TOCTOU
  races.
- **Probe-only hot-reload.** `POST /reload` re-reads only the probe file. The ops config is
  immutable after startup; changing exporters, TLS, or the bind address requires a restart.
- **Exposure.** `GET /config` returns only the probe config (global defaults + probes) as
  JSON, or YAML when the request sends `Accept: application/x-yaml`. The ops config is
  never served; there is no `/ops-config` endpoint. The SHA256 fingerprint logged at
  reload is computed over the probe file bytes only.

## Consequences

- Matches the convention of nginx/Prometheus: network-topology-and-credentials config is
  load-once; the "what to probe" config is live-reloadable.
- Credentials in the ops config never leak via the HTTP surface.
- Operators must restart to change exporters/TLS/bind — accepted, as those change rarely.
