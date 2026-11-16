---
name: qrypt-read-tuning
description: Tune qrypt ReadPolicy windows, splitting, prefetch and concurrency using real-cloud measurements. Use for slow mounted reads or seek, driver-wide read-policy audits, driver recommendations, parallel_range decisions, playback-session prefetch diagnosis, and A/B experiments with probe test read.
---

# qrypt Read Tuning

Choose read policies from measurements. Range support alone does not establish
that parallel requests improve throughput or seek latency. Start by reading the
current driver recommendation and the mounted source's effective policy.

## Boundaries

- Prefer `qrypt probe test read --path` on existing files; do not edit or delete
  user content. Fixture mode creates and removes its own temporary file.
- Use a dedicated test mount and local config copy. Preserve existing Qrypt
  processes, mounts, config edits and credentials. Keep any temporary
  `test_enabled = true` change local and restore only your own changes.
- Interleave configurations on the same file in the same time window. Provider
  drift can reverse a ranking. Save raw JSON and errors from every run.
- Use measurement flags or the test mount's config for parameter sweeps. For
  stage policies or an authorized executor change, freeze separate control and
  candidate builds/Go overlays; do not edit the live control between samples.
  If testing an older release, check its own `--help`
  and use its matching config; new and old config formats are incompatible.
- Report functional tests, fake-driver tests and real-cloud measurements
  separately. Missing accounts or FUSE access mean performance is unverified.

## Current policy model

Read [execution architecture](../../../docs/for-developer/read-policy.md) when
changing a driver or caller, and
[configuration migration](../../../docs/for-user/read-policy-migration.md) when
converting an existing config. Check current Go declarations rather than
inferring defaults from historical benchmarks.

- Drivers build a complete `drive.ReadPolicy` from `drive.DefaultReadPolicy()`.
  Only `ReadOverride` is sparse; zero in a complete policy is not inheritance.
- `Interactive.Initial`, `Seek` and `Sequential` each contain a window policy
  (`caller` or `fixed`) and prefetch policy (`disabled`, `fixed`, `adaptive`).
  Fixed prefetch specifies both byte size and depth (1–4) and executes them
  literally; it has no consumption-credit ramp. Adaptive decisions resolve
  before execution and at every sequential-stream refill.
- An adaptive mount session starts from a seed of `min(Hcap, Wcap, 1 MiB)` and
  doubles its forward byte budget only after the caller consumed a full budget
  of new bytes. Credit counts delivered bytes rather than admitted requests, so
  the same range split into smaller reads earns the same budget. `Wcap × Dcap` is a ceiling, not the current range, so a short
  seek or a brief play is not padded with a full lookahead. Deleted, duplicate,
  failed and background bytes earn no credit, and a seek, idle retirement, mode
  switch or lowered ceiling resets it. Raising a ceiling alone does not grow the
  budget. Unidentified callers, unknown-size objects, probe, disabled prefetch
  and `ReadStream` keep their original bounded batch scheduling.
- `Bulk.WindowBytes` bounds a logical window, at most 32 MiB.
  `Bulk.Streams` limits workers. `Bulk.Split.PartBytes` is a strict part ceiling;
  zero divides the current window among granted workers. Task count and
  worker count are independent: 24 MiB / 4 MiB / 3 workers yields six tasks.
- `Bulk.Split.MinParallelBytes` gates on the actual window, not file size.
  A large video can still issue a single small seek request.
- Explicit interactive reads remain single-stream per window. Probe intent is
  single-block, single-stream and has no prefetch. The CLI command named
  `probe test read` measures mounted workload reads; its name does not force
  every measured read into probe intent. Eligible FUSE sequential reads may
  borrow bulk parallelism while retaining their original intent and cache rule.
- `MaxInFlight` limits simultaneous shared-layer requests to one object across
  foreground, prefetch, copy and share-fetch. Consumers reuse one mount-owned
  `ReadSource` and stable object ID. Different sources have independent budgets;
  this is not an account-wide or cross-process limit. Atomic admission acquires
  global and object capacity together; idle lookahead also works at budget one.
- Driver `ReadLimits` and capabilities constrain recommendations and overrides.
  No `parallel_range`, or `internal_parallel_read`, prevents shared fan-out.
  Driver-private HTTP requests are outside the shared layer's request count.

Mount overrides expose five fields only:

| TOML field | Meaning |
| --- | --- |
| `read.max_in_flight` | Per-object request budget, 1–32 |
| `read.bulk.streams` | Maximum workers, 1–32 |
| `read.bulk.window` | Bounded logical window in bytes or KiB/MiB |
| `read.bulk.part_size` | Strict part ceiling, or `"auto"` for even division |
| `read.bulk.min_parallel_size` | Window threshold, or `"0"` to remove it |

