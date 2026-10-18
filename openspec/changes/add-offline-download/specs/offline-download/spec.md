## Purpose

Provide a durable, mount-aware way to submit remote sources to a supported cloud drive for
provider-side downloading, and to inspect, observe, control, fetch, and wait for those tasks
without transferring the source bytes through the qrypt process except when the user explicitly
asks for the result at a local or encrypted destination.

## ADDED Requirements

### Requirement: Offline commands are provider-neutral and owned by a running Core

The system MUST expose offline download through a provider-neutral command surface. The parse
destination mount MUST be the only selector of the provider; no command may require a provider
name, driver name, socket path, or provider task identifier as input. Offline commands MUST
operate against a running Core and MUST fail with a clear runtime error when none is running,
without creating or mutating any task.

#### Scenario: Provider is selected by the parse destination

- **WHEN** a user submits an offline task with `--parse-to /quark-test/downloads`
- **THEN** qrypt uses the `quark-test` mount's provider without any provider-specific flag

#### Scenario: No running Core

- **WHEN** a user runs an offline command while no Core is running for the selected configuration
- **THEN** the command fails with a runtime error naming the missing Core and creates no task

#### Scenario: Provider identifiers are not required input

- **WHEN** a user inspects or controls an offline task
- **THEN** the only task identity required is the qrypt task id, and any provider task id remains in diagnostic detail

### Requirement: Offline download selects exactly one parse destination mount

The system MUST resolve an offline parse destination to exactly one configured mount and a remote
path within that mount. The canonical form MUST accept a mount-qualified virtual path such as
`/quark-test/downloads`; an explicit `--mount NAME --parse-to /remote/path` form MAY be provided,
but conflicting mount selections MUST be rejected.

#### Scenario: Mount-qualified parse destination is accepted

- **WHEN** a user submits offline work with `--parse-to /quark-test/downloads`
- **THEN** qrypt selects the `quark-test` mount and treats `/downloads` as the remote parse destination path

#### Scenario: Cross-mount parse destination is rejected

- **WHEN** a selection names one mount and the parse path names another
- **THEN** qrypt rejects the request instead of asking a provider to interpret a foreign mount path

#### Scenario: Unknown mount is reported before provider submission

- **WHEN** the first component of a mount-qualified parse destination does not name a configured mount
- **THEN** qrypt returns a structured usage or not-found error and creates no provider task

### Requirement: The parse destination defaults or must be usable

The parse destination MUST be optional and MUST default to the provider's magnet/system directory
when omitted. When supplied, it MUST be an existing directory inside the selected mount, or the
mount root, and a missing directory MUST fail with an actionable error before any provider task is
created unless the request explicitly asks qrypt to create it. The mount root MUST be accepted.

#### Scenario: Omitted parse destination uses the provider default

- **WHEN** a user submits offline work without a parse destination
- **THEN** qrypt stages the source in the provider's magnet/system directory and proceeds

#### Scenario: Missing parse directory fails by default

- **WHEN** a supplied parse destination does not exist and creation was not requested
- **THEN** qrypt reports the missing directory with a hint to create it or request creation, and creates no task

#### Scenario: Creation is honored when requested

- **WHEN** a supplied parse destination does not exist and the request asks qrypt to create it
- **THEN** qrypt creates the directory and proceeds with the offline task

### Requirement: Encrypted parse destinations are rejected while fetch destinations may be encrypted

The system MUST reject an offline parse destination whose mount applies qrypt encryption, because
provider-side bytes never pass through qrypt's encryption, and MUST create no provider task. A final
destination for an offline fetch MUST be allowed to be an encrypted mount, because those bytes do
pass through qrypt.

#### Scenario: Encrypted parse destination is rejected

- **WHEN** a user submits offline work with a parse destination on a mount configured with content or filename encryption
- **THEN** qrypt rejects the request with an encryption-specific error and creates no task

#### Scenario: Encrypted final destination is accepted

- **WHEN** a user fetches an offline source into a destination on an encrypted mount
- **THEN** qrypt proceeds and the written bytes are encrypted by the destination mount

### Requirement: Sources are normalized to a provider-neutral form

The system MUST accept magnet links, HTTP(S) URLs, and local `.torrent` files, and MAY accept
additional schemes a provider declares. A local `.torrent` MUST be normalized into a magnet link
derived from its info hash; the torrent file itself MUST NOT be uploaded. Share links and unsupported
schemes MUST be rejected before any provider task is created.

