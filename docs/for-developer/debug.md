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

## Run One Test With A Temporary Mount

Use `scripts/dev/debug-mount.sh` when you need to run one `probe test` against
a real qrypt mount from the current checkout, but do not need to keep the mount
running for interactive investigation. This is useful for repeatable driver and
VFS regression checks such as `batchupload`, `batchmove`, and `fs`: the script
builds qrypt, creates an isolated temporary mount point and debug socket, waits
for both to become ready, runs the selected test, then unmounts and removes the
temporary local files. Read caches, thumbnail caches, upload staging, driver
state, logs, and temporary data are also redirected below that temporary
directory instead of using the config's `[storage]` paths. For valid test
arguments, standard output contains only the `qrypt probe test` result, so it
can be captured or compared directly.

The selected mount must exist in the config and have `test_enabled = true`.
The script supports single-mount tests only; use the normal `qrypt mount` and
`qrypt probe test xfer` commands when testing transfers between two mounts. Use
the manual workflow later in this guide when the mount must remain available
for `collect`, `watch`, raw endpoints, or an interactive reproduction.

Run a test with the repository `qrypt.toml`:

```sh
./scripts/dev/debug-mount.sh quark-test batchmove --count 50 --size 4k
./scripts/dev/debug-mount.sh quark-test read --size 256m --samples 3 --cache-mode both --pattern both --seek-count 10 --seek-size 1m
./scripts/dev/debug-mount.sh quark-test read --size 256m --cache-mode cold --pattern seek --seek-scenario prefetch
```

Pass a different config before the mount name:

```sh
./scripts/dev/debug-mount.sh --config ./path/to/qrypt.toml quark-test fs --size 30m
```

List all available test cases or inspect the wrapper options:

```sh
./scripts/dev/debug-mount.sh quark-test --help
./scripts/dev/debug-mount.sh --help
```

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
go run ./cmd/qrypt mount --config ./qrypt.toml
```

Mount always starts the built-in per-config local endpoint. Unix-like systems
use a filesystem socket; Windows uses a named pipe. Then connect using the
normal command:

```sh
go run ./cmd/qrypt debug collect --mount quark-test
```

When the same config is discoverable or passed with `--config`, debug commands
resolve the built-in endpoint automatically:

```sh
go run ./cmd/qrypt debug collect --config ./qrypt.toml --mount quark-test
```

Then collect one focused report:

```sh
go run ./cmd/qrypt debug collect --mount quark-test
```

If a specific path is involved, include it:

```sh
go run ./cmd/qrypt debug collect /quark-test/example.bin --mount quark-test
```

On Unix-like systems the socket file is created with `0600` permissions.
qrypt refuses to replace a live endpoint and removes stale Unix socket files
during startup. Windows named pipes use the operating system's pipe access
control instead of filesystem permissions.

Android and remote-device debugging uses the Android app's explicit loopback
debug server and tools such as `adb forward`; the CLI mount endpoint is local
and does not expose the debug API on the LAN.

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

On Windows, qrypt uses a named pipe derived from the per-config runtime
identity; the CLI does not require an endpoint path:

```powershell
qrypt.exe mount --config .\qrypt.toml
```

Keep the mount process running, then use the same path in another PowerShell
window:

```powershell
qrypt.exe debug collect --mount local
qrypt.exe debug collect /local/example.txt --mount local
qrypt.exe debug raw health
```

The runtime identity is created from the resolved qrypt configuration and is
shared by mount, debug, probe, and control commands. Named pipe access is
restricted by the Windows pipe security descriptor; no password is required
for the default same-user local workflow.

## Choose A Command

| Goal | Command |
| --- | --- |
| Collect one structured diagnosis | `debug collect --mount NAME` |
| Sample state while reproducing a timing issue | `debug watch --mount NAME` |
| Create a shareable zip artifact | `debug bundle --mount NAME --out FILE` |
| Query one socket endpoint | `debug raw ENDPOINT` |
| Verify driver credentials | `probe test auth --mount NAME` |
| Verify driver CRUD behavior | `probe test crud --mount NAME` |
| Verify VFS upload behavior | `probe test fs --mount NAME` |
| Measure full mounted-file read latency and throughput | `probe test read --mount NAME --mount-point PATH` |
| Clear one mount's read cache for a cold run | `probe cache clear --mount NAME` |
| Drop one file's read cache | `probe cache invalidate --mount NAME --path REMOTE` |
| Warm one file's read cache through the mount | `probe cache fill --mount NAME --path REMOTE --bytes 64m` |
| Verify the read shape the provider actually executed | `probe verify read --mount NAME --path REMOTE --window 4MiB --streams 3` |
| Print stable diagnostics JSON | `debug events --json`, `debug ops --json`, `debug read-memory --json` |
| Reproduce a stalled or truncated provider read | `probe fault read --path REMOTE --kind stall` |
| Emulate a per-connection provider throttle | `probe fault read --path REMOTE --kind rate --rate 150k` |
| Verify same-directory batch upload caching | `probe test batchupload --mount NAME` |
| Measure same-mount batch moves | `probe test batchmove --mount NAME` |
| Verify resumable upload recovery | `probe test resume --mount NAME` |
| Run the full contract suite | `probe test contract --mount NAME` |
| Compare performance or regressions | `probe bench ...` |

`probe test contract` runs the complete suite against the named mount
(auth, contract, crud, fs, instantupload, resume, multipart) and requires a
running mount server (`qrypt mount --config ./qrypt.toml`). The built-in
per-config probe socket is resolved automatically. The mount must
have `test_enabled = true` in its `[[mounts]]` block; unlisted mounts are
rejected with 403. This is the same suite the Contract Tests CI workflow
runs nightly against real providers.

Use `collect` first when you are unsure. Use `raw` when you already know which
endpoint you need. Use `watch` when the issue only appears during a time window,
for example upload retries or stale reads.

### Read Structured Diagnostics JSON

The read-only diagnostics commands also print one stable JSON object when you
pass `--json`. The object keeps the raw endpoint payload under `data` and adds
the fields a log pipeline can key on:

```json
{
  "schema_version": 1,
  "command": "debug events",
  "generated_at": "2026-11-19T05:00:00Z",
  "socket": "/Users/me/.qrypt/run/debug.sock",
  "data": { "schema_version": 2, "generated_at": "...", "events": [] }
}
```

`debug read-memory --json` follows the same shape. With `--watch` it prints one
compact envelope per poll as JSON Lines, so `--json --watch` can be piped into
`jq` or a log shipper. Without `--json` the default output is unchanged: the raw
endpoint body for `read-memory`, and one line per event or operation for the new
commands.

```sh
# Warnings and errors, filtered like /v1/events supports.
qrypt debug events --level warn --limit 200 --mount quark-encrypt --json
qrypt debug events --component QUARK --path /quark-encrypt/videos/movie.mp4

