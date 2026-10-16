# qrypt Driver Contract

Everything here is checked against the code in `pkg/drive`, `pkg/vfs`, and
`pkg/mount`. Verify symbols with grep before relying on them: this file is a
map, not the territory.

## The Driver Interface

`pkg/drive/drive.go` defines one interface every backend implements:

```go
type Driver interface {
	Init(ctx context.Context) error
	Drop(ctx context.Context) error
	List(ctx context.Context, parentID string) ([]Entry, error)
	Read(ctx context.Context, entry Entry, offset, size int64) (io.ReadCloser, error)
	Mkdir(ctx context.Context, parentID, name string) (Entry, error)
	Move(ctx context.Context, entry Entry, dstParentID string) (Entry, error)
	Rename(ctx context.Context, entry Entry, newName string) (Entry, error)
	Remove(ctx context.Context, entry Entry) error
	PutSource(ctx context.Context, req UploadRequest) (Entry, error)
	RequiredUploadHashes() []HashAlgorithm
	ResolvePath(ctx context.Context, path string) (string, error)
	ResolveRemoteName(ctx context.Context, plainName string) (RemoteNameInfo, error)
	ForeignEntries(ctx context.Context, parentID string) ([]ForeignEntry, error)
	Space(ctx context.Context) (Space, error)
	Capabilities() []Capability
	DebugSnapshot(ctx context.Context) (DebugSnapshot, error)
	Metrics(ctx context.Context, since time.Time) ([]MetricEvent, error)
}
```

`drive.UnsupportedOperations` supplies `ErrUnsupported` defaults for
`Mkdir`, `Move`, `Rename`, `Remove`, `PutSource`, `RequiredUploadHashes`,
`ResolvePath`, `ResolveRemoteName`, and `ForeignEntries`. Embed it, implement
the rest, and declare only the capabilities you actually back. Operations are
gated by that contract, not by embedding: `List`, `Read`, `Space`,
`Capabilities`, `DebugSnapshot`, and `Metrics` must always be real
implementations.

There is no `drive.Uploader`, `drive.FileUploader`, `drive.Writer`,
`drive.Debugger`, `drive.HealthChecker`, `drive.SpaceQuerier`, or
`drive.UploadHashRequirements`. Uploading is `PutSource`; writing is the four
methods above; space is `Space`; health and metrics come from `DebugSnapshot`
and `Metrics`.

## Capabilities

`pkg/drive/capability.go` defines the constants reported by
`Capabilities()` and read by `drive.HasCapability`:

| Capability | Backed by | Declare when |
|---|---|---|
| `writer` | `Mkdir`/`Move`/`Rename`/`Remove` | the backend supports them |
| `source_uploader` | `PutSource` | uploads stream from a `ReadOnlyFileSource` |
| `resumable_uploader` | `PutSource` + `pkg/drive/session` | an interrupted upload continues instead of restarting |
| `space` | `Space` | the backend reports capacity |
| `path_resolver` | `ResolvePath` | a virtual path resolves to a backend id |
| `remote_name_resolver` | `ResolveRemoteName` | the driver maps plaintext names to different remote names |
| `foreign_entries` | `ForeignEntries` | the backend reports objects the mount does not own (only crypt does today) |
| `mtime` | `UploadRequest.ModTime` honored on upload | the backend stores the mtime you set |
| `server_side_copy` | `Copy` (`ServerSideCopier`) | the copy happens provider-side |

`drive.CheckUnsupportedCapabilities` asserts the negative direction: without
the capability, the operation must return `ErrUnsupported`. The positive
direction (declared ⇒ works) is covered by `pkg/drive/contract_matrix_test.go`
and `pkg/drivers/capability_test.go`; add your driver to the latter.

Interfaces that are *not* constants and are discovered by type assertion:

- `drive.RemoteHasher` (`RemoteHash`) — content hash a backend already knows
  (list metadata). `drive.RemoteHashFromExtra` reads it from a
  `map[string]any` `Extra`; a driver with a typed `Extra` writes its own.
