## Context

See `proposal.md` for motivation and `specs/drime-driver/spec.md` for the behavior contract. qrypt's
driver contract lives in `pkg/drive`; the closest existing backend shapes are `pkg/drivers/onedrive`
(HTTP, chunked upload, session state) and `pkg/drivers/p123` (resumable upload bindings).

A live probe against a real Drime account (personal workspace, no team workspace available on the
test account) produced the facts this design depends on. The probe ran outside the repository; its
token and report are not committed. Key observations:

- `GET /drive/file-entries` uses `folderId`; both the numeric id and the encoded `hash` work, an
  invalid value returns `404`. `parentIds` accepts numeric ids but silently ignores an invalid
  string and returns the root listing, so it is not safe as the primary filter.
- `perPage` is clamped to 100; a 105-entry folder lists correctly on two pages. A page past the end
  returns empty data with an unreliable `total`/`last_page` (105 entries produced `total=200` on
  page 3). Default order is `updated_at desc`; `orderBy=name&orderDir=asc` was stable across pages.
- Folder `file_size` is the aggregate child size and must be flattened to 0.
- `entry.url` is `api/v1/file-entries/{id}`. A request with the bearer token returns `302` to R2;
  following it with `Range: bytes=10-19` returns `206` and the correct bytes. `bytes=0-0` and
  `bytes=99-` return `206`; `bytes=1000-` on a 100-byte file returns `416` with a 135-byte body and
  no `Content-Range`. `/file-entries/download/{hash}` behaves the same.
- Literal `PUT /file-entries/{id}` renames successfully. The rclone workaround
  (`POST` + `X-HTTP-Method-Override: PUT`) returns `405` here; it must not be used.
- A folder rename returns `200`, keeps its `hash`, and its children were visible on the first list
  about 1.4 s later. No empty-listing window was reproduced, but rclone reported one.
- A 304-byte name was accepted and stored truncated to 255 bytes, losing the extension.
- `fileHash` is stored as declared and is **not** verified against the bytes: a deliberately wrong
  SHA-256 was accepted and `hash/check` returned that entry. `verify-integrity` recomputes and
  replaces `file_hash` with the real SHA-256.
- `duplicate` preserves `file_hash` and `client_last_modified`, and names a collision `name (1)`.
- Multipart create/sign/put/complete/entries works; `get-uploaded-parts` returned part numbers,
  quoted ETags, and sizes after uploading parts 1 and 3, so resume is provider-driven.
- Trash and permanent delete both disappeared from listings immediately; `deletedOnly=true` lists
  the trash, restore works, and deleting a folder cascades to its children.
- The test account reported `X-Ratelimit-Limit: 10000`; a root listing took about 1.2 s.
- `TestWorkSpace` (a test workspace) is an owned workspace with `files.view/create/update/delete`. Folder
  creation, `/uploads`, multipart, rename, move, and duplicate all accepted `workspaceId=<id>` and
  returned entries whose `workspace_id` matched; nothing leaked into the personal root.
- Listing a workspace folder with the wrong workspace context returned an **empty `200`**, not an
  error. Metadata by entry id worked with and without `workspaceId`.
- Cross-workspace `duplicate` succeeded in both directions (`workspaceId=0` copied a workspace file
  into personal space; the workspace context copied a personal file into the workspace), with the
  resulting entry taking the destination workspace.
- A non-empty workspace folder deleted with `deleteForever=true` cascaded and marked both children.
  One personal folder delete returned success without `marked` while its child was still settling,
  and succeeded on a retry after the child was gone.
- Workspace space usage is separate (`used=174405`, `available=21474836480` in the probe), while
  `workspace_files` reported `files=0, size=0`; the stats endpoint is not a substitute for
  `space-usage`.
- Share links are per entry. There is no account-wide link list endpoint. The owner API can create,
  read, update, and delete the link for one entry; a link URL is `https://dri.me/{hash}` and resolves
  to `https://app.drime.cloud/drive/s/{hash}`.
- The public share API is usable without the account token:
  `GET /api/v1/shareable-links/{hash}?withEntries=true&page=1&order=updated_at:desc` returns the
  link plus one page of `folderChildren`; subfolders use
  `GET /api/v1/shareable-links/{hash}:{folderHash}?withEntries=true&page=N&order=updated_at:desc`.
  `GET /api/v1/folders/{folderHash}/path?shareable_link={linkID}` returns the breadcrumb.
- A passcode-protected link answers `403 {"passwordInvalid":true}` without a passcode and succeeds
  with `?password=...`; `POST /api/v1/shareable-links/{hash}/check-password` reports a match. An
  authenticated owner can read the same public endpoint without the passcode.
