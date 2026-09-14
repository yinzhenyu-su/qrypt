#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
export GOCACHE="${GOCACHE:-/tmp/qrypt-go-build}"

usage() {
  cat <<'EOF'
Usage: scripts/ci-check.sh [--install-system-deps]

Run the repository's CI check job locally.

Linux/macOS only: the Windows gate (build + vet + unit tests + localfs
smoke) runs in GitHub Actions on windows-latest (ci.yaml test-windows),
and the real WinFsp mount smoke runs nightly (nightly.yaml windows-mount).

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

# CI pins go1.27.0 exactly; any go1.27.x patch satisfies the toolchain line.
case "$(go env GOVERSION)" in
  go1.27.*) ;;
  *)
    echo "CI requires go1.27.x; found $(go env GOVERSION)" >&2
    exit 1
    ;;
esac

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

# The layers run in order rather than concurrently. ci.yaml spreads them over
# four runners, and overlapping them here is worth about a third of the wall
# clock when the box is idle (measured 87s against 126s) - but the suites are
# not hardened for it: four separate tests assert on a snapshot that the load
# left stale, and each one only showed up by running the gate that way. Overlap
# only the layers whose work is genuinely waiting rather than computing, which
# is why test-layers.sh runs the vfs-stability repeats concurrently and
# coverage.sh fans its profiles out.
step() {
  printf '\n== %s ==\n' "$1"
  shift
  "$@"
}

step "Vet" go vet ./...
scripts/install-ci-tools.sh
TOOLS_DIR="${QRYPT_TOOLS_DIR:-${HOME}/.cache/qrypt-tools}"
step "Staticcheck" "$TOOLS_DIR/staticcheck" ./...
step "Golangci-lint" "$TOOLS_DIR/golangci-lint" run ./...
step "Architecture boundaries" scripts/check-arch.sh
step "Vulncheck" "$TOOLS_DIR/govulncheck" ./...

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