- `drive.ServerSideCopier` (`Copy`) — see `server_side_copy` above.
- `drive.EncryptedHashProvider` — provided by the crypt wrapper, not by raw drivers.
- `drive.BandwidthLimitInstaller` (`InstallBandwidthLimiter`) — apply the shared
  limiter at the transport level and return the directions you handle.
- `drive.StateStoreInstaller` (`InstallStateStore`) — persist tokens and upload
  session bindings.
- `drive.EntryRemoteNamer` / `drive.EntryRawExtraer` — used by
  `drive.EntryExtraWrapper` when a higher layer rewrites a name or wraps `Extra`.
  Read through `drive.RawEntryExtra` and `drive.EntryRemoteName`.

## Wrapper Interactions

- crypt (`pkg/crypt`) encrypts names and content above the raw driver. Its
  `Capabilities()` forwards `writer`, `source_uploader`, `resumable_uploader`,
  and `space`, drops `path_resolver` and `server_side_copy`, and adds
  `foreign_entries` and `remote_name_resolver`. Its `RemoteHash` delegates to the
  raw driver so a ciphertext hash stays available.
- crypt replaces `Entry.Extra` with `drive.EntryExtraWrapper{RemoteName, Raw}`.
  Raw drivers must unwrap before reading their own metadata.
- `pkg/mount` normalizes for the OS: `fillStatWithCreds` sets `Size`/`Blocks`
  only for non-directories, so a directory's size never reaches FUSE regardless
  of what the driver returned.
- `pkg/vfs/drivecopy` rebuilds the source entry from stat info and therefore
  drops `Extra`. `Read`/`Copy` must tolerate a missing `Extra`: re-resolve
  required provider metadata, for example through the parent listing.

## Entry Semantics

`drive.Entry` is the only metadata type VFS understands:

- `ID`/`ParentID`: stable provider-native ids (or normalized paths for
  path-based backends). Keep ids stable across `Move`/`Rename` when the provider
  assigns them; return the new location's id when the provider derives it from
  the location.
- `Name`: the provider basename only, never a path prefix. Crypt translates
  names above the raw driver; do not decrypt them in the driver.
- `IsDir`: exact.
- `Size`: bytes, and **0 for directories**. Providers may report a lagging
  content aggregate for a directory (e.g. 123pan keeps the sum after a child is
  removed); flatten it to 0 in the entry and provider metadata carried in Extra.
- `ModTime`: provider metadata, zero when unknown. Never fabricate `time.Now()`.
- `CreatedAt`/`UpdatedAt`: provider metadata when present.
- `Extra`: provider metadata the driver needs later (content hash, storage flag,
  parent id). Keep it consistent with the public fields: if a merge step patches
  `Entry.Size`, patch the provider object it was built from, not the entry
  afterwards, or `Read` will see a stale zero size and return EOF.

## Pagination

Terminate on the provider cursor/`next`, an empty page, or a reached total.
A safety page cap must raise an error, never return a short listing: callers
treat missing entries as absent and may delete or re-upload them.

## Read Semantics

`Read(ctx, entry, offset, size)`:

- Reject negative `offset`/`size`.
- `size > 0`: up to `size` bytes from `offset`. `size == 0`: from `offset` to EOF.
- Clamp to the known size; a window that runs past EOF returns what is there.
- `offset >= entry.Size` (known, non-zero) returns an empty reader, not an error.
- A provider-confirmed zero-length object returns an empty reader without a
  network probe: probing `bytes=0-0` can draw a `416`.
- An HTTP `200` that ignored the `Range` header is the whole object, not the
  requested window: skip `offset` bytes lazily and cap the length. A `416` means
  EOF at/past a known size; during a resolve probe it means the URL already
  serves the (empty) object.
- Resolve redirect layers explicitly and follow both a `Location` header and a
  JSON envelope; make relative targets absolute against the responding URL and
  bound the hop count. Keep the probe's `Range` header on every hop.
- Treat `403` on a signed URL as expired, invalidate the cached URL, and refresh
  once. Cache resolved URLs per file id with an expiry.
- Close response bodies on every error path.

