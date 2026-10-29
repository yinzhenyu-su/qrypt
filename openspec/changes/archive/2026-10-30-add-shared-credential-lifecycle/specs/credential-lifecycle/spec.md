## Purpose

Let one credential serve any number of directory mounts of the same account with exactly one
lifecycle: one declared secret, one runtime session, one owner of rotation and refresh, and one place
to observe or invalidate it — without coupling mounts' paths, caches, upload sessions, or offline
tasks.

## ADDED Requirements

### Requirement: Credentials are declared once and referenced by mounts

The configuration SHALL provide a top-level `[[credentials]]` entry with a unique `name`, a driver
`type`, and its credential-scope `params`. A mount SHALL reference a credential by `credential` and
SHALL NOT repeat credential-scope params inline. The referencing mount's `type` MUST equal the
credential's type; an unknown reference, a type mismatch, a duplicate credential name, or a name that
is not a single path component MUST be rejected before any driver is opened. Mount-scope params (for
example `root_path`) MUST stay per mount, so the same credential can serve several directories.

#### Scenario: Several directories share one credential

- **WHEN** two mounts of the same driver reference the same credential and declare different `root_path` values
- **THEN** both mounts open independently and only one credential definition exists in the config

#### Scenario: Unknown or mistyped reference is rejected

- **WHEN** a mount references a credential that does not exist, or one whose driver type differs from the mount's type
- **THEN** validation fails with an error naming the mount and the reference, and no driver is opened

#### Scenario: Inline credential params conflict with a reference

- **WHEN** a mount declares both `credential` and a credential-scope parameter such as `cookie`
- **THEN** validation rejects the mount instead of silently choosing one source

#### Scenario: Legacy inline mounts keep working

- **WHEN** a mount declares inline credential params and no `credential` reference
- **THEN** qrypt synthesizes an implicit per-mount credential and preserves the existing per-mount state location and behavior

### Requirement: Parameters declare whether they belong to a credential or a mount

The canonical driver schema SHALL declare a scope for every built-in parameter: `credential`, `mount`,
or `both`. Generated runtime metadata, validation, and generated documentation MUST expose the same
scope. A credential-scope parameter MUST be accepted only on a credential, and a mount-scope parameter
MUST be accepted only on a mount. A `both` parameter MUST be accepted in either place, and a mount
value MUST override the credential value. Built-in parameters that omit a scope MUST fail the schema
check.

#### Scenario: Mount parameter on a credential is rejected

- **WHEN** a credential declares `root_path`, which is mount-scope
- **THEN** validation rejects the credential with an error naming the parameter and its scope

#### Scenario: Mount overrides a shared parameter

- **WHEN** a `both` parameter is set on a credential and on one referencing mount
- **THEN** the driver receives the mount's value while other mounts receive the credential's value

#### Scenario: Documentation shows scope

- **WHEN** driver documentation is generated
- **THEN** each parameter is presented with the scope that validation enforces

### Requirement: One credential has exactly one runtime session

Every mount referencing a credential MUST share one runtime session object and one persisted session
record. A rotation or refresh committed by any mount MUST be persisted once and MUST become visible to
every other mount of that credential without restart. Mounts referencing different credentials MUST
NOT observe each other's sessions.

#### Scenario: Rotation propagates to sibling mounts

- **WHEN** one mount rotates a shared cookie or access token and commits it
- **THEN** the other mounts of the same credential use the rotated value on their next request without re-authenticating or restarting

#### Scenario: Different credentials stay isolated

- **WHEN** two mounts reference credentials for different accounts of the same driver
- **THEN** each mount uses only its own session and account

#### Scenario: Session survives restart

- **WHEN** qrypt restarts and the credential params are unchanged
- **THEN** the persisted session is reused instead of re-authenticating from the static params

### Requirement: Session reuse is bound to the credential parameters

A persisted session MUST be reused only while the credential's static params match the params it was
created from, compared through a stable fingerprint. If the params change, the session MUST be
discarded for every referencing mount, the static params MUST become authoritative, and a session
created for a different credential MUST never be attributed to this one. A change to mount-scope
params MUST NOT invalidate the session.

