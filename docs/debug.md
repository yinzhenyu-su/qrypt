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
go run ./cmd/qrypt --config ./qrypt.toml fs pending
go run ./cmd/qrypt --config ./qrypt.toml fs pending --verbose
go run ./cmd/qrypt --config ./qrypt.toml journal
```

Use `--mount NAME` to inspect one configured mount:

```sh
go run ./cmd/qrypt --config ./qrypt.toml journal --mount aliyun
```

`fs pending --verbose` shows virtual path, expected size, staging file path,
retry count, last error, and next retry time. `journal` checks
`pending.jsonl`, missing staging files, size mismatches, and orphan staging
files.

## Live Socket

Start qrypt with a local control socket:

```sh
go run ./cmd/qrypt \
  --config ./qrypt.toml \
  --debug-socket /tmp/qrypt.sock \
  mount
```

Query it from another shell:

```sh
go run ./cmd/qrypt --debug-socket /tmp/qrypt.sock debug health
go run ./cmd/qrypt --debug-socket /tmp/qrypt.sock debug state
go run ./cmd/qrypt --debug-socket /tmp/qrypt.sock debug staging
go run ./cmd/qrypt --debug-socket /tmp/qrypt.sock debug uploads --history
```

The socket file is created with `0600` permissions. qrypt refuses to replace a
live socket and removes stale socket files during startup.

## Common Workflows

### Upload Is Not Visible

```sh
go run ./cmd/qrypt --debug-socket /tmp/qrypt.sock debug uploads /aliyun/a.txt --history
go run ./cmd/qrypt --debug-socket /tmp/qrypt.sock debug staging /aliyun/a.txt
go run ./cmd/qrypt --debug-socket /tmp/qrypt.sock debug list /aliyun
go run ./cmd/qrypt --debug-socket /tmp/qrypt.sock debug consistency --path /aliyun/a.txt
```

### Upload Is Stuck

```sh
go run ./cmd/qrypt --debug-socket /tmp/qrypt.sock debug uploads --history
go run ./cmd/qrypt --debug-socket /tmp/qrypt.sock debug events --level warn --limit 100
go run ./cmd/qrypt --debug-socket /tmp/qrypt.sock debug staging /aliyun/a.txt
go run ./cmd/qrypt --debug-socket /tmp/qrypt.sock debug goroutines
go run ./cmd/qrypt --config ./qrypt.toml journal
```

### Filename Mapping Looks Wrong

```sh
go run ./cmd/qrypt --debug-socket /tmp/qrypt.sock debug resolve /aliyun/a.txt --remote-name
```

Use this only for targeted checks because plaintext filenames and remote names
can both be sensitive.

### Preparing a Bug Report

```sh
go run ./cmd/qrypt --debug-socket /tmp/qrypt.sock debug bundle --out /tmp/qrypt-debug.zip
```

Review the bundle before sharing it.

## Command Reference

| Command | Purpose |
|---|---|
| `debug health` | process reachability and API version |
| `debug state` | process metadata, mount encryption/driver summary, pending count, active uploads, timers, cache summary, driver snapshots |
| `debug staging [path]` | staging file integrity check: cross-references pending/journal with on-disk staging files, detects orphans and size mismatches |
| `debug uploads [path] --history` | upload runtime state: per-file progress (bytes uploaded / total), retry count, errors, completion history |
| `debug events --level L --limit N --path P --component C` | recent warning/error events, optionally filtered by path or component |
| `debug list [path]` | live remote list through VFS/driver, bypassing directory cache |
| `debug resolve [path...] --remote-name` | virtual path, mount, driver, encryption state, object ID, cache ID, parent ID, and optional encrypted remote name |
| `debug cache [path]` | read cache size, files, chunks, hits, misses, evictions |
| `debug consistency --path PATH / --dir DIR [--recursive]` | compare pending state with live remote parent listing |
| `debug driver` | static per-driver debug snapshot (config, cookie age, etc.) |
| `debug driver health [mount]` | live driver health check: real API call, latency, error details |
| `debug driver test crud [mount]` | destructive CRUD test: mkdir → put → read → rename → remove |
| `debug runtime` | Go runtime and memory summary |
| `debug goroutines [debug]` | goroutine dump |
| `debug bundle --out <zip>` | collect a diagnostic zip |
| `journal` | offline journal integrity check |

**Which one to use?**
- `staging` — 文件是否在磁盘上、大小是否一致、有没有孤儿文件（**磁盘完整性**）
- `uploads` — 上传进度、重试状态、错误详情、历史记录（**上传引擎运行时**）
- `driver test crud` — 验证云盘驱动的基本操作是否正常。**有副作用**：会在云盘上创建和删除文件

The live debug commands mirror HTTP endpoints under `/v1/...`.
