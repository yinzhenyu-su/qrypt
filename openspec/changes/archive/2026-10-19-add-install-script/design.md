## Context

See `proposal.md` for motivation and `specs/installation/spec.md` for requirements.

Constraints found in the codebase and in the reference implementations:

- Releases publish `qrypt_<version>_<goos>_<goarch>.tar.gz|.zip` (version without the leading `v`)
  plus `qrypt_<version>_checksums.txt`; the in-archive binary is `qrypt-<goos>-<goarch>[.exe]`. The
  same rule is implemented in Go by `pkg/selfupdate`, so an installer that re-implements it is a
  second copy that can drift.
- The release repository (`yinzhenyu-su/qrypt`) differs from the Go module path, so the installer
  must hardcode the download repo or take it from a variable.
- `~/.qrypt` is qrypt's data root today: it holds the config candidate `~/.qrypt/qrypt.toml`,
  `logs/`, `run/`, and sync sessions. `RemoveAll` call sites only target subdirectories (thumbnail
  cache, read-cache, sync session dirs), so nothing wipes the root — but installing into it mixes
  install artifacts with runtime/user data.
- `os.Executable()` plus `filepath.EvalSymlinks` yields the real binary; on Linux it already resolves
  symlinks, so the binary cannot infer its own symlink from itself.
- The `qrypt upgrade` command (already specified) replaces the resolved real binary and preserves the
  symlink, so a symlinked, versionless layout needs no change there. Its Windows path renames the
  running `.exe` aside and places the new one, so a writable `%USERPROFILE%\.qrypt\bin\qrypt.exe`
  upgrades without administrator rights.
- Windows has no `~/.local/bin` convention and per-user symlink creation needs Developer Mode or
  administrator rights, so the Unix two-path layout (private dir + symlink) does not carry over; the
  install directory itself is the entry point, and `PATH` is a user-scope registry value.
- References: rustup installs via a shell script and removes itself with a Go/Rust command
  (`self uninstall`), editing rc files with a single exact line it later matches verbatim
  (whitespace-fragile by its own admission); bun ships a shell and a PowerShell installer (`irm … | iex`)
  and auto-edits `~/.zshrc`/`~/.bashrc` (interactive files) with a `# bun` block and no marker check.
  Neither verifies a checksum.

## Goals / Non-Goals

**Goals:**

- One-command install that chooses a location where `qrypt upgrade` needs no privilege.
- Removal that is complete, safe, idempotent, and reversible, without disturbing the user's own
  `PATH` or data.
- A single, checked source of truth for release/asset naming shared with the updater.

**Non-Goals:**

- Package managers, versioned side-by-side installs, signature verification.
- A manifest of install actions: the layout is fixed, so uninstall can re-derive it.

## Decisions

### Layout: versionless binary; symlink on Unix, direct on Windows

On linux and darwin, install the binary at `~/.qrypt/bin/qrypt` (versionless) and link
`~/.local/bin/qrypt` to it. The symlink keeps `PATH` stable across versions, the versionless real name
keeps `qrypt upgrade` a no-op, and the private directory keeps the entry point in the XDG-correct
location. On windows, install to `%USERPROFILE%\.qrypt\bin\qrypt.exe` with no symlink layer (see below).

_Alternatives:_ install straight to `~/.local/bin/qrypt` — simpler, but makes "is this ours?" harder
at uninstall and puts a managed file among the user's own tools; version-in-filename — rejected
because `upgrade` preserves the name and would leave it stale.

_Known cost:_ this puts the binary under the data root `~/.qrypt`. Nothing wipes that root today, but
if the mixing is unwanted, use `~/.qrypt/install/bin` instead — a one-line change to the layout
constant.

### Windows: direct layout and a user-scope registry PATH

Windows has no user-symlink convention, so the installer places the binary directly at
`%USERPROFILE%\.qrypt\bin\qrypt.exe` and that directory is the `PATH` entry. `PATH` is edited at user
scope through `[Environment]::SetEnvironmentVariable('Path', …, 'User')`, so no administrator rights
are needed; it is idempotent (append only when absent) and reversible (uninstall removes the single
entry naming that directory). The registry has no comment syntax, so there is no marker block; the
entry is unambiguous because it names qrypt's own private directory.

_Alternatives:_ create a per-user symlink in a `PATH` directory — rejected because it needs Developer
Mode or administrator rights and Windows has no agreed link directory; a machine-scope `PATH` entry —
rejected because it needs administrator rights and affects other users.

### Windows uninstall clears displaced binaries and reports what it cannot

`qrypt uninstall` runs from the managed `qrypt.exe`, so Windows cannot delete the running image; the
same mechanism `qrypt upgrade` already uses renames it aside as `<binary>.old.<pid>`. A prior upgrade
can also have left `<binary>.old`, which only a later upgrade clears today. Uninstall therefore sweeps
every `qrypt.exe.old*` beside the managed binary, and when the file it must dispose of is the running
image it reports that copy as still present (naming the file) instead of silently leaving the install
directory's last occupant behind. The install directory is removed only once it is empty.

