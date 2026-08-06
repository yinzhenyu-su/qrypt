#!/usr/bin/env bash
# Layered test entry points. Each layer is a separate command so CI and
# developers can run exactly the coverage they need:
#
#   ./scripts/test-layers.sh fast        # default suite (~15s)
#   ./scripts/test-layers.sh race        # concurrency-heavy packages under -race
#   ./scripts/test-layers.sh all         # everything except real-netdisk tests
#
# Real-netdisk contract tests are NOT part of go test: they need a running
# `qrypt mount` debug server with mounted cloud accounts. Run them manually:
#   qrypt debug test contract --socket /tmp/qrypt.sock --mount <name>
set -euo pipefail
cd "$(dirname "$0")/.."

case "${1:-fast}" in
  fast)
    go test -count=1 ./...
    ;;
  race)
    go test -race -count=1 ./pkg/vfs ./pkg/drive ./pkg/drivers/... \
      ./internal/control ./internal/logging ./internal/cli \
      ./pkg/core ./pkg/mobile ./pkg/crypt ./pkg/task ./cmd/qrypt
    ;;
  all)
    go test -count=1 ./...
    go test -race -count=1 ./pkg/vfs ./pkg/core
    scripts/smoke-localfs.sh
    ;;
  *)
    echo "usage: $0 [fast|race|all]" >&2
    exit 2
    ;;
esac
