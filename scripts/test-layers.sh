#!/usr/bin/env bash
# Layered test entry points. Each layer is a separate command so CI and
# developers can run exactly the coverage they need:
#
#   ./scripts/test-layers.sh fast         # default unit suite
#   ./scripts/test-layers.sh race         # concurrency-heavy packages under -race
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
  "$@"
  local end=$(( $(date +%s) - start ))
  printf '== %s: %ds ==\n' "$label" "$end"
  if [ "$end" -ge 20 ]; then
    printf '== WARNING: %s took %ds; if this is fast, consider splitting the slow package ==\n' "$label" "$end"
  fi
}

case "${1:-fast}" in
  fast)
    measure "fast: go test ./..." go test -count=1 ./...
    ;;
  race)
    measure "race: pkg/vfs drive drivers control logging cli core mobile crypt task cmd" \
      go test -race -count=1 ./pkg/vfs ./pkg/drive ./pkg/drivers/... \
      ./internal/control ./internal/logging ./internal/cli \
      ./pkg/core ./pkg/mobile ./pkg/crypt ./pkg/task ./cmd/qrypt
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
    echo "usage: $0 [fast|race|all|integration <mount>...]" >&2
    exit 2
    ;;
esac
