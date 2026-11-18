# Read policy execution

Mount assembly creates one `drive.ReadSource`. Pass it to every production
consumer: VFS, sequential streams, copy, share-fetch fallback, and edit staging.
The owner closes the source during teardown. Never create a new source for each
request to the same mount, because admission is scoped to the source instance.

Drivers optionally implement `ReadPolicyProvider` and `ReadLimitProvider`.
Build complete recommendations from `DefaultReadPolicy`; zero is not inheritance.
Only `ReadOverride` is sparse. `ResolveReadPolicy` applies overrides and hard
limits once. `ReadSource.Policy` returns a value copy and a copied origins map.
`ReaderDeps` takes the source, without accepting a second policy input.

Sizes outside the VFS cache adapter are bytes. Each logical window is bounded by
32 MiB. Fixed parts are strict ceilings, and worker count limits concurrency
independently of task count. Even division applies to the current window. The
source uses a bounded output buffer and a fixed number of workers; it does not
preallocate tasks for the complete file.

`ReadSource.Fetch` shares strict-range checks, timeout handling, truncated-body
validation, status backoff and replay with `ReadSource.Read`. Replay happens
before a successful window is delivered. Interactive windows stay single
stream; lookahead size is fixed by the resolved W×D horizon. FUSE sequential
eligibility never rewrites the original intent or cache admission. Stream
refill re-evaluates the access stage.

