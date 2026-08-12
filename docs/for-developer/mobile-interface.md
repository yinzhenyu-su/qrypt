# Mobile Interface

This document is for Android and iOS developers integrating qrypt through
`pkg/mobile`. It explains the recommended call flows first, then lists the API
surface by category.

## Integration Model

Mobile clients should call `pkg/mobile`; they should not depend on `internal/cli`
or qrypt desktop paths.

```text
Mobile app code
-> gomobile binding
-> pkg/mobile
-> pkg/core
-> pkg/vfs.FileSystem
-> pkg/drive.Driver
```

`pkg/core` owns engine construction: it loads `qrypt.toml`, applies the runtime
layout supplied by the app, builds the VFS namespace, and exposes path-based
filesystem operations.

`pkg/mobile` owns gomobile-friendly sessions, handles, JSON envelopes, byte-array
read/write functions, and task stream handles.

The mobile binding imports `pkg/drivers/all`, so the generated binding registers
the same bundled driver set as the CLI.

## Call Semantics

Mobile APIs are split by caller intent:

```text
short request       returns data or mutates one small piece of state
stream handle       lets the app move bytes without JSON/base64
task                represents long-running or multi-item work
task event stream   drives UI updates without polling
```

### Threading

Every function in this interface is blocking. In particular
`ReadTaskEventsJSON(handleID, waitMS)` with `waitMS > 0` blocks until an
event arrives or the timeout expires. Call all APIs from a background thread
or coroutine; never call them from the Android main thread / iOS main queue,
or the UI will freeze or ANR.

### Handle IDs Are Not Durable

File, virtual file, upload stream, download stream, and task event handles
live in an in-memory registry inside the mobile session. They are invalidated
by `CloseJSON`, `ReloadConfigJSON`, and process death. Never persist a handle
ID across restarts. Task IDs are durable (persisted to the task store) and
can be used to reload state after a process restart via `GetTaskJSON` /
`ListTasksJSON`.

Use `deadlineMS` only for a single request or handle operation. A deadline error
means that call stopped waiting; it does not mean a task failed unless the app
explicitly called cancel, fail, or dismiss.

Use `waitMS` only for event reads. `ReadTaskEventsJSON(handleID, waitMS)` treats
`waitMS <= 0` as a non-blocking poll, while `waitMS > 0` is long polling and
returns an empty event array on timeout.

Use `Commit*ItemJSON` to finish app-owned input/output for one stream item. A
commit releases the handle. Upload commit queues cloud upload and returns before
remote completion; download commit validates that the app has acknowledged all
bytes and then marks the item complete.

Use `Pause*ItemJSON` when the app intentionally drops a stream handle but wants
to resume later. Use `Fail*ItemJSON` when the app's input/output stream failed
and qrypt should record a retryable item error. Use `CancelTaskItemJSON` or
`CancelTaskJSON` when the user wants the work canceled. Use `DismissTaskJSON` for
the UI delete-row action.

## Runtime Layout

Always pass an explicit runtime layout JSON to `ImportConfigJSON`,
`OpenImportedJSON`, or `OpenJSON`. The layout tells qrypt where each class of
runtime data belongs.

Suggested Android mapping:

```text
filesDir/qrypt/
  config/qrypt.toml
  upload/
  state/
    driver/
  logs/

cacheDir/qrypt/
  read/
  thumbnail/
  tmp/
```

Example:

```json
{
  "config_dir": "<filesDir>/qrypt/config",
  "storage": {
    "read_cache_dir": "<cacheDir>/qrypt/read",
    "thumbnail_cache_dir": "<cacheDir>/qrypt/thumbnail",
    "upload_dir": "<filesDir>/qrypt/upload",
    "state_dir": "<filesDir>/qrypt/state",
    "log_dir": "<filesDir>/qrypt/logs",
    "tmp_dir": "<cacheDir>/qrypt/tmp"
  }
}
```

qrypt stores mount-specific data under the provided roots:

```text
<read_cache_dir>/<mount>
<upload_dir>/<mount>
<state_dir>/driver/<mount>
```

