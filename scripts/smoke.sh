#!/usr/bin/env bash
#
# --- HELP-START
# smoke.sh — end-to-end smoke test for trimon.
#
# Orchestration layer of the smoke suite: it builds and starts the lean
# dev-stack (the real Linux binary + OTel Collector) via compose, waits for the
# trimon HTTP server to come up, then runs the Go assertion layer
# (test/smoke, build tag `smoke`) which checks that every probe type reports
# through /metrics and that results reach the collector over OTLP.
#
# The daemon is Linux-only, so it runs in a container; the Go assertions are
# plain HTTP clients and run on the host. Probes hit public targets
# (8.8.8.8, 1.1.1.1, example.com), so this needs outbound network at run time.
#
# Usage:
#   scripts/smoke.sh [--keep] [--no-build] [--runtime <docker|podman>]
#
#   --keep                 leave the stack running on exit (for debugging)
#   --no-build             skip the image rebuild (reuse the existing image)
#   --runtime <r>          container runtime (default: $CONTAINER_RUNTIME or docker)
#
# Env overrides honored by the Go layer:
#   TRIMON_BASE_URL, OTELCOL_METRICS_URL, SMOKE_TIMEOUT
# --- HELP-END

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(dirname "$SCRIPT_DIR")"
COMPOSE_FILE="$ROOT/examples/local-stack/docker-compose.yml"

RUNTIME="${CONTAINER_RUNTIME:-docker}"
KEEP=0
BUILD_ARG="--build"
HEALTHZ_URL="${TRIMON_BASE_URL:-http://localhost:8080}/healthz"
HEALTHZ_TIMEOUT=60

print_usage() { awk '/^# --- HELP-START/{f=1; next} /^# --- HELP-END/{exit} f{ sub(/^# ?/,""); print }' "$0"; }

while [ $# -gt 0 ]; do
  case "$1" in
    --keep)      KEEP=1 ;;
    --no-build)  BUILD_ARG="" ;;
    --runtime)   RUNTIME="$2"; shift ;;
    -h|--help)
      print_usage
      exit 0 ;;
    *)
      echo "smoke: unknown argument '$1'" >&2
      exit 2 ;;
  esac
  shift
done

compose() { "$RUNTIME" compose -f "$COMPOSE_FILE" "$@"; }

# Invoked indirectly via `trap teardown EXIT` below.
# shellcheck disable=SC2329
teardown() {
  if [ "$KEEP" -eq 1 ]; then
    echo ""
    echo "smoke: --keep set; leaving the stack running."
    echo "       tear down with: $RUNTIME compose -f $COMPOSE_FILE --profile demo down -v"
    return
  fi
  echo ""
  echo "smoke: tearing down the stack..."
  compose --profile demo down -v >/dev/null 2>&1 || true
}
trap teardown EXIT

echo "smoke: starting dev-stack (runtime=$RUNTIME)..."
# shellcheck disable=SC2086  # BUILD_ARG is intentionally word-split (empty or --build)
compose up $BUILD_ARG -d

echo "smoke: waiting for $HEALTHZ_URL (up to ${HEALTHZ_TIMEOUT}s)..."
deadline=$(( $(date +%s) + HEALTHZ_TIMEOUT ))
until curl -fsS "$HEALTHZ_URL" >/dev/null 2>&1; do
  if [ "$(date +%s)" -ge "$deadline" ]; then
    echo "smoke: FAIL — trimon /healthz not ready within ${HEALTHZ_TIMEOUT}s" >&2
    echo "----- trimon logs -----" >&2
    compose logs --tail 50 trimon >&2 || true
    exit 1
  fi
  sleep 2
done
echo "smoke: trimon is up; running Go assertions..."

rc=0
( cd "$ROOT" && go test -tags smoke -count=1 -v ./test/smoke/... ) || rc=$?

echo ""
if [ "$rc" -eq 0 ]; then
  echo "smoke: PASS — all probe types report end-to-end."
else
  echo "smoke: FAIL — assertions did not pass (exit $rc)." >&2
  echo "----- trimon logs -----" >&2
  compose logs --tail 50 trimon >&2 || true
fi
exit "$rc"
