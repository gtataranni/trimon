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

### SEC-08 · HIGH — Split config into user-facing (probes) and ops/sensitive (exporters, TLS, server)
**Status:** TO BE PLANNED  
**Files:** `internal/config/config.go`, `cmd/trimon/main.go`, `internal/server/server.go`, `config.example.yaml`, `config.docker.yaml`  
**Context:** The `/config` endpoint dumps the full config including OTLP endpoint URLs (may contain credentials), TLS cert/key file paths, and retry settings. These are not fields a typical operator ever needs to inspect at runtime, and they are not fields end-users care about changing frequently. The deeper fix — rather than redacting individual fields — is to split config into two files: a **probe config** (targets, labels, intervals — the thing users edit and want to inspect) and an **ops config** (exporters, TLS, server listen, OTLP endpoint — set once by the platform team and kept secure). The `/config` endpoint then only dumps the probe config file.

**Plan required:** Before implementing, produce a written design covering:
1. What exactly goes in each file — draw the proposed `ProbeConfig` file structure and the proposed `OpsConfig` file structure.
2. How the two files are loaded (two separate `--config` and `--ops-config` flags? or a single directory? or a `!include` directive?).
3. How hot-reload works for each — should ops config be reloadable at all, or is it load-once-at-startup?
4. Backward compatibility — what happens if a user passes a single config file that contains everything (migration path)?
5. What `/config` exposes after the split, and whether the ops config endpoint should exist at all.

Only begin coding once the design is confirmed. The plan document should be left as a comment on this task or as a separate `docs/config-split.md`.

---

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

### SEC-14 · LOW — Make OTLP Close() flush timeout configurable
**Status:** DONE  
**See also:** SEC-08 (if SEC-08 is completed first, `OTLPExporterConfig` may move to a new struct; add the field to wherever it lands)  
**Files:** `internal/exporter/otlp/otlp.go` (`Close()` method)  
**Context:** `Close()` uses a hard-coded 30-second timeout. In fast-shutdown environments (e.g., Kubernetes with 30s `terminationGracePeriodSeconds`), this competes with the pod's grace period. A configurable timeout would let operators tune it.  
**Action:**
1. Add a `ShutdownTimeout time.Duration` field to `OTLPExporterConfig` in `config.go` (yaml tag `shutdown_timeout`, default `10s`).
2. In `otlp.go`, store the configured timeout in the `Exporter` struct and use it in `Close()`.
3. Update `config.example.yaml` to show the option (commented out).

---

### SEC-15 · LOW — Extend /healthz to reflect pipeline buffer saturation
**Status:** DONE  
**See also:** OPT-12 (configurable buffer size makes the saturation threshold more meaningful; do together or OPT-12 first)  
**Files:** `internal/pipeline/pipeline.go`, `internal/server/server.go`  
**Context:** `/healthz` always returns `200 OK {"status":"ok"}` even when the results channel is 100% full and results are being dropped. Operators relying on health checks for load balancer routing get a false signal.  
**Action:**
1. Add a `BufferUsage() float64` method to `Pipeline` that returns `len(p.results) / cap(p.results)` as a ratio.
2. In `server.go`, store a reference to the pipeline (or just the checker function via `SetHealthChecker(fn func() bool)`).
3. In `handleHealthz()`, if buffer usage > 0.9 (configurable threshold), return `503 Service Unavailable {"status":"degraded","reason":"results buffer near capacity"}`.

---

## OPTIMIZATION