Omission inherits. Explicit zero is invalid for budget, streams and window.
Driver stage policies and fetch timeouts are not mount config fields.

**Check the shape before comparing speeds.** Quark recommends a 4 MiB bulk
window, an 8 MiB part ceiling, three workers, adaptive interactive prefetch and
a per-object budget of seven. That bulk window creates one task; a three-stream
setting alone does not prove three requests occurred. The budget is a bounded
replacement for the released reader, not an account-wide concurrency limit. For an even-division experiment, set `part_size = "auto"`
and a threshold below the tested windows in the isolated test config. Preserve
an unmodified default-policy control, and record every config difference.

## Choose the experiment

State the symptom and success criteria before changing a setting: cold seek
completion, a seek within an open playback session, sustained useful throughput,
or request/download amplification. These are different workloads. Preserve an
unmodified default-policy control and change one factor at a time.

For playback/seek diagnosis, custom mounted runners, code-version comparisons,
or long sequential passes, read
[Playback measurement and diagnosis](references/playback-measurements.md).
It covers FD/session lifetime, cache boundaries, frozen binaries, complete logs,
sliding-window invariants and the repository's same-FD playback runner. The
commands below are the standard CLI path.

## Audit drivers and choose defaults

For a driver-wide audit or a change to driver recommendations, read
[Driver validation and policy decisions](references/driver-validation.md).
Derive the inventory from current registrations, capabilities and policy
resolution; do not maintain another fixed driver list in this skill.

Cover applicable read contracts across drivers, validate shared scheduling on
representative backends, and require backend-specific performance evidence for
each changed default. A representative backend's speedup does not establish
another driver's performance. Missing accounts leave live acceptance unverified.

Keep three controls distinct: workers within a window, active prefetch windows,
and the shared per-object request budget. A one-worker bulk recommendation does
not require a one-request object budget, and Range support does not prove that
fan-out helps. Identify the bottleneck, shortlist single-factor candidates with
short measurements, then apply the active task's joint acceptance gates to the
finalists. Retain the current default when evidence is incomplete or conflicting.
Record the evidence, executed shape and parameter decision separately from the
validation status; the reference includes a reusable decision record.

## Measure

### 1. Establish a control

Use a file large enough for several windows (often 64 MiB or larger), a working
FUSE mount, and a binary with `probe test read --path`, `--streams`, `--window`.
Koofr may reject `.bin` files with `FileBlocked`; prefer a suitable `.txt` file
or the probe fixture.

The repository helper creates an isolated runtime and mount, then cleans up:

```sh
./scripts/dev/debug-mount.sh --config ./qrypt-bench.toml cloud read \
  --path "/existing/file" --samples 1 --cache-mode cold --pattern sequential
```

The config copy must also give `storage.work_dir` its own directory; the helper
isolates the runtime and mount point but does not rewrite that config field.
It deletes its temporary logs on exit, so use a persistent isolated mount when
complete traces are needed.

For repeated runs, start one dedicated mount with a separate `QRYPT_HOME` and
mount directory, and address the same runtime in each probe command. Do not
restart the user's active mount to change an experimental config.

Optionally measure raw driver headroom first:

```sh
qrypt probe bench read --mount cloud --size 64m --concurrency 1,2,4,8 --samples 1
```

This creates and removes a fixture, requires `test_enabled = true`, and bypasses
VFS, encryption and the shared source policy. A flat aggregate rate suggests a
provider/account ceiling for that workload; it does not validate mounted
planning, seek latency or budget enforcement. A rising then falling rate gives
candidate concurrency levels to test on the mounted path.

### 2. Sweep the mounted workload

`--streams N` (1–32; zero or omission keeps the plan) and `--window SIZE`
(positive, at most 32 MiB; omission keeps the plan) are temporary measurement
overrides cleared after the call. They do not change part size, split threshold
or driver hard limits. They affect the mounted VFS during the measurement, so
keep other workloads off that test mount. VFS aligns windows to cache chunks.

For an auto-part experiment with sufficient budget and eligible sequential
reads, 4/8/16 MiB windows with four workers target roughly 1/2/4 MiB parts.
Compare each against a one-worker control at the **same window size**. Confirm
actual execution; thresholds, caching and intent can suppress splitting.

Use separate invocations for the control and candidate, rather than three
consecutive control samples followed by three candidate samples. A useful
three-round order is A/B, B/A, A/B; keep `--samples 1` per invocation so the
rounds actually interleave. Repeat the sweep only after narrowing the variables.

```sh
QRYPT_HOME=/tmp/qrypt-bench/home qrypt probe test read \
  --config ./qrypt-bench.toml --mount cloud \
  --mount-point /tmp/qrypt-bench/mnt --path "/existing/file" \
  --length 256m --block-size 1m --pattern sequential \
  --cache-mode cold --samples 1 > round-1-control.json
```

