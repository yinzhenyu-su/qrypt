# Architecture

qrypt is organized as a small set of layers with one-way dependencies. The
main rule is that cloud-drive details stay below `pkg/drive`, filesystem
semantics stay in `pkg/vfs`, and platform mount details stay in
`internal/mount`.

## Layers

```text
cmd/qrypt
  minimal executable entry point

internal/cli
  configuration and commands

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

pkg/drivers/<name>
  provider-specific API clients and metadata mapping

pkg/drivers/all
  bundled driver registration set

pkg/core
  reusable runtime assembly for CLI, mobile, and future clients
```

Dependencies should point downward in this list. Provider drivers must not
import VFS, mount, control, or CLI packages. VFS and mount code must not import
concrete provider packages. These rules are enforced automatically by
`scripts/check-arch.sh` in CI (every PR); when the boundary is violated the
build fails.

### Internal tool layer

Beneath the layer list sits the internal tool layer, which `pkg/*` packages
depend on freely (tools have no downward dependencies of their own):

- `internal/config` — configuration model shared by `pkg/core` and `internal/cli`
- `internal/logging` — logger singleton used across runtime layers
- `internal/timeutil` — NTP-backed clock (fallback to system time)
- `internal/retry`, `internal/fileutil` — retry policies, atomic writes
- `internal/util` — driver HTTP errors, trace redaction
- `internal/util/httpclient`, `internal/util/httputil`, `internal/util/uploadsession`
  — driver HTTP plumbing (request/response, transport, upload sessions)
- `internal/drivecopy` — driver-level direct copy (single-file and directory),
  shared by the sync executor, `pkg/core`, and the CLI; the debug server calls
  through the same helpers
- `internal/contracttest` — the driver contract/benchmark harness (spec
  registry, fixture, xfer/fstest runners, benchmark reports). The debug server
  schedules these through the exported `Specs()` registry; the CLI's
  `debug test`/`debug bench` commands consume the same report types

`internal/control` is the debug-socket HTTP API only. The sync executor must
not depend on it (rule 4 in `check-arch.sh`); `pkg/core` and `internal/cli`
are its host and client and may. Driver-level copy logic lives in
`internal/drivecopy`, contract harnesses in `internal/contracttest` — never
back in `internal/control`.

### VFS domain packages (`internal/vfs/*`)

`pkg/vfs` is the public facade (VFS/Namespace API, capability + health
gates, thin runtime adapters and exported DTO aliases). Its implementation
lives in `internal/vfs` sub-packages, one per domain; `pkg/vfs` wires them
together and never holds domain runtime state:

- `vfstypes` — shared types and path helpers
- `upload` — upload engine, persistent pending store (write-ahead journal),
  and per-path debounce scheduling via the injected `KeyedScheduler`
- `readcache` — durable read-chunk cache, index persistence (debounced via
  the injected `Debouncer`), read/write queues; no other vfs-domain imports
- `delete` — delayed remote delete executor
- `read` — read domain (window/coalescing/chunk cache) and bounded
  read-event history
- `listing` — list domain (directory cache, prefetch, effective view)
- `mutation` — Rename/Mkdir/Remove coordinator use cases with per-use-case
  narrow interfaces
- `observe` — active-op tracking state (no Debug* query API)
- `faultinject` — cancel-injection rule registry (claim/release/fire state
  machine) plus the per-upload `Progress` applicator
- `diagnostics` — read-only DTOs and cross-domain aggregation (cache,
  staging, resolve/consistency, snapshot, health, read events, drivers)
- `scheduler` — neutral real-time keyed scheduler shared by upload and
  delete; deterministic fake timers in tests

Capability contracts are enforced at compile time: `pkg/vfs` asserts the
full diagnostics surface on both `*VFS` and `*Namespace`
(`namespace_diagnostics_test.go`), and `internal/drivecopy` asserts
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
7. Pass the resulting `vfs.FileSystem` to FUSE, CLI commands, mobile bindings,
   or future clients.

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
by domain; the `vfsXxxRuntime` types are thin method-grouping views over the
shared `VFS` state, used to keep each domain's helpers close to its public
operations.

