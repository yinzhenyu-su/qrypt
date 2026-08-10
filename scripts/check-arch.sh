#!/usr/bin/env bash
# Architecture boundary gate. Arrows show compile-time dependencies:
#
#   pkg/mobile -> pkg/core -> pkg/vfs -> pkg/drive
#                      |                    ^
#                      v                    |
#              internal/control      pkg/drivers
#
#   cmd/qrypt -> internal/cli -> pkg/core / pkg/vfs
#   internal/mount -> pkg/vfs
#
#   pkg/mobile and internal/cli import pkg/drivers/all only to register the
#   bundled concrete providers; pkg/core remains provider-independent.
#
# Rules enforced here (production code, except where noted):
#   1. pkg/drivers never imports vfs / mount / control / cli (tests included)
#   2. pkg/vfs, internal/mount, internal/control, pkg/core never import a
#      concrete provider (their tests may use FakeDriver/localfs)
#   3. pkg/mobile only imports the registration aggregate pkg/drivers/all
#   4. internal/control is the debug-socket HTTP API only: the sync
#      executor must not depend on it (it is the pure production path;
#      pkg/core and internal/cli are the debug server's host/client and
#      may). Shared driver-level operations live in internal/drivecopy and
#      contract-test harnesses in internal/contracttest.
#
# Runs in CI on every PR; costs milliseconds.
set -euo pipefail
cd "$(dirname "$0")/.."

fail=0

note() {
  echo "!! $1"
  fail=1
}

# 1. Providers never touch upper layers.
while IFS= read -r f; do
  [ -n "$f" ] && note "driver layer imports an upper layer: $f"
done < <(rg -l 'github.com/yinzhenyu/qrypt/(pkg/vfs|internal/mount|internal/control|internal/cli)' pkg/drivers/ 2>/dev/null || true)

# 2. Interface layers never import concrete providers.
for dir in pkg/vfs internal/mount internal/control pkg/core; do
  while IFS= read -r f; do
    [ -n "$f" ] && note "$dir imports a concrete driver: $f"
  done < <(rg -l 'github.com/yinzhenyu/qrypt/pkg/drivers/' "$dir" -g '!**/*_test.go' 2>/dev/null || true)
done

# 3. pkg/mobile only wires in the driver registration aggregate.
while IFS= read -r line; do
  [ -n "$line" ] && note "pkg/mobile imports a non-aggregate driver package: $line"
done < <(rg -n 'github.com/yinzhenyu/qrypt/pkg/drivers/' pkg/mobile -g '!**/*_test.go' 2>/dev/null | grep -v 'pkg/drivers/all' || true)

# 4. internal/control stays a debug-API leaf: production executors may use
#    internal/drivecopy / internal/contracttest but never the HTTP server.
for dir in internal/sync; do
  while IFS= read -r f; do
    [ -n "$f" ] && note "$dir depends on the debug server package internal/control: $f"
  done < <(rg -l 'github.com/yinzhenyu/qrypt/internal/control' "$dir" -g '!**/*_test.go' 2>/dev/null || true)
done

if [ "$fail" -ne 0 ]; then
  echo "== FAIL: architecture boundary violated =="
  exit 1
fi
echo "== architecture boundaries clean =="
