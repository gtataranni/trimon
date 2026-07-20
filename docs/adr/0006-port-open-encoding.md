# ADR-0006: `port_open` + `up` encode reachability as two gauges

- **Status:** accepted
- **Date:** 2026-07-20

## Context

TCP and UDP probes need to distinguish an *open* port from a *closed but reachable* port
from an *unreachable/filtered* target — three operational states. Encoding a 3-valued enum
as a `state=` attribute would violate ADR-0002 (state in labels). A refused port is still
network reachability, not packet loss, so it must not be conflated with a timeout.

## Decision

trimon sets `probe.port_open` (a Float64Gauge) alongside the existing `probe.up`, and
recovers the three states by combining them:

| `probe.up` | `port_open` | TCP meaning | UDP meaning |
|---|---|---|---|
| 1 | 1 | OPEN — SYN/ACK | OPEN — a matching reply |
| 1 | 0 | CLOSED — RST | CLOSED — ICMP port-unreachable |
| 0 | 0 | FILTERED / DROPPED / DOWN — no reply | OPEN\|FILTERED / DOWN — silence |
| 0 | 1 | impossible | impossible |

A refused port counts as a received reply (`probe.up = 1`, `packet_loss = 0`, status
`success`, `port_open = 0`). For TCP the refusal is a RST (connect-mode `ECONNREFUSED`
treated identically to a SYN-mode RST); for UDP it is an ICMP port-unreachable delivered
as `ECONNREFUSED`. Only silence (timeout) is loss. `port_open` is `NaN` for ICMP/HTTP and
on `error`.

## Consequences

- Extends the binary-gauge idiom of ADR-0002/0005 instead of adding a label dimension.
- UDP's `open|filtered` ambiguity is inherent (no handshake): a quietly-open service and a
  dropped packet are both silence. UDP positively confirms only *open* (reply) or *closed*
  (ICMP unreachable); a protocol-appropriate `payload`/`expected_response` narrows it.
- PromQL: open = `trimon_probe_port_open == 1`; closed = `trimon_probe_up == 1 and
  trimon_probe_port_open == 0`; unreachable/filtered = `trimon_probe_up == 0`. Scope with
  the `probe_type` label.
