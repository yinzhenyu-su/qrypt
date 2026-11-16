# Validation: adapt-session-prefetch-to-consumption

This record separates what was measured from what was not. Raw traces, runner
JSON, binaries and logs stay outside the repository under
`/private/tmp/qrypt-slow-link/adapt-a-20261116/` and
`/private/tmp/qrypt-slow-link/adapt*`; only sanitized numbers appear here.

## Build identity

| Role | Source | Binary SHA-256 |
| --- | --- | --- |
| A (control) | `git archive` of HEAD `eaaf001578021cda5dec1266082fa9be7be25de9` (contains every prerequisite slow-link fix) | `99d7d640578fe3c511ecb896c8d2ab99dc256eb45e5d78280f172879a4eedb6d` |
| B (first measured set) | A plus this change | `0e96c50555b4f74e11ebd85754e805ccc7cf8a095924c69ca96471be3cac3552` |
| B (final, re-checked) | A plus this change, with the plan snapshot also reporting the last budget reason | `e3d5c05be0888899207164f6adb755a269cda8d0222b4b6cba2b9a4cc7fea996` |

Toolchain `go1.27.4 darwin/arm64`, both roles built with
`go build -o <binary> ./cmd/qrypt`. Both binaries were built and measured in the
same session; no historical measurement is used as this change's control. The
rebuildable manifest is `build-manifest.json` in the artifact directory.

The two B binaries differ only in one debug-projection value: the `prefetch_plan`
event now carries the last budget reason instead of an empty string. That code
path is a read-only snapshot taken under the session lock and is never consulted
by scheduling, so ranges, bytes and timing are unaffected; the re-check below
confirms it (same amplification and range count).

## Status per acceptance layer

| Layer | Status | Notes |
| --- | --- | --- |
| Deterministic unit/boundary tests | passed | `go test ./pkg/vfs/read ./pkg/vfs` |
| Race and duplicate-boundary tests | passed | `go test -race ./pkg/vfs/read` |
| SFTP original 16 MiB short load | **passed** | three complete rounds on the first B; two complete rounds re-checked on the final B |
| SFTP small-segment speed check (7 MiB per arm) | **passed** | six arms A/B, B/A, A/B; see below |
| Formal seek on SFTP, Quark encrypted sample, Koofr (≥20 points, 3 rounds) | **not measured** | not run |
| ≥256 MiB one-FD continuous with 3/35 s pauses on those backends | **not measured** | not run |
| Cross-backend adoption decision | **incomplete** | only the SFTP short-load gate is decided |

Tasks 5.2, 5.3 and 5.4 stay unchecked, and this change does not complete any
acceptance task of `unify-read-policy-execution`. The long passes were dropped
after the user's guidance that the SFTP link tops out below 1 MiB/s, which makes
multi-hundred-MiB interleaved rounds impractical here; the short load is the
scenario this change targets.

## SFTP short-load method

Same existing SFTP file `512mb.bin` (536,870,912 bytes) as the prerequisite
validation. One isolated read-only mount per arm, one FD, Darwin `F_NOCACHE`,
both the persistent and in-memory read cache cleared once before the FD is
opened. Each arm reads 8 MiB from offset 0 with 1 MiB preads, then four 2 MiB
points at `point × (file_size − 8 MiB) // 21 + 137` with point order `[1,20,2,19]`
(starting offsets 25,165,961 / 503,316,617 / 50,331,785 / 478,150,793), pausing
three seconds after the second point. The FD is closed and cancellation drains
before the trace is copied. The runner's 600-second cap applied to every arm;
none approached it. Serial order was A/B, B/A, A/B.

`P95` over four points is nearest rank, i.e. the maximum point. Amplification is
executor plaintext `body_bytes` (completed + canceled + retried windows) divided
by the caller's 16,777,216 delivered bytes; it is not wire traffic.

## Per-arm results, first B (`0e96c5`), three complete rounds