### OPT-07 · MEDIUM — Add integration smoke test for cmd/trimon/main.go
**Status:** DONE  
**See also:** OPT-01, OPT-06 (integration test is most valuable once the ICMP unit tests and ErrorType field are in place; gaps in unit coverage make it harder to isolate failures found here)  
**Files:** `cmd/trimon/main_test.go` (create new file)  
**Context:** No test covers the main wiring: config → scheduler → pipeline → exporter. If wiring is broken, only a live run reveals it. A test with a minimal in-memory config would catch most integration bugs.  
**Action:**
1. Create `cmd/trimon/main_test.go` with build tag `//go:build integration`.
2. Write a test that: loads `config.example.yaml`, starts the daemon with a 2-second timeout context, verifies at least one ProbeResult is produced, and verifies the HTTP `/healthz` endpoint returns 200.
3. Use a loopback target (127.0.0.1) so the test works without network access.

---

### OPT-08 · LOW — Inline buildExporters() into main()
**Status:** DONE  
**Files:** `cmd/trimon/main.go` (`buildExporters` function)  
**Context:** `buildExporters()` is a 6-line function called exactly once. It adds a layer of indirection for no reuse benefit and slightly obscures the startup sequence when reading `main()`.  
**Action:**
1. Delete `buildExporters()`.
2. Inline its logic directly in `main()` before the `pipeline.New(...)` call.

---

