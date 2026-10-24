# Package Boundaries

This document is the target contract for package ownership. It complements
the current-state overview in [architecture.md](architecture.md): migrations
may be incremental, but new code must move toward these boundaries rather than
preserve an accidental dependency.

## Dependency direction

```text
cmd/qrypt -> internal/cli -> pkg/core -> pkg/vfs -> pkg/drive
                                      \            ^
                                       -> pkg/task |
                                       -> pkg/share -> pkg/drive / pkg/task

mobile app -> pkg/mobile -> pkg/core                  pkg/drivers/<name>

pkg/mount -> pkg/vfs
pkg/control -> diagnostic operations -> pkg/vfs / pkg/drive
pkg/control -> pkg/share
pkg/crypt -> pkg/drive
```

Composition roots may register concrete drivers through `pkg/drivers/all`.
All other dependencies point toward lower-level contracts. A package must not
bypass the layer directly below it merely to reuse an implementation type.

`scripts/ci/check-arch.sh` enforces this contract from Go package metadata.
Every top-level package under `pkg` declares the other top-level packages its
production code may import; packages not present in that policy fail the gate.
Tests follow the same private-application and provider-direction rules, while
remaining free to use test adapters such as `localfs` where the production
policy deliberately forbids concrete drivers.

The gate also parses the upload module's Go declarations: `VFS` may own upload
state through `upload.Domain`, while `upload.Domain` must not expose its store,
hash tracker, scheduler service or engine through exported fields or method
results. This is a type/ownership check, not a blacklist of receiver or method
names. The compiler and this gate do not prove that re-exported value types
preserve every semantic client contract; compatibility tests remain responsible
for those contracts.

## Ownership table

| Package | Owns | Must not own |
| --- | --- | --- |
| `pkg/drive` | Provider contracts, capabilities, provider-neutral errors and health | Filesystem semantics, concrete providers, UI/runtime policy |
| `pkg/drivers/<name>` | One provider's protocol, authentication and provider-specific session state | VFS, mount, control or CLI behavior |
| `pkg/crypt` | Encryption wrapper implementing drive contracts | VFS cache or application task state |
| `pkg/vfs` | Provider-independent filesystem facade, namespace routing and assembly of VFS domains | Provider APIs, mobile contracts or platform mount policy |
| `pkg/vfs/<domain>` | State and transitions for one VFS domain | Cross-domain composition or platform-specific filename policy |
| `pkg/task` | Generic task state machine, persistence and subscriptions | File-transfer implementations or client transport DTOs |
| `pkg/share` | Provider-neutral share-link and receive application contract: request/response DTOs, the service interface and secret redaction | Provider protocols, task execution, HTTP transport |
| `pkg/core` | Stable application facade and runtime composition | Client-specific JSON/gomobile types or provider protocols |
| `pkg/core/internal/*` | Application services hidden behind `core.Core` | Public entry points that let clients bypass `core.Core` |
| `pkg/mobile` | Gomobile-safe sessions, handles, callbacks and JSON envelopes | VFS/media/task implementation types or business workflows |
| `pkg/mount` | FUSE translation, handles, mount lifecycle and OS compatibility | Provider calls or provider-independent filesystem semantics |
| `pkg/media` | Media probing and virtual-file mapping: MP4 probe, virtual-file info and read mappings, virtual-file handles | VFS domain state, task orchestration or provider protocol |
| `pkg/control` | Local debug/probe/control HTTP transports, validation, route adapters and response encoding | Provider behavior, task orchestration or reusable diagnostic algorithms |
| `pkg/syncer` | Sync comparison, planning, snapshots and execution | Debug HTTP or client adaptation |
| `internal/cli` | Cobra commands and desktop process integration | Reusable domain behavior |

## Stable client boundary

`pkg/core` is the only application API consumed by `pkg/mobile`. Mobile may
blank-import `pkg/drivers/all` as a composition-root registration mechanism,
but it must not import `pkg/vfs`, `pkg/media`, `pkg/task`, concrete drivers,
mount, control or CLI packages. Core exposes client-oriented handles and value
types when an implementation object would otherwise cross this boundary.

Existing mobile JSON function signatures, error envelopes and cancellation
semantics are compatibility contracts. Moving ownership must not silently
change them.

## Core decomposition rules

`core.Core` remains the public facade. Implementation is extracted behind it
in this order:

