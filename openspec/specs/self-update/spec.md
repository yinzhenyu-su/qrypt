# self-update Specification

## Purpose

Let a user update the installed qrypt executable to a newer GitHub release with one command, without
manually finding, downloading, and unpacking the right archive, and without risking a running binary
being left replaced by a corrupt or unverified file.

## Requirements

### Requirement: Upgrade replaces the running executable

`qrypt upgrade` MUST fetch the newest applicable qrypt release for the running platform, verify it,
and replace the executable that is currently running. When the newest applicable release is not
newer than the current version, the command MUST report that the installation is up to date and
MUST NOT modify the executable.

#### Scenario: Newer release is installed

- **WHEN** a user runs `qrypt upgrade` and a newer stable release exists for the current OS and architecture
- **THEN** the command downloads and verifies that release and the executable at the running binary's resolved path is replaced with it
- **AND** the command reports the previous and new versions

#### Scenario: Already up to date

- **WHEN** a user runs `qrypt upgrade` and the current version is the newest applicable release
- **THEN** the command reports that it is up to date, exits successfully, and does not modify or re-download the executable

#### Scenario: Check only

- **WHEN** a user runs `qrypt upgrade --check`
- **THEN** the command reports the current version and the newest available version
- **AND** it performs no download and no write to the executable

### Requirement: Only release builds are upgraded by default

The command MUST refuse to upgrade when the current build is not a release version (for example a
development or locally modified build), because no released version can be compared against it. The
refusal MUST name the reason and exit non-zero. `--force` MUST override this refusal.

#### Scenario: Development build is refused

- **WHEN** the current build is not a clean release version and the user runs `qrypt upgrade` without `--force`
- **THEN** the command refuses, explains that the build is not a release version, and exits non-zero without modifying the executable

#### Scenario: Force overrides the release-build guard

- **WHEN** the current build is not a clean release version and the user runs `qrypt upgrade --force`
- **THEN** the command proceeds to upgrade to the newest applicable release

### Requirement: Release and asset selection

The command MUST select the newest stable (non-prerelease, non-draft) release by default. `--to
<version>` MUST select the named release instead, and MUST allow installing a version older than the
current one. A named version that does not exist as a release MUST fail. The command MUST select the
release asset matching the running operating system and architecture, and MUST fail if no matching
asset exists.

#### Scenario: Latest stable release is selected

- **WHEN** the user runs `qrypt upgrade` and the release list mixes stable and prerelease entries
- **THEN** the newest stable release is selected

#### Scenario: A specific version is requested

- **WHEN** the user runs `qrypt upgrade --to <version>` and that release exists
- **THEN** that release is selected even when it is older than the current version

#### Scenario: A requested version does not exist

- **WHEN** the user runs `qrypt upgrade --to <version>` and no such release exists
- **THEN** the command fails with an error naming the requested version and does not modify the executable

#### Scenario: No asset for the current platform

- **WHEN** the selected release has no asset matching the running OS and architecture
- **THEN** the command fails with an error naming the platform and does not modify the executable

### Requirement: Downloads are integrity-verified

The command MUST verify the downloaded archive against the sha256 checksum published for it in the
release. A mismatched checksum, a missing checksum entry, or a download that fails MUST abort the
upgrade and MUST leave the current executable unchanged.

#### Scenario: Checksum matches

- **WHEN** the downloaded archive's sha256 matches the published checksum
- **THEN** the command proceeds to install the archive's binary

#### Scenario: Checksum mismatch aborts

- **WHEN** the downloaded archive's sha256 does not match the published checksum
- **THEN** the command aborts with an integrity error and the current executable is unchanged

#### Scenario: Checksum entry is missing

- **WHEN** the release publishes no checksum entry for the selected asset
- **THEN** the command aborts with an error and the current executable is unchanged

### Requirement: Replacement is atomic and preserves the installation layout

The replacement MUST publish the new binary atomically, so a reader or a crash never observes a
partially written executable. On Unix the new binary MUST replace the resolved real file, and a
symlink that points at the executable MUST remain pointing at the replacement target. A failed
download, verification, or extraction MUST leave the current executable unchanged.

#### Scenario: Atomic replacement

- **WHEN** the command installs a verified binary
- **THEN** the executable path transitions from the old binary to the new binary in a single step, and no partially written file is ever visible at that path

#### Scenario: Symlinked install is preserved

- **WHEN** the running executable is reached through a symlink
- **THEN** the symlink still points at the same target after the upgrade and resolves to the new binary

### Requirement: Unwritable targets are refused with a next step

When the process cannot write the resolved executable path, the command MUST NOT attempt to escalate
privileges. It MUST fail with a message that names the path and gives a copy-pastable command that
performs the upgrade with the required privilege.

#### Scenario: Not writable

- **WHEN** the resolved executable path is not writable by the current user
- **THEN** the command fails, names that path, and prints a runnable command that repeats the upgrade with the required privilege
- **AND** the command does not prompt for or acquire elevated privileges itself

### Requirement: Windows replaces a running executable

On Windows, where a running executable cannot be overwritten in place, the command MUST move the
running executable aside, place the new binary at the executable path, and arrange for the displaced
file to be removed later. Repeating an upgrade MUST NOT fail because a previously displaced file is
still present.

#### Scenario: Running executable is replaced

- **WHEN** a user runs `qrypt upgrade` on Windows and the process is writable
- **THEN** the running executable is moved aside, the new binary is placed at the executable path, and the upgrade reports success

#### Scenario: Leftover displaced file does not block a later upgrade

- **WHEN** a displaced file from an earlier upgrade still exists and the user runs `qrypt upgrade` again
- **THEN** the command still succeeds and does not fail on the leftover file

### Requirement: Command output and exit codes

The command MUST print progress and result text that names each step (lookup, download, verify,
install) and the versions involved. `--json` MUST emit a machine-readable result on standard output
and MUST NOT interleave progress text with it. Success and "already up to date" MUST exit 0; usage
errors (such as an unparsable version) MUST exit 2; runtime failures (network, integrity, permission)
MUST exit 1.

#### Scenario: JSON output

- **WHEN** the user runs `qrypt upgrade --json`
- **THEN** exactly one JSON object is written to standard output containing at least the current version, the target version, and the outcome

#### Scenario: Exit codes

- **WHEN** the command succeeds or reports "already up to date"
- **THEN** it exits 0
- **WHEN** it fails on a network, integrity, or permission error
- **THEN** it exits 1
- **WHEN** it is given an invalid argument such as an unparsable version
- **THEN** it exits 2
