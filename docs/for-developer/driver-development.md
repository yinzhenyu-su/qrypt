# Driver Development

This guide describes how to add a cloud-drive backend to qrypt.

## Boundaries

- `pkg/drive` defines the provider contract.
- `pkg/vfs` owns filesystem behavior: path resolution, read cache, staged
  writes, pending journal recovery, and upload scheduling.
- `pkg/crypt` wraps any driver with rclone-compatible encryption.
- `internal/mount` translates FUSE callbacks into VFS calls.
- `internal/driver/<name>` is the only place that should know provider API
  details.

Drivers must not import FUSE, mount lifecycle code, or qrypt's encryption
implementation. VFS and mount code must not import provider SDKs.

## File Layout

```text
internal/driver/<name>/
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

Then add a blank import to the bundled-driver registry in
`internal/driver/all/all.go`:

```go
_ "github.com/yinzhenyu/qrypt/internal/driver/baidu"
```

Users should be able to inspect parameters with:

```sh
qrypt driver schema baidu
```

Keep provider-specific options in `[[mounts]].params`. Do not add top-level
config fields unless the setting applies to all drivers.

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
hashes for rapid upload or provider APIs should call `drive.SourceHash` first
and only scan the source when the required hash metadata is unavailable.

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

## Errors

Prefix driver errors with the driver name and operation:

```go
return nil, fmt.Errorf("baidu: list: %w", err)
```

Include useful context such as `list`, `read`, `upload`, `mkdir`, or
`resolve root_path`. Do not include credential values.

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

## Checklist

- Driver lives under `internal/driver/<name>`.
- Driver is registered with `drive.Register`.
- `internal/driver/all/all.go` imports the driver for registration.
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
