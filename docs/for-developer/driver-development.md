# Driver Development

This guide describes how to add a cloud-drive backend to qrypt.

## Boundaries

- `pkg/drive` defines the provider contract.
- `pkg/vfs` owns filesystem behavior: path resolution, read cache, staged
  writes, pending journal recovery, and upload scheduling.
- `pkg/crypt` wraps any driver with rclone-compatible encryption.
- `pkg/mount` translates FUSE callbacks into VFS calls.
- `pkg/drivers/<name>` is the only place that should know provider API
  details.

Drivers must not import FUSE, mount lifecycle code, or qrypt's encryption
implementation. VFS and mount code must not import provider SDKs.

## File Layout

```text
pkg/drivers/<name>/
  driver.go
  client.go        # optional provider API client
  types.go         # optional response and metadata mapping
  driver_test.go
```

Use a short stable driver name. It becomes the config `type`:

```toml
[[mounts]]
name = "baidu-main"
type = "baidu"

[mounts.params]
token = "..."
root_path = "/qrypt"
```

## Registration

Register the driver from `init` and declare parameter metadata:

```go
func init() {
	drive.Register("baidu", func(params drive.Params) (drive.Driver, error) {
		token := params["token"]
		if token == "" {
			return nil, fmt.Errorf("baidu: missing token")
		}
		return New(token, Options{
			RootPath: params["root_path"],
		}), nil
	},
		drive.ParamDef{
			Name:        "token",
			Required:    true,
			Secret:      true,
			Description: "Baidu cloud drive access token",
			Example:     "your-token",
		},
		drive.ParamDef{
			Name:        "root_path",
			Description: "Virtual root path on the drive",
			Default:     "/",
			Example:     "/qrypt",
		},
	)
}
```

Then add a blank import to the full bundled-driver registry in
`pkg/drivers/all/all.go`:

```go
_ "github.com/yinzhenyu/qrypt/pkg/drivers/baidu"
```

Users should be able to inspect parameters with:

```sh
qrypt driver schema baidu
```

Keep provider-specific options in `[[mounts]].params`. Do not add top-level
config fields unless the setting applies to all drivers.

### Platform-authorized directory drivers

`pkg/drivers/scopedfs` is the bundled driver for user-authorized local/provider
directories on mobile platforms. It is intentionally not Android-specific:

- Android apps map `root_token` to a persisted SAF tree URI.
- iOS apps map `root_token` to security-scoped bookmark data.
- The driver depends only on a registered `scopedfs.Backend`; platform APIs stay
  in the mobile app or `pkg/mobile` adapter.

Do not import Android, iOS, gomobile, or app UI packages from a driver. If a
platform needs a new operation shape, add it behind the scopedfs backend
adapter instead of coupling `pkg/core` or `pkg/vfs` to that platform.

`scopedfs` declares writer and source-upload support, so direct upload tasks can
write into authorized directories without qrypt staging. It does not declare
mtime or resumable upload because common SAF/File Provider backends cannot
guarantee those semantics consistently.

## Required Interface

Every driver must implement `drive.Driver`:

```go
type Driver interface {
	Init(ctx context.Context) error
	Drop(ctx context.Context) error
	List(ctx context.Context, parentID string) ([]Entry, error)
	Read(ctx context.Context, entry Entry, offset, size int64) (io.ReadCloser, error)
}
```

Rules:

- `Init` validates credentials and the configured root.
- `Drop` releases resources; return nil if there is nothing to close.
- `List` returns direct children only.
- `Read` respects `offset` and `size`; `size == 0` means read to EOF.
- All network calls respect `context.Context`.

## Write Support

Writable drivers should implement both `drive.Writer` and `drive.SourceUploader`:

```go
type Writer interface {
	Mkdir(ctx context.Context, parentID, name string) (Entry, error)
	Move(ctx context.Context, entry Entry, dstParentID string) error
	Rename(ctx context.Context, entry Entry, newName string) error
	Remove(ctx context.Context, entry Entry) error
}

type SourceUploader interface {
	PutSource(ctx context.Context, parentID, name string, source ReadOnlyFileSource) (Entry, error)
}
```

`PutSource` returns the committed remote object after the provider confirms upload.
The returned `Entry` should describe the final object, not a temporary upload
session.

