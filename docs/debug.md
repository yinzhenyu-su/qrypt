# Debugging

qrypt exposes three debug surfaces:

- log files configured in `qrypt.toml`
- offline cache and pending-upload checks
- an optional live Unix socket for runtime state

Debug output is read-only unless a command explicitly says otherwise. Logs and
events are sanitized, but paths and filenames can still be sensitive.

## Enable Logging

```toml
[logging]
log_level = "debug"
log_file = "~/.qrypt/qrypt.log"
error_file = "~/.qrypt/qrypt-error.log"
```

The debug log includes FUSE callback names, paths, return codes, and durations.

## Offline Checks

Offline checks read cache files and do not require a running mount.

```sh
go run ./cmd/qrypt -config ./qrypt.toml pending
go run ./cmd/qrypt -config ./qrypt.toml pending --verbose
go run ./cmd/qrypt -config ./qrypt.toml debug journal
```

Use `-mount-name NAME` to inspect one configured mount:

```sh
go run ./cmd/qrypt -config ./qrypt.toml -mount-name aliyun debug journal
```

`pending --verbose` shows virtual path, expected size, staging file path,
retry count, last error, and next retry time. `debug journal` checks
`pending.jsonl`, missing staging files, size mismatches, and orphan staging
files.

## Live Socket

Start qrypt with a local control socket:

```sh
go run ./cmd/qrypt \
  -config ./qrypt.toml \
  -debug-socket /tmp/qrypt.sock \
  mount
```

Query it from another shell:

```sh
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live health
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live state
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live pending
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live uploads --history
```

The socket file is created with `0600` permissions. qrypt refuses to replace a
live socket and removes stale socket files during startup.

## Common Workflows

### Upload Is Not Visible

```sh
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live uploads /aliyun/a.txt --history
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live pending
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live list /aliyun
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live consistency /aliyun/a.txt
```

### Upload Is Stuck

```sh
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live uploads --history
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live events warn 100
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live staging /aliyun/a.txt
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live goroutines
go run ./cmd/qrypt -config ./qrypt.toml debug journal
```

### Filename Mapping Looks Wrong

```sh
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live resolve /aliyun/a.txt --remote-name
```

Use this only for targeted checks because plaintext filenames and remote names
can both be sensitive.

### Preparing a Bug Report

```sh
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug bundle -out /tmp/qrypt-debug.zip
```

Review the bundle before sharing it.

## Command Reference

| Command | Purpose |
|---|---|
| `debug live health` | process reachability and API version |
| `debug live state` | process metadata, mount encryption/driver summary, pending uploads, active uploads, timers, cache summary, driver snapshots |
| `debug live pending` | flat pending upload list with basic staging status |
| `debug live uploads [path] [--history]` | active and recent upload state |
| `debug live events [level] [limit] [--path path] [--component name]` | recent warning/error events, optionally filtered by path or component |
| `debug live list <path>` | live remote list through VFS/driver, bypassing directory cache |
| `debug live resolve <path> [--remote-name]` | virtual path, mount, driver, encryption state, object ID, cache ID, parent ID, and optional encrypted remote name |
| `debug live cache [path]` | read cache size, files, chunks, hits, misses, evictions |
| `debug live staging [path]` | staging file counts, pending matches, orphans, and per-file checksum |
| `debug live consistency <path>` | compare pending state with live remote parent listing |
| `debug live driver` | optional per-driver debug snapshots |
| `debug live runtime` | Go runtime and memory summary |
| `debug live goroutines [debug]` | goroutine dump |
| `debug bundle -out <zip>` | collect a diagnostic zip |

The live socket mirrors these commands as HTTP endpoints under `/v1/...`.
