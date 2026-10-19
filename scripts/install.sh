#!/bin/sh
# qrypt installer.
#
# Downloads the newest (or a requested) qrypt release for this platform, verifies
# it against the published sha256 checksum, installs the real binary at
# ~/.qrypt/bin/qrypt, and links ~/.local/bin/qrypt to it. Only ~/.local/bin is
# added to PATH.
#
# Usage:
#   curl -fsSL <url> | sh
#   sh install.sh [--version vX.Y.Z] [--force]
#
# Environment overrides (mainly for testing):
#   QRYPT_INSTALL_REPO      owner/repo             (default yinzhenyu-su/qrypt)
#   QRYPT_INSTALL_API       GitHub API base        (default https://api.github.com)
#   QRYPT_INSTALL_DOWNLOAD  release download base  (default https://github.com/<repo>/releases/download)
#   QRYPT_INSTALL_HOME      home directory         (default $HOME)
#   GITHUB_TOKEN / GH_TOKEN optional token, raising the API rate limit
set -eu

REPO="${QRYPT_INSTALL_REPO:-yinzhenyu-su/qrypt}"
API_BASE="${QRYPT_INSTALL_API:-https://api.github.com}"
DOWNLOAD_BASE="${QRYPT_INSTALL_DOWNLOAD:-https://github.com/$REPO/releases/download}"
HOME_DIR="${QRYPT_INSTALL_HOME:-$HOME}"
TOKEN="${GH_TOKEN:-${GITHUB_TOKEN:-}}"

# These must match pkg/install (enforced by the naming drift test).
REL_REAL=".qrypt/bin"
REL_LINK=".local/bin"
MARKER_BEGIN="# >>> qrypt >>>"
MARKER_END="# <<< qrypt <<<"

DATA_DIR="$HOME_DIR/$REL_REAL"
LINK_DIR="$HOME_DIR/$REL_LINK"
REAL="$DATA_DIR/qrypt"
LINK="$LINK_DIR/qrypt"

VERSION=""
FORCE=0
TMP=""

# ── Output helpers ──────────────────────────────────────────────────

bold=""
reset=""
if [ -t 1 ]; then
    bold="$(printf '\033[1m')"
    reset="$(printf '\033[0m')"
fi

info() { printf '%s\n' "$*"; }
info_bold() { printf '%s%s%s\n' "$bold" "$*" "$reset"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
error() { printf 'error: %s\n' "$*" >&2; exit 1; }

usage() {
    cat <<'EOF'
Install qrypt.

Usage:
  sh install.sh [options]

Options:
  --version <v>   install a specific release (for example v0.5.0)
  --force         replace an existing, unmanaged ~/.local/bin/qrypt
  -h, --help      show this help
EOF
}

# ── Release naming (must match pkg/selfupdate) ──────────────────────

archive_ext() {
    if [ "$1" = "windows" ]; then
        printf 'zip'
    else
        printf 'tar.gz'
    fi
}

version_num() { printf '%s' "${1#v}"; }

asset_name() { # <version-no-v> <os> <arch>
    printf 'qrypt_%s_%s_%s.%s\n' "$1" "$2" "$3" "$(archive_ext "$2")"
}

checksum_name() { # <version-no-v>
    printf 'qrypt_%s_checksums.txt\n' "$1"
}

inner_name() { # <os> <arch>
    if [ "$1" = "windows" ]; then
        printf 'qrypt-%s-%s.exe\n' "$1" "$2"
    else
        printf 'qrypt-%s-%s\n' "$1" "$2"
    fi
}

# print_manifest emits the naming rule and layout this script uses, so a Go test
# can prove they match pkg/selfupdate and pkg/install.
print_manifest() { # <version> <os> <arch>
    mver="$(version_num "$1")"
    printf 'asset=%s\n' "$(asset_name "$mver" "$2" "$3")"
    printf 'checksum=%s\n' "$(checksum_name "$mver")"
    printf 'inner=%s\n' "$(inner_name "$2" "$3")"
    printf 'real_dir=%s\n' "$REL_REAL"
    printf 'link_dir=%s\n' "$REL_LINK"
    printf 'marker_begin=%s\n' "$MARKER_BEGIN"
    printf 'marker_end=%s\n' "$MARKER_END"
}

detect_platform() {
    uname_s="$(uname -s)"
    uname_m="$(uname -m)"
    case "$uname_s" in
        Darwin) OS=darwin ;;
        Linux) OS=linux ;;
        *) error "unsupported operating system: $uname_s (install manually from the releases page)" ;;
    esac
    case "$uname_m" in
        x86_64 | amd64) ARCH=amd64 ;;
        arm64 | aarch64) ARCH=arm64 ;;
        *) error "unsupported architecture: $uname_m (install manually from the releases page)" ;;
    esac
}

