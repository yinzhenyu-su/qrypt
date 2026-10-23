# Architecture

The target ownership rules and migration acceptance criteria are defined in
[Package Boundaries](package-boundaries.md). This document describes the
runtime architecture; when current code is still being migrated, the boundary
contract is authoritative for new dependencies.

qrypt is organized as a small set of layers with one-way dependencies. The
main rule is that cloud-drive details stay below `pkg/drive`, filesystem
semantics stay in `pkg/vfs`, and platform mount details stay in
`pkg/mount`.

## Layers

```text
Mobile app
  -> pkg/mobile (gomobile-safe API, sessions, handles, JSON envelopes)
  -> pkg/core (reusable business engine and runtime composition root)
  -> pkg/vfs (provider-independent filesystem semantics)
  -> pkg/drive (backend contracts and capability model)

cmd/qrypt
  -> internal/cli (commands and desktop runtime integration)
  -> pkg/core / pkg/vfs

pkg/drivers/<name>
  -> pkg/drive

pkg/drivers/all
  bundled driver registration imported by client composition roots

pkg/mount
  FUSE callback adapter and platform mount lifecycle over pkg/vfs

pkg/control
  local debug/command HTTP transports hosted by pkg/core or internal/cli

pkg/crypt
  rclone-compatible encryption wrapper around pkg/drive.Driver
```

Arrows show compile-time dependency direction. In particular, `pkg/mobile`
depends on `pkg/core`; they are not peer layers, and `pkg/core` never depends
on `pkg/mobile`. Provider drivers must not import VFS, mount, control, or CLI
packages. VFS and mount code must not import concrete provider packages. These
direct dependency and ownership rules are enforced automatically from Go
package metadata and declarations by
`scripts/ci/check-arch.sh` in CI (every PR); when the boundary is violated the
build fails.

### Reusable Support Packages

Reusable support packages live in `pkg` when public packages need them. These
packages should remain narrow and avoid depending on higher runtime layers:

- `pkg/config` — configuration model shared by `pkg/core` and `internal/cli`
- `pkg/logging` — logger singleton used across runtime layers
- `pkg/util` — filesystem, OS, size-formatting, and NTP-backed time helpers;
  files use `fileutil_`, `osutil_`, and `timeutil_` prefixes to keep the
  utility domains visible
- `pkg/util` — retry policies
- `pkg/drive` — driver contracts plus shared HTTP error classification and
  redacted diagnostic snippets
- `pkg/drivers/internal/driverutil` — driver trace buffers and metric field
  helpers
- `pkg/drivers/internal/driverutil/httpclient`, `pkg/drivers/internal/driverutil/httputil`, `pkg/drivers/internal/driverutil/uploadsession`
  — driver HTTP plumbing (request/response, transport, upload sessions)
- `pkg/vfs/drivecopy` — driver-level direct copy (single-file and directory),
  shared by the sync executor, `pkg/core`, and the CLI; the debug server calls
  through the same helpers
- `pkg/contracttest` — the driver contract/benchmark harness (spec
  registry, fixture, xfer/fstest runners, benchmark reports). The probe server
  schedules these through the exported `Specs()` registry; the CLI's
  `probe test`/`probe bench` commands consume the same report types

`pkg/control` owns only local HTTP transport and route encoding. Its debug
server is read-only; its command server is the separately constructed mutable
surface. The sync executor's dependency policy must not include it;
`pkg/core` and `internal/cli` are its host and client and may. Driver-level copy logic lives in
`pkg/vfs/drivecopy`, contract harnesses in `pkg/contracttest` — never
back in `pkg/control`.

### VFS domain packages (`pkg/vfs/*`)

`pkg/vfs` is the public filesystem facade (VFS/Namespace API and stable file
capabilities). Its implementation
lives in `pkg/vfs` sub-packages, one per domain; `pkg/vfs` wires them
together and holds one owner object per domain rather than the domain's
individual runtime collaborators:

- `vfstypes` — shared types and path helpers
- `upload` — upload engine, persistent pending store (write-ahead journal),
  per-path scheduling, quiet-window checks, and upload admission
