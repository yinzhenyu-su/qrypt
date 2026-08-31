#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
OUT_DIR="${1:-$ROOT_DIR/dist/android}"
SKIP_TESTS="${SKIP_TESTS:-0}"
# ABI=arm64 builds only the device ABI for fast dev iteration (~4x less Go
# cross-compilation); ABI=all (default) keeps the full release matrix.
ABI="${ABI:-all}"
GOPATH_BIN="$(go env GOPATH)/bin"
PATH="$PATH:$GOPATH_BIN"

if ! command -v gomobile >/dev/null 2>&1; then
  echo "gomobile is required. Install it with: go install golang.org/x/mobile/cmd/gomobile@latest" >&2
  echo "Then run: gomobile init" >&2
  exit 1
fi

mkdir -p "$OUT_DIR"
cd "$ROOT_DIR"

if [ "$SKIP_TESTS" != "1" ]; then
  go test ./pkg/mobile ./pkg/core ./pkg/vfs ./pkg/drive
fi

if [ "$ABI" = "all" ]; then
  TARGET="android"
else
  TARGET="android/$ABI"
fi

gomobile bind \
  -target="$TARGET" \
  -androidapi 21 \
  -o "$OUT_DIR/qrypt-mobile.aar" \
  ./pkg/mobile

echo "$OUT_DIR/qrypt-mobile.aar"