# In-flight read, chunk, window, and prefetch operations.
qrypt debug ops --mount quark-encrypt --json
```

`debug events` accepts `--level` (default `warn`), `--limit` (default 100),
`--path` (substring over the message), `--component`, and repeated `--mount`.
`debug ops` accepts repeated `--mount`. Human output prints one operation per
line with `op=`, `id=`, requested bytes, offset, background flag, wait reason,
and age in milliseconds.

## Inspect Uploads And Upload

This is the most useful workflow for staging, pending upload, and slow upload
problems.

If you need to trigger a controlled VFS write, run:

```sh
go run ./cmd/qrypt probe test fs --mount quark-test --size 30m
```

`probe test fs` creates temporary files through the VFS, writes data, flushes,
waits for upload, reads back, and removes the test data. It writes to the
selected mount, so use it only when temporary remote writes are acceptable.

Cleanup runs even when the probe is canceled or an upload fails: it uses a
context detached from the request, discards pending uploads under the test
directory, and waits for the delayed remote deletes. A permanently failed
upload is reported as a terminal failure instead of blocking the idle wait
until its timeout; inspect it with `qrypt fs journal` and clear it with
`qrypt fs journal prune --failed` after unmounting. Only a hard process kill
can still leave `__qrypt_fs_test_*` directories behind for manual removal.

For small-file and move workloads, run:

```sh
go run ./cmd/qrypt probe test batchupload --mount quark-test --count 50 --size 4k
go run ./cmd/qrypt probe test batchmove --mount quark-test --count 50 --size 4k
```

`batchupload` writes and flushes every file through VFS, waits for the upload
queue to become idle, verifies the directory and sampled content, and reports
the parent-directory cache miss/hit/shared counts. When upload history is
available, more than one parent-cache miss fails the test. `batchmove` first
uploads the source batch and waits for it to become idle, then measures only
the sequential VFS `Rename` calls before verifying the empty source and the
destination data. Both tests clean their randomized temporary directories.

To measure the complete mounted-file read path, including the OS filesystem,
FUSE/macFUSE adapter, VFS read cache and remote driver, run:

```sh
./scripts/dev/debug-mount.sh quark-test read --size 256m --samples 3 --cache-mode both --pattern both --seek-count 10 --seek-size 1m
```

The wrapper supplies its temporary mount point automatically. When invoking
qrypt directly, pass the real FUSE mount directory explicitly:

```sh
go run ./cmd/qrypt probe test read --mount quark-test --mount-point /Volumes/qrypt --size 256m --block-size 1m --samples 3 --cache-mode both --pattern both --seek-count 10 --seek-size 1m
```

The test creates and uploads a deterministic temporary file before timing,
then reads it with `os.Open` through the supplied mount point. Setup and cleanup
are excluded from the read measurements. `cold` clears qrypt's durable and
in-memory read caches; every measured read requests OS cache bypass/drop where
supported, while `warm` keeps qrypt's read-cache state from the preceding read.
`both` reports both modes. A multi-sample run re-reads the same range, so
later samples can hit bytes an earlier sample fetched; add `--fresh-offsets`
(with `--path`) to advance every sample by one read length, or clear the cache
between samples. `--hold-open` and `--timeline` switch the run to sustained-session
shape and per-second delivery detail. Each measurement includes open and time-to-first-byte
latency, total and steady throughput, peak throughput over a one-second window,
data-bearing read-call latency percentiles, cache hit/miss deltas, VFS read-call
count, byte count, and content-hash validation. The test fails when the supplied
path bypasses qrypt (for example, when `--mount-point` names the backing
directory instead of the FUSE mount). `metrics_truncated` marks a measurement
whose phase-level history exceeded the bounded runtime history. `resolved_plan`
reports the plan the read layer resolved from the driver policy and the
measurement override (stage, intent, window/streams/part/prefetch fields), so a
shape can be checked before any bytes move; `wire_bytes_trusted` and
`amplification_trusted` say whether those derived numbers are complete for that
measurement (both are false when `metrics_truncated` is true). The test does
not use a fixed network-speed threshold.

`--pattern sequential` preserves the full-file test above and remains the
default. Add `--offset` and/or `--length` to measure one range instead of the
whole file; both flags only apply to sequential reads. `--offset` accepts a
byte offset (including `0`), and `--length` accepts a positive byte count.
When `--length` is omitted, the measurement runs from the offset to EOF.

```sh
qrypt probe test read --mount quark-encrypt --mount-point ~/Qrypt \
  --path /quark-encrypt/videos/movie.mp4 \
  --offset 12g --length 16m --cache-mode cold