Deleted objects are not always observable: a provider may keep serving a
working download URL for a trashed file. Classify not-found with an endpoint
that reflects live state (`drive.ErrNotFound`).

## Write Semantics

`PutSource(ctx, UploadRequest)`:

```go
type UploadRequest struct {
	ParentID string
	Name     string
	Source   ReadOnlyFileSource   // Size(), Open(ctx) -> Read/ReadAt/Seek/Close
	Progress UploadProgress       // Phase(), Uploaded(n)
	ModTime  time.Time            // zero = provider stamps upload time
}
```

- Upload exactly the bytes it is given, including ciphertext when crypt wraps
  the driver. Crypt wrapping is above the driver.
- Prefer hashes the caller already computed (`drive.SourceHash(source, algo)`)
  and declare them with `RequiredUploadHashes`; scan the source only when a
  required hash is missing. Report progress with
  `drive.ReportUploadProgress`/`ReportUploadPhase`.
- Return the finalized provider entry. If the provider's completion response
  omits fields, fill them on the provider object *before* building the entry.
- Provider-side dedup (a content hash the provider already stores) is an instant
  upload and is also the mechanism to implement `Copy` on backends without a
  copy endpoint: issue a create/upload request for the destination name with the
  source's content hash, and finish when the provider reports reuse.
- Any body handed to an SDK that retries must be seekable. `io.LimitReader`
  drops `Seek`; use a bounded `io.ReadSeeker` (a small section reader type) so a
  transient 5xx can be retried instead of failing with
  `request stream is not seekable`.

`Mkdir(parentID, name)`:

- Return the created **or existing directory**.
- A same-name non-directory is a conflict: wrap `fs.ErrExist` so
  `drive.ErrorCategory` reports `conflict`. The VFS recovers an existing
  directory only for that category, and it does not re-check `IsDir` after
  `Mkdir`, so a driver that returns a file here corrupts the namespace.
- Providers may keep files and directories in separate namespaces and answer a
  same-name directory with a duplicate error while happily creating a directory
  beside a same-name file. Look the name up before creating.

`Remove` takes the exact entry; when the backend moves to a recycle bin rather
than deleting, say so in the operation's error classification only if it is
genuinely not-found.

## Upload Sessions (`pkg/drive/session`)

`session.Identity{ParentID, Name, Size, Fingerprint}.Key()` is the content key.
Rules:

- Use the *resolved* parent id. Callers may pass aliases (`""`, `"0"`, `/`) that
  mean different roots at different times.
- Reserve the binding before creating provider state, update it after, and
  delete it once the provider commit succeeds (a leftover is reclaimed by the
  expiry pass).
- `Index.Create` writes atomically; a failed write must abort the attempt rather
  than continue without durable state.
- Without a provider progress-query endpoint, keep a confirmed-parts bitmap
  (`session.ConfirmedBitmap`/`MarkConfirmed`/`ConfirmedParts`) inside the token
  and update it through `Index.TouchWith` (serialized, throttled to about one
  disk write per minute). Re-uploaded parts must be idempotent.
- `Index.Touch` marks the binding alive for long uploads. `Index.Flush` is
  needed from `Drop` so disk matches memory, and `Drop` must cancel the expirer
  started with `session.RunExpirer`.
- `Reclaim` must be idempotent and tolerate already-committed or already-gone
  provider resources; when the provider has no abort endpoint, reclaiming is
  just dropping the binding.
- A stored handle can be dead (provider GC, crash after commit). Detect the
  rejection, drop the binding, and restart once from a fresh provider request.

## Config, Schema, And Roots

- `drive.Register(name, factory, ParamDef...)` declares params; the CLI and
  config validation read them, so a param missing from the `ParamDef` list makes
  the mount invalid.
- Secrets are `Secret: true` and must never appear in errors, logs, metrics, or
  debug snapshots.
- Validation is dynamic: `pkg/config/validation.go` accepts exactly the params
  in `ParamSchema(mount.Type)` and requires those marked `Required`.
