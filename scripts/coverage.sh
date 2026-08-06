#!/usr/bin/env bash
# Coverage baseline for the reliability-critical packages. Prints the
# package totals plus every function under 50% coverage, so a future
# regression can be spotted by diffing the numbers, not by re-reading
# the whole suite.
#
# Baseline (2026-08-06): vfs 75%  core 72%  drive 62%  sync 80% (via CLI
# integration)  config 74%  crypt 79%. New error-path coverage (state
# machine tests + fuzz) targets the gaps in these packages.
set -euo pipefail
cd "$(dirname "$0")/.."

out=$(mktemp)
trap 'rm -f "$out"' EXIT

go test -coverprofile="$out" \
  ./pkg/vfs ./pkg/core ./pkg/drive ./internal/sync \
  ./internal/config ./pkg/crypt

echo
echo "== functions under 50% coverage =="
go tool cover -func="$out" | awk '$3+0 < 50 {print}' | head -30
echo
echo "== totals =="
go tool cover -func="$out" | tail -1
