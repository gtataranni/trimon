# trimon — Audit Task Backlog

Each task is self-contained enough for a sub-agent to pick up and implement.
Severity/priority guides sequencing; dependencies are listed where one task blocks another.

---

## How to use this file

- Pick any OPEN task whose dependencies are DONE.
- Read the **Context** and **Action** sections before touching any file.
- Mark the task `IN PROGRESS` (with your agent ID) while working, `DONE` when complete.
- If you discover the task is more complex than described, add a note and leave it OPEN.

---

## SECURITY

### SEC-10 · MEDIUM — Probe all resolved IPs per hostname target
**Status:** TO BE PLANNED  
**Files:** `internal/config/config.go` (`resolveTarget`), `internal/probe/icmp/icmp.go`, `pkg/types/types.go`, `internal/scheduler/scheduler.go`  
**Context:** `resolveTarget()` validates that a hostname resolves but the IP is discarded. At probe time, `probing.NewPinger(target)` re-resolves, meaning probes can be silently redirected if DNS changes. The naive fix (pin the IP at config load) breaks legitimate cases: CDNs, DNS-based load balancers, and any target whose IP rotates intentionally. The correct approach is to **re-resolve on every probe run and probe all currently-returned IPs**, so trimon faithfully measures the actual reachability of the service as DNS presents it — not just one arbitrary IP pinned at startup.

**Plan required:** Before implementing, produce a written design covering:
1. **Per-run resolution model**: on each scheduler tick, resolve the hostname, get the current IP list, and run one ICMP probe per IP. How does this interact with the scheduler's one-goroutine-per-probe model? Does each resolved IP become its own sub-probe, or does one probe fan-out internally?
2. **ProbeResult granularity**: does each resolved IP produce its own `ProbeResult` (with `probe.target = "8.8.8.8"` and a new `probe.hostname = "dns.google"` attribute), or are all IPs aggregated into one result? Consider what makes dashboards most useful.
3. **Metric cardinality**: if a hostname resolves to 8 IPs and each produces its own time-series, that multiplies cardinality. Is this acceptable? Should there be a configurable `max_resolved_ips` cap?
4. **Failure semantics**: if 3 of 4 IPs are unreachable, what is the aggregate status? Does it depend on the use case (any-up vs all-up)?
5. **Backward compatibility**: existing dashboards filter on `probe.target = "hostname"`. If target becomes an IP, dashboards break. A `probe.hostname` label preserves this.

Only begin coding once the design is confirmed.

---

## OPTIMIZATION

### OPT-14 · LOW — Emit NaN for RTT gauges on failure/error instead of retaining last value
**Status:** OPEN  
**Depends on:** none  
**Files:** `internal/exporter/otlp/otlp.go`, `docs/metrics.md`, `docs/observability-latency.md`  
**Context:** RTT gauges (`rtt.min/mean/max/stddev`) are synchronous OTel `Float64Gauge` instruments. On `failure` or `error` status there is no RTT to report. Two approaches were considered:

- **Retain last value** (currently reverted): skip `Record()` calls so the gauge holds its last good measurement. Honest about the current RTT being unknown, but produces a flat line in Grafana during an outage — visually easy to miss if probe_up is not on the same panel.
- **Record 0ms** (original behaviour): semantically wrong; 0ms RTT is physically impossible and corrupts alerting rules that threshold on latency.
- **Emit NaN** (desired): consistent with `packet_loss_ratio` which already uses `math.NaN()` on `error`. NaN causes Grafana to **break the graph line**, which is visually striking and unambiguous — more visible than a flat line and honest (means "no measurement").

The correct behaviour is NaN on both `failure` and `error`, mirroring the packet_loss convention already in the codebase.

**Action:**
1. In `Export()` in `otlp.go`, after the `switch` block, record `math.NaN()` for all four RTT gauges when `!r.Status.IsUp()` (i.e. add an `else` branch to the existing `if r.Status.IsUp()` guard, or restructure the condition). Do not skip the `Record()` call — emit NaN explicitly.
2. Update the **Notes** column for all four RTT rows in `docs/metrics.md` to read: `NaN on failure/error`.
3. Remove or update the "Restart behaviour" section added to `docs/observability-latency.md` — the rationale there was written for the "retain last value" approach and no longer applies.

---

## TRACEABILITY

---


# Future tasks to re-evaluate once further down in the roadmap

### OPT-13 · LOW — Unify probe timeout through context; remove pinger.Timeout from ICMP prober
**Status:** OPEN  
**Depends on:** none  
**Files:** `internal/probe/icmp/icmp.go`, `internal/probe/icmp/icmp_test.go`  
**Context:** The scheduler already wraps each probe run with `context.WithTimeout(ctx, cfg.Timeout)` (scheduler.go:121). The ICMP prober also sets `pinger.Timeout = p.cfg.Timeout`, creating a double timeout: whichever fires first wins, but both represent the same deadline. This redundancy means the timeout is set in two places with no clear single source of truth.

The Prober interface contract should be: **the caller embeds the timeout in the context; Run() uses the context deadline as its sole stopping signal**. This makes future probe implementations (TCP, HTTP) follow a uniform pattern without needing to know about per-library timeout fields.

Integration tests confirmed that `context.WithTimeout` cancellation produces the same partial-stats behaviour as `pinger.Timeout` — loss ratio is computed against actual packets sent, and the probe returns `StatusFailure` cleanly.

**Action:**
1. In `icmp.go`, remove the line `pinger.Timeout = p.cfg.Timeout`. The pinger will rely entirely on `RunWithContext(ctx)` for its deadline.
2. Verify that pro-bing does not run forever when `pinger.Timeout == 0` and a context deadline is set. If it does, document this assumption with a comment.
3. In `icmp_test.go`, update the two tests that use `context.Background()` to instead use `context.WithTimeout(context.Background(), cfg.Timeout)`, mirroring what the scheduler does. This ensures integration tests exercise the same code path as production.
4. Add a comment to the `Run()` signature: the context must carry a deadline; callers that omit one will block until all packets are sent (no built-in fallback timeout).