This example needs a file at least 256 MiB long. For the candidate, add only the
chosen measurement overrides (for example `--streams 4 --window 8MiB`); the
control omits them. For independent part-size/budget configs or binary versions,
use the matching isolated runtime and preserve exact differences.

`cold` clears VFS cache before sequential samples and before each seek; inspect
`os_cache_control`,
`traversed_vfs` and cache counters before claiming remote cold reads. Setting
`read_cache.max_size = "0"` disables the durable cache, but in-memory hot chunks
can still coalesce reads within a sample. To measure without any VFS read cache,
resolve the existing plaintext entry and use the mount-owned `ReadSource.Fetch`
through the wrapped encrypted driver. `ReadRaw` would read ciphertext on an
encrypted mount and is not a plaintext seek comparison. Keep this measurement
separate from mounted latency: it bypasses stage planning and FUSE. The first
sample may include CDN startup. Compare distributions, not peak throughput.

For seek regressions, keep the default-policy control and use
`--pattern seek --seek-count 20 --seek-size 1m --cache-mode both --samples 1`
in each interleaved invocation. Measure
`--seek-scenario isolated`, `prefetch` and `concurrent` separately; verify the
recorded offsets are identical between candidates and that overlap scenarios
actually overlapped. Keep seek size, warmup and overlap timeout fixed. Stage
policy experiments require a candidate driver policy, not just bulk overrides.

### 3. Inspect observations

```sh
jq -r '.[0].read.measurements[] | "sample=\(.sample) bps=\(.end_to_end_bps) ttfb_us=\(.ttfb_us)"' run.json
jq '.[0].read.seek_summary' run.json
jq '[.[0].metrics[] | select(.phase == "fetch_window" or .phase == "prefetch_window") | {phase, extra}]' run.json
```

Read the diagnostic `read_policy` object's `recommended`, `effective`, `limits`,
`origins` and `source_id`. Three layers stay separate: `recommended` is what the
driver asked for, `effective` is what resolution allowed, and the session's
`prefetch_plan` / `prefetch_credit` events show the dynamic budget it executes
(`prefetch_mode`, `budget_bytes`, `credit_bytes`, `delivered_end`,
`window_cap_bytes`, `depth_cap`). In fetch-event extras distinguish:

- `planned_streams` / `read_streams`: planning context; not proof of concurrency.
- `granted_streams`, `range_tasks`, `window_bytes`, `part_bytes`: executed shape.
- `actual_in_flight`: peak admitted shared-layer requests for that trace;
  not provider-private socket concurrency or an account-wide peak.
- `queue_us`, `open_us`, `body_us`, `local_wait_us`, `validate_us`: execution
  timing. Read delivery timing uses `deliver_us`. Worker durations may aggregate,
  and local wait overlaps body time; do not sum them as wall-clock latency.

When `metrics_truncated` is true, missing fetch events leave actual concurrency
unknown. Cache-hit events cannot establish remote concurrency. Shorten the
measured range or capture additional diagnostics before drawing that conclusion.
A strict sub-range returning 200 produces `ErrUnsupported` and single-stream
fallback; exclude such runs from claims about parallel performance.

Mounted `ttfb_us` measures delivery to the mounted reader. `seek_load_us` and
`seek_total_us` measure the seek workload, not player decoding/rendering.
Driver open time is not CDN TTFB or player first-frame latency.

### 4. Adjust from the observed bottleneck

| Observation | Next experiment or fix |
| --- | --- |
| High `queue_us`, normal remote times | Inspect shared global/object usage and foreground demand; change budget only within hard limits and measure again. |
| Fast first block, long target wait | Correlate target readiness and `wait_window_*`; investigate publication/coalescing/takeover before enlarging a window or shortening timeouts. |
| Bulk workers configured but one task executed | Check actual window, part ceiling, split threshold, intent and capability. Test `part_size = "auto"` or a smaller ceiling in the config copy if parallelism is eligible. |
| Sequential throughput low with gaps in useful lookahead | Verify completion and consumption refill, capacity notifications, session hints, cursor holes and buffering. Bulk stream overrides alone do not alter interactive prefetch. |
| Many short speculative Range requests | Inspect sliding tail fragmentation and batch distant fills while preserving the horizon, cursor urgency and EOF handling; this is a planner change, not a new TOML knob. |
| Short play or seek downloads far past the consumed bytes | Check the session's `budget_change` sequence: it should start at `seed` and grow only on delivery. A `fixed` stage policy, a hintless caller or an unknown-size object never ramps, and a trace without session/request IDs cannot attribute the tail. |
| Body makes no progress vs. progresses slowly | Separate idle, total, truncation and local limiter wait. A progressing body or bare total expiry does not establish a bad CDN connection. |

