// Package smoke holds trimon's end-to-end smoke suite: the assertion layer that
// drives a running dev-stack (trimon + OTel Collector) over HTTP and checks that
// every probe type produces well-formed metrics through the real binary and the
// real OTLP push path.
//
// The network-touching tests in smoke_test.go are guarded by the `smoke` build
// tag and are meant to run against an already-running stack — see scripts/smoke.sh,
// wired to `make smoke` — not as part of `go test ./...`. The tag-free helpers in
// parse.go (Prometheus text parsing) are unit-tested in parse_test.go and run with
// the normal test suite.
package smoke
