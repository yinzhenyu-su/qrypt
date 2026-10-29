## Why

Drime Cloud is requested in issue #13. It exposes a documented REST API and already has an
official rclone Tier 1 backend, but neither qrypt nor OpenList ships a driver, so users must leave
qrypt to use it. The provider fits qrypt's driver contract (id-based tree, range downloads,
presigned multipart uploads, SHA-256 and mtime metadata, server-side copy), but it has several
provider-specific hazards — client-asserted hashes, silent filename truncation, unstable listing
order, and a slow API — that the driver must absorb instead of leaking into VFS behavior.

## What Changes

- Add a built-in `drime` driver authenticated by a personal access token (`access_token`), with
  `workspace_id` (0 = personal space), `root_path`/`root_id`, `api_base_url`, `chunk_size`, and
  `hard_delete` parameters. One mount maps to one workspace; workspaces are not browsed as folders,
  and a resolved root is verified to belong to the selected workspace because a mismatched
  workspace context returns an empty listing instead of an error.
- Implement the read path over `api/v1/file-entries/{id}`: follow the 302 to R2, preserve `Range`,
  handle `206`, `416`, and servers that ignore `Range`. Build the URL from the entry id when
  `Extra` is unavailable rather than spending one API call per read.
- Implement listing with `folderId` (never `parentIds`), `perPage=100`,
  `orderBy=name&orderDir=asc`, empty-page termination, a 20,000-entry safety cap, duplicate-id
  detection, folder sizes flattened to zero, and `client_last_modified` preferred over `updated_at`.
- Implement writes: `Mkdir` returns the existing directory or a conflict; `Rename` uses the literal
  `PUT` endpoint (the provider rejects the `X-HTTP-Method-Override` workaround); `Move` and
  `Remove` carry `workspaceId`; copy uses `/file-entries/duplicate` plus a rename, and directory
  moves wait briefly for the children listing to settle.
- Implement uploads in two lanes: `POST /uploads` below 5 MiB, and resumable S3 multipart at or
  above 5 MiB with configurable chunk size, per-part signing, quoted ETags, UUID-based
  `/s3/entries`, and progress recovered from `/s3/multipart/get-uploaded-parts`.
- Send SHA-256 (`fileHash`) and `lastModified` on every upload. Reject names longer than 255 bytes
  instead of accepting the provider's silent truncation. Expose `RemoteHash` only for 64-hex
  SHA-256 values, and do not implement hash-based instant upload in v1 because the probe proved
  that `file_hash` is client-declared and accepted without content verification.
- Implement per-entry share link management: create, show, update, and delete links with optional
  passcode, expiry, download, and edit permissions. One link covers exactly one entry, so a
  multi-entry create is rejected instead of silently publishing only the first entry. Link titles,
  visit limits, and a public/private toggle are not supported by the provider and are rejected
  through the share policy.
- Degrade `share list` explicitly: the provider has no account-wide link listing endpoint, so the
  driver declares `SharePolicy.List=false` and `ListShares` returns `ShareErrUnsupported`. Core
  rejects the operation before calling the driver, so the CLI reports a clear error instead of an
  empty list. `share create/show/update/remove` remain available.
- Implement share receiving and direct fetch: parse `dri.me` and `app.drime.cloud/drive/s/...`
  links, verify passcodes, browse share folders, and produce direct download targets for individual
  share entries. Provider-side save-to-drive (`share save`) is deferred because the discovered
  import endpoint still needs investigation.
- Declare `writer`, `source_uploader`, `resumable_uploader`, `space`, `path_resolver`, `mtime`,
  `server_side_copy`, `share`, `share_receive`, and `share_fetch`. Do not declare `share_save`,
  vault, offline download, or foreign entries.
- Register the driver in the schema, generated metadata, capability tests, and generated user
  documentation; add a redacted debug snapshot and normalized metrics.

## Capabilities

### New Capabilities

- `drime-driver`: Drime mount authentication and workspace/root scoping, deterministic and bounded
  listing, range reads, write identity/conflict behavior, two-lane resumable uploads with hash and
  mtime preservation, per-entry link management with explicit list degradation, share receive and
  direct fetch, and the exact capability/observability surface of the `drime` driver.

### Modified Capabilities

<!-- None: adding a built-in driver follows the existing `driver-parameter-schema` requirements
     without changing them. -->

## Impact

- `pkg/drivers/drime` (new): client, listing, read, write, upload/session, share link management,
  share receive/fetch, types, observability, tests with an `httptest` fake provider.
- `pkg/drivers/all/all.go`: blank import.
- `schema/qrypt.schema.json` and regenerated `pkg/drive/builtin_schema_gen.go`,
  `internal/cli/i18n/catalog_gen.go`, `docs/for-user/full-config.md`,
  `docs/for-user/support-drivers.md`.
- `scripts/schema/gen-driver-docs.py`: driver metadata and order.
- `pkg/drivers/capability_test.go`: exact capability list for `drime`.
- `docs/for-developer/quality-gates.md`: real-account smoke procedure for Drime.
- Tests: fake-provider unit tests for listing/pagination, range/redirect/416, rename/move/delete,
  multipart resume, hash and mtime round-trips, share create/update/delete, `share list` rejection,
  share link parsing/passcodes/folder browsing/download targets, and debug redaction; a live-account
  smoke test.
