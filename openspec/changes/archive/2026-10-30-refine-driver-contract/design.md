## Context

See `proposal.md` for motivation and the delta specs for the behavior contract. The Drime driver
implemented every workaround this change addresses: `SharePolicy.List=false` plus an unsupported
`ListShares`, a synchronous `SaveShareEntries` plus an unsupported `GetShareSave`, destination
workspace resolution through `Entry.Extra`, a client-declared `file_hash` surfaced as
`RemoteHash`, `HTTPError` wrapped locally for `422`, and a driver-local `qrypt~` name codec.

qrypt already has the related behavior specs `share-links`, `share-receive`, and `upload-pipeline`.
This change records the interface-level decisions needed to remove those workarounds without
changing the user-facing command surface.

## Goals / Non-Goals

**Goals:**

- Make optional provider behavior optional at the type level, not only at runtime via a policy flag.
- Make hash trust explicit so sync never treats a client-declared hash as authoritative.
- Give Core enough destination/result context to drive share saves without reading driver-private
  `Extra` fields.
- Phase the work so correctness fixes land before cross-driver refactors.

**Non-Goals:**

- Implementing trash, file versions, search, archive extraction, or user-to-user sharing.
- Changing the "one workspace per mount" model.
- Making every driver implement every optional interface.
- Replacing the existing `RemoteHasher`/`ShareManager`/`ShareSaver` types in one breaking release.

## Decisions

### Split share interfaces, keep the capability names

`ShareManager` becomes create/get/update/delete. `ShareLister` owns account-wide listing.
`ShareSaver` owns `SaveShareEntries`; `ShareSavePoller` owns `GetShareSave`. Core selects the
optional behavior with a type assertion and still uses `SharePolicy` for pre-call validation.
Existing drivers migrate mechanically: quark keeps both list and poll; Drime omits both.
Alternative: keep monolithic interfaces and accept stub methods. Rejected because every new provider
pays for methods it cannot implement, and compile-time intent is lost.

### Add missing link policy and request fields

`SharePolicy` gains `Title`, `Public`, `Download`, `Edit`, and `MaxEntries`. `ShareCreateRequest`
gains optional `Title`, `Public`, `Download`, and `Edit`; `MaxEntries` lets Core reject a multi-entry
request before the driver. Existing policies default to today's behavior: unknown fields are
unsupported and `MaxEntries=0` means no declared limit. Alternative: leave validation to each driver.
Rejected because the CLI cannot explain a provider gap until the driver call fails.

### Carry destination provider-space context explicitly

`ShareDestination` gains `SpaceID string`; drivers that need a workspace/drive identifier use it
instead of reaching into `Entry.Extra`. `ShareSaveItem` gains `SavedID`, `SavedName`, and `Verified`
so a synchronous provider can report a verified identity without a full `Entry`; Core still verifies
the destination listing within its bounded window. Alternative: keep `Extra` as the transport.
Rejected because `Extra` is driver-private and is dropped by copy/rebuild paths.

### Hash provenance is an optional, additive interface

Add an optional `ProvenanceHasher` returning `{Algorithm, Hash, Verified, Source}`. The existing
`RemoteHasher` remains for compatibility and is treated as `declared` unless the driver also
implements the new interface. Sync uses a hash only when `Verified` is true; declared hashes remain
advisory. Alternative: change `RemoteHasher`'s return type. Rejected as a breaking change across
every driver and every fake. The Drime driver will report `verified` only after
`verify-integrity`, and its instant-upload path already verifies before reuse.

### Entry resolution is optional and preferred over re-listing

Add `EntryResolver.Stat(ctx, id)`. Core attempts it when the entry's private metadata is missing;
drivers without it keep the existing listing/parent behavior. This is not required for correctness,
so it is scheduled after the share and hash work. Alternative: require `Stat` everywhere. Rejected
because path-based and S3-like providers may have no cheap by-id endpoint.

### HTTP classification is shared; retry/rate middleware is phased

Extend `HTTPError` to classify `403` (permission), `409` (conflict), and `422` (invalid input) in
addition to today's mappings, with an option for endpoints where `422` means not-found. Record a
shared contract for `Retry-After` and per-mount request-rate limiting, but implement the full
middleware after the correctness items. Alternative: immediately build a shared HTTP client layer.
Deferred because existing drivers already implement local retry and the cost is cross-cutting.

### Remote-name codec is optional and explicit

Add an optional `RemoteNameCodec` with `Encode`/`Decode`, plus a helper for wrapping an entry's
private metadata while keeping the plain name at the VFS boundary. Providers that preserve names
unchanged do not implement it. Providers that escape names must return the plain name from listings
and reads and reject names whose provider form exceeds the limit. Alternative: standardize one
global escaping scheme. Rejected because providers differ in forbidden characters and length limits.

## Phases

1. **P0 — correctness:** hash provenance and HTTP `403`/`409`/`422` classification.
2. **P1 — share surface:** split share interfaces, policy/request fields, destination `SpaceID`, and
   lightweight save results.
3. **P2 — metadata and names:** optional `EntryResolver.Stat` and `RemoteNameCodec`.
4. **P3 — transport:** shared retry/backoff and request-rate middleware in `driverutil`.

## Risks / Trade-offs

- **Interface churn across drivers.** Keep old types available and migrate drivers incrementally;
  Core type-asserts optional behavior so an unmigrated driver still compiles.
- **Policy fields can drift from actual behavior.** Contract tests must assert every declared policy
  field in both directions, including `MaxEntries` and permission flags.
- **Unverified hashes can remain in existing caches.** Sync must treat the new provenance as
  authoritative even when a legacy driver returns only the old interface.
- **`422` is overloaded.** The HTTP option must let a driver map a structured not-found body without
  weakening invalid-input classification elsewhere.
- **Name codecs can create confusing remote names.** Only providers that cannot preserve names
  should implement them; the codec's provider form must stay within the provider limit.

## Open Questions

- Whether the CLI exposes download/edit/public controls in the first share-policy release or only
  validates them when supplied.
- Whether `SpaceID` is enough for every multi-space provider or whether a generic destination context
  map is needed later.
- Whether a second provider needs name encoding before the codec interface is implemented.