| Arm | body_bytes | Amplification | Ranges begin/end | Source peak | 8 MiB continuous | Seek first-1 MiB P95 | Four seeks | Takeovers | Closed clean |
| --- | ---: | ---: | --- | ---: | ---: | ---: | ---: | ---: | --- |
| A1 | 52,936,708 | 3.155× | 34/34 | 4 | 56.4 s (0.142 MiB/s) | 7,773 ms | 66.1 s | 0 | yes |
| B1 | 22,396,932 | 1.335× | 36/36 | 3 | 24.2 s (0.331 MiB/s) | 3,967 ms | 25.4 s | 0 | yes |
| B2 | 21,381,124 | 1.274× | 37/37 | 3 | 21.5 s (0.372 MiB/s) | 4,004 ms | 25.6 s | 0 | yes |
| A2 | 52,707,332 | 3.142× | 34/34 | 4 | 56.0 s (0.143 MiB/s) | 7,543 ms | 65.7 s | 0 | yes |
| A3 | 53,133,316 | 3.167× | 34/34 | 4 | 56.4 s (0.142 MiB/s) | 7,772 ms | 66.0 s | 0 | yes |
| B3 | 21,413,892 | 1.276× | 37/37 | 3 | 21.1 s (0.380 MiB/s) | 3,967 ms | 25.7 s | 0 | yes |

All six arms are complete; every delivered byte count is exactly 16,777,216;
every `source_range_begin` has exactly one matching end; no FD-close tail was
active after close; no three-second takeover occurred; the pause issued no new
Range after its first 500 ms.

| Round | Order | Offset/length/hash equal | Amplification | Reduction vs A | Seek P95 change | Continuous rate change | Ranges |
| --- | --- | --- | ---: | ---: | ---: | ---: | --- |
| 1 | A then B | yes | 1.335× (≤2×) | −57.7% (≥30%) | −49.0% | +132.8% | 36 vs 34 (+5.9%) |
| 2 | B then A | yes | 1.274× (≤2×) | −59.4% (≥30%) | −46.9% | +160.2% | 37 vs 34 (+8.8%) |
| 3 | A then B | yes | 1.276× (≤2×) | −59.7% (≥30%) | −49.0% | +167.8% | 37 vs 34 (+8.8%) |

Every round passes every short-load gate. The fresh A arms (3.142–3.167×)
reproduce the historical 3.149× anchor within noise, so the improvement is not
an artifact of comparing against an old build.

## Per-arm results, final B (`e3d5c0`), two complete rounds

| Arm | body_bytes | Amplification | Ranges begin/end | 8 MiB continuous | Seek P95 | Takeovers | Closed clean |
| --- | ---: | ---: | --- | ---: | ---: | ---: | --- |
| A1 | 52,903,940 | 3.153× | 34/34 | 56.3 s (0.142 MiB/s) | 7,611 ms | 0 | yes |
| B1 | 21,413,892 | 1.276× | 37/37 | 21.6 s (0.370 MiB/s) | 3,971 ms | 0 | yes |
| B2 | 21,381,124 | 1.274× | 37/37 | 21.8 s (0.368 MiB/s) | 3,968 ms | 0 | yes |
| A2 | 53,035,012 | 3.161× | 34/34 | 56.2 s (0.142 MiB/s) | 7,729 ms | 0 | yes |

| Round | Order | Hashes equal | Amplification | Reduction vs A | Seek P95 change | Continuous rate change |
| --- | --- | --- | ---: | ---: | ---: | ---: |
| 1 | A then B | yes | 1.276× | −59.5% | −47.8% | +160.3% |
| 2 | B then A | yes | 1.274× | −59.7% | −48.7% | +159.2% |

The re-check reproduces the first set within noise. Its third round was not run
because of the user's bandwidth guidance; the two completed rounds already meet
the "at least two rounds" part of the reduction gate and every per-round gate.

## SFTP small-segment speed check

The full-file passes above cost 16 MiB of a link that tops out below 1 MiB/s. On
the user's guidance, the remaining SFTP speed check uses a small segment instead:
the repository runner with `--sequential-mib 2 --seek-count 4 --seek-mib 1
--seek-offset-bias 137 --resume-mib 1 --pause-seconds 3 --idle-seconds 5`, i.e.
2 MiB continuous, four 1 MiB fixed seeks at offsets
`point × (file_size − 1 MiB) // 5 + 137` for points `[1,4,2,3]`, a three-second
pause after the second point, then a 1 MiB resume after five idle seconds.
7,340,032 useful bytes per arm, one isolated mount, one FD, `F_NOCACHE`, caches
cleared once. Serial order A/B, B/A, A/B.

