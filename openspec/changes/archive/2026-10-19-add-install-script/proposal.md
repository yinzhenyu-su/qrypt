## Why

qrypt ships one binary per platform through GitHub Releases, but the first install is manual: find the
release, pick the archive, unzip it, and put the file on `PATH`. That friction is the whole reason a
`curl | sh` installer exists for tools like this. It also undermines the `qrypt upgrade` command added
earlier: a manual unzip usually lands in a root-owned directory such as `/usr/local/bin`, where every
upgrade needs `sudo` — exactly the friction self-update was meant to remove. A first-install path that
chooses a stable, user-writable location makes `upgrade` work without privilege, and a matching
uninstall closes the lifecycle.

## What Changes

- Add a shell bootstrap `install.sh` (`curl -fsSL <url> | sh`) for linux and darwin, and a PowerShell
  bootstrap `install.ps1` (`irm <url> | iex`) for windows. Each detects the platform, downloads the
  newest release, **verifies its sha256** against the release's `*_checksums.txt`, and installs the
  binary to a versionless path (`~/.qrypt/bin/qrypt`, or `%USERPROFILE%\.qrypt\bin\qrypt.exe` on
  windows). On linux/darwin the shell installer also creates the symlink `~/.local/bin/qrypt`.
- Add `-Version` / `--version <v>` to pin the installed release, and keep re-runs idempotent.
- Add PATH configuration: on linux/darwin only `~/.local/bin` is placed on `PATH` (never the private
  `~/.qrypt/bin`), through one idempotent marker-delimited block in a login-shell rc file; on windows
  only `%USERPROFILE%\.qrypt\bin` is added, as a single **user-scope** registry entry so no
  administrator rights are needed. When the change cannot be applied, print it instead of editing.
- Add `qrypt uninstall` to reverse an install: on linux/darwin remove the symlink (only if it points
  at the managed binary), the binary, and the marker block; on windows remove the binary and the
  user-scope `PATH` entry. Keep user data (config, cache, logs, sessions) unless `--purge` is given.
- Require every installer to resolve platform and asset names by the **same rule as the updater**, and
  add a CI drift check that fails when `install.sh` or `install.ps1` and `pkg/selfupdate` disagree.
- Document the manual download steps as the fallback when a binary is missing or broken.

Non-goals: package-manager channels (Homebrew/winget/scoop), versioned side-by-side installs, release
signature verification, and automatic/timed updates.

## Capabilities

### New Capabilities

- `installation`: first install through a platform-appropriate bootstrap (shell on linux/darwin,
  PowerShell on windows), the on-disk install layout, download integrity verification, PATH
  configuration, and uninstall.

### Modified Capabilities

<!-- none: `self-update` is not yet an archived main spec, so its shared naming rule is captured here
     as a requirement of `installation` rather than as a delta to it. -->

## Impact

- `scripts/install.sh` and `scripts/install.ps1` (new): the bootstraps; `scripts/`-level CI (shellcheck
  for the shell script, a container smoke for linux, and a Windows smoke on the windows CI job).
- `pkg/install`: layout constants, the marker block, rc-file selection, and platform-tagged PATH
  handling (rc files on Unix, the user registry on Windows) so the uninstall command is unit-testable.
- `internal/cli/uninstall` (new command) and registration in `internal/cli/command_root.go`.
- `pkg/selfupdate`: behavior unchanged; the drift check reads its asset-naming rule.
- `docs/for-user/quickstart.md`, `docs/for-user/cli.md`, `README.md`, and `README.zh.md`: the one-line
  install for each platform and `qrypt uninstall`.