# ── Network and verification ────────────────────────────────────────

download() { # <url> <dest>
    curl -fsSL --retry 2 -o "$2" "$1" || error "failed to download $1"
}

api_get() { # <url> <dest>
    if [ -n "$TOKEN" ]; then
        curl -fsSL -H "Authorization: Bearer $TOKEN" -o "$2" "$1"
    else
        curl -fsSL -o "$2" "$1"
    fi
}

latest_tag() {
    if [ -n "${QRYPT_INSTALL_LATEST_TAG:-}" ]; then
        printf '%s\n' "$QRYPT_INSTALL_LATEST_TAG"
        return 0
    fi
    api_get "$API_BASE/repos/$REPO/releases/latest" "$TMP/latest.json" ||
        error "failed to query the latest release (rate limit? set GITHUB_TOKEN)"
    tag="$(sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' "$TMP/latest.json" | head -n 1)"
    [ -n "$tag" ] || error "could not read the latest release tag"
    printf '%s\n' "$tag"
}

sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{ print $1 }'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | awk '{ print $1 }'
    elif command -v openssl >/dev/null 2>&1; then
        openssl dgst -sha256 "$1" | awk '{ print $NF }'
    else
        error "no sha256 tool found (need sha256sum, shasum, or openssl)"
    fi
}

verify_archive() { # <checksum-file> <asset-name> <archive>
    expected="$(awk -v n="$2" '$2 == n || $2 == "*" n { print $1; exit }' "$1")"
    [ -n "$expected" ] || error "no checksum published for $2"
    actual="$(sha256_of "$3")"
    if [ "$(printf '%s' "$expected" | tr 'A-F' 'a-f')" != "$(printf '%s' "$actual" | tr 'A-F' 'a-f')" ]; then
        error "checksum mismatch for $2"
    fi
}

# ── Install ─────────────────────────────────────────────────────────

check_existing_link() {
    if [ -e "$LINK" ] || [ -L "$LINK" ]; then
        current="$(readlink "$LINK" 2>/dev/null || true)"
        if [ "$current" = "$REAL" ]; then
            return 0
        fi
        if [ "$FORCE" = 1 ]; then
            return 0
        fi
        error "$LINK already exists and is not managed by this installer; re-run with --force to replace it"
    fi
}

install_binary() { # <archive> <inner-name>
    mkdir -p "$DATA_DIR" "$LINK_DIR" || error "could not create install directories"
    tar -xzf "$1" -C "$TMP" || error "failed to extract $1"
    inner_path="$(find "$TMP" -type f -name "$2" | head -n 1)"
    [ -n "$inner_path" ] || error "archive does not contain $2"

    staged="$DATA_DIR/.qrypt-new.$$"
    cp "$inner_path" "$staged" || error "could not stage the binary"
    chmod 755 "$staged"
    mv -f "$staged" "$REAL" || error "could not install the binary"

    ln -sfn "$REAL" "$LINK" || error "could not create the symlink $LINK"
}

# ── PATH configuration ──────────────────────────────────────────────

on_path() {
    case ":${PATH:-}:" in
        *":$LINK_DIR:"*) return 0 ;;
        *) return 1 ;;
    esac
}

# The written line must keep $HOME/$PATH literal for the shell that later
# sources the rc file, so the single quotes here are intentional.
# shellcheck disable=SC2016
path_line() {
    if [ "$LINK_DIR" = "$HOME/.local/bin" ]; then
        printf 'export PATH="$HOME/.local/bin:$PATH"\n'
    else
        printf 'export PATH="%s:$PATH"\n' "$LINK_DIR"
    fi
}

