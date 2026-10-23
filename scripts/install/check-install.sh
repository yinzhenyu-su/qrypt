#!/usr/bin/env bash
# Checks for the qrypt installers: required shellcheck lint plus the Go tests
# that prove scripts/install/install.sh and scripts/install/install.ps1 agree
# with pkg/selfupdate about release naming. The PowerShell side runs where pwsh
# is installed (the Windows CI job) and skips elsewhere.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

if ! command -v shellcheck >/dev/null 2>&1; then
    echo "check-install: shellcheck is required to validate Bash installers" >&2
    echo "install shellcheck, or run scripts/ci/check-all.sh --install-system-deps on Linux/macOS" >&2
    exit 1
fi

shellcheck scripts/install/install.sh scripts/install/smoke-install.sh scripts/install/check-install.sh

go test ./pkg/install/ -run 'TestInstaller|TestPowerShellInstallerNamingMatchesUpdater' -count=1

echo "install script checks passed"
