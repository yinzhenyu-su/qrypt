## ADDED Requirements

### Requirement: 115 cloud drives support provider-side offline download

The system MUST support provider-side offline download on both the `115` cookie driver and the
`115_open` access-token driver. Both drivers MUST declare the `offline_download` capability and
MUST accept the provider-neutral source kinds `magnet`, `http`, and `ftp`. A task submitted to a
115 mount MUST stage the source in the selected parse destination on the 115 account without
transferring the source bytes through qrypt.

#### Scenario: Magnet source is staged on 115

- **WHEN** a user runs `offline add 'magnet:?xt=urn:btih:...' --parse-to /115/downloads`
- **THEN** qrypt submits the magnet to 115, stores the provider task identity in task detail, and reports a queued or subsequent provider state

#### Scenario: HTTP source is staged on 115

- **WHEN** a user runs `offline add 'https://example.com/file.iso' --parse-to /115/downloads`
- **THEN** qrypt submits the URL to 115 and observes the resulting provider task

#### Scenario: FTP source is accepted by 115

- **WHEN** a user submits an FTP URL to a 115 mount
- **THEN** qrypt accepts the source kind instead of rejecting it as unsupported before submission

#### Scenario: The same capability is available through the open platform driver

- **WHEN** the parse destination mount uses the `115_open` driver
- **THEN** offline add, observation, cancellation, and fetch use the same provider-neutral behavior as the cookie driver

### Requirement: 115 offline tasks report provider-neutral state, progress, and result files

The system MUST map 115 task states into the canonical offline states: `0` to `queued`, `1` to
`running`, `2` to `succeeded`, and `-1` to `failed`. A succeeded task MUST report its staged files
with a provider-neutral relative `Path` that includes the resource folder 115 created, and MUST
report the staged result set as available only after the result tree can be resolved. A task that
is no longer present in the provider list MUST be reconciled against the parse destination, and
MUST NOT be reported as succeeded unless the expected files are present.

#### Scenario: Multi-file result mirrors the resource folder

- **WHEN** a completed 115 task produced a resource folder containing several files
- **THEN** each reported file has a path such as `<resource>/episode.mkv`, and a fetch recreates that folder at the final destination

#### Scenario: Single-file result has a flat path

- **WHEN** a completed 115 task produced a single file directly in the parse destination
- **THEN** the reported file path is the file name and a fetch writes it as a single file

#### Scenario: Failed provider task is reported as failed

- **WHEN** 115 reports status `-1` for a task
- **THEN** qrypt reports the task as failed with a provider-neutral offline error and does not transfer any partial result

#### Scenario: Removed provider task is reconciled from the destination

- **WHEN** a 115 task disappears from the provider list after its files were staged
- **THEN** qrypt verifies the expected files in the parse destination and reports success only when they are present

### Requirement: 115 fetch supports explicit and default parse destinations

An offline fetch from a 115 mount MUST transfer the staged result to the final destination. When
`--parse-to` is given, the transfer MAY read the staged files through the VFS. When only `--mount`
is given, the transfer MUST use the provider download URL for each staged file. A 115 fetch MUST
NOT require an accelerated session and MUST NOT wait for a provider acceleration capability.

#### Scenario: Fetch with an explicit parse destination

- **WHEN** a user runs `offline fetch SOURCE --parse-to /115/downloads --download-to ./out`
- **THEN** qrypt transfers the staged 115 files to `./out`, recreating their staged paths

#### Scenario: Fetch with only a mount selector

- **WHEN** a user runs `offline fetch SOURCE --mount 115 --download-to ./out`
- **THEN** qrypt resolves each staged file's download URL from 115 and transfers it without requiring `--parse-to`

#### Scenario: No accelerated session is requested

- **WHEN** the driver reports no accelerated-fetch capability
- **THEN** qrypt does not send an accelerated request and never fails a fetch solely because no accelerated session exists

### Requirement: Unsupported 115 offline refinements fail before creating a provider task

115 does not expose inspect-without-submit, file selection through URL submission, or an
accelerated download session. The system MUST report inspect as unsupported, MUST reject any
selection other than `all` before creating a provider task, and MUST NOT advertise accelerated
fetch for either 115 driver.

#### Scenario: Inspect is rejected

- **WHEN** a user runs `offline inspect SOURCE` against a 115 mount
- **THEN** qrypt returns a typed unsupported-inspection error and creates no 115 task

#### Scenario: Non-all selection is rejected

- **WHEN** a user runs `offline add SOURCE --parse-to /115/downloads --select largest:1`
- **THEN** qrypt rejects the request before submission because 115 cannot select files through its URL add API

#### Scenario: Accelerated fetch is not advertised

- **WHEN** qrypt inspects the capabilities of either 115 driver
- **THEN** the accelerated-fetch capability is absent

### Requirement: 115 retry and duplicate behavior is explicit

A failed 115 task MUST remain observable as failed until it is removed or replaced by the
provider. Because 115 rejects a source whose task already exists, retrying a failed 115 task by
re-creating it MAY return an `offline_duplicate` error. The system MUST NOT silently reuse an
existing provider task with the same source, and the user-facing documentation MUST explain how to
remove the failed task before retrying.

#### Scenario: Retry conflicts with a retained failed task

- **WHEN** a user retries a failed 115 task while 115 still lists its source
- **THEN** the retry fails with a provider-neutral duplicate error instead of reporting success or creating a second task

#### Scenario: Cancel removes the provider task

- **WHEN** a user cancels a 115 offline task
- **THEN** qrypt asks 115 to delete the task and keeps the already staged cloud files

#### Scenario: A canceled task can be submitted again

- **WHEN** the provider task has been deleted and the source no longer exists in 115's task list
- **THEN** a new submission of the same source creates a new provider task

### Requirement: 115 staged files are cleaned up after a successful fetch

After a successful fetch from a 115 mount, cleanup MUST remove only the staged files this task
created and MUST remove the resource folder 115 created when that folder contains nothing but
entries this cleanup removed. The parse destination MUST never be removed. A retention request
MUST keep both the files and the resource folder.

#### Scenario: Resource folder is removed after fetch

- **WHEN** a fetch completes for a multi-file 115 source and every file under the resource folder was staged by this task
- **THEN** qrypt removes the files and the now-empty resource folder

#### Scenario: Unrelated entry keeps the folder

- **WHEN** the 115 resource folder contains an entry this fetch did not stage
- **THEN** qrypt leaves the folder and the unrelated entry in place

#### Scenario: Keep-cloud retains the staged result

- **WHEN** a user runs `offline fetch --keep-cloud`
- **THEN** qrypt leaves the staged 115 files and resource folder in place