On Android, driver state and upload sessions should stay under app-private
`filesDir`, while read cache, thumbnails, and temp data can live under
`cacheDir`. iOS should map the same runtime classes to app-private Documents,
Library, Caches, or tmp directories according to its storage policy.

## Platform-Authorized Directories

Use the bundled `scopedfs` driver when the app wants to expose a user-authorized
directory as a qrypt mount.

```toml
[[mounts]]
name = "phone"
type = "scopedfs"

[mounts.params]
root_token = "<app-owned grant token>"
root_id = "root"
```

`root_token` is opaque to qrypt. Android apps should map it to a persisted SAF
tree URI obtained from `ACTION_OPEN_DOCUMENT_TREE` and
`takePersistableUriPermission`. iOS apps should map it to stored
security-scoped bookmark data created from a Document Picker folder URL.

Before opening a config containing `type = "scopedfs"`, install the platform
backend:

```text
SetScopedFSBackendJSON(backend)
OpenJSON(configPath, runtimeJSON)
```

The backend implements directory operations with platform APIs:

| Platform | Backend mapping |
| --- | --- |
| Android | SAF `ContentResolver` / `DocumentsContract` |
| iOS | security-scoped `URL` plus `FileManager` / file handles |

`scopedfs` supports normal browse/read/write/remove/rename and direct upload
without qrypt staging. It does not advertise resumable source upload or mtime
writing, so interrupted direct uploads restart from byte zero and VFS uses the
same temporary-name cleanup path as other non-resumable direct drivers.

## Recommended Flows

### Open qrypt

Use import/open when the app receives or bundles a config file:

```text
ImportConfigJSON(srcPath, runtimeJSON)
OpenImportedJSON(runtimeJSON)
```

Use direct open when the app already has the intended config path:

```text
OpenJSON(configPath, runtimeJSON)
```

`ImportConfigJSON` copies the config to `<config_dir>/qrypt.toml` and clears
desktop runtime paths such as `mount_point`, `storage.*_dir`, and log file paths.
The supplied runtime layout is then applied when opening the imported config.

### Browse Files

Use these for file-browser screens:

```text
ListJSON(coreID, path, deadlineMS)
ListPageJSON(coreID, path, cursor, limit, deadlineMS)
StatJSON(coreID, path, deadlineMS)
FileInfoJSON(coreID, path, deadlineMS)
ValidateResumeJSON(coreID, path, fileID, size, modTime, deadlineMS)
```

Use `ListPageJSON` for large directories. It returns a deterministic,
name-sorted page (`entries`) plus a `next_cursor`; pass the cursor back for
subsequent pages and stop when `next_cursor` is empty. Entries are sorted by
name (then id), and the cursor encodes the last entry's `(name, id)`, so
paging stays correct even when a directory contains entries that share a
name. Treat `next_cursor` as opaque — always pass back exactly what qrypt
returned. `limit <= 0` returns the whole listing without a cursor. The list
cache inside qrypt makes later pages cheap after the first fetch.

Use `ValidateResumeJSON` before resuming app-managed reads or downloads when the
app has cached file metadata. `fileID` is the remote driver file ID returned by
`StatJSON` or `FileInfoJSON`, not a task item ID.

### Read Media

Do not transfer media bytes through JSON. JSON encodes `[]byte` as base64 and
creates large temporary strings.

Use byte-buffer APIs for playback, preview, and other large reads:

```text
OpenFileJSON(coreID, path, optionsJSON)
ReadAtInto(handleID, offset, dst, deadlineMS)
OpenVirtualFileJSON(coreID, path, mode, deadlineMS)
ReadVirtualFileAtInto(handleID, offset, dst, deadlineMS)
CloseVirtualFileJSON(handleID)
CloseFileJSON(handleID)
```

`optionsJSON` may be empty. Supported fields are:

```json
{"deadline_ms": 5000, "priority": "high"}
```

`priority` accepts `normal` or `high`.

Use `deadlineMS` for seek-heavy playback so the app can stop waiting on stale
reads. Do not use JSON APIs for media bytes.

