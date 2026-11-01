# koofr-driver Specification

## Purpose
Provide first-class Koofr support as a built-in qrypt mount: app-password authentication, one
provider mount per qrypt mount, complete path-based listings, range reads, deterministic write and
conflict semantics, single-request uploads that preserve modification times, case-insensitive
remote-name resolution, and per-entry share links with anonymous receive and fetch — while
containing the provider's observed quirks (auto-rename defaults, cascading deletes, mount-scoped
link endpoints, out-of-sync external mounts) so VFS and sync observe normal backend behavior.

## Requirements

### Requirement: Mounts authenticate with an app password

A Koofr mount SHALL require `email` and `password` (an app password) and MUST validate them against
the provider before the mount becomes usable; a rejected credential MUST surface as an
authentication error and MUST NOT open the mount. Credentials MUST NEVER appear in errors, logs,
or snapshots.

#### Scenario: Invalid credential is rejected

- **WHEN** the provider answers the credential validation with `401`
- **THEN** initialization fails with an authentication error and no remote directory is served

#### Scenario: Credentials stay secret

- **WHEN** an authenticated request fails
- **THEN** the resulting error and any diagnostic output contain no credential material

### Requirement: One provider mount is selected and health-checked

`mount_id` SHALL select one provider mount, MUST default to the primary mount discovered from the
provider, and MUST be verified online and writable before initialization succeeds. `root_path`
SHALL be resolved during initialization; an unresolved root MUST fail initialization without
falling back to the mount root. Retried initialization MUST be idempotent.

#### Scenario: Primary mount is mounted by default

- **WHEN** a mount declares valid credentials and omits `mount_id`
- **THEN** initialization selects the provider's primary mount and the mount serves it

#### Scenario: Out-of-sync external mount is rejected

- **WHEN** the selected mount id refers to an external mount whose operations fail with a
  not-synced provider error
- **THEN** initialization fails with a provider-state error instead of serving a mount whose every
  operation fails

#### Scenario: Missing root path does not fall back

- **WHEN** `root_path` names a path that does not exist
- **THEN** initialization fails instead of mounting the mount root, including on a retry

### Requirement: Listings are complete and path-addressed

A directory listing SHALL return every direct child of the requested absolute path in one provider
response. The driver MUST NOT issue a listing for an empty or relative path, because the provider
treats an empty path as the mount root. Entries SHALL map the provider name, directory flag, byte
size, millisecond modification time, and content hash; directory sizes SHALL be flattened to zero.

#### Scenario: Mixed directory

- **WHEN** a directory contains files and subdirectories
- **THEN** every child is listed once with its name, size, mtime, and type, and directories report
  size zero

#### Scenario: Empty path is rejected before the request

- **WHEN** a listing is requested for an empty path
- **THEN** the driver rejects it locally and never sends the request

#### Scenario: Missing path

- **WHEN** a listing is requested for a path that does not exist
- **THEN** the driver returns a not-found error

#### Scenario: Nested path resolution

- **WHEN** a multi-segment path is resolved
- **THEN** each segment is verified in order and a missing segment returns not-found without
  side effects

### Requirement: Remote names resolve case-insensitively

Remote-name resolution SHALL match a requested name against the directory listing without regard to
case and SHALL return the provider's canonical casing and identity, because the provider treats
names as case-insensitive. A name matching no existing entry case-insensitively SHALL report
not-found rather than a second identity.

#### Scenario: Case-variant resolves to the existing entry

- **WHEN** `A.BIN` is resolved in a directory that contains `a.bin`
- **THEN** the resolution returns `a.bin`'s identity and not a new name

#### Scenario: Unknown name

- **WHEN** a name matches no existing entry case-insensitively
- **THEN** resolution reports not-found and creates nothing

### Requirement: Reads honor byte ranges

Reading a file SHALL request the provider's content endpoint with a standard `Range` header. A
`206` response SHALL return exactly the requested window. Reads MUST NOT issue a per-read metadata
request, and authentication, not-found, and rate-limit responses MUST map to qrypt's stable error
categories.

#### Scenario: Exact range

- **WHEN** a caller reads a middle window of a file
- **THEN** the driver returns exactly those bytes

#### Scenario: Offset at or past end of file

- **WHEN** a caller reads at an offset equal to or beyond a known size
- **THEN** the driver returns an empty reader rather than an error

#### Scenario: Out-of-bounds range past EOF

- **WHEN** the provider answers `416` for a start offset beyond the file size
- **THEN** the driver maps it consistently with the offset-at-EOF rule and never returns partial
  garbage

#### Scenario: Provider ignores the range

- **WHEN** the content endpoint answers `200` with the whole object
- **THEN** the driver discards bytes before the offset and returns at most the requested length

### Requirement: Writes report conflicts through provider semantics

