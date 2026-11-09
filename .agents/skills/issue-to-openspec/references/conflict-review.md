# Conflict review checklist

Use this after the OpenSpec artifacts are drafted and before asking the user to
approve or implement anything. The goal is to find places where the proposal
fights an existing spec, a code contract, a default, or a lifecycle assumption.

## How to gather evidence

1. Read every relevant spec **in full**, including scenarios:
   `openspec show "<spec-id>" --type spec`.
2. Read the code the requirement would touch. Cite `file:line`.
3. Check the interfaces and sentinel errors the requirement depends on.
4. Check defaults, config schema, generated docs, and existing tests.
5. Compare the new requirement to the existing one line by line.

Do not rely on summaries or on the proposal's own description of the current
system. The proposal is the thing under review.

## Severity

- **Hard conflict** — the requirement cannot be implemented under the current
  contract, or an existing `MUST` says the opposite.
- **Spec gap** — the behavior changes an existing capability's assumptions but
  that capability has no delta.
- **Semantic inconsistency** — two requirements can both be satisfied only
  under an unstated precedence or ownership rule.
- **Scope gap** — the change claims a goal (for example "saturate bandwidth")
  but leaves an adjacent path out without saying so.

## Categories and worked examples

### 1. The contract cannot express the requirement

Example: a spec says the VFS must detect a server ignoring `Range` and fall
back to a single stream. `drive.Driver.Read` returns only an `io.ReadCloser`;
the VFS sees no status code or `Content-Range`, and reading only the requested
bytes cannot reveal a 200 full-body response.

Resolution: move the detection to the layer that sees the response (driver or a
typed error), or change the contract. Do not write a VFS requirement that the
contract cannot satisfy.

### 2. The capability model cannot express a parameter

Example: a spec says a driver may declare a numeric parallel-read limit, but
`drive.Capability` is a string list.

Resolution: add an optional interface for the numeric value, or drop the
numeric limit from the spec and constrain it globally.

### 3. Existing behavior contradicts a new `MUST`

Example: `ReleaseReadSession` is documented as "does not cancel foreground
reads or already useful cache fills"; a new spec says releasing a session must
cancel prefetch.

Resolution: treat it as an intentional behavior change. Update the comment,
docs, and tests; distinguish "cancel unfinished prefetch" from "discard
completed cache fills". Do not leave the old comment in place.

### 4. Layering and double ownership

Example: a driver already parallelizes one `Read` internally (Quark uses three
workers); the VFS proposal adds four parallel sub-ranges on top. The result is
4 × 3 requests and slot accounting that sees four, not twelve.

Resolution: distinguish "driver supports arbitrary Range" from "driver already
parallelizes internally". The VFS must stay single-stream for the latter, or
the driver's internal parallelism must be disabled when the VFS parallelizes.

### 5. Missing capability delta

Example: a new read-pipeline requirement says bulk reads skip the durable read
cache, while the existing `read-cache` spec is built around one-shot sequential
content being admitted and evicted first. The proposal only modifies
`observability`.

Resolution: add a `read-cache` delta that says admission is decided by read
intent and eviction only governs admitted content, or explicitly scope the
existing spec to admitted content.

### 6. Priority and coalescing

Examples:

- The spec says `bulk` MUST use low priority, but the caller already has an
  explicit `ReadPriority` (mobile `priority` option). Intent cannot override
  the explicit value.
- Two readers of the same window have different intents; one `windowLoad`
  decides the fetch shape and cache policy for both.
- "Single-file parallel limit" is ambiguous: per window or total across
  prefetch windows?

Resolution: define precedence (explicit priority wins), coalesce only
compatible fetch shapes, write cache per waiter, and state that the per-file
limit is total concurrent sub-ranges.

### 7. Window semantics

Example: `probe` must issue exactly one small Range, but the existing window
helper deliberately fetches two chunks for a large unaligned boundary read;
`interactive` must use a small window, but the existing default is eight
chunks.

Resolution: define a concrete window policy per intent and make it override the
generic boundary optimization.

### 8. Lifecycle and ownership

Example: a spec says caller cancellation must stop prefetch, but prefetch is
deliberately detached with `context.WithoutCancel`. Mobile `CloseFileJSON` only
cancels the foreground read context and never calls the VFS session-release
path.

Resolution: introduce an explicit read scope keyed by file/session, and add an
explicit VFS cancel entry for mobile handles. Do not assume the foreground
context outlives the prefetch.

### 9. Observability and health accounting

Example: a spec says "cancellation is not a read failure", but the existing
observability spec requires read errors to remain diagnosable and the code
records prefetch cancellation as an error detail.

Resolution: distinguish background prefetch cancellation (a canceled outcome)
from foreground caller cancellation (keep existing error/cancel semantics), and
define which event carries the reason.

### 10. Defaults and backward compatibility

Example: a change enables four parallel streams for bulk reads and stops
populating the read cache for one-shot transfers. That is a user-visible
behavior change even though no API breaks.

Resolution: call it out in the proposal and changelog; provide a rollback path
(driver default or code change, not necessarily a user knob); decide whether the
default should be opt-in.

### 11. Configuration surface

Example: `[read_cache]` currently means "cache capacity"; the proposal adds
`parallel_streams`, `window_chunks`, and `prefetch_depth` to the same section,
and `max_size = 0` becomes ambiguous.

Resolution: either add a separate `[read]` section, or document that
`max_size = 0` only disables cache writes and does not disable scheduling. If
the project decides that stability-sensitive parameters are driver-owned, do
not expose them to users at all.

### 12. Scope gaps

Examples: the change claims "saturate bandwidth" but only parallelizes one
window per file; many small files remain serial; `ReadStream` has no
cross-window read-ahead; `fs copy` / `share fetch` bypass the VFS entirely.

Resolution: include the adjacent work or state it as a non-goal/follow-up. Do
not let the proposal imply coverage it does not have.

### 13. Driver rate limits and retries

Example: parallel sub-ranges multiply requests, but the driver contract requires
one mount's concurrent calls to share a request-rate budget and to honor
`Retry-After`.

Resolution: require parallel sub-ranges to go through the same driver client and
limiter; add a 429 scenario test.

### 14. Tests that pin the old contract

Example: a driver capability test asserts an exact capability list; adding a
new capability fails it. A task says "verify with tests" but the test suite
still encodes the old behavior.

Resolution: update the pinned tests as part of the change and add a test for the
new behavior.

## Output template

Use this table in your review notes:

| Severity | Finding | Evidence | Impact | Resolution | User decision? |
|---|---|---|---|---|---|
| hard conflict | VFS cannot detect a 200 full-body | `pkg/drive/drive.go:376`, `pkg/vfs/read/reader.go:...` | fallback scenario is unimplementable | move detection to driver/typed error | no |
| spec gap | read-cache admission assumption changed | `openspec/specs/read-cache/spec.md:9` | two specs describe different admission | add read-cache delta | no |
| semantic inconsistency | intent priority vs explicit priority | `pkg/core/client.go:66` | caller intent silently overridden | explicit priority wins | no |
| scope gap | cross-file concurrency absent | `internal/cli/fs/read.go:370` | many-small-file goal not met | include or mark non-goal | yes |

## Rules

- Do not invent conflicts. If the new requirement is compatible, say so.
- Distinguish a real conflict from a missing detail or a follow-up.
- Fix what you can answer yourself; do not silently choose a user-owned
  trade-off.
- After every artifact edit, re-run `openspec validate --strict`.
- If a finding changes the capability list, update the proposal as well.
