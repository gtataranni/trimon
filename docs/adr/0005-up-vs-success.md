# ADR-0005: `probe.up` and `probe.success` are distinct signals

- **Status:** accepted
- **Date:** 2026-07-20

## Context

Two natural questions about a probe run are "did the target respond at all?" and "did
every packet get through?". Collapsing them into one gauge forces a choice that is wrong
for one use case: a strict signal pages on any single dropped packet; a lenient signal
hides partial degradation.

## Decision

trimon exposes both as separate binary gauges:

- `probe.up` = 1 if at least one packet received a reply (status `success` or `partial`),
  else 0. This is the **alerting** signal — `ALERT IF probe_up == 0` does not fire on
  intermittent ICMP filtering.
- `probe.success` = 1 only when every sent packet received a reply (status `success`).
  This is the **strict** signal for quality dashboards.

| status | probe.up | probe.success |
|--------|----------|---------------|
| `success` | 1 | 1 |
| `partial` | 1 | 0 |
| `failure` | 0 | 0 |
| `error` | 0 | 0 |

## Consequences

- Alerting stays quiet under intermittent partial loss; quality can still be tracked via
  `probe.success` and `probe.packet_loss`.
- Consumers must pick the correct gauge for their intent; using `probe.success` for
  alerting would over-page.