- `readcache` — durable read-chunk cache: index persistence (debounced via
  the injected `Debouncer`), async write queue, and the admission/replacement
  policy (`eviction.go`, which derives its budget tree from the index on each
  drain rather than maintaining byte counters); no other vfs-domain imports
- `delete` — delayed remote delete executor
- `read` — read domain (window/coalescing/chunk cache) and bounded
  read-event history
- `listing` — list domain (directory cache, prefetch, effective view)
- `mutation` — Rename/Mkdir/Remove coordinator use cases with per-use-case
  narrow interfaces
- `observe` — active-op tracking state (no Debug* query API)
- `faultinject` — cancel-injection rule registry (claim/release/fire state
  machine), control/test capability DTOs, plus the per-upload
  `Progress` applicator; fault-injection types are not re-exported by the
  `pkg/vfs` facade
- `diagnostics` — read-only DTOs and cross-domain aggregation (cache,
  staging, resolve/consistency, snapshot, health, read events, drivers), plus
  the capability interfaces consumed by control, CLI and test tools;
  diagnostics DTOs live in `pkg/vfs/diagnostics`
- `scheduler` — neutral real-time keyed scheduler shared by upload and
  delete; deterministic fake timers in tests

Capability contracts are enforced at compile time: `pkg/vfs` asserts the
full diagnostics surface on both `*VFS` and `*Namespace`
(`namespace_diagnostics_test.go`), and `pkg/vfs/drivecopy` asserts
`DriverCopySource` on `*Namespace`.

## Runtime Assembly

`pkg/core` is the reusable composition root:

1. Load config.
2. Build each concrete `drive.Driver` from the registered `pkg/drivers/*`
   packages.
3. Install driver state stores where supported.
4. Install bandwidth limiting into drivers that support it, then optionally wrap with `pkg/crypt`.
5. Build one `pkg/vfs.VFS` per configured mount.
6. Combine mounts into `pkg/vfs.Namespace`.
7. Expose filesystem and business operations to clients. `pkg/mobile` calls
   this Core API and adapts it for gomobile; desktop CLI/FUSE paths may also use
   the constructed `vfs.FileSystem` where appropriate.

This keeps construction policy out of VFS and keeps provider details out of the
mount layer.

`internal/cli` and `pkg/mobile` import `pkg/drivers/all` for the full bundled
driver set.

## Drive Contracts

`pkg/drive.Driver` is the minimum read contract. Optional runtime behavior is
reported through `drive.Capabilities(driver)` and small focused interfaces:

- `Writer`
- `SourceUploader`
- `SpaceQuerier`
- `PathResolver`
- `Debugger`
- `RemoteNameResolver`
- `ForeignEntryLister`

Use `drive.HasCapability` before enabling optional behavior in higher layers.
Wrappers whose concrete method set is wider than their real runtime support
must implement `drive.CapabilityReporter`.

Upload sources may expose precomputed hashes through `drive.HashProvider`; this
is source metadata, not a driver capability.
Drivers that benefit from hashes before upload can declare the needed algorithms
with `drive.UploadHashRequirements`.

Construction-time hooks such as state-store installation and native bandwidth
limiter installation are not runtime capabilities. They are used only during
assembly.

## VFS Responsibilities

`pkg/vfs` owns provider-independent filesystem semantics. Files are grouped
by domain. Cross-domain adapters expose only the view, driver, invalidation or
fault-injection seam needed by the receiving domain; they do not reconstruct
another domain's internal state.

- `vfs.go`: VFS construction, lifecycle, health, cache controls, shared path
  helpers, pending-upload lookup, and path locking. Upload state is owned by
  one `pkg/vfs/upload.Domain`, which contains the service/store, hash tracker, target
  index and engine; `debug` and `pathLocks` stay top-level because they are
  single-state and cross-domain.
- `driver_runtime.go`: driver capability checks and driver-backed backend
  factory for list, mutation, remote mutation, and debug driver exposure
- `listing.go`: public list operations, directory cache, list coalescing,
  directory prefetch, list backend around driver `List`, and list scheduler
  state