Cancel in-flight reads when the user cancels a preview or the player seeks
past a stalled range:

```text
CancelFileReadJSON(handleID)
CancelVirtualReadJSON(handleID)
```

These abort the current reads for the handle without closing it; the handle
remains usable for future reads. `CloseFileJSON` / `CloseVirtualFileJSON`
also cancel any in-flight reads.

### Upload App-Owned Files

Pick the upload entry point that matches the source of the data:

| API | Input source | Behavior | Task visible / events |
| --- | --- | --- | --- |
| `CreateUploadTaskJSON` | app input stream (SAF `content://` on Android, security-scoped on iOS) | creates `upload_stream_batch`; app writes chunks | yes |
| `CreateDirectUploadTaskJSON` | reopenable source token; with the current gomobile wrapper this is a qrypt-readable local path unless the embedding app wires a custom source provider | creates `upload_stream_direct`; qrypt reads the source directly, hashes first, then uploads without staging when supported | yes |
| `CreateLocalUploadTaskJSON` | stable local filesystem path | creates a user-scope `upload_remote`/`upload_batch` task; waits for stability when `wait_stable` | yes |
| `UploadLocalFileJSON` | stable local filesystem path | convenience wrapper that streams the file into an `upload_stream_batch` task and returns after staging | yes |
| `CreateTaskJSON(type="upload_batch")` | qrypt-readable local paths | generic task API | yes |

All upload paths create user-visible tasks: they appear in the default
`ListTasksJSON` result (which defaults to user-scope tasks) and emit
`task_updated` events. The VFS sync tasks that carry out the actual cloud
upload (`upload_remote`, sync scope) stay hidden from the default list and do
not emit events; user tasks surface their cloud progress by watching those
sync tasks internally.

Use `CreateUploadTaskJSON` when the app owns the input stream. On Android this
is usually a SAF/content URI InputStream. On iOS this can be a security-scoped
file or provider stream.

Use `CreateDirectUploadTaskJSON` when qrypt can reopen the source later from a
stable token. `source_path` in the item JSON is the source token. On Android,
call `SetUploadSourceOpenerJSON` once (it applies to all open and future
sessions) with an implementation of the `UploadSourceOpener` interface that
maps the token back to `ContentResolver.openAssetFileDescriptor(uri, "r")`
and seeks to the requested offset. On iOS, map the token to a security-scoped
bookmark or file-provider URL, call `startAccessingSecurityScopedResource`,
open the file, seek, and close/stop access when qrypt closes the handle. Without
a custom opener, `source_path` is opened as a local filesystem path by default.
qrypt reads the source once to compute upload hashes for instant upload, then
reopens it (at the requested offset) for cloud upload. If the destination driver
supports source upload, qrypt uploads directly without qrypt staging.
Drivers that also advertise resumable source upload can continue an interrupted
provider upload; non-resumable direct drivers, such as scopedfs, localfs, and
generic WebDAV, restart the direct upload from the beginning after interruption.

Create the task:

```json
{
  "items": [
    {
      "item_id": "local-1",
      "source_path": "/app/private/cache/a.jpg",
      "dest_path": "/drive/photos/a.jpg",
      "size": 123456
    }
  ]
}
```

If `[upload]` config sets `default_mount`, `dest_path` may be omitted or passed
as a relative path. qrypt resolves it under
`/<default_mount>/<default_path>/...`. A full absolute `dest_path` still
overrides the default target. When the default target is used, qrypt attempts to
create `default_path` itself if missing, but it does not create missing parent
directories.

`CreateUploadTaskJSON` always creates an app-stream staging upload task, and
`CreateDirectUploadTaskJSON` always creates a direct upload task, so the app
does not need to pass a task type for either flow.

Then write each item:

```text
OpenUploadItemJSON(coreID, taskID, itemID, deadlineMS)
read bytes from the app input stream into a reusable byte array
WriteUploadItem(handleID, reusableByteArray, deadlineMS)
CommitUploadItemJSON(handleID, deadlineMS)
```

