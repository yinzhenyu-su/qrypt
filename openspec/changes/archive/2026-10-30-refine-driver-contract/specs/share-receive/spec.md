## MODIFIED Requirements

### Requirement: Save is a durable verified task

`receive save` MUST create a durable `share_save` task that opens the link, lists the selected
entries, starts the provider-side save, and persists any opaque provider wait handle before polling.
The destination MUST carry the destination mount's provider identity and, when the provider scopes
writes by workspace/drive/space, that provider-space context. Because provider listings are
eventually consistent, the task MUST verify the destination within a bounded window and MUST NOT
report success for entries that never appear. A provider MAY return a verified provider identity
instead of a full entry during the save.

#### Scenario: Save completes and is verified

- **WHEN** the provider reports the save finished and the target listing contains the entries
- **THEN** the task succeeds with the saved entries as items

#### Scenario: Lagging listing still succeeds

- **WHEN** the provider reports the save finished before the target listing shows the entries
- **THEN** qrypt retries the listing within its bounded window and succeeds once they appear

#### Scenario: Entries never appear

- **WHEN** the saved entries never appear in the target listing inside the bounded window
- **THEN** the task fails as unverified instead of reporting success

#### Scenario: Save into a provider workspace

- **WHEN** the destination mount writes inside a provider workspace or drive
- **THEN** qrypt passes that provider-space identity with the destination instead of relying on driver-private entry metadata

#### Scenario: Saving one's own link

- **WHEN** the acting account owns the link and the provider forbids self-save
- **THEN** qrypt reports `share_own_save` and creates no copy

### Requirement: Save outcomes are per entry

A save MUST fold provider results per entry: every selected entry MUST appear as a task item with
either its created entry or its own neutral error. An item's success MAY carry a verified provider
identity and name instead of a full entry object; qrypt MUST still verify the destination before
reporting success. Completion MUST distinguish all entries saved (succeeded), some saved (partially
failed), and none saved (failed), because providers report partial success and some finish
synchronously without any wait handle.

#### Scenario: Partial success is not a whole-batch failure

- **WHEN** the provider saves some entries and rejects others with per-entry errors
- **THEN** the task ends partially failed, the saved entries remain as items, and each rejected entry carries its own neutral error

#### Scenario: Synchronous provider save

- **WHEN** the provider finishes the save inside the request and returns no wait handle
- **THEN** qrypt records the outcome directly and still verifies the destination

#### Scenario: Lightweight verified result

- **WHEN** the provider returns a verified entry identity and name without a full entry object
- **THEN** qrypt records the item as saved and verifies the destination identity without requiring the provider to re-fetch the full entry
