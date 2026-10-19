#!/usr/bin/env bash
# End-to-end smoke test for the qrypt installer.
#
# Builds a fake release, serves it over HTTP, and runs scripts/install.sh against
# it in a throwaway HOME. It asserts the layout, the PATH block, that repeated
# runs converge, that a bad checksum aborts, and that the installed command
# runs. The release "binary" is a small shell stub: the point is to exercise the
# installer's download/verify/extract/install/link path, not to run a real
# FUSE build.
#
# Usage:
#   scripts/smoke-install.sh            # run on this host
#   scripts/smoke-install.sh --docker   # also run inside a clean container
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INSTALLER="$ROOT/scripts/install.sh"
RUN_DOCKER=0
[ "${1:-}" = "--docker" ] && RUN_DOCKER=1

command -v python3 >/dev/null || { echo "python3 is required" >&2; exit 1; }

case "$(uname -s)" in
    Darwin) OS=darwin ;;
    Linux) OS=linux ;;
    *) echo "smoke-install: unsupported host $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
    x86_64 | amd64) ARCH=amd64 ;;
    arm64 | aarch64) ARCH=arm64 ;;
    *) echo "smoke-install: unsupported arch $(uname -m)" >&2; exit 1 ;;
esac

VERSION="0.5.3"
TAG="v$VERSION"
ASSET="qrypt_${VERSION}_${OS}_${ARCH}.tar.gz"
CHECKSUM="qrypt_${VERSION}_checksums.txt"
REPO="smoke/qrypt"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/qrypt-smoke.XXXXXX")"
SERVER_PID=""
cleanup() {
    if [ -n "$SERVER_PID" ]; then
        kill "$SERVER_PID" 2>/dev/null || true
    fi
    rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

SERVE="$WORK/serve"
mkdir -p "$SERVE/api/repos/$REPO/releases"

sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    else
        shasum -a 256 "$1" | awk '{print $1}'
    fi
}

# make_release <version> <os> <arch> publishes a stub release for that platform.
# The stub answers `version`, so the installed command is runnable; the point is
# to exercise download/verify/extract/install/link, not a real FUSE build.
make_release() {
    local version="$1" os="$2" arch="$3"
    local tag="v$version"
    local inner="qrypt-$os-$arch"
    local asset="qrypt_${version}_${os}_${arch}.tar.gz"
    local checksum="qrypt_${version}_checksums.txt"
    local stub="$WORK/$inner"
    cat >"$stub" <<STUB_EOF
#!/bin/sh
case "\${1:-}" in
    version) echo "qrypt v$version" ;;
    *) echo "qrypt" ;;
esac
STUB_EOF
    chmod 755 "$stub"
    mkdir -p "$SERVE/dl/$tag"
    : >"$SERVE/dl/$tag/$checksum"
    tar -C "$WORK" -czf "$SERVE/dl/$tag/$asset" "$inner"
    printf '%s  %s\n' "$(sha256_of "$SERVE/dl/$tag/$asset")" "$asset" >>"$SERVE/dl/$tag/$checksum"
}

# publish_latest <tag> points both the release "latest" location and the API at
# <tag>, so the installer resolves the same version either way.
publish_latest() {
    printf '%s\n' "$1" >"$SERVE/latest-tag.txt"
    printf '{\n  "tag_name": "%s",\n  "draft": false,\n  "prerelease": false\n}\n' "$1" \
        >"$SERVE/api/repos/$REPO/releases/latest"
}

make_release "$VERSION" "$OS" "$ARCH"
# The container runs a linux build, so publish one for that platform too.
[ "$OS" = linux ] || make_release "$VERSION" linux "$ARCH"
cp "$SERVE/dl/$TAG/$CHECKSUM" "$WORK/good-checksums"
publish_latest "$TAG"