`CommitUploadItemJSON` flushes and freezes the local staging file, queues the
cloud upload, releases the stream handle, and returns without waiting for remote
upload completion. Do not call a close API after it. Watch task events or query
task state for cloud upload progress and final success/failure.

If the input stream fails or permission is lost:

```text
FailUploadItemJSON(handleID, code, message)
```

qrypt records `waiting_input` and `resume_offset`. The app can reopen the item,
skip the input stream to `resume_offset`, and continue writing.
Use `PauseUploadItemJSON(handleID)` only when the app intentionally drops the
current handle but wants to resume later without marking an error.

Direct upload tasks do not use `OpenUploadItemJSON` / `WriteUploadItem`.
Instead, the app creates the task with `source_path`, keeps the source permission
valid until completion, and follows task events. Direct task result items use
`cloud_bytes_*` for upload progress and keep `staging_bytes_*` at zero. If a
future driver lacks source upload entirely, qrypt can still fall back to staging
and report `phase=queued_upload`.

Use `UploadLocalFileJSON` only when qrypt can read a stable local filesystem
path directly. It streams the file into an `upload_stream_batch` task, returns
`{entry, task}` once the file is staged and the cloud upload is queued, and
emits progress events from then on — watch the task like any other upload.

For client-side directory watch uploads, keep the watcher in the client and send
stable files to qrypt through upload task APIs. Do not copy watched files into
the FUSE mount to trigger upload. FUSE remains the filesystem adapter for local
mount access; upload tasks are the business API for queued uploads, progress,
conflict policy, completion events, and remote confirmation.

Before creating a task from a filesystem watcher event, the app can ask qrypt to
wait until the local source file is stable:

```text
WaitLocalFileStableJSON(coreID, localPath, {"quiet_ms":2000,"poll_ms":250}, deadlineMS)
```

The helper returns the last observed `size`, `mod_time`, and `observed_at` after
the file's size and mtime stay unchanged for `quiet_ms`. It rejects directories
and non-regular files. The client still owns platform-specific watching,
background execution, ignore rules, and local permissions.

For the common watcher path, use the local-upload wrapper instead of manually
building a generic task request:

```text
CreateLocalUploadTaskJSON(coreID, {
  "wait_stable": true,
  "stability": {"quiet_ms":2000,"poll_ms":250},
  "items": [{"local_path":"/local/DCIM/a.jpg","dest_path":"DCIM/a.jpg"}],
  "options": {"conflict_policy":"skip"}
}, deadlineMS)
```

`local_path` is converted to the internal `source_path`; empty or relative
`dest_path` still resolves through `[upload].default_mount/default_path`.

When creating an upload task, `options.conflict_policy` supports:

```text
overwrite  upload and replace existing remote content (default)
replace    alias of overwrite
fail       fail the item if the destination already exists
error      alias of fail
skip       keep the existing remote item and mark the result item phase skipped
```

After an upload item completes, inspect `task.result.items[*]`. Successful items
include `dest_path`, `mount`, `remote_id`, and cloud byte counts. A skipped item
has `state=succeeded` and `phase=skipped`. If the cloud driver reports instant
upload, the result item has `phase=instant`.

### Show Upload Progress

Use task events for realtime upload UI. Open an event stream for the returned
upload task id:

```text
OpenTaskEventsJSON(coreID, {"id":"upload-stream-..."}, deadlineMS)
ReadTaskEventsJSON(handleID, waitMS)
```

Each `task_updated` event includes `task.result.items`. For upload stream tasks,
each item carries both app-to-qrypt staging progress and qrypt-to-cloud progress:

```text
cloud_bytes_done     bytes accepted by the cloud driver
staging_bytes_done   bytes written into qrypt staging
output_bytes_done    bytes acknowledged by the app output stream
```

For Quark, cloud bytes advance when an OSS part finishes, so the UI may update
in part-sized jumps. The app should follow the upload task event stream instead
of querying progress by path.

### Download To App-Owned Output

Use `CreateDownloadTaskJSON` when the app owns the output stream. On Android
this usually means SAF, MediaStore, or public-directory OutputStreams.

Create the task:

```json
{
  "items": [
    {
      "item_id": "remote-1",
      "source_path": "/drive/video.mp4"
    }
  ]
}
```

`CreateDownloadTaskJSON` always creates an app-stream download task, so the app
does not need to pass a task type for this flow.

Then copy each item:

```text
OpenDownloadItemJSON(coreID, taskID, itemID, deadlineMS)
ReadDownloadItemInto(handleID, reusableByteArray, deadlineMS)
write bytes to the app output stream
AckDownloadItemJSON(handleID, bytesWritten)
CommitDownloadItemJSON(handleID, deadlineMS)
```

`AckDownloadItemJSON` can acknowledge many reads at once. Call it after the
app has successfully written bytes to the output target; `output_bytes_done`
advances only after ack.
`CommitDownloadItemJSON` releases the stream handle. Do not call a close
API after it.

If the output target is deleted or writing fails:

```text
FailDownloadItemJSON(handleID, code, message)
```

qrypt records `waiting_output` and `resume_offset`. The app can open a new
output target and continue from that offset.
Use `PauseDownloadItemJSON(handleID)` only when the app intentionally drops
the current handle but wants to resume later without marking an error.

### Manage Task UI

Use `CreateTaskJSON` for UI-created file operations:

```text
upload_stream_batch     app input stream -> qrypt/dest_path
upload_stream_direct    app source token -> qrypt/dest_path
download_stream_batch   qrypt source_path -> app output stream
move_remote             qrypt source_path -> qrypt dest_path
```

`ListTasksJSON` defaults to user-visible mobile tasks. The app can use the
returned `type`, `state`, `progress`, `capabilities`, and `result.items` fields
directly to render task rows.

`CreateTaskJSON` returns the created task. Stream tasks include item summaries in
`data.detail.items` and current row state in `data.result.items`, so common
single-item upload/download flows can open the returned `item_id` without an
extra `ListTaskItemsJSON` call.

Use task item APIs for per-file rows:

```text
ListTaskItemsJSON(coreID, taskID, filterJSON)
GetTaskItemJSON(coreID, taskID, itemID)
CancelTaskItemJSON(coreID, taskID, itemID, deadlineMS)
```

Stream task items expose row capabilities:

```json
{
  "open_input": true,
  "open_output": false,
  "cancelable": true
}
```

Use `CancelTaskItemJSON` for active or waiting stream items. For
`upload_stream_batch`, canceling an unfinished item removes that item's qrypt
staging data. For `upload_stream_direct`, the item cancel marks the direct row
canceled; if the task has fallen back to staging, the fallback upload is cleaned
up by the staging path. Non-stream tasks currently use whole-task cancellation.

For top-level task rows, use capabilities directly:

```text
if task.capabilities.cancelable or task.capabilities.dismissible:
    show delete -> DismissTaskJSON
if task.capabilities.cancelable:
    show cancel
```

`DismissTaskJSON` is the UI delete action. It cancels pending or active work
when possible and removes completed task history when the task is already
terminal. `CancelTaskJSON` remains available for an explicit cancel action that
does not mean "remove this row from the UI".

Use task events instead of high-frequency polling:

```text
OpenTaskEventsJSON(coreID, filterJSON, deadlineMS)
ReadTaskEventsJSON(handleID, waitMS)
CloseTaskEventsJSON(handleID)
```

`ReadTaskEventsJSON` is long polling. If queued events exist, it returns
immediately. If no event exists and `waitMS > 0`, it waits until a task event
arrives or the timeout expires. Timeout returns an empty event array, not an
error. If `waitMS <= 0`, it returns queued events without blocking.

Event example:

```json
{
  "seq": 1,
  "type": "task_updated",
  "task_id": "...",
  "task": {}
}
```

Event types are `task_updated` and `task_removed`. The event task is a full
snapshot, so the UI can replace its cached row directly. Events currently cover
qrypt-managed tasks created through `CreateTaskJSON` and task history removal.

The app learns that a task has finished from a `task_updated` event whose
`task.state` is terminal:

```text
succeeded
partial_failed
failed
canceled
```