`ReadOnlyFileSource` provides a stable upload source with `Size()` and `Open()`.
Each opened file supports `Read`, `ReadAt`, `Seek`, and `Close`, so drivers can
stream the body, retry reads, or perform multipart uploads without requiring VFS
to expose a local path.

Sources may also implement `drive.HashProvider`. Drivers that need full-file
hashes for instant upload or provider APIs should call `drive.SourceHash` first
and only scan the source when the required hash metadata is unavailable.

Declare `CapabilitySourceUploader` and `CapabilityWriter` when `PutSource` can
stream from a `ReadOnlyFileSource` into the backend. This is enough for direct
upload tasks to bypass qrypt staging. Add `CapabilityResumableUploader` only
when the driver can continue an interrupted source upload instead of
restarting from byte zero.

### Upload sessions: provider-authoritative resume

Resumable drivers persist one small thing locally: a binding from a
**content-addressed identity** (`pkg/drive/session.Identity`: parent, name,
size, and a content fingerprint — the provider-mandated hash, SHA-256 for
s3/sftp/onedrive/yun139, SHA-1 for p115/p115open/aliyundrive/quark, MD5 for
p189/baidu) to a **provider-side upload reference** (S3 UploadID, OneDrive
upload URL, SFTP staging path, uploadFileId, ...). Upload progress is
reconstructed from the provider where possible (`ListParts`, `stat`, Graph
`nextExpectedRanges`, OSS ListParts, Baidu superfile2 list), so for those
drivers the session file stays tiny and per-part work costs zero local I/O.
Use `pkg/drive/session.Index` (installed via
`drive.StateStoreInstaller`) for the binding store and `session.RunExpirer`
with a driver-provided idempotent `Reclaim` for orphan cleanup.

Contract rules:

- The binding is written before provider upload work starts ("reserve, then
  create"), so a crash at any point leaves a resumable or reclaimable state —
  never an untracked provider-side orphan. Binding writes are transactional:
  `Index` commits memory only after the disk write succeeds, and a failed
  reservation must abort the attempt (s3/p115/p115open abort the provider
  session, sftp refuses to start) instead of proceeding without durable state.
- Content identity is mandatory: resuming parts whose identity does not match
  the current source is forbidden. When the caller provides no fingerprint,
  either scan the source (cheap local reads) or degrade to a fresh session —
  never reuse a session keyed without content proof. s3 and sftp scan when the
  hash is missing; onedrive degrades, since its sources may be remote streams.
- The provider commit (complete/rename/finish) succeeding **is** the upload
  succeeding. Local binding cleanup is best-effort; leftover bindings are
  reclaimed by the expiry pass, and a binding pointing at a completed upload
  is detected as invalid on the next attempt.
- `Reclaim` must be idempotent and tolerate already-committed or already-gone
  provider resources (abort of a completed multipart returns 404, removal of
  a renamed-away staging file is not-exist, cancel of a finished upload
  session is 404/410). Drivers whose provider has no abort endpoint
  (yun139/p189/quark/aliyundrive/baidunetdisk) reclaim by dropping the binding;
  the provider session expires server-side.
- When the provider can neither query part progress nor make partial writes
  unsafe, the fallback is a **local confirmation record**: a compact
  confirmed-parts bitmap (`session.ConfirmedBitmap`/`MarkConfirmed`) or the
  per-part ETags (quark, needed by OSS Complete) folded into the binding token
  via `Index.TouchWith`. This covers yun139/p189/quark and aliyundrive (whose
  deployment has no query endpoint: the `getUploadUrl`/`listUploadedParts`
  paths only exist on the openapi.alipan.com gateway with an open-platform
  token; aliyundrive also persists the createWithFolders part URLs in the
  token, since URLs cannot be re-fetched). `TouchWith` serializes the token
  read-modify-write under the index lock (safe under concurrent part
  confirmations) and persists throttled to one disk write per minute; a crash
  loses at most a minute of confirmations, and those parts are re-uploaded
  idempotently (part uploads overwrite by number/seq) — never corrupt.
- Mark the binding alive with `Index.Touch` per part (throttled to one disk
  write per minute), so long uploads are not reclaimed by the expiry scan;
  call `Index.Flush` from `Drop` so the on-disk index matches memory before
  the process exits.

Drivers that can skip network upload when hashes are available before streaming
should implement `drive.UploadHashRequirements`. The crypt wrapper uses this to
precompute encrypted-content hashes only when `content_dedup` is enabled and the
raw driver asks for them.

If the provider is eventually consistent, return after the commit API succeeds
and expose follow-up state through debug data. Do not sleep in the driver.

## Entry Semantics

`drive.Entry` is the only metadata type VFS understands:

```go
type Entry struct {
	ID       string
	ParentID string
	Name     string
	IsDir    bool
	Size     int64
	ModTime  time.Time
	Extra    any
}
```

Guidelines:

- `ID` and `ParentID` should be stable provider-native IDs.
- Path-based providers may use stable normalized paths.
- `Name` is the driver-visible basename, not a full path.
- `Size` is zero for directories.
- `ModTime` should come from provider metadata when available. If a create or
  upload response does not include time fields, use a stable operation-start
  time rather than leaving it to FUSE fallback.
- `Extra` may hold provider-specific metadata, but behavior outside the driver
  must not depend on provider-specific types.

## Optional Capabilities

Implement optional interfaces when useful:

```go
type SpaceQuerier interface {
	Space(ctx context.Context) (Space, error)
}

type PathResolver interface {
	ResolvePath(ctx context.Context, path string) (string, error)
}

type Debugger interface {
	DebugSnapshot(ctx context.Context) (DebugSnapshot, error)
}

type RemoteNameResolver interface {
	ResolveRemoteName(ctx context.Context, plainName string) (RemoteNameInfo, error)
}
```

Use `drive.Capabilities(driver)` in tests and diagnostics when you need a
stable list of runtime capabilities. It is the canonical capability inventory
for VFS-facing behavior. Construction-time hooks such as state-store injection
and bandwidth limiter installation are intentionally not reported there.

### Hash provenance

`drive.RemoteHasher` is still the compatibility surface, but its hashes are
treated as **declared**: qrypt may display them, but must not use them to
conclude that content is unchanged. A provider that computes the hash from
stored bytes, or that confirms a declared hash with an integrity check,
implements `drive.ProvenanceHasher`:

```go
type ProvenanceHasher interface {
	RemoteHashInfo(ctx context.Context, entry drive.Entry) (drive.RemoteHashInfo, error)
}
```

`RemoteHashInfo.Verified` is the authority bit. Sync fails a content match when
either side's hash is declared, because an unverified hash cannot prove
equality. Mark `Source` as `provider` when the backend computed the hash and as
`verified` when qrypt confirmed it (for example with an integrity endpoint).
`drive.RemoteHashInfoFor` normalizes a legacy driver to `declared`, so a driver
that has not migrated cannot accidentally claim authority.

### Optional share interfaces

The share surface is intentionally split so a provider implements only what it
has:

- `drive.ShareManager` — create, show, update, and remove links.
- `drive.ShareLister` — account-wide link listing. Do not implement a stub that
  returns unsupported; omit the interface and leave `SharePolicy.List` false.
- `drive.ShareSaver` — submit a provider-side save.
- `drive.ShareSavePoller` — poll an asynchronous save. A synchronous provider
  omits it and returns an empty `Handle`.

Declare every supported option in `SharePolicy` (`Title`, `Public`, `Download`,
`Edit`, `VisitLimit`, `MaxEntries`, and so on). Core rejects an option the
policy does not declare before calling the provider, so the driver does not
silently drop it. A provider that scopes writes by workspace, drive, or space
implements `drive.ProviderSpaceReporter`; Core passes that identity in
`ShareDestination.SpaceID`. A save may return a lightweight verified identity
in `ShareSaveItem.SavedID`/`SavedName`/`Verified` instead of a full `Entry`,
but Core still verifies the destination listing.

### Entry resolution and remote names

A provider that can fetch one entry by id implements:

```go
type EntryResolver interface {
	Stat(ctx context.Context, id string) (drive.Entry, error)
}
```

Core uses it when persisted private metadata is missing and otherwise falls
back to the driver's listing behavior. A missing id must wrap
`drive.ErrNotFound`.

A provider that cannot store a plain name unchanged implements
`drive.RemoteNameCodec` (`Encode`/`Decode`) and returns the plain name from
listings. Use `drive.WrapRemoteName` to preserve existing `Entry.Extra` while
exposing the provider name through `drive.EntryRemoteName`; reads, writes,
renames, and copies must use the encoded form, and an encoded name that exceeds
the provider limit must be rejected before any write.

### Offline download

A provider that can import a remote source into cloud storage implements
`drive.OfflineDownloader` and declares `drive.CapabilityOfflineDownload`. The
provider owns its own task identifier and maps its status model into
`drive.OfflineState` (`queued`/`running`/`succeeded`/`failed`/`canceled`);
provider-specific identifiers and status stay in `drive.OfflineDownload` and the
qrypt task detail, never in the common task schema.

Independent optional capabilities refine the surface:

- `drive.CapabilityOfflineInspect` + `drive.OfflineInspector` — resolve a source
  into a file list without creating a task. Return `drive.ErrOfflineInspectPending`
  while an asynchronous parse is still running so the caller can retry.
- `drive.CapabilityOfflineSelection` — import a subset of the parsed files.
  `drive.OfflineSelection` is the provider-neutral selector; a provider that does
  not declare it must reject anything except `all`.
- `drive.CapabilityAcceleratedFetch` — hand out an accelerated download session
  at submit time. A missing session must degrade to the normal download URL, not
  fail the fetch.
- `drive.OfflineFetcher` — hand out a signed download URL for staged content.
  Return a URL plus its expiry; the caller refreshes on expiry.
- `drive.OfflineRetrier` — re-queue a failed/canceled provider task in place
  instead of making the caller re-create it.

Surface provider failures as `drive.OfflineError` with a stable
`drive.OfflineErrorCode`; `drive.NewOfflineError` derives the retryable flag.
Quota, policy, auth, unsupported-source, and crypto failures are non-retryable;
only transient provider problems (`offline_provider_unavailable`,
`offline_provider_task_missing`) are retryable. Wrappers (`pkg/crypt`,
bandwidth) must preserve the offline capabilities and forward the optional
offline interfaces to the raw driver.

Debug and health output must not include tokens, cookies, authorization
headers, signed URLs, raw request headers, or full provider responses that may
contain secrets.

## Wrappers

Do not build encryption into a driver. `pkg/crypt` handles content and
filename encryption around any `drive.Driver`.

Do not build global bandwidth policy into a driver unless the provider upload
implementation has internal concurrency that must be limited at the native
request level. The generic bandwidth wrapper handles normal reads and uploads.
Provider request-rate throttling is a separate driver concern.

When adding optional interfaces, update crypt and bandwidth wrappers so they
preserve or provide safe fallbacks for those capabilities.
If a wrapper has fallback methods whose concrete method set is wider than the
raw driver's real support, implement `drive.CapabilityReporter` so VFS and
debug tooling see the intended runtime capabilities instead of raw type
assertions.

A wrapper that renames things must translate names **both ways** on every
method that returns an entry. Outgoing arguments belong to the backend's
vocabulary (the crypt wrapper encrypts them); incoming entries belong to the
caller's vocabulary (plaintext), with the backend name exposed through
`drive.EntryExtraWrapper` and read back with `drive.EntryRemoteName`. `List`,
`PutSource` and `Mkdir` in `pkg/crypt` follow this; a method that returns the
backend's own entry verbatim hands callers a name the VFS cannot address - the
view is keyed by path, so the ciphertext name would surface in listings as a
file nobody can open.

## Errors

Prefix driver errors with the driver name and operation:

```go
return nil, fmt.Errorf("baidu: list: %w", err)
```

Include useful context such as `list`, `read`, `upload`, `mkdir`, or
`resolve root_path`. Do not include credential values.

When an HTTP response body, URL, or raw server text would end up inside an
error string (they flow into logs, task `LastError`, debug snapshots and
contract output), route it through the shared redactor — never roll your
own pattern list:

```go
return nil, drive.HTTPError("quark: upload", nil, resp, body)
```

`drive.Snippet` / `drive.HTTPError` (in `pkg/drive`) truncate at 300 bytes
and mask tokens, cookies, download URLs, authorization headers and URL
userinfo; their mask patterns are covered by unit tests in that package. A
duplicated pattern set in a driver is a review failure — extend `drive.Snippet`
instead.

### Stable error sentinels

Errors crossing layer boundaries must be classifiable with `errors.Is` /
`errors.As` — never by parsing text. `drive.HTTPError` already wraps the
right sentinel from the HTTP status (401 → `drive.ErrAuth`, 403 →
`drive.ErrPermission`, 404 → `drive.ErrNotFound`, 409 → `drive.ErrConflict`,
422/400 → `drive.ErrInvalidInput`, 429 → `drive.ErrRateLimit`), so most driver
errors get it for free. An endpoint where a structured 422 means "missing"
passes `drive.HTTPErrorStatusAsNotFound(http.StatusUnprocessableEntity)` for
that call only; every other endpoint keeps invalid-input classification. For
non-HTTP failures, wrap the sentinel yourself:

```go
return fmt.Errorf("quark: %s auth failed: %w: %s", op, drive.ErrAuth, drive.Snippet([]byte(msg)))
```

Sentinels: `drive.ErrAuth` (credential expired/rejected → refresh and
retry), `drive.ErrPermission` (authenticated but not allowed → never retry),
`drive.ErrNotFound` (missing object), `drive.ErrConflict` (state conflict,
usually no retry), `drive.ErrRateLimit` (back off longer),
`drive.ErrInvalidInput` (data/validation problem, never retryable), plus
`context.Canceled` / `context.DeadlineExceeded`. "Retryable" is a derived
property (`drive.RetryableCategory`); only wrap `drive.NonRetryable` for
deterministic failures.

### Shared driver infrastructure

Cross-cutting HTTP plumbing lives under `pkg/drivers/internal/driverutil` so drivers do not
hand-roll the same code: trace buffers and metric field helpers, `httpclient`
(JSON request construction, response body reading, JSON decoding),
`uploadsession` (upload session persistence), and `internal/httputil`
(transport). `driverutil.RetryPolicy` owns bounded retries: it retries
transport failures and `5xx`/`429`, honors `Retry-After`, and never retries
`401`/`403`/`404`/`422`. `driverutil.RateLimiter` owns a per-client API request
budget; one instance must be created per provider client so concurrent calls
share it. Use these building blocks; keep provider-specific orchestration
(auth header injection, metric reporting) inside the driver.

When a missing object is part of normal path resolution, prefer returning a
typed error that the VFS/mount layer can distinguish from backend failures.

## Minimum Tests

Add tests for:

- factory registration and missing required params
- provider response to `drive.Entry` mapping
- directory `IsDir`, file size, parent ID, and modification time
- read offset and size handling
- provider API failures with driver-prefixed errors
- `Mkdir`, `Put`, `Rename`, `Move`, and `Remove` when supported
- debug snapshots without secrets
- CRUD tests for active driver probing when writes are supported
- optional interfaces surviving crypt and bandwidth wrappers when relevant
- `drive.Capabilities(driver)` reporting the intended runtime interfaces

Use fake provider servers or clients. Unit tests should not require real
accounts.

New drivers also get the local contract matrix for free by being testable
against `drive.FakeDriver` (`pkg/drive/contract_matrix_test.go`): capability
declarations must match observable behaviour, `UnsupportedOperations` errors
classify as unsupported, listings must be stable across calls,
`drive.RunBehaviorChecks` must pass, and debug snapshots must not carry
credential material. Keep the golden-reference behaviour (fake, localfs)
green; real-provider contract runs happen nightly or manually via the
Contract Tests workflow.

## Checklist

- Driver lives under `pkg/drivers/<name>`.
- Driver is registered with `drive.Register`.
- `pkg/drivers/all/all.go` imports the driver for full CLI registration.
- Required params are declared and validated.
- `Init` validates credentials and root selection.
- `List` returns direct children only.
- `Read` handles offset and size.
- Writable drivers implement both `drive.Writer` and `drive.SourceUploader`.
- Returned entries include stable IDs, names, sizes, parent IDs, and mod times.
- Debug output excludes secrets.
- Active driver probing uses CRUD tests instead of a separate active health interface.
- Tests cover success and failure paths.
- Existing wrappers preserve optional interfaces.
- `go test ./...` passes.
