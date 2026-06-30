# qrypt Driver Contract

## Core Interfaces

All drivers implement `drive.Driver`:

```go
Init(ctx context.Context) error
Drop(ctx context.Context) error
List(ctx context.Context, parentID string) ([]drive.Entry, error)
Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error)
```

Add optional interfaces only when supported:

- `drive.Uploader`: stream upload from an `io.Reader`.
- `drive.FileUploader`: upload directly from a local file path when backend benefits from it.
- `drive.Writer`: mkdir, remove, rename, move.
- `drive.Debugger`: expose safe driver snapshot.
- `drive.HealthChecker`: explicit backend health check.
- `drive.SpaceQuerier`: backend capacity.
- `drive.RemoteNameResolver`: map plaintext name to backend remote name for debug.

## Entry Semantics

`drive.Entry` must be stable and VFS-friendly:

- `ID`: native backend ID or normalized backend path used by future driver calls.
- `ParentID`: native parent ID/path used by future list/write calls.
- `Name`: basename visible to qrypt users. Raw drivers return plaintext names.
- `IsDir`: true only for directories.
- `Size`: file size in bytes; zero is acceptable for directories.
- `ModTime`: backend modification time. Use zero value if unknown; do not use `time.Now()` as a fake remote time.

VFS resolves paths by repeated `List(parentID)` calls and matching `Entry.Name`. Do not return names containing parent path prefixes.

## Read Semantics

`Read(ctx, entry, offset, size)` must follow qrypt conventions:

- Reject negative offset or size.
- `size > 0`: return up to `size` bytes from `offset`.
- `size == 0`: return from `offset` to EOF.
- `offset >= entry.Size` for known nonzero size: return an empty reader, not an error.
- HTTP-backed drivers should clamp range end to `entry.Size - 1` when size is known.
- Treat HTTP 416 as EOF only when the requested offset is at or past known EOF. Otherwise surface it as a read error.
- Always close response bodies on errors.

## Write Semantics

Raw drivers receive plaintext from VFS:

- `Put(parentID, name, size, body)` receives plaintext `name` and plaintext body.
- `PutFile(parentID, name, size, localPath)` receives a local staging file containing plaintext.
- Crypt wrapping happens above raw drivers. A raw driver must not encrypt or decrypt qrypt content.

Writer operations should use backend-native IDs:

- `Mkdir(parentID, name)` returns the created or existing directory entry.
- `Remove(entry)` removes the exact entry.
- `Rename(entry, newName)` changes basename in same parent.
- `Move(entry, newParentID, newName)` changes parent and optionally basename.

## Cache And Staging Boundaries

VFS owns read cache, staging, pending journal, upload queues, and directory list cache.

- Drivers must not read or write qrypt cache directories.
- Read cache stores VFS-visible plaintext bytes. It is keyed by `Entry.ID`; on disk qrypt hashes IDs before creating batch filenames.
- Staging stores plaintext pending upload data, even for encrypted mounts.
- Pending journal stores virtual path, parent ID, name, staging local path, size, retry state, and upload errors.
- If a mount switches between plain and crypt mode, stop qrypt and clear that mount's cache directory first.

## Config And Schema

For each migrated driver:

- Use `drive.Register(name, factory, ParamDef...)`.
- Mark auth tokens, cookies, passwords, refresh tokens, and access keys as `Secret: true`.
- Validate all required params in constructor or `Init`.
- Add params to `qrypt.schema.json`.
- Use `root_id`, `root_path`, or backend-specific root fields deliberately. If both ID and path exist, document precedence in code/tests.
- Keep `qrypt.toml` examples minimal and avoid committing live secrets.

## URL And Path Handling

When migrating WebDAV or path-based HTTP backends:

- Escape URL path segments independently.
- Do not concatenate decoded paths directly into URLs.
- Preserve backend IDs as returned by list APIs when they are stable.
- Decode URL hrefs only after parsing absolute URLs.
- Test Unicode, spaces, `#`, `?`, `%`, and trailing slash behavior.

## Error Mapping

Use errors that qrypt/VFS can reason about:

- Not found should become `drive.ErrNotFound` or a wrapped equivalent when available in local patterns.
- Existing directory/file conflicts should be distinguishable from transient backend errors.
- Auth failures should mention the driver and operation but not secrets.
- Rate-limit, retry, and async task states should expose safe details through debug snapshots.

## Debug Snapshot Guidance

`drive.Debugger` should return safe, concise state:

- `Driver`: driver name.
- `Health`: `"unknown"`, `"ok"`, or backend-specific coarse status.
- `Stats`: root ID/path, user ID, ordering, feature flags, counters.
- `Extra`: last non-secret error, active backend upload task IDs, crypt marker only if wrapping.

Never include cookies, tokens, passwords, authorization headers, or full signed download URLs.

## OpenList Migration Notes

Treat OpenList code as behavioral reference:

- Extract API endpoints, request bodies, response schemas, pagination, auth refresh, upload commit flow, and error codes.
- Rebuild qrypt code around `drive.Driver` and optional interfaces instead of porting OpenList abstractions.
- Replace OpenList path abstractions with qrypt `Entry.ID`/`ParentID` semantics.
- Replace OpenList cache/meta assumptions with VFS-owned cache/staging.
- Add tests before live debugging when adapting tricky backend behavior.