For per-file completion, inspect `task.result.items[*].state`. After the target
task reaches a terminal state, close the event handle with
`CloseTaskEventsJSON`. If the app process lost its event handle, use
`GetTaskJSON`, `ListTasksJSON`, or `GetTaskItemJSON` to reload the latest task
snapshot and continue from that state.

Events cover qrypt-managed user tasks created through `CreateTaskJSON` and its
wrappers (`CreateUploadTaskJSON`, `CreateDownloadTaskJSON`,
`CreateLocalUploadTaskJSON`, `UploadLocalFileJSON`). Sync-scope VFS tasks
(`upload_remote` bookkeeping) are not managed by the mobile task manager and do
not emit events. `ListTasksJSON` with an empty filter defaults to user-scope
tasks, so the app's task list naturally excludes sync internals.

### Recursive Operations

When a mobile flow allows directory input, pass `options.recursive=true`.
qrypt expands directory tasks before execution so `items_total/items_done`
represent actual child entries instead of one opaque directory operation.

Stream task parallelism is controlled by how many item handles the app opens
concurrently. Directory creation and deletion remain ordered.

## API Reference

### Session And Config

```text
ImportConfigJSON(srcPath, runtimeJSON)
OpenImportedJSON(runtimeJSON)
OpenJSON(configPath, runtimeJSON)
CloseJSON(coreID)
ClassifyErrorMessageJSON(message)
DriverNamesJSON()
DriverSchemaJSON(name)
```

### Configuration Management

```text
ConfigSummaryJSON(coreID)
UpdateConfigJSON(coreID, updateJSON, deadlineMS)
ReloadConfigJSON(coreID, deadlineMS)
```

`ConfigSummaryJSON` returns a settings-UI friendly view of the current config:
mounts (secret params masked as `***`), read cache, thumbnail cache, and
upload settings.

`UpdateConfigJSON` mutates the config file with mount changes and optional
settings, validates the result, and saves it atomically (a failed update
leaves the previous config untouched; the file itself is written via a temp
file + rename, so a reader never sees a truncated config). Mount params use
field-level merge semantics: params not present in the update are kept, and a
masked secret placeholder (`***`) for an existing secret keeps the previous
value. A typical settings-UI round trip — read the summary, edit plain
fields, submit with the `***` placeholders intact — therefore never erases
credentials. Submit a real value to change a secret, or omit the param to
leave it unchanged. Request shape:

```json
{
  "mounts": [
    {"action": "add",    "name": "backup", "type": "localfs", "params": {"root_path": "..."}},
    {"action": "update", "name": "quark",  "type": "quark",  "params": {"cookie": "..."}},
    {"action": "remove", "name": "old"}
  ],
  "read_cache": {"max_size": "2G"},
  "upload": {"upload_delay": "5s", "upload_workers": 4, "default_mount": "quark"}
}
```

Driver params are validated against the driver schema before saving. Changes
take effect on the next open; call `ReloadConfigJSON` to apply them to the
running session. `ReloadConfigJSON` reopens the core from the current config
with the same runtime layout, invalidates all handles for the session (the app
must re-open file/task/event handles), and keeps the same `coreID`. If the
config fails to load, the previous core keeps running and an error is
returned.

### Filesystem

```text
ListJSON(coreID, path, deadlineMS)
ListPageJSON(coreID, path, cursor, limit, deadlineMS)
StatJSON(coreID, path, deadlineMS)
MkdirJSON(coreID, path, deadlineMS)
RenameJSON(coreID, oldPath, newPath, deadlineMS)
RemoveJSON(coreID, path, deadlineMS)
CapabilitiesJSON(coreID, path, deadlineMS)
MountsJSON(coreID)
FileInfoJSON(coreID, path, deadlineMS)
ValidateResumeJSON(coreID, path, fileID, size, modTime, deadlineMS)
RefreshPathJSON(coreID, path)
```

### Reads