- `read.go`: public read operation and read-slot acquisition entrypoint
- `read_range.go`: range reads, chunk slicing, and bounded driver reads
- `pkg/vfs/read/reader.go`: read window load/coalescing, in-flight wait handling,
  read-path staging flush, chunk availability, read-slot admission,
  prefetch reservations, cache/window/hot-chunk/range-hit state, and the
  read-window driver read / chunk-cache write backend
- `read_prefetch.go`: adjacent read prefetch scheduling and execution
- `read_fast_path.go`: hot chunk cache and cached-range promotion helpers
- `read_keys.go` and `read_debug_helpers.go`: read cache keys and diagnostic
  helpers
- `staging.go`: local write staging, create/write/flush/truncate/mtime
  operations, and write-side pending/staging/hash transitions
- `pkg/vfs/upload/domain.go`: the single upload owner assembled from service/store,
  hash tracker, target index and engine
- `upload.go`: VFS-facing upload methods, domain assembly, worker loop,
  snapshot hashing and read-cache seeding
- `pkg/vfs/upload/worker.go`: current-generation, quiet-window and admission
  checks behind `Service.ProcessPending`
- `pkg/vfs/upload/engine.go` and `commit.go`: one claimed generation's remote
  preparation, upload, replacement, failure recording, commit and rollback
- `upload_recovery.go`: persisted staging checks and pending-upload resume
  after restart; corrupt frozen generations remain available for recovery
- `upload_runtime.go`: view, observer, snapshot, fault and write-hash adapters
  at the upload domain's cross-domain seams
- `driver_runtime.go`: upload-side remote mutation adapter around
  driver calls used by the upload engine and upload replacement policy
- `pkg/vfs/upload/replace.go`: temporary upload naming and existing-file
  replacement
- `pkg/vfs/upload/service.go`: upload enqueue, quiet-window scheduling,
  cancellation, worker claims and concurrency admission
- `pkg/vfs/upload/store.go`: pending upload state, journal persistence and
  staging ownership
- `mutation.go`: public mutation operations, remote mkdir/list/rename/move
  backend, and local view/cache/pending-upload state boundaries for mkdir,
  rename, remove, and directory-copy flows
- `delete_executor.go`: delayed delete execution, remote delete runtime
  boundary, and delete scheduler state
- `delete_task.go`: delete task source boundary for deleted overlay records,
  restore, retry scheduling, and failure cleanup
- `visibility_overlay.go`: delete visibility, rename overlay, copy-hidden,
  unavailable filtering, and local child visibility state
- `resolve.go`: virtual path resolution against cached and remote entries,
  and the resolve-path cached-entry lookup / child commit boundary
- `view_state.go`: virtual view cache state, cached path rebase, list
  invalidation, local directory TTL, and local mtime overlays
- `stores.go`: read/upload store types and constructors
- `namespace.go`: multi-mount namespace contract (interfaces, errors,
  construction, lifecycle, path resolution)
- `namespace_read.go`, `namespace_write.go`, `namespace_task.go`,
  `namespace_query.go`: namespace route-by-operation (reads/cache control,
  writes/mutations, tasks, upload/space queries)
- `task.go`: VFS background task source integration for upload/delete tasks,
  including the upload task source boundary
- `debug.go`: debug schema types and debug process start time
- `debug_snapshot.go`: VFS and namespace snapshot aggregation, queue timers,
  overlay state, and read runtime counters
- `debug_active.go`: active debug operation state for begin, update, finish,
  and snapshot
- `debug_cache.go`: debug cache snapshot for read-cache and upload-journal
  state
- `debug_read.go`: debug read events for read sequence, cache counters,
  history append, history reads, and reset
- `debug_staging.go`: debug staging for pending uploads, active upload state,
  and local staging directory scans
- `debug_resolve.go`: debug resolve/consistency for pending lookup, cache IDs,
  driver diagnostics, remote names, and foreign entries
- `debug_health.go`: debug health for local health state and driver metric
  health merge
- `debug_upload.go`: debug upload active snapshot, history, progress, event,
  metadata, and extra-field state
- `debug_fault.go`: debug upload cancel fault injection for arm, clear, list,
  match, expiration, and fired marking
