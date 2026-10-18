## ADDED Requirements

### Requirement: Provider-backed offline downloads use the stable task contract

The task API MUST expose provider-backed offline work as durable tasks under the existing `download`
operation: `offline_download` for cloud-side work and `offline_fetch` when a final destination is
requested. Both MUST use the same task snapshot, event, idempotency, cancellation, retry, and recovery
contracts as other user-created tasks while keeping provider-specific identifiers and status in detail
fields.

#### Scenario: Offline task participates in the task API

- **WHEN** a client creates an offline download or fetch through the provider-neutral offline control surface
- **THEN** the returned task can be queried, observed through task events, canceled, retried, and recovered using the existing task interfaces

#### Scenario: Offline task types declare policy

- **WHEN** the task type policy is inspected
- **THEN** both `offline_download` and `offline_fetch` have one declared `download` operation identity, explicit persistence, visibility, and creation-path policy, and an explicit recovery and retry strategy

#### Scenario: Remote state does not replace terminal task state

- **WHEN** a provider observation arrives after an offline task has become canceled, failed, or succeeded
- **THEN** the canonical task state and error remain unchanged while provider observations remain available for diagnostics

### Requirement: Offline task snapshots retain provider detail and result items

An offline task snapshot MUST retain the selected mount, parse destination, normalized source, effective
selection, provider task identifier, latest provider observation, and provider error, without adding
provider-specific fields to the common task schema. An offline fetch snapshot MUST additionally retain
its final destination and cleanup policy. When an offline task completes with result files, the snapshot
MUST expose them as task items.

#### Scenario: Provider detail survives the snapshot round trip

- **WHEN** an offline task snapshot is persisted and read back, including after a restart
- **THEN** the mount, parse destination, source, selection, provider identifier, and latest provider observation are unchanged

#### Scenario: Fetch snapshot retains both destinations

- **WHEN** an `offline_fetch` snapshot is persisted and read back
- **THEN** the parse destination, final destination, current phase, and cleanup policy are unchanged

#### Scenario: Result files appear as items on completion

- **WHEN** an offline task completes and the provider reports result files
- **THEN** the completed snapshot exposes those files as task items with their remote identity and size

#### Scenario: Existing clients remain compatible

- **WHEN** an existing task client reads an `offline_download` or `offline_fetch` snapshot
- **THEN** the common fields remain readable and provider-specific data stays confined to detail and items

### Requirement: Offline fetch is a durable two-phase task

An `offline_fetch` MUST represent a cloud staging phase followed by a transfer phase inside one durable
task. The task MUST persist enough state to resume from the correct phase after a Core restart, MUST NOT
restart a completed staging phase, and MUST report progress for the transfer separately from the staging
observation.

#### Scenario: Restart resumes the transfer phase

- **WHEN** Core restarts while an offline fetch has completed staging
- **THEN** the task resumes at the transfer phase without submitting the source again

#### Scenario: Cancel stops both phases

- **WHEN** the user cancels an offline fetch during either phase
- **THEN** the task enters the cancellation path, the pending staging or transfer is stopped, and the provider task is removed when it exists

#### Scenario: Transfer progress is observable

- **WHEN** an offline fetch is transferring to its final destination
- **THEN** the snapshot reports the transfer progress separately from the staging observation

### Requirement: Offline tasks are created and controlled through the provider-neutral offline surface

The task API MUST allow creating and controlling offline tasks with inputs that are independent of any
provider: normalized source, parse destination, selection, optional final destination, optional title,
optional directory creation, optional cloud-copy retention, and optional idempotency key. The API MUST
NOT require a provider name, driver name, socket path, or provider task identifier.

#### Scenario: Provider-neutral creation input

- **WHEN** a client creates an offline task
- **THEN** the accepted input names a source and a parse destination with an optional selection, and no provider-specific field is required

#### Scenario: Final destination selects the fetch task type

- **WHEN** a client creates offline work that includes a final destination
- **THEN** the API returns an `offline_fetch` task, and without one it returns an `offline_download` task

#### Scenario: Unsupported request fails before task creation

- **WHEN** a client requests an unsupported source kind or an unsupported selection for the selected mount
- **THEN** the API returns a structured error and persists no task

#### Scenario: Duplicate active submission is idempotent by default

- **WHEN** a client submits the same normalized source, parse destination, and selection while a matching task is still active and does not request a new task
- **THEN** the API returns the existing task instead of creating a second provider task
