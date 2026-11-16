# Mounted playback measurements

Use this reference for slow seeks, unstable continuous reads, sliding prefetch,
or a control/candidate code comparison. Start with the parent skill's policy and
authorization boundaries. Verify current declarations and CLI `--help` before
reusing commands from a different release.

## Select the path and the clock

| Workload | What it establishes | What it bypasses or leaves unknown |
| --- | --- | --- |
| Existing file via mounted `probe test read --path` | FUSE, hints, planning, encryption, coalescing and delivery | Player decoding/rendering; literal CDN TTFB |
| Custom read-only mounted FD runner | Exact offsets, FD lifetime, hashes and interval rates | Same player/network limits; runner must verify VFS traversal and OS cache behavior |
| Mount-owned `ReadSource.Fetch` with wrapped plaintext entry | Shared executor, wrapped driver and encrypted plaintext correctness without VFS data cache | FUSE, stage classification, VFS prefetch and player behavior |
| Raw `probe bench read` | Raw-driver concurrency/throughput headroom on its generated fixture | VFS, encryption and source-policy execution; not the same existing object |

Do not compare a raw-driver timing with mounted seek completion as if it were a
version regression. A 1 MiB read completion includes receiving that whole range.
Even mounted `ttfb_us` is the first caller read completion, not the first network
byte; fix caller block size when comparing it. Player first-frame latency needs
player-side timestamps.

## Freeze the experiment

1. Record Git root, HEAD, `git status --short`, untracked paths, toolchain,
   driver recommendation and effective policy. A staged snapshot, working tree,
   tag and another checkout can contain different code despite the same HEAD.
2. Create a short private temporary root (`umask 077`, mode 0700). Short paths
   avoid macOS Unix-socket length limits. Copy config locally; select only the
   test mount and isolate its mount point, `QRYPT_HOME` and `storage.work_dir`.
   For an existing-file request, use read-only mounting. Keep credentials out
   of Git and printed reports. Only enable test/debug controls in this copy.
3. Build control and candidate separately. For a code comparison, use separate
   snapshots/checkouts or a recorded Go overlay. Map newly added production
   files appropriately (an empty package file can exclude a candidate-only
   implementation from the control). Include all changed dependencies needed
   for a coherent control. Preserve source and binary hashes and overlay maps.
4. Keep file, offset list, read length, block size, policy, timeouts, cache mode,
   warmup and overlap rules fixed. Change one factor. Interleave A/B, B/A, A/B
   in the same environment; one invocation/sample per arm makes the ordering
   explicit. Three samples inside one A run are not three interleaved rounds.
5. Freeze each measured binary for its entire run. Do not rebuild it between
   points. Keep full CI and other heavy local work outside cloud timing.

The `debug-mount.sh` helper builds the working tree and deletes temporary logs
on exit. It suits an individual CLI smoke, not a retained multi-version trace
experiment. It does not rewrite the copied config's storage directory.

## Define cache and session semantics

`read_cache.max_size = "0"` disables durable data storage, not hot chunks,
in-flight coalescing, the OS page cache, nonce, metadata, URL or CDN caches.
Disable durable storage both globally and for the tested mount in the config
copy when this is part of the experiment. Record what actually remained warm.

Before a cold isolated point, wait for foreground and speculative reads to
drain, clear VFS state using the test runtime's cache control, then open a new
read-only FD and apply the platform's supported cold-read mechanism. Inspect
`os_cache_control` and `traversed_vfs`; an unsupported OS hint cannot establish
an OS-cold result. On macOS the historical runner used `F_NOCACHE`; use the
platform's named API/constant rather than assuming its numeric value elsewhere.

Clearing VFS cache also resets sequence hints and retires session prefetch.
A fresh FD starts at Initial; the small FUSE requests within one large caller
read may then become Sequential. This is a **cold mounted-load** experiment,
not proof of seeking within a long-lived player session. Intra-point hot chunks
can still be reused. If the request requires no VFS data-cache path at all, use
the wrapped `ReadSource.Fetch` path and report it separately. `ReadRaw` on an
encrypted mount would measure ciphertext.

