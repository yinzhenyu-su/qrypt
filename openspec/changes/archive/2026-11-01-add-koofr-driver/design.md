# Design: add-koofr-driver

## Context

See `proposal.md` and `specs/koofr-driver/spec.md`. qrypt's driver contract lives in `pkg/drive`;
the closest reference implementations are `pkg/drivers/drime` (HTTP driver with fake-provider
tests) and the shared utilities in `pkg/drivers/internal/driverutil`.

A live probe against a real app.koofr.net account (primary mount only; the account's external
Google Drive mount was out of sync and returned `500 FilesNotSynced` for every call, including root
listing) produced the facts below. The probe used only `qrypt-probe-*` temporary directories and
cleaned everything up; credentials are not committed.

Key probe observations (full report in the mission library):

- Routes: metadata under `/api/v2.1`, content under `/content/api/v2.1`. `/content/.../files/list`
  does not exist (404); `/content/.../files/listrecursive?path=` does exist and streams NDJSON with
  paths relative to the requested directory. `files/get`/`files/put` work under both `/content/api/v2`
  and `/content/api/v2.1`. A non-content `files/get` returns `303` to a signed storage URL.
- `mkdir` on an existing name returns `409 AlreadyExists` (error code in a JSON envelope) — the
  rclone-era "400 means exists" rule is obsolete on app.koofr.net.
- `files/put` without `overwrite`/`autorename` **auto-renames** to `name (1).bin` and returns `200`.
  `overwrite=true` replaces the file, and when `modified` is not sent the mtime resets to server
  time. The query `filename` parameter takes precedence over the multipart part filename; an
  invalid-UTF-8 query filename is silently ignored and the part filename is used instead.
- Backslash names and 300-byte names are rejected with `400 InvalidPath` (no silent truncation).
  `mkdir` with an empty or slash-containing name returns `500` with a plain-text body
  (`Name is required`), so error parsing must tolerate non-JSON bodies.
- Range downloads are exact: `206` for `0-0`, `99-`, `50-60`; `416` (`error.code=Other`,
  "Invalid range") for a start past EOF. MD5 hashes are returned by list/info/put.
- Deletion: `removeIfSize` mismatch → `409 Conflict`; match → `200 {"version": ...}`; missing →
  `404 NotFound`. Deleting a non-empty directory without `removeIfEmpty` succeeds and cascades:
  children appear individually in the trash.
- Trash: paginated listing; `POST /trash/undelete` starts an async job (`200` with a job object)
  that completes quickly; `DELETE /trash` empties it (`200`).
- Share links: `POST /mounts/{id}/links` → `201` with `id`, `url`, `shortUrl` (`k00.fr/{hash}`),
  `hash`, flags. All sub-resources (detail, `password/set`, `password/reset`, `validity`,
  `downloadable`, DELETE) live under `/api/v2.1/mounts/{mid}/links/{linkId}`; the account-scoped
  `/api/v2.1/links/{linkId}` returns `404 Page not found`. `GET /api/v2.1/links` lists all links.
  `password/set` returns the plaintext password in the response; `password/reset` replaces the
  password with a random one instead of removing it.
- Anonymous fetch: `GET {base}/content/links/{linkId}/files/get?path=<relative>` needs no auth and
  returns exact file bytes for root-level and nested files (MD5-verified). A directory path returns
  a zip and is not used. The rclone-era "nested file returns a single-member zip" quirk is gone.
- Space: `GET /mounts` / `GET /mounts/{id}` report `spaceTotal`/`spaceUsed` in **MiB**, plus
  `online`, `canWrite`, `canUpload`, `overQuota`.
- Names are case-insensitive: renaming to a case-variant of an existing name returns
  `409 AlreadyExists`; uploading `PROBE2.BIN` with `overwrite=true` overwrote `probe2.bin`.

## Goals / Non-Goals

**Goals:**

- A correct, small v1 driver for the primary hosted mount: list/read/write/upload/space/share with
  the observed provider quirks absorbed inside the driver.
- Path-based identity mapping that survives case-insensitive collisions.
- A fake provider that reproduces the observed request/response shapes, including `409 AlreadyExists`
  on mkdir, auto-rename defaults, `416` ranges, `FilesNotSynced`, and the anonymous content-link
  route.

**Non-Goals:**

- Resumable or chunked uploads (no provider API).
- Standalone set-mtime (provider only accepts mtime at upload/copy time).
- Cross-mount copy/move guarantees for external mounts (the probe account's external mount was
  entirely out of sync); `mount_id` is exposed but only the primary mount is live-verified.
- Trash browsing UI, file versions, batch jobs, search, thumbnails, OAuth2 (app-password Basic
  auth only), save-to-drive.

