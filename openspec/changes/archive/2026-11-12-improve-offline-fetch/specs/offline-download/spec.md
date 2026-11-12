## RENAMED Requirements

- FROM: `### Requirement: Offline fetch cleans up its cloud copy and blocks by default`
- TO: `### Requirement: Offline fetch cleans up its cloud copy and reports by default`

## MODIFIED Requirements

### Requirement: Offline fetch cleans up its cloud copy and reports by default

After a successful transfer, an offline fetch MUST remove the cloud copy it created: the staged files it
created, and the resource folder the provider created for them when that folder holds nothing but
entries this cleanup removed. It MUST NOT remove the parse destination itself, and it MUST leave a
folder that still holds an unrelated entry. A retention request MUST keep both the files and the
folder. By default an offline fetch MUST return once the
provider task first becomes observable, printing the task snapshot and how to observe it later, and
MUST NOT hold the terminal until the transfer finishes; when that first observation is already failed
or canceled the command MUST exit non-zero. A `--wait` form MUST block until the final destination is
written and MUST render progress while it blocks. Interrupting either form MUST detach the wait without
canceling the Core task.

#### Scenario: Cloud copy is removed on success

- **WHEN** an offline fetch finishes transferring to its final destination
- **THEN** qrypt removes the staged cloud files it created and leaves other entries in the directory untouched

#### Scenario: Auto-created resource folder is removed

- **WHEN** a multi-file source was staged under a resource folder the provider created, and the fetch finishes
- **THEN** qrypt removes that folder once every file under it was one this cleanup removed

#### Scenario: A folder with an unrelated entry is kept

- **WHEN** the resource folder still holds an entry this fetch did not create
- **THEN** qrypt leaves the folder and that entry in place

#### Scenario: The parse destination is never removed

- **WHEN** the staged files sit directly in the parse destination with no resource folder of their own
- **THEN** qrypt removes the files but never the parse destination

#### Scenario: Cloud copy is kept on request

- **WHEN** the request asks to keep the cloud copy
- **THEN** qrypt leaves the staged files and their resource folder in place and reports their location

#### Scenario: Default returns after the first observation

- **WHEN** a user runs `offline fetch SOURCE --download-to PATH` without `--wait`
- **THEN** the command prints the task snapshot and a pointer to observe it, and returns while the Core task keeps transferring

#### Scenario: Blocking form renders progress

- **WHEN** a user runs `offline fetch SOURCE --download-to PATH --wait`
- **THEN** the command reports staging and transfer progress until the final destination is written and exits according to the terminal outcome

#### Scenario: Interrupt detaches without canceling

- **WHEN** the user interrupts a blocking offline fetch
- **THEN** the Core task keeps running and the user can observe it later

### Requirement: Offline failures use stable error codes and command outcomes

The system MUST report offline failures with stable, provider-neutral error codes and an explicit
retryable flag. Quota, policy, unsupported-source, and invalid-destination-path failures MUST NOT be
reported as retryable. Command outcomes MUST be distinguishable by exit code: success, runtime or task
failure (including cancellation), and usage error.

#### Scenario: Quota exhaustion is not retryable

- **WHEN** the provider rejects a source because the account's download quota is exhausted
- **THEN** qrypt records a non-retryable quota error and reports an actionable message

#### Scenario: Invalid staged path is not retryable

- **WHEN** a staged result path cannot be validated for the final destination
- **THEN** qrypt records the non-retryable `offline_path_invalid` error, writes no file, and reports which path was refused

#### Scenario: Canceled task is distinguishable from success

- **WHEN** a waited task ends as canceled
- **THEN** the command exits with the same non-zero code as a failure, and JSON output reports the canceled state

#### Scenario: Usage errors do not create tasks

- **WHEN** a request has an invalid source, conflicting destination selectors, or identical parse and download destinations
- **THEN** qrypt reports a usage error and creates no task

## ADDED Requirements

### Requirement: Offline fetch mirrors the staged provider layout

When an offline fetch resolves the files to transfer, it MUST write each file at the final destination
joined with that file's staged relative path, recreating the provider's directory structure (including
the resource's own folder) instead of flattening every file into one directory. A provider that can
locate a staged file MUST report the file's path relative to the staging destination. When a provider
reports no relative path, the file MUST be written by its own name. When the final destination names a
single file rather than a directory, the fetch MUST accept the request only if exactly one file is
selected and that file has no directory component; otherwise the task MUST fail with
`offline_path_invalid` without writing any file. An existing directory at a resolved path MUST be
reused without deleting its other contents.

#### Scenario: Multi-file torrent recreates its folder

- **WHEN** a user fetches a multi-file source whose staged files live under one resource folder
- **THEN** qrypt writes them under `<download-to>/<resource folder>/` instead of flat in `<download-to>`

#### Scenario: Nested directories are recreated

- **WHEN** the staged result set contains files in nested directories
- **THEN** qrypt creates each directory level and writes every file at its staged relative path

#### Scenario: Provider without a path falls back to the file name

- **WHEN** a provider reports result files without a staged relative path
- **THEN** qrypt writes each file by its own name, as before

#### Scenario: Single-file destination rejects a directory layout

- **WHEN** `--download-to` names a single file path but the result set has more than one file or a directory component
- **THEN** qrypt fails the task with `offline_path_invalid` and writes no file

#### Scenario: Flat opt-out writes by name

- **WHEN** a user requests a flat layout
- **THEN** qrypt writes every selected file by its own name even when the provider reports a staged relative path

### Requirement: Staged result paths are validated before writing

Every path component the fetch derives from provider data MUST be validated before any byte is written:
absolute paths, `.` and `..` components, empty components, NUL and control characters, and
Windows-reserved names or trailing dots and spaces MUST be rejected. A rejected path MUST fail the task
with the `offline_path_invalid` code and MUST NOT write any file. The fetch MUST NOT write through an
existing symlink component that would leave the destination root. Two result files that resolve to the
same destination MUST fail the task with `offline_path_invalid` instead of overwriting one another.

#### Scenario: Traversing or absolute path fails the task

- **WHEN** a staged result path is absolute or contains a `..` component
- **THEN** qrypt fails the task with `offline_path_invalid`, writes no file, and names the refused path

#### Scenario: Symlinked ancestor is refused

- **WHEN** a destination path would resolve through an existing symbolic link that leaves the destination root
- **THEN** qrypt refuses that path and fails the task with `offline_path_invalid`

#### Scenario: Duplicate destinations fail instead of overwriting

- **WHEN** two result files resolve to the same destination path
- **THEN** qrypt fails the task with `offline_path_invalid` instead of silently truncating one of them

### Requirement: Providers report when staged results are available

A provider that can expose the staged result set before its task reaches a terminal state MUST report a
provider-neutral availability signal. An offline fetch MUST NOT start transferring until it has either
that signal or a terminal provider observation, so it never transfers a partial or reordered result set.
When a provider never reports availability, the fetch MUST wait for a terminal observation before
transferring.

#### Scenario: Available results transfer before the task terminalizes

- **WHEN** a provider reports that the staged results are available while the task is still running
- **THEN** the fetch resolves the layout and starts transferring without waiting for the terminal state

#### Scenario: Unavailable results wait for the terminal observation

- **WHEN** a provider reports no availability signal
- **THEN** the fetch waits for a terminal provider observation before transferring

#### Scenario: Provider without the signal still fetches

- **WHEN** a provider never reports availability
- **THEN** a fetch still completes through the normal terminal observation instead of failing