Test open-session behavior separately: keep one FD, establish forward reading,
then seek without clearing history. Use `isolated`, `prefetch` and `concurrent`
CLI scenarios as appropriate and confirm the recorded overlap actually occurred.
The prefetch/concurrent scenarios include their own warmup; inspect the current
probe implementation rather than assuming all scenarios share FD lifetimes.
Hold seek size, warmup and overlap timeout fixed.

For a complete same-FD pass, use the repository's
[read-only session runner](../../../../scripts/dev/measure-playback-session.py)
after starting an isolated read-only mount and clearing its VFS cache once:

```sh
python3 scripts/dev/measure-playback-session.py \
  --path /tmp/qrypt-bench/mnt/existing-video.mp4 \
  --output /tmp/qrypt-bench/session-a-1.json
```

Defaults are 256 MiB continuous reading, 20 alternating near/far seeks with
8 MiB read after each, a 3-second pause midway, a 35-second idle pause and a
16 MiB resume, all through one FD. The file must fit the configured passes.
Every 1 MiB read has a hash and timing; the first read after each discontinuity
is the seek load sample. Read durations exclude hashing/report writes; pass wall
time includes them. The runner has a 600-second deadline, preserves partial or
failed results and refuses to overwrite an existing report. It closes its FD,
but its caller still owns mount/process cleanup and post-close task checks.
OS-cold mode currently requires Darwin F_NOCACHE; `--allow-os-cache` is for
explicitly warm/local runner checks. F_NOCACHE does not eliminate kernel
readahead. Verify VFS traversal, session identity and any post-pause kernel reads
against the mount logs. Resume may consume buffered data and is not a remote
bandwidth sample. Keep raw output local, outside Git.

Binary-sized fixtures can make every generated seek chunk-aligned. Use a fixed
`--seek-offset-bias 137` in both arms when testing unaligned tail-page handling;
the runner rejects points beyond EOF. Record the bias with the offsets. A
uniform fixture (for example all zero bytes) cannot detect wrong-offset reads
through hashes alone; use deterministic nonuniform local fault tests as well.

## Reproduce useful reads

For the CLI, run seek and sequential workloads separately on the existing file.
For each arm/round, use `--samples 1` with the matching isolated runtime:

```sh
QRYPT_HOME=/tmp/qrypt-bench/home qrypt probe test read \
  --config ./qrypt-bench.toml --mount cloud \
  --mount-point /tmp/qrypt-bench/mnt --path "/existing/video.mp4" \
  --pattern seek --seek-count 20 --seek-size 1m \
  --seek-scenario isolated --cache-mode both --samples 1 > seek-a-1.json

QRYPT_HOME=/tmp/qrypt-bench/home qrypt probe test read \
  --config ./qrypt-bench.toml --mount cloud \
  --mount-point /tmp/qrypt-bench/mnt --path "/existing/video.mp4" \
  --pattern sequential --offset 0 --length 256m --block-size 1m \
  --cache-mode cold --samples 1 > sequential-a-1.json
```

Choose a file large enough for the requested length. `--size` describes fixture
size (or must match the existing file); use `--length` to bound an existing-file
sequential pass. A 32/64 MiB pass can expose startup and regressions, but a
256 MiB or longer bounded pass better spans repeated lookahead replenishment.
Neither proves hours of player behavior or physical maximum bandwidth.

When exact seek hashes or per-span rates are needed, use a custom mounted runner:

- Fix offsets once, e.g. `i * (file_size - read_size) // 20` for `i=1..20` with
  `file_size > read_size`, and reuse them across arms. Persist each result as it
  completes, including offset, requested/actual bytes, duration and SHA-256.
- Use the monotonic clock around the read; state whether open is included.
  Close the FD in `finally`. Treat short reads as errors, not fast samples.
  Compare hashes only for identical offset and length; without a trusted hash
  this proves agreement, not independent correctness of the whole source.
- For continuous reading, keep one FD, read contiguous equal blocks, hash all
  bytes, and time fixed spans (for example each 8 MiB of a 256 MiB pass).
  Report useful bytes / wall time, median and slow spans as well as startup,
  steady and peak rates. A fast span may consume buffered data.
