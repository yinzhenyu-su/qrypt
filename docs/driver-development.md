# Driver Development Guide

This guide describes how to add a new cloud-drive backend to qrypt.

Drivers adapt provider-specific APIs to the small interfaces in `pkg/drive`.
The VFS, encryption layer, rate limiter, namespace mount, FUSE adapter, and
debug socket must not depend on provider SDKs or provider-specific types.

## File Layout

Put each driver under `internal/driver/<name>`:

```text
internal/driver/<name>/
  driver.go
  client.go        # optional provider API client
  types.go         # optional response and metadata mapping
  driver_test.go
```

Use a short stable driver name, for example `quark`, `localfs`, or `baidu`.
The name is used in config:

```toml
[[mounts]]
name = "baidu-main"
type = "baidu"

[mounts.params]
token = "..."
root_path = "/qrypt"
```

## Registration

Register the driver from `init`:

```go
func init() {
	drive.Register("baidu", func(params drive.Params) (drive.Driver, error) {
		token := params["token"]
		if token == "" {
			return nil, fmt.Errorf("baidu: missing token")
		}
		return New(token, Options{
			RootPath: params["root_path"],
			RootID:   params["root_id"],
		}), nil
	},
		// Declare the parameter schema so the CLI can show help and validate.
		drive.ParamDef{
			Name:        "token",
			Type:        "string",
			Required:    true,
			Secret:      true,
			Description: "Baidu cloud drive access token",
			Example:     "your-token",
		},
		drive.ParamDef{
			Name:        "root_path",
			Type:        "string",
			Description: "Virtual root path on the drive",
			Default:     "/",
			Example:     "/qrypt",
		},
		drive.ParamDef{
			Name:        "root_id",
			Type:        "string",
			Description: "Root directory ID",
			Default:     "root",
			Example:     "root",
		},
	)
}
```

Users can then query the schema from the CLI:

```sh
qrypt help driver baidu
```

Then add a blank import in `cmd/qrypt/main.go`:

```go
_ "github.com/yinzhenyu/qrypt/internal/driver/baidu"
```

Avoid adding provider-specific fields to the top-level config schema. Use
`[[mounts]].params` unless a setting is genuinely shared by all drivers.

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

`Init` validates credentials and resolves the configured root. It should fail
early when the account, token, root ID, or root path is unusable.

`Drop` releases resources. If there is nothing to close, return nil.

`List` returns direct children of one directory. It must not recurse.

`Read` returns file content for a byte range. When `size > 0`, read exactly the
requested range if the provider supports range requests. When `size == 0`, read
from `offset` to EOF.

## Write Support

Writable drivers should implement `drive.Writer`:

```go
type Writer interface {
	Mkdir(ctx context.Context, parentID, name string) (Entry, error)
	Move(ctx context.Context, entry Entry, dstParentID string) error
	Rename(ctx context.Context, entry Entry, newName string) error
	Remove(ctx context.Context, entry Entry) error
}
```

And `drive.Uploader`:

```go
type Uploader interface {
	Put(ctx context.Context, parentID, name string, size int64, body io.Reader) (Entry, error)
}
```

`Put` must return the committed remote object after the provider confirms the
upload. The returned `Entry.ID`, `Entry.ParentID`, `Entry.Name`, and
`Entry.Size` should describe the final remote object, not a temporary upload
session.

If a provider is eventually consistent, prefer returning after the commit API
succeeds, and expose consistency details through debug fields instead of
sleeping in the driver.

## Entry Semantics

`drive.Entry` is the only file metadata type the VFS understands:

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

Use provider-native IDs for `ID` and `ParentID` when possible. If the provider
is path-based, use stable normalized paths like `localfs` does.

`Name` is the name visible inside this driver before qrypt's encryption wrapper
is applied. Do not put full paths in `Name`.

`Extra` may hold provider-specific metadata, but VFS behavior must not require
callers outside the driver to understand it.

## Optional Capabilities

Implement optional interfaces when the provider supports them:

```go
type SpaceQuerier interface {
	Space(ctx context.Context) (Space, error)
}

type PathResolver interface {
	ResolvePath(ctx context.Context, path string) (string, error)
}
```

`SpaceQuerier` feeds capacity reporting.

`PathResolver` maps a driver-relative path to a native object ID. It is useful
for debug and for providers where resolving by path can be cheaper than walking
directories through VFS.

## Debug Capabilities

