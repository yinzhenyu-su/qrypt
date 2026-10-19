## Why

qrypt ships a single binary per platform through GitHub Releases, but the only way to update is to
find the release page, pick the right archive, unzip it, and copy the file over the current binary by
hand. Every other tooling-style CLI offers a one-command update; users expect `qrypt upgrade` to
fetch the newest verified build and put it in place.

## What Changes

- Add `qrypt upgrade`: fetch the latest stable GitHub release, verify it, and atomically replace the
  running executable (linux, darwin, windows).
- Add `--check` to report current vs latest without downloading or writing.
- Add `--to <version>` to install a specific release (including a downgrade), `--force` to override
  the release-build and same/newer guards, and `--json` for machine-readable output.
- Require sha256 verification against the release's `*_checksums.txt`; a mismatch or missing entry
  aborts and leaves the current binary untouched.
- Refuse to replace a binary the process cannot write; print the exact re-run command instead of
  escalating privileges.

Non-goals: passive "update available" notices on other commands, automatic/timed updates, release
signature verification (cosign), delta updates, and package-manager (Homebrew/winget) integration.

## Capabilities

### New Capabilities

- `self-update`: replacing the running qrypt executable with a newer, checksum-verified GitHub
  release, including the version gate, release/asset selection, download verification, and
  per-platform atomic swap.

### Modified Capabilities

<!-- none: `cli-help` is a standing contract the new command satisfies, not a requirement change -->

## Impact

- `internal/cli/upgrade`: new thin command package, registered in `internal/cli/command_root.go`.
- `pkg/selfupdate`: new package for release lookup, semver comparison, download, checksum
  verification, extraction, and platform-specific atomic replacement.
- `pkg/util`: extend the existing atomic/replace helpers with the Windows rename-and-place swap.
- `pkg/buildinfo`: consumed for the current version; a non-release build is rejected unless `--force`.
- `docs/for-user/cli.md`: document the command.
- Tests: release-fixture HTTP server (httptest) for download/verify/select paths, plus swap tests
  per platform.
