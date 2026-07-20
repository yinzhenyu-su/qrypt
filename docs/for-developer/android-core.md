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
an app-private work directory to `mobile.Open`.

Suggested layout:

```text
filesDir/qrypt/
  config/qrypt.toml
  cache/
  state/
  logs/
```

When `WorkDir` is set, qrypt core stores each mount cache under:

```text
WorkDir/cache/<mount>
```

Driver state stores are installed under the mount cache directory, so Quark
state files also stay inside the app-private work directory.

## Mobile API

Current gomobile-facing functions:

```text
Open(configPath, workDir) -> coreID
List(coreID, path) -> JSON
Stat(coreID, path) -> JSON
OpenFile(coreID, path) -> handleID
ReadAt(handleID, offset, length) -> bytes
CloseFile(handleID)
Close(coreID)
ClassifyErrorMessage(message) -> JSON
```

`List` and `Stat` return JSON to keep the binding stable and avoid exposing Go
interfaces to Kotlin.

`ReadAt` is intended for preview and random-access readers. Android should keep
chunk sizes bounded and call `ReadAt` repeatedly for seek-heavy consumers.

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

`pkg/mobile` prefixes returned errors with the code, for example:

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