```

`--pattern seek` opens the mounted file, seeks to widely spaced
offsets, and loads `--seek-size` bytes at each offset;
`--pattern both` runs both patterns. Seek output separates the local seek call
(`seek_us`) from the following data load (`seek_load_us`) and their combined
latency (`seek_total_us`). `seek_summary` reports cold/warm P50, P95, P99, max,
and load throughput. Cold probes clear qrypt's read cache before every offset;
warm probes read the same offset immediately after it has been primed. The
result also requires `vfs_offset_match=true`, proving that the measured FUSE
read reached qrypt at the requested seek offset. The defaults are 10 probes of
1 MiB each, with a maximum of 32 probes.

Use `--seek-scenario` to control the load present when each seek starts:

- `isolated` is the default baseline and issues no intentional competing read.
- `prefetch` first reads sequential chunks on the same file descriptor, waits
  until qrypt reports an active background prefetch for that file, then seeks
  on that descriptor.
- `concurrent` starts a bounded sequential read on a second file descriptor,
  waits until qrypt reports its active chunk load or resulting prefetch, then
  seeks on the first descriptor.

For example:

```sh
./scripts/dev/debug-mount.sh quark-test read \
  --size 256m --cache-mode cold --pattern seek \
  --seek-scenario concurrent --seek-warmup-chunks 2 \
  --seek-overlap-timeout 2s
```

The loaded range and seek target are kept apart so the competing request does
not pre-warm the bytes being measured. The test synchronizes against qrypt's
active-operation state instead of sleeping for a fixed interval. Every loaded
scenario reports `overlap`, `overlap_wait_us`, `active_kind`, `active_offset`,
and `active_requested`; it fails if no matching in-flight request is observed
within `--seek-overlap-timeout`. Every probe waits for its reads and prefetches
to become idle before its events are collected; the wait is excluded from
`seek_load_us`. This prevents old work from satisfying the next overlap check,
repopulating a newly cleared cache, or being attributed to the following
measurement. `chunks_touched` distinguishes aligned single-chunk loads from
cross-chunk loads. Compare `isolated`, `prefetch`, and `concurrent` runs with the
same size, cache mode, probe count, and seek size.

### Simulate Playback On A Real File

`scripts/dev/playback-sim.py` replays the access pattern of a video player on
an existing mounted file: it reads ahead through the FUSE mount at a target
bitrate, fills a bounded buffer, seeks on an interval, and prints live
throughput, block latency, buffer depth, and underrun count. It needs no test
enablement and writes nothing, so it works on a production mount.

```sh
scripts/dev/playback-sim.py \
  --path ~/Qrypt/quark-encrypt/movie.mp4 \
  --duration 60s --bitrate 25m --block 4m \
  --seek-every 10s --seek-mode random \
  --mount quark-encrypt
```

Pass `--max-speed` to measure raw download throughput instead of playback
behaviour: the reader runs flat out, seeks are disabled, and the summary adds
`avg_mib_s`, `steady_mib_s` (excluding `--warmup` seconds), `peak_1s_mib_s`,
and the p50/p95 of one-second windows. `--start OFFSET` picks a cold region so
qrypt's read cache does not inflate the number.

```sh
scripts/dev/playback-sim.py --path ~/Qrypt/quark-encrypt/movie.mp4   --max-speed --start 25g --duration 12s --block 512k --mount quark-encrypt
```

The summary reports median/p95 read latency, seek latency, stalls, and (with
`--mount`) the matching `/v1/reads` window events split into
`open_us`, `body_us` and `close_us` (microseconds, summed over attempts). That split distinguishes provider
time-to-first-byte from body throughput, and the event count shows whether a
stall came from the remote read or from the cache. `--warm` keeps the OS page
cache instead of bypassing it with `F_NOCACHE`; `--json FILE` stores the
per-second series for comparison between runs.

### Compare Two Builds On The Same File

`scripts/dev/read-ab.py` measures a control build against the working tree
with `qrypt mount --isolated` mounts: each side gets its own temporary mount
point, `QRYPT_HOME`, read cache and log file, so the production mount, its
cache, and its logs never affect the numbers. The script builds the control from a git ref in a
temporary worktree (uncommitted changes stay untouched), mounts both sides with
quiet logging and no bandwidth limit, and interleaves playback-shaped reads
with rotating order. Each round reads a fresh region; samples where the
provider stalls on TTFB or where qrypt's cache serves the read are excluded and
retried at a shifted offset.

```sh
scripts/dev/read-ab.py --mount quark-encrypt \
  --path "/videos/sample.mp4" \
  --offsets 200,450,650,850 --length 2MiB --rounds 2
