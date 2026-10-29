## Purpose

Define the provider-neutral optional interfaces that let a driver expose only the behavior a backend
actually implements, and make hash trust, by-id metadata, HTTP failure handling, and remote-name
translation explicit instead of driver-specific conventions.

## ADDED Requirements

### Requirement: Optional share behavior is independently discoverable

The share contract SHALL expose link management (create/get/update/delete), account-wide listing,
synchronous save, and asynchronous save polling as independently discoverable interfaces. A driver
MUST NOT be required to implement listing when the provider has no account-wide list, and MUST NOT be
required to implement polling when the provider completes saves synchronously. Core SHALL select each
behavior from the declared share policy and a type assertion on the concrete driver, and MUST reject
an operation as unsupported before calling the provider when the corresponding behavior is absent.

#### Scenario: Provider without account-wide listing

- **WHEN** a driver implements create/get/update/delete but not account-wide listing
- **THEN** `share create/show/update/remove` work, and `share list` fails with a stable unsupported error before a provider call

#### Scenario: Provider with a synchronous save

- **WHEN** a driver completes a share save inside the request and does not implement save polling
- **THEN** Core records the outcome directly and never requires a polling handle

#### Scenario: Provider with an asynchronous save

- **WHEN** a save returns a provider wait handle and the driver implements save polling
- **THEN** Core persists the handle and polls until the save reaches a terminal state

### Requirement: Remote hashes carry provenance

The remote-hash contract SHALL distinguish a `verified` hash, computed by the provider or checked
against stored bytes, from a `declared` hash supplied by a client. A driver MAY return either, but
MUST label it. Sync and integrity decisions MUST treat only verified hashes as authoritative;
declared hashes MAY be displayed or used as advisory metadata and MUST NOT by themselves justify
skipping a transfer.

#### Scenario: Verified provider hash

- **WHEN** an entry's hash was computed by the provider or confirmed by an integrity check
- **THEN** the driver reports it as verified, and sync may use it to decide that content is unchanged

#### Scenario: Client-declared hash

- **WHEN** an entry carries only a client-supplied hash that the provider has not verified
- **THEN** the driver reports it as declared, and sync does not skip a transfer based on it alone

#### Scenario: Hash unavailable

- **WHEN** the provider exposes no usable hash
- **THEN** the driver reports the hash as unavailable

### Requirement: Entries can be resolved by provider identity when supported

A driver that can fetch one entry by its provider id SHALL expose an optional entry-resolution
operation. Core SHALL use it when an entry's private metadata is missing, for example after a copy
rebuilds an entry from public fields, and MUST fall back to the driver's existing listing behavior
when the operation is absent. A missing identity MUST map to the stable not-found error.

#### Scenario: Resolve a stripped entry

- **WHEN** an operation receives an entry whose private metadata was dropped but whose provider id is known
- **THEN** the driver resolves the full entry by id and the operation continues without listing the parent

#### Scenario: Provider does not support by-id resolution

- **WHEN** a driver does not expose entry resolution
- **THEN** Core uses the existing listing/path behavior and does not require the operation

#### Scenario: Deleted identity

- **WHEN** the provider no longer has the requested id
- **THEN** the operation returns the stable not-found error

### Requirement: HTTP failures and throttling have shared semantics

The driver HTTP contract SHALL classify `401`, `403`, `404`, `409`, `422`, and `429` responses into
stable neutral categories without parsing localized provider text. Drivers SHALL retry retryable
transport failures and `5xx`/`429` responses with bounded backoff, MUST honor a `Retry-After` value
when present, and MUST NOT retry `401`, `403`, `404`, or `422`. A configured request-rate limit SHALL
be shared by all concurrent calls of one mount's provider client.

#### Scenario: Permission denied

- **WHEN** the provider answers `403`
- **THEN** the driver returns a permission-classified error and does not retry

#### Scenario: Invalid payload

- **WHEN** the provider answers `422` with a structured body
- **THEN** the driver returns an invalid-input error, unless the operation explicitly maps that body to not-found

#### Scenario: Rate limited

- **WHEN** the provider answers `429` with `Retry-After`
- **THEN** the driver waits the indicated bounded delay and retries, sharing the mount's request-rate budget with concurrent calls

### Requirement: Remote-name translation is explicit

A driver that cannot store a plain name exactly SHALL expose a reversible remote-name mapping. It MUST
return the plain name to callers, MUST use the mapped name for provider reads and writes, and MUST
round-trip the mapping through listing and metadata. A mapping whose provider form exceeds the
provider's name limit MUST reject the name before any write. Drivers that store names unchanged MUST
NOT expose a mapping that changes them.

#### Scenario: Escaped name is stored and read back

- **WHEN** a name cannot be stored unchanged and the driver maps it
- **THEN** the provider stores the mapped form and listings/reads return the original plain name

#### Scenario: Mapping exceeds the provider limit

- **WHEN** the mapped form is longer than the provider allows
- **THEN** the driver rejects the name before creating or renaming anything

#### Scenario: Provider preserves names exactly

- **WHEN** a provider can store every valid qrypt name unchanged
- **THEN** the driver does not translate names and callers observe the exact input
