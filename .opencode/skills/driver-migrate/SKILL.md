---
name: driver-migrate
description: Migrate a storage driver from OpenList/OpenListTeam into the qrypt Go codebase, or debug a partially migrated one. Use when asked to add a qrypt driver, port an OpenList cloud-drive implementation, compare OpenList behavior with qrypt's driver contract, or fix list/read/write/path/schema/session/upload behavior of a driver under pkg/drivers/.
---

# Driver Migrate

## Workflow

1. Resolve the target driver before coding (see Driver Discovery). Do not start
   implementation until it is explicit.

2. Read qrypt's *real* contract before writing code.
   - `pkg/drive/drive.go`: the `Driver` interface, capability constants, `ParamDef`.
   - `pkg/drive/source.go`, `state.go`, `debug.go`, `session/`: upload sources,
     state store, debug/metrics types, resumable-upload bindings.
   - The closest existing driver under `pkg/drivers/<name>` (e.g. `quark`,
     `p189`, `yun139`).
   - `references/qrypt-driver-contract.md` in this skill.
   - Verify every symbol you are about to call with grep. Interface names from
     memory, from OpenList, or from older qrypt docs are frequently wrong; the
     code is the only authority.

3. Probe the provider live before designing (see Provider Probing).
   OpenList is a *hypothesis* about the provider, not a specification: parts of
   it are wrong or incomplete for a given account. Record the observed
   request/response shapes; they drive the design.

4. Implement in layers.
   - Register: `init()` + `drive.Register` with `ParamDef`s.
   - `Init`/`Drop`, then `List`, then `Read`.
   - Writes: `Mkdir`/`Remove`/`Rename`/`Move`, then `PutSource`.
   - Optional: `Space`, `DebugSnapshot`, `Metrics`, `RequiredUploadHashes`,
     `RemoteHash`, `Copy`, resumable upload sessions.
   - Declare only the capabilities the backend truly implements, and implement
     exactly what you declare (see Capability Rules).

5. Keep VFS boundaries clear.
   - Drivers return backend entries. VFS owns path resolution, read cache,
     staging, the pending journal, upload scheduling, and FUSE semantics.
   - Drivers must not know about qrypt staging or the read cache.
   - Crypt and bandwidth wrapping happen above the raw driver.

6. Register the plumbing, not just the code (see Registration Checklist). The
   schema, generated docs, and capability test all fail CI if skipped.

7. Test, then verify live, then clean up (see Verification).

## Driver Discovery

When the driver is not named:

1. List local drivers with `ls pkg/drivers/` (and `pkg/drivers/all/all.go`).
2. List OpenList drivers:
   `gh api repos/OpenListTeam/OpenList/contents/drivers --jq '.[].name'`.
3. Remove names qrypt already ships; present the rest as candidates.
4. Ask which one to migrate and wait for the answer before editing code.

One OpenList driver is a small directory (`driver.go`, `meta.go`, `types.go`,
`util.go`, `upload.go`):

```bash
gh api repos/OpenListTeam/OpenList/contents/drivers/<driver> --jq '.[] | "\(.name) \(.size)"'
gh api repos/OpenListTeam/OpenList/contents/drivers/<driver>/util.go --jq '.content' | base64 -d
```

Use another repository access method if `gh` is unavailable or unauthenticated,
and say so.

## Provider Probing

Use a throwaway Go program in a session-owned temporary directory; prefer the
standard library, but reuse existing SDKs/helpers when they exercise the real
upload path more faithfully. Within the authorized account/test scope, it:

1. authenticates with the real credentials;
2. lists the root and paginates a directory;
3. uploads a small file, then a *random* file larger than the provider's actual
   multipart threshold/part size (random avoids provider-side dedup); use a
   sufficiently large source to interrupt and resume when testing that branch;
4. downloads a byte range, following whatever redirect/signature layer the
   provider uses;
5. creates a directory, renames, moves, and deletes;
6. records redacted responses, retaining status, field names/types, missing
   fields, and `null` values. Mask tokens, cookies, passwords, temporary storage
   credentials, and signed URL query values before printing or saving them.

Do not dump the user's full config. Keep credential-bearing temporary files
owner-readable only.

Auth/signing algorithms in OpenList are usually copy-pasteable; the *flow* is
what needs proof. Real cases where OpenList was wrong or silent:

- Deletion may not be observable: the provider can keep serving a working
  download URL for a trashed object, so not-found classification needs a
  different endpoint (123pan: `/restful/goapi/v1/file/details`).
- "Copy not supported" can be false: a same-content upload request that the
  provider deduplicates into the destination directory is a zero-byte
  server-side copy (123pan).
- Completion endpoints can be mutually exclusive per branch (123pan:
  `upload_complete` vs `upload_complete/v2`), so calling both fails.
- A create response may carry the new id in a nested field (`data.Info.FileId`)
  while the top-level one is 0.
- Files and directories may live in separate namespaces, so creating a
  directory beside an existing same-name file can succeed and yield two
  entries with one name.

Keep the probe out of the repository and delete it when done.