```

The table reports each sample's throughput and TTFB plus the candidate/control
ratio; the summary gives the median over clean pairs, which is the number to
decide on (individual pairs move with CDN variance). `--amplify` adds a second
debug-logging pass that reports provider wire bytes per delivered byte for both
sides, and `--min-ratio` / `--max-amplification` turn the run into a gate: the
process exits nonzero when the clean median ratio or the candidate amplification
misses the threshold, so a nightly or pre-merge check needs no hand-written
comparison. For sustained playback shape, add `--length 64MiB` and one offset;
the per-second timeline comes from `probe test read --hold-open --timeline` when
a single read's rate drop is the question. The script needs a client config with
credentials (default: repository `qrypt.toml`) and Python 3.11+. The control
build must include `qrypt mount --isolated`, so compare against a revision at
or after the isolated mount landed. `--keep` keeps the temporary binaries,
logs, and results for inspection.

### Tune A Read Shape

`probe tune read` sweeps read shapes against one existing mounted file and
prints the fastest one as a TOML block. It needs the running mount's control
socket and the mount point, writes nothing remote, and therefore also works on
a read-only mount:

```sh
qrypt probe tune read --mount quark-encrypt \
  --mount-point /mnt/qrypt/quark-encrypt \
  --path /videos/sample.mp4 --size 32m --rounds 3
```

The run measures playback-shaped cold reads for the interactive window
(`caller`, 1, 2, 4, 8 MiB), then lookahead depth at the best window, then bulk
downloads (`8`/`16` MiB window with one or two streams). Every variant is
measured with the same block size and fresh region, and the order rotates per
round so a provider rate change cannot favour one shape. The table reports the
median/min/max of each variant; the evidence lines name the winner of each
phase, and the config block is ready to paste under the mount in `qrypt.toml`.
Add `--json` to keep the raw result (including every sample) for a report.

`scripts/dev/tune.py` wraps the common case: it builds the working tree (or
reuses `--binary`), starts a read-only `qrypt mount --isolated` mount from the
client config, runs the same sweep and removes the temporary mount on exit, so
no rewritten credential config is left behind:

```sh
scripts/dev/tune.py --mount quark-encrypt --path /videos/sample.mp4 \
  --size 32m --rounds 3
```

Use `--json` for the raw result, `--keep` to inspect the temporary mount and
`--log-level debug` when a window shape needs to be checked in the log (the
mount writes it to `runtime.log` under the temporary root).

The measurement uses the same plan override as `probe test read`, which is
also reachable through its individual flags: `--window`, `--prefetch-window`,
`--prefetch-depth` (`--no-prefetch` for depth 0), `--streams`, `--part-size`
and `--min-parallel` set the executed shape for one measurement, with `0`
keeping the resolved value for `--streams` and `auto` meaning an even split for
`--part-size`. The override is applied through the mount's read layer, so it
affects the same code path a real playback read takes; the same flags are
covered by `probe test read` when a single shape needs confirmation.

### Benchmark An Existing File

`probe bench read` uploads a generated fixture by default, which needs a
writable mount. Add `--path` to read an existing virtual file through the
driver instead, so read-only mounts and read-only drivers can be measured:

```sh
qrypt probe bench read --mount koofr --path "/My videos/movie.mp4" --size 32m --concurrency 1,2,4,8
```

The benchmark still bypasses the VFS and encryption layers; `--size` defaults
to the smaller of the file and 32 MiB and is clamped to the file length.

### Measure Cache-Miss Load Time

`playback-sim.py` bypasses the OS page cache by default but still reads through
qrypt's own durable and in-memory cache. Clear that cache without restarting
the mount:

```sh
qrypt probe cache clear --mount quark-encrypt
```

The command talks to the running mount's probe socket, drops the durable
chunks, hot chunks, range-hit state, and sequential-read state, and returns
JSON. It needs the mount to have `test_enabled = true` (or an isolated
`QRYPT_HOME` test mount); otherwise the probe socket rejects the request with
`mount "NAME" is not enabled for probe tests`. In-flight reads finish normally; the next read starts cold. Add `--cold`
to `playback-sim.py` to run the clear immediately before the measurement:

```sh
scripts/dev/playback-sim.py \
  --path ~/Qrypt/quark-encrypt/movie.mp4 \
  --max-speed --start 25g --duration 12s --block 512k \
  --cold --mount quark-encrypt
```

To make one file cold without clearing the whole mount, invalidate just that
path:

```sh
qrypt probe cache invalidate --mount quark-encrypt --path /quark-encrypt/videos/movie.mp4
```

`--mount` is optional but recommended when the virtual path spelling matters:
the probe socket resolves the mount from the path, so a path that belongs to a
different mount fails the command instead of silently dropping another mount's
chunks. `probe cache invalidate --json` prints the stable diagnostics envelope.

To warm one file instead, read it through the mount before measuring. The fill
command goes through the same VFS read path as a player, so it populates the
durable chunks and hot chunks a mount actually serves:

```sh
qrypt probe cache fill --mount quark-encrypt --path /quark-encrypt/videos/movie.mp4 --bytes 64m
qrypt probe cache fill --mount quark-encrypt --path /quark-encrypt/videos/movie.mp4 --bytes 0 --json
```

`--bytes 0` fills the whole file up to the server cap (1 GiB); `--block-size`
controls the per-read granularity (default 1 MiB, 4 KiB–16 MiB). The command
returns the file size, the bytes actually read, the read-call count, the
duration, and the read-cache byte count before and after, so a warm-up can be
confirmed in the same JSON. `probe cache clear` and `probe cache invalidate` also
accept `--json` and emit the same stable envelope as the diagnostics commands.

For seek tests where every jump should start cold, pass `--cold-every-seek`
instead of clearing the whole mount on each seek:

```sh
scripts/dev/playback-sim.py \
  --path ~/Qrypt/quark-encrypt/movie.mp4 \
  --bitrate 25m --seek-every 10s --cold-every-seek --mount quark-encrypt