Sequential playback reads are small FUSE reads, so a caller window pins one
single connection and only reaches the provider's per-stream rate. Quark
therefore recommends a fixed 2 MiB sequential window (initial and seek keep the
caller window): interleaved 32 MiB playback-shaped reads over the same CDN
sustained about 4.5–5.7 MB/s with that window versus 2.2–3.4 MB/s with the
caller window, while a 4 MiB window was slower. Lookahead follows the resolved depth (the
driver's recommendation capped by the shared prefetch capacity), i.e. four
2 MiB lookahead windows.

The scheduler atomically acquires global and object capacity. Stable provider
IDs identify objects inside one source; unknown IDs fail. Speculation uses
`TryFetch`, which reserves complete admission without opening a connection.
`PendingRead.Context` hands that reservation to the read layer and `Close`
releases it; there is no separate pending-read executor. `ReadSource.FetchStream`
publishes each complete 512 KiB plaintext block as immutable and independently
ready while the same Range request continues through the tail; VFS lookahead and
foreground window fetches both use it. A speculative window runs through that
same `FetchStream` call under the admission `TryFetch` reserved, so executing it
does not take a second permit. A foreground read therefore returns its target block
as soon as that block arrives, while the rest of the window keeps filling in the
background for later reads; merging or joining the window later is unaffected.
The final block waits for EOF, response length and successful Close. Encryption
authenticates its own blocks before returning plaintext. An incomplete block is
never published; already published prefix blocks remain valid if a later block
fails. A split foreground window is partitioned by the read layer itself: it
chooses the worker count and part size, hands each part to `ReadSource.FetchPart`
as a single admitted Range, and reassembles the parts' blocks in order, so part
scheduling and cancellation stay in one place. Bulk downloads and `ReadStream`
windows go through the same scheduler: the read layer translates the driver's
bulk split policy into an explicit worker count and part size
(`drive.WindowPartLayout`), so `RangePlan` carries only offset, size, workers and
part and the source never interprets policy. `ReadStream` has no separate read
engine: each refill calls the ordinary `Read` for one plan window, marked silent
so the stream still reports a single read record, and keeps one lifetime scope
so the lookahead scheduled by a refill survives until the handle is closed. Idle/truncation retries resume
at the first unpublished block, under the original total deadline and retry
limits, without replaying delivered bytes. Unknown-size objects retain buffered
whole-window validation. Closing a running job cancels it; admission is released
after its fetch has stopped.

Range attempts have a single owner: the drive layer replays idle stalls,
truncated bodies and retryable status codes, resuming at the first unpublished
block under one fetch deadline, so a slow provider never forces the read layer
to re-fetch bytes it already holds. The read layer does not retry a finished
window; it re-enters the window registry at most twice — once when the window it
joined finished without its block (it then owns a window shaped for that read)
and once to adopt an abandoned prefetch. Anything beyond that surfaces the
original error to the caller.

Crypt is charged at the outer source and invokes its raw driver without a second source admission.

Local bandwidth wait is excluded from remote idle detection and included in the
fetch total bound. Drivers can inspect explicit cancellation causes. Idle stalls
and truncation justify connection penalties; caller cancellation and total
expiry alone do not. Fetch traces contain bounded counters and durations, with
no credentials or signed URLs. Local wait overlaps body timing; driver open is
not CDN TTFB or player first-frame time.

Driver setup deadlines must not accidentally become body lifetimes. SFTP bounds
connection checks, open and seek together to 15 seconds, then reads under the
caller's context. Shared reads retain their idle and total budgets; raw
`Driver.Read` callers own cancellation, deadlines and closing the returned body.
Closing a body stops its cancellation watcher without canceling its caller.

Throttled single connections: the adaptive foreground split is on by
default. A foreground window whose provider transfer rate (measured over the
body, so a slow request open does not arm it) falls below 350 KiB/s makes the
next window four Range requests over parallel streams, split evenly by the read
layer (a 512 KiB window becomes four 128 KiB parts, a 2 MiB window four 512 KiB
parts), bounded by the same admission and pausing speculative lookahead; one
window at 900 KiB/s or more reverts to a single stream. For five minutes after such a sample, a file
with no measurements of its own starts split as well, so opening the next file
during the same throttled session does not pay the cold-start window again.
Measured on the quark CDN with each connection paced at 150 KiB/s, a
playback-shaped read sustained about 0.11 MB/s single-stream versus about
0.33 MB/s split, a 2 MiB cold read dropped from 18.8 s to 9.2 s, and the first
byte of a newly opened file dropped from 4.2 s to 1.5 s. On a healthy link the
split does not arm, so the default stays neutral. `QRYPT_FG_SPLIT=0` disables
it for one run when triaging.

With debug logging enabled, `[READ]` lines retain initial/seek completion,
window fetches, prefetches and foreground wait transitions even after the
diagnostic history wraps. Match the operation ID, window range and session to
follow `read_access` → `wait_window_begin` → `wait_window_done`; a joined
window that finishes without the target block is a miss and the read continues
with its own `fetch_window`. Coalescing during window registration uses the
corresponding `wait_window_load_*` phases.

`source_range_begin` / `source_range_end` identify each observed driver call by
`source_id` and `range_id`. `source_in_flight` counts active calls across the source,
including opening, until body EOF/Close or an open failure. It excludes reserved,
unstarted jobs and provider-private requests; it does not measure socket count.
Match complete begin/end pairs for concurrency timelines. In contrast,
`actual_in_flight` in a window trace is local to that trace. Pending reads retain
their scheduler admission through executor completion, so admitted and active
counts can briefly differ. The executor exposes an optional `ReadTrace` sink;
VFS installs the diagnostic adapter. A caller without a sink still contributes
to the source count but emits no pair, so missing pairs leave its timeline
incomplete. The trace accumulates typed counters (queue/open/body/wait/close
timings, byte and progress frontiers, executed window shape, retry and failure
facts); the diagnostic key map is rendered only in `Snapshot`, so execution
code never keys a map to report what it did.

Debug-only `prefetch_plan` snapshots report the consumed `cursor_chunk`, the
exclusive `horizon_end_chunk` and the contiguous memory-resident
`ready_end_chunk`. They also record the fixed plan numbers
`prefetch_window_chunks` (`W`) and `prefetch_depth` (`D`), and `fill_stop`,
which distinguishes a full horizon, the depth
gate, an EOF tail, a failure, a cancellation and unsuccessful admission;
`not_admitted` can also mean another request filled the hole during selection.
`source_admitted`, `object_admitted`, `prefetch_in_use` and `pending_windows`
expose shared capacity and session work. These independently sampled counters
are not an atomic snapshot. Correlate them with source range pairs and
foreground waits before attributing a gap to queueing or a policy limit.

Wait events include the speculative window's age and stream count plus a
live trace snapshot under `window_*`. `body_bytes` counts plaintext returned by
the wrapped driver, including canceled and retried attempts; it is not wire
traffic. `first_body_us` measures from driver open start to the first returned
bytes. `first_chunk_us` measures when one 512 KiB chunk has been returned within
an individual range attempt. Neither means the window has been validated or
delivered: lookahead publishes complete blocks, while ordinary `Fetch` publishes
completed windows. A `wait_window_done` event can therefore precede the matching
`prefetch_window` completion. Compare these progress fields with `wait_us`,
`open_us`, `queue_us` and `local_wait_us` before attributing a slow seek to admission, the provider or a foreground wait timer. A measured
1 MiB read completion is not a first-byte measurement.

The old `ReadProfile`, resolver helpers, `ReadStageSuggestion`,
`ParallelReadLimiter`, and `ReadParallel` entry points have been removed. User
configuration migration is documented in [读取配置迁移](../for-user/read-policy-migration.md).

Foreground waiters subscribe to their target block, not to the end of a
speculative window: a joined window that publishes the block satisfies them,
and one that finishes without it is a miss, so the reader loads its own small
range. There is no separate foreground give-up budget and no cancel-and-reGET of
in-flight work; the fetch's own idle and total deadlines remain the only bounds,
together with caller cancellation. A waiter only joins another session's
lookahead window for its head block or an already published block: a stalled
lookahead from an unrelated handle must not delay this caller. Unknown-size loads keep whole-window
validation.

`ReadPlan` records the two shapes a read can take. Streaming plans resolve the
confirmed sequential window from `Interactive.WindowBytes` (initial and seek
reads keep the caller window), and the lookahead as `W = PrefetchWindowBytes`
(or the read window when unset) times `D = max(1, min(PrefetchDepth,
AdaptiveDepth))`; bulk plans execute their window, worker, part and threshold
numbers literally. The resolved numbers are the whole rule: there is no mode to
decode and no forward byte budget or credit controller between them and the
scheduler.

Lookahead is scheduled by reads, not by a per-handle worker: after a foreground
read, `pkg/vfs/read/prefetch.go` refreshes a fixed `W × D` horizon ahead of the
consumed cursor, admitting contiguous windows nearest the cursor first. Blocks
already ready, cached or in flight are skipped, and scheduling stops at the
depth gate or EOF. The in-flight window count, bounded by `D` and the global
prefetch capacity, is the only state; there is no cursor bookkeeping, idle timer
or completion ledger, so a paused file stops growing its horizon and a later
read simply tops it up. Only foreground consumption advances the horizon.

Read state travels with one context value: `pkg/vfs/read/request.go` stores a
single `readRequest` with the resolved plan, the granted stream count, the
prefetch marker, the caller intent, the access hint, the cache run and the
per-call timing. The `With*` helpers rewrite that one value copy-on-write, so a
stage that re-resolves the plan cannot leave a stale plan, stream count or
prefetch marker behind. Cross-package callers keep the same constructor names
(`WithReadPlan`, `WithGrantedStreams`, `WithReadIntent`, `WithAccessHint`,
`WithCacheAccess`, `WithPriority`, `WithPrefetchRead`, `WithoutReadPrefetch`).
Fetch timeouts and the stream scope stay on separate keys because drivers and
the scope registry consume them outside the read request.

A seek, a stage change, handle release, invalidation and cache clearing retire
the matching read scope; dependent foreground readers keep cancellation
protection. There is no
second chunk cache and no per-file ledger, and this change does not tune driver
defaults or infer a physical bandwidth ceiling from a configured request cap.

For a mounted same-FD workload, use
[measure-playback-session.py](../../scripts/dev/measure-playback-session.py)
against an isolated read-only mount. It records continuous reads, alternating
seeks, pause/resume, per-block hashes and bounded failure results. Prepare and
clear the test mount before the run; the runner never clears between reads or
owns the mount lifecycle.

Reconcile the result with the mount's read trace using
[analyze-read-traffic.py](../../scripts/dev/analyze-read-traffic.py). It
classifies each window row by the stall its timings show (provider TTFB, slow
body, local wait, retry, no body) and lists the slowest rows, counts chunk
coverage (distinct chunks versus visits, so a lookahead that re-admits a range
another window already fetches shows up as duplicate visits and estimated
redundant bytes), then reports executor `body_bytes`, caller deliveries (total
and unique), the delivered bytes
inside each window, shared bytes another session reused, real unused tails of
completed windows, and an explicit unknown residual for canceled windows whose
received bytes cannot be split between a useful prefix and a dead tail. A
canceled window is traffic, not automatically waste: only the prefix it actually
received is credited, and delivered bytes outside that prefix are not credited
to it. That prefix comes from `progress_end`, the furthest received offset,
never from `body_bytes`: the body count is cumulative and counts a retried
attempt twice, so it cannot say which bytes arrived. A single-stream window
keeps the frontier contiguous; a parallel one does not, and a window without an
evidenced prefix contributes its whole body to the unknown residual. The
difference between cumulative body bytes and the evidenced receipt is reported
as `replay_bytes`. A row without a traffic count is unknown traffic, never the
row's caller byte count: the read trace always publishes `body_bytes` (zero when
the body never started), so an absent count means the log predates that
publication or was cut, and the report stays incomplete instead of reporting a
comfortable 1.0x. Per-pass and per-session ratios divide by that pass's or that
session's own delivery, never by the whole run, and bytes from a neighbouring
pass are reported separately instead of being pooled in. Per-session ratios need
more than interval overlap: two sessions reading the same range produce full
coverage while the trace recorded only one of them, so each caller delivery must
match a traced read with a session identity, or attribution switches off with
`per_session_gap` explaining why. A window is charged to the pass
that requested it, not to the pass that was running when its row was logged: a
prefetch canceled by a seek is written after the next pass has begun, and a
caller that asks for the range last, before any pass begins, leaves the window
unmatched instead of guessed at. A window whose request started in one pass and
finished later is counted in `cross_pass_tail_bytes` for its own pass. The
report is marked incomplete
when the runner reports a partial run, when the trace windows cover less than
99% of delivered bytes, when a driver range has an unmatched begin/end row, or
when a driver range sits outside every window row, and an incomplete report has
no amplification figure. Covered intervals alone cannot prove the window rows
are complete, because a dropped row is invisible while a neighbouring window
still covers the same bytes; the driver-call rows can, because every admitted
call opens and closes. A suppressed trace reports global delivery only. The
[measurement reference](../../.agents/skills/qrypt-read-tuning/references/playback-measurements.md)
describes cache boundaries, interleaving and interpretation. Keep raw JSON and
reports local.
