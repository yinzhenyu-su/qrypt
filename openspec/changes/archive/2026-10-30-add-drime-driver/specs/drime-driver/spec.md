## Purpose

Provide first-class Drime Cloud support as a built-in qrypt mount: one workspace per mount, complete
and deterministic listings, range reads, safe writes, resumable uploads, preserved modification
times and SHA-256 content hashes, while containing the provider's documented inconsistencies so VFS
and sync observe normal backend behavior.

## ADDED Requirements

### Requirement: Mounts authenticate with a personal access token and one workspace

A Drime mount SHALL require an `access_token` and MUST validate it against the provider before the
mount becomes usable; a rejected token MUST surface as an authentication error and MUST NOT open the
mount. `workspace_id` SHALL select one workspace (`0` is the personal space), MUST default to `0`,
and MUST be applied to every provider call that accepts a workspace context. A mount MUST expose
exactly one workspace; workspaces MUST NOT be projected as directories. `root_path` SHALL be resolved
inside the selected workspace during initialization, and `root_id` SHALL take precedence when set.
An unresolved root MUST fail initialization. A resolved root whose reported workspace does not match
`workspace_id` MUST also fail initialization, because the provider answers a mismatched workspace
with an empty listing rather than an error. Retried initialization MUST be idempotent and MUST NOT
fall back to the account root after a failed `root_path` resolution.

#### Scenario: Personal workspace is mounted

- **WHEN** a mount declares a valid token and omits `workspace_id`
- **THEN** listing and writes operate on the personal space and the mount reports workspace `0`

#### Scenario: Invalid token is rejected

- **WHEN** the provider answers the credential validation with `401`
- **THEN** initialization fails with an authentication error and no remote directory is served

#### Scenario: Root path is resolved inside the workspace

- **WHEN** a mount sets `workspace_id` to a team workspace and `root_path` to an existing folder
- **THEN** the mount root is that folder and all paths are relative to it, not to the workspace root

#### Scenario: Missing root path does not fall back

- **WHEN** `root_path` names a folder that does not exist
- **THEN** initialization fails instead of mounting the account root, including on a retry

#### Scenario: Root belongs to another workspace

- **WHEN** the resolved root entry reports a workspace different from the mount's `workspace_id`
- **THEN** initialization fails instead of mounting an empty directory

#### Scenario: Workspace contexts are isolated

- **WHEN** a folder from one workspace is listed with another workspace's context
- **THEN** the provider returns no entries, and the driver does not treat that as a valid empty folder during root resolution

### Requirement: Listings are complete, deterministic, and bounded

A directory listing SHALL return every direct child of the requested folder and MUST NOT substitute
the account root for an unknown or deleted folder. Listings MUST use the folder identity filter, a
page size no larger than the provider maximum, and a deterministic name-ascending order. Paging MUST
terminate only on an empty page, and the driver MUST fail with an error rather than return a partial
listing when the provider repeats a page, returns duplicate identities, or exceeds the provider's
20,000-entry listing ceiling. Directory sizes MUST be reported as zero; file sizes MUST be the
provider's byte count. Modification time MUST come from the client modification timestamp when
present and from the server update timestamp otherwise.

#### Scenario: Directory spans several pages

- **WHEN** a folder contains more entries than one provider page
- **THEN** the union of pages contains each entry exactly once and no entry is missing

#### Scenario: Page past the end

- **WHEN** paging reaches an empty page, even if the provider reports a bogus total or last page
- **THEN** listing stops successfully with the entries already collected

#### Scenario: Stale or unknown folder

- **WHEN** listing is requested for a folder id that was deleted or never existed
- **THEN** the driver returns a not-found error and never returns account-root entries

#### Scenario: Listing ceiling is reached

- **WHEN** a directory cannot be listed completely because the provider caps the result set
- **THEN** the driver returns an explicit error and does not report the truncated set as complete

### Requirement: Reads honor byte ranges and provider redirects

Reading a file SHALL request the provider's content endpoint for that entry and MUST preserve the
requested byte window across redirects. A `206` response SHALL return exactly the requested window.
A `416` for an offset at or past a known size SHALL return an empty reader; other `416` responses
MUST be errors. When the provider ignores the range and returns `200`, the driver MUST skip to the
requested offset and cap the returned length. Authentication, not-found, and rate-limit responses
MUST map to qrypt's stable error categories. The driver MUST NOT need an extra metadata request per
read when the entry identity is available.

#### Scenario: Partial read across a redirect

- **WHEN** a caller reads a middle window of a file and the content endpoint redirects to signed storage
- **THEN** the driver follows the redirect with the same range and returns exactly those bytes