- A single shared file downloads anonymously with
  `GET /api/v1/file-entries/download/{entryHash}?shareable_link={linkID}&password=...`, which
  redirects to R2. Folder ZIP downloads use a separate `download-urls` token flow.
- The provider's `POST /api/v1/shareable-links/{hash}/import` endpoint exists for save-to-drive, but
  an owner importing their own link returned a server error; it is not part of v1.

## Goals / Non-Goals

**Goals:**

- Absorb every observed provider hazard inside the driver so VFS sees stable ids, complete
  listings, exact sizes, and normal range semantics.
- Keep the first version small enough to review: reads, writes, uploads/resume, space, mtime,
  SHA-256, server-side copy, per-entry link management, and share receive/fetch.
- Make the fake provider cover the same request/response shapes that the live probe observed.

**Non-Goals:**

- Account-wide share link listing, provider-side share save, vault, offline download, or browsing
  multiple workspaces in one mount.
- Hash-based instant upload and automatic `verify-integrity` passes.
- A provider-specific cache layer; qrypt's VFS already owns caching and staging.
- Changes to cross-cutting specs such as `upload-pipeline`; the driver conforms to them.

## Decisions

### One workspace per mount, not a virtual directory tree

`workspace_id` is a mount parameter defaulting to `0`; all calls that accept it carry it. A workspace
is not a folder in the provider, and composing `workspace/folder` ids would force a second identity
scheme through VFS. Alternative: project workspaces as top-level folders and encode composite ids.
Rejected because it complicates `Entry.ID`, `ResolvePath`, copy/move, and session identities for a
feature that a second mount already provides.

The probe showed that a wrong workspace context is not an error: listing a folder from workspace A
with workspace B returns an empty `200`. Initialization therefore resolves the root folder and
verifies the returned entry's `workspace_id`, failing on a mismatch instead of mounting an empty
directory. Cross-workspace `duplicate` works, but qrypt's copy is same-mount only, so the driver
treats the mount workspace as fixed.

### `root_path` and `root_id` follow the existing driver convention

`root_id` (a numeric folder id as a string) wins; otherwise `root_path` is resolved from the
workspace root during `Init`. The resolved id is committed only after success so a retried `Init`
does not silently mount the account root. `ResolvePath` walks segments with `folderId` listings.
`access_token` is credential-scoped, so it can live in a shared `[[credentials]]` entry and serve
several mounts. A personal access token is static (no refresh or rotation), so the driver needs no
runtime credential-session binding; the config layer merges the credential params before the
factory runs.

### Listing uses `folderId`, name ordering, and empty-page termination

