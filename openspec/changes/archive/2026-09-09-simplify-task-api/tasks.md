## 1. Baseline and contract inventory

- [x] 1.1 Inventory all task creation, query, item, cancellation, retry, dismiss and event callers across `pkg/task`, `pkg/core`, `pkg/mobile`, control APIs and developer docs; verify the inventory covers every exported task entry point.
- [x] 1.2 Add contract-level tests for current upload, download, VFS bookkeeping, mobile stream, recovery and event behavior; verify the baseline suite passes before changing the task model.
- [x] 1.3 Define the versioned public task snapshot and operation schema, including operation kind, source/target policy, lifecycle states, phases, actions, item snapshots and structured errors; verify strict OpenSpec validation and API fixture review.

## 2. Stable task model and persistence

- [x] 2.1 Introduce stable operation commands and typed validation so direct/staging transport is internal policy; verify invalid combinations fail before a task is persisted.
- [x] 2.2 Split canonical task and item snapshots from creation detail, aggregate progress and derived actions; verify query and event fixtures expose identical item versions and no duplicate authoritative item lists.
- [x] 2.3 Add idempotency key persistence and conflict detection with the documented state-directory/session scope; verify same-key retry returns the original task and changed parameters return a conflict.
- [x] 2.4 Add execution generation to task updates and recovery; verify stale runner updates cannot overwrite the current runner or terminal result.
- [x] 2.5 Add journal versioning and migration for existing task records, including old upload task types and interrupted states; verify old fixtures replay into valid new snapshots.

## 3. Lifecycle, item controller and events

- [x] 3.1 Separate cancellation request, runner cleanup, terminal cancellation/failure and dismissal; verify an active task remains observable until cleanup completes and emits removal only afterward.
- [x] 3.2 Introduce a common item controller for query, cancel, input commit and output resume; verify upload and download stream tasks use the same routing contract without Core type-specific public branching.
- [x] 3.3 Add bounded event replay with sequence cursors and an explicit gap/snapshot-required result; verify reconnect from a sequence and overflow recovery both converge to the latest task snapshot.
- [x] 3.4 Keep stream handles session-scoped while making task item offsets and actions recoverable; verify process recovery resumes committed input without re-uploading confirmed bytes.

## 4. Mobile compatibility and old implementation cleanup

- [x] 4.1 Add the new mobile task JSON entry points and adapters while preserving existing request and result fields; verify existing mobile task and stream tests remain green.
- [x] 4.2 Route `CreateUploadTaskJSON`, `CreateDirectUploadTaskJSON`, `CreateLocalUploadTaskJSON` and related wrappers through the new operation model; verify direct/staging selection is invisible to callers.
- [x] 4.3 Mark old transport-specific task types and wrappers as compatibility-only and document the removal boundary; verify no internal caller depends on them directly.
- [x] 4.4 Remove the compatibility wrappers and obsolete public task-type branches only after migration coverage passes; verify `rg` finds no production references and the full API regression suite passes. Legacy transport types remain restricted to journal recovery and internal executor dispatch so existing persisted tasks stay readable.

## 5. Final verification and rollout

- [x] 5.1 Run core, task, mobile, VFS and control tests plus race tests; verify upload, download, cancellation, retry, recovery, idempotency and event-gap scenarios pass.
- [x] 5.2 Run `go vet ./...`, static checks, architecture checks, generated-doc checks and `git diff --check`; verify no unrelated API or data-plane behavior changes.
- [x] 5.3 Validate the OpenSpec change strictly and perform a compatibility review of old journal files and mobile clients; verify rollback leaves old task snapshots readable.