# The release server. It serves the published files, answers the release "latest"
# location with a 302 to the current tag (which is what the installer follows
# instead of the API), and 403s the API when $SERVE/api-403 exists so the
# rate-limit case can be exercised.
cat >"$WORK/server.py" <<'PY'
import http.server
import os
import socketserver
import sys

root, host, port = sys.argv[1], sys.argv[2], int(sys.argv[3])


class Handler(http.server.SimpleHTTPRequestHandler):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, directory=root, **kwargs)

    def send_text(self, status, body):
        payload = body.encode()
        self.send_response(status)
        self.send_header('Content-Type', 'text/plain')
        self.send_header('Content-Length', str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def do_GET(self):
        path = self.path.split('?', 1)[0]
        if path == '/releases/latest':
            tag_file = os.path.join(root, 'latest-tag.txt')
            if os.path.exists(tag_file):
                with open(tag_file) as handle:
                    tag = handle.read().strip()
                self.send_response(302)
                self.send_header('Location', '/releases/tag/' + tag)
                self.end_headers()
                return
            self.send_text(404, 'no latest tag')
            return
        if path.startswith('/releases/tag/'):
            self.send_text(200, path.rsplit('/', 1)[-1])
            return
        if path.startswith('/api/') and os.path.exists(os.path.join(root, 'api-403')):
            self.send_text(403, 'rate limited')
            return
        super().do_GET()

    def log_message(self, *args):
        pass


socketserver.TCPServer.allow_reuse_address = True
with socketserver.TCPServer((host, port), Handler) as httpd:
    httpd.serve_forever()
PY

PORT="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
python3 "$WORK/server.py" "$SERVE" 0.0.0.0 "$PORT" >"$WORK/server.log" 2>&1 &
SERVER_PID=$!
# Wait for the server to accept connections.
for _ in $(seq 1 50); do
    curl -fsS "http://127.0.0.1:$PORT/" >/dev/null 2>&1 && break
    sleep 0.1
done

# invoke <home> <base-url> <path> <shell> [extra install args...]
# Runs the installer silently; the caller reports and prints the log on an
# unexpected result.
invoke() {
    local home="$1" base="$2" path="$3" shell="$4"
    shift 4
    SHELL="$shell" \
        QRYPT_INSTALL_REPO="$REPO" \
        QRYPT_INSTALL_API="$base/api" \
        QRYPT_INSTALL_LATEST_URL="$base/releases/latest" \
        QRYPT_INSTALL_DOWNLOAD="$base/dl" \
        QRYPT_INSTALL_HOME="$home" \
        PATH="$path" \
        sh "$INSTALLER" "$@" >"$WORK/install.log" 2>&1
}

# run_install uses a PATH without the link dir and a zsh login shell.
run_install() {
    local home="$1" base="$2"
    shift 2
    invoke "$home" "$base" "/usr/bin:/bin" /bin/zsh "$@"
}

assert_layout() {
    home="$1"
    want="${2:-v$VERSION}"
    [ -x "$home/.qrypt/bin/qrypt" ] || { echo "FAIL: real binary missing" >&2; return 1; }
    [ -L "$home/.local/bin/qrypt" ] || { echo "FAIL: symlink missing" >&2; return 1; }
    [ "$(readlink "$home/.local/bin/qrypt")" = "$home/.qrypt/bin/qrypt" ] ||
        { echo "FAIL: symlink target wrong" >&2; return 1; }
    out="$("$home/.local/bin/qrypt" version)"
    [ "$out" = "qrypt $want" ] || { echo "FAIL: installed command output: $out" >&2; return 1; }
    grep -qF '# >>> qrypt >>>' "$home/.zprofile" || { echo "FAIL: PATH block missing" >&2; return 1; }
}

BASE="http://127.0.0.1:$PORT"

# 1. Fresh install.
HOME1="$WORK/home1"
mkdir -p "$HOME1"
: >"$HOME1/.zprofile"
if ! run_install "$HOME1" "$BASE"; then
    echo "FAIL: fresh install failed; log:" >&2
    cat "$WORK/install.log" >&2
    exit 1
fi
assert_layout "$HOME1"

# 2. Re-running converges: still exactly one block, binary intact.
if ! run_install "$HOME1" "$BASE"; then
    echo "FAIL: re-run failed; log:" >&2
    cat "$WORK/install.log" >&2
    exit 1
fi
assert_layout "$HOME1"
blocks="$(grep -cF '# >>> qrypt >>>' "$HOME1/.zprofile")"
[ "$blocks" = "1" ] || { echo "FAIL: $blocks PATH blocks after re-run" >&2; exit 1; }

# 3. A bad checksum aborts without touching the existing install.
printf '%s  %s\n' "$(printf '0%.0s' $(seq 1 64))" "$ASSET" >"$SERVE/dl/$TAG/$CHECKSUM"
HOME2="$WORK/home2"
mkdir -p "$HOME2"
if run_install "$HOME2" "$BASE"; then
    echo "FAIL: install succeeded despite a bad checksum" >&2
    exit 1
fi
grep -q "checksum mismatch" "$WORK/install.log" ||
    { echo "FAIL: wrong error for a bad checksum:" >&2; cat "$WORK/install.log" >&2; exit 1; }
[ ! -e "$HOME2/.qrypt/bin/qrypt" ] || { echo "FAIL: a bad checksum left a binary" >&2; exit 1; }
# Restore the good checksums for the container run.
cp "$WORK/good-checksums" "$SERVE/dl/$TAG/$CHECKSUM"

# 4. A pre-existing, unmanaged file is not clobbered without --force.
HOME3="$WORK/home3"
mkdir -p "$HOME3/.local/bin"
printf '#!/bin/sh\necho other-qrypt\n' >"$HOME3/.local/bin/qrypt"
chmod 755 "$HOME3/.local/bin/qrypt"
if run_install "$HOME3" "$BASE"; then
    echo "FAIL: installer overwrote an unmanaged file without --force" >&2
    exit 1
fi
grep -q "already exists" "$WORK/install.log" ||
    { echo "FAIL: wrong error for a pre-existing file:" >&2; cat "$WORK/install.log" >&2; exit 1; }
if [ ! -f "$HOME3/.local/bin/qrypt" ] || [ -L "$HOME3/.local/bin/qrypt" ]; then
    echo "FAIL: pre-existing file was changed" >&2
    exit 1
fi
if ! run_install "$HOME3" "$BASE" --force; then
    echo "FAIL: --force did not replace the unmanaged file; log:" >&2
    cat "$WORK/install.log" >&2
    exit 1
fi
[ -L "$HOME3/.local/bin/qrypt" ] || { echo "FAIL: --force did not create the symlink" >&2; exit 1; }

# 5. Already on PATH: no rc file is edited.
HOME4="$WORK/home4"
mkdir -p "$HOME4/.local/bin"
: >"$HOME4/.zprofile"
if ! invoke "$HOME4" "$BASE" "$HOME4/.local/bin:/usr/bin:/bin" /bin/zsh; then
    echo "FAIL: install failed when the link dir was already on PATH; log:" >&2
    cat "$WORK/install.log" >&2
    exit 1
fi
grep -q "already on PATH" "$WORK/install.log" ||
    { echo "FAIL: installer did not notice the link dir was on PATH" >&2; exit 1; }
if grep -qF '# >>> qrypt >>>' "$HOME4/.zprofile"; then
    echo "FAIL: installer edited the rc file although the link dir was already on PATH" >&2
    exit 1
fi

# 6. Unknown shell: print instructions instead of editing.
HOME5="$WORK/home5"
mkdir -p "$HOME5"
: >"$HOME5/.zprofile"
if ! invoke "$HOME5" "$BASE" "/usr/bin:/bin" /bin/tcsh; then
    echo "FAIL: install failed for an unknown shell; log:" >&2
    cat "$WORK/install.log" >&2
    exit 1
fi
grep -q "Add .* to your PATH" "$WORK/install.log" ||
    { echo "FAIL: unknown shell did not get PATH instructions:" >&2; cat "$WORK/install.log" >&2; exit 1; }
if grep -qF '# >>> qrypt >>>' "$HOME5/.zprofile"; then
    echo "FAIL: installer edited an rc file for an unknown shell" >&2
    exit 1
fi

# 7. A different qrypt earlier on PATH is reported.
SHADOW="$WORK/shadow"
mkdir -p "$SHADOW"
printf '#!/bin/sh\necho old-qrypt\n' >"$SHADOW/qrypt"
chmod 755 "$SHADOW/qrypt"
HOME6="$WORK/home6"
mkdir -p "$HOME6"
if ! invoke "$HOME6" "$BASE" "$SHADOW:/usr/bin:/bin" /bin/zsh; then
    echo "FAIL: install failed with a shadowing copy present; log:" >&2
    cat "$WORK/install.log" >&2
    exit 1
fi
grep -q "another qrypt is on PATH" "$WORK/install.log" ||
    { echo "FAIL: a shadowing copy was not reported:" >&2; cat "$WORK/install.log" >&2; exit 1; }

# 8. Re-running after a newer release is published installs the newer release.
NEW_VERSION="0.5.4"
make_release "$NEW_VERSION" "$OS" "$ARCH"
[ "$OS" = linux ] || make_release "$NEW_VERSION" linux "$ARCH"
publish_latest "v$NEW_VERSION"
if ! run_install "$HOME1" "$BASE"; then
    echo "FAIL: re-run after a newer release failed; log:" >&2
    cat "$WORK/install.log" >&2
    exit 1
fi
assert_layout "$HOME1" "v$NEW_VERSION"
blocks="$(grep -cF '# >>> qrypt >>>' "$HOME1/.zprofile")"
[ "$blocks" = "1" ] || { echo "FAIL: $blocks PATH blocks after the upgrade re-run" >&2; exit 1; }

# 9. The API rate limit does not stop the newest release from being installed:
#    the tag comes from the release redirect, so a 403 from the API is never hit.
touch "$SERVE/api-403"
HOME7="$WORK/home7"
mkdir -p "$HOME7"
: >"$HOME7/.zprofile"
if ! run_install "$HOME7" "$BASE"; then
    echo "FAIL: install failed while the API was rate limited; log:" >&2
    cat "$WORK/install.log" >&2
    exit 1
fi
assert_layout "$HOME7" "v$NEW_VERSION"
rm -f "$SERVE/api-403"

# 10. Optional: the same flow inside a clean container.
if [ "$RUN_DOCKER" = 1 ]; then
    if ! command -v docker >/dev/null 2>&1; then
        echo "smoke-install: docker not found; skipping container run" >&2
    else
        if [ "$OS" = linux ]; then
            NET=(--network host)
            SRV_HOST="127.0.0.1"
        else
            NET=(--add-host host.docker.internal:host-gateway)
            SRV_HOST="host.docker.internal"
        fi
        echo "== container install (alpine) =="
        docker run --rm "${NET[@]}" \
            -v "$ROOT:/src:ro" \
            -e QRYPT_INSTALL_REPO="$REPO" \
            -e QRYPT_INSTALL_API="http://$SRV_HOST:$PORT/api" \
            -e QRYPT_INSTALL_LATEST_URL="http://$SRV_HOST:$PORT/releases/latest" \
            -e QRYPT_INSTALL_DOWNLOAD="http://$SRV_HOST:$PORT/dl" \
            alpine:3.20 sh -c '
                set -eu
                apk add --no-cache bash curl >/dev/null
                SHELL=/bin/bash sh /src/scripts/install.sh
                test -x "$HOME/.qrypt/bin/qrypt"
                test -L "$HOME/.local/bin/qrypt"
                "$HOME/.local/bin/qrypt" version
            '
    fi
fi

echo "install smoke test passed"
