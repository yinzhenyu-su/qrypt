# Spec Delta

## MODIFIED Requirements

### Requirement: Upload observers use snapshots as the recovery source of truth

The system MUST expose task and item state, meaningful progress, capabilities, reliable resume offsets, and structured errors through the existing task snapshot contract. After gaps or restart, consumers MUST be able to resynchronize from snapshots. A live item MUST expose both the offset usable for continuing this session and a durable offset that is safe after restart; after recovery the resume offset MUST equal the durable offset. Reconciliation and missing-source conditions MUST be distinguishable through additive phase, stable error codes, and existing action fields without changing terminal-state or caller-pause authority.

#### Scenario: Agent obtains the current upload state
- **WHEN** an agent queries user-visible upload tasks
- **THEN** the response includes snapshots sufficient to classify progress, waiting, retry, failure, completion, and any unresolved remote outcome

#### Scenario: Android resynchronizes after a gap
- **WHEN** the task event stream reports that a snapshot is required
- **THEN** the client fetches the latest task snapshot before resuming incremental event processing, using its reliable durable offset rather than reconstructing one from progress events

#### Scenario: Accepted bytes were not checkpointed before termination
- **WHEN** a process terminates after accepting more bytes than its durable checkpoint
- **THEN** the recovered snapshot reports only the validated durable resume position and does not imply that unconfirmed tail bytes are guaranteed

#### Scenario: A live client continues writing
- **WHEN** a client is still feeding an open item and the latest snapshot includes both resume and durable offsets
- **THEN** it may continue from the live resume offset, while treating the durable offset as the maximum it could rely on after a restart

#### Scenario: Remote replacement outcome is uncertain
- **WHEN** a remote replacement timed out after it might have taken effect
- **THEN** the snapshot exposes a recognizable reconciliation phase or structured reason without claiming remote completion or instructing an unsafe blind retry

#### Scenario: A direct source must be supplied again
- **WHEN** an unfinished direct upload has no reusable candidate and its source cannot be reopened
- **THEN** the snapshot exposes an actionable missing-source error and only valid recovery actions, without pretending that qrypt retained local staging

#### Scenario: An older mobile client does not know the new phases
- **WHEN** a client receives a reconciliation or missing-source phase it does not recognize
- **THEN** the existing state, stable error code, and available actions still prevent treating the item as complete or blindly retrying it
