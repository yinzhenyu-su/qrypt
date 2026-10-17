## ADDED Requirements

### Requirement: Provider-backed offline downloads use the stable task contract

The task API MUST expose provider-backed offline downloads as a durable `offline_download` task under the existing `download` operation. The task MUST use the same task snapshot, event, idempotency, cancellation, retry, and recovery contracts as other user-created tasks while keeping provider-specific identifiers and status in detail fields.

#### Scenario: Offline task participates in the task API

- **WHEN** a client creates an offline download through the operation-level task API
- **THEN** the returned task can be queried, observed through task events, canceled, retried, and recovered using the existing task interfaces

#### Scenario: Offline task type declares policy

- **WHEN** the task type policy is inspected
- **THEN** `offline_download` has one declared `download` operation identity, explicit persistence and visibility defaults, and an explicit recovery/retry strategy

#### Scenario: Remote state does not replace terminal task state

- **WHEN** a provider observation arrives after an offline task has become canceled, failed, or succeeded
- **THEN** the canonical task state and error remain unchanged while provider observations remain available for diagnostics
