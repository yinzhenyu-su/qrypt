# Driver validation and policy decisions

Use this reference for a driver-wide read audit or selecting new default read
policies. Use the parent skill's measurement workflow and
[playback measurements](playback-measurements.md) for commands, cache/session
semantics, timing and traffic attribution. Auditing a driver does not itself
require a new recommendation or authorize a live configuration change.

## Derive the inventory

Inspect current driver registrations, `ReadPolicyProvider`, `ReadLimitProvider`
and capability declarations, then compare them with a configured mount's resolved
policy when available. Defaults come from `DefaultReadPolicy`; wrappers may
forward or constrain the underlying driver. Record a dated snapshot for the
audit rather than adding a permanent capability table to this skill.

For each registered driver, record:

- The inherited or explicit Initial/Seek/Sequential window and prefetch policies,
  bulk window, workers, split ceiling/threshold, object budget and fetch bounds.
- Declared shared Range parallelism, internal parallelism and hard limits;
  distinguish a declared capability from verified provider behavior.
- The resolved recommendation, mount overrides and effective policy, plus the
  actual task sizes and concurrent calls observed under the tested workload.
- Protocol or wrapper constraints, such as ignored Range responses, URL setup,
  unknown sizes, encryption, path scoping or provider-private reads.
- Available tests, live account/sample availability and remaining evidence gaps.

Keep window workers, active lookahead windows and `MaxInFlight` separate.
`Bulk.Streams = 1` can coexist with several useful lookaheads. Lack of shared
parallel-range capability constrains within-window fan-out; it does not by
itself mean all foreground and prefetch requests must serialize. Internal driver
parallelism must not accidentally receive a second layer of shared fan-out.

## Assign coverage to the right layer

Reuse the shared contract and executor suites for inherited behavior. Add driver
adapter tests where provider behavior differs; avoid duplicating the entire
shared suite in each driver package.

| Layer | Applicable checks | Evidence boundary |
| --- | --- | --- |
| Driver read contract | Nonuniform bytes at nonzero/unaligned offsets, cross-block ranges, EOF/empty/short bodies, cancellation, Close and request lifetime | Provider adapters still need live smoke where fakes cannot establish behavior |
| HTTP adapters | Strict sub-range 206, ignored Range/200 fallback, 416 and response-length handling, expired URL/auth retry where applicable | Do not impose HTTP cases on local or SFTP adapters |
| Shared executor | Object/global budget one, mixed foreground/direct/prefetch demand, bounded retries, idle versus total, admission release and cancellation races | Passing these tests establishes shared correctness, not provider speed |
| Mounted session planner | Small adaptive start, delivery-only growth, repeats/stale completions, seek, cap/mode changes, 3/35-second pauses, no autonomous refetch, fixed/disabled/probe bypasses | Keep deterministic lifecycle tests separate from timed cloud measurements |
| Encryption and wrappers | Plaintext offsets, authentication/final block, stable object identity, one outer source admission, propagated cancellation and policy | Test both wrapped correctness and the relevant mounted workload |

For a shared scheduling change, choose representative backends by behavior:
high open latency, slow progressing single streams, and demonstrated parallel
benefit. Include local deterministic controls and encryption when affected.
Use this to establish shared behavior; do not copy the representatives' tuned
defaults to the rest of the inventory. Every changed driver recommendation needs
its own live workload comparison, or remains an unaccepted candidate.

## Select a small set of candidates

Start from the parent skill's bottleneck table. Attribute the delay before
choosing a knob: high queue time, slow open, slow body, delayed publication and
an empty lookahead pipeline call for different changes. A target block already
ready while the caller waits points to the shared delivery/wait path; increasing
driver concurrency is not evidence of a fix.

Retain an unchanged control. Use short, interleaved measurements to reject
obvious regressions, changing one factor per candidate. Confirm the executed
shape changed: a new worker count with the same single task is not a parallelism
experiment. Use per-window tasks/streams and source-wide call timelines at their
respective scopes. Shortlisting does not establish sustained playback or permit
changing a default; run the full workload matrix on the finalists.

Prefer the simplest policy that meets the goals. Keep inherited defaults when
they suffice. Add driver-specific values only for a demonstrated provider need;
preserve justified provider timeout bounds during unrelated window experiments.
Choose fixed versus adaptive from measured workload behavior, while preserving
the explicit mode contracts. Do not switch a fixed policy merely to claim it
benefits from adaptive session growth.

## Decide from joint evidence

Take workload sizes, repeat counts and acceptance margins from the active task
or OpenSpec and state them before the comparison. The current changes' 20-seek,
256 MiB and 10% gates are scoped requirements, not universal tuning constants.
Use the parent skill for those changes' exact criteria and interleaving rules.

Correctness, budget or lifecycle failures disqualify a candidate. For performance,
evaluate seek latency, total/steady useful throughput, requests, downloaded bytes,
errors and retries together, per round and per applicable workload. Averages must
not hide a failing round; lower traffic does not compensate for an unacceptable
seek or throughput regression. Check steady spans for new periodic request gaps.

Validate the reconciliation tool using the adversarial cases in
[Validate measurement evidence](playback-measurements.md#validate-measurement-evidence)
before using its ratios to accept a candidate. Incomplete observations leave the
affected metric unknown. Spatial overlap alone proves neither full logging nor
cross-session reuse; a plausible ratio is not a completeness check.

Keep validation status separate from the parameter decision:

- **Passed:** all applicable stated gates passed with sufficient evidence.
- **Failed:** a correctness check or acceptance gate failed.
- **Inconclusive:** measurements exist but noise, missing observations or
  contradictory rounds prevent a decision.
- **Not measured:** the required live workload was not run, including absent
  accounts, suitable files or mount access.

Adopt a candidate default only for the scope that passed. Otherwise retain the
current default and identify the missing evidence or failed gate. Broader or
cross-account claims require corresponding evidence. Store sanitized conclusions
in the change's validation document; retain raw artifacts outside Git.

## Decision record

Use one record per driver/candidate; include per-round workload rows rather than
only a pooled summary. Keep account names, provider file IDs, credentials and
signed URLs out of committed records.

```text
Driver / wrapper / date:
Scope and workload goals:
Control / candidate source and binary identities, toolchain:
Baseline recommendation / effective policy / hard limits:
Bottleneck evidence and layer responsible:
Single-factor candidate difference and expected effect:
Executed windows / tasks / workers / source concurrency:
Reproduction commands, sample properties, offsets and hash evidence:
Cache boundaries / FD lifetime / timing and percentile conventions:
Acceptance criteria and originating task/spec:
Per round and workload: seek P50/P95, useful total/steady rate,
  requests, body bytes, delivery bytes, amplification, errors/retries:
Trace completeness / unknown metrics / canceled tails and replay:
Validation status and failed gates or evidence gaps:
Parameter decision: retain default / adopt candidate / revert candidate:
Applicable environments, remaining limitations and follow-up:
Private artifact location or a sanitized evidence reference:
```