- `qrypt.schema.json` is a committed, hand-maintained mirror: the `type` `enum`,
  an `allOf` `if/then` per driver, and `definitions.<name>Params`. Docs are
  generated from it by `scripts/gen-driver-docs.py` and CI checks they are in sync.
- `root_id` vs `root_path`: an explicit id wins and skips path resolution.
  Resolve `root_path` from the account root, and commit the resolved root only
  after success so a retried `Init` does not fall back to the account root.
- `Init` is retried by the mount layer: keep it idempotent and side-effect free
  until the credentials and root are both confirmed.

## Errors

Cross-layer classification uses `errors.Is`/`errors.As`, never string parsing:

- Sentinels: `drive.ErrUnsupported`, `drive.ErrSpaceUnsupported`,
  `drive.ErrNotFound`, `drive.ErrAuth`, `drive.ErrRateLimit`,
  `drive.ErrInvalidInput`.
- `drive.HTTPError(prefix, req, resp, body)` builds a driver-prefixed error with
  the body and URL redacted through `drive.Snippet`, and wraps the sentinel
  implied by the status (401/404/429/400). Never splice a raw body or URL into
  an error by hand.
- For non-HTTP failures wrap the right sentinel yourself, e.g. a name collision
  as `fmt.Errorf("acme: mkdir %q: %w", name, fs.ErrExist)` (→ `conflict`).
- `drive.ErrorCategory(err)` and `drive.RetryableCategory(category)` are the
  single source of truth for JSON retry flags and backoff decisions; do not
  re-derive them. `conflict`, `not_found`, `invalid_request`, and
  `unsupported` are non-retryable by design.
- `drive.NonRetryable(err)` marks a deterministic upload failure so the VFS
  stops re-driving the same payload.

## Debug And Metrics

- Drivers implementing provider-side instant upload expose
  `drive.DebugExtraInstantUploadCount` (`instant_upload_count`) in
  `DebugSnapshot.Extra`, including zero before any successes. Keep the counter
  concurrency-safe and increment only after provider reuse is successfully
  finalized, never on a failed attempt. `ReportUploadPhase` alone is not
  sufficient: the live instant-upload suite checks the counter delta.
- `DebugSnapshot` must be safe: driver name, coarse health, non-secret stats
  (root id, counters), and a last error string that has already been redacted.
  Never include tokens, cookies, authorization headers, or signed URLs.
- `Metrics` returns `drive.MetricEvent`s; drivers buffer them under
  `pkg/drivers/internal/driverutil` and normalize with
  `drive.NormalizeMetricEvents("<driver>", events)`.
- Useful shared helpers: `pkg/drivers/internal/driverutil/httpclient`
  (request/body/decode), `.../httputil` (transport), `pkg/util`
  (`WaitExponential`, `ExponentialBackoff`).

## OpenList Migration Notes

Trust level, by area:

- Reliable copy-paste: auth/signing/HMAC algorithms, endpoint paths, request
  bodies, response field names, error code meanings (usually).
- Requires live proof: whether dedup/instant upload triggers, whether a
  "not supported" operation has an undocumented equivalent, which completion
  endpoint applies to which branch, how deletion is observed, how pagination
  terminates, whether a directory listing returns file ids in `data` or
  `data.Info`, and what the provider reports for directory sizes.
- Never copy: OpenList's driver abstractions, caches, meta tables, path
  handling, and error types. Rebuild around qrypt's `Driver`/`Entry` and the
  local `pkg/drivers/<name>` shape.

Observed 123pan examples to check rather than assume for other providers:

- Trashed objects can retain working download URLs; use a live-state endpoint
  such as `/restful/goapi/v1/file/details` to classify deletion.
- Same-content creation can provide server-side copy through provider dedup.
- Completion branches use mutually exclusive `upload_complete` and
  `upload_complete/v2` endpoints; do not call both.
- A create response can populate `data.Info.FileId` while the top-level id is 0.
- Separate file/directory namespaces can allow same-name entries; check before
  creating a directory.

Keep the observed provider behavior in code comments where it differs from
OpenList, so the next reader does not "fix" it back.