Creating a directory SHALL return the existing directory when the provider reports the name already
exists. Renaming SHALL map a name collision, including a case-only variant, to a conflict. Moving
SHALL keep the entry within the mounted provider mount. Deleting SHALL map an already-missing entry
to not-found and a failed conditional guard to a conflict.

#### Scenario: Directory already exists

- **WHEN** a directory is created and the provider answers the already-exists conflict
- **THEN** the existing entry is returned and no duplicate is created

#### Scenario: Case-only rename collides

- **WHEN** an entry is renamed to a name that differs only in case from an existing entry
- **THEN** the driver reports a conflict and does not create a second entry

#### Scenario: Cross-mount destination is rejected

- **WHEN** a move or copy targets another provider mount
- **THEN** the driver rejects it locally instead of issuing the request

### Requirement: Deletes are conditional and cascade non-empty directories

Deleting SHALL carry the entry's size, mtime, and hash as conditional guards when available. A
guard mismatch MUST report a conflict and leave the entry in place. A non-empty directory delete
MUST be treated as a provider-side cascade into the trash, because the provider deletes it without
an explicit flag.

#### Scenario: Conditional delete guard fails

- **WHEN** a delete carries a size guard and the stored size differs
- **THEN** the driver reports a conflict and the entry remains

#### Scenario: Non-empty directory delete cascades

- **WHEN** a caller deletes a directory that still contains children
- **THEN** the delete succeeds and the children move to the provider trash

#### Scenario: Delete an already-missing entry

- **WHEN** a caller deletes an entry that no longer exists
- **THEN** the driver reports not found

### Requirement: Server-side copy preserves metadata within the mount

Server-side copy SHALL copy an entry to a destination on the same provider mount in one provider
operation and SHALL send the source's modification time so the copy preserves it. A destination
collision MUST map to a conflict; a missing source MUST report not-found.

#### Scenario: Copy preserves mtime

- **WHEN** a file is copied to a new name
- **THEN** the copy has the source's byte size, content hash, and modification time

#### Scenario: Copy destination occupied

- **WHEN** the destination name already exists
- **THEN** the driver reports a conflict instead of overwriting or auto-renaming

### Requirement: Uploads are single-request and deterministic

Uploading SHALL use one multipart request that always pins overwrite and disables auto-rename, so an
exact-name collision replaces the entry deterministically instead of silently creating a renamed copy.
A requested name that collides case-insensitively with a different existing name SHALL report a
conflict before the request, because the provider would silently overwrite the sibling while keeping
its canonical casing. The driver MUST NOT declare resumable uploads.

#### Scenario: Auto-rename is never used

- **WHEN** any upload is sent
- **THEN** the request pins overwrite and disables auto-rename, and a collision replaces rather
  than forks the file

#### Scenario: Case-variant collision is a conflict

- **WHEN** `B.bin` is uploaded into a directory that contains `b.bin`
- **THEN** the driver reports a conflict without sending the upload and `b.bin` keeps its content

#### Scenario: Resumable upload is not advertised

- **WHEN** the driver's capabilities are queried
- **THEN** no resumable-upload capability is declared, matching the provider's single-request
  upload model

#### Scenario: Progress is observable

- **WHEN** an upload transfers bytes
- **THEN** the driver reports cumulative uploaded bytes for the full source size

### Requirement: Uploads preserve modification time

Every upload SHALL send the source's modification time in milliseconds, because an overwrite
without it resets the modification time to server time. The returned entry MUST reflect the exact
byte size, requested name, provider hash, and requested modification time.

#### Scenario: Upload round-trips metadata

- **WHEN** a file is uploaded with a modification time
- **THEN** the resulting entry has the same size, name, and modification time, and exposes the
  provider's content hash

#### Scenario: Overwrite preserves mtime

- **WHEN** an existing entry is uploaded over with the same content and a requested modification
  time
- **THEN** the stored modification time is the requested one, not the server time

### Requirement: Upload names are validated before any request

Names MUST be validated before any request: non-empty, without `/`, NUL, or backslash, valid
UTF-8, and bounded in length, because the provider falls back to the multipart part filename when
a query name is invalid and would silently store the wrong name.

#### Scenario: Invalid name is rejected before the request

- **WHEN** an upload name contains a backslash, invalid UTF-8, or exceeds the length bound
- **THEN** the driver rejects the upload locally and no request is sent

### Requirement: Provider errors map to stable categories

Non-2xx responses SHALL be mapped by status and structured error code to qrypt's stable error
categories: authentication, not-found, conflict, invalid input, rate limit, and provider state.
Error bodies that are not JSON MUST still produce a redacted, categorized error. Provider request
identifiers SHALL be recorded in diagnostics.

#### Scenario: Structured conflict

