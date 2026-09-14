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
  [pkg/syncer]=79
  [pkg/config]=74
  [pkg/crypt]=79
)

pkg_total() { # $1 = profile, $2 = package path prefix
  local profile=$1 prefix=$2
  if [ "$prefix" = "pkg/syncer" ]; then
    # profile covers only sync via -coverpkg, so the global total is ours.
    go tool cover -func="$profile" | tail -1 | awk '{print $NF}' | tr -d '%'
  else
    go tool cover -func="$profile" | tail -1 | awk '{print $NF}' | tr -d '%'
  fi
}

run_one() { # $1 = package, $2 = profile, $3 = log
  local pkg=$1 profile=$2 log=$3
  if [ "$pkg" = "pkg/syncer" ]; then
    go test -coverpkg=./pkg/syncer/ -coverprofile="$profile" \
      ./internal/cli/ ./pkg/syncer/ >"$log" 2>&1
  else
    go test -coverprofile="$profile" "./$pkg" >"$log" 2>&1
  fi
}

print_mode=false
[ "${1:-}" = "-print" ] && print_mode=true

fail=0
run_fail=0
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

echo "== coverage gate =="
# The six profiles are independent test binaries and each is mostly waiting on
# timers rather than burning CPU, so they overlap well: the gate then costs the
# slowest package instead of the sum of all six.
declare -A pid_of profile_of log_of
for pkg in "${!FLOOR[@]}"; do
  profile_of[$pkg]="$tmpdir/$(echo "$pkg" | tr '/' '_').out"
  log_of[$pkg]="$tmpdir/$(echo "$pkg" | tr '/' '_').log"
  run_one "$pkg" "${profile_of[$pkg]}" "${log_of[$pkg]}" &
  pid_of[$pkg]=$!
done

for pkg in "${!FLOOR[@]}"; do
  if ! wait "${pid_of[$pkg]}"; then
    echo "  !! $pkg: coverage run failed"
    sed 's/^/     /' "${log_of[$pkg]}"
    run_fail=1
    continue
  fi
  total=$(pkg_total "${profile_of[$pkg]}" "$pkg")
  floor=${FLOOR[$pkg]}
  printf '  %-16s %5s%%  (floor %d%%)\n' "$pkg" "$total" "$floor"
  if [ "$print_mode" = false ] && awk -v t="$total" -v f="$floor" 'BEGIN{exit !(t+0 < f+0)}'; then
    echo "  !! $pkg dropped below its floor (want >= $floor%)"
    fail=1
  fi
done

# -print skips the floor gate, but a package whose tests could not even run
# is still a failure: the report would otherwise silently lose that row.
if [ "$run_fail" -ne 0 ]; then
  echo "== FAIL: a coverage run did not complete; see the output above =="
  exit 1
fi
if [ "$print_mode" = true ]; then
  echo "(print mode: no gate applied)"
  exit 0
fi
if [ "$fail" -ne 0 ]; then
  echo "== FAIL: coverage regressed; add tests or raise the floor in $0 =="
  exit 1
fi
echo "== coverage gate passed =="