Use a small single-variable sweep, confirm that the executed shape changed,
then recheck both seek and sequential behavior. Larger windows, more workers,
more lookahead and shorter timeouts can worsen contention or amplification.
Do not change all of them in one candidate. Respect unknown-size, single-stream,
internal-parallel, explicit interactive, probe and prefetch-disabled paths.

### 5. Decide and record

Use the active change's acceptance criteria. For `unify-read-policy-execution`,
compare old v0.6.1, implementation baseline and candidate in at least three
interleaved rounds with at least 20 fixed seek points per round. Cover Quark,
a high-RTT single-stream backend and a backend that benefits from parallelism.
Record cold/warm P50/P95, sustained useful throughput, errors, retries, downloaded
bytes and request amplification, plus actual concurrency.

For `adapt-session-prefetch-to-consumption`, acceptance is joint, not
traffic-only: the same short SFTP seek workload must cut amplification by at
least 30% in two of three interleaved rounds while its first-window P95 and its
continuous rate regress by no more than 10%, and the formal seek/long-pass gates
must hold per backend. Reaching the 2x short-load target does not excuse a
throughput or seek regression, and less traffic does not prove a faster player.

Attribute traffic with
[analyze-read-traffic.py](../../../scripts/dev/analyze-read-traffic.py) on the
mount trace plus the runner JSON. It separates executor `body_bytes`, total and
unique caller delivery, the delivered bytes inside each window, bytes another
session reused, real unused tails of completed windows, and an unknown residual
for canceled windows. A canceled window is traffic: only the prefix it received
is useful, delivered bytes outside it are not credited, and the remainder is
reported as unknown rather than claimed as waste. Take that prefix from
`progress_end`, not from `body_bytes`: the body count is cumulative and counts a
replayed attempt twice. A row with no `body_bytes` is unknown traffic, not
delivery-sized traffic; the trace publishes the count as zero when the body
never started, so a missing one means a truncated or pre-publication log. Each pass divides by its own delivery, and bytes from a
neighbouring pass are reported separately. Per-session ratios need each caller
delivery matched to a traced read with a session identity; interval overlap alone
switches attribution off with `per_session_gap` set. An incomplete report
(partial runner, windows covering under 99% of delivered bytes, an unmatched
driver begin/end row, or a driver call outside every window row) has no
amplification figure; treat it as failing evidence rather than a passing ratio. Recompute amplification from a delimited phase; do not quote a
canceled total as the waste figure, and do not pool cold and warm points.

For that change, seek P95 and sustained throughput must not regress by more
than 10%; Quark must also return within that margin of the old release.
Download amplification must not increase by more than 10%. A speedup claim
needs at least 20% improvement in two repeated rounds. Keep failed, interrupted,
slow and superseded runs; do not hide a per-round regression behind pooled
medians. Noise beyond the margin is inconclusive. These gates belong to that
change; other tuning tasks use their stated criteria.

Mixed results do not justify changing a driver's default recommendation or
claiming stable maximum bandwidth. Keep correctness/scheduling conclusions
separate from measured speed. Passing fake tests, race or CI is not performance
acceptance, and a local mount override is not an accepted global default.

Record date, commit plus working-tree/build identity, toolchain, mount/encryption,
file/range/hash, exact commands, config/effective policy, FD/session and cache
semantics, environment, raw sample paths, actual shape, fallback, amplification
and the decision. Sequential existing-file results include `content_hash`;
existing-file seek probes do not verify the fixture pattern or emit a content
hash. A custom runner must compare hashes for identical offsets and lengths
before claiming seek-byte equality. State the percentile convention; the CLI
uses nearest rank, so an independently calculated standard median can differ.

For code changes, run relevant fault/lifecycle/budget/race tests and the repo's
required checks after formatting with the module's GOROOT gofmt. Run full local
CI separately from cloud timing to avoid CPU/I/O contention; follow AGENTS.md
before committing. If code changes after measurement, identify which binary
was measured and whether the changed path requires new measurements.

Historical results are examples, not defaults or acceptance. The Quark
[comparison summaries](../../../openspec/changes/unify-read-policy-execution/validation.md)
show why faster delivery, bounded scheduling and stable throughput need separate
proof. The sliding-window comparison retained equal request counts but did not
establish a speedup.
Check current declarations and remeasure rather than copying old numbers.

### 6. Clean up

Stop and unmount only the test instance you started. Verify the exact mount is
inactive before removing its directories. Remove only fixtures created by the
experiment, plus its temporary configs, credential/runtime state and binaries;
preserve private raw reports/logs and the reproduction/build instructions.
Keep measurement JSON and raw logs in local artifact directories, outside Git;
commit only methods and sanitized conclusion summaries. Raw logs can contain
signed URLs, headers or credentials. Restore only your own config edits and compare Git
status with the starting state; unrelated edits and active user mounts may remain.