#### Scenario: Local torrent becomes a magnet

- **WHEN** a user submits a local `.torrent` file as the source
- **THEN** qrypt derives a magnet link from the torrent's info hash and submits that as the source without uploading the file

#### Scenario: Torrent and magnet are equivalent sources

- **WHEN** the same torrent content is submitted once as a local `.torrent` file and once as its equivalent magnet link
- **THEN** both normalize to the same source identity for de-duplication and provider submission

#### Scenario: Unsupported source is rejected

- **WHEN** a user submits a share link or a scheme no provider accepts
- **THEN** qrypt rejects the source with a typed error and creates no task

### Requirement: Providers declare their offline capabilities

The system MUST allow a provider to declare offline download, offline inspection, offline file
selection, and accelerated fetch independently. A request for a surface the provider does not declare
MUST fail before any provider task is created.

#### Scenario: Unsupported provider is rejected before submission

- **WHEN** a user submits offline work to a mount whose provider does not support offline download
- **THEN** qrypt reports offline download as unsupported for that mount and creates no task

#### Scenario: Selection without provider support is rejected

- **WHEN** a user requests a file subset from a provider that does not support offline selection
- **THEN** qrypt rejects the selection with a typed error, while `--select all` remains valid

#### Scenario: Accelerated fetch is optional

- **WHEN** a provider does not declare accelerated fetch
- **THEN** a fetch still works through the provider's normal download URL instead of failing

### Requirement: Offline submission returns a durable qrypt task

The system MUST represent accepted offline work as a persistent user task under the `download`
operation: `offline_download` for cloud-side work and `offline_fetch` when a final destination is
given. The task MUST retain the selected mount, parse destination, normalized source, selection,
provider task identifier, provider observations, final destination when present, and idempotency
metadata needed for observation and recovery.

#### Scenario: Cloud task is created

- **WHEN** the provider accepts an offline source
- **THEN** qrypt returns a task snapshot with a stable task id, the selected mount, the parse destination, and an observable state

#### Scenario: First observation determines the reported outcome

- **WHEN** the first observable provider state for a submission is already failed or canceled
- **THEN** the submission command reports a non-zero outcome rather than reporting success

#### Scenario: Provider rejects submission

- **WHEN** the provider rejects the source or parse destination
- **THEN** qrypt returns the provider error and persists no runnable task without a provider task identifier

### Requirement: Active submissions are de-duplicated

The system MUST return the existing non-terminal qrypt task when a submission matches an active
offline task with the same normalized source, parse destination, and selection, unless the request
explicitly asks for a new task. The final destination of a fetch MUST NOT participate in this match.
An explicit idempotency key MUST use the existing idempotency contract. A provider-reported duplicate
MUST be surfaced as a typed duplicate error rather than a silent success.

#### Scenario: Repeated submission reuses the active task

- **WHEN** a user submits a source that an active offline task already stages in the same parse destination with the same selection
- **THEN** qrypt returns the existing task instead of creating a provider task again

#### Scenario: Different final destinations share one staging task

- **WHEN** the same source is fetched twice into different final destinations while staging is still active
- **THEN** both fetches reuse the existing staging work rather than creating a second provider task

#### Scenario: Completed work can be requested again

- **WHEN** the previous matching task is in a terminal state
- **THEN** qrypt creates a new task rather than returning the terminal task

#### Scenario: Provider duplicate is explicit

- **WHEN** the provider reports that the source already has a task
- **THEN** qrypt returns a typed duplicate error rather than claiming success

### Requirement: Provider states map to the canonical task lifecycle

The system MUST map provider-owned queued, running, succeeded, failed, and canceled observations into
the canonical qrypt task state without rewriting a task that has already reached a terminal state.
Provider-specific status and identifiers MAY remain in diagnostic detail.

#### Scenario: Provider task succeeds

- **WHEN** observation reports a successful provider task
- **THEN** qrypt marks the task succeeded and records any resulting remote identity

#### Scenario: Provider task fails

- **WHEN** observation reports a provider failure
- **THEN** qrypt marks the task failed with a readable error and preserves the provider status in detail

#### Scenario: Terminal task is not resurrected