```

The script derives the virtual path from `--path` and `--mount`; pass
`--remote-path /quark-encrypt/...` if the mount directory name differs from the
configured mount name. `--cold` requires `--mount` and clears through the probe
socket next to `--socket` (normally `~/.qrypt/run/probe.sock`); pass
`--probe-socket` when the probe socket lives elsewhere. Repeat seeks within one
run can still hit what that run already fetched; run the script again, or seek
far away, when every sample must be a cache miss. `probe test read --cache-mode
cold` remains the benchmark-native option: it clears the cache before every
cold sample and also reports mounted read latency, throughput, and seek
behavior. When measuring an existing file with `--path`, add
`--fresh-offsets` to advance every sample by one read length (or
`--offset-step` bytes) instead of re-reading the same range; each sample then
lands in a region no earlier sample fetched. Each measurement also reports
`wire_bytes` and `amplification` (provider bytes per delivered byte) computed
from the window events of that read, so over-fetch on a slow link shows up in
the same output as the latency and throughput numbers. `executed_streams`,
`executed_window_chunks`, `executed_window_bytes` and `executed_part_bytes`
report the shape the provider actually ran, so a plan override or a driver
default can be checked against what executed instead of what was requested.
When `--path` points at a file that is still uploading, the command refuses to
measure it: reads of a pending file can come from local staging, so the run
waits for the upload to drain instead of reporting local disk speed as a
provider result. `--require-parallel` turns the executed shape into a gate: the
run fails when every window ran single-stream, which is how a declared
`parallel_range` capability or a bulk stream policy is proven to reach the
provider instead of only being configured.

### Interleaved A/B Reads

`qrypt probe ab read` compares two read shapes on the same file without
relying on provider state staying quiet between runs: rounds alternate A/B and
B/A, both sides measure the same offsets with the cold reader, and the report
carries per-round throughput/TTFB, the median ratio, a winner with a 5 %
threshold, and each side's `resolved_plan`. Shape strings reuse the
`probe test read` flags as `key=value` tokens (`window=4m`,
`prefetch-window=2m`, `prefetch-depth=4`, `no-prefetch`, `part-size=auto`,
`min-parallel=8m`, `streams=2`); the default control side is the driver
default, so one command answers "does this override beat what the policy
already does":

```sh
qrypt probe ab read --mount drime --mount-point ~/Qrypt \
  --path /movie.mp4 --size 16m --rounds 3 --a control --b "window=4m,prefetch-window=4m"
```

### Verify The Executed Read Shape

Plan overrides and driver defaults describe intent; `probe verify read` checks
the shape the provider actually executed. It clears the mount's read cache,
reads the file once with the requested window, streams, prefetch depth, and
optional part size, then compares the executed window/stream/prefetch shape
against the request:

```sh
qrypt probe verify read --mount quark-encrypt \
  --path /quark-encrypt/videos/movie.mp4 \
  --window 4MiB --streams 3 --prefetch-depth 2 --length 16m
```

The command resolves the mount point from the config, clamps `--length` to the
file size, and runs `probe test read` internally with a cold cache and one
sample. It exits `0` when the executed shape matches and `4` when it does not;
use `4` as the regression signal in scripts. `--json` prints the flat report
(`requested`, `executed`, `counters`, `measurements`, `mismatches`, `warnings`)
instead of the human summary.

The comparison covers four facts:

- window bytes must equal the requested window rounded up to the 512 KiB read
  chunk, and the executed chunk count must match;
- requested streams greater than one must run multi-stream (a single-stream
  execution is a mismatch; adaptive split above the request is only a warning);
- a requested part size must be the executed part size (an executed zero is a
  warning, not a mismatch);
- a prefetch depth greater than zero must produce prefetch windows, read from
  `executed_prefetch_windows` or the `/v1/read-memory` prefetch counter delta.

Cache hits and read or prefetch timeouts add warnings instead of mismatches.
Each measurement still carries the `executed_streams`,
`executed_window_chunks`, `executed_window_bytes`, `executed_part_bytes`, and
`executed_prefetch_windows` fields documented for `probe test read`.

### Inject Read Faults

A real provider only misbehaves when it wants to. Arm a fault on the running
mount to exercise the same recovery paths on demand:

```sh
# Stop delivering bytes after 1 MiB: the idle timeout should abort and replay.
qrypt probe fault read --path /quark-encrypt/videos/movie.mp4 --kind stall --after 1m

# End the body early: the shared layer should replay it once.
qrypt probe fault read --path /quark-encrypt/videos/movie.mp4 --kind truncate --after 2m

# Answer the next Range GET with 500: the retryable-status backoff should run.
qrypt probe fault read --path /quark-encrypt/videos/movie.mp4 --kind status --status 500

# Delay the body's first byte so open latency is visible.
qrypt probe fault read --path /quark-encrypt/videos/movie.mp4 --kind delay --delay 2s

# Pace the body at 150 KiB/s: every Range read behaves like a throttled
# per-connection CDN link, which is how parallel fan-out and first-byte
# latency changes are measured without a real throttled provider.
qrypt probe fault read --path /quark-encrypt/videos/movie.mp4 --kind rate --rate 150k --times 10000