pick_rc_file() {
    shell_name="$(basename "${SHELL:-}")"
    case "$shell_name" in
        zsh) set -- "$HOME_DIR/.zprofile" "$HOME_DIR/.zshrc" ;;
        bash) set -- "$HOME_DIR/.bash_profile" "$HOME_DIR/.profile" "$HOME_DIR/.bashrc" ;;
        fish) set -- "$HOME_DIR/.config/fish/config.fish" ;;
        *) set -- ;;
    esac
    for candidate in "$@"; do
        if [ -f "$candidate" ] && [ -w "$candidate" ]; then
            printf '%s\n' "$candidate"
            return 0
        fi
    done
    for candidate in "$@"; do
        if [ ! -e "$candidate" ] && [ -w "$(dirname "$candidate")" ]; then
            printf '%s\n' "$candidate"
            return 0
        fi
    done
    return 1
}

ACTIVATE=""
configure_path() {
    if on_path; then
        info "$LINK_DIR is already on PATH"
        return 0
    fi
    if rc="$(pick_rc_file)"; then
        if grep -qF "$MARKER_BEGIN" "$rc" 2>/dev/null; then
            info "PATH is already configured in $rc"
        else
            {
                printf '\n%s\n' "$MARKER_BEGIN"
                path_line
                printf '%s\n' "$MARKER_END"
            } >>"$rc" || error "could not write to $rc"
            info "Added $LINK_DIR to PATH in $rc"
        fi
        ACTIVATE=". \"$rc\""
    else
        info "Add $LINK_DIR to your PATH:"
        info "  $(path_line)"
    fi
}

check_shadow() {
    found="$(command -v qrypt 2>/dev/null || true)"
    if [ -n "$found" ] && [ "$found" != "$LINK" ]; then
        warn "another qrypt is on PATH: $found"
        warn "this installer placed $LINK; it runs only once $LINK_DIR precedes that copy on PATH"
    fi
}

# ── Main ────────────────────────────────────────────────────────────

main() {
    while [ "$#" -gt 0 ]; do
        case "$1" in
            --version)
                [ "$#" -ge 2 ] || error "--version requires a value"
                VERSION="$2"
                shift 2
                ;;
            --force)
                FORCE=1
                shift
                ;;
            --help | -h)
                usage
                exit 0
                ;;
            --print-manifest)
                [ "$#" -ge 4 ] || error "--print-manifest requires <version> <os> <arch>"
                print_manifest "$2" "$3" "$4"
                exit 0
                ;;
            *)
                error "unknown argument: $1"
                ;;
        esac
    done

    command -v curl >/dev/null 2>&1 || error "curl is required"

    if [ -n "$VERSION" ]; then
        printf '%s' "$VERSION" | grep -Eq '^v?[0-9]+\.[0-9]+\.[0-9]+$' ||
            error "invalid version: $VERSION (expected something like v0.5.0)"
    fi

    detect_platform

    TMP="$(mktemp -d "${TMPDIR:-/tmp}/qrypt-install.XXXXXX")" || error "could not create a temporary directory"
    trap 'rm -rf "$TMP"' EXIT INT TERM

    if [ -n "$VERSION" ]; then
        TAG="v$(version_num "$VERSION")"
    else
        TAG="$(latest_tag)"
    fi
    VER="$(version_num "$TAG")"

    ASSET="$(asset_name "$VER" "$OS" "$ARCH")"
    CHECKSUM="$(checksum_name "$VER")"
    INNER="$(inner_name "$OS" "$ARCH")"

    check_existing_link

    info "Installing qrypt $TAG for $OS/$ARCH"
    download "$DOWNLOAD_BASE/$TAG/$ASSET" "$TMP/$ASSET"
    download "$DOWNLOAD_BASE/$TAG/$CHECKSUM" "$TMP/$CHECKSUM"
    verify_archive "$TMP/$CHECKSUM" "$ASSET" "$TMP/$ASSET"

    install_binary "$TMP/$ASSET" "$INNER"
    info_bold "Installed qrypt to $REAL"

    configure_path
    check_shadow

    printf '\n'
    info "qrypt $TAG is installed."
    if [ -n "$ACTIVATE" ]; then
        info "To use it in this shell, run:"
        info_bold "  $ACTIVATE"
    fi
    info_bold "  qrypt version"
}

main "$@"