- Use a bounded read-call/runner timeout with owned-process cleanup. Preserve
  completed samples and label a canceled partial pass; do not assign it a fake
  completed latency or silently replace it with a better run.

## Correlate a stalled target with execution

Retain debug `[READ]` logs in the private test runtime. The in-memory diagnostic
ring can evict initial events under tiny FUSE reads. Correlate operation, file,
session, target chunk, window range and time:

`read_access` → `wait_window_begin` → `wait_window_done` or
`wait_window_bypass` → `fetch_window`. Registration joins use
`wait_window_load_*`. A later `prefetch_window` completes the network window.

Check these explanations in order:

1. Was the request Initial, Seek or Sequential, and was prefetch/parallelism
   eligible? A `Bulk.Streams` override is not a playback-prefetch override.
2. Did queueing dominate, or did opening/body progress dominate? Inspect global
   and object budgets before assuming the CDN was slow.
3. Did the wrapped body already contain the target's full block? Compare
   `first_body_us`, `first_chunk_us`, `window_body_bytes` and `wait_us` with the
   target position. Receiving the first block does not make a distant target
   ready. Ordinary Fetch validates a whole window; progressive speculative
   loads publish complete authenticated blocks, with final EOF/Close checks.
4. Did the delay align with `prefetchWaitBudget` (3 s of target inactivity)?
   The old fixed elapsed-time rule discarded progressing slow blocks. Current
   lookup/registration joins renew only for new plaintext inside the target
   block of a known-size single-stream progressive load. Unknown-size buffered
   fallback keeps the fixed wait budget. Prefix/tail traffic and replay
   do not renew it; shared total and caller deadlines still apply. A repeated
   local takeover interval is not a fixed CDN first-byte delay. Correlate the
   target offset, partial body bytes and takeover counts before changing timeouts.
   Use a paced nonuniform body exceeding 3 s per block to verify both joins,
   plus no-progress, distant target, replay, caller and total-deadline controls.
5. Did takeover refetch received bytes, or did retries request only the missing
   suffix? Count canceled work as traffic; keep authentication, response-length
   validation, total deadlines and bounded retry limits intact.

Executor `body_bytes` counts wrapped plaintext, including cancellations/retries;
it omits nonce/private HTTP/headers/TLS. Sum complete `fetch_window` and
`prefetch_window` events for a clearly delimited phase. Exclude metadata warmup,
include tails canceled at FD close, and do not confuse earlier seek traffic
with a later offset-zero sequential pass. If history/logs are incomplete,
report amplification as unknown. Whitelist numerical fields and safe enums
when exporting logs; do not publish entire raw lines or configs.

Reconcile the phase instead of reporting one ratio. Run
[analyze-read-traffic.py](../../../../scripts/dev/analyze-read-traffic.py) with the
mount trace and the runner JSON; it reports executor body bytes, caller delivery
(total and unique), the delivered bytes inside each window, shared bytes another
session reused, real unused tails of completed windows, duplicate attempts and an
unknown residual. A canceled or superseded window is traffic, not waste: only the
prefix it actually received is credited, delivered bytes outside that prefix are
not, and the remainder cannot be split from a dead tail without per-chunk
receipt, so it stays `unknown_residual_bytes`. The prefix is `progress_end`, the
furthest received offset; `body_bytes` is cumulative and counts a replay again, so
the gap between them is reported as `replay_bytes`. A window with no evidenced
prefix (parallel streams, or a build that does not emit `progress_end`) keeps its
whole body in the unknown residual instead of guessing a prefix. The traffic
count itself is always published (zero when the body never started), so a row
without it is a truncated or pre-publication log: the report becomes incomplete
rather than falling back to the row's caller byte count, which would hide
retries and unpublished bytes behind a 1.0x ratio. Each pass is divided by its own
delivery, so one long pass cannot flatter the others; bytes spilling over from a
neighbouring pass are reported as `cross_pass_delivered_bytes`. Each window is
charged to the pass that requested it, not to the pass running when the row was
logged, so a prefetch canceled by a seek stays with the pass that caused it and
its late tail shows up as `cross_pass_tail_bytes`; a window requested before any
pass begins stays unmatched. A report is
marked incomplete (`complete: false`, `completeness_problems`) when the runner
ended early, when the windows cover less than 99% of delivered bytes, when a
driver range has an unmatched begin/end row, or when a driver range sits outside
every window row, and an incomplete report carries no amplification figure.
Covered intervals are not completeness: a dropped window row stays invisible
while a neighbouring window covers the same bytes. Source begin/end pairs also
need association with the window that owns those calls; their presence alone
does not prove that every window's traffic was recorded. Per-session attribution
requires every caller delivery to match a traced read carrying a session identity; interval
coverage of 100% is not evidence, because two sessions reading the same range
cover it while only one of them was logged. When that evidence is missing,
`per_session_gap` says so and the report falls back to global delivery. Never publish the raw report.

