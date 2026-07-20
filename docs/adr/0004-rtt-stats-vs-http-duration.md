# ADR-0004: ICMP RTT statistics vs a single HTTP `duration`

- **Status:** accepted
- **Date:** 2026-07-20

## Context

ICMP probes send N packets per run and can describe a latency *distribution*
(min/mean/max/stddev) — network jitter is meaningful. HTTP is different: a probe sends one
request per tick. Repeating requests 2..N within a run would reuse the TCP connection and
produce a bimodal distribution (cold vs. warm), making mean and stddev misleading rather
than informative.

## Decision

trimon exposes separate instruments:

- `trimon.probe.rtt.{min,mean,max,stddev}` — ICMP only, describing the packet distribution;
- `trimon.probe.duration` — HTTP only, a single wall-clock measurement covering
  DNS + TCP + TLS + TTFB + body drain.

Each is `NaN` for probe types it does not apply to.

## Consequences

- HTTP latency is the canonical single-number measure, matching blackbox_exporter,
  Datadog Synthetics, and Checkly.
- Dashboards and alerts must select the right instrument per probe type; there is no
  unified "latency" gauge across ICMP and HTTP.