```text
OpenFileJSON(coreID, path, optionsJSON)
ReadAtInto(handleID, offset, dst, deadlineMS)
CancelFileReadJSON(handleID)
OpenVirtualFileJSON(coreID, path, mode, deadlineMS)
ReadVirtualFileAtInto(handleID, offset, dst, deadlineMS)
CancelVirtualReadJSON(handleID)
CloseVirtualFileJSON(handleID)
CloseFileJSON(handleID)
```

### Media

```text
ProbeMP4JSON(coreID, path, deadlineMS)
```

### Uploads

```text
SetUploadSourceOpenerJSON(opener)
SetScopedFSBackendJSON(backend)
ClearScopedFSBackendJSON()
UploadLocalFileJSON(coreID, localPath, remotePath, deadlineMS)
WaitLocalFileStableJSON(coreID, localPath, optionsJSON, deadlineMS)
CreateLocalUploadTaskJSON(coreID, requestJSON, deadlineMS)
CreateUploadTaskJSON(coreID, requestJSON, deadlineMS)
CreateDirectUploadTaskJSON(coreID, requestJSON, deadlineMS)
OpenUploadItemJSON(coreID, taskID, itemID, deadlineMS)
WriteUploadItem(handleID, data, deadlineMS)
CommitUploadItemJSON(handleID, deadlineMS)
FailUploadItemJSON(handleID, code, message)
PauseUploadItemJSON(handleID)
```

### Downloads

```text
CreateDownloadTaskJSON(coreID, requestJSON, deadlineMS)
OpenDownloadItemJSON(coreID, taskID, itemID, deadlineMS)
ReadDownloadItemInto(handleID, dst, deadlineMS)
AckDownloadItemJSON(handleID, bytesWritten)
CommitDownloadItemJSON(handleID, deadlineMS)
FailDownloadItemJSON(handleID, code, message)
PauseDownloadItemJSON(handleID)
```

### Tasks

```text
CreateTaskJSON(coreID, requestJSON, deadlineMS)
ListTasksJSON(coreID, filterJSON)
GetTaskJSON(coreID, taskID)
ListTaskItemsJSON(coreID, taskID, filterJSON)
GetTaskItemJSON(coreID, taskID, itemID)
CancelTaskJSON(coreID, taskID, deadlineMS)
CancelTaskItemJSON(coreID, taskID, itemID, deadlineMS)
RetryTaskJSON(coreID, taskID, deadlineMS)
DismissTaskJSON(coreID, taskID, deadlineMS)
DismissFinishedTasksJSON(coreID, filterJSON, deadlineMS)
OpenTaskEventsJSON(coreID, filterJSON, deadlineMS)
ReadTaskEventsJSON(handleID, waitMS)
CloseTaskEventsJSON(handleID)
```

### Storage, Logs, And Debug

```text
DebugSnapshotJSON(coreID)
FlushReadCacheJSON(coreID)
StorageUsageJSON(coreID)
ClearReadCacheJSON(coreID, deadlineMS)
GetThumbnailFileJSON(coreID, path, preset, deadlineMS)
PutThumbnailFileJSON(coreID, path, preset, mime, localPath, deadlineMS)
ThumbnailCacheUsageJSON(coreID, deadlineMS)
ClearThumbnailCacheJSON(coreID, deadlineMS)
StartDebugServerJSON(coreID, listen)
StopDebugServerJSON(coreID)
LogFilesJSON(coreID)
ReadLogJSON(coreID, name, offset, length)
```

`DebugSnapshotJSON` includes a `mobile_handles` object reporting the number of
open file, virtual file, download, upload stream, and task event handles for
the session — useful for diagnosing handle leaks. It also includes
`task_persistence`: `degraded=true` means task history could not be written
reliably; it does not mean the file operation represented by the task failed.
`ThumbnailCacheUsageJSON`
returns `{"bytes": N}`; `RefreshPathJSON` returns `{"refreshed": true}`.

## Performance

The Go side of the mobile interface is already near its practical limit, so
performance work belongs mostly in the client, not in qrypt.

Measured on Apple M1 Pro (localfs, 16 MiB file):

