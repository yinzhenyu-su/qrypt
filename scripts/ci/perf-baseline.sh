#!/usr/bin/env bash
set -euo pipefail

# Measure the resident Core startup and cache-reuse baseline (issue #31).
# The Go test prints a markdown table; this script also appends it to the
# GitHub job summary so nightly runs keep a visible trend of the numbers that
# gate the persistent list cache (#32).

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

export QRYPT_PERF_BASELINE=1
export QRYPT_LANG=en

REPORT="$(mktemp)"
trap 'rm -f "$REPORT"' EXIT

QRYPT_PERF_REPORT="$REPORT" go test ./internal/cli \
  -run '^TestResidentCoreBaseline$' \
  -count=1 \
  -v \
  -timeout 5m

echo
cat "$REPORT"

if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  {
    echo "## Resident Core baseline"
    echo
    cat "$REPORT"
  } >>"$GITHUB_STEP_SUMMARY"
fi