### OPT-09 · LOW — Document discarded error in srv.Shutdown()
**Status:** OPEN  
**Files:** `cmd/trimon/main.go` (~line 118)  
**Context:** `_ = srv.Shutdown(shutCtx)` silently discards a shutdown error. While this is intentional (we're exiting anyway), it reads as a missed error.  
**Action:**
1. Replace `_ = srv.Shutdown(shutCtx)` with:
   ```go
   if err := srv.Shutdown(shutCtx); err != nil {
       logger.Warn("http server shutdown error", "error", err)
   }
   ```
   This preserves the non-blocking behavior while surfacing the error in logs.

---

### OPT-10 · LOW — Document RTT field validity conditions
**Status:** OPEN  
**Files:** `pkg/types/types.go` (ProbeResult struct), `internal/probe/icmp/icmp.go`  
**Context:** `RTTMinMS`, `RTTMeanMS`, `RTTMaxMS`, and `RTTStddevMS` are zero when `PacketsReceived == 0`. Consumers (exporters, tests) must know this. Without documentation, a future exporter could naively read RTT fields for a failed probe and report zeros as legitimate measurements.  
**Action:**
1. Add a comment on the RTT fields in `ProbeResult`: `// Only valid when PacketsReceived > 0; zero otherwise.`
2. In `icmp.go`, add a brief comment before the RTT assignment block explaining the conditional.

---

### OPT-11 · LOW — Explain nolint:nilerr in icmp.go
**Status:** OPEN  
**Files:** `internal/probe/icmp/icmp.go` (lines with `//nolint:nilerr`)  
**Context:** The prober returns `(result, nil)` even when it encountered an error — the error is embedded in `result.Status` and `result.ErrorMsg`. The nolint suppresses the linter complaint but future readers might remove it or be confused by the pattern.  
**Action:**
1. Add a comment before each nolint line: `// Error is embedded in result.Status/ErrorMsg so the pipeline processes all probes uniformly.`

---

### OPT-12 · LOW — Make pipeline buffer size configurable
**Status:** DONE  
**See also:** SEC-08 (if SEC-08 is completed first, put `PipelineConfig` in the ops config section, not the probe config section); SEC-15 (depends on this for a meaningful buffer saturation threshold)  
**Files:** `internal/pipeline/pipeline.go`, `internal/config/config.go`, `cmd/trimon/main.go`  
**Context:** `bufferSize = 1000` is hardcoded. Deployments with hundreds of fast probes may overflow it; deployments with a handful of slow probes waste memory.  
**Action:**
1. Add `Pipeline.BufferSize int` to a new `PipelineConfig` section in config (yaml tag `pipeline.buffer_size`, default `1000`).
2. Pass `bufferSize` as a parameter to `pipeline.New()`.
3. Update `main.go` to pass `cfg.Pipeline.BufferSize`.
4. Update `config.example.yaml` with a commented-out example.

---

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

---

## TRACEABILITY

### TRC-01 · SUSPECT — Document pipeline shutdown contract
**Status:** OPEN  
**Depends on:** OPT-03 (the comments must describe the refactored shutdown behavior; writing them against the current lock-cycling code means rewriting them again after OPT-03)  
**Files:** `internal/pipeline/pipeline.go`, `cmd/trimon/main.go`  
**Context:** The pipeline's results channel is never explicitly closed. Safety depends on the invariant that `sched.Stop()` fully drains all probe workers *before* `cancel()` is called. If this order is violated, workers could try to send on a channel with no receiver after `pipe.Run()` exits.  
**Action:**
1. In `pipeline.go`, add a comment at the top of `Run()` explaining the required shutdown sequence: callers must stop all senders before cancelling context.
2. In `main.go`, add a brief comment at the shutdown block (lines ~108–112) explicitly documenting the order: `// Shutdown order: stop senders → cancel pipeline context → wait for drain → close exporters`.
3. Consider: after the drain loop exits in `Run()`, close `p.results` so any late sender panics loudly (fail-fast) rather than blocking silently.

---

### TRC-02 · SUSPECT — Add metric for dropped results
**Status:** OPEN  
**See also:** SEC-13 (both register new counters in `otlp.go`; coordinate so the two counters are defined together and follow the same naming convention, avoiding separate PRs that each add one instrument)  
**Files:** `internal/scheduler/scheduler.go` (~lines 127–131)  
**Context:** When the pipeline buffer is full, the scheduler logs `"results channel full, dropping result"` and silently moves on. This is a warning in logs but invisible in metrics, so operators have no way to alert on it.  
**Action:**
1. Add a `trimon.probe.results_dropped_total{probe.name}` counter instrument to `otlp.go`.
2. Expose a `RecordDroppedResult(probeName string)` method on the OTLP exporter.
3. In `scheduler.go`, call this method (via the exporter reference or a registered callback) when a result is dropped.
4. Alternatively, add a `DroppedResults int64` counter to `Pipeline` (accessed via `atomic.Int64`) and expose it via an observable gauge.

---

### TRC-03 · SUSPECT — Remove vestigial yaml tags from types.ProbeConfig
**Status:** OPEN  
**Files:** `pkg/types/types.go`  
**Context:** `ProbeConfig` has `yaml:"..."` tags on all fields but is never unmarshalled from YAML — it is constructed by `config.go`. The tags are harmless but misleading: they suggest this struct is parsed directly, which could lead a future developer to add YAML parsing logic in the wrong place.  
**Action:**
1. Remove all `yaml:"..."` tags from `types.ProbeConfig` fields.
2. Add a comment on the struct: `// ProbeConfig is the merged, validated probe configuration built by internal/config. It is not directly unmarshalled from YAML.`

---

### TRC-04 · SUSPECT — Add omitempty handling for RTT fields in stdout JSON
**Status:** OPEN  
**See also:** OPT-10 (OPT-10 adds the comments explaining when RTT fields are valid; TRC-04 is the implementation counterpart — do together or OPT-10 first)  
**Files:** `internal/exporter/stdout/stdout.go`  
**Context:** The stdout JSON record always emits RTT fields (`rtt_min_ms: 0`, etc.) even when the probe failed and RTTs are undefined. A consumer parsing the JSON cannot distinguish "RTT was measured as 0ms" from "RTT was not measured." The `error_msg` field uses `omitempty` — RTT fields should behave consistently.  
**Action:**
1. Change `RTTMinMS`, `RTTMeanMS`, `RTTMaxMS`, `RTTStddevMS`, and `PacketLoss` in `jsonRecord` to use pointer types (`*float64`) so they can be nil/omitted.
2. In `writeJSON()`, only assign these fields when `r.PacketsReceived > 0` (for RTT) and `r.Status != StatusError` (for PacketLoss).
3. Add `omitempty` to those json tags.
4. Update `stdout_test.go` to assert these fields are absent in the error case.
