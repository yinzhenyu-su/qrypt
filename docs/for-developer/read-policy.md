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
before a successful window is delivered. Interactive/probe windows stay single
stream; probe disables prefetch. FUSE sequential eligibility never rewrites the
original intent or cache admission. Stream refill re-evaluates the access stage.

The scheduler atomically acquires global and object capacity. Stable provider
IDs identify objects inside one source; unknown IDs fail. Speculation uses
`TryFetch`, which reserves complete admission without opening a connection.
Call `Run`, `RunChunks` or `Close` on the returned pending read. `Run` retains
whole-window validation. VFS lookahead uses `RunChunks`: each complete 512 KiB
plaintext block becomes immutable and independently ready, while the same Range
request continues through the tail. The final block waits for EOF, response
length and successful Close. Encryption authenticates its own blocks before
returning plaintext. An incomplete block is never published; already published
prefix blocks remain valid if a later block fails. Idle/truncation retries resume
at the first unpublished block, under the original total deadline and retry
limits, without replaying delivered bytes. Unknown-size objects retain buffered
whole-window validation. Closing a running job cancels it; admission is released
after its fetch has stopped. Crypt is charged at the outer source and invokes its raw driver without a second source admission.

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

With debug logging enabled, `[READ]` lines retain initial/seek completion,
window fetches, prefetches and foreground wait transitions even after the
diagnostic history wraps. Match the operation ID, window range and session to
follow `read_access` → `wait_window_begin` → `wait_window_done` or
`wait_window_bypass` → `fetch_window`. Coalescing during window registration uses
the corresponding `wait_window_load_*` phases.

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
incomplete.

Debug-only `prefetch_plan` snapshots report the consumed `cursor_chunk`, exclusive
`horizon_end_chunk`, and contiguous memory-resident `ready_end_chunk`. They also
project the session's dynamic plan beside the stage ceilings: `prefetch_mode`,
`budget_bytes` (`H`), `credit_bytes`, `delivered_end`, `window_cap_bytes`
(`Wcap`), `depth_cap` (`Dcap`) and `stage_ceiling_bytes` (`Hcap`). A separate
`prefetch_credit` event records each budget transition with a constant
`budget_change` reason (`seed`, `grow`, `cap`, `fixed`, `disabled`), so a trace
can be reconciled without per-chunk history. Both are read-only projections:
they do not count completed-but-evicted markers as ready data, do not change the
hot-cache LRU, and never drive scheduling.
Chunk readiness may advance during collection. `fill_stop` distinguishes a full
horizon, depth, tail batching, failure, cancellation and unsuccessful admission;
`not_admitted` can also mean another request filled the hole during selection.
`source_admitted`, `object_admitted`, `prefetch_in_use` and `pending_windows` expose
shared capacity and session work. These independently sampled counters are not
an atomic snapshot. Correlate them with source range pairs and foreground waits
before attributing a gap to queueing or a policy limit.

Wait events include the speculative window's age, stream count, budget and a
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

Foreground waiters subscribe to their target block, rather than to the end of a
speculative window. Multiple sessions may consume the same immutable block.
A small initial/seek read may reuse an already published block or wait for the
head block of a larger progressive window; a target deep in an unfinished
lookahead still loads its own small range. The three-second takeover budget is
an inactivity budget for a known-size progressive target block: new plaintext
bytes inside that block
renew it. Replayed bytes and progress in an unrelated prefix or tail do not.
Both cache lookup and window-registration joins use this rule. Opening with no
body progress and a stalled target still trigger bounded takeover; a progressing
slow block keeps its original request instead of discarding partial bytes. The
original executor total deadline and caller cancellation remain in force.
Unknown-size loads retain whole-window validation and the original fixed
wait budget, since a received prefix cannot be delivered early.
Progress is a single-stream receive frontier, never a substitute for validated
block readiness. Block readiness does not complete the window or release its
admission early.

`ReadPlan` records the stage's prefetch mode beside its numbers, so a later
stage never infers the rule from a value's provenance. For an adaptive stage the
plan carries the stage ceilings: `Wcap` (largest single window) and `Dcap`
(largest number of active windows), with `Hcap = Wcap × Dcap`. `recommended`
versus `effective` remains the policy-resolution layer; the session's forward
budget is a third, per-pass layer resolved on every fill.