New cloud drivers should implement `drive.HealthChecker` and `drive.Debugger`.
Implement `drive.RemoteNameResolver` when the driver can expose a meaningful
plain-name to remote-name mapping.

```go
type HealthChecker interface {
	HealthCheck(ctx context.Context) HealthStatus
}

type Debugger interface {
	DebugSnapshot(ctx context.Context) (DebugSnapshot, error)
}

type RemoteNameResolver interface {
	ResolveRemoteName(ctx context.Context, plainName string) (RemoteNameInfo, error)
}
```

`HealthCheck` powers `/v1/driver/health`. It may touch the backend, so keep it
bounded and respect `ctx`. Include latency and the latest error, but never
include credentials.

`DebugSnapshot` powers `/v1/driver`. It should return stable generic fields:

```go
drive.DebugSnapshot{
	Driver:      "baidu",
	Health:      "ok",
	GeneratedAt: time.Now(),
	Stats: map[string]any{
		"root_id": rootID,
	},
	Extra: map[string]any{
		"recent_uploads": uploads,
	},
}
```

Good debug data includes root ID, selected API endpoint, cache sizes, recent
upload stage, retry count, last non-secret error, and provider task IDs.

Bad debug data includes tokens, cookies, authorization headers, signed URLs,
raw request headers, and full provider responses that may contain secrets.

## Rate Limiting And Encryption Wrappers

Do not build qrypt encryption into a driver. The `pkg/crypt` wrapper handles
content and filename encryption around any `drive.Driver`.

Do not build global bandwidth policy into a driver unless the provider upload
implementation has its own internal concurrency that must be limited at the
native request level. The generic rate-limit wrapper handles normal reads and
uploads. If the driver needs native limiter installation, follow the existing
Quark pattern and keep the public behavior behind `pkg/drive`.

Optional interfaces should keep working through wrappers. When adding a new
optional interface, update wrappers and tests so `crypt` and rate limiting do
not accidentally hide or misreport capabilities.

## Error Conventions

Prefix driver errors with the driver name:

```go
return nil, fmt.Errorf("baidu: list: %w", err)
```

Include operation context such as `list`, `read`, `upload`, `mkdir`, or
`resolve root_path`. Avoid logging or returning credential values.

Respect `context.Context` in all network calls. Cancellation should stop new
requests and unblock long reads or uploads when the provider client supports it.

## Configuration Example

Single mount:

```toml
mount_point = "~/Qrypt"
cache_dir = "/tmp/qrypt-cache"

[[mounts]]
name = "baidu"
type = "baidu"

[mounts.params]
token = "..."
root_path = "/qrypt"

[mounts.cache]
upload_delay = "2s"
upload_workers = 2

[mounts.encryption]
password = "secret"
salt = ""
filename_encryption = "standard"
filename_encoding = "base32"
```

Multiple accounts of the same provider are just multiple mounts with the same
`type` and different `name` values.

## Minimum Tests

Add tests for the factory:

- `drive.New("<name>", params)` returns the concrete driver.
- Missing required params return a clear error.

Add tests for metadata mapping:

- Provider file response maps to `drive.Entry`.
- Directories set `IsDir`.
- File size and modification time are preserved when available.

Add tests for read behavior:

- Full read.
- Range read with non-zero offset and size.
- Provider HTTP/API failures are wrapped with the driver prefix.

Add tests for write behavior when supported:

- `Mkdir` returns the created directory entry.
- `Put` returns the committed remote object.
- `Rename`, `Move`, and `Remove` call the expected provider APIs.

Add tests for debug behavior:

- `DebugSnapshot` includes useful state and excludes secrets.
- `HealthCheck` reports success, failure, and latency.
- `ResolveRemoteName` returns a deterministic mapping when implemented.

Add integration-style tests with a fake provider client when the real provider
requires network credentials. Unit tests should not require real accounts.

## PR Checklist

- Driver lives under `internal/driver/<name>`.
- Driver is registered with `drive.Register`.
- `cmd/qrypt/main.go` imports the driver for registration.
- Required params are validated by the factory.
- `Init` validates credentials and root selection.
- `List` returns only direct children.
- `Read` handles offset and size correctly.
- Writable drivers implement both `drive.Writer` and `drive.Uploader`.
- Debug output does not expose secrets.
- `HealthCheck` respects context and has bounded latency.
- Tests cover success and failure paths.
- Existing wrappers still preserve optional interfaces.
- `go test ./...` passes.
