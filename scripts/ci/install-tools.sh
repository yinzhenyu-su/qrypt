#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

TOOLS_DIR="${QRYPT_TOOLS_DIR:-${HOME}/.cache/qrypt-tools}"
VERSIONS_FILE="$TOOLS_DIR/versions"
mkdir -p "$TOOLS_DIR"

tool_version() {
  local name="$1"
  sed -nE "s/^[[:space:]]*${name}[[:space:]]*=[[:space:]]*\"([^\"]+)\".*/\1/p" tools.go
}

# CI pins the Go toolchain in exactly one place: go.mod. Read it here instead
# of hardcoding a second copy, and let QRYPT_GO_TOOLCHAIN override it for local
# experiments. The toolchain directive wins over the go directive when both
# exist, because that is what the go command itself selects.
go_toolchain() {
  local version
  version="$(sed -nE 's/^toolchain[[:space:]]+go([0-9]+\.[0-9]+(\.[0-9]+)?).*/\1/p' go.mod | head -n1)"
  if [ -z "$version" ]; then
    version="$(sed -nE 's/^go[[:space:]]+([0-9]+\.[0-9]+(\.[0-9]+)?).*/\1/p' go.mod | head -n1)"
  fi
  if [ -z "$version" ]; then
    echo "cannot read the Go version from go.mod" >&2
    return 1
  fi
  printf 'go%s\n' "$version"
}

staticcheck_version="$(tool_version StaticcheckVersion)"
golangci_version="$(tool_version GolangCILintVersion)"
govulncheck_version="$(tool_version GovulncheckVersion)"
shellcheck_version="$(tool_version ShellcheckVersion)"
actionlint_version="$(tool_version ActionlintVersion)"
expected_versions=$(printf '%s\n' \
  "staticcheck=$staticcheck_version" \
  "golangci-lint=$golangci_version" \
  "govulncheck=$govulncheck_version" \
  "shellcheck=$shellcheck_version" \
  "actionlint=$actionlint_version")

if [[ -f "$VERSIONS_FILE" && -x "$TOOLS_DIR/shellcheck" && -x "$TOOLS_DIR/actionlint" ]] &&
  cmp -s <(printf '%s\n' "$expected_versions") "$VERSIONS_FILE"; then
  exit 0
fi

install_shellcheck() {
  local os arch asset checksum archive tmp
  case "$(uname -s)" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    *) echo "unsupported ShellCheck platform: $(uname -s)" >&2; return 1 ;;
  esac
  case "$(uname -m)" in
    x86_64|amd64) arch=x86_64 ;;
    arm64|aarch64) arch=aarch64 ;;
    *) echo "unsupported ShellCheck architecture: $(uname -m)" >&2; return 1 ;;
  esac
  asset="${os}.${arch}"
  case "$asset" in
    linux.x86_64) checksum=6c881ab0698e4e6ea235245f22832860544f17ba386442fe7e9d629f8cbedf87 ;;
    linux.aarch64) checksum=324a7e89de8fa2aed0d0c28f3dab59cf84c6d74264022c00c22af665ed1a09bb ;;
    darwin.x86_64) checksum=ef27684f23279d112d8ad84e0823642e43f838993bbb8c0963db9b58a90464c2 ;;
    darwin.aarch64) checksum=bbd2f14826328eee7679da7221f2bc3afb011f6a928b848c80c321f6046ddf81 ;;
  esac
  tmp=$(mktemp -d "${TMPDIR:-/tmp}/qrypt-shellcheck.XXXXXX")
  archive="$tmp/shellcheck.tar.xz"
  curl -fsSL "https://github.com/koalaman/shellcheck/releases/download/v${shellcheck_version}/shellcheck-v${shellcheck_version}.${asset}.tar.xz" -o "$archive"
  if command -v sha256sum >/dev/null 2>&1; then
    printf '%s  %s\n' "$checksum" "$archive" | sha256sum -c -
  else
    [ "$(shasum -a 256 "$archive" | awk '{print $1}')" = "$checksum" ]
  fi
  tar -xJf "$archive" -C "$tmp"
  install -m 0755 "$tmp/shellcheck-v${shellcheck_version}/shellcheck" "$TOOLS_DIR/shellcheck"
  rm -rf "$tmp"
}

# The Go tools build independently, so overlap them: a cold cache then costs
# the slowest tool instead of the sum. golangci-lint is much the largest, so it
# sets that floor; shellcheck is a Go-independent download that overlaps too.
go_toolchain_version="$(go_toolchain)"
install_tool() {
  # CI pins the project toolchain to the go.mod version. Do not let a tool
  # module's go.mod silently select an older toolchain, which can no longer
  # parse the project's Go syntax.
  GOTOOLCHAIN="${QRYPT_GO_TOOLCHAIN:-$go_toolchain_version}" GOBIN="$TOOLS_DIR" go install "$1"
}

pids=()
install_tool "honnef.co/go/tools/cmd/staticcheck@$staticcheck_version" &
pids+=("$!")
install_tool "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$golangci_version" &
pids+=("$!")
install_tool "golang.org/x/vuln/cmd/govulncheck@$govulncheck_version" &
pids+=("$!")
install_tool "github.com/rhysd/actionlint/cmd/actionlint@$actionlint_version" &
pids+=("$!")
install_shellcheck &
pids+=("$!")

status=0
for pid in "${pids[@]}"; do
  wait "$pid" || status=1
done
# Versions are stamped only once every install succeeded, so a failed
# download leaves the marker stale and the next run retries it.
if [ "$status" -ne 0 ]; then
  echo "installing CI tools failed; re-run $0" >&2
  exit 1
fi

# A go install that exits 0 can still leave the wrong binary behind (stale
# GOBIN entry, interrupted earlier run); fail early here instead of inside the
# gate that runs the tool.
for tool in staticcheck golangci-lint govulncheck shellcheck actionlint; do
  if [ ! -x "$TOOLS_DIR/$tool" ]; then
    echo "installed CI tools are incomplete: $TOOLS_DIR/$tool is missing" >&2
    exit 1
  fi
done

printf '%s\n' "$expected_versions" >"$VERSIONS_FILE"
