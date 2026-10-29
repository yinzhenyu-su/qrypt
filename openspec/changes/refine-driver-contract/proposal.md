## Why

The Drime driver landed successfully, but it exposed contract gaps that every new provider has to
work around: `ShareManager` requires a list method even when the provider has no listing endpoint,
`ShareSaver` requires a poll method even for synchronous saves, `file_hash` has no trust/provenance,
there is no by-id entry resolver after `Extra` is dropped, HTTP status mapping is incomplete, and
name encoding plus retry/rate limiting are re-implemented per driver. This change records a
prioritized contract refinement so future providers do not repeat those workarounds.

## What Changes

- Split optional share behavior behind independently discoverable interfaces:
  `ShareManager` keeps create/get/update/delete, `ShareLister` owns listing, `ShareSaver` owns the
  synchronous save request, and `ShareSavePoller` owns async polling. Core uses the policy and type
  assertions instead of requiring empty stub methods.
- Extend the share policy/request surface so providers can declare and callers can request link
  title, public/private mode, download permission, edit permission, and the maximum number of entries
  per link. Unsupported options are rejected before any provider call.
- Add provider-space context to share save destinations and allow save outcomes to carry a verified
  provider identity without forcing a full `Entry` round trip.
- Add hash provenance to remote hashing. A hash is either `verified` (provider computed or checked
  against stored bytes) or `declared` (client supplied). Sync MUST treat only verified hashes as
  authoritative.
- Add an optional `EntryResolver.Stat` interface for providers that can fetch one entry by id.
- Extend HTTP error classification for `403`/`409`/`422` and record a shared `Retry-After` +
  request-rate-limit contract; the full shared middleware is phased after the correctness items.
- Define an optional remote-name codec contract so drivers that must escape names keep plain names
  at the VFS boundary and round-trip through provider metadata.
- Record the implementation order: hash provenance and status mapping first, share interface/policy
  changes second, `Stat` and name codec later, shared retry/rate middleware last.

## Capabilities

### New Capabilities

- `driver-contract`: optional driver interfaces, hash provenance, by-id entry resolution, HTTP
  failure/retry semantics, and remote-name codec behavior shared by providers.

### Modified Capabilities

- `share-links`: link policy declarations and creation options gain title, public/private, download,
  edit, and maximum-entry declarations with pre-call validation.
- `share-receive`: save destinations gain provider-space context and save outcomes may carry a
  verified provider identity without a full entry object.

## Impact

- `pkg/drive`: optional share interfaces, `SharePolicy`/requests, remote-hash info, `EntryResolver`,
  HTTP error mapping, name codec.
- `pkg/core`: share validation and save verification use the new declarations and lightweight
  identities.
- `pkg/drivers/*`: existing share drivers split list/poll methods; providers that can resolve by id
  implement `Stat`; providers with name constraints implement the codec.
- `pkg/drivers/internal/driverutil`: shared retry/backoff/request-rate middleware in the final phase.
- Tests: contract-matrix coverage for optional interfaces, unverified-hash handling, pre-call policy
  rejection, save destination space, and name round-trips.