#### Scenario: Offset at end of file

- **WHEN** a caller reads at an offset equal to or beyond a known file size
- **THEN** the driver returns an empty reader rather than an error

#### Scenario: Provider ignores the range

- **WHEN** the content endpoint answers `200` with the whole object instead of `206`
- **THEN** the driver discards bytes before the offset and returns at most the requested length

#### Scenario: Content no longer exists

- **WHEN** the entry was deleted and the content endpoint answers not found
- **THEN** the read fails with a not-found error

### Requirement: Writes preserve identity and report conflicts

Creating a directory SHALL return the existing directory when one with the same name exists, and
MUST report a conflict when a non-directory occupies the name. Renaming and moving SHALL preserve the
provider entry identity and SHALL return the updated entry. Deleting SHALL remove the entry from
listings and MUST map an already-missing entry to not found; the default SHALL move the entry to the
provider trash, with an explicit permanent-delete option. Server-side copy SHALL produce a
content-identical entry with the requested name. After renaming or moving a directory, the driver
MUST make the directory's children observable before reporting success, within a bounded wait.

#### Scenario: Directory already exists

- **WHEN** a caller creates a directory whose name already exists as a directory
- **THEN** the existing entry is returned and no duplicate is created

#### Scenario: Name collides with a file

- **WHEN** a caller creates a directory whose name is occupied by a file
- **THEN** the driver reports a conflict instead of returning the file

#### Scenario: Rename a directory

- **WHEN** a directory with children is renamed
- **THEN** the renamed directory lists its children before the operation reports success

#### Scenario: Delete an already-missing entry

- **WHEN** a caller deletes an entry that no longer exists
- **THEN** the driver reports not found and does not create a new entry

#### Scenario: Copy over a name collision

- **WHEN** a server-side copy is requested and the destination directory already has the name
- **THEN** the returned entry has exactly the requested name, or the operation fails

### Requirement: Uploads preserve metadata and resume after interruption

Uploads below the provider's single-request limit SHALL use one upload request. Larger uploads SHALL
use resumable multipart with parts of at least the provider minimum and at most the provider's part
count, choosing a chunk size that keeps the file under that count. Multipart uploads MUST sign each
part, reuse the provider's returned ETags exactly, finalize before creating the entry, and MUST NOT
commit an entry when any part fails. An interrupted upload SHALL resume from the provider's uploaded
part list instead of restarting; a stale or already-completed provider handle MUST be discarded and
the upload restarted once. Every upload MUST send the source SHA-256 and modification time when the
provider accepts them, and the returned entry MUST reflect the exact byte size, requested name,
content hash, and modification time. Names longer than the provider's 255-byte limit, names with leading or trailing
whitespace, and names containing a backslash MUST be rejected before upload rather than accepted
with silent truncation or rewriting. Progress MUST be reported per uploaded
byte and phase.

#### Scenario: Small file uploads in one request

- **WHEN** a file below the single-request limit is uploaded with a hash and modification time
- **THEN** the resulting entry has the same size, name, content hash, and modification time

#### Scenario: Large file resumes after interruption

- **WHEN** a multipart upload is interrupted after some parts were accepted and is retried
- **THEN** already-uploaded parts are skipped and the completed entry is content-identical

#### Scenario: Stale multipart handle

- **WHEN** the provider no longer recognizes a stored multipart upload
- **THEN** the driver discards it, starts a fresh upload once, and succeeds

#### Scenario: Overlong name is rejected

- **WHEN** an upload name exceeds 255 bytes
- **THEN** the driver rejects the upload before sending it and does not rely on provider truncation

#### Scenario: Progress is observable

- **WHEN** a multipart upload transfers parts
- **THEN** the driver reports the uploading phase and accumulates uploaded bytes monotonically

### Requirement: Content hashes are explicit and not over-trusted

The driver SHALL require a SHA-256 hash for uploads and SHALL send it with every upload. It SHALL
report a remote hash only for entries whose provider hash is a 64-character hexadecimal SHA-256, and
MUST report the hash as unavailable otherwise. It MUST NOT perform hash-based instant upload or reuse
because the provider accepts a client-declared hash without verifying the stored bytes; content
verification, when needed, SHALL be an explicit operation rather than an implicit upload shortcut.

#### Scenario: Uploaded file exposes its SHA-256

- **WHEN** a file is uploaded and later listed
- **THEN** its remote hash is the SHA-256 that was sent at upload time

#### Scenario: Provider hash is missing or not SHA-256

- **WHEN** an entry has no content hash, or a non-SHA-256 value such as an MD5
- **THEN** remote hash is reported as unavailable rather than converted or guessed

