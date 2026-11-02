## 1. Driver skeleton and configuration

- [x] 1.1 Add `pkg/drivers/koofr` with `Options`, `Driver`, and `init()`/`drive.Register("koofr", ...)`;
  declare `email` and `password` (secret, required, credential scope), `mount_id` (optional,
  defaults to the primary mount), `root_path` (optional), and `api_base_url` (default
  `https://app.koofr.net`); add the blank import in `pkg/drivers/all/all.go`. Verify `pkg/config`
  validation accepts a complete credential + mount pair and rejects a missing email or password.
- [x] 1.2 Implement the HTTP client: Basic auth, JSON encode/decode, status→sentinel mapping
  (`401` auth, `404` not found, `409 AlreadyExists|Conflict` conflict, `400 InvalidPath` invalid
  input, `429` rate limit), tolerant parsing of non-JSON bodies, `requestId` capture,
  `FilesNotSynced` as a non-retryable provider-state error, 5xx/transport retries, and
  `drive.HTTPError` redaction. Verify an `httptest` server covers each status and that no password
  appears in an error string.
- [x] 1.3 Implement `Init`/`Drop`: validate credentials via `GET /user`, resolve `mount_id` via
  `GET /mounts` (default `isPrimary`), reject offline / not-writable / `FilesNotSynced` mounts,
  resolve `root_path` via `files/info`, commit the root only after success, and make retries
  idempotent. Verify fakes for primary discovery, an explicit mount id, an offline mount, a missing
  root path, and a rejected credential.
- [x] 1.4 Implement `Space` from `GET /mounts/{id}` (`spaceTotal`/`spaceUsed` are MiB; multiply to
  bytes). Verify the fake returns MiB figures and the driver reports bytes.

## 2. Listing and entry mapping

- [x] 2.1 Implement directory listing via `GET /api/v2.1/mounts/{id}/files/list?path=` with
  absolute non-empty paths (an empty path silently means the root). Verify fakes for a mixed
  directory, an empty directory, a missing path (`404`), and a guard that rejects an empty path
  before the request.
- [x] 2.2 Map provider entries to `drive.Entry`: path-derived `ID`/`ParentID`, basename, `IsDir`
  from `type=dir`, directory size flattened to `0`, ms `modified`, and MD5 kept in `Extra`. Verify
  table tests include a directory, a file, and an entry without a hash.
- [x] 2.3 Implement `ResolvePath` by walking segments and `files/info`; expose `path_resolver`.
  Verify `/`, nested paths, a missing segment, and `..`/empty-segment normalization.
- [x] 2.4 Implement `ResolveRemoteName` with case-insensitive matching against the listing and the
  provider's canonical casing; expose `remote_name_resolver`. Verify that resolving `A.BIN` on a
  directory containing `a.bin` returns `a.bin`'s identity, and that an absent name reports
  not-found rather than creating one.
- [x] 2.5 Add a `listrecursive` (content prefix, NDJSON) helper for full scans with paths relative
  to the request root. Verify the fake streams nested entries and that the driver stops on the
  first malformed line with an error.

## 3. Read path

- [x] 3.1 Implement `Read` over `GET /content/api/v2.1/mounts/{id}/files/get?path=`: validate
  ranges, return an empty reader at/past a known size, return `206` bodies, map `416` at/past EOF
  to an empty reader and other `416`s to errors, and lazily skip/cap a `200` that ignored the
  range. Verify fakes for `206` exactness, `416` in-range vs past-EOF, an ignored range, and
  `404`.
- [x] 3.2 Apply the shared bandwidth limiter to download bodies when installed. Verify the limiter
  records the direction and that reads work without one.
- [x] 3.3 Ensure reads issue no metadata request. Verify a counting fake records zero `files/info`
  calls for a ranged read.

## 4. Write path

- [x] 4.1 Implement `Mkdir`: `POST files/folder`; map `409 AlreadyExists` to the existing
  directory (or a conflict when a file holds the name) and the plain-text `500 Name is required`
  shape to invalid input; create level-by-level for nested paths. Verify fakes for create,
  existing directory, file conflict, invalid name, and a two-level path.
- [x] 4.2 Implement `Rename` via `PUT files/rename`; map `409 AlreadyExists` (including
  case-variant targets) to conflict. Verify rename success, a case-variant collision, and a
  missing source.
- [x] 4.3 Implement `Move` via `PUT files/move` with `{toMountId, toPath}` pinned to the mounted
  mount. Verify a file move, a directory move, a destination conflict, and a rejection of a
  cross-mount destination.
- [x] 4.4 Implement `Copy` (`server_side_copy`) via `PUT files/copy`, sending `modified` from the
  source entry so mtime survives. Verify copy success with mtime preservation, a destination
  conflict (`409 AlreadyExists`), and a missing source.
- [x] 4.5 Implement `Remove` via `DELETE files/remove` with `removeIfSize`/`removeIfModified`/
  `removeIfHash` as a CAS guard; map `409 Conflict` to conflict and `404` to not found; document
  and test that a non-empty directory cascades into the trash. Verify conditional success,
  conditional mismatch, missing entry, and directory cascade.

## 5. Uploads

- [x] 5.1 Implement `PutSource` as one `POST /content/api/v2.1/mounts/{id}/files/put` multipart
  request: field `file`, query `filename`, `info=true`, `overwrite=true`, `autorename=false`, and
  `modified=<ms>` derived from the source ModTime on every upload. Verify a fake asserts all five
  query decisions on every put, returns the created entry with size/hash/mtime, and that an
  overwrite preserves the requested mtime.
