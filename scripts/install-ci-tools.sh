#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

TOOLS_DIR="${QRYPT_TOOLS_DIR:-${HOME}/.cache/qrypt-tools}"
VERSIONS_FILE="$TOOLS_DIR/versions"
mkdir -p "$TOOLS_DIR"

tool_version() {
  local name="$1"
  sed -nE "s/^[[:space:]]*${name}[[:space:]]*=[[:space:]]*\"([^\"]+)\".*/\1/p" tools.go
}

staticcheck_version="$(tool_version StaticcheckVersion)"
golangci_version="$(tool_version GolangCILintVersion)"
govulncheck_version="$(tool_version GovulncheckVersion)"
expected_versions=$(printf '%s\n' \
  "staticcheck=$staticcheck_version" \
  "golangci-lint=$golangci_version" \
  "govulncheck=$govulncheck_version")

if [[ -f "$VERSIONS_FILE" ]] && cmp -s <(printf '%s\n' "$expected_versions") "$VERSIONS_FILE"; then
  exit 0
fi

GOBIN="$TOOLS_DIR" go install "honnef.co/go/tools/cmd/staticcheck@$staticcheck_version"
GOBIN="$TOOLS_DIR" go install "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$golangci_version"
GOBIN="$TOOLS_DIR" go install "golang.org/x/vuln/cmd/govulncheck@$govulncheck_version"
printf '%s\n' "$expected_versions" >"$VERSIONS_FILE"