For a playback session, the session budget is the thing under test, not a
timeout: `prefetch_credit` events must show `seed` first and `grow` only after
delivery, `budget_bytes` must stay at or below `stage_ceiling_bytes`, and a seek,
idle retirement or lowered ceiling must reset it. Credit is a byte count, not a
request count: the same range read in one call or in several smaller calls must
reach the same `budget_bytes`. A `fixed` stage policy, an
unknown-size object, a hintless caller or `ReadStream` never ramps, so their
amplification and request shape should be compared on the original rules.

`actual_in_flight` is per trace/window. Four simultaneous one-stream lookaheads
can each report one; that is not source-wide concurrency one. Summed worker
durations overlap, and a peak/span rate can include cached data. Neither proves
aggregate socket concurrency or an account bandwidth ceiling.

Use `source_range_begin` / `source_range_end` and `(source_id, range_id)` for
shared-call timelines. `source_in_flight` includes admitted calls while opening
and consuming a body; provider-private requests and unstarted reservations are
excluded. Calls without a trace sink contribute to the count but emit no pair.
`prefetch_plan` reports consumed `cursor_chunk`, exclusive `horizon_end_chunk`,
contiguous in-memory `ready_end_chunk`, `pending_windows`, `fill_stop`, and
source/object admitted counts, plus the session's dynamic plan beside the stage
ceilings: `prefetch_mode`, `budget_bytes`, `credit_bytes`, `delivered_end`,
`window_cap_bytes`, `depth_cap`, `stage_ceiling_bytes`. `prefetch_credit` records
each budget transition with a constant `budget_change` reason. These are
independently sampled, not an atomic snapshot. Completed-but-evicted markers are
not ready bytes. Inspect whether a slow target coincides with full active depth
and body progress, or with no requests and delayed refill. A full paused horizon
intentionally stops fetching.

Separate startup from continued reads while preserving both. An encrypted
first open can wait for a private 32-byte nonce/header read before the plaintext
Range opens; driver `open_us` includes that work. Repeated response-header
timeouts can dominate the initial pass without any scheduler wait. Correlate
safe driver range sizes, retry counts and timeout classes; do not export URLs
or infer that a larger read window cures header setup.

Check driver-private body lifetimes too. The SFTP failure baseline applied its
15-second operation context to the whole body, despite a longer shared fetch
budget. A progressing large Range failed at 15 seconds; raising the shared
budget could not remove that inner deadline. The corrected driver limits only
connection checks, open and seek, and returns a body governed by the caller's
context. Verify default large Ranges actually complete beyond 15 seconds,
caller cancellation still closes the body, and another Range remains usable.
Shared idle/total protection must remain enabled; do not merely raise the private
deadline or use small parts to hide the failure.

Preserve the default failure, then isolate smaller fixed parts at the same
window size to test transport headroom. Confirm actual task/worker counts. Flat
one/four-stream rates do not prove a parallel benefit or identify an account
ceiling by themselves; do not change interactive prefetch from a bulk-only
experiment. A bounded mounted smoke after a lifecycle fix verifies that path,
but does not replace the full interleaved performance acceptance workload.

