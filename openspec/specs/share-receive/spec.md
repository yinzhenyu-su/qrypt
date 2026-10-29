# share-receive Specification

## Purpose

Let a user look inside a share link, copy its content into their own drive provider-side (转存), or
download it locally — with or without saving — as durable, observable, recoverable tasks that never
require a provider name.

## Requirements

### Requirement: Receive commands are provider-neutral and infer the link's provider

The system MUST accept a share link as the source and MUST derive its provider from the link using
the driver's link parser. The acting account MUST be resolved in this order: an explicit `--mount`, a
`--to` destination mount, or the only configured mount of that provider. When the link's provider is
unknown or several mounts could act, the system MUST fail with an actionable usage error and MUST
create no task.

#### Scenario: Link provider is inferred

- **WHEN** a user runs `qrypt receive inspect https://pan.quark.cn/s/<id>` with one Quark mount configured
- **THEN** qrypt uses that mount's account without a provider flag

#### Scenario: Several mounts of the same provider

- **WHEN** the link's provider has more than one configured mount and no `--mount` or `--to` is given
- **THEN** qrypt lists the candidate mounts and creates no task

#### Scenario: Foreign link

- **WHEN** no configured driver recognizes the link
- **THEN** qrypt returns `share_link_invalid` and creates no task

#### Scenario: No running Core

- **WHEN** a receive command runs while no Core is running
- **THEN** the command fails with a runtime error naming the missing Core and creates no task

### Requirement: Receive destinations must belong to the link's provider

A receive that writes into a cloud destination MUST resolve that destination to one mount and MUST
require it to be the link's provider. A cross-provider destination MUST be rejected with
`share_cross_provider_unsupported` and an actionable hint, because a native save is a provider-side
copy that only exists inside one provider.

#### Scenario: Same-provider save

- **WHEN** a user runs `receive save <quark-link> --to /quark-download/inbox`
- **THEN** the save runs provider-side and no bytes pass through qrypt

#### Scenario: Cross-provider destination is rejected

- **WHEN** a user runs `receive save <quark-link> --to /aliyun/inbox`
- **THEN** qrypt returns `share_cross_provider_unsupported` and suggests `receive fetch --download-to` followed by `fs put`

#### Scenario: Missing destination directory

- **WHEN** the destination directory does not exist and `--mkdir` was not given
- **THEN** qrypt reports the missing directory and creates no task

#### Scenario: Destination is a mount root

- **WHEN** a user selects only `--mount NAME`
- **THEN** the mount root is the destination and the root is accepted

### Requirement: The cloud destination must not be an encrypted mount

A receive that saves into the cloud MUST reject a destination whose mount applies qrypt
encryption, because a provider-side copy never passes through qrypt and cannot honour that mount's
ciphertext convention. A local download destination inside an encrypted mount MUST remain allowed,
because those bytes are written through qrypt.

#### Scenario: Encrypted cloud destination is rejected

- **WHEN** a user runs `receive save LINK --to /vault/inbox` where `/vault` applies qrypt encryption
- **THEN** qrypt rejects the request before any provider call and creates no task

#### Scenario: Local download into an encrypted mount is allowed

- **WHEN** a user runs `receive fetch LINK --download-to /vault/downloads`
- **THEN** qrypt writes through the encrypted mount and encrypts the bytes as usual

#### Scenario: Same-provider check precedes the encryption check

- **WHEN** a destination is both a different provider and an encrypted mount
- **THEN** qrypt reports the provider mismatch, because a cross-provider destination is rejected first

### Requirement: Inspect is read-only

Inspection MUST open the link, list one directory, and report neutral metadata and entries; it
MUST NOT create a task, save anything, mutate the provider, or persist a session token.

#### Scenario: Browse without side effects

- **WHEN** a user runs `receive inspect <link>`
- **THEN** qrypt lists the entries with names, sizes, and directory flags and leaves the drive unchanged

#### Scenario: Navigate into a directory

- **WHEN** a user inspects a subdirectory of the link
- **THEN** qrypt lists that directory's entries using share-relative paths only

### Requirement: Passcodes are handled explicitly

A passcode MUST be accepted from the link's query, from `--passcode`, or from a single stdin line
when the flag has no value. A link that needs a passcode MUST report `share_passcode_required`; a
wrong passcode MUST report `share_passcode_wrong`. Neither error may expose provider-specific text.