## Registration Checklist

- Package: `pkg/drivers/<name>/`. A Go package name cannot start with a digit,
  so use a `p` prefix and keep the registered name numeric: `p115` → `"115"`,
  `p123` → `"123"`, `p189` → `"189"`.
- `init()` calls `drive.Register(name, factory, ParamDef...)`, with
  `Required`, `Secret`, `Default`, and `Example` set for every user-facing param.
- `pkg/drivers/all/all.go`: add the blank import.
- `qrypt.schema.json`: add the name to the `type` `enum` under
  `definitions.mountConfig`; add an `allOf` `if/then` block that `$ref`s the new
  params; add `definitions.<name>Params` whose descriptions match the `ParamDef`s.
- `scripts/gen-driver-docs.py`: add a `DRIVER_META` entry and put its key in
  `DRIVER_ORDER`.
- `pkg/drivers/capability_test.go`: add an entry with the exact expected
  capability list.
- Regenerate docs: `python3 scripts/gen-driver-docs.py` and
  `python3 scripts/gen-config-docs.py` (CI fails when `docs/for-user/` is stale).
- Avoid new modules; prefer what `go.mod` already has (for S3,
  `github.com/aws/aws-sdk-go-v2/service/s3` is present).

## Migration Checklist

Entries:
- `ID`/`ParentID` are provider-native ids, `Name` is the basename, `IsDir` is
  exact, `ModTime` comes from provider metadata (never `time.Now()`).
- `Size` is 0 for directories. Providers often report a lagging content
  aggregate instead; flatten it to 0 in the entry *and* in the provider
  metadata you carry in `Extra`, so the two can never disagree.
- Everything a later call needs (content hash, storage flag, parent id) travels
  in `Entry.Extra`. Read it back with `drive.RawEntryExtra`, because the crypt
  wrapper replaces `Extra` with `drive.EntryExtraWrapper`.
- `drivecopy` rebuilds entries from stat info and drops `Extra`: a driver that
  needs provider metadata must re-resolve it from the parent listing (the
  `refreshMeta` pattern) instead of assuming it is present.

Reads:
- Reject negative offset/size; `size == 0` means "to EOF"; clamp a window that
  runs past a known size; `offset >= size` returns an empty reader, not an error.
- A `200` that ignored your `Range` is *not* the requested window: skip `offset`
  bytes lazily and cap the length. A `416` is EOF at/past a known size, and
  during a resolve probe it means the URL already serves the (empty) object.
- Resolve the download layer explicitly: decode wrapped URLs, follow both a
  `Location` header and any JSON `redirect_url` envelope, make relative targets
  absolute against the responding URL, and allow a bounded number of hops.
- Cache the resolved URL per file id with an expiry, and refresh it on 403.

Writes:
- `Mkdir` may only ever return a directory. A same-name non-directory is a
  conflict wrapped as `fs.ErrExist`, so `drive.ErrorCategory` reports `conflict`
  and the VFS's existing-directory recovery applies. When the provider keeps
  files and directories in separate namespaces, look the name up before creating.
- `PutSource` uploads exactly the bytes it is given (possibly ciphertext from
  the crypt wrapper). Prefer hashes the
  caller already computed (`drive.SourceHash`) over re-reading the source, and
  declare them via `RequiredUploadHashes`.
- When merging an "upload finished" response into an entry, fill missing fields
  on the provider object *before* building the entry, so the public fields and
  `Extra` stay consistent (a size that stays 0 on a non-empty file makes `Read`
  return EOF).
- Anything handed to an SDK that retries must be seekable. `io.LimitReader`
  drops `Seek` and turns a transient 5xx into `request stream is not seekable`.

Roots:
- An explicit `root_id` wins and skips `root_path`. Resolve `root_path` only
  when no id is configured, and resolve it from the account root, not from the
  configured id.
- Commit the resolved root only after resolution succeeds, so a retried `Init`
  runs it again instead of silently falling back to the account root.

Pagination:
- Terminate on the provider cursor/`next`, an empty page, or a reached total.
- A safety page cap must raise an error, never return a short listing: callers
  treat missing entries as absent and may delete or re-upload them.

Resumable uploads (`pkg/drive/session`):
- Key the binding by content identity (parent + name + size + fingerprint) and
  use the *resolved* parent id, not the caller's alias (`""`/`"0"` can refer to
  different roots over time).
- Reserve the binding before creating provider state, update it after, delete it
  when the provider commit succeeds.
- Without a progress-query endpoint, fold a confirmed-parts bitmap into the
  binding through `Index.TouchWith` (throttled) and re-upload idempotently.
- Detect a dead provider handle and restart once; reclaim expired bindings by
  dropping them when the provider has no abort endpoint.
- `Drop` cancels the expirer and `Flush`es the index.

## Capability Rules

- Capability declarations must match observable behaviour in both directions:
  a declared capability has to work, and an undeclared one has to return
  `ErrUnsupported` (`drive.CheckUnsupportedCapabilities` enforces the latter).
