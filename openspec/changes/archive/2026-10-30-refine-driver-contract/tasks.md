## 1. P0: hash provenance and HTTP classification

- [x] 1.1 Add an optional `ProvenanceHasher` interface (algorithm, hash, verified flag, source) while keeping `RemoteHasher` compatible; verify a legacy driver is treated as `declared` and a provenance-aware driver can report `verified`.
- [x] 1.2 Update sync/integrity decisions to use only verified hashes; verify a declared hash does not skip a transfer while a verified hash does.
- [x] 1.3 Extend `drive.HTTPError` to classify `403`, `409`, and `422`, with a per-call option for endpoints whose structured `422` means not-found; verify localized messages are never parsed.
- [x] 1.4 Migrate the Drime driver to report `verified` hashes only after `verify-integrity`, and to use the shared HTTP classification; verify its instant-upload fallback still uploads on failed verification.

## 2. P1: share interfaces and destination context

- [x] 2.1 Split `ShareManager` into management (create/get/update/delete) and `ShareLister`; split `ShareSaver` into save and `ShareSavePoller`. Keep the capability names and policy checks stable; verify a provider can declare any subset without stub methods.
- [x] 2.2 Add `Title`, `Public`, `Download`, `Edit`, and `MaxEntries` to `SharePolicy` and the corresponding optional fields to `ShareCreateRequest`; verify unsupported options and over-limit entry counts fail before the provider call.
- [x] 2.3 Add `SpaceID` to `ShareDestination` and `SavedID`/`SavedName`/`Verified` to `ShareSaveItem`; verify Core verifies the destination without requiring a full provider entry.
- [x] 2.4 Migrate the share drivers and Core/CLI validation; verify Drime keeps create/show/update/remove, rejects unsupported title/visit/public/over-limit requests before calling, and saves across a provider-space destination.

## 3. P2: entry resolution and remote names

- [x] 3.1 Add optional `EntryResolver.Stat`; verify Core prefers it when private metadata is missing, falls back to listing when absent, and maps missing ids to not-found.
- [x] 3.2 Add optional `RemoteNameCodec` and an entry-extra wrapping helper; verify encoded names round-trip through list/read and over-limit names fail before write.
- [x] 3.3 Migrate Drime's `qrypt~` codec to the shared contract; verify special names still round-trip and the remote form is used for reads, writes, renames, and copies.

## 4. P3: shared retry and request-rate middleware

- [x] 4.1 Add a reusable retry policy that honors `Retry-After`, applies bounded backoff to transport/5xx/429 failures, and does not retry 401/403/404/422.
- [x] 4.2 Add a per-mount request-rate limiter shared by all concurrent calls of one provider client; verify concurrent calls share the budget.
- [x] 4.3 Migrate at least Drime and one existing driver to the shared middleware; verify request counts, retry counts, and cancellation behavior.

## 5. Documentation and verification

- [x] 5.1 Document the optional interface model and hash-trust rules in `docs/for-developer/driver-development.md`.
- [x] 5.2 Add contract-matrix coverage for optional share interfaces, unverified hashes, pre-call policy rejection, destination space, and name round-trips.
- [x] 5.3 Run `openspec validate refine-driver-contract --strict`, `gofmt -w`/`gofmt -l` on changed Go files, `git diff --check`, and `scripts/ci/check-all.sh` before each implementation merge.