#### Scenario: Passcode from the link

- **WHEN** a user passes a link containing `?pwd=abcd`
- **THEN** qrypt uses `abcd` and proceeds without prompting

#### Scenario: Missing passcode

- **WHEN** a passcode-protected link is opened without a passcode
- **THEN** qrypt reports `share_passcode_required` and saves nothing

#### Scenario: Wrong passcode

- **WHEN** the supplied passcode is rejected by the provider
- **THEN** qrypt reports `share_passcode_wrong` and creates no task

### Requirement: Selection is provider-neutral

A receive MUST apply the same selection surface as offline work — `all` (default), `largest[:N]`,
and include/exclude globs — before any provider save or transfer. A provider without selection
support MUST reject a selector other than `all` before creating a task.

#### Scenario: Largest selection

- **WHEN** a user saves with `--select largest:2`
- **THEN** exactly the two largest selected entries are saved

#### Scenario: Unsupported selection

- **WHEN** a user requests a glob selection on a provider without selection support
- **THEN** qrypt returns a structured error and creates no task

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

### Requirement: Fetch downloads with or without saving and cleans up after itself

`receive fetch` MUST require a local destination. With a cloud destination it MUST perform the save
and then download the saved entries; without one it MUST download the selected share entries
directly. After a successful download of saved content it MUST remove only the entries it saved,
unless the caller asks to keep them. A direct download MUST never delete provider content.

#### Scenario: Save then download

- **WHEN** a user runs `receive fetch <link> --to /quark/inbox --download-to ./out`
- **THEN** qrypt saves into `/quark/inbox`, downloads from that saved copy, and removes the copy it created

#### Scenario: Keep the cloud copy

- **WHEN** the same command is run with `--keep-cloud`
- **THEN** the saved entries remain in the cloud after a successful download

#### Scenario: Direct download

- **WHEN** a user runs `receive fetch <link> --download-to ./out` with no cloud destination
- **THEN** qrypt downloads the selected entries without saving and leaves the provider unchanged

#### Scenario: Partial failure keeps everything

- **WHEN** a multi-entry download fails partway
- **THEN** qrypt deletes no saved entries and reports the failure with the completed entries visible

#### Scenario: Destination writers

- **WHEN** the local destination is a plain path, a namespace path, or a path inside the Core's mount tree
- **THEN** qrypt writes directly, through the namespace, or through VFS respectively, and creates missing parent directories

### Requirement: Receive tasks are observable and controllable

Receive work MUST be a durable task in the task API: listable and showable through the receive
commands, cancelable without deleting the user's copy, retryable from persisted detail, and
observable to a terminal state without canceling. An interrupt of a wait MUST stop only the
observation.

#### Scenario: Cancel during save

- **WHEN** the user cancels a running save
- **THEN** the task stops observing and reports canceled while any provider-side copy already started is left in place

#### Scenario: Retry after failure

- **WHEN** the user retries a failed receive task
- **THEN** qrypt reopens the link and resumes from persisted detail without repeating a completed save

#### Scenario: Interrupt does not cancel

- **WHEN** the user interrupts `receive wait`
- **THEN** the observation stops with exit code 130 and the task keeps running

#### Scenario: Restart recovery

- **WHEN** Core restarts while a receive task is non-terminal
- **THEN** the task resumes from the persisted phase and provider ids

### Requirement: Receive errors are stable and provider-neutral

Every receive failure MUST surface as a stable neutral code from the set documented in the change
design, and MUST carry a usage, not-found, or runtime class that the CLI maps to an exit code.
Adding a code MUST NOT change the meaning of an existing one.

#### Scenario: Provider detail does not leak as the only message

- **WHEN** a provider rejects a save because a ticket is stale
- **THEN** the task error carries `share_ticket_invalid` and the provider's text stays in diagnostic detail

#### Scenario: Human verification is surfaced, not retried forever

- **WHEN** a provider demands a captcha or SMS verification that a stored credential cannot satisfy
- **THEN** qrypt reports `share_verification_required` as a non-retryable runtime error with a hint to complete the verification in the provider's own client

#### Scenario: Throttling is retryable

- **WHEN** a provider rate-limits the account
- **THEN** qrypt reports `share_rate_limited` marked retryable so the task retry path applies a backoff instead of failing permanently
