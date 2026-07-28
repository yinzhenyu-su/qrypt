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
StatJSON(coreID, path, deadlineMS)
FileInfoJSON(coreID, path, deadlineMS)
ValidateResumeJSON(coreID, path, fileID, size, modTime, deadlineMS)
```

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

### Upload App-Owned Files

Use `CreateUploadTaskJSON` when the app owns the input stream. On Android this
is usually a SAF/content URI InputStream. On iOS this can be a security-scoped
file or provider stream.

Create the task:

```json
{
  "items": [
    {
      "item_id": "local-1",
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

`CreateUploadTaskJSON` always creates an app-stream upload task, so the app does
not need to pass a task type for this flow.

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

Use `UploadLocalFileJSON` only when qrypt can read a stable local filesystem
path directly.

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
staging data. Non-stream tasks currently use whole-task cancellation.

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

### Filesystem

```text
ListJSON(coreID, path, deadlineMS)
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
OpenVirtualFileJSON(coreID, path, mode, deadlineMS)
ReadVirtualFileAtInto(handleID, offset, dst, deadlineMS)
CloseVirtualFileJSON(handleID)
CloseFileJSON(handleID)
```

### Media

```text
ProbeMP4JSON(coreID, path, deadlineMS)
```

### Uploads

```text
UploadLocalFileJSON(coreID, localPath, remotePath, deadlineMS)
WaitLocalFileStableJSON(coreID, localPath, optionsJSON, deadlineMS)
CreateLocalUploadTaskJSON(coreID, requestJSON, deadlineMS)
CreateUploadTaskJSON(coreID, requestJSON, deadlineMS)
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
