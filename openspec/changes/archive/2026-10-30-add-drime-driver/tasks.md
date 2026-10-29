## 1. Driver skeleton and configuration

- [x] 1.1 Add `pkg/drivers/drime` with `Options`, `Driver`, and `init()`/`drive.Register("drime", ...)`; declare `access_token` (secret, required), `workspace_id` (int, default `0`), `root_path`, `root_id`, `api_base_url`, `chunk_size` (MiB, default `64`, minimum `5`), and `hard_delete` (bool, default `false`); add the blank import in `pkg/drivers/all/all.go`. Verify `pkg/config` validation accepts a complete mount and rejects a missing token and a bad chunk size.
- [x] 1.2 Implement the HTTP client: base URL, bearer auth, user agent, JSON decode, 401 → `drive.ErrAuth`, 404 → `drive.ErrNotFound`, 429 → `drive.ErrRateLimit`, 422 → `drive.ErrInvalidInput`, retryable 5xx/transport retries, and `drive.HTTPError` redaction. Verify an `httptest` server covers each status and that no token appears in an error string.
- [x] 1.3 Implement `Init`/`Drop`: validate the token with the account endpoint, resolve `root_path` inside `workspace_id`, verify the resolved entry's `workspace_id` matches the mount, honor `root_id`, commit the root only after success, and make retries idempotent. Verify a fake provider validates the `workspaceId` query, resolves a two-level path, rejects a root from another workspace, and fails a missing root without falling back to the account root.
- [x] 1.4 Implement `Space` from the workspace usage endpoint (`Total = used + available`, `Free = available`). Verify the fake returns a scoped total/free pair and an unsupported result when the endpoint is missing.

## 2. Listing and entry mapping

- [x] 2.1 Implement directory listing with `folderId`, `perPage=100`, `page`, `orderBy=name`, `orderDir=asc`; stop on an empty page; deduplicate by id; fail if a full page contains only already-seen ids; cap at 200 pages and return an error at the cap. Verify fakes for a 105-entry directory across two pages, a bogus `total`/`last_page` past the end, a repeated page, duplicate ids, a wrong workspace context returning an empty `200`, and the page cap.
- [x] 2.2 Map provider entries to `drive.Entry`: numeric id, parent id (`""` for root), basename, `IsDir` from `type=folder`, size `0` for directories, `ModTime` from `client_last_modified` else `updated_at`, and `Extra` carrying `hash`, `file_hash`, and `url`. Verify table tests include a folder, a file with and without `client_last_modified`, and a null hash.
- [x] 2.3 Reject unknown/deleted roots: `folderId` must return not found and must never fall back to the account root. Verify the fake returns `404` for a deleted folder id and the driver surfaces `drive.ErrNotFound`.
- [x] 2.4 Implement `Mkdir` by resolving the parent and matching the exact name: return the existing directory, return a conflict for a same-name file, and create the folder with the workspace query. Verify fakes for create, existing directory, file conflict, and invalid name.

## 3. Read path

- [x] 3.1 Implement `Read`: validate ranges, return an empty reader at/past a known size and for zero-length files, construct the content URL from the entry id when `Extra` is absent, follow redirects up to a bounded hop count, preserve `Range`, return `206` bodies, treat `416` at/past EOF as EOF, and lazily skip/cap a `200` that ignored the range. Verify a fake that redirects to a range-capable endpoint, one that ignores `Range`, one that returns `416`, and a missing token.
- [x] 3.2 Apply the shared bandwidth limiter to download bodies when one is installed. Verify a limiter records the download direction and that removing it restores raw reads.
- [x] 3.3 Ensure reads do not fetch entry metadata per call. Verify a counting fake records zero `GET /file-entries/{id}/model` calls for a normal ranged read and one call only when the entry carries no usable URL/hash.

## 4. Write path

