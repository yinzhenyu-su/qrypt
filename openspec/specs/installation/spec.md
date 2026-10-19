# installation Specification

## Purpose

Give qrypt a one-command first install that lands the binary in a stable, user-writable location and
puts it on `PATH`, plus a matching uninstall, so the tool can be installed and removed without manual
archive handling and without privileges.

## Requirements

### Requirement: One-command first install

A bootstrap script MUST install qrypt from a single invocation on each supported platform: a shell
one-liner on linux and darwin, and a PowerShell one-liner on windows. It MUST detect the running
operating system and architecture, download the applicable release for that platform, and install it.
When a specific version is requested, it MUST install that release instead of the newest one.
Re-running the installer MUST be safe: it MUST converge on the same result without duplicating its
effects or corrupting an existing install.

#### Scenario: Newest release is installed on Unix

- **WHEN** a user runs the shell bootstrap on linux or darwin with no prior install
- **THEN** the newest stable release for that platform is downloaded, verified, and installed, and the `qrypt` command becomes available

#### Scenario: Newest release is installed on Windows

- **WHEN** a user runs the PowerShell bootstrap on windows with no prior install
- **THEN** the newest stable release for that architecture is downloaded, verified, and installed, and the `qrypt` command becomes available

#### Scenario: A specific version is requested

- **WHEN** the user requests a version such as `v0.5.0`
- **THEN** that release is installed instead of the newest one

#### Scenario: Re-running the installer converges

- **WHEN** the user runs a bootstrap script a second time
- **THEN** the install is left in a valid state with no duplicated `PATH` entries and no duplicate or corrupt binary

### Requirement: Install layout

The installer MUST place the real binary in a versionless path and mark it executable: on linux and
darwin at `~/.qrypt/bin/qrypt`, and on windows at `%USERPROFILE%\.qrypt\bin\qrypt.exe`. On linux and
darwin it MUST additionally expose the binary through a symlink at `~/.local/bin/qrypt`. Windows has
no user-symlink convention, so the binary itself is the entry point there. The binary's path MUST NOT
encode the installed version, so an in-place upgrade cannot leave a stale name.

#### Scenario: Symlinked layout on Unix

- **WHEN** the shell installer completes on linux or darwin
- **THEN** `~/.qrypt/bin/qrypt` is the real executable and `~/.local/bin/qrypt` is a symlink that resolves to it

#### Scenario: Direct layout on Windows

- **WHEN** the PowerShell installer completes on windows
- **THEN** `%USERPROFILE%\.qrypt\bin\qrypt.exe` is the installed executable and is the command's entry point

#### Scenario: The version is not part of the path

- **WHEN** the installer has installed any version
- **THEN** the binary's path does not change between versions

### Requirement: Only the entry-point directory is placed on PATH

The installer MUST place exactly one directory on `PATH`: on linux and darwin the symlink's directory
`~/.local/bin` (never the private `~/.qrypt/bin`), and on windows its install directory
`%USERPROFILE%\.qrypt\bin`. It MUST NOT add any other directory.

#### Scenario: Private directory stays off PATH on Unix

- **WHEN** the shell installer configures `PATH`
- **THEN** it adds `~/.local/bin` and never `~/.qrypt/bin`

#### Scenario: Windows install directory is the PATH entry

- **WHEN** the PowerShell installer configures `PATH`
- **THEN** it adds `%USERPROFILE%\.qrypt\bin` and no other directory

### Requirement: Downloads are integrity-verified

Each installer MUST verify the downloaded archive against the release's published sha256 checksum. A
mismatched checksum, a missing checksum entry, or a failed download MUST abort the install without
leaving a partial or unverified binary in place.

#### Scenario: Checksum mismatch aborts

- **WHEN** the downloaded archive does not match the published checksum
- **THEN** the installer fails and no binary is installed

#### Scenario: A failed download leaves the prior install intact

- **WHEN** the install fails during download or verification and a prior install exists
- **THEN** the prior binary remains installed and usable

### Requirement: Existing installs and stale copies are handled

When the entry-point path already exists as a file the installer did not create, the installer MUST
NOT overwrite it without explicit consent. After installing, the installer MUST check whether the
`qrypt` resolved from `PATH` is the one it just installed, and MUST warn when a different copy would
run.

#### Scenario: Pre-existing file is not clobbered

- **WHEN** the entry-point path already exists as a non-managed file and consent is not given
- **THEN** the installer refuses and leaves that file unchanged

#### Scenario: A shadowing copy is reported

- **WHEN** the `qrypt` found on `PATH` resolves somewhere other than the managed install
- **THEN** the installer reports which copy will run and where the managed copy is

### Requirement: PATH configuration is idempotent and reversible

