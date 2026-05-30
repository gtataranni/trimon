# Contributing to trimon

Thanks for your interest in improving **trimon** — push-based Target Reachability
Inspection and Monitoring. This guide covers how to set up a dev environment, the
standards a change must meet, and how to get it merged.

For what trimon *is* and how to run it, start with the [README](README.md). For the
phase plan and what's in/out of scope, see [ROADMAP.md](ROADMAP.md). For the active
work backlog, see [TASKS.md](TASKS.md).

---

## Ways to contribute

- **Report a bug or request a feature** — open an issue. Include trimon version
  (`trimon --version`), OS, your probe/ops config (redacted), and what you expected vs.
  observed.
- **Pick up a task** — [TASKS.md](TASKS.md) holds self-contained, prioritized tasks.
  Pick any `OPEN` task whose dependencies are `DONE`, read its **Context** and **Action**
  sections, and follow the workflow described at the top of that file. Some tasks require
  a written design to be confirmed before coding — respect that.
- **Send a pull request** — see [Pull request process](#pull-request-process) below.

Before starting non-trivial work, open an issue so we can agree on the approach. trimon
follows a phased roadmap; a change that doesn't fit the current phase will be asked to
wait (see [Scope and roadmap](#scope-and-roadmap)).

---

## Development environment

### Prerequisites

- **Go** — the version pinned in [go.mod](go.mod) (currently 1.25) or newer.
- **No CGO.** trimon is pure Go (`CGO_ENABLED=0`); do not introduce CGO.
- **golangci-lint** (v2) — for `make lint`. See the config in [.golangci.yaml](.golangci.yaml).
- **Linux** — trimon is a **Linux-only** project; macOS, Windows, and other OSes are out
  of scope for now (see [ROADMAP.md](ROADMAP.md)). ICMP and source-IP binding need Linux
  raw sockets / `CAP_NET_RAW`. You can develop on a non-Linux host by building and running
  via containers (`make container`, `make dev-stack`), but don't add platform-specific
  support for non-Linux runtime targets.

### Build, test, lint

```bash
make build   # compile ./bin/trimon (CGO disabled, version/commit stamped)
make test    # go test -race -count=1 ./...
make lint    # golangci-lint run ./...
```

Run all three before opening a PR. To run trimon locally after building (Linux):

```bash
sudo setcap cap_net_raw+ep ./bin/trimon
./bin/trimon --config config.example.yaml --probes probes.example.yaml --log-level debug
```

For an end-to-end stack (trimon + OTel Collector, optionally Prometheus + Grafana), use
`make dev-stack` / `make demo` — see the README's [Development](README.md#development)
section and [examples/](examples/).

---

## Architecture orientation

`internal/` holds the implementation; `pkg/types/` holds the few types an external
consumer might import. The pipeline is: config → scheduler (one goroutine + ticker per
probe) → probers → buffered result channel → exporters (stdout / OTLP + Prometheus
bridge). The README's [How it works](README.md#how-it-works) diagram is the quickest map.

### Two load-bearing interfaces

The entire design hangs off two interfaces:

- [`internal/probe/prober.go`](internal/probe/prober.go) — `Prober`
- [`internal/exporter/exporter.go`](internal/exporter/exporter.go) — `Exporter`

**These are stable on purpose.** If your change requires modifying either interface,
**stop and flag it in your issue/PR before writing code** — it affects every probe and
exporter implementation and needs to be agreed upfront.

### Adding a probe type or exporter

These follow a fixed recipe — see the README:
[Adding a new probe type](README.md#adding-a-new-probe-type) and
[Adding a new exporter](README.md#adding-a-new-exporter). Implement against the interface
above; don't special-case the factory beyond the documented wiring points.

---

## Coding standards

Follow Go best practices, KISS, and DRY. Match the style of the surrounding code.

- **Formatting:** `gofmt` clean (enforced by `make lint`).
- **Errors:** wrap with context — `fmt.Errorf("doing x: %w", err)`. No bare returns of
  upstream errors.
- **Logging:** structured `log/slog`; JSON by default, text under `--log-format=text`.
- **Context:** every blocking call takes a `context.Context`. No `context.Background()`
  outside `cmd/trimon/main.go`.
- **Concurrency:** every goroutine has a clear owner and a shutdown path (context cancel
  or closed channel). No fire-and-forget.
- **No global state** outside `cmd/trimon/main.go`.
- **Dependencies:** keep the set small and prefer the standard library. Don't add a
  dependency without a clear reason raised in the issue/PR first.

### Status semantics — don't collapse `failure` and `error`

A `ProbeResult.status` is one of `success` (0% loss), `partial` (0 < loss < 100%),
`failure` (100% loss — the probe ran and the target didn't answer), or `error` (trimon
couldn't run the probe at all). `failure` and `error` are **different**; keep them
distinct.

### Metrics and label cardinality

Metrics are defined once via the OTel SDK in `internal/exporter/otlp/otlp.go` and exposed
on `/metrics` through the Prometheus bridge. The full spec lives in
[docs/metrics.md](docs/metrics.md) — read it before adding or changing a metric. In
particular: **never put per-run or high-churn values** (ephemeral ports, IDs, timestamps,
measured latencies, drifting counters) into `ProbeResult.Labels` — every label becomes a
metric attribute on every series and creates an unbounded cardinality leak. A value may be
a label only if its domain is small and stable.

---

## Tests

- Tests are **table-driven** and run with the race detector (`make test`).
- New behavior needs tests. Bug fixes should include a regression test.
- Tests are **required** for these packages when you touch them:
  - `internal/config` — validation
  - `pkg/types`
  - `internal/scheduler` — lifecycle
  - `internal/exporter/stdout` — output shape
- Integration tests that need a live Collector are behind the `integration` build tag.

---

## Commit conventions

trimon uses [Conventional Commits](https://www.conventionalcommits.org/):

```
feat(probe): add DNS probe with answer validation
fix(scheduler): stop ticker on reload diff
docs(metrics): clarify NaN-on-error semantics
```

Common types: `feat`, `fix`, `refactor`, `docs`, `test`, `chore`. Keep commits focused
and the history readable.

### Sign your commits (DCO)

All commits **must be signed off** under the
[Developer Certificate of Origin](https://developercertificate.org/) — a lightweight
statement that you have the right to submit the contribution under the project's license.
Add the sign-off with `-s`:

```bash
git commit -s -m "feat(probe): add UDP probe"
```

This appends a trailer to your commit message:

```
Signed-off-by: Your Name <you@example.com>
```

The name and email must match your Git identity (`git config user.name` / `user.email`).

### AI-assisted contributions

If a commit was produced with help from an AI coding tool, add an `Assisted-by:` trailer
instead of `Co-Authored-By:` — e.g.:

```
Assisted-by: claude-code:claude-sonnet-4-6
```

A commit can carry both `Signed-off-by:` and `Assisted-by:` trailers. You remain
responsible for everything you submit, including the DCO sign-off.

---

## Pull request process

1. **Fork** the repo and create a topic branch off `main`
   (`git checkout -b feat/udp-probe`).
2. Make your change. Keep it scoped to one logical concern.
3. Run `make test` and `make lint` — both must pass.
4. Commit with a conventional message, signed off (`-s`).
5. Push and open a PR against `main`. Describe **what** changed and **why**, and link the
   issue or `TASKS.md` task it addresses.

### PR checklist

- [ ] `make test` passes (race detector, `-count=1`)
- [ ] `make lint` passes and code is `gofmt`-clean
- [ ] Tests added/updated for the changed behavior (and for the required packages above)
- [ ] Docs updated ([README](README.md), [docs/](docs/)) if behavior, config, or metrics changed
- [ ] Commits are conventional and **signed off** (`git commit -s`)
- [ ] Change stays within the current roadmap phase
- [ ] If you touched the `Prober` or `Exporter` interface, you flagged it in the PR description

---

## Scope and roadmap

trimon is built in phases, each ending in a tagged release. **Do not start phase N+1 work
in a phase N PR.** Before implementing, check [ROADMAP.md](ROADMAP.md) for the current
phase and [TASKS.md](TASKS.md) for the active backlog — if your idea isn't in the current
phase, open an issue to discuss it rather than bundling it in.

Releases are cut by maintainers via `make release V=vX.Y.Z` (see the README's
[Releasing](README.md#releasing) section).

---

## License

trimon is licensed under [Apache 2.0](LICENSE). By contributing — and by signing off your
commits under the DCO — you agree that your contributions are licensed under the same
terms.