- `capability.go`, `diskfree.go`, `mount.go`, and `path.go`: capability
  reporting, disk space helpers, mount metadata, and path utilities
- `dir_prefetch_state.go`, `list_state.go`, `path_lock_state.go`, and
  `read_state.go`: small state containers for their domains
  health/driver listing

VFS also keeps mutable runtime state grouped by ownership instead of keeping
every mutex and map directly on `VFS`:

- `viewState`: cached virtual entries, directory list cache, locally created
  directory visibility, local mtime overlays, and delete/rename visibility
  overlays
- `deleteTaskState`: delayed delete timers, active remote deletes, and delete
  failures; it shares the delete overlay lock so visibility and execution state
  remain consistent
- `uploadScheduleState`: upload timers and delayed enqueue scheduling
- `uploadDebugState`: active upload snapshots and upload history
- `uploadFaultState`: debug upload cancellation fault injection state
- `uploadAdmission`: upload concurrency admission state
- `readHistoryState`: VFS read history and read operation sequence
- `activeDebugState`: currently active read/window/prefetch diagnostic
  operations
- `readSlotState`, `readWindowState`, `readFastPathState`, and
  `readPrefetchState`: read concurrency slots, in-flight read windows,
  hot-chunk/range-hit acceleration, and read prefetch tracking
  - `readFastPathState` groups `hotChunkState` for resident chunk data and
    `rangeHitState` for cached range promotion counters
- `dirPrefetchState`: directory prefetch in-flight/cooldown state and
  background context
- `listState`: in-flight directory list coalescing
- `pathLockState`: per-path serialization for staging mutation, truncate, and
  generation rotation

The grouping is an internal maintenance boundary, not a public extension API.
Callers should still use `vfs.FileSystem`, `vfs.Namespace`, or `pkg/core`.
Tests should prefer constructing VFS through `New` unless they intentionally
exercise a small internal helper with explicit state initialization.

`vfs.FileSystem` is the file-operation contract. Upload internals such as
`PendingUpload` are exposed through the narrower `UploadInspector` diagnostic
interface, not as part of the base filesystem contract. VFS and Namespace also
implement `task.Source`, so core task management can consume upload/delete
background tasks without a legacy adapter layer.

VFS should not know provider API details. It should operate on `drive.Entry`
and optional `drive` capabilities only.

## Upload State Flow

A pending upload moves through stages owned by `pkg/vfs/upload.Domain`. The journal is
the source of truth; schedules and worker claims are disposable runtime
indexes over it:

1. **Staging** (`staging.go`): `WriteAt`/`Flush` write
   the local staging file and update the `PendingUpload` record and matching
   incremental hash state. The record holds the staging reference.
2. **Schedule** (`pkg/vfs/upload/service.go`): `EnqueueAfter` replaces the
   path's deadline in the journal-backed minimum-heap index. It does not move
   pending content into a production channel or create a timer per file.
3. **Worker** (`upload.go`, `pkg/vfs/upload/worker.go`): `Service.Receive`
   claims an eligible path, reloads its current journal record, and
   `ProcessPending` applies current-generation, quiet-window and admission
   checks before calling the engine.
4. **Engine - claim/snapshot** (`pkg/vfs/upload/engine.go`, `upload.go`): the
   engine atomically marks the generation claimed before
   `freezeSnapshot` syncs/stats/hashes the staging file into an
   `uploadSnapshot`, then rechecks the pending record. Ownership: the
   snapshot freezes staging contents; the record must still be the latest
   for the path or the upload is skipped (removed/superseded).
5. **Engine - remote** (`pkg/vfs/upload/engine.go`): target preparation decides
   replace semantics, then `PutSource` streams the snapshot. Failures are
   recorded via `recordFailure` (permanent vs retryable) and the record
   decides requeue.
6. **Replace** (`pkg/vfs/upload/replace.go`): `replaceUploadedFile` applies an
   existing-file replacement under the final name.