1. local-file, storage, thumbnail and media services;
2. task application orchestration;
3. transfer operations and recovery;
4. runtime construction and lifecycle helpers.

As of this slice, the local-file and thumbnail services live in
`pkg/core/internal/localfile` and `pkg/core/internal/thumbnail`; storage and
media remain in `pkg/core` until their slices land. The thumbnail service
owes the cache key, persistence and pruning implementation; `core.Core` owns
the `ThumbnailInfo` DTO, the client-facing error wording and the runtime
composition (cache directory, budget and filesystem source), and adapts the
filesystem entry type onto the service's narrow `Source` interface.

An extracted service receives narrow interfaces and configuration values. It
does not import `pkg/core`, mutate another service's state, or publish a second
public facade. Task orchestration owns state transitions; transfer services
own byte/file movement. Neither responsibility is duplicated in the other.

## Diagnostic boundary

The control server is a transport adapter. Long-running driver checks,
benchmarks and process probes live in explicit diagnostic packages and are
callable without HTTP. The normal sync, mount, mobile and VFS execution paths
must not depend on control or diagnostic packages.

Code used by a production debug endpoint is not named `contracttest` merely
because tests also call it. Test-only fixtures and assertions remain in
`pkg/contracttest`; reusable diagnostic operations use names describing their
runtime responsibility.

## Migration acceptance criteria

Every boundary migration must satisfy all of the following:

1. Public CLI and mobile contracts remain compatible unless a separate API
   change is approved.
2. State has one documented owner; adapters depend on narrow interfaces.
3. Architecture checks reject the dependency that was removed.
4. Focused tests cover behavior and lifecycle/error paths affected by the
   migration.
5. `scripts/ci/check-all.sh` passes before merge.
6. Generated documentation is changed through its generator, never by editing
   generated output directly.
7. Unrelated worktree changes are preserved and excluded from the merge.

## Agent-assisted migration workflow

Architecture ownership stays with the integrator even when implementation is
delegated to Pi or another coding agent. Use one branch and Git worktree per
independent slice, and state the allowed files, compatibility invariants,
forbidden overlap with other active workers, and required verification in the
task. Workers do not commit by default; the integrator reviews the actual diff,
requests corrections in the same persistent session when useful, and owns the
final commit and integration.

Verification is layered rather than report-driven:

1. Inspect dependency direction, public API and JSON compatibility, lifecycle,
   cancellation, errors, concurrency, and the validity of new tests.
2. Run focused tests and focused race tests independently of the worker.
3. Run vet, the repository-pinned linters, build, `scripts/ci/check-arch.sh`, and
   `git diff --check`.
4. Run the complete `scripts/ci/check-all.sh` before committing. Package-level
   checks are not a substitute for this gate.

When a new architecture rule is added, add a focused checker test that makes
the forbidden import or ownership shape fail, then verify the clean repository
passes `scripts/ci/check-arch.sh`. Tests must prove the contract they claim: interface
satisfaction does not pin an exact concrete type, and race tests must not
mutate process-wide timing variables while background workers are active.

An agent or RPC timeout is not completion or failure by itself. Recover the
persistent session from its event log/session directory, inspect the worktree,
and resume it instead of restarting blindly. Meanwhile, the main branch may
receive unrelated user commits; re-check HEAD and status before integration
and never rewrite unknown concurrent work.

After cherry-picking, `git branch --merged` may not recognize the source
branch because the commit IDs differ. A completed worktree may be removed only
when it is clean and `git cherry main <branch>` marks its commits with `-`
(patch-equivalent on main). Active worktrees with uncommitted changes are kept.

CI failures are classified with evidence. Network/dependency failures are
environment failures. For an unrelated VFS stability failure, reproduce the
exact failing test repeatedly and then rerun the complete CI gate; neither an
isolated pass nor a worker's report is sufficient to waive the gate.

## Planned slices

The migration proceeds in independently mergeable slices:

1. Make mobile depend only on core (plus driver registration) and enforce it.
2. Extract low-coupling core services without changing the facade.
3. Make `pkg/task` the single task model and separate task orchestration from
   transfer execution.
4. Move runtime diagnostic execution out of control and split test-only
   contract helpers from production diagnostic operations.
5. Remove residual platform policy and duplicated helpers from VFS domains.
6. Re-audit imports, package documentation and state ownership, then tighten
   the architecture gate to encode the final graph.