| Arm | body_bytes | Amplification | Windows | 2 MiB continuous | Seek first-1 MiB P95 | Four 1 MiB seeks (mean rate) | Resume |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| A1 | 21,544,964 | 2.935× | 30 | 6.6 s (0.304 MiB/s) | 7,839 ms | 0.131 MiB/s | 0.202 MiB/s |
| B1 | 11,091,972 | 1.511× | 21 | 5.3 s (0.375 MiB/s) | 3,919 ms | 0.273 MiB/s | cache hit |
| B2 | 11,091,972 | 1.511× | 21 | 5.6 s (0.357 MiB/s) | 4,074 ms | 0.275 MiB/s | cache hit |
| A2 | 21,512,196 | 2.931× | 30 | 6.7 s (0.299 MiB/s) | 7,750 ms | 0.131 MiB/s | 0.200 MiB/s |
| A3 | 21,544,964 | 2.935× | 31 | 6.5 s (0.309 MiB/s) | 7,813 ms | 0.131 MiB/s | 0.202 MiB/s |
| B3 | 11,091,972 | 1.511× | 21 | 5.5 s (0.361 MiB/s) | 3,911 ms | 0.272 MiB/s | cache hit |

| Round | Order | Hashes equal | Amplification | Continuous rate | Seek first-1 MiB P95 | Seek mean rate | Windows |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | A then B | yes | 1.511× vs 2.935× (−48.5%) | +23.4% | −50.0% | +108.6% | 21 vs 30 |
| 2 | B then A | yes | 1.511× vs 2.931× (−48.5%) | +19.4% | −47.4% | +109.4% | 21 vs 30 |
| 3 | A then B | yes | 1.511× vs 2.935× (−48.5%) | +16.7% | −49.9% | +107.5% | 21 vs 31 |

For 1–2 MiB reads the candidate keeps the speed advantage instead of trading it
away: half the seek latency, about twice the seek rate and 17–23% more
continuous throughput for roughly half the executor bytes and fewer Ranges
(21 vs 30) than the control. The amplification floor here is higher than on the
16 MiB load (1.511× vs 1.27×) because every 1 MiB seek still starts a fresh pass
at the 1 MiB seed and can leave up to one seed-sized tail unconsumed.

The resume rate for B is a cache hit, not a network read: after the fourth seek
the candidate had already delivered the following MiB, so those numbers are
excluded from every comparison. This check supplements, it does not replace, the
formal ≥20-point and ≥256 MiB gates, which remain unmeasured.

## SFTP small-segment check of the final candidate

The review fixes changed credit accounting, so the small-segment check was
repeated on binaries built from the reviewed tree:
`qrypt-c-darwin-arm64` (sha256
`03eb8ad3534b03c2d3dad9d27b60423656e1704a82c162a19cd6d207444057a5`) and, after
the probe fix, `qrypt-d-darwin-arm64` (sha256
`72569556c82ba10466cd938d8577fded0cc8b1298d278fef3feb7fa70cbaf441`). Both are
go1.27.4 darwin/arm64 from HEAD `eaaf001` plus the working-tree fixes. The
control is the frozen A binary. Same runner options and isolated mount; C and D
run interleaved with control arms.

| Arm | body_bytes | Amplification | Windows | 2 MiB continuous | Seek first-1 MiB P95 | Four 1 MiB seeks (mean rate) | Resume |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| A4 | 21,315,588 | 2.904× | 30 | 6.6 s (0.305 MiB/s) | 7,595 ms | 0.134 MiB/s | 0.199 MiB/s |
| C1 | 10,502,148 | 1.431× | 21 | 4.2 s (0.478 MiB/s) | 3,883 ms | 0.276 MiB/s | cache hit |
| C2 | 10,502,148 | 1.431× | 21 | 4.1 s (0.491 MiB/s) | 3,815 ms | 0.273 MiB/s | cache hit |
| A5 | 21,577,732 | 2.940× | 30 | 6.7 s (0.298 MiB/s) | 7,781 ms | 0.131 MiB/s | 0.199 MiB/s |
| C3 | 10,567,684 | 1.440× | 17 | 4.2 s (0.477 MiB/s) | 3,860 ms | 0.272 MiB/s | cache hit |
| C4 | 10,567,684 | 1.440× | 18 | 4.3 s (0.468 MiB/s) | 3,960 ms | 0.273 MiB/s | cache hit |
| A6 | 21,512,196 | 2.931× | 30 | 6.8 s (0.296 MiB/s) | 7,690 ms | 0.131 MiB/s | 0.199 MiB/s |