When the entry-point directory is not already on `PATH`, the installer MUST add it in the platform's
standard way, creating no duplicate on repeated runs. On linux and darwin it MUST write exactly one
marker-delimited block into a login-shell startup file (for example `~/.zprofile`,
`~/.bash_profile`, `~/.profile`, or the fish config). On windows it MUST add exactly one user-scope
(not machine-scope) `PATH` entry, so no administrator rights are needed. When the change cannot be
applied — no writable startup file, an unrecognized shell, or insufficient permission — the installer
MUST print what to add instead of editing anything. The installer MUST tell the user how to activate
the change in the current session.

#### Scenario: Directory already on PATH

- **WHEN** the entry-point directory is already on `PATH` before the install
- **THEN** the installer makes no change to the user's `PATH` configuration

#### Scenario: Block is added once on Unix

- **WHEN** the shell installer runs repeatedly and the directory is not on `PATH`
- **THEN** at most one marker-delimited block is present afterwards

#### Scenario: Registry entry is added once on Windows

- **WHEN** the PowerShell installer runs repeatedly and the directory is not on `PATH`
- **THEN** exactly one user-scope `PATH` entry naming that directory is present afterwards

#### Scenario: Configuration cannot be applied

- **WHEN** the installer cannot write a startup file or a `PATH` entry
- **THEN** it prints the change to make and edits nothing

### Requirement: Uninstall reverses an install

`qrypt uninstall` MUST remove the managed entry point, the managed binary, and the `PATH`
configuration the installer added. On linux and darwin that is the symlink, the real binary, and the
marker-delimited block; on windows it is the binary and the user-scope `PATH` entry. It MUST remove a
symlink only when it points at the managed binary, and MUST NOT touch a `PATH` entry the user already
had. It MUST be idempotent — a second run with nothing to remove MUST succeed. It MUST confirm before
making changes unless told to proceed non-interactively.

#### Scenario: Managed Unix install is removed

- **WHEN** a user runs `qrypt uninstall` on a managed linux or darwin install and confirms
- **THEN** the symlink, the binary, and the installer's `PATH` block are removed

#### Scenario: Managed Windows install is removed

- **WHEN** a user runs `qrypt uninstall` on a managed windows install and confirms
- **THEN** the binary and the installer's user-scope `PATH` entry are removed

#### Scenario: The user's own PATH lines are preserved

- **WHEN** the directory was already on `PATH` before the install (so the installer added nothing)
- **THEN** uninstall removes no `PATH` entry or line

#### Scenario: An unrelated symlink is preserved

- **WHEN** the entry-point path is a symlink that does not point at the managed binary
- **THEN** uninstall leaves it in place

#### Scenario: Uninstall is idempotent

- **WHEN** uninstall runs again after a successful uninstall that left no deferred remnant
- **THEN** it reports nothing to remove and succeeds

### Requirement: Uninstall clears remnants of earlier installs

Uninstall MUST remove the displaced binaries a prior install or upgrade left beside the managed
binary (for example `<binary>.old` and `<binary>.old.<pid>`). When a remnant cannot be removed
because it is the executable currently running, uninstall MUST report it as still present instead of
reporting a clean removal, and MUST let a later run or the operating system remove it.

#### Scenario: A leftover from an upgrade is removed

- **WHEN** an upgrade has left a displaced `<binary>.old` file and the user uninstalls
- **THEN** uninstall removes that file together with the managed binary and, once the install directory is empty, removes the directory itself

#### Scenario: The running image is reported

- **WHEN** the file uninstall must remove is the executable that is running
- **THEN** uninstall reports that this copy remains on disk and will be removed later, rather than reporting that everything was removed

#### Scenario: A deferred remnant is cleared by a later run

- **WHEN** a later uninstall runs after the process that held a deferred remnant has exited
- **THEN** uninstall removes the remnant and succeeds

### Requirement: Uninstall preserves user data

Uninstall MUST leave the user's qrypt data — configuration, cache, logs, and session state — in
place unless the user explicitly requests its removal.

#### Scenario: Data survives a normal uninstall

- **WHEN** a user uninstalls without requesting data removal
- **THEN** the configuration and other data under the qrypt data directory remain

#### Scenario: Explicit data removal

- **WHEN** a user uninstalls requesting data removal
- **THEN** the data directory is removed as well, and the command states that it is doing so

### Requirement: Release naming is shared with the updater

Every installer MUST select releases and assets by the same naming rule the updater uses
(`qrypt_<version>_<os>_<arch>.<ext>`, `qrypt_<version>_checksums.txt`, and the in-archive binary
name). A check MUST fail when any installer and the updater disagree about that rule, so they cannot
drift silently.

#### Scenario: Drift is detected

- **WHEN** an installer's asset-naming rule and the updater's diverge
- **THEN** the check fails and names the disagreement

### Requirement: Supported platforms

First install MUST support linux, darwin, and windows, each through its platform-appropriate
bootstrap. The installer MUST NOT claim to support a platform for which no release asset exists.

#### Scenario: Unsupported platform

- **WHEN** the installer runs on a platform with no matching release asset
- **THEN** it fails with a clear message rather than installing an incompatible binary