- **WHEN** a provider observation reporting progress or success arrives after the qrypt task became canceled, failed, or succeeded
- **THEN** the canonical state and error remain unchanged while the provider observation is recorded only as diagnostics

### Requirement: Cancellation deletes the provider task and retry prefers in-place retry

Cancellation MUST record the cancellation before asking the provider to remove its task, and a provider
task identifier MUST NOT be required for the user to request cancellation. Retry MUST be available for
failed or canceled tasks only; when the provider supports retrying a task in place, qrypt MUST use that
path with a new execution generation, and otherwise MUST create a new provider task from the persisted
source, parse destination, and selection.

#### Scenario: User cancels a running offline task

- **WHEN** the user cancels an offline task
- **THEN** qrypt records the cancellation first and then requests provider-side removal

#### Scenario: Cancel before a provider task exists

- **WHEN** the user cancels a task whose provider task has not been created yet
- **THEN** qrypt prevents the pending submission or removes the provider task once its identifier becomes known

#### Scenario: Retry reuses provider retry when available

- **WHEN** the user retries a failed task on a provider that supports in-place retry
- **THEN** qrypt re-queues the provider task and advances the qrypt execution generation without creating a second provider task

#### Scenario: Retry recreates when provider retry is unavailable

- **WHEN** the user retries a failed task on a provider without in-place retry
- **THEN** qrypt creates a new provider task from the persisted source and parse destination and keeps the previous provider identifier in detail

#### Scenario: Retry of a succeeded task is rejected

- **WHEN** the user retries an offline task that already succeeded
- **THEN** qrypt rejects the retry and reports that a new task is required instead

### Requirement: Completion is provider-authoritative and destination-verified

The system MUST treat the provider's success observation as authoritative for completion. When the
provider reports success but the parse destination entry cannot yet be resolved, the task MUST still be
recorded as succeeded with that observation marked as unverified. When the provider task no longer
exists, qrypt MUST fall back to resolving the destination entry, and MUST NOT report success without
either a provider success observation or a resolved destination entry.

#### Scenario: Success despite a lagging destination listing

- **WHEN** the provider reports success but the destination entry is not yet visible
- **THEN** qrypt records success and marks the destination as unverified

#### Scenario: Provider task disappears after completion

- **WHEN** the provider no longer exposes the task but the destination entry can be resolved
- **THEN** qrypt treats the offline task as successfully completed

#### Scenario: Provider task disappears without a destination entry

- **WHEN** the provider no longer exposes the task and the destination entry cannot be resolved after a bounded window
- **THEN** qrypt records a typed failure rather than an automatic success

### Requirement: Offline tasks survive restart

The system MUST persist enough offline task context to resume after a Core restart, and a resumed
runner MUST NOT conflict with any previous runner for the same task.

#### Scenario: Core restarts while a provider task runs

- **WHEN** Core restarts with a persistent offline task in a non-terminal state
- **THEN** Core restores the task and resumes using its persisted mount, provider identifier, and observation

#### Scenario: Core restarts during a fetch transfer

- **WHEN** Core restarts while an offline fetch is transferring to its final destination
- **THEN** Core resumes from the persisted phase, re-obtains a download URL when needed, and does not restart the staging provider task

### Requirement: File selection uses provider-neutral selectors

The system MUST express offline file selection with provider-neutral selectors: all files, the largest
N files, or include/exclude name patterns. The selection MUST be applied to the parsed resource before
submission, and the effective selection MUST be retained for retry and for user inspection.

#### Scenario: Default selects all files

- **WHEN** a user submits a multi-file source without a selection
- **THEN** all parsed files are submitted

#### Scenario: Include and exclude patterns are applied

- **WHEN** a user submits include or exclude patterns
- **THEN** qrypt submits exactly the parsed files matching the resulting selection

#### Scenario: Selection is retained for retry

- **WHEN** a task created with a selection is retried
- **THEN** qrypt re-applies the same selection

### Requirement: Inspection previews a source without creating a task

The system MUST provide a read-only inspection of a source that reports the normalized source, resource
title, total size, the parsed file list, an instant-save prediction when available, and parse-destination
checks when a destination is supplied. Inspection MUST NOT create a task or persist a provider parse
token, and MUST accept the same selection selectors as submission.

#### Scenario: Inspect lists parsed files

