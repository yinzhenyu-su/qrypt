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
applies a caller-provided work directory, builds the VFS namespace, and exposes
path-based filesystem operations.

`pkg/mobile` owns gomobile-friendly session and file-handle APIs.

The mobile binding imports `pkg/drivers/all`, so the generated AAR registers
the same bundled driver set as the CLI.

## WorkDir

Android should copy the imported `qrypt.toml` into app-private storage and pass
an app-private work directory to `mobile.OpenImportedJSON`.

Suggested layout:

```text
filesDir/qrypt/
  config/qrypt.toml
  cache/
  state/
  logs/
  tmp/
```

When `WorkDir` is set, qrypt core stores each mount cache under:

```text
WorkDir/cache/<mount>
```

Driver state stores are installed under:

```text
WorkDir/state/<mount>/driver
```

Quark cookie and upload-session state therefore stay inside the app-private
work directory. Runtime logs are written under `WorkDir/logs`.

## Config Import

Android should import config through the mobile API:

```text
ImportConfigJSON(srcPath, workDir)
OpenImportedJSON(workDir)
```

Import copies the config to:

```text
WorkDir/config/qrypt.toml
```

During import, desktop runtime paths are cleared:

- `mount_point`
- `cache_dir`
- `logging.log_file`
- `logging.error_file`
- `[[mounts]].cache.dir`

The core then applies Android `WorkDir` paths at runtime.

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
ImportConfigJSON(srcPath, workDir)
OpenImportedJSON(workDir)
OpenJSON(configPath, workDir)
ListJSON(coreID, path)
StatJSON(coreID, path)
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
LogFilesJSON(coreID)
ReadLogJSON(coreID, name, offset, length)
```

The older non-envelope functions remain available for compatibility but should
not be the primary Android integration surface.

`ReadAtJSON` is intended for preview and random-access readers. The core
enforces a default 4 MiB chunk limit. Android should call it repeatedly for
seek-heavy consumers and pass `timeoutMS` for preview/download cancellation.

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
