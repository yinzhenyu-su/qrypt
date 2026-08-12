#!/usr/bin/env bash
# Layered test entry points. Each layer is a separate command so CI and
# developers can run exactly the coverage they need:
#
#   ./scripts/test-layers.sh fast         # default unit suite
#   ./scripts/test-layers.sh race         # concurrency-heavy packages under -race
#   ./scripts/test-layers.sh vfs-stability # VFS upload engine flake guard (x3)
#   ./scripts/test-layers.sh smoke        # localfs mount smoke test
#   ./scripts/test-layers.sh all          # everything except real-netdisk tests
#   ./scripts/test-layers.sh integration  # real provider/HTTP contract tests
#
# Every layer prints its wall-clock duration. If the fast layer grows past
# ~20s, split the slow package's tests into a separate layer instead of
# letting the default suite creep.
#
# Real-netdisk contract tests are NOT part of go test: they need a running
# `qrypt mount` debug server with mounted cloud accounts. Run them manually:
#   qrypt debug test contract --socket /tmp/qrypt.sock --mount <name>
set -euo pipefail
cd "$(dirname "$0")/.."

measure() {
  local label="$1"; shift
  local start
  start=$(date +%s)
  if [ "$1" = "go" ] && [ "$2" = "test" ] && [ "${3:-}" = "-json" ]; then
    # JSON mode: stream to a temp file so the slowest packages can be
    # reported afterwards. The grep -q exit code drives the result.
    local tmp json_ok
    tmp=$(mktemp)
    "$@" | tee "$tmp" >/dev/null || true
    json_ok=0
    grep -q '"Action":"fail"' "$tmp" && json_ok=1
    if [ "$json_ok" -ne 0 ]; then
      printf '== FAILURES in %s ==\n' "$label"
      awk -F'"' '
        /"Action":"fail"/ && /"Test":"[^"]+"/ {
          for (i=1;i<=NF;i++) if ($i=="Test") print "  FAIL " $(i+2)
        }
      ' "$tmp" | head -20
    fi
    printf '== slowest packages ==\n'
    awk -F'"' '
      /"Action":"pass"/ && !/"Test":"/ && /"Package":"[^"]+"/ && /"Elapsed":[0-9.]+/ {
        pkg=$0; sub(/.*"Package":"/, "", pkg); sub(/".*/, "", pkg)
        el=$0; sub(/.*"Elapsed":/, "", el); sub(/[^0-9.].*/, "", el)
        print el, pkg
      }
    ' "$tmp" | sort -rn | head -10 | while read -r t p; do
      printf '  %6.2fs  %s\n' "$t" "$p"
    done
    # CI: write the per-package timings into the step summary so a >20s
    # regression can be traced without re-running locally. Separate the
    # layers by job name via the caller-provided label.
    if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
      {
        echo "### $label"
        awk -F'"' '
          /"Action":"pass"/ && !/"Test":"/ && /"Package":"[^"]+"/ && /"Elapsed":[0-9.]+/ {
            pkg=$0; sub(/.*"Package":"/, "", pkg); sub(/".*/, "", pkg)
            el=$0; sub(/.*"Elapsed":/, "", el); sub(/[^0-9.].*/, "", el)
            print "| " pkg " | " el "s |"
          }
        ' "$tmp" | sort -t'|' -k3 -rn | head -10
        echo
      } >> "$GITHUB_STEP_SUMMARY"
    fi
    rm -f "$tmp"
    local end=$(( $(date +%s) - start ))
    printf '== %s: %ds ==\n' "$label" "$end"
    if [ "$end" -ge 20 ]; then
      printf '== WARNING: %s took %ds; see the slowest-package list above ==\n' "$label" "$end"
    fi
    exit "$json_ok"
  fi
  "$@"
  local end=$(( $(date +%s) - start ))
  printf '== %s: %ds ==\n' "$label" "$end"
  if [ "$end" -ge 20 ]; then
    printf '== WARNING: %s took %ds; see the slowest-package list above ==\n' "$label" "$end"
  fi
}

case "${1:-fast}" in
  fast)
    # -json so the per-package wall clock is reported; the slowest 10
    # packages pinpoint where the fast layer's budget goes.
    measure "fast: go test -json ./..." go test -json -count=1 ./...
    ;;
  race)
    measure "race: pkg/vfs drive drivers contracttest drivecopy control logging mount syncer cli core mobile crypt task cmd" \
      go test -race -count=1 ./pkg/vfs ./pkg/drive ./pkg/drivers/... \
      ./pkg/contracttest ./pkg/vfs/drivecopy ./pkg/control ./pkg/logging ./pkg/mount ./pkg/syncer ./internal/cli \
      ./pkg/core ./pkg/mobile ./pkg/crypt ./pkg/task ./cmd/qrypt
    ;;
  vfs-stability)
    # The async upload engine (worker shutdown, staging cleanup, journaling)
    # is timing sensitive; a triple run catches the occasional flake.
    measure "vfs-stability: go test -count=3 ./pkg/vfs" go test -count=3 ./pkg/vfs/
    ;;
  smoke)
    measure "smoke: localfs" scripts/smoke-localfs.sh
    ;;
  all)
    measure "all: default suite" go test -count=1 ./...
    measure "all: race vfs+core" go test -race -count=1 ./pkg/vfs ./pkg/core
    measure "all: localfs smoke" scripts/smoke-localfs.sh
    ;;
  integration)
    # Requires a running debug server with real cloud mounts; see the
    # contract-test comment above. Each mount runs the full CRUD/contract
    # suite, so pass only the mounts you actually have configured.
    : "${QRYPT_CONTRACT_SOCKET:?integration layer needs QRYPT_CONTRACT_SOCKET (e.g. /tmp/qrypt.sock)}"
    mounts=("$@")
    if [ "${#mounts[@]}" -le 1 ]; then
      echo "usage: $0 integration <mount> [mount...]" >&2
      exit 2
    fi
    for m in "${mounts[@]:1}"; do
      measure "integration: contract $m" \
        go run ./cmd/qrypt debug test contract --socket "$QRYPT_CONTRACT_SOCKET" --mount "$m"
    done
    ;;
  *)
    echo "usage: $0 [fast|race|vfs-stability|smoke|all|integration <mount>...]" >&2
    exit 2
    ;;
esac
