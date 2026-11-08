---
name: qrypt-read-tuning
description: Tune and validate qrypt driver read concurrency with real-cloud measurements — choosing BulkStreams, BulkWindowChunks, per-stream chunk size, or whether a driver should declare parallel_range at all. Use whenever someone asks whether multi-stream/parallel reads help a driver, wants to set or verify read profiles, reports slow mounted reads, or asks to A/B read configurations with `probe test read`.
---

# qrypt Read Tuning

Decide read concurrency from measurements, not from protocol capability alone.
A driver accepting arbitrary Range requests does not mean parallel reads are
faster: fixed per-request latency, account limits, and CDN behaviour often make
them slower. This workflow produces a defensible per-driver decision and a
recorded baseline.

## Ground rules

- **Read-only on user data.** Measure existing files with
  `probe test read --path`. Never edit or delete user content. The probe's
  fixture mode creates and removes its own temporary file; prefer `--path`
  when a suitable file exists.
- **Control provider drift.** Throughput can vary 2–3x within an hour. Compare
  configurations on the same file, in the same time window, interleaved
  (A B C A B C), never "morning vs afternoon".
- **Never ship a tuning hack.** Use the measurement flags (`--streams`,
  `--window-chunks`). Only fall back to temporary `ReadProfile` or window
  overrides when the binary predates them, and then restore the source and
  confirm `git diff --quiet`.
- **Keep credentials local.** `qrypt.toml` is gitignored; never copy secrets
  into scripts, docs, commits, or chat output.

## Prerequisites

- A mount in `qrypt.toml` with `test_enabled = true`. If it is false, flip it
  locally for the experiment and restore it afterwards.
- A binary with `probe test read --path` and the `--streams` /
  `--window-chunks` measurement overrides (older builds lack them).
- macOS/Linux with a working FUSE mount, and a real mount point you control.
- A file large enough to exercise the parallel threshold (default 8 MiB).
  Prefer 16–64 MiB. Small files are dominated by startup and TTFB.
- Provider quirks: Koofr refuses downloads for `.bin` (`FileBlocked`) — use a
  `.txt` file or the probe fixture (which already uses `.txt`).

## Configuration model

The read plan maps to concrete request sizes:

- `BulkStreams` — concurrent sub-ranges per window.
- `BulkWindowChunks` — window size in 512 KiB chunks.
- **Per-stream chunk = BulkWindowChunks × 512 KiB / BulkStreams.**
- Driver defaults are authoritative. A user may opt one mount into
  `[mounts.read_profile]` with `streams` / `window_chunks`; do not change the
  driver default to match one user's network unless the result holds across
  environments. The override applies to `bulk` only.

For FUSE adaptive sequential reads the window normally follows the caller's
read size (`readWindowChunks`). `--window-chunks` overrides it for the
measurement, which is what lets you sweep per-stream size on the mounted path.

Useful matrix:

| Config | Streams | Window | Per stream |
| --- | --- | --- | --- |
| single | 1 | 8 chunks (4 MiB) | 4 MiB |
| parallel-1m | 4 | 8 chunks (4 MiB) | 1 MiB |
| parallel-2m | 4 | 16 chunks (8 MiB) | 2 MiB |
| parallel-4m | 4 | 32 chunks (16 MiB) | 4 MiB |

## Raw driver headroom check

Before tuning VFS windows, establish what the driver itself can do. This reads
a generated fixture back at several concurrency levels, bypassing the VFS and
encryption layers, then removes the fixture (the mount must have
`test_enabled = true`):

```sh
qrypt probe bench read --mount <mount> --size 64m --concurrency 1,2,4,8 --samples 1
```

The report gives `aggregate_bps` and `per_stream_bps` per level. Interpretation:

- aggregate flat from 1→8 streams → provider/account ceiling; no VFS tuning can
  exceed it, record the ceiling and stop;
- aggregate rises then falls → per-request overhead or throttling; mirror the
  knee in `ReadProfile`;
- single-stream ≈ mounted throughput → VFS/encryption overhead is small and the
  provider is the limit.

This is the same check as "measure the raw CDN with a throwaway Go test", but
repeatable and without touching the repository.

## Workflow

### 1. Record the baseline

```sh
./scripts/dev/debug-mount.sh <mount> read --size 64m --samples 3 \
  --cache-mode cold --pattern sequential
```

Or against an existing file (read-only, no upload, no cleanup):

```sh
QRYPT_HOME=/tmp/qrypt-bench/home <binary> mount <mount> \
  --config ./qrypt.toml --mount-point /tmp/qrypt-bench/mnt

QRYPT_HOME=/tmp/qrypt-bench/home <binary> probe test read \
  --config ./qrypt.toml --mount <mount> \
  --mount-point /tmp/qrypt-bench/mnt --path "/path/on/mount.bin" \
  --samples 3 --cache-mode cold --pattern sequential --block-size 1m
```

Save every run's JSON; do not trust a single run.

### 2. Run the matrix on one mount

`probe test read` applies measurement-only overrides to the mounted VFS for the
duration of one call, so one mount is enough:

| Flag | Meaning |
| --- | --- |
| `--streams N` | pin concurrent sub-ranges (0 = driver default) |
| `--window-chunks N` | pin window size in 512 KiB chunks (0 = default) |

The overrides are cleared when the probe returns. They are not config and
never reach production read paths.

```sh
QRYPT_HOME=/tmp/qrypt-bench/home <binary> mount <mount> \
  --config ./qrypt.toml --mount-point /tmp/qrypt-bench/mnt

# streams window (in 512 KiB chunks); "1 0" is the single-stream control.
for cycle in 1 2; do
  for cfg in "1 0" "4 8" "4 16" "4 32"; do
    set -- $cfg
    QRYPT_HOME=/tmp/qrypt-bench/home <binary> probe test read \
      --config ./qrypt.toml --mount <mount> \
      --mount-point /tmp/qrypt-bench/mnt --path "<file>" \
      --streams "$1" --window-chunks "$2" \
      --samples 3 --cache-mode cold --pattern sequential --block-size 1m \
      > "/tmp/qrypt-bench/${1}-${2}-${cycle}.json" 2>&1
  done
done
```

`--cache-mode cold` clears the VFS read cache before each sample; the first
sample is usually slower (CDN cold path), so keep 3 samples and compare
medians, not maxima. Interleaving matters: a later single-cycle run can reverse
the ranking, which is drift, not a signal.

If the binary predates these flags, fall back to one binary per configuration:
temporarily add `ReadProfile{BulkStreams: N}` or change the adaptive window
assignment, build, restore the source, and confirm `git diff --quiet` before
continuing.

### 4. Extract metrics

```sh
jq -r '.[0].read.measurements[] | "sample=\(.sample) bps=\(.end_to_end_bps) ttfb=\(.ttfb_us)"' run.json
jq -r '[.[0].read.measurements[].end_to_end_bps] | sort | .[length/2]' run.json
```

Confirm the plan actually used the intended streams:

```sh
jq -r '.[0].metrics[] | select(.extra.read_streams? != null) |
  "streams=\(.extra.read_streams) phase=\(.phase)"' run.json | sort | uniq -c
```

The read-event ring is bounded; when `metrics_truncated` is true, prefer
`cache_hot_hit`/`read` events (they carry `read_streams`) over window events.

Treat `ErrUnsupported` in driver status/errors as a fallback signal: the CDN
answered a strict sub-range with 200. Fix strict-range handling before judging
performance.

### 5. Decide

- **Single wins in both cycles** → set `ReadProfile{BulkStreams: 1}` (or drop
  `parallel_range`). Keep the capability only if direct callers benefit.
- **Parallel wins in both cycles by >20% on every sample** → enable it with
  that measured stream and window. Require `strict range` (200 → `ErrUnsupported`)
  and no fallback events.
- **Mixed or within noise** → keep single. Provider variance is larger than the
  effect; do not enable on a coin flip.
- **Chunk-size axis**: if 1 MiB/stream loses but 2 MiB/stream recovers most of
  the gap, the bottleneck is fixed per-request latency. If 2 → 4 MiB does not
  help further, larger windows only add tail latency.

Reference outcomes from qrypt's own measurements (2026-11):

- Quark: 3-stream VFS split was 25–30% slower than single in interleaved runs;
  reverted.
- Koofr: in two interleaved cycles, 4×1 MiB lost to single and 4×2 MiB closed
  part of the gap but still lost. A later single-pass run ranked 4 streams
  ahead — provider drift. This is why the decision needs interleaved cycles, not
  one comparison.
- 2026-11-08, 64 MiB, fast home network, two interleaved cycles per mount:
  Koofr single 4.34–5.29 MB/s vs 4×2 MiB 2.30–2.37 MB/s; Drime 8.07–10.68 vs
  5.62–6.35 MB/s. 16 MiB was too noisy. Keep Koofr/Drime single-stream by
  default; offer `[mounts.read_profile]` for constrained links.

### 6. Clean up

- Delete any benchmark fixture you created (`rm -rf` the mount path, wait for
  the delayed delete, verify from a fresh mount).
- Unmount everything; `mount | grep qrypt` must be empty.
- Remove temp binaries and `QRYPT_HOME` directories.
- Restore `test_enabled` in `qrypt.toml`.
- `git status` must show only intended changes (no profile hacks).

### 7. Record the result

Add a `benchmark-baseline.md` to an OpenSpec change (or the relevant planning
artifact) with: date, commit, mount/encryption, file size and hash, exact
command, environment, per-sample table, streams observed, fallback count, and
the decision. Future tuning starts from that record instead of guessing.

## Pitfalls

- **Fixture upload cost dominates** on slow providers. Use `--path` and create
  one fixture manually when possible.
- **`--path` skips content-pattern verification.** Sequential samples are
  cross-checked by SHA-256; seek probes skip the fixture byte pattern. Keep
  `--cache-mode cold` and check the hash fields if you need integrity.
- **Do not compare across hours.** Interleave, then compare medians within the
  same window.
- **`--mount-point` is mandatory** for `probe test read` and `--path`.
- **`test_enabled=false` blocks probe tests**, not mounting. Flip it locally,
  restore it, and never commit the change.
