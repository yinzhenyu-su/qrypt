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

# The three tools build independently, so overlap them: a cold cache then
# costs the slowest tool instead of the sum. golangci-lint is much the
# largest of the three, so it sets that floor.
install_tool() {
  GOBIN="$TOOLS_DIR" go install "$1"
}

pids=()
install_tool "honnef.co/go/tools/cmd/staticcheck@$staticcheck_version" &
pids+=("$!")
install_tool "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$golangci_version" &
pids+=("$!")
install_tool "golang.org/x/vuln/cmd/govulncheck@$govulncheck_version" &
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

printf '%s\n' "$expected_versions" >"$VERSIONS_FILE"
