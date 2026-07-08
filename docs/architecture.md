# Architecture

qrypt is organized as a small set of layers with one-way dependencies. The
main rule is that cloud-drive details stay below `pkg/drive`, filesystem
semantics stay in `pkg/vfs`, and platform mount details stay in
`internal/mount`.

## Layers

```text
cmd/qrypt
  configuration, commands, runtime assembly

internal/control
  debug socket HTTP API over runtime snapshots

internal/mount
  FUSE callback adapter, platform mount lifecycle

pkg/vfs
  provider-independent filesystem semantics

pkg/crypt
  rclone-compatible encryption wrapper around drive.Driver

pkg/drive
  backend contracts, optional capability model, registry

internal/driver/<name>
  provider-specific API clients and metadata mapping
```

Dependencies should point downward in this list. Provider drivers must not
import VFS, mount, control, or CLI packages. VFS and mount code must not import
concrete provider packages.

## Runtime Assembly

`cmd/qrypt/filesystem_builder.go` is the composition root:

1. Load config.
2. Build each concrete `drive.Driver` from `internal/driver/*`.
3. Install driver state stores where supported.
4. Wrap with bandwidth limiting and optional `pkg/crypt`.
5. Build one `pkg/vfs.VFS` per configured mount.
6. Combine mounts into `pkg/vfs.Namespace`.
7. Pass the resulting `vfs.FileSystem` to `internal/mount` or CLI commands.

This keeps construction policy out of VFS and keeps provider details out of the
mount layer.

## Drive Contracts

`pkg/drive.Driver` is the minimum read contract. Optional runtime behavior is
reported through `drive.Capabilities(driver)` and small focused interfaces:

- `Writer`
- `Uploader`
- `FileUploader`
- `SpaceQuerier`
- `PathResolver`
- `Debugger`
- `HealthChecker`
- `RemoteNameResolver`
- `ForeignEntryLister`

Use `drive.HasCapability` before enabling optional behavior in higher layers.
Wrappers whose concrete method set is wider than their real runtime support
must implement `drive.CapabilityReporter`.

Construction-time hooks such as state-store installation and native bandwidth
limiter installation are not runtime capabilities. They are used only during
assembly.

## VFS Responsibilities

`pkg/vfs` owns provider-independent filesystem semantics. It is split by
internal responsibility:

- `vfs.go`: public VFS operations and path-level orchestration
- `listing.go`: directory cache, list coalescing, directory prefetch
- `reading.go`: chunk reads, read cache, read prefetch
- `writeback.go`: pending uploads, retry, snapshots, upload scheduling
- `mutation.go`: delayed delete, rename overlays, hidden local state
- `cache.go`: durable read cache and pending journal
- `staging.go`: local write staging
- `namespace.go`: multi-mount namespace
- `debug.go`: snapshots and debug reports

VFS should not know provider API details. It should operate on `drive.Entry`
and optional `drive` capabilities only.

## Mount Responsibilities

`internal/mount` translates FUSE callbacks into `vfs.FileSystem` calls and
contains platform mount/unmount behavior. FUSE operation files are grouped by
behavior:

- `adapter_fuse.go`: common attribute/access operations
- `adapter_fuse_dir.go`: directory operations
- `adapter_fuse_file.go`: file open/read/write/truncate operations
- `adapter_fuse_mutation.go`: unlink/rmdir/rename operations
- `adapter_fuse_metadata.go`: chmod/chown/time/statfs operations
- `adapter_fuse_xattr.go`: extended attributes
- `adapter_apple.go`: macOS Finder metadata compatibility

Mount code should not implement cloud-drive semantics. It should convert FUSE
inputs and errors, track handles, and delegate behavior to VFS.

## Extension Rules

- Add a new provider under `internal/driver/<name>`.
- Register it through `drive.Register`.
- Keep provider parameters under `[[mounts]].params`.
- Use `drive.Capabilities` in diagnostics and contract tests.
- Preserve optional capabilities when adding wrappers.
- Add provider behavior tests with fake clients or fake servers; do not require
  real accounts for unit tests.
- Prefer adding VFS behavior in the responsibility file that owns that behavior,
  not in a generic catch-all file.

## Verification

Before merging architectural changes, run:

```sh
go test ./...
git diff --check
```

For driver changes, also verify `docs/driver-development.md` and add capability
or CRUD contract coverage when the driver supports writes, uploads, health, or
debug snapshots.