- **WHEN** the provider answers `409` with an already-exists code
- **THEN** the driver reports a conflict with the provider message redacted

#### Scenario: Non-JSON error body

- **WHEN** the provider answers `500` with a plain-text body
- **THEN** the driver reports a categorized error and never fails to parse

#### Scenario: Rate limit is mapped

- **WHEN** the provider answers `429`
- **THEN** the driver reports a rate-limit error suitable for backoff

### Requirement: Out-of-sync mounts are not retried as transient failures

A provider not-synced error on an external mount MUST NOT be retried as a transient failure,
because it persists while the mount is out of sync; it MUST surface as a distinct provider-state
error.

#### Scenario: Not-synced mount is not retried

- **WHEN** the provider answers `500` with a not-synced code
- **THEN** the driver surfaces a provider-state error without retry loops

### Requirement: Share links are managed per entry

The driver SHALL create, read, list, update, and delete share links for existing entries through
the provider's mount-scoped link endpoints, including listing all of the account's links. A link
MUST cover exactly one entry; passcode protection, an expiry window, and the downloadable flag
SHALL each be settable.

#### Scenario: Create and list a link

- **WHEN** a link is created for one file and links are listed
- **THEN** the link appears in the mount's list and the account-wide list with its URL

#### Scenario: Set a passcode

- **WHEN** a passcode is set on a link
- **THEN** the link reports passcode protection

#### Scenario: Set an expiry window

- **WHEN** an expiry is set on a link
- **THEN** the link reports the requested validity window

#### Scenario: Link deletion

- **WHEN** a link is deleted
- **THEN** the provider confirms removal and subsequent link access fails as not found

#### Scenario: Rate-limited link creation

- **WHEN** the provider answers link creation with `429`
- **THEN** the driver reports a rate-limit error

### Requirement: Share passwords are never leaked and removal is unsupported

Provider responses that echo share passwords MUST NEVER be logged, persisted in diagnostics, or
included in debug snapshots. Removing a passcode MUST be rejected as unsupported, because the
provider's reset operation replaces the password with a random one instead of clearing it.

#### Scenario: Passcode material stays out of diagnostics

- **WHEN** a passcode is set and diagnostics or a debug snapshot are collected
- **THEN** the passcode never appears in any output

#### Scenario: Passcode removal is unsupported

- **WHEN** a caller asks to remove a link's passcode
- **THEN** the driver rejects the update as unsupported rather than replacing the passcode with a
  random one

### Requirement: Share links can be parsed and fetched anonymously

The driver SHALL parse the provider's share URLs, including short URLs, into link identities. It
SHALL produce one direct anonymous download target per selected file through the provider's
content-link route, which requires no authentication. Save-to-drive SHALL NOT be advertised.

#### Scenario: Parse share URLs

- **WHEN** a full share URL or a short URL is parsed
- **THEN** the driver resolves the link identity

#### Scenario: Anonymous fetch of a nested file

- **WHEN** a caller fetches a file nested inside a shared directory
- **THEN** the download target returns exactly the file's bytes without authentication

#### Scenario: Directory target is rejected

- **WHEN** a fetch selects a directory path
- **THEN** the driver rejects it instead of returning a zip archive, because the content-link route
  zips directories

#### Scenario: Save-to-drive is unsupported

- **WHEN** a caller asks to save share content into a Koofr mount
- **THEN** the driver reports the save capability as unsupported

### Requirement: Capabilities, space, and observability match implemented behavior

The driver SHALL declare exactly `writer`, `source_uploader`, `space`, `path_resolver`,
`remote_name_resolver`, `mtime`, `server_side_copy`, `share`, `share_receive`, and `share_fetch`,
and MUST NOT declare resumable uploads, share save, or offline capabilities. Space reporting SHALL
convert the provider's MiB-scale totals to bytes. Metrics MUST be emitted in the normalized driver
event format.

#### Scenario: Capability list is exact

- **WHEN** the driver reports its capabilities
- **THEN** the list contains exactly the ten declared capabilities and no resumable, save, or
  offline capability

#### Scenario: Standalone mtime update is unsupported

- **WHEN** a modification time is set on an existing remote entry outside an upload or copy
- **THEN** the driver reports the operation as unsupported

#### Scenario: Space converts MiB to bytes

- **WHEN** space is queried
- **THEN** total and free bytes are derived from the provider's MiB figures

### Requirement: Debug snapshots are redacted

The debug snapshot MUST report the driver name, base URL, mount identity, root, health, counters,
and a redacted last error, and MUST NOT contain credentials, authorization headers, or share
passcodes.

#### Scenario: Debug output is safe

- **WHEN** a debug snapshot is collected after a failed request or a share operation
- **THEN** it contains no credential, authorization header, or share passcode
