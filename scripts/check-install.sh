#!/usr/bin/env bash
# Checks for the qrypt installers: shellcheck, when available, plus the Go tests
# that prove scripts/install.sh and scripts/install.ps1 agree with pkg/selfupdate
# about release naming. The PowerShell side runs where pwsh is installed (the
# Windows CI job) and skips elsewhere.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if command -v shellcheck >/dev/null 2>&1; then
    shellcheck scripts/install.sh scripts/smoke-install.sh scripts/check-install.sh
else
    echo "check-install: shellcheck not found; skipping shell lint (install it to run locally)"
fi

go test ./pkg/install/ -run 'TestInstaller|TestPowerShellInstallerNamingMatchesUpdater' -count=1

echo "install script checks passed"