User-facing docs stay minimal by design: the reference names the command and the data-preserved
default, and the deferred remnant is surfaced by the command's own output rather than explained in
prose (users are not expected to reason about `.old` files).

_Alternatives:_ a detached helper process that deletes the running image after exit — rejected as
disproportionate for one file; relying solely on `MoveFileEx(MOVEFILE_DELAY_UNTIL_REBOOT)` — it stays
as a best-effort attempt, but it needs administrator rights, so it cannot be the only path; having
`install.ps1` also sweep on reinstall — deferred, since uninstall now clears the leftovers and a later
upgrade already does; extracting one shared `.old` helper for `pkg/selfupdate` and `pkg/install` —
deferred, because the two serve different needs (the swap versus self-removal) and merging them is a
refactor rather than part of closing this seam.

### Bootstrap is a shell script plus a PowerShell script; uninstall is a Go command

The first install cannot be a qrypt subcommand (chicken and egg), so `scripts/install.sh` (linux,
darwin) and `scripts/install.ps1` (windows, `irm … | iex`) are the bootstraps. Uninstall has no such
constraint, so it is `qrypt uninstall`, which is unit-testable, offline, and mirrors `qrypt upgrade`.

_Alternatives:_ an `uninstall.sh`/`uninstall.ps1` served like the installers — more symmetric and works
when the binary is broken, but duplicates the `PATH`-editing logic; documented manual steps stay
available as the fallback.

### PATH: one entry per platform, idempotent and reversible

Only the entry-point directory is added, and only when it is not already present. On linux and darwin
that is `~/.local/bin`, written as one marker-delimited block (`# >>> qrypt >>>` … `# <<< qrypt <<<`)
in a login-shell file (`.zprofile`, `.bash_profile`/`.profile`, fish config) so scripts and non-login
processes see the path and removal is block-exact. On windows it is the install directory, written as
one user-scope registry entry.

_Alternatives:_ rustup's single exact-line removal — simpler but fragile to whitespace. bun's
interactive-file block with a `# bun` comment that is never checked — rejected for the duplication it
allows (the author's own `~/.local/bin` is already duplicated twice).

### Integrity: verify sha256, mirroring the updater

The installer fetches the release checksum file and verifies the archive before extracting, exactly as
`pkg/selfupdate` does. bun trusts TLS alone; qrypt already publishes checksums, so this is strictly
better and keeps the two paths aligned.

### Existing file, stale copies, and unsupported platforms

An unmanaged entry point is not overwritten without consent: on Unix an existing `~/.local/bin/qrypt`,
on Windows an existing `%USERPROFILE%\.qrypt\bin\qrypt.exe`. After install the installer resolves the
`qrypt` that will run (`command -v qrypt`, or `Get-Command qrypt` on Windows) and warns when a
different copy would. A platform with no matching asset fails loudly instead of installing something
incompatible.

### Shared release-naming contract with a CI drift check

The installer derives platform, asset name, checksum name, and inner binary name by the same rule as
`pkg/selfupdate`, and a check fails when the two disagree. This is the one piece of `installation` that
exists because `self-update` exists.

## Risks / Trade-offs

- [`curl | sh` and `irm | iex` are trust decisions] → Publish the scripts in-repo (reviewable), verify
  checksums, and support version pinning; a signature remains a separate, deferred decision.
- [Auto-editing rc files surprises users] → Only one marker block, only when the dir is missing from
  `PATH`, never touch pre-existing lines, and make it removable by `qrypt uninstall`.
- [Editing the user registry `PATH` can truncate a long value] → Use the .NET
  `SetEnvironmentVariable` API rather than `setx` (which truncates), preserve the existing value, and
  append only when the directory is absent.
- [Installing under `~/.qrypt` mixes install with data] → No current code wipes the root; if that
  changes, move to `~/.qrypt/install/bin`.
- [The installers and the updater drift] → The CI drift check (above).
- [Windows has no symlink layer, so uninstall cannot rely on one] → Uninstall targets the binary and
  the exact `PATH` entry naming qrypt's own directory.
- [The Windows naming-drift check needs PowerShell] → Run it where `pwsh` is available (the Windows CI
  job) and skip it where it is not.
- [Uninstall cannot infer the symlink on Linux, where `os.Executable` resolves it] → Uninstall scans
  the canonical link dir for an entry whose target is the managed binary.
- [Removing the running binary] → Unix unlinks a running image safely; on Windows the running `.exe`
  is renamed aside by the same mechanism `qrypt upgrade` uses.
- [Windows uninstall cannot delete the running image] → Sweep the displaced `qrypt.exe.old*` files,
  report the running copy as still present instead of claiming a clean removal, and let a later run
  (or the best-effort reboot delete) clear it.

## Migration Plan

Additive: two new scripts and a new command, no data migration and no change to existing behavior.
Rollback is deleting the scripts and the command registration. Uninstall is the user-facing reverse.

## Open Questions

- Where the installers are served from (a moving `main` URL versus a release-pinned URL).
- Whether to physically separate the install directory from the data root.
- Whether a package-manager channel (Homebrew/winget/scoop) follows later.
