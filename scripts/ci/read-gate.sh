#!/usr/bin/env bash
# Read-path quality gate: adaptive foreground split, duplicate-lookahead dedupe
# and range retry determinism, all driven through the deterministic simulated
# provider (pkg/testsupport/simread). Hermetic mode needs no network, no
# credentials and no clock-sensitive thresholds.
#
# Real-cloud mode runs the same sustained-read probe the maintainers use for
# read tuning. It is only invoked by .github/workflows/read-gates.yaml when the
# optional secret/variable are configured; it never runs on pull requests.
set -euo pipefail
cd "$(dirname "$0")/../.."

REAL_CLOUD=0
MOUNT=""
CONFIG=""
PROBE_OPTIONS=(--size 64m --block-size 1m --samples 2 --cache-mode cold)

usage() {
  cat <<'USAGE'
usage: scripts/ci/read-gate.sh
       scripts/ci/read-gate.sh --real-cloud MOUNT --config PATH [-- PROBE_OPTIONS...]

Hermetic mode (default) runs the deterministic read-path tests:
  ./pkg/testsupport/...        simulated provider primitives
  ./pkg/drive/ (Sim*)          range retry and truncation replay determinism
  ./pkg/vfs/read/ (Sim|Split|Lookahead)
                               adaptive foreground split and lookahead dedupe

Real-cloud mode mounts the configured provider through
scripts/dev/debug-mount.sh and runs a sustained sequential read probe. It
requires both --config and a mount name, and is skipped by CI when the optional
secret/variable are absent.
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --real-cloud)
      REAL_CLOUD=1
      MOUNT="${2:-}"
      if [[ -z "$MOUNT" ]]; then
        echo "read-gate: --real-cloud requires a mount name" >&2
        exit 2
      fi
      shift 2
      ;;
    --config)
      CONFIG="${2:-}"
      if [[ -z "$CONFIG" ]]; then
        echo "read-gate: --config requires a path" >&2
        exit 2
      fi
      shift 2
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    --)
      shift
      PROBE_OPTIONS=("$@")
      break
      ;;
    *)
      echo "read-gate: unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

step() { printf '\n==> %s\n' "$*"; }

if [[ "$REAL_CLOUD" == 1 ]]; then
  if [[ -z "$CONFIG" || ! -f "$CONFIG" ]]; then
    echo "read-gate: real-cloud mode needs --config with an existing qrypt.toml" >&2
    exit 2
  fi
  step "real-cloud sustained read on mount $MOUNT"
  scripts/dev/debug-mount.sh --config "$CONFIG" "$MOUNT" read "${PROBE_OPTIONS[@]}"
  echo "== real-cloud read gate passed =="
  exit 0
fi

export GOCACHE="${GOCACHE:-/tmp/qrypt-go-build}"

step "simulated read provider"
go test -count=1 -timeout 120s ./pkg/testsupport/...

step "range retry and truncation replay determinism"
go test -count=1 -timeout 120s -run 'Sim' ./pkg/drive/

step "adaptive foreground split and lookahead dedupe"
go test -count=1 -timeout 120s -run 'Sim|Split|Lookahead' ./pkg/vfs/read/

echo "== read gate passed =="