## Decisions

### Authentication and mount selection

App password + HTTP Basic against `api_base_url` (default `https://app.koofr.net`). `email` and
`password` are credential-scoped secrets so one `[[credentials]]` entry can serve several mounts.
`Init` calls `GET /user` to validate credentials, then `GET /mounts` to resolve `mount_id` —
defaulting to the `isPrimary` entry — and checks `online`/`canWrite`/`canUpload` before committing.
A mount that reports offline or not-writable fails initialization rather than serving a read-only
or broken tree. `root_path` is resolved with `files/info` during `Init` and committed only after
success so a retried `Init` never silently mounts the account root.

### Path-based identity

Koofr has no object ids; every operation addresses `mountId + path`. `Entry.ID` is the absolute
provider path (like the localfs model), `ParentID` the parent path, `Basename` the file name.
Because names are case-insensitive, `ResolveRemoteName` folds lookups case-insensitively and
reports the provider's canonical casing from listings, so a local `A.bin` overwriting a remote
`a.bin` resolves to one entry instead of two. `ResolvePath` walks segments via `files/info`/list
and is the authority for identity; `listrecursive` is reserved for full scans, not per-directory
listing.

### Listing

`GET /api/v2.1/mounts/{id}/files/list?path=` returns the full directory in one response (no
pagination observed); entries map with directory sizes flattened to zero, ms `modified`, provider
MD5 in `Extra`. Because `path=` with an empty value silently means the root, the driver validates
that every outgoing path is absolute and non-empty. Duplicate names differing only by case cannot
occur provider-side, but the mapper tolerates and deduplicates them defensively.

### Reads

`GET /content/api/v2.1/mounts/{id}/files/get?path=` with a standard `Range` header. `206` bodies
are returned directly; `416` for a window at/past a known size maps to an empty reader, other
`416`s are errors; a `200` that ignored the range is lazily skipped and capped (same defensive
pattern as drime). Reads do not issue per-call metadata requests — the path in `Entry.ID` is the
content address.

### Writes and conflict semantics

- `Mkdir`: `POST files/folder`; `409 AlreadyExists` re-lists the target and returns the existing
  directory (or a conflict if a file holds the name). The plain-text `500 Name is required` shape
  maps to invalid input.
- `Rename`: `PUT files/rename`; `409 AlreadyExists` (including case-variant targets) maps to
  `drive.ErrConflict`.
- `Move`/`Copy` (`server_side_copy`): `PUT files/move|files/copy` with `{toMountId, toPath}`; copy
  sends `modified` from the source entry so mtime survives. Destination conflicts map to
  `ErrConflict`. Cross-mount targets are rejected by the driver when they reference a mount other
  than the mounted one (a mount is one provider mount; the probe showed external mounts can be
  persistently `FilesNotSynced`, which would surface as opaque 500s otherwise).
- `Remove`: `DELETE files/remove` with `removeIfSize`/`removeIfModified`/`removeIfHash` carried from
  the entry as a CAS guard; `409 Conflict` maps to `ErrConflict`, `404` to `ErrNotFound`. Because
  the provider cascades non-empty directories without `removeIfEmpty`, `Remove` never claims a
  directory is protected — VFS empty-check semantics stay client-side, and the spec documents the
  cascade explicitly.

### Uploads

One lane only: `POST /content/api/v2.1/mounts/{id}/files/put` with multipart field `file`, query
`filename`, `info=true`, `overwrite=true`, `autorename=false`, and `modified=<ms>` **always**
derived from the source `ModTime` (or now, if absent) — the probe proved an overwrite without
`modified` resets mtime to server time. `autorename=false` plus `overwrite=true` gives
deterministic replace semantics; the provider default (silent auto-rename) must never be relied
on. Parent directories are created level-by-level with `files/folder`, treating `409 AlreadyExists`
as success. Names are validated before any request: non-empty, no `/`, no NUL, no backslash, valid
UTF-8, length-bounded — the probe showed the server falls back to the multipart part filename when
the query name is invalid UTF-8, which would silently store the wrong name. `RequiredUploadHashes`
returns none (the provider computes MD5 itself); the returned `hash` is exposed via `RemoteHash`
as MD5 for entries whose hash is 32-hex. No instant upload: the provider has no
hash-based create. `resumable_uploader` is not declared; progress is reported per byte within the
single request.

### Error mapping