```text
ReadAtInto 64 KiB buffer   ~10.7 µs/call   6.1 GB/s    49 allocs/call
ReadAtInto 1 MiB buffer    ~90 µs/call    11.6 GB/s    49 allocs/call
ListJSON 1000 entries      ~28 µs/call    ~38 KB JSON  199 allocs
```

The per-call overhead of the mobile layer itself (handle registry lookups,
read-cancel bookkeeping, context creation) is under 1 µs and a few allocations
— there is no meaningful optimization left inside qrypt's call path. The real
cost is the gomobile/JNI boundary: every Kotlin-to-Go call pays a fixed JNI
overhead plus a `byte[]` copy in each direction.

### Client Best Practices (biggest wins, one line each)

1. **Use larger read buffers.** Cutting call count is the single most
effective change: reading a 1 GiB file with a 64 KiB buffer means ~16k JNI
calls; a 1 MiB buffer means ~1k. Size the buffer to your seek granularity —
256 KiB to 1 MiB is a good range for media playback and copy/export loops.
2. **Reuse the destination `ByteArray`** across reads instead of allocating a
new one per call, to avoid GC pressure on the Kotlin heap.
3. **Browse large directories with `ListPageJSON`** (opaque cursor paging)
instead of `ListJSON`, so each response stays small and the UI can render
incrementally.
4. **Use task event long polling** (`ReadTaskEventsJSON` with `waitMS > 0`)
for progress instead of polling `ListTasksJSON`/`GetTaskJSON` — events are
batched and return full snapshots.
5. **Never move media bytes through `*JSON` APIs** — they base64-encode and
double the memory footprint. Use the byte-buffer APIs everywhere.

### Structural Directions (future, higher cost)

These would change the communication model and are only worth pursuing with
real throughput requirements on a specific flow:

- **Bulk read API**: one call returns a whole file/large chunk in a single
Go-allocated `[]byte` (one JNI round trip). Fits small files, thumbnails, and
import flows; unsuitable for seek-heavy playback.
- **Guaranteed read-fill**: make `ReadAtInto` loop internally until the buffer
is full or EOF, removing short-read handling from the client. Reduces client
logic, not call count.
- **Native direct memory**: bypass gomobile with a custom JNI binding and
direct `ByteBuffer`s to avoid copies entirely. Highest throughput, highest
maintenance cost — only for extreme cases.

## Error Handling

`*JSON` functions return a consistent envelope:

```json
{
  "ok": true,
  "data": {},
  "error": null
}
```

Errors use stable client codes:

```json
{
  "ok": false,
  "error": {
    "code": "network_retryable",
    "category": "network",
    "retryable": true,
    "message": "..."
  }
}
```

Important codes:

```text
network_retryable
auth_expired
permission
not_found
rate_limited
local_io
cancelled
unsupported
unknown
```

Legacy non-JSON mobile APIs prefix returned errors with the code, for example:

```text
network_retryable: context deadline exceeded
```

Mobile clients can also call `ClassifyErrorMessageJSON` when they only have an
error string.

## Platform Notes

Android upload/download UI should prefer stream tasks for SAF, MediaStore, and
public-directory access. This avoids requiring broad filesystem permissions and
avoids an extra app-private copy before qrypt staging.

iOS should use the same stream task model for security-scoped files or document
provider streams. Map runtime storage roots to the platform location that matches
the data lifetime: cache data in cache directories, durable driver/session state
in app-private persistent storage, and tmp data in tmp directories.

## Android Build

Generate an Android AAR:

```sh
scripts/build-android-aar.sh
```

Optional output directory:

```sh
scripts/build-android-aar.sh /tmp/qrypt-aar
```

The script requires `gomobile` and a completed `gomobile init`.

## Verification

Run core and mobile tests:

```sh
GOCACHE=/tmp/qrypt-go-build go test ./pkg/core ./pkg/mobile
```

Run CLI regression tests after core boundary changes:

```sh
GOCACHE=/tmp/qrypt-go-build go test ./internal/cli
```

Run the full project regression before handing off mobile integration:

```sh
GOCACHE=/tmp/qrypt-go-build go test ./...
```