- [x] 4.1 Implement `Rename` with literal `PUT /file-entries/{id}` plus `workspaceId`, normal retries, and the returned entry; do not send `X-HTTP-Method-Override`. Verify a fake rejects the override request and accepts `PUT`.
- [x] 4.2 Implement `Move` with `entryIds` + `destinationId` + `workspaceId`; update the returned entry. For a directory, count children before the move and poll the destination briefly (short backoff, ~5 s bound) until the same count is visible before returning. Verify fakes for a file move, a directory move with a delayed child listing, and a move that never settles (returns success after the bound but logs).
- [x] 4.3 Implement `Remove` with `deleteForever=false` by default and `true` when `hard_delete=true`; map a `422` carrying `errors.entryIds` to `drive.ErrNotFound`; never parse the localized message. Verify trash, permanent delete, already-missing entry, and folder cascade.
- [x] 4.4 Implement `Copy` through `/file-entries/duplicate` plus a rename when the provider returns `name (1)`; return the final entry and preserve `client_last_modified`/`file_hash`. Verify a same-folder collision, a clean destination, and a rename failure.
- [x] 4.5 Implement `ResolvePath` by walking path segments with `folderId` from the resolved root, and expose `path_resolver`. Verify `/`, nested paths, a missing segment, and a path with `..`/empty segments.

## 5. Upload and resume

- [x] 5.1 Implement the single-request lane for files below 5 MiB: multipart form with `file`, `parentId`, `workspaceId`, `lastModified`, and `fileHash`; report progress and return the created entry. Verify a fake covers success, 422, a zero-length file, and a name at the 255-byte boundary.
- [x] 5.2 Implement the multipart lane for files at/above 5 MiB: create with filename/mime/size/extension/parent/workspace, choose a chunk size of at least 5 MiB that keeps parts ≤10,000, sign each part, PUT to R2 without the bearer token, capture quoted ETags, complete in part order, then create the entry with the UUID key segment as `filename`. Verify a fake asserts every request field and the exact complete body.
- [x] 5.3 Add durable upload sessions: bind `session.Identity{ParentID, Name, Size, Fingerprint}`, persist `key`, `uploadId`, `partSize`, and the target identity, update/delete the binding around provider calls, and run the expirer. Verify resume, a stale handle restart, commit cleanup, and `Drop` flush.
- [x] 5.4 Recover progress through `get-uploaded-parts`: skip parts the provider reports, re-sign missing parts, and accept ETags from the provider rather than local MD5s. Verify a fake that reports parts 1 and 3 and expects only part 2 to be uploaded.
- [x] 5.5 Reject invalid names before any request: empty, `/`, and names longer than 255 bytes. Verify a 256-byte name fails without an HTTP call, while a 255-byte name succeeds.
- [x] 5.6 Report phases and monotonic byte progress for both lanes; report `instant` never. Verify a recording progress sink sees `uploading` and cumulative bytes equal to the source size.

## 6. Hashes, copy semantics, and observability

- [x] 6.1 Return `[]HashAlgorithm{HashSHA256}` from `RequiredUploadHashes`, send the hash on both upload lanes, and implement `RemoteHash` only for 64-character hexadecimal `file_hash` values; return `drive.ErrUnsupported` for null, MD5, malformed, and directory entries. Verify table tests.
- [x] 6.2 Ensure no hash-based instant upload path exists: a hash-check match must not skip the upload. Verify a fake that reports an existing hash still receives the full upload and no `instant` phase is reported.
- [x] 6.3 Declare exactly `writer`, `source_uploader`, `resumable_uploader`, `space`, `path_resolver`, `mtime`, `server_side_copy`, `share`, `share_receive`, `share_fetch`; verify `drive.CheckUnsupportedCapabilities` rejects `share_save`, vault, and offline calls.
- [x] 6.4 Implement `DebugSnapshot` with driver name, root id, workspace, health, counters, and a redacted last error; implement `Metrics` through the shared driver utility. Verify a failure snapshot contains no token, authorization header, signed URL, or share passcode, and that metric names normalize.

## 7. Share links, receive, and fetch