- Do not declare `resumable_uploader` if an interrupted upload restarts from
  byte zero; not `mtime` unless the backend stores the mtime you set; not
  `server_side_copy` unless the copy is provider-side.
- Optional interfaces found by type assertion still matter: `RemoteHasher`
  (cheap content hash from list metadata), `EncryptedHashProvider` (crypt),
  `BandwidthLimitInstaller`, and `StateStoreInstaller`. `DebugSnapshot` and
  `Metrics` are required `Driver` methods, not optional interfaces.
- For provider-side instant upload, expose the success counter described in
  the reference's Debug And Metrics section; reporting an upload phase alone
  does not let the live suite verify reuse.
- Wrappers must preserve or report capabilities. The crypt wrapper drops
  `path_resolver` and `server_side_copy`, and adds `foreign_entries` and
  `remote_name_resolver`.

## Verification

Unit tests first, with fakes:
- Registration/config validation; entry mapping (ids, dirs, sizes, times);
  pagination and its limit error; read offsets/EOF/range-ignored/416/redirects;
  mkdir conflict paths; upload branches (instant, presigned, static-credential,
  multipart retry); session resume and stale-handle restart; `RemoteHash`;
  `Copy`; and a debug snapshot that leaks no credential.
- Fake the provider with `httptest`. For SDK branches fake an S3-compatible
  endpoint path-style (`?uploads`, `?partNumber`, `?uploadId`) and inject
  `500`/`416`/`302`/stale-session responses to reach retry and recovery paths.
- Every bug fix ships with a regression test that fails on the old code. When
  checking that failure, use an isolated copy/worktree or temporary overlay;
  do not revert the user's working tree. Exercise the real call chain (e.g.
  list-normalized metadata through Remove), not only hand-built metadata.

Live verification against a real account:
- The mount needs `test_enabled = true` for `debug test`.
- Run `auth`, `contract`, `crud`, `fs`, `instantupload`, `resume`, and
  `multipart` separately; `contract` is not an alias for all suites. Report
  each result and capability-based skip reason, test sizes, residual objects,
  and cleanup failures. Add read/batch/xfer tests where relevant.
- `./scripts/mount-debug-test.sh <mount> <spec> [flags]` builds qrypt, mounts
  into an isolated temporary tree (its own cache/staging/state), runs one
  `debug test` (`auth`, `crud`, `contract`, `fs`, `multipart`,
  `instantupload`, `resume`, `read`, `batchupload`, `batchmove`, `xfer`), then
  tears it down. Prefer it over hand-rolled temporary configs when no existing
  service/socket was requested.
- For an existing socket, first verify the mount/driver, running server build,
  test enablement, staging availability, and pending work using read-only
  diagnostics. Building a new CLI does not upgrade that server. Do not replace
  or restart the user's service without authorization.
- Against a running mount: `qrypt debug collect --mount NAME --socket PATH` for
  structured JSON, or `qrypt debug raw '/v1/state?mount=NAME' --socket PATH` for
  one endpoint. Useful endpoints: `/v1/state`, `/v1/driver`, `/v1/resolve?path=`,
  `/v1/consistency?path=`, `/v1/cache`, `/v1/staging`, `/v1/events?level=warn`,
  `/v1/mounts/health`. The full list lives in `docs/for-developer/debug.md`.
- One-shot `fs` commands debounce: uploads flush after `upload_delay` (default
  30s), deletes after `delete_delay` (default 2s). Lower those in the config or
  wait before believing a result — a pending write is not a driver bug.
- Classify failures by layer before changing code: provider/driver, VFS, or
  environment. Missing staging directories are not a reason to add staging
  knowledge to a raw driver.
- Clean up only remote objects and exclusive temporary directories created by
  this test session. Wait for test work to settle and stop an owned temporary
  service before removing its runtime directories. Never delete an active
  mount's shared state/cache/staging/upload data. Retain redacted failure
  reports and list any residual objects rather than broadening cleanup.

Repository gates, all run by CI:

```bash
gofmt -l .
go vet ./...
scripts/check-arch.sh
scripts/test-layers.sh fast
scripts/test-layers.sh race
scripts/test-layers.sh smoke
python3 scripts/gen-driver-docs.py && python3 scripts/gen-config-docs.py
```

`scripts/ci-check.sh` runs the whole local gate (vet, staticcheck,
golangci-lint, architecture boundaries, govulncheck, format, docs sync, layers).
For race/runner flakes, repeat the failing test with `-race` before rerunning
the full gate. Prefer explicit synchronization; tests not checking timeout
semantics need realistic scheduling budgets. A local pass is not proof of
GitHub Linux/Windows success; when CI verification is requested, check the
exact submitted commit.

## References

- `references/qrypt-driver-contract.md`: the real qrypt driver contract —
  interfaces, capabilities, entry/read/write semantics, errors, sessions, and
  what is (and is not) trustworthy in OpenList.
- `docs/for-developer/driver-development.md`, `docs/for-developer/debug.md`,
  `docs/for-developer/quality-gates.md`: the repository's own guides.
