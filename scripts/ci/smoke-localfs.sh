#!/usr/bin/env bash
set -euo pipefail

# Human-facing output is localized from the system locale. CI assertions below
# intentionally match English text, so pin the language.
export QRYPT_LANG=en
# The legacy section below exercises the in-process filesystem path. Keep the
# resident-Core data plane in its own section further down, because a running
# Core owns the state directory and rejects a second in-process runtime.
export QRYPT_CORE_FS_REQUIRED=0

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKDIR="$(mktemp -d)"
CORE_PID=""
cleanup() {
  if [ -n "$CORE_PID" ] && kill -0 "$CORE_PID" 2>/dev/null; then
    "$BINARY" core stop --config "$CONFIG" >/dev/null 2>&1 || kill "$CORE_PID" 2>/dev/null || true
    wait "$CORE_PID" 2>/dev/null || true
  fi
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

REMOTE="$WORKDIR/remote"
CACHE="$WORKDIR/cache"
CONFIG="$WORKDIR/qrypt.toml"
LOCAL="$WORKDIR/source.txt"
DOWNLOADED="$WORKDIR/downloaded.txt"
LARGE="$WORKDIR/large.bin"
LARGE_DOWNLOADED="$WORKDIR/large.downloaded.bin"
BINARY="$WORKDIR/qrypt"
mkdir -p "$REMOTE" "$CACHE"

cat >"$CONFIG" <<EOF
mount_point = "$WORKDIR/mount"

[storage]
read_cache_dir = "$CACHE/read"
upload_dir = "$WORKDIR/upload"
state_dir = "$WORKDIR/state"

[time]
ntp_enabled = false

[upload]
upload_delay = "1ms"
delete_delay = "1ms"
upload_workers = 2

[[mounts]]
name = "local"
type = "localfs"

[mounts.params]
root_path = "$REMOTE"
EOF

printf 'qrypt smoke %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$LOCAL"

(cd "$ROOT" && go build -o "$BINARY" ./cmd/qrypt)

qrypt() {
  "$BINARY" "$@" --config "$CONFIG"
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

# Consume the full output: grep -q can close the pipe early and make qrypt
# exit with SIGPIPE under pipefail, even when the expected text was found.
qrypt fs list / | grep "local" >/dev/null
qrypt fs mkdir /local/docs
qrypt fs put "$LOCAL" /local/docs/hello.txt
qrypt fs stat /local/docs/hello.txt | grep "name: hello.txt" >/dev/null
qrypt fs cat /local/docs/hello.txt | cmp -s "$LOCAL" -
qrypt fs get /local/docs/hello.txt "$DOWNLOADED"
cmp -s "$LOCAL" "$DOWNLOADED"
qrypt fs mv /local/docs/hello.txt /local/docs/renamed.txt
qrypt fs cat /local/docs/renamed.txt | cmp -s "$LOCAL" -
printf 'qrypt smoke overwrite %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$LOCAL"
qrypt fs put "$LOCAL" /local/docs/renamed.txt
qrypt fs get /local/docs/renamed.txt "$DOWNLOADED" --force
cmp -s "$LOCAL" "$DOWNLOADED"

dd if=/dev/urandom of="$LARGE" bs=1M count=8 2>/dev/null
qrypt fs put "$LARGE" /local/docs/large.bin
qrypt fs get /local/docs/large.bin "$LARGE_DOWNLOADED"
if [ "$(sha256_file "$LARGE")" != "$(sha256_file "$LARGE_DOWNLOADED")" ]; then
  echo "large file checksum mismatch" >&2
  exit 1
fi

for i in 1 2 3 4 5; do
  printf 'concurrent file %s\n' "$i" >"$WORKDIR/concurrent-$i.txt"
  qrypt fs put "$WORKDIR/concurrent-$i.txt" "/local/docs/concurrent-$i.txt"
done
for i in 1 2 3 4 5; do
  qrypt fs get "/local/docs/concurrent-$i.txt" "$WORKDIR/concurrent-$i.out"
  cmp -s "$WORKDIR/concurrent-$i.txt" "$WORKDIR/concurrent-$i.out"
  qrypt fs rm "/local/docs/concurrent-$i.txt"
done

qrypt fs rm /local/docs/large.bin
qrypt fs rm /local/docs/renamed.txt
qrypt fs rm /local/docs

if qrypt fs pending | grep . >/dev/null; then
  echo "pending uploads remain after smoke test" >&2
  qrypt fs pending --verbose >&2
  exit 1
fi

# Resident Core data plane: desktop CLI list/stat/put/mv go through a long-lived
# Core over its control socket instead of opening an in-process VFS.
export QRYPT_CORE_FS_REQUIRED=1
"$BINARY" core serve --foreground --keep --config "$CONFIG" &
CORE_PID=$!
core_ready=
for _ in $(seq 1 100); do
  if "$BINARY" core status --config "$CONFIG" 2>/dev/null | grep -q "state: running"; then
    core_ready=1
    break
  fi
  sleep 0.1
done
if [ -z "$core_ready" ]; then
  echo "resident Core did not become ready" >&2
  exit 1
fi

qrypt fs list / | grep "local" >/dev/null
qrypt fs put "$LOCAL" /local/core.txt --wait-timeout 10s
cmp -s "$LOCAL" "$REMOTE/core.txt"
qrypt fs stat /local/core.txt | grep "name: core.txt" >/dev/null
qrypt fs mv /local/core.txt /local/core-moved.txt
for _ in $(seq 1 100); do
  if qrypt fs list /local 2>/dev/null | grep -q "core-moved.txt"; then
    break
  fi
  sleep 0.1
done
qrypt fs list /local | grep "core-moved.txt" >/dev/null
cmp -s "$LOCAL" "$REMOTE/core-moved.txt"

"$BINARY" core stop --config "$CONFIG" >/dev/null
wait "$CORE_PID" 2>/dev/null || true
CORE_PID=
export QRYPT_CORE_FS_REQUIRED=0

echo "localfs smoke test passed"
