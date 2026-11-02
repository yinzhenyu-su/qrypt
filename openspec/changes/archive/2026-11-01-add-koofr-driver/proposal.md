## Why

Koofr is a provider qrypt does not yet support. It exposes a documented REST API (also reused by
Koofr-compatible services such as Digi Storage) with app-password Basic authentication, path-based
file operations, MD5 content hashes, upload-time mtime, server-side copy/move, a trash model, and
per-entry share links — a good fit for qrypt's driver contract. A live probe against a real
app.koofr.net account confirmed the routes and corrected several rclone-era assumptions (mkdir
conflicts return `409 AlreadyExists`, not `400`; the default upload behavior is auto-rename, not
conflict; invalid names are rejected, not truncated), so the driver can be specified against
observed behavior instead of folklore.

## What Changes

- Add a built-in `koofr` driver authenticated by app password (`email` + `password`, secret,
  credential scope) with an optional `mount_id` parameter (default: the primary mount discovered
  via `GET /mounts`) and `root_path`. The driver targets the primary hosted mount in v1; external
  mounts (e.g. connected Google Drive) are selectable but their behavior is only recorded, not
  guaranteed.
- Use Basic auth against `api_base_url` (default `https://app.koofr.net`) with the documented
  metadata prefix `/api/v2.1` and content prefix `/content/api/v2.1`. Listing uses the metadata
  prefix; uploads, downloads, and recursive listing use the content prefix; share-link sub-resources
  are mount-scoped (`/api/v2.1/mounts/{id}/links/{linkId}`), not account-scoped.
- Implement listing via `files/list` (path-based, case-insensitive names, MD5 hashes, ms
  timestamps) and entry mapping that flattens directory sizes to zero. `files/info` resolves single
  entries; `listrecursive` (content prefix, NDJSON stream) is available for full scans.
- Implement range reads over `files/get` with standard `Range` semantics: exact `206` windows and
  `416` for out-of-bounds windows.
- Implement writes with observed conflict semantics: `mkdir` maps `409 AlreadyExists` to the
  existing directory and rejects a same-name file; `rename`/`move` are path operations; `copy`
  (server-side) works within the mounted mount; deletion is conditional-CAS
  (`removeIfSize`/`removeIfModified`/`removeIfHash`, `409 Conflict` on mismatch) and cascades
  non-empty directories into the trash.
- Implement uploads as a single multipart request (`files/put`) with `filename`, `info=true`,
  `overwrite=true`, `autorename=false`, and `modified=<ms>` on every upload so overwrites preserve
  mtime (an overwrite without `modified` resets it to server time). Names are validated client-side:
  non-empty, no `/`, no NUL, no backslash, valid UTF-8, bounded length — the server rejects bad
  names but silently falls back to the multipart part filename when the query name is invalid
  UTF-8. There is no chunked or resumable upload API, so `resumable_uploader` is not declared.
- Map provider errors by status and structured `error.code`: `401` auth, `404` not found,
  `409 AlreadyExists`/`Conflict` conflict, `400 InvalidPath` invalid input, `429` rate limit, and
  `500 FilesNotSynced` (external mount out of sync) as a distinct provider-state error that is not
  retried blindly; tolerate non-JSON error bodies; thread `requestId` into diagnostics.
- Implement share links for existing entries: create, list (mount-scoped and account-wide `GET
  /links`), password set, validity window, downloadable flag, and delete — all through mount-scoped
  endpoints. Password material returned by the provider is never logged. `password/reset`
  regenerates a random password rather than removing one, so removing a passcode is not offered.
- Implement share receive/fetch: parse `app.koofr.net/links/{id}` and `k00.fr/{hash}` URLs and
  produce anonymous direct-download targets via `GET /content/links/{linkId}/files/get?path=`
  (probe-verified: returns exact file bytes for both root-level and nested files; the rclone-era
  single-member-zip quirk is gone; a directory path returns a zip and is not used). Provider-side
  save-to-drive is not available; `share_save` is not declared.
- Declare exactly `writer`, `source_uploader`, `space`, `path_resolver`, `remote_name_resolver`,
  `mtime`, `server_side_copy`, `share`, `share_receive`, and `share_fetch`. Do not declare
  `resumable_uploader`, `share_save`, or offline capabilities. `mtime` covers upload-time and
  copy-time preservation only; a standalone set-mtime is not available. `remote_name_resolver`
  exists because Koofr names are case-insensitive (`A.bin` and `a.bin` collide).
- Register the driver in the schema, generated metadata, capability tests, and generated user
  documentation; add a redacted debug snapshot and normalized metrics.

## Capabilities

### New Capabilities

- `koofr-driver`: Koofr mount authentication and mount/root scoping, deterministic listing, range
  reads, write and conditional-delete semantics with trash cascade behavior, single-request uploads
  with mtime preservation, case-insensitive remote-name resolution, per-entry share link management
  with mount-scoped endpoints, anonymous share receive/fetch via content links, and the exact
  capability/observability surface of the `koofr` driver.

### Modified Capabilities

<!-- None: adding a built-in driver follows the existing `driver-parameter-schema` requirements
     without changing them. -->

## Impact

- `pkg/drivers/koofr` (new): client (Basic auth, error mapping, FilesNotSynced handling), listing,
  read, write, upload, share links, share receive/fetch, types, observability, tests with an
  `httptest` fake provider.
- `pkg/drivers/all/all.go`: blank import.
- `schema/qrypt.schema.json` and regenerated `pkg/drive/builtin_schema_gen.go`,
  `internal/cli/i18n/catalog_gen.go`, `docs/for-user/full-config.md`,
  `docs/for-user/support-drivers.md`.
- `scripts/schema/gen-driver-docs.py`: driver metadata and order.
- `pkg/drivers/capability_test.go`: exact capability list for `koofr`.
- `docs/for-developer/quality-gates.md`: real-account smoke procedure for Koofr.
- Tests: fake-provider unit tests for listing/info, range/416, mkdir 409 semantics, rename/move/
  copy conflicts, conditional delete, upload mtime/overwrite behavior, name validation, share link
  lifecycle and redaction, anonymous download targets, FilesNotSynced mapping, and debug redaction;
  a live-account smoke test gated by environment variables.
