# Spec Delta

## MODIFIED Requirements

### Requirement: One-command first install

A bootstrap script MUST install qrypt from a single invocation on each supported platform: a shell
one-liner on linux and darwin, and a PowerShell one-liner on windows. It MUST detect the running
operating system and architecture, download the applicable release for that platform, and install it.
When a specific version is requested, it MUST install that release instead of the newest one.
Re-running the installer MUST install the newest applicable release: it MUST replace the existing
binary with that release rather than leaving the installed version in place, and MUST do so without
duplicating its effects or corrupting the existing install.

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

#### Scenario: Re-running installs a newer release

- **WHEN** a newer stable release has been published and the user runs the bootstrap again on a machine that already has qrypt installed
- **THEN** the newer release is downloaded, verified, and installed, and the `qrypt` command reports the newer version

#### Scenario: The newest release is resolved without the rate-limited API

- **WHEN** the unauthenticated GitHub API rate limit is exhausted and the bootstrap is run without a token
- **THEN** the newest release is still resolved from the release's `latest` location and installed

### Requirement: PATH configuration is idempotent and reversible

When the entry-point directory is not already on `PATH`, the installer MUST add it in the platform's
standard way, creating no duplicate on repeated runs. On linux and darwin it MUST write exactly one
marker-delimited block into a login-shell startup file (for example `~/.zprofile`,
`~/.bash_profile`, `~/.profile`, or the fish config). On windows it MUST add exactly one user-scope
(not machine-scope) `PATH` entry, so no administrator rights are needed, MUST preserve that value's
registry type, and MUST NOT expand or otherwise rewrite the entries it did not add. When the change
cannot be applied — no writable startup file, an unrecognized shell, or insufficient permission — the
installer MUST print what to add instead of editing anything. The installer MUST tell the user how to
activate the change in the current session.

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

#### Scenario: The user's existing PATH value is preserved

- **WHEN** the user's `PATH` contains an unexpanded reference such as `%USERPROFILE%\bin` and the installer adds its own directory
- **THEN** the existing entries are written back unchanged and the value's registry type is preserved

#### Scenario: Running processes are notified

- **WHEN** the PowerShell installer changes the user `PATH`
- **THEN** it notifies the system that the environment changed, so newly started processes see the new value

## ADDED Requirements

### Requirement: Reinstalling while the binary is in use

The installer MUST be able to replace the managed binary while that binary is running. On windows,
where a running executable cannot be overwritten in place, it MUST move the running image aside
instead of failing on the locked file, and MAY leave that displaced file for `qrypt uninstall` to
sweep. On linux and darwin, unlinking the running image MUST be used so the replacement needs no
special handling.

#### Scenario: Windows reinstall over a running binary

- **WHEN** `qrypt.exe` is running and the user runs the PowerShell bootstrap again on windows
- **THEN** the newest release is installed at the managed path and the command succeeds, rather than failing on the locked file

#### Scenario: Unix reinstall over a running binary

- **WHEN** a qrypt process is running and the user runs the shell bootstrap again on linux or darwin
- **THEN** the newest release replaces the binary and the install remains valid

### Requirement: Bootstrap commands are copy-pasteable and release-matched

The published instructions MUST give one command per platform that a user can paste into a shell as
it is. The windows command MUST be the `powershell -c "irm <url>|iex"` form, so it runs from any
shell and not only from an already-open PowerShell session. The bootstrap script MUST be served from
the release being installed rather than from a moving branch, so the script and the binaries it
installs cannot drift apart.

#### Scenario: Bootstrap from a non-PowerShell shell

- **WHEN** a user pastes the documented windows command into `cmd.exe` or a POSIX shell on windows
- **THEN** PowerShell runs the bootstrap and qrypt is installed

#### Scenario: The bootstrap matches the release

- **WHEN** a release is published
- **THEN** its bootstrap scripts are downloadable from that release's stable location, and the documented command installs that release's binaries
