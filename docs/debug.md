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
  --config ./qrypt.toml \
  mount --debug-socket /tmp/qrypt.sock
```

The socket file is created with `0600` permissions. qrypt refuses to replace a
live socket and removes stale socket files during startup.

## AI-First Commands

Use `collect` when you want a complete snapshot:

```sh
go run ./cmd/qrypt debug collect --socket /tmp/qrypt.sock
```

Use `inspect` when a specific file or directory is involved:

```sh
go run ./cmd/qrypt debug inspect /baiduyun/path/file.html --socket /tmp/qrypt.sock
```

`collect --path PATH` combines the global snapshot with a path-focused
inspection:

```sh
go run ./cmd/qrypt debug collect --socket /tmp/qrypt.sock --path /baiduyun/path/file.html
```

Use `watch` while reproducing timing-sensitive problems such as repeated
saves, upload retries, stale reads, or `file changed while writing`:

```sh
go run ./cmd/qrypt debug watch --socket /tmp/qrypt.sock --path /baiduyun/path/file.html --duration 30s --interval 2s
```

The JSON reports include:

- process, runtime, and debug socket health
- mount, driver, encryption, upload, read-cache, and staging state
- recent warning/error events
- path resolution, cache, staging, upload history, and consistency checks for
  inspected paths
- normalized diagnostics with `severity`, `code`, `component`, `path`,
  `message`, and supporting `evidence`

Run live driver health checks only when needed because they may call provider
APIs:

```sh
go run ./cmd/qrypt debug collect --socket /tmp/qrypt.sock --driver-health
```

## Bug Report Bundle

`bundle` is the preferred artifact to share with an AI assistant or developer.
It includes `collect.json`, `diagnostics.json`, raw endpoint outputs, and
`inspect.json` when a path is provided:

```sh
go run ./cmd/qrypt debug bundle --socket /tmp/qrypt.sock --out /tmp/qrypt-debug.zip
go run ./cmd/qrypt debug bundle --socket /tmp/qrypt.sock --path /baiduyun/path/file.html --out /tmp/qrypt-debug.zip
go run ./cmd/qrypt debug bundle --socket /tmp/qrypt.sock --path /baiduyun/path/file.html --watch 30s --out /tmp/qrypt-debug.zip
```

Review the bundle before sharing it.

## Offline Checks

Offline checks read cache files and do not require a running mount:

```sh
go run ./cmd/qrypt --config ./qrypt.toml fs pending
go run ./cmd/qrypt --config ./qrypt.toml fs pending --verbose
go run ./cmd/qrypt --config ./qrypt.toml debug journal
go run ./cmd/qrypt --config ./qrypt.toml debug journal --json
```

Use `--mount NAME` to inspect one configured mount:

```sh
go run ./cmd/qrypt --config ./qrypt.toml debug journal --mount aliyun
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
```

The raw endpoints live under `/v1/...`. Endpoint-specific debug commands have
been removed from the CLI; use `debug raw` for direct socket access.