qrypt probe fault list
qrypt probe fault clear            # all rules
qrypt probe fault clear --id ID    # one rule
```

Rules fire once by default, so the first attempt fails and the replay succeeds.
`--times N` keeps the fault active for more attempts; `--offset` and `--length`
scope it to a byte range. Rules skip background prefetch windows by default so
a canceled speculative read cannot swallow the rule; add `--include-prefetch`
when the prefetch path itself is under test. `--kind rate` paces the body
instead of failing it, and needs enough `--times` to cover every read in the
measurement (a cold playback read issues several windows). Faults live in the
running mount and disappear on restart.

Quark's driver snapshot (`/v1/driver?mount=NAME`) now includes
`download_rotations`, `last_download_rotate_reason`, and `penalized_edges`
(with the reason and remaining TTL), so an edge penalty or connection rotation
can be asserted from JSON instead of grepping the log.

Combine a fault with the measurement-only timeout override to keep the test
short:

```sh
qrypt probe fault read --path /quark-encrypt/videos/movie.mp4 --kind stall --after 1m
qrypt probe test read --mount quark-encrypt --mount-point ~/Qrypt \
  --path /quark-encrypt/videos/movie.mp4 --offset 0 --length 8m \
  --cache-mode cold --idle-timeout 2s --total-timeout 5s
```

The probe result reports `timed_out` when its own read-call bound fires; the
fault's `fetch_attempts` / `fetch_timeouts` fields appear in `metrics` when the
window-level timeout path runs.

If the issue is about interrupted uploads or resumable upload sessions, run:

```sh
go run ./cmd/qrypt probe test resume --mount quark-test --size 64m
```

`probe test resume` writes one temporary file, injects a one-shot upload
`context canceled` fault after upload progress starts, waits for qrypt to retry,
then checks that the final remote file has the expected size. A passing result
means the VFS upload path recovered from the cancellation. The JSON `outcome`
field tells you whether the driver reused a resumable session or safely fell
back to a fresh upload after the provider rejected the old session.

To inspect the current state without continuous sampling, use these raw
endpoints:

```sh
go run ./cmd/qrypt debug raw '/v1/uploads?mount=quark-test&history=1'
go run ./cmd/qrypt debug raw '/v1/pending?mount=quark-test'
go run ./cmd/qrypt debug raw '/v1/staging?mount=quark-test'
go run ./cmd/qrypt debug raw '/v1/cache?mount=quark-test'
go run ./cmd/qrypt debug raw '/v1/events?level=warn&limit=100'
go run ./cmd/qrypt debug raw '/v1/events?level=warn&mount=quark-test'
```

`/v1/events` filters on the event's fields, not on the message text: `mount=`
(repeatable, matching any listed mount) selects lines produced by a
mount-scoped call site, and `component=` matches the message's leading `[TAG]`.
A line is therefore never returned for a mount it merely mentions in its text,
and `path=` remains a plain substring match over the message.

Mount attribution currently covers the pending-upload and read-cache stores, so
their journal, eviction and cleanup warnings carry `mount="NAME"` in both the
log text and the event field. Call sites that are not yet mount-scoped produce
lines with no mount field, and `/v1/events?mount=` does not return them — an
empty result means "no attributed line for that mount", not "that mount was
quiet".

Useful fields:

- `uploads.uploads[]`: active and recent upload attempts, state, retry count,
  uploaded bytes, and last error.
- `pending.pending[]`: files waiting for upload.
- `staging.mounts[].files[]`: local staging file size and whether it matches
  the pending size.
- `staging.mounts[].orphans[]`: staging files not referenced by pending state.
- `cache.mounts[].cache.journal`: pending journal size, duplicate entries, and
  whether compaction is recommended.
- `events.events[]`: recent warnings and errors.
- `faults[]`: currently armed debug upload-cancel faults. This should normally
  be empty outside an active `probe test resume` run.

For one report instead of separate raw calls:

```sh
go run ./cmd/qrypt debug collect --mount quark-test
```

For time-series samples during a reproduction window:

```sh
go run ./cmd/qrypt debug watch --mount quark-test --duration 120s --interval 2s
```

## Inspect Reads And Read Cache

For stale reads, unexpectedly slow reads, or cache behavior, start with a path
focused report:

```sh
go run ./cmd/qrypt debug collect /quark-test/path/file.bin --mount quark-test
```

Raw endpoints:

```sh
go run ./cmd/qrypt debug raw '/v1/reads?mount=quark-test&path=/quark-test/path/file.bin'
go run ./cmd/qrypt debug raw '/v1/cache?mount=quark-test&path=/quark-test/path/file.bin'
go run ./cmd/qrypt debug raw '/v1/consistency?path=/quark-test/path/file.bin'
```

Useful fields:

- `reads.reads[]`: recent read events, source, bytes, cache hits/misses, and
  errors.
- `cache.mounts[].cache`: read cache counters and per-file cache details.
- `consistency.report`: whether the path is pending, found remotely, and size
  matched.

### Reading The Read-Cache Replacement State

`cache.mounts[].cache` carries the counters visible to the eviction policy, and
they answer three different questions:

- `probationary_bytes` / `protected_bytes` split the cache by **state**: a chunk
  is probationary until a read run other than the one that admitted it touches
  it. A sequential stream consuming its own read-ahead only ever produces
  probationary content, which is why a one-pass scan shows up here rather than as
  hits.
- `pinned_bytes` is the **budget** view: chunks of files that saw a read or write
  in the last minute. A drain takes unpinned content first, so the file being
  read keeps its read-ahead while the rest of the cache pays.
- `evicted_bytes` and `promotions` are event counters: how much the cache has
  released, and how many chunks a later run has confirmed as reused.

A drain empties probationary before touching protected, so a cache whose
protected bytes hold steady across a large scan is working as intended. The
first pass skips pinned chunks; only when unpinned content cannot satisfy the
drain are pinned chunks evicted too, so the pin never suspends the `max_size`
bound.

Expect the disk footprint to settle just under `max_size`: one-pass streaming content is admitted probationary and drained as it ages, so a cache serving only streaming reads does not pin at the cap. Content that is read again is promoted to protected and is drained only after the probationary segment empties.

Read events are kept in two bounded rings and merged into one append-ordered
list: **summaries** (one per read: offset, bytes, cache hits/misses, duration,
throughput, error) retain 128 entries, and **details** (per chunk and per
window phase timing) retain 512 entries. Summaries are never evicted by a
read's detail burst, so the recent reads stay visible even while a whole-file
read walks every chunk; details are recency-biased and a large sequential read
can flush older ones. The two rings are bounded at roughly 320 KiB per mount,
allocated only once the mount has served a read.

Each mount also carries cumulative `runtime.counters`, which answer "how fast
and how reliable overall" where the bounded histories answer "which read, and
where did it spend its time":

- `ops`, `errors`, `bytes`, `duration_ms`, `mean_ms` are totals since process
  start — they never reset, so they do not equal anything in the bounded event
  history and are the right baseline for spotting degradation.
- `latency.bounds_ms` / `latency.counts` are a fixed-bucket histogram, with the
  last count being the overflow bucket. Derive a percentile from the counts:
  the reported value is that bucket's upper bound, so read it as "p95 is at
  most this". `exceed` is true when the percentile falls in the overflow
  bucket.
- `bytes` counts what the read path materialized: a remote read counts the data
  it fetched, while a staging passthrough counts 0 because it only opens a local
  file and hands the stream back. The read event for that passthrough instead
  reports the bytes the consumer drained, so the two differ by design.

## Inspect Cross-Mount Transfers

For copy or transfer issues, provide both endpoints and both mount names:

```sh
go run ./cmd/qrypt debug collect /local/source.bin \
  --dest /quark/archive/source.bin \
  --mount local \
  --mount quark
