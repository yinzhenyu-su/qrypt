# Android Core

This document tracks the qrypt-side work needed by an Android client.

## Package Boundary

Android should use `pkg/mobile`, not `internal/cli`.

The package stack is:

```text
Android Kotlin
-> gomobile AAR
-> pkg/mobile
-> pkg/core
-> pkg/vfs.FileSystem
-> pkg/drive.Driver
```

`pkg/core` owns reusable qrypt engine construction. It loads `qrypt.toml`,
applies a caller-provided runtime layout, builds the VFS namespace, and exposes
path-based filesystem operations.

`pkg/mobile` owns gomobile-friendly session and file-handle APIs.

The mobile binding imports `pkg/drivers/all`, so the generated AAR registers
the same bundled driver set as the CLI.

## Runtime Layout

Android should pass an explicit runtime layout JSON to `mobile.ImportConfigJSON`,
`mobile.OpenImportedJSON`, and `mobile.OpenJSON`. The layout tells qrypt where
each class of runtime data belongs.

Suggested layout:

```text
filesDir/qrypt/
  config/qrypt.toml
  writeback/
  state/
    driver/
  logs/

cacheDir/qrypt/
  read/
  tmp/
```

Example `runtimeJSON`:

```json
{
  "config_dir": "<filesDir>/qrypt/config",
  "storage": {
    "read_cache_dir": "<cacheDir>/qrypt/read",
    "writeback_dir": "<filesDir>/qrypt/writeback",
    "state_dir": "<filesDir>/qrypt/state",
    "log_dir": "<filesDir>/qrypt/logs",
    "tmp_dir": "<cacheDir>/qrypt/tmp"
  }
}
```

qrypt stores each mount read cache under:

```text
<read_cache_dir>/<mount>
```

Pending uploads and staging files are stored under:

```text
<writeback_dir>/<mount>
```

Driver state stores are installed under:

```text
<state_dir>/driver/<mount>
```

Quark cookie and upload-session state therefore stay inside the app-private
files directory, while read cache and temp data can live in Android `cacheDir`.

## Config Import

Android should import config through the mobile API:

```text
ImportConfigJSON(srcPath, runtimeJSON)
OpenImportedJSON(runtimeJSON)
```

Import copies the config to:

```text
<config_dir>/qrypt.toml
```

During import, desktop runtime paths are cleared:

- `mount_point`
- `storage.read_cache_dir`
- `storage.writeback_dir`
- `storage.state_dir`
- `storage.log_dir`
- `storage.tmp_dir`
- `logging.log_file`
- `logging.error_file`

The core then applies the supplied runtime layout when opening the imported
config.

## Mobile API

Android should prefer the `*JSON` functions. They return:

```json
{
  "ok": true,
  "data": {},
  "error": null
}
```

Errors use:

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

Current gomobile-facing JSON functions:

```text
ImportConfigJSON(srcPath, runtimeJSON)
OpenImportedJSON(runtimeJSON)
OpenJSON(configPath, runtimeJSON)
ListJSON(coreID, path)
StatJSON(coreID, path)
UploadLocalFileJSON(coreID, localPath, remotePath, timeoutMS)
OpenStreamingUploadJSON(coreID, remotePath, timeoutMS)
WriteStreamingUploadJSON(handleID, data, timeoutMS)
FinishStreamingUploadJSON(handleID, timeoutMS)
CancelStreamingUploadJSON(handleID, timeoutMS)
ListTasksJSON(coreID, filterJSON)
GetTaskJSON(coreID, taskID)
CancelTaskJSON(coreID, taskID, timeoutMS)
RetryTaskJSON(coreID, taskID, timeoutMS)
FileInfoJSON(coreID, path)
ValidateResumeJSON(coreID, path, id, size, modTime)
OpenFileJSON(coreID, path)
ReadAtJSON(handleID, offset, length, timeoutMS)
CloseFileJSON(handleID)
CloseJSON(coreID)
DriverNamesJSON()
DriverSchemaJSON(name)
DebugSnapshotJSON(coreID)
FlushReadCacheJSON(coreID)
StorageUsageJSON(coreID)
ClearReadCacheJSON(coreID, timeoutMS)
LogFilesJSON(coreID)
ReadLogJSON(coreID, name, offset, length)
```

The older non-envelope functions remain available for compatibility but should
not be the primary Android integration surface.

Do not use JSON functions for media byte transfer. JSON encodes `[]byte` as
base64 and forces extra large string allocations. Video playback should use
the byte-buffer APIs instead:

```text
ReadAtInto(handleID, offset, dst)
ReadAtIntoWithTimeout(handleID, offset, dst, timeoutMS)
ReadVirtualFileAtInto(handleID, offset, dst, timeoutMS)
```

`ReadAtJSON` and `ReadVirtualFileAtJSON` remain for compatibility and small
preview reads only. The core enforces a default 4 MiB chunk limit. Android
should call the byte-buffer APIs repeatedly for seek-heavy consumers and pass
`timeoutMS` for preview/playback cancellation.

For large uploads from `content://`, prefer streaming into qrypt staging instead
of first copying to an app-private temp file:

```text
OpenStreamingUploadJSON(coreID, remotePath, timeoutMS)
WriteStreamingUpload(handleID, data, timeoutMS)
FinishStreamingUploadJSON(handleID, timeoutMS)
CancelStreamingUploadJSON(handleID, timeoutMS)
```

`WriteStreamingUpload` accepts a Java/Kotlin `byte[]` through gomobile and
appends it to the staging file. Keep the chunk buffer reusable on the Android
side. Use `FinishStreamingUploadJSON` after EOF to flush staging and enqueue the
normal qrypt upload. Use `CancelStreamingUploadJSON` when the Android transfer is
aborted; it removes the pending staging file.

`FinishStreamingUploadJSON` returns an `entry` plus a `task`. Android should keep
the returned `task.id` and use `GetTaskJSON` or `ListTasksJSON` to show remote
upload progress after staging is complete. `CancelTaskJSON` cancels pending
writeback upload state and removes its staging file. `RetryTaskJSON` clears
retry wait/failure state and schedules the upload immediately.

Android and iOS should use `StorageUsageJSON` to show qrypt-owned storage and
`ClearReadCacheJSON` to clear only reusable read cache data. `ClearReadCacheJSON`
does not remove upload staging, pending upload journals, config, driver state,
or logs.

## Error Handling

`pkg/core` exposes stable client error codes through `ErrorInfo`.

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

Android can also call `ClassifyErrorMessage` when it only has an error string.

## Build

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

Run the full project regression before handing off Android integration:

```sh
GOCACHE=/tmp/qrypt-go-build go test ./...
```
