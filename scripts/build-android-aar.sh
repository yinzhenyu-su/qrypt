#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
OUT_DIR="${1:-$ROOT_DIR/dist/android}"

if ! command -v gomobile >/dev/null 2>&1; then
  echo "gomobile is required. Install it with: go install golang.org/x/mobile/cmd/gomobile@latest" >&2
  echo "Then run: gomobile init" >&2
  exit 1
fi

mkdir -p "$OUT_DIR"
cd "$ROOT_DIR"

gomobile bind \
  -target=android \
  -androidapi 21 \
  -o "$OUT_DIR/qrypt-mobile.aar" \
  ./pkg/mobile
