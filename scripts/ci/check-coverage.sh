#!/usr/bin/env bash
# Coverage gate for the reliability-critical packages. Fails (exit 1) when
# any package drops below its floor, so coverage can only move forward.
#
# Floors are each package's measured coverage minus one point, so the gate
# catches a real regression instead of sitting several points below the
# suite. Re-measure with `-print` before changing them; latest local snapshot
# (2026-11-16, darwin/arm64, go1.27.4):
#   vfs 82.1  core 75.8  drive 78.7  sync 83.8  config 85.1  crypt 80.8
# Note drive/config/crypt were measured well above their floors: the floors
# come from earlier measurements and should be tightened in a dedicated PR
# (raising a floor is intentional, lowering one needs a measured regression).
#
# Coverage is not perfectly reproducible: the same commit measured vfs 81.0%
# and 80.9% on two nightly runs (2026-11-10), so the gate allows a small
# measurement margin below the floor before failing.
#
# sync is measured via -coverpkg against the CLI integration tests plus its
# own unit tests (single-package coverage alone is ~15%, misleadingly low).
#
# Raising a floor is intentional: edit the FLOOR map and commit the new
# number with a note of what was added. Use `./scripts/ci/check-coverage.sh -print`
# to see current values without failing.
set -euo pipefail
cd "$(dirname "$0")/../.."

declare -A FLOOR=(
  [pkg/vfs]=81
  [pkg/core]=75
  [pkg/drive]=69
  [pkg/syncer]=82
  [pkg/config]=83
  [pkg/crypt]=79
)

# Measurement noise tolerance (percentage points): the same commit can measure
# a few tenths below the floor just because of scheduling/timing, which is not
# a coverage regression. Fail only below floor - MARGIN; warn inside the band.
MARGIN=0.2

pkg_total() { # $1 = profile
  # syncer is measured with -coverpkg, so the profile's global total is the
  # package's coverage; for the others the profile only contains the package.
  go tool cover -func="$1" | tail -1 | awk '{print $NF}' | tr -d '%'
}

run_one() { # $1 = package, $2 = profile, $3 = log
  local pkg=$1 profile=$2 log=$3
  if [ "$pkg" = "pkg/syncer" ]; then
    go test -count=1 -coverpkg=./pkg/syncer/ -coverprofile="$profile" \
      ./internal/cli/ ./pkg/syncer/ >"$log" 2>&1
  else
    go test -count=1 -coverprofile="$profile" "./$pkg" >"$log" 2>&1
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
  total=$(pkg_total "${profile_of[$pkg]}")
  floor=${FLOOR[$pkg]}
  printf '  %-16s %5s%%  (floor %d%%)\n' "$pkg" "$total" "$floor"
  if [ "$print_mode" = false ]; then
    if awk -v t="$total" -v f="$floor" -v m="$MARGIN" 'BEGIN{exit !(t+0 < f+0-m)}'; then
      echo "  !! $pkg dropped below its floor (want >= $floor%, margin $MARGIN)"
      fail=1
    elif awk -v t="$total" -v f="$floor" 'BEGIN{exit !(t+0 < f+0)}'; then
      echo "  note: $pkg is within the ${MARGIN}pt measurement margin of its floor"
    fi
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