```

Useful commands:

```sh
go run ./cmd/qrypt probe test xfer --source local --dest quark --size 16m
go run ./cmd/qrypt probe test xfer --source local --dest quark --size 16m --vfs
go run ./cmd/qrypt probe bench xfer --source local --dest quark --size 16m --samples 3 --sample-interval 2s
```

`test xfer` and `bench xfer` create temporary remote objects. The `--vfs` mode
tests transfer behavior through the VFS layer instead of directly through
drivers.

## Inspect Driver Behavior

Use driver tests when the question is about the provider API or credentials,
not local staging or VFS upload.

Read-only auth check:

```sh
go run ./cmd/qrypt probe test auth --mount quark-test
```

Write-capable CRUD check:

```sh
go run ./cmd/qrypt probe test crud --mount quark-test
```

Driver state and health:

```sh
go run ./cmd/qrypt debug raw '/v1/driver?mount=quark-test'
go run ./cmd/qrypt debug raw '/v1/mounts/health?mount=quark-test'
```

`probe test auth` does not create remote objects. `probe test crud` creates
temporary remote objects and cleans them up best-effort.

## Create A Shareable Bundle

Use `bundle` when you want a zip file that another contributor or AI assistant
can inspect:

```sh
go run ./cmd/qrypt debug bundle --mount quark-test --out /tmp/qrypt-debug.zip
```

For a path-specific issue:

```sh
go run ./cmd/qrypt debug bundle /quark-test/path/file.bin \
  --mount quark-test \
  --out /tmp/qrypt-path-debug.zip
```

For a transfer issue:

```sh
go run ./cmd/qrypt debug bundle /local/source.bin \
  --dest /quark/archive/source.bin \
  --mount local \
  --mount quark \
  --out /tmp/qrypt-transfer-debug.zip