| Round | Order | Amplification | Continuous rate | Seek first-1 MiB P95 | Seek mean rate | Windows |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | A4 then C1 | 1.431× vs 2.904× (−50.7%) | +56.7% | −48.9% | +106.0% | 21 vs 30 |
| 2 | C2 then A5 | 1.431× vs 2.940× (−51.3%) | +64.8% | −51.0% | +108.4% | 21 vs 30 |
| 3 | C3 then A6 | 1.440× vs 2.931× (−50.9%) | +61.1% | −48.5% | +104.5% | 17 vs 30 |
| 4 | C4 (C3 pair) | 1.440× vs 2.931× (−50.9%) | +58.1% | −49.8% | +108.4% | 18 vs 30 |

The fixes do not cost performance: the pre-fix candidate measured 1.511×
amplification at 0.357–0.375 MiB/s here, and both reviewed builds measure about
1.43× at 0.47–0.49 MiB/s. Stricter credit means smaller windows sooner, which is
both less waste and less competition with the foreground read on a slow link. The
probe fix also removed a controller per seek, taking the window count from 21 to
17–18. All seven arms report `complete: true`, zero reconciliation error and
equal hashes; the candidate resume passes are cache hits and stay out of the
comparison. Those stored traces predate the always-published traffic count (see
below), so re-analysing them now flags the rows that never received a byte and
withholds the ratio; their byte totals are unchanged, and the adjudicated
evidence is the re-measure below.

## Traffic-count re-measure (adjudicated small-segment evidence)

The reconciler refuses a window row that has no executor traffic count instead of
substituting the row's caller bytes, so a measurement now needs a build that
publishes the count even when a body never started. The candidate publishes it
(`qrypt-e-darwin-arm64`, sha256
`557f2abab6b72a203c658fd7480e686c83f7a6bf462b5ed59f785cb9470817f1`); the control
is `eaaf001` plus the diagnostics-only publication patch
(`qrypt-a2-darwin-arm64`, sha256
`e90e6570f194e8fcd1eea9780648f46d8c1e2dbcfb51bde02fa06e52d6c02ea8`), which keeps
the baseline prefetch behaviour unchanged. Same runner options and isolated
mount, interleaved A/B/B/A:

| Arm | Body bytes | Amplification | Windows | 2 MiB continuous | Seek first-1 MiB max | Four 1 MiB seeks (mean) | Complete |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| Control A7 | 21,413,892 | 2.917× | 30 | 0.417 MiB/s | 7,819 ms | 0.132 MiB/s | yes |
| Candidate E1 | 10,567,684 | 1.440× | 17 | 0.511 MiB/s | 3,924 ms | 0.274 MiB/s | yes |
| Candidate E2 | 10,567,684 | 1.440× | 18 | 0.520 MiB/s | 3,895 ms | 0.273 MiB/s | yes |
| Control A8 | 21,610,500 | 2.944× | 30 | 0.621 MiB/s | 7,824 ms | 0.131 MiB/s | yes |

Amplification falls 50.6–51.1%, the slowest seek is 49.8–50.2% faster and the
mean seek rate doubles (+108%) in both pairs; all four reports have
`body_unknown_windows` 0 and zero reconciliation error. The 2 MiB continuous rate
did *not* separate this round: the second control arm reached 0.621 MiB/s against
0.511–0.520 MiB/s for the candidate, so on this link the earlier +17…+65%
continuous gains are not reproducible and are not claimed here. What repeats is
the traffic and seek result, which is what the change targets.

## Interval reconciliation

`scripts/dev/analyze-read-traffic.py` reconciled each arm's mount trace with the
runner result; `reconciliation_error_bytes` is 0 in all ten arms.