- **WHEN** a user inspects a magnet link or local torrent
- **THEN** qrypt reports the file names, sizes, and the files the current selectors would choose

#### Scenario: Inspect checks a parse destination

- **WHEN** a user inspects with a parse destination
- **THEN** qrypt reports whether the mount and directory are usable, including the encrypted-mount rule

#### Scenario: Inspect does not create a task

- **WHEN** inspection succeeds
- **THEN** no qrypt task and no provider task exist afterwards

### Requirement: Wait observes a task without canceling it

The system MUST provide a way to observe an existing offline task until it reaches a terminal state.
Waiting MUST NOT cancel or otherwise mutate the task, and interrupting the wait MUST leave the task
running. When the Core connection is lost during a wait, the command MUST attempt to reconnect and
resume observing by task id.

#### Scenario: Wait reports the terminal outcome

- **WHEN** a user waits for a running offline task
- **THEN** the command reports progress until the task reaches a terminal state and exits according to that outcome

#### Scenario: Interrupt only stops waiting

- **WHEN** the user interrupts a wait
- **THEN** the task continues running and is not canceled

#### Scenario: Optional timeout

- **WHEN** a wait timeout is supplied and elapses before a terminal state
- **THEN** the command reports a typed timeout and leaves the task running

### Requirement: Offline fetch stages in the cloud and transfers to a final destination

An offline fetch MUST first stage the source in the provider's cloud storage, then transfer the staged
result to the requested final destination. The final destination MUST accept a qrypt namespace path, a
path inside a mounted FUSE tree, or a plain local path. The transfer MUST obtain a download URL from the
provider, MUST re-obtain it when it is missing or expired, and MUST report progress for the transfer phase.
A final destination directory that does not exist MUST be created before the transfer starts; this
deliberately differs from the parse destination, which requires an explicit creation request.

#### Scenario: Fetch completes end to end

- **WHEN** a user runs `offline fetch SOURCE --parse-to /MOUNT/DIR --download-to PATH`
- **THEN** qrypt stages the source, transfers the selected files to `PATH`, and records both phases on the task

#### Scenario: Long transfer refreshes the download URL

- **WHEN** the provider download URL expires before the transfer finishes
- **THEN** qrypt re-obtains a URL and continues the transfer instead of failing

#### Scenario: Namespace destination uses the mounted writer

- **WHEN** the final destination is a qrypt namespace path or a path inside the Core's own mount tree
- **THEN** qrypt writes through the destination mount so encryption and upload semantics apply

### Requirement: Offline fetch transfer parallelism is caller-tunable

An offline fetch MUST let the caller choose how many bounded Range requests the provider URL transfer
keeps in flight per file (1–16 workers) and how many bytes each request covers (1–64 MiB per chunk).
Unset values MUST fall back to the Core defaults of 4 workers and 8 MiB chunks. Values outside those
ranges, or transfer tuning supplied without a final destination, MUST be rejected as usage errors
before any provider task exists. The chosen values MUST be persisted with the task so a recovered or
retried fetch resumes with the same parallelism.

#### Scenario: Explicit transfer tuning is applied

- **WHEN** a user runs `offline fetch SOURCE --download-to PATH --workers 8 --chunk-size 4M`
- **THEN** the transfer uses eight concurrent bounded Range requests of four megabytes each

#### Scenario: Defaults apply when nothing is set

- **WHEN** a user fetches without transfer tuning flags
- **THEN** the transfer uses the Core's default parallelism and chunk size

#### Scenario: Invalid or inapplicable tuning is rejected

- **WHEN** the workers count or chunk size is outside the documented range, or tuning is supplied for a cloud-only submission
- **THEN** qrypt rejects the request as a usage error and creates no provider task

#### Scenario: Resumed fetch keeps its tuning

- **WHEN** a fetch with explicit transfer tuning resumes after a Core restart
- **THEN** the resumed transfer uses the workers and chunk size recorded on the task

#### Scenario: Explicit transfer tuning is applied

- **WHEN** a user runs `offline fetch SOURCE --download-to PATH --workers 8 --chunk-size 4M`
- **THEN** the transfer uses eight concurrent bounded Range requests of four megabytes each

#### Scenario: Defaults apply when nothing is set

- **WHEN** a user fetches without transfer tuning flags
- **THEN** the transfer uses the Core's default parallelism and chunk size