#### Scenario: Replaced cookie takes effect

- **WHEN** the user replaces a credential's cookie with one for a different account and qrypt restarts
- **THEN** the stale session is discarded and every mount of that credential authenticates with the new cookie

#### Scenario: Rotation is not lost on restart

- **WHEN** the credential's static params are unchanged after rotation
- **THEN** the rotated session is reused on restart

#### Scenario: Changing root_path keeps the session

- **WHEN** a mount's `root_path` changes while the credential params stay the same
- **THEN** the existing session is reused

### Requirement: Refresh and rotation have a single owner

The credential layer MUST coalesce concurrent refreshes of the same credential so that at most one
provider refresh runs at a time and every waiting mount receives the same result. Session commits MUST
be versioned so a stale writer cannot overwrite a newer session. A failed refresh MUST NOT overwrite the
last known good session.

#### Scenario: Single-use refresh token is refreshed once

- **WHEN** several mounts of one credential decide to refresh concurrently
- **THEN** the provider sees one refresh call and all mounts receive its result

#### Scenario: Concurrent rotation does not lose a newer session

- **WHEN** a commit is attempted with a version that is no longer current
- **THEN** the commit is rejected as a conflict and the caller re-reads the current session instead of overwriting it

#### Scenario: Failed refresh preserves the session

- **WHEN** a refresh fails
- **THEN** the persisted session and account remain unchanged and the failure is reported to the caller

### Requirement: Credential lifetime is independent of mount and core lifetime

The credential manager MUST be process-wide and keyed by the state directory and credential name, so
a Core reload or several concurrent Core instances in one process share the same session. Removing a
mount MUST NOT delete a credential or its session; only an explicit credential removal may do so.

#### Scenario: Core reload keeps sessions

- **WHEN** the configuration is reloaded and a new Core replaces the old one
- **THEN** unchanged credentials keep their sessions instead of re-authenticating

#### Scenario: Removing one mount keeps the credential

- **WHEN** the last mount referencing a credential is removed from the config, but the credential remains declared
- **THEN** the session is retained for future mounts

### Requirement: Persistence is atomic and safe against concurrent processes

The credential session MUST be written atomically with owner-only permissions and MUST be guarded by a
file lock when more than one process can open the same state directory. A missing or corrupt session
MUST degrade to authenticating from the static params instead of failing the mount, and it MUST NOT be
silently attributed to a different credential or account.

#### Scenario: Corrupt session falls back to static params

- **WHEN** the session file cannot be parsed
- **THEN** qrypt logs the problem and authenticates from the credential's static params

#### Scenario: Two processes do not corrupt the session

- **WHEN** two qrypt processes persist the same credential concurrently
- **THEN** writes are serialized, each commit is either applied wholly or rejected with a version conflict, and the file is never left partial

### Requirement: Credential lifecycle is observable without exposing secrets

The control surface and CLI MUST report each credential's name, type, account identity when known,
session source, version, update time, and the mounts that reference it. Per-mount diagnostic snapshots
MUST name the credential and session source. Secret parameter values MUST be masked everywhere and
MUST never be written to logs or diagnostic output.

#### Scenario: Credential usage is listed

- **WHEN** a user lists credentials
- **THEN** each entry shows its account and referencing mounts with secrets masked

#### Scenario: Diagnostic snapshot identifies the credential

- **WHEN** a mount's debug snapshot is requested
- **THEN** it reports the credential name, source, version, and update time but no secret value

### Requirement: Legacy state migrates explicitly and safely

Existing per-mount rotation state MUST remain usable for legacy inline mounts. Migration into a shared
credential MUST be an explicit operation, MUST only adopt state whose account matches the credential,
and MUST NOT delete the legacy state unless the user requests it.

#### Scenario: Migration adopts matching state

- **WHEN** a user migrates a legacy mount state into a credential whose params identify the same account
- **THEN** the session is adopted into the credential store and all referencing mounts reuse it

#### Scenario: Mismatched state is not adopted

- **WHEN** the legacy state belongs to a different account than the credential
- **THEN** migration skips it and reports the mismatch instead of adopting it
