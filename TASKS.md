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
**Status:** DONE (v0.3.0)  
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

### OPT-18 · HIGH — Remove RTT statistics from HTTP probe; replace with single-request duration
**Status:** DONE
**Files:** `internal/probe/http/http.go`, `internal/probe/http/http_unit_test.go`, `pkg/types/types.go`, `docs/metrics.md`, `internal/exporter/otlp/otlp.go`, `internal/exporter/stdout/stdout.go`

**Context:** HTTP and ICMP have different statistical semantics. For ICMP, `Count=N` sends N independent packets over the same network path; min/mean/max/stddev meaningfully describe network jitter. For HTTP, requests 2–N reuse the TCP connection (skipping DNS + TCP handshake + TLS), producing a bimodal RTT distribution that makes stddev and mean actively misleading. No comparable HTTP monitoring tool (blackbox_exporter, Datadog Synthetics, Checkly) computes repeated-request RTT statistics — they all measure a single request.

**Action:**
1. Remove `Count` and `PacketInterval` semantics from the HTTP prober — always send exactly one request per probe tick. The scheduler cadence (`Interval`) already controls how often the probe runs.
2. Drop `RTTMinMS`, `RTTMeanMS`, `RTTMaxMS`, `RTTStddevMS` from `ProbeResult` for HTTP results; replace with a single `DurationMS float64` representing wall-clock time from request start to body drain.
3. Consider whether `PacketsSent`/`PacketsReceived`/`PacketLossRatio` still make sense for a single-request model, or whether HTTP should use a simpler `up bool` equivalent. If kept, they are always 1/0–1/0.0–1.0.
4. Update `docs/metrics.md` to reflect the new HTTP metric shape.
5. Update unit tests accordingly; remove tests that were specifically exercising multi-request RTT behaviour.

**Note:** `probe.RTTStats` in `internal/probe/targets.go` becomes unused after this change — delete it if no other prober calls it.

---

### OPT-19 · MEDIUM — Add per-phase HTTP timing breakdown via httptrace
**Status:** OPEN
**Depends on:** OPT-18
**Files:** `internal/probe/http/http.go`, `pkg/types/types.go`, `docs/metrics.md`

**Context:** After reducing HTTP to a single request (OPT-18), the single `DurationMS` field blends DNS resolution, TCP handshake, TLS handshake, time-to-first-byte (TTFB), and body transfer into one opaque number. This mirrors what blackbox_exporter provides via `probe_http_duration_seconds{phase}` and is the most actionable HTTP metric for diagnosing latency — it answers "is it DNS, the network, or the server?" without requiring a distributed trace.

**Action:**
1. Use `net/http/httptrace.ClientTrace` to capture phase timestamps inside `probeOne`. No new dependency — `net/http/httptrace` is stdlib.
2. Populate new fields on `ProbeResult` (or a nested `HTTPTimings` struct):
   - `DNSLookupMS` — time from DNS start to DNS done (zero if target was a bare IP)
   - `TCPConnectMS` — time from connect start to connect done
   - `TLSHandshakeMS` — time from TLS start to TLS done (zero for plain HTTP)
   - `TTFBMS` — time from request sent to first response byte received
   - `TransferMS` — time from first byte to body fully read
   - `TotalDurationMS` — replaces `DurationMS` from OPT-18 (sum of all phases)
3. All phase fields are zero when the phase did not occur (e.g. `TLSHandshakeMS=0` for HTTP, `DNSLookupMS=0` for bare-IP targets).
4. Update `docs/metrics.md` and the Prometheus/OTLP instrument definitions in `internal/exporter/otlp/otlp.go`.
5. Add unit tests that verify each phase field is populated or zero as expected.

**Note:** pro-bing's `HTTPCaller` also wraps `httptrace` but its scheduler model conflicts with trimon's. Implement directly in `http.go` rather than adopting `HTTPCaller`.

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

### OPT-16 · LOW — add resolve_fqnd_every
**Status:** OPEN  
**Depends on:** none  
**Context:** we could add a config option to decide when we want to resolve target names again, instead of resolving them at each probe.

### OPT-17 · MID - simpler target groups
**Status:** OPEN  
**Depends on:** none  
**Context:** revert to single target and solve target grouping by simply using common custom labels. Reduces responsibility of trimon by delegating complexity downstream.