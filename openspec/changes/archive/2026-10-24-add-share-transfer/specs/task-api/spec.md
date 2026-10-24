## ADDED Requirements

### Requirement: Share tasks use the stable task contract

The task API MUST expose share receive work as durable tasks: `share_save` under a new stable
`share` operation for provider-side copies of share content, and `share_fetch` under the existing
`download` operation when a local destination is requested. Both MUST use the same task snapshot,
event, idempotency, cancellation, retry, and recovery contracts as other user-created tasks while
keeping provider-specific identifiers, tickets, and status in detail fields.

#### Scenario: Share task participates in the task API

- **WHEN** a client creates a receive task through the provider-neutral control surface
- **THEN** the returned task can be queried, observed through task events, canceled, retried, and recovered using the existing task interfaces

#### Scenario: Share task types declare policy

- **WHEN** the task type policy is inspected
- **THEN** `share_save` declares the `share` operation and `share_fetch` declares `download`, each with explicit persistence, visibility, creation-path, recovery, and retry policy

#### Scenario: Remote state does not replace terminal task state

- **WHEN** a provider observation arrives after a share task has become canceled, failed, or succeeded
- **THEN** the canonical task state and error remain unchanged while provider observations remain available for diagnostics

### Requirement: Share task snapshots retain link, destination, and result items

A share task snapshot MUST retain the link, acting mount, selection, both destinations, provider save
id, phase, cleanup policy, and latest observation in detail fields rather than in the common schema.
On completion it MUST expose the entries it created or downloaded as task items, and it MUST NOT
print passcodes or session tokens by default.

#### Scenario: Detail survives the snapshot round trip

- **WHEN** a share task snapshot is persisted and read back, including after a restart
- **THEN** the link, acting mount, selection, destinations, provider ids, phase, and cleanup policy are unchanged

#### Scenario: Provider-specific fields stay out of the common schema

- **WHEN** an existing task client reads a share task snapshot
- **THEN** the common fields remain readable and provider identifiers, tickets, and tokens stay confined to detail

#### Scenario: Result items on completion

- **WHEN** a `share_save` task completes and the provider reports created entries
- **THEN** the snapshot exposes those entries as items with their names, sizes, and remote identities

#### Scenario: Secrets are not part of the default view

- **WHEN** a share task snapshot is rendered without an explicit secrets request
- **THEN** the passcode and any session token are redacted while the link identity stays readable

### Requirement: Share detail is part of the durable task key

The persisted task key used to decide whether a task needs a journal write MUST include the share
detail, so a share phase transition or a new provider identifier is durable even when the task
state and progress do not change.

#### Scenario: Phase change is journaled

- **WHEN** a share fetch moves from its save phase to its download phase
- **THEN** the durable key changes and the transition is persisted

#### Scenario: Progress-only updates are skipped

- **WHEN** only transfer progress changes inside a phase
- **THEN** the durable key is unchanged and no journal write is required

### Requirement: Share save outcomes are per entry

A share save MUST fold provider results per entry: each selected entry MUST carry its own result
(either the entry created in the drive or its own neutral error), and the task outcome MUST
distinguish all-saved, partially-saved, and none-saved. A provider wait handle MUST stay opaque and
may be absent when the provider finishes synchronously.

#### Scenario: Partial save is visible per entry

- **WHEN** a save completes with some entries created and others rejected
- **THEN** the task is partially failed, created entries appear as items with remote identities, and rejected entries appear with their own error codes

#### Scenario: Synchronous provider does not need a handle

- **WHEN** a provider returns a finished outcome with no wait handle
- **THEN** the task completes from that outcome and no polling is attempted

#### Scenario: Opaque handle round trip

- **WHEN** a provider returns a wait handle that is a monitor URL rather than a task id
- **THEN** the task persists and uses it unchanged, without interpreting its format

### Requirement: Share fetch is a durable multi-phase task

A `share_fetch` MUST represent an optional provider-side save, a transfer phase, and a cleanup
phase inside one durable task. The task MUST persist enough state to resume from the correct phase
after a Core restart, MUST NOT repeat a completed save, and MUST report transfer progress separately
from the provider save observation.

#### Scenario: Restart resumes the transfer

- **WHEN** Core restarts while a share fetch has completed its save phase
- **THEN** the task resumes at the transfer phase without saving again

#### Scenario: Cancel stops observation and transfer

- **WHEN** the user cancels a share fetch during either phase
- **THEN** the task enters the cancellation path, the in-flight transfer stops, and no cleanup of already-saved entries is attempted

#### Scenario: Transfer progress is observable

- **WHEN** a share fetch is transferring to its destination
- **THEN** the snapshot reports the transfer progress separately from the provider save observation

### Requirement: Share tasks are created through the provider-neutral receive surface

The task API MUST allow creating receive tasks with inputs independent of any provider: the share
link, optional passcode, optional cloud destination, optional local destination, optional mount,
selection, directory creation, cloud-copy retention, transfer tuning, and an optional idempotency
key. The API MUST NOT require a provider name, driver name, socket path, or provider task
identifier.

#### Scenario: Local destination selects the fetch type

- **WHEN** a client creates receive work that includes a local destination
- **THEN** the API returns a `share_fetch` task, and without one it returns a `share_save` task

#### Scenario: Unsupported request fails before task creation

- **WHEN** a client names a mount that cannot receive shares, or a cross-provider destination
- **THEN** the API returns a structured error and persists no task

#### Scenario: Duplicate active receive is idempotent by default

- **WHEN** a client submits the same link, mount, selection, and destination while a matching task is active and does not request a new task
- **THEN** the API returns the existing task instead of starting a second provider save
