#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CONFIG="$ROOT/qrypt.toml"
STARTUP_TIMEOUT=30

usage() {
  cat <<'EOF'
Usage:
  scripts/mount-debug-test.sh [OPTIONS] MOUNT TEST [TEST_OPTIONS...]

Mount qrypt at a temporary directory, run one debug test, then unmount and
remove all temporary local files. TEST_OPTIONS are passed directly to
`qrypt debug test TEST`.

Options:
  --config PATH             qrypt config file (default: repository qrypt.toml)
  -h, --help                show this help

Examples:
  scripts/mount-debug-test.sh quark-test batchmove --count 50 --size 4k
  scripts/mount-debug-test.sh --config ./qrypt.toml quark-test contract
EOF
}

require_value() {
  if [ "$#" -lt 2 ]; then
    echo "$1 requires a value" >&2
    usage >&2
    exit 2
  fi
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --config)
      require_value "$@"
      CONFIG="$2"
      shift 2
      ;;
    --config=*)
      CONFIG="${1#*=}"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    --)
      shift
      break
      ;;
    -*)
      echo "unknown script option: $1" >&2
      usage >&2
      exit 2
      ;;
    *)
      break
      ;;
  esac
done

if [ "$#" -eq 2 ] && { [ "$2" = "-h" ] || [ "$2" = "--help" ]; }; then
  (cd "$ROOT" && go run ./cmd/qrypt debug test --help)
  exit $?
fi

if [ "$#" -lt 2 ]; then
  usage >&2
  exit 2
fi

MOUNT_NAME="$1"
TEST_NAME="$2"
shift 2

if [ ! -f "$CONFIG" ]; then
  echo "config file not found: $CONFIG" >&2
  exit 2
fi

if [ "$TEST_NAME" = "xfer" ]; then
  echo "xfer needs two mounts and is not supported by this single-mount script" >&2
  exit 2
fi

TEMP_BASE="${TMPDIR:-/tmp}"
TEMP_BASE="${TEMP_BASE%/}"
WORKDIR="$(mktemp -d "$TEMP_BASE/qrypt-mount-test.XXXXXX")"
WORKDIR="$(cd "$WORKDIR" && pwd -P)"
MOUNT_POINT="$WORKDIR/mount"
SOCKET="$WORKDIR/qrypt.sock"
MOUNT_LOG="$WORKDIR/mount.log"
MOUNT_PID=""
mkdir -p "$MOUNT_POINT"
export QRYPT_HOME="$WORKDIR/runtime"

is_mounted() {
  if command -v mountpoint >/dev/null 2>&1; then
    mountpoint -q "$MOUNT_POINT"
  else
    mount | grep -F " on $MOUNT_POINT " >/dev/null 2>&1
  fi
}

stop_mount_process() {
  if [ -z "$MOUNT_PID" ] || ! kill -0 "$MOUNT_PID" 2>/dev/null; then
    [ -z "$MOUNT_PID" ] || wait "$MOUNT_PID" 2>/dev/null || true
    return
  fi

  kill -INT "$MOUNT_PID" 2>/dev/null || true
  wait "$MOUNT_PID" 2>/dev/null || true
}

cleanup() {
  local status=$?
  trap - EXIT INT TERM

  stop_mount_process
  if is_mounted; then
    echo "mount point is still active; preserving temporary directory: $WORKDIR" >&2
    status=1
  else
    rm -rf "$WORKDIR"
  fi
  exit "$status"
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

BINARY="$WORKDIR/qrypt"
(cd "$ROOT" && go build -o "$BINARY" ./cmd/qrypt)

"$BINARY" mount "$MOUNT_NAME" \
  --config "$CONFIG" \
  --mount-point "$MOUNT_POINT" \
  --socket "$SOCKET" >"$MOUNT_LOG" 2>&1 &
MOUNT_PID=$!

deadline=$((SECONDS + STARTUP_TIMEOUT))
while :; do
  if ! kill -0 "$MOUNT_PID" 2>/dev/null; then
    echo "qrypt exited before the mount became ready" >&2
    tail -n 100 "$MOUNT_LOG" >&2 || true
    exit 1
  fi
  if is_mounted && [ -S "$SOCKET" ] && \
      "$BINARY" debug raw /v1/health --config "$CONFIG" --socket "$SOCKET" >/dev/null 2>&1; then
    break
  fi
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "mount did not become ready within ${STARTUP_TIMEOUT}s" >&2
    tail -n 100 "$MOUNT_LOG" >&2 || true
    exit 1
  fi
  sleep 0.2
done

TEST_MOUNT_ARGS=()
if [ "$TEST_NAME" = "read" ]; then
  TEST_MOUNT_ARGS=(--mount-point "$MOUNT_POINT")
fi

"$BINARY" debug test "$TEST_NAME" \
  --config "$CONFIG" \
  --socket "$SOCKET" \
  --mount "$MOUNT_NAME" \
  "${TEST_MOUNT_ARGS[@]}" \
  "$@"