- [x] 5.2 Validate names before any request: non-empty, no `/`, no NUL, no backslash, valid UTF-8,
  bounded length. Verify a name with a backslash, invalid UTF-8, or excessive length fails without
  an HTTP call, mirroring the probe's `400 InvalidPath` and the part-filename fallback hazard.
- [x] 5.3 Create missing parent directories level-by-level, treating `409 AlreadyExists` as
  success. Verify a two-level parent creation and a concurrent-style duplicate create.
- [x] 5.4 Report progress per byte for the single request; never report an `instant` phase. Verify
  a recording sink sees cumulative bytes equal to the source size.
- [x] 5.5 Implement `RemoteHash` exposing MD5 for 32-hex provider hashes and `ErrUnsupported`
  otherwise; `RequiredUploadHashes` returns none. Verify table tests for MD5, missing, and
  malformed hashes.

## 6. Observability

- [x] 6.1 Implement `DebugSnapshot` with driver name, base URL, mount id/name, root path, health,
  counters, and a redacted last error; implement `Metrics` through the shared driver utility.
  Verify a failure snapshot contains no password, authorization header, share passcode, or
  password/set response body, and that metric names normalize.
- [x] 6.2 Thread provider `requestId` values into error diagnostics and counters. Verify a fake
  error response's requestId appears in the mapped error and the snapshot.

## 7. Share links, receive, and fetch

- [x] 7.1 Declare `CapabilityShare` with a policy of optional passcode, provider-supported expiry
  windows, download toggle, and list enabled. Implement `CreateShare`, `GetShare`, `UpdateShare`,
  and `DeleteShare` over the mount-scoped endpoints `POST/GET/PUT/DELETE
  /api/v2.1/mounts/{id}/links[/{linkId}]`, plus `password/set` and `validity`. Verify fakes for
  create (`201` shape), list (mount and account-wide `GET /links`), password set, validity update,
  delete (`204`), and rejection of titles, visit limits, and passcode removal (the provider's
  `password/reset` generates a random password instead of clearing it).
- [x] 7.2 Never persist or log provider responses that echo the share password. Verify a fake
  `password/set` response containing the plaintext passcode is absent from errors, logs, and
  debug snapshots.
- [x] 7.3 Implement `ShareLinkParser` for `https://app.koofr.net/links/{id}` and
  `https://k00.fr/{hash}` (resolving the short URL's redirect to the link id). Verify both forms
  and a non-Koofr URL.
- [x] 7.4 Implement `ShareDownloadTargets` producing one anonymous target per selected file via
  `GET {base}/content/links/{linkId}/files/get?path=<relative>`; reject directory paths (they
  return a zip) and non-downloadable links. Verify the fake serves exact bytes for a root-level
  and a nested file, a zip for a directory path, and an error for a disallowed link.
- [x] 7.5 Implement `ShareReceiver` bounded to what the provider's public surface allows; a
  passcode-protected link without a passcode maps to passcode-required. Verify the fake covers the
  open and passcode-required paths and the live gate confirms real behavior (see design Open
  Questions).
- [x] 7.6 Do not implement or declare `ShareSaver`. Verify `drive.CheckUnsupportedCapabilities`
  rejects a save request.

## 8. Registration, schema, and documentation

- [x] 8.1 Add `koofr` to `schema/qrypt.schema.json`: mount `type` enum, credentials `type` enum,
  `definitions.koofrParams` with English and `zh-CN` descriptions, and the `if/then` blocks.
  Regenerate `pkg/drive/builtin_schema_gen.go` and `internal/cli/i18n/catalog_gen.go`; verify a
  second generation is a no-op.
- [x] 8.2 Add driver metadata/order for `koofr` in `scripts/schema/gen-driver-docs.py`; regenerate
  `docs/for-user/support-drivers.md` and `docs/for-user/full-config.md`; verify the generated
  tables match the schema.
- [x] 8.3 Add the `koofr` entry with the exact capability list to
  `pkg/drivers/capability_test.go`. Verify the capability contract test passes.
- [x] 8.4 Add a real-account Koofr procedure to `docs/for-developer/quality-gates.md`: app-password
  prerequisites, primary-mount scope, upload mtime checks, share lifecycle checks, cleanup, and the
  external-mount caveat.

## 9. Verification and release gate

- [x] 9.1 Run focused unit tests for `pkg/drivers/koofr`, `pkg/drivers`, and affected
  config/schema packages; run the driver package with `-race`.
- [x] 9.2 Run the live gate on the real account (env-gated `KOOFR_LIVE_*`): auth, mount discovery,
  root and nested listing, range reads (start/middle/end/past EOF), mkdir 409 idempotency,
  rename/move/copy with conflicts, conditional delete, upload with mtime round-trip and overwrite
  mtime preservation, share create/list/password/validity/delete with redaction checks, anonymous
  download of a root-level and a nested file, and full cleanup including the trash. Record
  residuals.
- [x] 9.3 Probe name edge cases live (case-variant collision, backslash, overlength, non-UTF-8)
  and confirm the driver rejects them before any request.
- [x] 9.4 Record the 429 and password-protected-anonymous-fetch behaviors if they occur naturally;
  do not synthesize link floods against the real account.
- [x] 9.5 Run `openspec validate add-koofr-driver --strict`, `gofmt -w`/`gofmt -l` on changed Go
  files, `git diff --check`, and `scripts/ci/check-all.sh` with the CI Go version; confirm the
  local CI marker.
