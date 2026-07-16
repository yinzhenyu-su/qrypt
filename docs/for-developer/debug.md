# Debugging

This guide is for contributors who need to understand or troubleshoot qrypt at
runtime. It starts with the common workflows, then lists the lower-level
commands and endpoints.

The debug tools collect structured evidence from a running qrypt process. Most
read-only commands return JSON and include a `diagnostics` section when qrypt can
identify likely problems.

Debug output is read-only unless a command is clearly named `test` or `bench`.
Secrets are masked by the driver and config layers, but local paths, filenames,
remote IDs, and short error messages may still be sensitive. Please review any
bundle before sharing it.

## Mental Model

There are three layers worth keeping separate:

- **Driver**: talks to the cloud provider.
- **VFS**: tracks pending writes, staging files, upload queues, delayed deletes,
  read cache, and namespace mounts.
- **Debug socket**: exposes runtime state through local `/v1/...` endpoints.

Most runtime debug commands need a mount scope:

```sh
--mount quark-test
```

Repeat `--mount` when two or more mounts are relevant:

```sh
--mount local --mount quark
```

Use `--all-mounts` only when the whole namespace is part of the issue. It can
produce much larger reports.

## Start Here

Start qrypt with a local debug socket:

```sh
go run ./cmd/qrypt mount --config ./qrypt.toml --socket /tmp/qrypt.sock
```

Then collect one focused report:

```sh
go run ./cmd/qrypt debug collect --socket /tmp/qrypt.sock --mount quark-test
```

If a specific path is involved, include it:

```sh
go run ./cmd/qrypt debug collect /quark-test/example.bin --socket /tmp/qrypt.sock --mount quark-test
```

The socket file is created with `0600` permissions. qrypt refuses to replace a
live socket and removes stale socket files during startup.

### Enable Debug Logs

When the issue involves FUSE callbacks, retries, upload failures, or timing,
enable debug logs in the config:

```toml
[logging]
log_level = "debug"
log_file = "~/.qrypt/qrypt.log"
error_file = "~/.qrypt/qrypt-error.log"
```

The debug log includes callback names, paths, return codes, durations, and
sampled runtime messages. The error log is usually the fastest place to look
for warnings and failures after a reproduction.

### Windows

On Windows, qrypt uses an AF_UNIX socket. In PowerShell, choose a path under the
current user's temporary directory:

```powershell
$socket = "$env:TEMP\qrypt.sock"
qrypt.exe mount --config .\qrypt.toml --socket $socket
```

Keep the mount process running, then use the same path in another PowerShell
window:

```powershell
$socket = "$env:TEMP\qrypt.sock"
qrypt.exe debug collect --socket $socket --mount local
qrypt.exe debug collect /local/example.txt --socket $socket --mount local
qrypt.exe debug raw health --socket $socket
```

This requires a Windows version with AF_UNIX support. The `--socket` value is a
filesystem path, not a Windows named pipe. Paths such as `\\.\pipe\qrypt` are
not supported.

## Choose A Command

| Goal | Command |
| --- | --- |
| Collect one structured diagnosis | `debug collect --mount NAME` |
| Sample state while reproducing a timing issue | `debug watch --mount NAME` |
| Create a shareable zip artifact | `debug bundle --mount NAME --out FILE` |
| Query one socket endpoint | `debug raw ENDPOINT` |
| Verify driver credentials | `debug test auth --mount NAME` |
| Verify driver CRUD behavior | `debug test crud --mount NAME` |
| Verify VFS writeback behavior | `debug test fs --mount NAME` |
| Verify resumable upload recovery | `debug test resume --mount NAME` |
| Compare performance or regressions | `debug bench ...` |

Use `collect` first when you are unsure. Use `raw` when you already know which
endpoint you need. Use `watch` when the issue only appears during a time window,
for example upload retries or stale reads.

## Inspect Uploads And Writeback

This is the most useful workflow for staging, pending upload, and slow upload
problems.

If you need to trigger a controlled VFS write, run:

```sh
go run ./cmd/qrypt debug test fs --socket /tmp/qrypt.sock --mount quark-test --size 30m
```

`debug test fs` creates temporary files through the VFS, writes data, flushes,
waits for upload, reads back, and removes the test data. It writes to the
selected mount, so use it only when temporary remote writes are acceptable.

If the issue is about interrupted uploads or resumable upload sessions, run:

```sh
go run ./cmd/qrypt debug test resume --socket /tmp/qrypt.sock --mount quark-test --size 64m
```

`debug test resume` writes one temporary file, injects a one-shot upload
`context canceled` fault after upload progress starts, waits for qrypt to retry,
then checks that the final remote file has the expected size. A passing result
means the VFS writeback path recovered from the cancellation. The JSON `outcome`
field tells you whether the driver reused a resumable session or safely fell
back to a fresh upload after the provider rejected the old session.

To inspect the current state without continuous sampling, use these raw
endpoints:

```sh
go run ./cmd/qrypt debug raw '/v1/uploads?mount=quark-test&history=1' --socket /tmp/qrypt.sock
go run ./cmd/qrypt debug raw '/v1/pending?mount=quark-test' --socket /tmp/qrypt.sock
go run ./cmd/qrypt debug raw '/v1/staging?mount=quark-test' --socket /tmp/qrypt.sock
go run ./cmd/qrypt debug raw '/v1/cache?mount=quark-test' --socket /tmp/qrypt.sock
go run ./cmd/qrypt debug raw '/v1/events?level=warn&limit=100' --socket /tmp/qrypt.sock
go run ./cmd/qrypt debug raw '/v1/debug/faults/upload-cancel' --socket /tmp/qrypt.sock
```

Useful fields:

- `uploads.uploads[]`: active and recent upload attempts, state, retry count,
  uploaded bytes, and last error.
- `pending.pending[]`: files waiting for writeback.
- `staging.mounts[].files[]`: local staging file size and whether it matches
  the pending size.
- `staging.mounts[].orphans[]`: staging files not referenced by pending state.
- `cache.mounts[].cache.journal`: pending journal size, duplicate entries, and
  whether compaction is recommended.
- `events.events[]`: recent warnings and errors.
- `faults[]`: currently armed debug upload-cancel faults. This should normally
  be empty outside an active `debug test resume` run.

For one report instead of separate raw calls:

```sh
go run ./cmd/qrypt debug collect --socket /tmp/qrypt.sock --mount quark-test
```

For time-series samples during a reproduction window:

```sh
go run ./cmd/qrypt debug watch --socket /tmp/qrypt.sock --mount quark-test --duration 120s --interval 2s
```

## Inspect Reads And Read Cache

For stale reads, unexpectedly slow reads, or cache behavior, start with a path
focused report:

```sh
go run ./cmd/qrypt debug collect /quark-test/path/file.bin --socket /tmp/qrypt.sock --mount quark-test
```

Raw endpoints:

```sh
go run ./cmd/qrypt debug raw '/v1/reads?mount=quark-test&path=/quark-test/path/file.bin' --socket /tmp/qrypt.sock
go run ./cmd/qrypt debug raw '/v1/cache?mount=quark-test&path=/quark-test/path/file.bin' --socket /tmp/qrypt.sock
go run ./cmd/qrypt debug raw '/v1/consistency?path=/quark-test/path/file.bin' --socket /tmp/qrypt.sock
```

Useful fields:

- `reads.reads[]`: recent read events, source, bytes, cache hits/misses, and
  errors.
- `cache.mounts[].cache`: read cache counters and per-file cache details.
- `consistency.report`: whether the path is pending, found remotely, and size
  matched.

## Inspect Cross-Mount Transfers

For copy or transfer issues, provide both endpoints and both mount names:

```sh
go run ./cmd/qrypt debug collect /local/source.bin \
  --dest /quark/archive/source.bin \
  --socket /tmp/qrypt.sock \
  --mount local \
  --mount quark
```

Useful commands:

```sh
go run ./cmd/qrypt debug test xfer --source local --dest quark --size 16m --socket /tmp/qrypt.sock
go run ./cmd/qrypt debug test xfer --source local --dest quark --size 16m --vfs --socket /tmp/qrypt.sock
go run ./cmd/qrypt debug bench xfer --source local --dest quark --size 16m --socket /tmp/qrypt.sock --samples 3 --sample-interval 2s
```

`test xfer` and `bench xfer` create temporary remote objects. The `--vfs` mode
tests transfer behavior through the VFS layer instead of directly through
drivers.

## Inspect Driver Behavior

Use driver tests when the question is about the provider API or credentials,
not local staging or VFS writeback.

Read-only auth check:

```sh
go run ./cmd/qrypt debug test auth --mount quark-test --socket /tmp/qrypt.sock
```

Write-capable CRUD check:

```sh
go run ./cmd/qrypt debug test crud --mount quark-test --socket /tmp/qrypt.sock
```

Driver state and health:

```sh
go run ./cmd/qrypt debug raw '/v1/driver?mount=quark-test' --socket /tmp/qrypt.sock
go run ./cmd/qrypt debug raw '/v1/mounts/health?mount=quark-test' --socket /tmp/qrypt.sock
```

`debug test auth` does not create remote objects. `debug test crud` creates
temporary remote objects and cleans them up best-effort.

## Create A Shareable Bundle

Use `bundle` when you want a zip file that another contributor or AI assistant
can inspect:

```sh
go run ./cmd/qrypt debug bundle --socket /tmp/qrypt.sock --mount quark-test --out /tmp/qrypt-debug.zip
```

For a path-specific issue:

```sh
go run ./cmd/qrypt debug bundle /quark-test/path/file.bin \
  --socket /tmp/qrypt.sock \
  --mount quark-test \
  --out /tmp/qrypt-path-debug.zip
```

For a transfer issue:

```sh
go run ./cmd/qrypt debug bundle /local/source.bin \
  --dest /quark/archive/source.bin \
  --socket /tmp/qrypt.sock \
  --mount local \
  --mount quark \
  --out /tmp/qrypt-transfer-debug.zip
```

For timing-sensitive issues, include a watch window:

```sh
go run ./cmd/qrypt debug bundle /quark-test/path/file.bin \
  --socket /tmp/qrypt.sock \
  --mount quark-test \
  --watch 30s \
  --out /tmp/qrypt-watch-debug.zip
```

Existing files are not overwritten unless `--force` is provided. `--goroutines`
adds a goroutine dump and should be used only when runtime deadlock or blocking
is suspected. `--all-mounts` can make the bundle much larger; prefer
`--mount NAME` when the relevant mount is known.

## Offline Checks

Offline checks read cache files and do not require a running mount:

```sh
go run ./cmd/qrypt fs pending --config ./qrypt.toml
go run ./cmd/qrypt fs pending --config ./qrypt.toml --verbose
go run ./cmd/qrypt fs journal --config ./qrypt.toml
go run ./cmd/qrypt fs journal --config ./qrypt.toml --json
go run ./cmd/qrypt fs journal --config ./qrypt.toml --mount quark-test
```

`fs pending --verbose` shows virtual path, expected size, staging file path,
retry count, last error, and next retry time. `fs journal` checks
`pending.jsonl`, missing staging files, size mismatches, and orphan staging
files.

## Endpoint Reference

Use `debug raw` when you need one endpoint directly:

```sh
go run ./cmd/qrypt debug raw health --socket /tmp/qrypt.sock
go run ./cmd/qrypt debug raw '/v1/state?mount=quark-test' --socket /tmp/qrypt.sock
```

Common endpoints:

| Endpoint | Purpose | Size notes |
| --- | --- | --- |
| `/v1/health` | Debug socket health | Small |
| `/v1/runtime` | Go runtime and memory summary | Small |
| `/v1/state?mount=NAME` | Mount snapshot | Can grow with pending, cache, events |
| `/v1/pending?mount=NAME` | Pending writeback files | Grows with pending count |
| `/v1/uploads?mount=NAME&history=1` | Active and recent uploads | Bounded history |
| `/v1/reads?mount=NAME` | Recent read events | Bounded history |
| `/v1/staging?mount=NAME` | Staging files and orphan staging files | Grows with staging files |
| `/v1/cache?mount=NAME` | Read cache and pending journal health | Grows with cache files |
| `/v1/events?level=warn&limit=100` | Recent warnings/errors | Limited by `limit` |
| `/v1/debug/faults/upload-cancel` | Armed upload cancellation test faults | Small |
| `/v1/driver?mount=NAME` | Driver debug snapshot and metrics | Driver dependent |
| `/v1/mounts/health?mount=NAME` | Recent operation health | Small |
| `/v1/resolve?path=PATH` | Resolve a virtual path | Small |
| `/v1/consistency?path=PATH` | Compare pending and remote state for a path | Small for one path |

Raw endpoints can take repeated `mount=NAME` query parameters when multiple
mounts are relevant.

## Benchmarks

Use benchmarks when you need comparable performance artifacts:

```sh
go run ./cmd/qrypt debug bench crud --mount quark-test --socket /tmp/qrypt.sock
go run ./cmd/qrypt debug bench crud --mount quark-test --socket /tmp/qrypt.sock --samples 3 --sample-interval 2s
go run ./cmd/qrypt debug bench fs --mount quark-test --socket /tmp/qrypt.sock --samples 3 --sample-interval 2s
go run ./cmd/qrypt debug bench xfer --source local --dest quark --size 16m --socket /tmp/qrypt.sock --samples 3 --sample-interval 2s
go run ./cmd/qrypt debug bench compare --base baseline.json --current current.json
```

Benchmark reports include `summary`, `assessment`, `environment.network_probe`,
`samples[]`, `cases[]`, and `events[]`. With `--samples`, the summary includes
median, p95, max duration, throughput statistics, and coefficient of variation.

`bench fs` reuses the VFS smoke test. It reports VFS operations such as
`write`, `flush`, `wait_upload`, `read`, `remove`, and `wait_cleanup`. Its
`summary.vfs` section includes pending/upload/delete drain state, cache hit
ratio, cache errors, staging orphan counts, staging size mismatch counts, and
active loader counters.

## Event And Snapshot Notes

Mount state is reported as `vfs.MountSnapshot`:

- `identity`: mount name, driver name, root, capabilities, and encryption state
- `queues`: upload/delete queue and timer state
- `overlay`: pending files, delayed deletes, rename overlays, restored
  directories, and hidden copy entries
- `upload_state`: active and historical uploads
- `cache`: read cache and pending journal health
- `events`: recent `drive.MetricEvent` streams such as VFS reads and driver
  metrics
- `runtime`: chunk, window, and prefetch activity counters

Events use `drive.MetricEvent`. `op_id`, `step`, and `name` are correlation
labels. `kind` identifies the event family such as `driver`, `vfs_read`,
`vfs_upload`, or `transfer`. `operation` is the low-cardinality operation name
used for comparisons, and `phase` is the measured sub-step.

HTTP debug events must keep secrets masked. Include method, sanitized URL
without query parameters, status, duration, request field names, response size,
and short masked error snippets only. Do not include cookies, session keys,
signed upload URLs, encrypted request blobs, or full response bodies.

## Safety Notes

- `collect`, `watch`, `bundle`, and normal `raw` GET endpoints are observation
  tools.
- `debug test` and `debug bench` may create, upload, read, rename, or delete
  temporary remote objects.
- `debug test fs --size` accepts bytes or `k`, `m`, `g` suffixes. Large values
  consume upload time and provider bandwidth.
- `debug test resume --size` has the same size format. It intentionally cancels
  one upload attempt through the VFS debug fault injector, then waits for normal
  retry or resumable-upload recovery.
- `--all-mounts` can significantly increase report size.
- `--goroutines` is useful for blocking or deadlock investigations, but it can
  make bundles larger.