Use `perPage=100`, `orderBy=name`, `orderDir=asc`, and increment `page` until `data` is empty.
Never trust `total` or `last_page` for termination because the provider returns inconsistent values
past the end. Deduplicate by id; if a full page contains only ids already seen, fail rather than
loop. Cap the walk at 200 pages (the provider's ~20,000-entry ceiling) and return an error when the
cap is hit. Alternative: `parentIds` — rejected because an invalid filter silently returns the
account root, which could make a deleted folder look like a full directory.

An empty page is only a valid end-of-directory when the request used the mount's workspace and a
verified root. A workspace mismatch also produces an empty `200`, so the driver must never conclude
"empty folder" from a listing whose workspace context it did not control.

### Read builds the content URL from the entry id

`entry.url` is deterministic (`api/v1/file-entries/{id}`), so `Read` constructs
`<download base>/api/v1/file-entries/<id>` when `Extra` lacks a URL. This avoids one metadata call per
read and survives `drivecopy` dropping `Extra`. The HTTP client follows up to a small number of
redirects, keeps `Range` on every hop, returns `206` bodies directly, treats `416` at/past EOF as an
empty reader, and lazily skips/caps a `200` that ignored the range. Alternative: resolve a signed URL
and cache it — rejected because the 302 is generated per request and caching adds expiry bugs for no
benefit.

### Rename uses literal `PUT`; directory moves get a bounded settle check

The probe shows literal `PUT` works and the method-override workaround returns `405`. On a directory
rename or move, list the children before and poll after (short backoff, ~5 s bound) until the same
count is observable, then return; this guards against the empty-listing window rclone observed
without penalizing file operations. Alternative: always trust the write response — rejected because
a transient empty listing is the kind of provider inconsistency that can make sync delete files.

### Delete defaults to the provider trash with an explicit permanent option

`Remove` calls `deleteForever=false` by default and `true` when `hard_delete=true`. The probe shows
both are immediately reflected in listings; trash-first is still useful for recoverability. A `422`
whose `errors.entryIds` is present maps to not found (the message is localized and must not be
parsed). A non-empty folder delete cascaded in the probe; if a directory delete returns success
without `marked`/`bytes`, the driver re-checks the parent once and retries the delete, because one
observation showed a success-without-mark while a child was still settling. Alternative: always
permanent-delete — rejected because it surprises users and loses data.

### Two upload lanes: single request below 5 MiB, multipart at or above it

Small files use `POST /uploads` (one request, supports `fileHash` and `lastModified`, no orphaned
storage object on failure). Larger files use `/s3/multipart/create` → per-part signing → R2 PUT →
`/s3/multipart/complete` → `/s3/entries`. Presigned single upload exists and works, but needs the
same `/s3/entries` step plus key parsing; it is not worth the extra failure mode for small files.
Multipart part size defaults to 64 MiB (`chunk_size`, MiB, minimum 5) and is grown automatically to
keep the part count at or below 10,000. A 64 MiB default keeps the number of signing round trips
low on a provider where each API call costs ~0.5–2 s; parts are read from the source with section
readers so the driver does not buffer 64 MiB per part. Alternative: rclone's 5 MiB default —
rejected as too many signing calls for large files.

### Resume state stores the provider handle, not a bitmap

The session token holds `key`, `uploadId`, `partSize`, and the target entry identity. On resume the
driver calls `get-uploaded-parts` and re-signs only the missing parts. A `404`/rejection from that
endpoint means the handle is dead; drop the binding and restart once. This is stronger than a local
confirmed-parts bitmap and matches qrypt's "provider progress endpoint" guidance.

### Hashes are declared, not trusted

`RequiredUploadHashes` returns SHA-256 and every upload sends `fileHash`. `RemoteHash` returns a hash
only when `file_hash` is 64 hex characters; null, MD5 (32 hex), or malformed values report
`ErrUnsupported`. No instant upload: the probe proved the provider stores a client-declared hash
without verifying content, so `hash/check` can point at an entry whose bytes do not match. The
alternative — verify the candidate with `verify-integrity` before duplicating — reads the whole file
server-side and defeats the purpose; it can be added later as an explicit operation.

### Reject overlong names instead of accepting truncation

Validate names in `Mkdir`, `Rename`, and `PutSource`: non-empty, no `/`, no NUL, no backslash, no
leading or trailing whitespace, at most 255 bytes. The provider silently truncates longer names (a
304-byte name became 255 bytes and lost its extension), trims leading/trailing spaces, and drops a
backslash from `back\slash.bin` to `slash.bin` (verified live); accepting those would desynchronize
local and remote paths.

### Error mapping uses status codes and structured fields

Use `drive.HTTPError` for non-2xx responses. Map `401` to `ErrAuth`, `404` to `ErrNotFound`, `429`
to `ErrRateLimit`, and `422` delete responses with `errors.entryIds` to `ErrNotFound`. Do not match
localized message text (the probe saw Chinese messages). Read `Retry-After` and the rate-limit headers
when present.

### Share links: manage one entry per link and degrade `list`

`CapabilityShare` is declared with `SharePolicy{Passcode: Optional, Expiries: NeutralShareExpiries(),
Update: true, List: false, Selection: true}`. `CreateShare` publishes a link for exactly one entry;
a multi-entry request is rejected with `ShareErrUnsupported` because the provider cannot attach one
link to several entries and silently picking the first would hide data. `Title` and `VisitLimit`
are rejected for the same reason: the provider has no such fields. `Expiry` maps to `expires_at`
(null for permanent), `Passcode` to `password`, and the download/edit flags follow qrypt's request.

The user-facing identity is the link hash (`Share.LinkID`); the owner's entry id is kept in
`Share.NativeID` for update and delete. `GetShare` accepts a hash through the public endpoint using
the account token (which bypasses the passcode for the owner) and a numeric entry id through the
owner endpoint. `UpdateShare` and `DeleteShare` resolve the entry id first. `ListShares` returns
`ShareErrUnsupported`; `SharePolicy.List=false` makes Core return that error before calling the
driver, so `qrypt share list` fails clearly instead of returning an empty page. Alternative: walk the
whole tree and query every entry's link — rejected as O(entries) calls at 0.5–2 s each and unable to
see links on inaccessible entries.

### Share receive and fetch use the public link API

`ParseShareLink` accepts `https://dri.me/{hash}` and
`https://app.drime.cloud/drive/s/{hash}`, preserving a `?pwd=` passcode. `OpenShare` calls the public
endpoint without the account token (so another user's link behaves the same as the owner's), maps
`403 passwordInvalid` to `ShareErrPasscodeRequired` when no passcode was supplied and
`ShareErrPasscodeWrong` otherwise, and maps missing/expired/revoked links to their stable share
errors. The session token is the opaque JSON `{hash, link_id, password}`; it is not persisted by
Core.

`ListShareEntries` uses `shareable-links/{hash}:{folderHash}` for subfolders and the bare link for
the root, always with `withEntries=true`, `order=updated_at:desc`, and a numeric page cursor. The
page size is the provider default; `Next` is set while `current_page < last_page`. Entry IDs are the
provider entry hashes so folder navigation and download use the same identity.

`ShareDownloadTargets` builds the anonymous
`file-entries/download/{entryHash}?shareable_link={linkID}&password=...` URL per entry. The caller
follows the 302 to R2; the target carries the entry size. A link whose `allow_download` is false, or
a file whose share permission lacks download, fails before a task is created. Folder ZIP tokens are
not used for `ShareFetch` because they change content identity. `ShareSave` is not declared in v1;
the import endpoint needs a separate investigation.

### Capabilities are the exact implemented set

`writer`, `source_uploader`, `resumable_uploader`, `space`, `path_resolver`, `mtime`,
`server_side_copy`, `share`, `share_receive`, `share_fetch`. `share_save` is absent because
save-to-drive is deferred; vault, offline download, and foreign entries are absent. `mtime` is
justified because `lastModified` round-trips on both upload lanes and `duplicate` preserves it.

## Risks / Trade-offs

- **Provider latency dominates** (about 0.5–2 s per API call; a listing took 1.2 s). Mitigation:
  avoid per-read metadata calls, use 64 MiB upload chunks, and keep the design open to an upload
  concurrency option later. Per-directory listing latency is inherent.
- **Listing instability or truncation** could make VFS treat entries as deleted. Mitigation:
  deterministic order, empty-page termination, duplicate detection, and a hard error at the 20,000
  ceiling instead of returning a short list.
- **`file_hash` is client-asserted**, so `RemoteHash` may be wrong for entries written by other
  clients. Mitigation: only expose 64-hex SHA-256, document that it is advisory, and disable instant
  upload. A future explicit verify operation can promote trust.
- **Directory rename/move may have a visibility window** that the probe did not reproduce.
  Mitigation: bounded pre/post listing check; if it proves unnecessary, it can be relaxed without
  changing the contract.
- **Name restrictions beyond 255 bytes are unprobed** (leading/trailing space, backslash). Mitigation:
  live-gate checks; reject invalid names in the driver rather than letting the server rewrite them.
- **A wrong workspace context silently returns empty listings**, not an error. Mitigation: carry the
  mount's workspace on every listing, verify the resolved root's `workspace_id`, and never retry a
  listing with another workspace.
- **Shared-member roles and restricted workspace permissions are unprobed** (the test workspace was
  owned with full permissions). Mitigation: map 403 to authorization errors, add a restricted-member
  live check before release, and never infer permissions from ownership.
- **Trash retains quota.** Mitigation: `hard_delete=true` for users who need space freed immediately.
- **Provider changes method behavior** (the rclone 520 workaround is already obsolete). Mitigation:
  literal `PUT` with normal retry; tests cover the request shape rather than pinning the workaround.
- **Public share endpoints are undocumented and may change.** Mitigation: isolate every public
  share call behind the driver client, cover them with fake tests, and keep the live share smoke
  test in the quality gate. If a public endpoint disappears, `share receive` fails without
  affecting list/read/write/upload.
- **Passcodes appear in query strings.** Mitigation: never log request URLs or query strings for
  share calls; redact them in errors and debug snapshots, and keep the passcode only in the
  in-memory session token.
- **Public share listing has no account-wide index.** Mitigation: `SharePolicy.List=false` and an
  explicit `ShareErrUnsupported`; do not emulate list by tree traversal.

## Migration Plan

New mount type only; no existing configs change. Ship schema and generated docs together so
`drime` is accepted by validation and visible in the support matrix. Rollback is removing the driver
registration and docs; existing Drime data is untouched.

## Open Questions

- Non-owner workspace roles and their 403 behavior need a shared-member account; the owner account
  used for the probe has full permissions.
- Whether names with leading/trailing spaces or backslashes are rejected, trimmed, or rewritten.
- The exact directory-listing ceiling in the current API version; the driver treats 20,000 as a hard
  error anyway.
- Whether `workspace_files` stats are eventually consistent or scoped differently; the driver uses
  `space-usage` for capacity and does not expose those stats.
- Whether `POST /shareable-links/{hash}/import` can serve `ShareSave` for non-owner links; the owner
  probe returned a server error, so save-to-drive stays out of v1.