```

For timing-sensitive issues, include a watch window:

```sh
go run ./cmd/qrypt debug bundle /quark-test/path/file.bin \
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
go run ./cmd/qrypt debug raw health
go run ./cmd/qrypt debug raw '/v1/state?mount=quark-test'
```

Common endpoints:

| Endpoint | Purpose | Size notes |
| --- | --- | --- |
| `/v1/health` | Debug socket health, including task-journal persistence status | Small |
| `/v1/runtime` | Go runtime and memory summary | Small |
| `/v1/debug/stacks` | Goroutine stack dump | Text, can be large |
| `/v1/state?mount=NAME` | Mount snapshot | Can grow with pending, cache, events |
| `/v1/pending?mount=NAME` | Pending upload files | Grows with pending count |
| `/v1/tasks?type=upload_stream_batch&state=waiting_input&limit=200` | Persistent tasks with item progress and recovery linkage | Grows with task and item count; use filters |
| `/v1/task-events?after_seq=N&wait_ms=25000` | Incremental task snapshots with cursor replay and gap recovery | Bounded by the task event history and response batch |
| `/v1/uploads?mount=NAME&history=1` | Active and recent uploads | Bounded history |
| `/v1/ops?mount=NAME` | Active VFS read, chunk, window, and prefetch ops | Small, current in-flight ops only |
| `/v1/reads?mount=NAME&limit=200&since=2m` | Recent read events: 128 summaries plus 512 phase/chunk details per mount | Bounded history, filtered server-side |
| `/v1/staging?mount=NAME` | Staging files and orphan staging files | Grows with staging files |
| `/v1/cache?mount=NAME` | Read cache and pending journal health | Grows with cache files |
| `/v1/events?level=warn&limit=100&mount=NAME&component=CACHE` | Recent warnings/errors, filtered by mount and component fields | Limited by `limit` |
| `/v1/debug/cache/fill?mount=NAME&path=PATH&bytes=N&block_size=N` | Warms one file through the mount read path (probe socket only) | Bounded by `bytes`/1 GiB cap |
| `/v1/driver?mount=NAME&limit=200&since=2m` | Driver debug snapshot and metrics | Driver dependent, metrics filtered server-side |
| `/v1/mounts/health?mount=NAME` | Recent operation health | Small |
| `/v1/resolve?path=PATH` | Resolve a virtual path | Small |
| `/v1/consistency?path=PATH` | Compare pending and remote state for a path | Small for one path |

Raw endpoints can take repeated `mount=NAME` query parameters when multiple
mounts are relevant.
`/v1/reads` and `/v1/driver` also support `operation`, `phase`, `op_id`,
`remote_id`, and `error_only=1`.

## Benchmarks

Use benchmarks when you need comparable performance artifacts:

```sh
go run ./cmd/qrypt probe bench crud --mount quark-test
go run ./cmd/qrypt probe bench crud --mount quark-test --samples 3 --sample-interval 2s
go run ./cmd/qrypt probe bench fs --mount quark-test --samples 3 --sample-interval 2s
go run ./cmd/qrypt probe bench xfer --source local --dest quark --size 16m --samples 3 --sample-interval 2s
go run ./cmd/qrypt probe bench compare --base baseline.json --current current.json
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
- `read_policy`: complete recommended/effective policy, hard limits, and field
  origins; bulk sizes are bytes and fetch bounds use explicit nanosecond keys

Two events carry the read path's diagnostics, with a deliberately small fixed
field set each.

The summary `phase=read` event describes the whole read: `read_intent`,
`read_stage`, `read_streams` (granted workers), `planned_streams`, the window
(`window_chunks`), the prefetch shape (`prefetch_chunks`, `prefetch_depth`),
the split thresholds (`part_bytes`, `min_parallel_bytes`),
`cache_writes`, and both fetch bounds (`fetch_idle_timeout_ms`,
`fetch_total_timeout_ms`). These are plan-level facts; they do not repeat on
every window of the same read.

A `phase=fetch_window` / `phase=prefetch_window` detail event carries only what
explains that window: identity (`session_id`, `request_id`, `chunk_index`), shape
(`window_chunks`, `window_bytes`, `part_bytes`, `read_streams` /
`granted_streams`, `range_tasks`, `actual_in_flight`), the two shape flags
(`read_intent`, `read_stage`) plus `prefetch_chunks`/`prefetch_depth` while a
lookahead is armed, and timing (`queue_us`,
`open_us`, `body_us`, `close_us`, `local_wait_us`, `validate_us`,
`first_body_us`, `fetch_us`) with progress (`body_bytes`, `progress_end`).
The window bounds are not repeated: `chunk_index` and `window_chunks` imply them.
Local wait overlaps body time and must not be added to it. Driver open is not CDN
TTFB. Player first-frame latency requires player telemetry.


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
- `probe test` and `probe bench` may create, upload, read, rename, or delete
  temporary remote objects.
- `probe test` and `probe bench` only run against mounts with
  `test_enabled = true` in `[[mounts]]`. Transfer tests require both source and
  destination mounts to opt in.
- `probe test fs --size` accepts bytes or `k`, `m`, `g` suffixes. Large values
  consume upload time and provider bandwidth.
- `probe test batchupload` and `probe test batchmove` accept `--count` (default
  50, maximum 100) and `--size` (default 4 KiB per file, maximum 1 MiB).
- `probe test read` requires `--mount-point`. It accepts `--size` (default
  256 MiB, maximum 2 GiB), `--block-size` (default 1 MiB), `--offset` and
  `--length` for a sequential sub-range, `--idle-timeout` / `--total-timeout`
  for a measurement-only fetch-bound override, `--samples` (default
  1, maximum 10), `--cache-mode cold|warm|both` (default `both`),
  `--pattern sequential|seek|both` (default `sequential`), `--seek-count`
  (default 10, maximum 32), `--seek-size` (default 1 MiB, maximum 16 MiB),
  `--seek-scenario isolated|prefetch|concurrent` (default `isolated`),
  `--seek-warmup-chunks` (default 2, maximum 16), and
  `--seek-overlap-timeout` (default 2s, range 100ms to 30s). It creates and
  removes a temporary remote file of the requested size.
- `probe test resume --size` has the same size format. It intentionally cancels
  one upload attempt through the VFS debug fault injector, then waits for normal
  retry or resumable-upload recovery.
- `--all-mounts` can significantly increase report size.
- `--goroutines` is useful for blocking or deadlock investigations, but it can
  make bundles larger.