#### Scenario: Duplicate content does not trigger an instant upload

- **WHEN** the provider already stores an entry with the same declared hash
- **THEN** qrypt still uploads the bytes instead of duplicating the existing entry

### Requirement: Share links are managed per entry and listing degrades explicitly

The driver SHALL manage share links for existing entries: create a link, read it back, update its
passcode, expiry, download permission, or edit permission, and delete it. A link MUST cover exactly
one entry; a request for several entries MUST fail with an unsupported-share error instead of
publishing only the first. Link titles, visit limits, and a public/private toggle MUST be rejected as
unsupported because the provider has no equivalent. Expiry MUST support permanent, one day, one week,
and one month. The driver SHALL NOT advertise an account-wide link list: because the provider has no
listing endpoint, listing MUST return an unsupported-share error rather than an empty page, and the
rest of link management MUST remain available.

#### Scenario: Create a link for one entry

- **WHEN** a caller creates a link for one file with a passcode and a one-week expiry
- **THEN** the returned link has a stable link identity and URL, the passcode is accepted, and the expiry is one week from creation

#### Scenario: Multi-entry create is rejected

- **WHEN** a caller asks for one link covering several entries
- **THEN** the driver returns an unsupported-share error and publishes no partial link

#### Scenario: Unsupported link options are rejected

- **WHEN** a caller requests a title, a visit limit, or a public/private change
- **THEN** the driver returns an unsupported-share error before calling the provider

#### Scenario: Update and remove a link

- **WHEN** a caller updates a link's passcode or expiry and later removes the link
- **THEN** the changes are visible on the provider and the removed link no longer resolves

#### Scenario: Listing links degrades

- **WHEN** a caller asks the Drime mount to list its account-wide links
- **THEN** the driver reports the list operation as unsupported and other share operations remain usable

### Requirement: Share links can be opened, browsed, and fetched

The driver SHALL parse the provider's public share URLs and open a link without requiring the acting
account to own it. A link with a passcode MUST report that a passcode is required when none is
supplied and MUST report a wrong passcode as such. Missing, expired, revoked, and inaccessible links
MUST map to their stable share errors. The driver SHALL list the share root and any share subfolder
with entry names, directory flags, sizes, modification times, and stable share-relative identities,
paginating until the last page. It SHALL produce one direct download target per selected file, in
request order, and MUST reject a link or entry that does not allow download. Save-to-drive SHALL NOT
be advertised.

#### Scenario: Open a public link without a passcode

- **WHEN** a caller opens a public link that has no passcode
- **THEN** the driver returns a session that can list and download its content

#### Scenario: Passcode is required or wrong

- **WHEN** a link requires a passcode and the caller supplies none or supplies a wrong one
- **THEN** the driver reports passcode-required or passcode-wrong respectively, before creating a task

#### Scenario: Browse root and subfolder

- **WHEN** a caller lists a shared folder with more entries than one page and then a nested folder
- **THEN** the pages contain every child exactly once and the nested folder lists its own children

#### Scenario: Download a shared file

- **WHEN** a caller fetches a selected file from a link that allows download
- **THEN** the driver returns one download target for that file with its size

#### Scenario: Download is disallowed

- **WHEN** a link or entry does not permit download
- **THEN** the driver refuses the fetch with an unsupported-share error instead of returning a URL

#### Scenario: Save-to-drive is unsupported

- **WHEN** a caller asks to save share content into a Drime mount
- **THEN** the driver reports the save capability as unsupported

### Requirement: Capabilities and observability match implemented behavior

The driver SHALL declare exactly `writer`, `source_uploader`, `resumable_uploader`, `space`,
`path_resolver`, `mtime`, `server_side_copy`, `share`, `share_receive`, and `share_fetch`, and MUST
NOT declare `share_save`, vault, offline download, or foreign entries. Space reporting SHALL derive
total and free bytes from the provider's used/available figures for the selected workspace. The debug
snapshot MUST report the driver name, root identity, workspace, health, counters, and a redacted last
error, and MUST NOT contain the access token, signed URLs, or share passcodes. Metrics MUST be emitted
in the normalized driver event format.

#### Scenario: Capability list is exact

- **WHEN** the driver reports its capabilities
- **THEN** the list contains exactly the ten declared capabilities, including share, share receive, and share fetch, and no save or offline capability

#### Scenario: Space is scoped to the workspace

- **WHEN** space is queried on a workspace mount
- **THEN** total and free bytes reflect that workspace's usage

#### Scenario: Debug output is safe

- **WHEN** a debug snapshot is collected after a failed request or a share operation
- **THEN** it contains no access token, authorization header, signed storage URL, or share passcode