7. **Commit/Cleanup** (`pkg/vfs/upload/commit.go`): `finalizeUpload` seeds the
   read cache, commits the entry into the effective view, removes the pending
   record (`RemoveIfUnchanged` - a moved-on record rolls the upload back
   via `rollbackUploadedEntry`), then removes the staging file.

The transfer of ownership is checked at each handoff: the record must be
unchanged (same generation) or the upload is superseded and rolled back;
staging is only removed once the record is gone; the uploaded entry is only
committed after the record confirms it.

## Upload Responsibilities

`pkg/core.UploadService` is the business-level upload boundary for clients,
mobile bindings, CLI flows, and directory watchers. It owns default destination
resolution, default upload directory creation, conflict policy, remote
completion confirmation, and cache refresh after successful upload.

Destination resolution is isolated in `UploadDestinationResolver`. It maps empty
or relative upload destinations to `[upload].default_mount/default_path` and
returns the default directory that UploadService should create before uploading.

UploadService writes through `UploadBackend`. The default `VFSUploadBackend`
wraps the internal `vfs.FileSystem` API so it can reuse staging, retry,
instant-upload hashes, and driver capabilities. Direct upload stream tasks use
the VFS `SourceUploader` fast path when the destination driver can stream a
`drive.ReadOnlyFileSource`; otherwise they can still fall back to staging.

UploadService is not a FUSE entry point: clients should not copy files into a
mounted qrypt directory to create upload tasks. FUSE and upload tasks share
VFS/driver state but remain separate protocol adapters.

## Mount Responsibilities

`pkg/mount` translates FUSE callbacks into `vfs.FileSystem` calls and
contains platform mount/unmount behavior. FUSE operation files are grouped by
behavior:

- `adapter_fuse.go`: common attribute/access operations
- `adapter_fuse_dir.go`: directory operations
- `adapter_fuse_file.go`: file open/read/write/truncate operations
- `adapter_fuse_mutation.go`: unlink/rmdir/rename operations
- `adapter_fuse_metadata.go`: chmod/chown/time/statfs operations
- `adapter_fuse_xattr.go`: extended attributes

Platform-specific FUSE mount attributes are configured under `[mount.options]`
and passed through by `pkg/mount`; the callback adapter does not emulate
Apple-specific metadata policy.

Mount code should not implement cloud-drive semantics. It should convert FUSE
inputs and errors, track handles, and delegate behavior to VFS.

## Extension Rules

- Add a new provider under `pkg/drivers/<name>`.
- Register it through `drive.Register`.
- Add it to `pkg/drivers/all` when it should be bundled by qrypt clients.
- Keep provider parameters under `[[mounts]].params`.
- Use `drive.Capabilities` in diagnostics and contract tests.
- Preserve optional capabilities when adding wrappers.
- Add provider behavior tests with fake clients or fake servers; do not require
  real accounts for unit tests.
- Prefer adding VFS behavior in the responsibility file that owns that behavior,
  not in a generic catch-all file.

## Complexity Guardrails

These rules keep the codebase from growing unbounded complexity; they apply to
all new code:

- New logic goes behind the caller's own narrow interface first; do not widen
  a package API to satisfy one caller.
- A new public API needs at least two real call sites before it is exported,
  unless an exported method is necessary for a narrow cross-package boundary;
  do not add a second call site solely to satisfy this rule.
- A new state field must document its owner and lock discipline.
- Every concurrency bug fix ships with a regression test.
- Split a large file's existing responsibilities before adding new code to it.
- Benchmarks are diagnostics only; keep them out of the business state machine.
- `context.Background()` is allowed only for process-level initialization and
  root lifecycle contexts. API calls, background tasks and shutdown paths
  derive from the owning context (mobile sessions keep a lifecycle context
  canceled at close; VFS background deletes use the VFS lifecycle context);
  shutdown uses a bounded timeout instead of a bare background.

## Verification

Before merging architectural changes, run:

```sh
go test ./...
git diff --check
```

For driver changes, also verify `docs/for-developer/driver-development.md` and
add capability or CRUD contract coverage when the driver supports writes,
uploads, or debug snapshots. Use CRUD tests for explicit active driver probing;
runtime mount health is derived from recent VFS operations.
