# Debugging

qrypt debug tools are designed to collect structured evidence for AI-assisted
troubleshooting. The primary commands output JSON by default and include a
`diagnostics` section that highlights likely issues while keeping the raw
runtime data in the same report.

Debug output is read-only unless a command explicitly says otherwise. Secrets
are masked by the driver/config layers, but paths and filenames can still be
sensitive.

## Enable Logging

```toml
[logging]
log_level = "debug"
log_file = "~/.qrypt/qrypt.log"
error_file = "~/.qrypt/qrypt-error.log"
```

The debug log includes FUSE callback names, paths, return codes, and durations.

## Start The Debug Socket

Start qrypt with a local control socket:

```sh
go run ./cmd/qrypt \
  mount --config ./qrypt.toml \
  --socket /tmp/qrypt.sock
```

The socket file is created with `0600` permissions. qrypt refuses to replace a
live socket and removes stale socket files during startup.

### Windows

On Windows, qrypt uses an AF_UNIX socket. In PowerShell, choose a socket path
under the current user's temporary directory:

```powershell
$socket = "$env:TEMP\qrypt.sock"
qrypt.exe mount --config .\qrypt.toml --socket $socket
```

Keep the mount process running, then use the same path in a second PowerShell
window:

```powershell
$socket = "$env:TEMP\qrypt.sock"
qrypt.exe debug collect --socket $socket
qrypt.exe debug inspect /local/example.txt --socket $socket
qrypt.exe debug raw health --socket $socket
```

This requires a Windows version with AF_UNIX support. The `--socket` value is
a filesystem path, not a Windows named pipe, so paths such as
`\\.\pipe\qrypt` are not supported. The flag can be omitted when runtime debug
access is not needed.

## AI-First Commands

Use `collect` when you want a complete snapshot:

```sh
go run ./cmd/qrypt debug collect --socket /tmp/qrypt.sock
```

Use `inspect` when a specific file or directory is involved:

```sh
go run ./cmd/qrypt debug inspect /baiduyun/path/file.html --socket /tmp/qrypt.sock
```

For a transfer or cross-mount copy problem, provide both endpoints. The report
keeps source and destination diagnostics separate and includes mount-pair
capabilities:

```sh
go run ./cmd/qrypt debug inspect /local/source.bin --dest /quark/archive/source.bin --socket /tmp/qrypt.sock
go run ./cmd/qrypt debug collect /local/source.bin --dest /quark/archive/source.bin --socket /tmp/qrypt.sock
```

`collect [REMOTE]` combines the global snapshot with a path-focused
inspection:

```sh
go run ./cmd/qrypt debug collect /baiduyun/path/file.html --socket /tmp/qrypt.sock
```

Use `watch` while reproducing timing-sensitive problems such as repeated
saves, upload retries, stale reads, or `file changed while writing`:

```sh
go run ./cmd/qrypt debug watch /baiduyun/path/file.html --socket /tmp/qrypt.sock --duration 30s --interval 2s
```

The JSON reports include:

- process, runtime, and debug socket health
- mount, driver, encryption, upload, read history, read-cache, and staging state
- runtime mount health based on recent VFS operations
- recent warning/error events
- path resolution, cache, staging, upload history, and consistency checks for
  inspected paths
- transfer context containing source and destination resolution, destination
  parent state, driver capabilities, encryption mode, and compatibility warnings
- normalized diagnostics with `severity`, `code`, `component`, `path`,
  `message`, and supporting `evidence`

## Bug Report Bundle

`bundle` is the preferred artifact to share with an AI assistant or developer.
It includes `collect.json`, `diagnostics.json`, raw endpoint outputs, and
`inspect.json` when a path is provided:

```sh
go run ./cmd/qrypt debug bundle --socket /tmp/qrypt.sock --out /tmp/qrypt-debug.zip
go run ./cmd/qrypt debug bundle /baiduyun/path/file.html --socket /tmp/qrypt.sock --out /tmp/qrypt-path-debug.zip
go run ./cmd/qrypt debug bundle /local/source.bin --dest /quark/archive/source.bin --socket /tmp/qrypt.sock --out /tmp/qrypt-transfer-debug.zip
go run ./cmd/qrypt debug bundle /baiduyun/path/file.html --socket /tmp/qrypt.sock --watch 30s --out /tmp/qrypt-watch-debug.zip
go run ./cmd/qrypt debug bundle --socket /tmp/qrypt.sock --goroutines --out /tmp/qrypt-deep-debug.zip
```

Existing output files are not overwritten unless `--force` is provided. Review
the bundle before sharing it.

## Offline Checks

Offline checks read cache files and do not require a running mount:

```sh
go run ./cmd/qrypt fs pending --config ./qrypt.toml
go run ./cmd/qrypt fs pending --config ./qrypt.toml --verbose
go run ./cmd/qrypt fs journal --config ./qrypt.toml
go run ./cmd/qrypt fs journal --config ./qrypt.toml --json
```

Use `--mount NAME` to inspect one configured mount:

```sh
go run ./cmd/qrypt fs journal --config ./qrypt.toml --mount aliyun
```

`fs pending --verbose` shows virtual path, expected size, staging file path,
retry count, last error, and next retry time. `journal` checks `pending.jsonl`,
missing staging files, size mismatches, and orphan staging files.

## Lower-Level Commands

Use `debug raw` when you need one socket endpoint directly:

```sh
go run ./cmd/qrypt debug raw health --socket /tmp/qrypt.sock
go run ./cmd/qrypt debug raw /v1/state --socket /tmp/qrypt.sock
go run ./cmd/qrypt debug raw '/v1/uploads?history=1' --socket /tmp/qrypt.sock
go run ./cmd/qrypt debug raw '/v1/reads?path=/local/source.bin' --socket /tmp/qrypt.sock
go run ./cmd/qrypt debug raw '/v1/transfer/context?source=/local/source.bin&dest=/quark/archive/source.bin' --socket /tmp/qrypt.sock
```

The raw endpoints live under `/v1/...`. Endpoint-specific debug commands have
been removed from the CLI; use `debug raw` for direct socket access.

Read and upload records use the versioned operation-event schema. VFS records
only observable operations such as read and upload; it does not infer whether
the caller is performing a copy. Transfer and future copy executors can attach
their own `op_id` and correlate these facts explicitly.

## Explicit Write Probes

Run a CRUD test to verify basic read/write operations:

```sh
go run ./cmd/qrypt debug test crud --socket /tmp/qrypt.sock
```

Run an instant upload test to verify content deduplication. Requires
`content_dedup = true` when encryption is enabled:

```sh
go run ./cmd/qrypt debug test instantupload --socket /tmp/qrypt.sock
```

Filter by mount name:

```sh
go run ./cmd/qrypt debug test crud --mount default --socket /tmp/qrypt.sock
```

Run a transfer test between two mounts:

```sh
go run ./cmd/qrypt debug test xfer --source local --dest quark --size 16m --socket /tmp/qrypt.sock
go run ./cmd/qrypt debug test xfer --source local --dest quark --vfs --socket /tmp/qrypt.sock
```

Test commands create temporary remote objects and clean them up best-effort.
Use them only when write access to the selected mount is acceptable.
