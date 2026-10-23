#!/usr/bin/env bash
# Inner bash -c scripts expand their own $VAR expressions.
# shellcheck disable=SC2016
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
export GOCACHE="${GOCACHE:-/tmp/qrypt-go-build}"

usage() {
  cat <<'EOF'
Usage: scripts/ci/check-all.sh [--install-system-deps]

Run the repository's CI check job locally.

Linux/macOS only: the Windows gate (build + vet + unit tests + localfs
smoke) runs in GitHub Actions on windows-latest (ci.yaml test-windows),
and the real WinFsp mount smoke runs nightly (nightly.yaml windows-mount).

Options:
  --install-system-deps  Install missing Linux FUSE3/ShellCheck packages or macOS ShellCheck.
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

# CI pins Go 1.27.3; any newer 1.27.x patch also satisfies the toolchain line.
case "$(go env GOVERSION)" in
  go1.27.*) ;;
  *)
    echo "CI requires go1.27.x; found $(go env GOVERSION)" >&2
    exit 1
    ;;
esac

platform="$(uname -s)"
if [ "$platform" = "Linux" ]; then
	missing_packages=()
	if ! pkg-config --exists fuse3 2>/dev/null; then
		missing_packages+=(libfuse3-dev fuse3)
	fi
	if ! command -v shellcheck >/dev/null 2>&1; then
		missing_packages+=(shellcheck)
	fi
	if [ "${#missing_packages[@]}" -gt 0 ]; then
		if [ "$install_system_deps" = true ]; then
			sudo apt-get update
			sudo apt-get install -y "${missing_packages[@]}"
		else
			echo "Linux CI dependencies are missing: ${missing_packages[*]}" >&2
			echo "Re-run with --install-system-deps" >&2
			exit 1
		fi
	fi
elif [ "$platform" = "Darwin" ] && ! command -v shellcheck >/dev/null 2>&1; then
	if [ "$install_system_deps" = true ]; then
		if ! command -v brew >/dev/null 2>&1; then
			echo "Homebrew is required to install ShellCheck automatically on macOS." >&2
			echo "Install Homebrew or install ShellCheck manually." >&2
			exit 1
		fi
		brew install shellcheck
	else
		echo "ShellCheck is required on macOS. Install it with 'brew install shellcheck'," >&2
		echo "or re-run with --install-system-deps." >&2
		exit 1
	fi
elif ! command -v shellcheck >/dev/null 2>&1; then
	echo "ShellCheck is required. Install it with your platform package manager." >&2
	exit 1
fi

# The layers run in order rather than concurrently. ci.yaml spreads them over
# four runners, and overlapping them here is worth about a third of the wall
# clock when the box is idle (measured 87s against 126s) - but the suites are
# not hardened for it: four separate tests assert on a snapshot that the load
# left stale, and each one only showed up by running the gate that way. Overlap
# only the layers whose work is genuinely waiting rather than computing, which
# is why test-layers.sh runs the vfs-stability repeats concurrently and
# check-coverage.sh fans its profiles out.
step() {
  printf '\n== %s ==\n' "$1"
  shift
  "$@"
}

step "Vet" go vet ./...
scripts/ci/install-tools.sh
TOOLS_DIR="${QRYPT_TOOLS_DIR:-${HOME}/.cache/qrypt-tools}"
step "Staticcheck" "$TOOLS_DIR/staticcheck" ./...
step "Golangci-lint" "$TOOLS_DIR/golangci-lint" run ./...
step "Architecture boundaries" scripts/ci/check-arch.sh
step "Vulncheck" "$TOOLS_DIR/govulncheck" ./...

step "Format" bash -c '
  unformatted=$(gofmt -l .)
  if [ -n "$unformatted" ]; then
    echo "gofmt needed on:"
    echo "$unformatted"
    exit 1
  fi
'

step "Schema metadata" bash -c '
  generated_before=$(mktemp -d "${TMPDIR:-/tmp}/qrypt-generated-schema.XXXXXX")
  trap "rm -rf \"$generated_before\"" EXIT
  cp pkg/drive/builtin_schema_gen.go "$generated_before/builtin_schema_gen.go"
  cp internal/cli/i18n/catalog_gen.go "$generated_before/catalog_gen.go"
  python3 scripts/schema/check-schema.py
  python3 scripts/schema/gen-drive-schema.py
  python3 scripts/schema/check-cli-i18n.py
  python3 scripts/schema/gen-cli-i18n.py
  if ! cmp -s "$generated_before/builtin_schema_gen.go" pkg/drive/builtin_schema_gen.go; then
    echo "generated driver schema is stale; re-run:"
    echo "  python3 scripts/schema/gen-drive-schema.py"
    exit 1
  fi
  if ! cmp -s "$generated_before/catalog_gen.go" internal/cli/i18n/catalog_gen.go; then
    echo "generated CLI catalogs are stale; re-run:"
    echo "  python3 scripts/schema/gen-cli-i18n.py"
    exit 1
  fi
'

step "Generated docs in sync" bash -c '
  python3 scripts/schema/test-gen-docs.py
  generated_before=$(mktemp -d "${TMPDIR:-/tmp}/qrypt-generated-docs.XXXXXX")
  trap "rm -rf \"$generated_before\"" EXIT
  cp docs/for-user/full-config.md "$generated_before/full-config.md"
  cp docs/for-user/support-drivers.md "$generated_before/support-drivers.md"
  python3 scripts/schema/gen-config-docs.py
  python3 scripts/schema/gen-driver-docs.py
  if ! cmp -s "$generated_before/full-config.md" docs/for-user/full-config.md ||
     ! cmp -s "$generated_before/support-drivers.md" docs/for-user/support-drivers.md; then
    echo "generated user docs are stale; re-run:"
    echo "  python3 scripts/schema/gen-config-docs.py"
    echo "  python3 scripts/schema/gen-driver-docs.py"
    git diff --stat -- docs/for-user/full-config.md docs/for-user/support-drivers.md
    exit 1
  fi
'

step "Install script" scripts/install/check-install.sh
step "Fast suite" scripts/ci/test-layers.sh fast
step "Race suite" scripts/ci/test-layers.sh race
step "VFS stability" scripts/ci/test-layers.sh vfs-stability
step "Localfs smoke" scripts/ci/test-layers.sh smoke

echo
echo "== local CI check passed =="
