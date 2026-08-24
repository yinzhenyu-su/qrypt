#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
export GOCACHE="${GOCACHE:-/tmp/qrypt-go-build}"

usage() {
  cat <<'EOF'
Usage: scripts/ci-check.sh [--install-system-deps]

Run the repository's CI check job locally.

Options:
  --install-system-deps  On Linux, install libfuse-dev with apt-get when needed.
  -h, --help             Show this help.
EOF
}

install_system_deps=false
while [ "$#" -gt 0 ]; do
  case "$1" in
    --install-system-deps)
      install_system_deps=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [ "$(go env GOVERSION)" != "go1.27.0" ]; then
  echo "CI requires go1.27.0; found $(go env GOVERSION)" >&2
  exit 1
fi

if [ "$(uname -s)" = "Linux" ]; then
  if ! pkg-config --exists fuse 2>/dev/null; then
    if [ "$install_system_deps" = true ]; then
      sudo apt-get update
      sudo apt-get install -y libfuse-dev
    else
      echo "Linux FUSE headers are missing. Re-run with --install-system-deps" >&2
      exit 1
    fi
  fi
fi

step() {
  printf '\n== %s ==\n' "$1"
  shift
  "$@"
}

step "Vet" go vet ./...
step "Staticcheck" go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...
step "Golangci-lint" go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.1 run ./...
step "Architecture boundaries" scripts/check-arch.sh
step "Vulncheck" go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...

step "Format" bash -c '
  unformatted=$(gofmt -l .)
  if [ -n "$unformatted" ]; then
    echo "gofmt needed on:"
    echo "$unformatted"
    exit 1
  fi
'

step "Generated docs in sync" bash -c '
  python3 scripts/gen-config-docs.py
  python3 scripts/gen-driver-docs.py
  if ! git diff --quiet -- docs/for-user/; then
    echo "docs/for-user is stale; re-run:"
    echo "  python3 scripts/gen-config-docs.py"
    echo "  python3 scripts/gen-driver-docs.py"
    git diff --stat docs/for-user/
    exit 1
  fi
'

step "Fast suite" scripts/test-layers.sh fast
step "Race suite" scripts/test-layers.sh race
step "VFS stability" scripts/test-layers.sh vfs-stability
step "Localfs smoke" scripts/test-layers.sh smoke

echo
echo "== local CI check passed =="
