#!/usr/bin/env bash
# Architecture boundary gate. Arrows show compile-time dependencies:
#
#   pkg/mobile -> pkg/core -> pkg/vfs -> pkg/drive
#                      |                    ^
#                      v                    |
#                pkg/control         pkg/drivers
#
#   cmd/qrypt -> internal/cli -> pkg/core / pkg/vfs
#   pkg/mount -> pkg/vfs
#
#   pkg/mobile and internal/cli import pkg/drivers/all only to register the
#   bundled concrete providers; pkg/core remains provider-independent.
#
# Rules enforced here (production code, except where noted):
#   1. pkg never imports this module's internal packages (tests included)
#   2. pkg/drivers never imports vfs / mount / control / cli (tests included)
#   3. pkg/vfs, pkg/mount, pkg/control, pkg/core never import a
#      concrete provider (their tests may use FakeDriver/localfs)
#   4. pkg/mobile only imports the registration aggregate pkg/drivers/all
#   5. pkg/core is pkg/mobile's application boundary: mobile must not
#      import pkg/vfs (or any subpackage), and consumes VFS value types
#      only through the client contracts pkg/core exposes
#   6. pkg/control is the debug-socket HTTP API only: the sync
#      executor must not depend on it (it is the pure production path;
#      pkg/core and internal/cli are the debug server's host/client and
#      may). Shared driver-level operations live in pkg/vfs/drivecopy and
#      contract-test harnesses in pkg/contracttest.
#   7. pkg/mobile reaches media value types (MP4 probe, virtual-file info
#      and read mappings, virtual-file handle) only through the client
#      contracts pkg/core exports, never through the media implementation
#      layer. The same directional shape applies as the VFS rule.
#   8. pkg/mobile consumes task DTOs (requests, filters, task/item results,
#      event types) and event subscriptions only through the pkg/core task
#      contracts (core.Task*, TaskEvent, TaskSubscription), never through
#      the pkg/task implementation layer. pkg/task is owned below pkg/core;
#      the same directional shape applies as the VFS and media rules.
#
# Runs in CI on every PR; costs milliseconds.
set -euo pipefail
cd "$(dirname "$0")/.."

fail=0

note() {
  echo "!! $1"
  fail=1
}

# 1. Public packages never reach back into this module's private app/runtime
#    layer. Put reusable code in pkg first; narrow it later when ownership is
#    clear.
while IFS= read -r line; do
  [ -n "$line" ] && note "pkg imports a private internal package: $line"
done < <(rg -n 'github.com/yinzhenyu/qrypt/internal/' pkg 2>/dev/null || true)

# 2. Providers never touch upper layers.
while IFS= read -r f; do
  [ -n "$f" ] && note "driver layer imports an upper layer: $f"
done < <(rg -l 'github.com/yinzhenyu/qrypt/(pkg/vfs|pkg/mount|pkg/control|internal/cli)' pkg/drivers/ 2>/dev/null || true)

# 3. Interface layers never import concrete providers.
for dir in pkg/vfs pkg/mount pkg/control pkg/core; do
  while IFS= read -r f; do
    [ -n "$f" ] && note "$dir imports a concrete driver: $f"
  done < <(rg -l 'github.com/yinzhenyu/qrypt/pkg/drivers/' "$dir" -g '!**/*_test.go' 2>/dev/null || true)
done

# 4. pkg/mobile only wires in the driver registration aggregate.
while IFS= read -r line; do
  [ -n "$line" ] && note "pkg/mobile imports a non-aggregate driver package: $line"
done < <(rg -n 'github.com/yinzhenyu/qrypt/pkg/drivers/' pkg/mobile -g '!**/*_test.go' 2>/dev/null | grep -v 'pkg/drivers/all' || true)

# 5. pkg/core is pkg/mobile's application boundary. pkg/core serves other
#    hosts too (internal/cli, pkg/mount); the rule is directional: mobile
#    reaches filesystem value types through core, never through the VFS
#    implementation layer (subpackage imports like pkg/vfs/read match the
#    prefix too, so a single pattern covers every domain below it).
while IFS= read -r line; do
  [ -n "$line" ] && note "pkg/mobile imports the VFS implementation layer: $line"
done < <(rg -n 'github.com/yinzhenyu/qrypt/pkg/vfs' pkg/mobile -g '!**/*_test.go' 2>/dev/null || true)

# 6. pkg/control stays a debug-API leaf: production executors may use
#    pkg/vfs/drivecopy / pkg/contracttest but never the HTTP server.
for dir in pkg/syncer; do
  while IFS= read -r f; do
    [ -n "$f" ] && note "$dir depends on the debug server package pkg/control: $f"
  done < <(rg -l 'github.com/yinzhenyu/qrypt/pkg/control' "$dir" -g '!**/*_test.go' 2>/dev/null || true)
done

# 7. pkg/mobile consumes media value types through the pkg/core client
#    contracts (core-owned probe/info/mapping DTOs and VirtualFileHandle),
#    never from the media implementation layer directly.
while IFS= read -r line; do
  [ -n "$line" ] && note "pkg/mobile imports the media implementation layer: $line"
done < <(rg -n 'github.com/yinzhenyu/qrypt/pkg/media' pkg/mobile -g '!**/*_test.go' 2>/dev/null || true)

# 8. pkg/mobile consumes task value types, events and subscriptions through
#    the pkg/core task contracts (aliased Task*/TaskEvent DTOs and the
#    TaskSubscription interface), never from the pkg/task implementation
#    layer directly.
while IFS= read -r line; do
  [ -n "$line" ] && note "pkg/mobile imports the task implementation layer: $line"
done < <(rg -n 'github.com/yinzhenyu/qrypt/pkg/task' pkg/mobile -g '!**/*_test.go' 2>/dev/null || true)

if [ "$fail" -ne 0 ]; then
  echo "== FAIL: architecture boundary violated =="
  exit 1
fi
echo "== architecture boundaries clean =="