- [x] 7.1 Declare `CapabilityShare` with a policy of optional passcode, the neutral expiry set, update enabled, selection enabled, and list disabled. Implement `CreateShare`, `GetShare`, `UpdateShare`, and `DeleteShare` for exactly one entry; reject multi-entry requests, titles, visit limits, and public/private changes with `ShareErrUnsupported`. Verify fake requests carry passcode/expiry/download/edit fields and that update/delete resolve the entry id.
- [x] 7.2 Implement `ListShares` as `ShareErrUnsupported` and add a core-level test proving `ListShareLinks` fails before the driver call when the policy disables list, while create/show/update/remove still work.
- [x] 7.3 Implement `ParseShareLink` for `https://dri.me/{hash}` and `https://app.drime.cloud/drive/s/{hash}`, including an optional passcode. Implement `OpenShare` against the public link endpoint with no account token; map a missing passcode to `ShareErrPasscodeRequired`, a wrong passcode to `ShareErrPasscodeWrong`, and missing/expired/revoked links to their stable errors.
- [x] 7.4 Implement `ListShareEntries` for the root and subfolders using the link-hash and `linkHash:folderHash` identities, `withEntries=true`, updated-at ordering, and a numeric page cursor; map names, directory flags, sizes, mtimes, and share-relative entry hashes. Verify a multi-page folder and a nested folder.
- [x] 7.5 Implement `ShareDownloadTargets` with one anonymous download URL per selected file, preserving request order and size, and reject links/entries that disallow download. Verify a fake 302 target, a disallowed link, and a password-protected link.
- [x] 7.6 Add fake-provider tests for share create/get/update/delete, passcode resolution, list degradation, folder pagination, download targets, and redaction of passcodes and query strings.

## 8. Registration, schema, and documentation

- [x] 8.1 Add `drime` to `schema/qrypt.schema.json`: mount `type` enum, `definitions.drimeParams` with English and `zh-CN` descriptions, and the `if/then` mount block. Regenerate `pkg/drive/builtin_schema_gen.go` and `internal/cli/i18n/catalog_gen.go`; verify a second generation is a no-op.
- [x] 8.2 Add `DRIVER_META`/`DRIVER_ORDER` for `drime` in `scripts/schema/gen-driver-docs.py`, regenerate `docs/for-user/support-drivers.md` and `docs/for-user/full-config.md`, and verify the generated tables match the schema.
- [x] 8.3 Add the `drime` entry with the exact capability list to `pkg/drivers/capability_test.go`. Verify the capability contract test passes.
- [x] 8.4 Add a real-account Drime procedure to `docs/for-developer/quality-gates.md`: token prerequisites, token scopes, the upload sizes to test, the folder-settle check, cleanup, and the team-workspace check.

## 9. Verification and release gate

- [x] 9.1 Run focused unit tests for `pkg/drivers/drime`, `pkg/drivers`, and affected config/schema packages; run the driver package with `-race`.
- [x] 9.2 Run the live gate on a real account: auth, root and nested listing, a >100-entry folder, range reads (start/middle/end/past EOF), mkdir/rename/move/delete/restore, small upload with mtime+hash, ≥5 MiB multipart upload, interrupted resume, long-name rejection, and cleanup. Record each result and any residual object.
- [x] 9.3 Use `TestWorkSpace` (or another team workspace) to repeat root resolution, listing, upload, multipart, rename, move, copy, and space; verify every created entry reports the workspace id and that a folder listed with the wrong workspace context is empty. Record non-owner behavior if a shared-member account is available.
- [x] 9.4 Create a file link and a folder link, verify create/show/update/remove, confirm `share list` returns an explicit unsupported error, then open both links anonymously, browse a nested folder, test a wrong and a correct passcode, and fetch one file. Confirm the downloaded bytes match and clean up the links and entries.
- [x] 9.5 Probe name edge cases (leading/trailing space, backslash, multi-byte 255-byte boundary) and either reject or normalize them deterministically; add the resulting rule to the spec and tests.
- [x] 9.6 Run `openspec validate add-drime-driver --strict`, `gofmt -w`/`gofmt -l` on changed Go files, `git diff --check`, and `scripts/ci/check-all.sh` with the CI Go version; confirm the local CI marker.
