## Purpose

Provide a durable, mount-aware way to submit remote sources to a supported cloud
drive for provider-side downloading without transferring the source bytes
through the qrypt process.

## ADDED Requirements

### Requirement: Offline download selects exactly one destination mount

The system MUST resolve an offline download destination to exactly one configured mount and a remote path within that mount. The canonical CLI form MUST accept a mount-qualified virtual path such as `/quark-test/downloads`; an explicit `--mount NAME --to /remote/path` form MAY be provided as an equivalent spelling, but conflicting mount selections MUST be rejected.

#### Scenario: Mount-qualified destination is accepted

- **WHEN** a user submits `offline add URL --to /quark-test/downloads`
- **THEN** qrypt selects the `quark-test` mount and passes `/downloads` as the remote destination path for that mount

#### Scenario: Cross-mount destination is rejected

- **WHEN** a user submits an offline task with an explicit source or selected provider of `115_open` and a destination belonging to another mount such as `/baidu/downloads`
- **THEN** qrypt rejects the request instead of asking the 115 provider to interpret a foreign mount path

#### Scenario: Unknown mount is reported before provider submission

- **WHEN** the first component of a mount-qualified destination does not name a configured mount
- **THEN** qrypt returns a structured usage or not-found error and MUST NOT create a provider task

### Requirement: Supported providers expose offline download as a capability

The system MUST allow a provider to advertise whether it supports cloud-side offline download. An offline request for a provider without that capability MUST fail before creating a qrypt task or a remote provider task.

#### Scenario: Unsupported provider is selected

- **WHEN** a user submits an offline task to a mount whose driver does not implement offline download
- **THEN** qrypt reports that offline download is unsupported for that mount and creates no task

#### Scenario: Supported provider receives a normalized request

- **WHEN** a supported provider receives an offline request
- **THEN** it receives the source URL or magnet URI and a destination path relative to its own mount, without qrypt namespace components

### Requirement: Offline submission returns a durable qrypt task

The system MUST represent a successful offline submission as a persistent user task with type `offline_download` and operation `download`. The task MUST retain the selected mount, destination path, source, provider task identifier, and idempotency metadata needed for observation and recovery.

#### Scenario: Offline task is created

- **WHEN** the provider accepts an offline source
- **THEN** qrypt returns a task snapshot with a stable task ID, `offline_download` type, the selected mount, and an observable queued or running state

#### Scenario: Provider rejects submission

- **WHEN** the provider rejects the source or destination
- **THEN** qrypt returns the provider error and MUST NOT persist a runnable task without a provider task identifier

#### Scenario: Retried submission uses the same idempotency key

- **WHEN** a client retries a timed-out submission with the same idempotency key and equivalent parameters
- **THEN** qrypt returns the original offline task rather than creating a second logical task

### Requirement: Provider states map to the canonical task lifecycle

The system MUST map provider-owned queued, running, successful, failed, canceled, and unavailable observations into the canonical qrypt task state without rewriting a task that has already reached a terminal state. Provider-specific status and identifiers MAY remain in diagnostic detail fields.

#### Scenario: Provider task completes

- **WHEN** polling observes that the provider task completed and the destination entry is visible
- **THEN** qrypt marks the offline task succeeded and records the resulting remote identity when available

#### Scenario: Provider task fails

- **WHEN** polling observes a provider failure
- **THEN** qrypt marks the task failed with a readable error and preserves the provider status in task detail

#### Scenario: Provider task disappears after completion

- **WHEN** the provider no longer lists a task but the destination entry can be resolved
- **THEN** qrypt treats the offline task as successfully completed rather than as a missing-task failure

#### Scenario: Canceled qrypt task is observed later

- **WHEN** a user cancels an offline task and a later provider observation reports queued, running, or succeeded
- **THEN** the qrypt task remains canceled and the later provider state is stored only as observation detail

### Requirement: Offline tasks survive restart and support cancellation

The system MUST persist enough offline task context to resume provider polling after Core restart. Cancel MUST request cancellation from the provider when a provider task identifier is available, and retry MUST create a new provider task without allowing two active qrypt runners for the same task.

#### Scenario: Core restarts while provider task is running

- **WHEN** Core restarts with a persistent offline task in a non-terminal state
- **THEN** Core restores the task and resumes polling using its persisted provider identifier and mount

#### Scenario: User cancels a running offline task

- **WHEN** the user cancels an offline task with a provider identifier
- **THEN** qrypt records cancellation before cleanup completes and requests provider-side cancellation

#### Scenario: User retries a failed offline task

- **WHEN** the user retries a failed offline task
- **THEN** qrypt creates a new provider task under the same qrypt task lifecycle and does not run the old provider task as the current execution

### Requirement: CLI separates cloud-side submission from local retrieval

The CLI MUST expose offline submission and task inspection separately from `fs get`. Offline commands MUST operate on remote destinations; downloading a completed remote entry to local storage MUST continue to use the existing filesystem command.

#### Scenario: Submit and inspect an offline task

- **WHEN** a user runs `qrypt offline add URL --to /mount/path` and then `qrypt offline show TASK_ID`
- **THEN** the first command creates the cloud-side task and the second reports its qrypt and provider-backed state

#### Scenario: Retrieve the completed file locally

- **WHEN** an offline task has produced `/mount/downloads/file.zip`
- **THEN** `qrypt fs get --mount mount /downloads/file.zip ./file.zip` downloads the remote entry to local storage