| Arm | Useful bytes | Completed unused tail | Canceled unknown residual | Duplicate attempts |
| --- | ---: | ---: | ---: | ---: |
| A1 (set 1) | 20,005,005 | 524,562 | 32,407,141 | 6,926,340 |
| B1 (set 1) | 16,793,604 | 2,097,152 | 3,506,176 | 536,580 |
| B2 (set 1) | 16,793,604 | 2,097,152 | 2,490,368 | 536,580 |
| A2 (set 1) | 19,972,237 | 524,562 | 32,210,533 | 6,893,572 |
| A3 (set 1) | 20,005,005 | 524,562 | 32,603,749 | 6,893,572 |
| B3 (set 1) | 16,793,604 | 2,097,152 | 2,523,136 | 536,580 |
| A1 (set 2) | 19,972,237 | 524,562 | 32,407,141 | 6,926,340 |
| B1 (set 2) | 16,793,604 | 2,097,152 | 2,523,136 | 536,580 |
| B2 (set 2) | 16,793,604 | 2,097,152 | 2,490,368 | 536,580 |
| A2 (set 2) | 19,972,237 | 524,562 | 32,538,213 | 6,926,340 |

Canceled-window bytes are not claimed as waste: for a canceled Range the trace
does not say whether the bytes past the delivered prefix arrived, so they stay in
the unknown residual. B moves roughly 29 MB per arm out of that residual and into
delivered bytes. "Useful" can exceed 16 MiB because a window's body is attributed
to the delivered interval it covers even when that interval was downloaded twice;
`duplicate_attempt_bytes` exposes that, and B cuts it by about 92%.

## Review fixes (consumption accounting, wake, depth, reconciliation)

Three review rounds raised eleven specification findings. Each one is now covered
by a test or a self-check fixture, and the small-segment performance claim was
re-checked on binaries built from the reviewed tree:

| Finding | Fix | Evidence |
| --- | --- | --- |
| Credit was gated by prefetch admission, so split reads earned less | `Read` credits any successful sequential delivery that is not a seek, stale, concurrent or prefetch-disabled read; admission only decides scheduling | `TestSessionCreditsSplitsInsideOneChunk` (same H, credit and delivered end for one 512 KiB read and two 256 KiB reads) |
| A growth without a cursor move did not wake the worker | The session update wakes on any budget transition, not only on cursor or plan changes | `TestSessionGrowthWakesWithoutCursorMove` (a 1-byte read growing H to 2 MiB admits the two new chunks); fails with `timed out waiting for growth refill` when the wake condition is reverted |
| A small budget still used the stage maximum depth | `depthChunks()` = `min(Dcap, ceil(H/Wcap))` drives every fill | `TestSessionDepthFollowsBudget` |
| A lowered stage ceiling kept stale credit when H still fit | `retuneLocked` clears credit whenever the new ceiling is below the previous one or below H | `TestSessionLoweredCeilingDropsCreditBelowBudget` (32 MiB to 16 MiB at H = 2 MiB); fails with `lowered ceiling reasons = [], want "cap"` when reverted |
| Canceled windows claimed bytes outside the prefix they received | Attribution is bounded by `received_end = min(end, start + body)` for a canceled window with a known byte count | review self-check: an 8 MiB request canceled after 1 MiB credited 1 MiB only when the read delivered inside that prefix |
| Per-pass ratios divided by the whole session's delivery | Passes divide by their own delivery; `cross_pass_delivered_bytes` reports neighbouring spill | review self-check: 1 MiB and 2 MiB passes both report 1.0x instead of 0.333x and 0.667x |
| A truncated log still printed a confident ratio | A partial runner or windows covering under 99% of delivered bytes mark the report `complete: false` with `completeness_problems`, and the amplification fields become `null` | review self-check: a 1 MiB delivery with no window rows reports `amplification_known: false` |
| Delivery accounting could start a prefetch controller, so an unconfirmed probe paid for speculative traffic | The session update takes a `create` flag: prefetch admission may create and start a controller, delivery accounting only updates one that already exists | `TestSessionCreditDoesNotStartController` (two 64 KiB reads inside one chunk at a non-zero offset create no controller and issue no prefetch); fails with `an unconfirmed probe created 1 prefetch controllers` without the flag |
| Windows were charged to the pass running when the row was logged, so a tail canceled by a seek landed on the next pass | Windows carry their request start (`at_ns - duration_us`) and are assigned by it; a request that finishes after its pass closes is counted in `cross_pass_tail_bytes`, and one that starts before the first pass stays unmatched | review self-check: a window requested at 75 ms that finished at 150 ms stays with the pass that ended at 100 ms, and the next pass reports its own 1.0x instead of 2x with no windows |
| Interval coverage was mistaken for complete session delivery, so two sessions reading the same range reported one of them as pure reuse | Per-session attribution needs every caller delivery matched to a traced read that carries a session identity; otherwise it switches off and `per_session_gap` names the gap | review self-check: two sessions reading 1 MiB with one logged read reports `per_session_attribution: false` plus a gap, instead of 0 bytes delivered for session 2 |
| Switching to an equal-budget fixed plan kept adaptive credit | `retuneLocked` handles the mode switch before the "budget unchanged" early return, so leaving adaptive always drops credit and records the transition | `TestSessionFixedSwitchDropsAdaptiveCredit`; fails with `switch to fixed reasons = [], want "fixed"` when reverted |
| The cumulative body count was used as the received prefix, although it counts replays | The trace exports `progress_end`, the furthest received offset; the analyzer trusts it as a prefix only for a single-stream window and reports the difference as `replay_bytes`; a window without an evidenced prefix keeps its whole body unknown | review self-check: a canceled window with a replayed 2 MiB body over a 1 MiB prefix reports 1 MiB useful, 1 MiB replay, no unused tail |
| 100% covered intervals were treated as proof that no window row was dropped | Completeness now also requires every driver range to have a matching begin/end row and to sit inside some window row, because a driver call cannot be admitted without opening and closing | review self-check: a range outside every window row with 100% coverage marks the report incomplete |