## Validate measurement evidence

When changing the analyzer, its trace producers, or relying on a new build's
attribution for acceptance, run the analyzer's self-check and exercise the actual
CLI on adversarial fixtures. Verify observable results, including exit status
and written JSON, rather than only calling internal reconciliation functions:

- Missing `body_bytes` stays unknown; an explicitly observed zero stays zero.
- Retry/replay traffic can exceed unique receipt; partial canceled bodies and
  parallel holes must not create a fictitious contiguous received prefix.
- Dropping a window row remains detectable even if another window covers the
  same offsets. Correlate source/window ownership; begin/end pairing and spatial
  coverage alone cannot establish that all window traffic was recorded. If the
  trace cannot establish ownership, leave completeness unverified.
- Two sessions reading identical ranges need separate delivery evidence; a
  missing read must not become a zero-delivery session or claimed reuse.
- A tail canceled after the next seek remains charged to its originating pass;
  pass ratios use their own delivery denominator.
- Partial runs, missing records and absent delivery produce unknown affected
  ratios. The CLI handles null ratios without a formatting exception and uses
  a documented exit status if it rejects incomplete evidence.

A passing self-check establishes only the cases it exercises. Keep these
checks separate from actual backend performance acceptance.

## Verify the sliding window, not just the knobs

For known-size mounted reads with valid session/request IDs, inspect
[the controller](../../../../pkg/vfs/read/prefetch_session.go),
[the plan resolver](../../../../pkg/vfs/read/plan.go) and actual ranges. The plan
keeps the stage's `Wcap`/`Dcap` ceilings; the session executes a forward budget
`H` that starts at `min(Hcap, Wcap, 1 MiB)` and doubles only after a full budget
of new delivered bytes. The horizon is consumption cursor + `H` in chunks,
clamped to EOF, and each fill admits `min(Wcap, H)`-sized windows up to
`min(Dcap, ceil(H/W))` of them. Ready and in-flight data occupy it; completion
may fill holes but cannot advance the cursor. A fixed stage policy executes its
window and depth literally, with no credit.

Verify completion refill without another foreground read, consumption refill,
nearest-hole ordering, and notification after capacity is released by direct or
other-session work. Distant tails are batched against the current budget; cursor
holes and EOF do not wait for a batch. A full paused horizon should stop
downloading and avoid busy waits. Failures, hot-cache eviction and late
completions must not cause endless refill, and none of them may grant credit.
Check that a seek, idle retirement or lowered ceiling resets or clamps `H` before
blaming a backend for a large short-load tail.

Many `read_access` discontinuities and `prefetch_cancel_reason=seek` events
during nominally contiguous user reads can indicate classification failure.
On macOS, unaligned 1 MiB preads can repeat the terminal 16 KiB page at every
boundary. Correlate offset/end coverage, not just the number of syscall seeks.
Known sessions tolerate a terminal overlap smaller than one cache chunk when
the end does not move backward; duplicate pages do not advance the horizon.
Verify real backward/gap seeks, stale/concurrent gates and unidentified callers
separately before changing this rule. Increasing lookahead cannot cure repeated
cancellation of the same session's windows.

Check shared budgets at one as well as larger values, fixed depth one,
multi-session dependencies, seek/close/concurrent/disabled gates, invalidation,
idle cleanup and completed-buffer release. Cancellation cannot acquire planner
and scope/registry locks in reverse order. Callers without session identity,
unknown lengths and ReadStream's own scope keep their batch behavior; do not
assume all reads enter the controller or break their merged windows incidentally.

If every small cursor advance creates a short tail Range, compare request-size
histograms before increasing depth. In the recorded Quark experiment, an
unbatched candidate reached 48 requests versus 40; batching restored 40, but
the final three rounds still did not prove a throughput gain. See the linked
summaries in the parent skill; preserve the failed candidate's raw evidence locally.

Conclude separately on bytes/correctness, scheduling behavior, performance and
acceptance. Preserve per-round results and limits even when pooled medians look
acceptable. Use the parent skill's cleanup and recording requirements.
