#!/usr/bin/env bash
# Coverage gate for the reliability-critical packages. Fails (exit 1) when
# any package drops below its floor, so coverage can only move forward.
#
# Floors (2026-08-06, measured on this repo's suite):
#   vfs 75  core 72  drive 62  sync 79  config 74  crypt 79
#
# sync is measured via -coverpkg against the CLI integration tests plus its
# own unit tests (single-package coverage alone is ~15%, misleadingly low).
#
# Raising a floor is intentional: edit the FLOOR map and commit the new
# number with a note of what was added. Use `./scripts/coverage.sh -print`
# to see current values without failing.
set -euo pipefail
cd "$(dirname "$0")/.."

declare -A FLOOR=(
  [pkg/vfs]=75
  [pkg/core]=72
  [pkg/drive]=62
  [internal/sync]=79
  [internal/config]=74
  [pkg/crypt]=79
)

pkg_total() { # $1 = profile, $2 = package path prefix
  local profile=$1 prefix=$2
  if [ "$prefix" = "internal/sync" ]; then
    # profile covers only sync via -coverpkg, so the global total is ours.
    go tool cover -func="$profile" | tail -1 | awk '{print $NF}' | tr -d '%'
  else
    go tool cover -func="$profile" | tail -1 | awk '{print $NF}' | tr -d '%'
  fi
}

run_one() { # $1 = package, $2 = profile
  local pkg=$1 profile=$2
  if [ "$pkg" = "internal/sync" ]; then
    go test -coverpkg=./internal/sync/ -coverprofile="$profile" \
      ./internal/cli/ ./internal/sync/ >/dev/null 2>&1
  else
    go test -coverprofile="$profile" "./$pkg" >/dev/null 2>&1
  fi
}

print_mode=false
[ "${1:-}" = "-print" ] && print_mode=true

fail=0
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

echo "== coverage gate =="
for pkg in "${!FLOOR[@]}"; do
  profile="$tmpdir/$(echo "$pkg" | tr '/' '_').out"
  run_one "$pkg" "$profile"
  total=$(pkg_total "$profile" "$pkg")
  floor=${FLOOR[$pkg]}
  printf '  %-16s %5s%%  (floor %d%%)\n' "$pkg" "$total" "$floor"
  if [ "$print_mode" = false ] && awk -v t="$total" -v f="$floor" 'BEGIN{exit !(t+0 < f+0)}'; then
    echo "  !! $pkg dropped below its floor (want >= $floor%)"
    fail=1
  fi
done

if [ "$print_mode" = true ]; then
  echo "(print mode: no gate applied)"
  exit 0
fi
if [ "$fail" -ne 0 ]; then
  echo "== FAIL: coverage regressed; add tests or raise the floor in $0 =="
  exit 1
fi
echo "== coverage gate passed =="
