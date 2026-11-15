## MODIFIED Requirements

### Requirement: Offline task snapshots retain provider detail and result items

An offline task snapshot MUST retain the selected mount, parse destination, normalized source, effective
selection, provider task identifier, latest provider observation, and provider error, without adding
provider-specific fields to the common task schema. An offline fetch snapshot MUST additionally retain
its final destination and cleanup policy. When an offline task completes with result files, the snapshot
MUST expose them as task items. A result item MUST carry the file's staged relative path when the
provider reports one, and MUST retain it across a restart. A snapshot written before this field existed
MUST remain readable, and its items MUST fall back to the file name.

#### Scenario: Provider detail survives the snapshot round trip

- **WHEN** an offline task snapshot is persisted and read back, including after a restart
- **THEN** the mount, parse destination, source, selection, provider identifier, and latest provider observation are unchanged

#### Scenario: Fetch snapshot retains both destinations

- **WHEN** an `offline_fetch` snapshot is persisted and read back
- **THEN** the parse destination, final destination, current phase, and cleanup policy are unchanged

#### Scenario: Result files appear as items on completion

- **WHEN** an offline task completes and the provider reports result files
- **THEN** the completed snapshot exposes those files as task items with their remote identity and size

#### Scenario: Result items carry the staged relative path

- **WHEN** the provider reports a staged relative path for a result file
- **THEN** the task item exposes that path, and it is unchanged after a restart

#### Scenario: Older snapshots without a path remain readable

- **WHEN** an existing client reads a snapshot whose result items have no staged relative path
- **THEN** the snapshot remains readable and each item falls back to its file name

#### Scenario: Existing clients remain compatible

- **WHEN** an existing task client reads an `offline_download` or `offline_fetch` snapshot
- **THEN** the common fields remain readable and provider-specific data stays confined to detail and items
