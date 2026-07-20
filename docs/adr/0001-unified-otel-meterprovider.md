# ADR-0001: Unified OTel SDK MeterProvider for both /metrics and OTLP

- **Status:** accepted
- **Date:** 2026-07-20

## Context

trimon must expose metrics in two ways: a Prometheus text endpoint (`/metrics`) for
direct scraping, and OTLP push to an OTel Collector when configured. A naive approach
defines instruments twice — once against `prometheus/client_golang`, once against the
OTel SDK — which duplicates every instrument definition and lets the two sets drift.

## Decision

trimon uses a single OTel SDK `MeterProvider` with two readers:

- the Prometheus bridge (`go.opentelemetry.io/otel/exporters/prometheus`), always active,
  serving `/metrics` in Prometheus text format at scrape time;
- an OTLP periodic reader, active only when `exporters.otlp.enabled: true`.

All instruments are defined once in `internal/exporter/otlp/otlp.go`. There is no separate
Prometheus client instrument set. `prometheus/client_golang` is pulled in only
transitively by the bridge and instruments are never defined directly against it.

## Consequences

- One instrument definition feeds both export paths; they cannot disagree.
- The Prometheus bridge reads the in-process gauge value at scrape time (no buffering);
  the OTLP path batches on `export_interval`. Their latency profiles differ — see
  `docs/observability-latency.md`.
- Adding, removing, or renaming an instrument is a single-site change in `otlp.go`.