Known-size mounted reads carrying current session/request IDs maintain a
`prefetchSession` inside their existing `ReadScope`. An adaptive session does not
execute the ceilings literally. It holds a forward byte budget `H` that starts at
`min(Hcap, Wcap, 1 MiB)` in whole cache chunks and a credit of unique delivered
bytes. Each successful caller delivery advances the pass's delivered end; only a
new positive suffix of the bytes the mount layer actually returned adds credit.
Crediting counts bytes, not admitted requests, so the same range split into
several smaller reads earns exactly the credit of one larger read.
A repeated page, a failed read, a stale request, a background fill, a predicted
request end and another session's delivery never do. When credit reaches `H` and
`H < Hcap`, that `H` is deducted and `H` doubles, at most once per consumed
budget; one large delivery may cross several steps. Spent credit is never reused,
and credit is discarded at the ceiling so a later stage upgrade cannot replay it.

Each fill derives `W = min(Wcap, H)`, `D = min(Dcap, ceil(H/W))` and admits only
inside `cursor + H`, clamped to EOF. Ready chunks, in-flight ranges and the
offset-zero warm-up all occupy that boundary, so skipping a covered prefix cannot
push a request past it. Both consumption and request completion fill holes
nearest the cursor, and a completion does not advance the cursor or authorize
downloading the rest of a paused file. The warm-up uses the seed size and counts
as the session's first active window instead of admitting a full stage window
before the controller knows the budget. A hintless or unknown-size caller never
creates this controller, so it keeps bounded batch scheduling. A non-zero seek
also keeps the existing confirmation gate: it opens no lookahead until the
following read proves a contiguous run.

Known sessions tolerate a terminal overlap smaller than one cache chunk when
the read end does not move backward. This preserves a confirmed forward run
when FUSE repeats an aligned page at an unaligned caller-block boundary. A
duplicate page does not advance the horizon. Backward ends, forward gaps,
whole-chunk overlaps and unidentified callers keep the original seek rules.
Distant tail fills wait until `min(W, half of the current horizon)` chunks fit.
The threshold follows this pass's budget rather than the stage maximum, so a
reduced budget never waits for a batch it cannot fill. Cursor holes and EOF are
filled immediately. Completed range references are cleared when collecting
pending work; ready data remains subject to the existing shared cache limits.
A fixed plan executes its window and depth literally and never earns credit;
disabled, probe, explicit bulk, unknown-size, sessionless and `ReadStream`
callers keep their original paths.

Admission remains nonblocking. `Scheduler.CapacityChanged` broadcasts releases
from foreground, direct and speculative reads; subscribe before attempting a
fill to avoid lost wakeups. A full, idle horizon stops listening for capacity
changes. Fixed depth and global/object limits still cap requests, including
lookahead already started before the session controller. The controller keeps
only bounded completion markers, not another data buffer. Hot-cache eviction
does not trigger speculative redownload; foreground misses retain normal fetch
semantics. A failed fill waits for new consumption before starting another
executor attempt, preserving bounded retry behavior.

A real seek, a mode switch, an idle retirement and a lowered stage ceiling all
reset or clamp the budget instead of carrying it forward: a new pass starts from
the seed, and a lowering drops the accumulated credit and stops new admission
while letting useful in-flight loads finish. Raising a stage ceiling never
expands `H` on its own; only new consumption can. Seek, handle release,
concurrent hints, disabled prefetch, invalidation and cache clearing retire the
matching scheduling work; dependent foreground readers keep the existing
cancellation protection. Stale completions cannot recreate retired sessions.
Workers expire after 30 seconds without consumption, and eviction from the
bounded access history also retires their scope. Unidentified callers,
unknown-size objects and `ReadStream` with its own close scope retain bounded
batch scheduling. The new state is a constant number of session fields: there is
no second chunk cache and no per-file ledger. This change does not tune driver
defaults or infer a physical bandwidth ceiling from a configured request cap.

For a mounted same-FD workload, use
[measure-playback-session.py](../../scripts/dev/measure-playback-session.py)
against an isolated read-only mount. It records continuous reads, alternating
seeks, pause/resume, per-block hashes and bounded failure results. Prepare and
clear the test mount before the run; the runner never clears between reads or
owns the mount lifecycle.

Reconcile the result with the mount's read trace using
[analyze-read-traffic.py](../../scripts/dev/analyze-read-traffic.py). It reports
executor `body_bytes`, caller deliveries (total and unique), the delivered bytes
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