#### Scenario: Invalid or inapplicable tuning is rejected

- **WHEN** the workers count or chunk size is outside the documented range, or tuning is supplied for a cloud-only submission
- **THEN** qrypt rejects the request as a usage error and creates no provider task

#### Scenario: Resumed fetch keeps its tuning

- **WHEN** a fetch with explicit transfer tuning resumes after a Core restart
- **THEN** the resumed transfer uses the workers and chunk size recorded on the task

#### Scenario: Plain local destination is written directly

- **WHEN** the final destination is a plain local path outside the qrypt mount tree
- **THEN** qrypt writes the bytes directly without going through a mount

#### Scenario: Missing download directory is created

- **WHEN** the final destination directory does not exist
- **THEN** qrypt creates it and proceeds, unlike a missing parse destination which requires an explicit creation request

### Requirement: Offline fetch rejects identical parse and download destinations

The system MUST reject an offline fetch whose parse destination and final destination resolve to the
same mount and the same directory, before creating any provider task. When the final destination is a
different directory on the same provider, the system MUST warn that the bytes will be downloaded and
uploaded again.

#### Scenario: Identical destinations are rejected

- **WHEN** `--parse-to /quark-test/dir` and `--download-to` resolve to the same `/quark-test/dir`
- **THEN** qrypt rejects the request as a usage error and creates no task

#### Scenario: Same provider, different directory warns

- **WHEN** the final destination is another directory on the same provider as the parse destination
- **THEN** qrypt proceeds but warns that the result will be uploaded back to the same provider

#### Scenario: Unresolvable local destination is not compared

- **WHEN** the final destination is a plain local path that cannot map to a mount
- **THEN** qrypt does not apply the same-location rule

### Requirement: Offline fetch cleans up its cloud copy and blocks by default

After a successful transfer, an offline fetch MUST remove the cloud copy it created, and only the fids
it created, unless the request asks to keep it. An offline fetch MUST block until the final destination
is written by default; interrupting MUST detach the wait while the Core task continues, and a `--no-wait`
form MUST return once staging is admitted.

#### Scenario: Cloud copy is removed on success

- **WHEN** an offline fetch finishes transferring to its final destination
- **THEN** qrypt removes the staged cloud files it created and leaves other entries in the directory untouched

#### Scenario: Cloud copy is kept on request

- **WHEN** the request asks to keep the cloud copy
- **THEN** qrypt leaves the staged files in place and reports their location

#### Scenario: Interrupt detaches without canceling

- **WHEN** the user interrupts a blocking offline fetch
- **THEN** the Core task keeps running and the user can observe it later

#### Scenario: No-wait returns after staging

- **WHEN** the request asks not to wait
- **THEN** the command returns once staging is admitted and the transfer continues in the Core

### Requirement: Offline failures use stable error codes and command outcomes

The system MUST report offline failures with stable, provider-neutral error codes and an explicit
retryable flag. Quota, policy, and unsupported-source failures MUST NOT be reported as retryable.
Command outcomes MUST be distinguishable by exit code: success, runtime or task failure (including
cancellation), and usage error.

#### Scenario: Quota exhaustion is not retryable

- **WHEN** the provider rejects a source because the account's download quota is exhausted
- **THEN** qrypt records a non-retryable quota error and reports an actionable message

#### Scenario: Canceled task is distinguishable from success

- **WHEN** a waited task ends as canceled
- **THEN** the command exits with the same non-zero code as a failure, and JSON output reports the canceled state

#### Scenario: Usage errors do not create tasks

- **WHEN** a request has an invalid source, conflicting destination selectors, or identical parse and download destinations
- **THEN** qrypt reports a usage error and creates no task

### Requirement: CLI separates cloud-side submission from local retrieval

The CLI MUST expose offline submission, inspection, fetch, and task control separately from `fs get`.
Downloading a completed remote entry into local storage MUST continue to be available through the
existing filesystem command.

#### Scenario: Submit and inspect an offline task

- **WHEN** a user submits offline work and then inspects it by task id
- **THEN** the first command creates the cloud-side task and the second reports its qrypt and provider-backed state

#### Scenario: Retrieve a completed file locally

- **WHEN** an offline task has produced a remote entry under the parse destination
- **THEN** `qrypt fs get` can still retrieve that remote entry into local storage
