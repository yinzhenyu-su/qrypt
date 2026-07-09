#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

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
cache_dir = "$CACHE"

[time]
ntp_enabled = false

[defaults.cache]
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

qrypt fs list / | grep -q "local"
qrypt fs mkdir /local/docs
qrypt fs put "$LOCAL" /local/docs/hello.txt
qrypt fs stat /local/docs/hello.txt | grep -q "name: hello.txt"
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

if qrypt fs pending | grep -q .; then
  echo "pending uploads remain after smoke test" >&2
  qrypt fs pending --verbose >&2
  exit 1
fi

echo "localfs smoke test passed"
