# Proposal

## Why

Re-running the installer is supposed to leave the user on the newest release, but three things can
stop that today. It resolves the newest tag through the unauthenticated GitHub API, which is
rate-limited (60 requests/hour/IP), so a re-run on a shared egress IP or CI can fail before it ever
downloads. On Windows the installer swaps the binary with `[System.IO.File]::Replace`, which throws
while `qrypt.exe` is running, so the most common reason to re-run — while qrypt is in use — is the
one that fails. And the Windows `PATH` is edited through `[Environment]::SetEnvironmentVariable`,
which expands `%VAR%` references and forces the registry value kind, disagreeing with the Go side in
`pkg/install` that preserves both. The Windows one-liner is also awkward: it assumes an already-open
PowerShell session and points at a long `raw.githubusercontent.com/.../main/...` URL.

## What Changes

- Publish `scripts/install.sh` and `scripts/install.ps1` as release assets, so the documented
  bootstrap URL is short, stable, and matches the release it installs rather than a moving `main`.
- Document the Windows bootstrap as the copy-pasteable `powershell -c "irm <url>|iex"` form, which
  works from `cmd.exe`, Git Bash, and any other shell, not only from inside PowerShell.
- Resolve the newest release without depending on the rate-limited API: follow the release `latest`
  redirect to its tag, and keep the token-authenticated API as a fallback.
- Re-running the installer installs the newest release; on Windows it replaces a running binary by
  moving the running image aside (leaving the displaced remnant that `qrypt uninstall` already
  sweeps) instead of failing on the locked file.
- Make the Windows installer write the user `PATH` the way `pkg/install` does: read the raw,
  unexpanded value, preserve its registry kind, and notify the system that the environment changed.
- Add tests: a smoke case that publishes a newer release and proves a re-run upgrades to it, and a
  Windows `PATH`-write case that proves `%VAR%` references and the value kind survive.

## Capabilities

### New Capabilities

(none)

### Modified Capabilities

- `installation`: re-running the installer must converge on the newest release (also while the
  installed binary is running on Windows); the documented bootstrap must be a copy-pasteable,
  release-matched one-liner; and the Windows `PATH` edit must preserve the user's existing registry
  value instead of expanding it.

## Impact

- `scripts/install.sh`, `scripts/install.ps1`, `.goreleaser.yaml` (release assets),
  `scripts/smoke-install.sh` and `scripts/smoke-install.ps1`, and the `pkg/install` naming/layout
  tests.
- `docs/for-user/quickstart.md`, `README.md`, `README.zh.md` (bootstrap one-liners).
- No change to `qrypt upgrade` or `qrypt uninstall` behavior; the Windows remnant the installer may
  leave is already covered by uninstall's existing sweep.