The 16 MiB short-load table was measured on the pre-fix candidate (`e3d5c05`);
it was not re-measured after the fixes and stays attributable to that build. The
small-segment check was repeated on both reviewed builds and improved (see the
tables above).

## Limitations

- One backend only (SFTP). No Quark encrypted sample, no Koofr, no ≥20-point
  formal seek pass and no ≥256 MiB continuous pass were run, so the cross-backend
  adoption decision is open and this change must not be reported as fully
  accepted.
- The sample is an all-zero file: identical hashes only prove cross-arm
  offset/length consistency. Non-uniform correctness comes from the deterministic
  tests, not from this measurement.
- Each arm clears the VFS cache once. Hot blocks, in-flight merging, the durable
  cache from earlier arms, OS caching and kernel readahead remain inside the
  measurement; the first 1 MiB completion is not CDN first byte or player first
  frame.
- Request counts rise 5.9–8.8% while bytes fall ~60%. That is inside the 10%
  request bound used by the formal gates, but it is a real trade-off: more,
  smaller early Ranges.
- The four-point P95 is the maximum of four points, not a 20-point percentile.
- The baseline binary predates `progress_end`, so its canceled windows have no
  evidenced received prefix and their bodies land in `unknown_residual_bytes`.
  Re-analysing the stored baseline arms therefore reports a larger unknown
  residual for them; executor body bytes, delivery and amplification are
  unchanged, and only the build under test can attribute a prefix.
- In the small-segment check, the control's resume pass was a real read while the
  candidate's was a cache hit; only the continuous and seek passes are compared.
- `configured streams` were never used to claim a parallel benefit; source peak
  in-flight dropped from four to three.

## Local checks

Run on the same tree as the final candidate; full logs are kept outside the
repository (`ci-final*.log`, `ci.log`).

| Check | Command | Result |
| --- | --- | --- |
| Format | `"$(go env GOROOT)/bin/gofmt" -l` on every changed `.go` file | no output |
| Unit + boundary | `go test ./pkg/vfs/read ./pkg/vfs` | pass |
| Race | `go test -race ./pkg/vfs/read` | pass |
| Strict specs | `openspec validate adapt-session-prefetch-to-consumption --strict` and `openspec validate unify-read-policy-execution --strict` | both valid |
| Traffic tool | `python3 scripts/dev/analyze-read-traffic.py --self-check` | pass |
| Diff hygiene | `git diff --check` | clean |
| Local CI | `bash scripts/ci/check-all.sh` with `go1.27.4`, default concurrency | exit 0, `== local CI check passed ==` |

The CI run is not a performance measurement and does not move any gate above
from "not measured".