`drive.HTTPError` for every non-2xx. JSON bodies are parsed for `error.code` and `requestId`:
`401`→`ErrAuth`, `404`→`ErrNotFound`, `409 AlreadyExists`/`Conflict`→`ErrConflict`,
`400 InvalidPath`→`ErrInvalidInput`, `429`→`ErrRateLimit` with exponential backoff. Non-JSON bodies
(the observed plain-text `500 Name is required`) are wrapped without parsing. `500 FilesNotSynced`
maps to a provider-state error that is **not** retried unconditionally — the probe showed it is
persistent while an external mount is out of sync; blind 5xx retry would hang operations.

### Share links

`ShareManager` over mount-scoped endpoints: `POST /mounts/{id}/links` (one entry per link,
`path`-addressed), `GET /mounts/{id}/links` and account-wide `GET /links` for listing, `GET/PUT/
DELETE /mounts/{id}/links/{linkId}` plus `password/set`, `validity` (validFrom/validTo in ms),
`downloadable`. `SharePolicy`: passcode optional, expiry restricted to the provider's
validity-window model (day/week/month/permanent), download toggle supported. Titles, visit limits,
and public toggles are rejected as unsupported. Passcode removal is not offered: the provider's
`password/reset` generates a random password rather than clearing it (probe-verified), and the
`password/set` response echoes the plaintext password — that response is never logged, never
placed in a debug snapshot, and the passcode is stored only where qrypt already stores share
secrets. `LinkID` is the provider link id; the URL/shortUrl are surfaced to the user.

### Share receive and fetch

`ShareLinkParser` accepts `https://app.koofr.net/links/{id}` and `https://k00.fr/{hash}` (the
short URL 303-redirects to the page URL; only the page URL's id is needed for content access).
`ShareFetcher` produces one anonymous target per selected file:
`GET {base}/content/links/{linkId}/files/get?path=<link-relative path>` — probe-verified to return
exact bytes for root-level and nested files with no authentication. Directory paths return a zip
and are rejected (single-entry selection only). Passcode-protected links: the anonymous content
route honors the link's password state; if a protected link cannot be fetched anonymously without
the password the fetch fails with a passcode-required error (the probe did not exercise a
password-protected anonymous fetch; the fake provider covers both paths and the live gate must
confirm — see Open Questions). `ShareReceive` (browse) is limited to what the provider exposes
without a documented public listing API: v1 supports receive for links whose entries can be
enumerated from the share creation context, and the fake provider pins the exact behavior the
implementation settles on. `share_save` is not declared (no save-to-drive endpoint).

### Capabilities are the exact implemented set

`writer`, `source_uploader`, `space`, `path_resolver`, `remote_name_resolver`, `mtime`,
`server_side_copy`, `share`, `share_receive`, `share_fetch`. `mtime` is upload/copy-time only
(documented as such); `resumable_uploader` absent (no API); `share_save` absent; offline absent.

## Risks / Trade-offs

- **No resumable uploads**: a failed large upload restarts from zero. Mitigation: document it;
  progress reporting keeps VFS informed; a future provider API would slot into `session`.
- **Case-insensitive names** can surprise POSIX users (`A.bin` shadows `a.bin`). Mitigation:
  `remote_name_resolver` reports the provider casing; conflicts surface as `ErrConflict` rather
  than silent shadowing.
- **Directory delete cascades** into the trash. Mitigation: spec documents it; VFS-level guards
  remain the client's empty-check; trash restore is possible within retention.
- **`FilesNotSynced` on external mounts** makes a `mount_id` pointing at an out-of-sync external
  mount fail every operation. Mitigation: fail `Init` early when the mount reports offline or the
  first listing returns `FilesNotSynced`; v1 live-verifies only the primary mount.
- **Auto-rename default** would silently fork files if parameters were ever omitted. Mitigation:
  the driver always pins `overwrite=true&autorename=false`; a fake test asserts both parameters on
  every put.
- **Plaintext password echo** from `password/set`. Mitigation: redaction in errors/snapshots and a
  dedicated test.
- **Probe gaps** (429 shape, password-protected anonymous fetch, external mounts in a healthy
  state) are listed as Open Questions and covered by the live gate before release.

## Migration Plan

New mount type only; no existing configs change. Schema, generated docs, and capability tests ship
together so `koofr` is accepted by validation and visible in the support matrix. Rollback is
removing the registration and docs; remote data is untouched.

## Open Questions

- The exact 429 behavior for link creation limits and its headers (never triggered in the probe).
- Whether the anonymous `content/links/.../files/get` route accepts a password parameter for
  passcode-protected links, and what error shape it returns otherwise.
- Whether cross-mount copy/move works when an external mount is healthy (the probe account's
  external mount was persistently `FilesNotSynced`).
- Whether `listrecursive` is practical for Change detection at scale (streaming NDJSON, no
  pagination) — v1 does not use it for directory listing.