- `vfs.go`: VFS construction, lifecycle, health, cache controls, shared path
  helpers, pending-upload lookup, and path locking. Domain state is grouped
  by ownership into `readState`/`uploadState`/`deleteState` (initialized
  together in `New`, mutated only by their domain's code paths); `debug`
  and `pathLocks` stay top-level because they are single-state and
  cross-domain.
- `driver_runtime.go`: driver capability checks and driver-backed backend
  factory for list, mutation, remote mutation, and debug driver exposure
- `listing.go`: public list operations, directory cache, list coalescing,
  directory prefetch, list backend around driver `List`, and list scheduler
  state
- `read.go`: public read operation and read-slot acquisition entrypoint
- `read_range.go`: range reads, chunk slicing, and bounded driver reads
- `read_window.go`: read window load/coalescing, in-flight wait handling,
  read-path staging flush, chunk availability, read-slot admission,
  prefetch reservations, cache/window/hot-chunk/range-hit state, and the
  read-window driver read / chunk-cache write backend
- `read_prefetch.go`: adjacent read prefetch scheduling and execution
- `read_fast_path.go`: hot chunk cache and cached-range promotion helpers
- `read_keys.go` and `read_debug_helpers.go`: read cache keys and diagnostic
  helpers
- `staging.go` and `staging_write.go`: local write staging, create/write/
  flush/truncate/mtime operations, and write-side pending/staging/hash state
- `upload_write_store.go`: write-side store/hash-tracker/remote-read adapters
  used by staging write operations
- `upload_worker.go`: upload worker loop, quiet-window checks, admission, and
  the worker runtime boundary
- `upload_engine.go`: one pending upload execution (remote preparation,
  driver upload, replacement, failure recording), plus the upload engine
  boundary and its runtime/fault/scheduler seams
- `upload_snapshot.go`: frozen staging snapshot validation, upload hashing,
  read-cache seeding, and the upload-engine snapshot/recheck step
- `upload_commit.go`: upload commit phase - read-cache seed, view commit,
  pending/staging cleanup, and stale-commit rollback
- `upload_runtime.go`: upload runtime and observer adapters over VFS state
- `upload_fault.go`: debug upload-cancel fault injection
- `upload_schedule.go`: upload enqueue, debounce, retry delay, cancellation,
  and concurrency admission (engine scheduler seam + VFS helpers)
- `pending_upload_store.go`: pending upload state/staging cleanup adapter used
  by the upload engine
- `remote_mutation_backend.go`: upload-side remote mutation adapter around
  driver calls used by the upload engine and upload replacement policy
- `upload_replace.go`: temporary upload naming and existing-file replacement
- `upload_schedule.go` and `upload_admission.go`: upload enqueue, debounce,
  retry delay, cancellation, and concurrency admission
- `upload_store.go` and `upload_journal.go`: pending upload state, journal
  persistence, and staging ownership
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
- `read_cache_store.go`, `read_cache_writer.go`, `read_cache_index.go`, and
  `read_cache_eviction.go`: durable read chunks, async write queue, index
  persistence, and eviction
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

A pending upload moves through stages with explicit state ownership. Each
stage owns exactly one piece of state and hands it to the next:

1. **Staging** (`staging.go`, `staging_write.go`): `WriteAt`/`Flush` write
   the local staging file and update the `PendingUpload` record. Ownership:
   `stagingStore` owns the disk file, `uploadStore` owns the pending record
   and its journal; the record holds the staging reference.
2. **Schedule** (`upload_schedule.go`): `enqueueAfter` arms a debounce timer
   in `uploadScheduleState`. Ownership: the timer; cancelled on reschedule,
   upload cancel, or shutdown (`uploadState.Close`).
3. **Worker** (`upload_worker.go`): the timer fires into `uploadState.queue`;
   a worker admits the record (quiet window, admission) and runs the engine.
4. **Engine - snapshot** (`upload_engine.go`, `upload_snapshot.go`):
   `freezeSnapshot` syncs/stats/hashes the staging file into an
   `uploadSnapshot`, then rechecks the pending record. Ownership: the
   snapshot freezes staging contents; the record must still be the latest
   for the path or the upload is skipped (removed/superseded).
5. **Engine - remote** (`upload_engine.go`): `prepareUploadTarget` decides
   replace semantics, then `PutSource` streams the snapshot. Failures are
   recorded via `recordFailure` (permanent vs retryable) and the record
   decides requeue.
6. **Replace** (`upload_replace.go`): `replaceUploadedFile` applies an
   existing-file replacement under the final name.
7. **Commit/Cleanup** (`upload_commit.go`): `finalizeUpload` seeds the read
   cache, commits the entry into `viewState.entries`, removes the pending
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
instant-upload hashes, and driver capabilities. A future backend can target a
driver upload fast path directly without changing mobile or task callers.

UploadService is not a FUSE entry point: clients should not copy files into a
mounted qrypt directory to create upload tasks. FUSE and upload tasks share
VFS/driver state but remain separate protocol adapters.

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
- A new public API needs at least two real call sites before it is exported.
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
