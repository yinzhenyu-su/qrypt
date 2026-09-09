## 1. Control-plane task event API

- [x] 1.1 Add a control HTTP task-event route backed by Core.OpenTaskEventsFrom, accepting task filters and after_seq; verify task_updated and task_removed events retain their original sequence and task snapshot.
- [x] 1.2 Preserve task_gap and snapshot_required responses from the existing subscription; verify an expired cursor instructs consumers to resnapshot.
- [x] 1.3 Add control server tests for replay, filtering, gap handling, malformed cursor input, and cancellation; verify the focused Go test package passes.

## 2. Mobile task contract and bindings

- [x] 2.1 Regenerate or update mobile bindings so CreateOperationJSON, CreateLocalUploadTaskJSON, and OpenTaskEventsFromJSON are available to qrypt-android; verify the generated API can be called from the Android build.
- [x] 2.2 Keep existing task and item JSON fields while documenting the snapshot-plus-event contract; verify fixtures parse progress, capabilities, resume_offset, and structured errors.
- [x] 2.3 Add or update mobile event tests for after-sequence replay and task-gap delivery; verify the mobile task test suite passes.

## 3. Android upload migration

- [x] 3.1 Migrate SAF/content URI upload creation in TransferManager to the operation API with staging_only while retaining stream item writes and commit; verify a staged upload still produces a user-scope task and terminal event.
- [x] 3.2 Migrate stable local-path upload callers to CreateLocalUploadTaskJSON and direct-capable source callers to prefer_direct; verify transport selection remains invisible to Android callers.
- [x] 3.3 Update QryptModels parsing for current task snapshot fields and item actions; verify open_input and commit_input decisions are represented without transport-specific assumptions.
- [x] 3.4 Update TasksScreen observation to snapshot once, apply events by task id, reconnect with the last sequence, and resnapshot on task_gap; verify live progress no longer depends on unconditional three-second full polling.
- [x] 3.5 Update resume handling to commit staged input, reopen source input at resume_offset, or show an explicit error; verify missing source, invalid offset, and unavailable capability each produce user-visible feedback.

## 4. Agent observer

- [x] 4.1 Update scripts/upload-status.sh to use task snapshots for baseline state and task events for incremental updates, keeping /v1/events as supporting logs only; verify one-shot JSON output remains stable.
- [x] 4.2 Document sequence cursor, gap recovery, stalled-upload classification, and resource diagnostics in docs/for-developer/upload-observer.md; verify a new agent can follow the workflow.

## 5. Integration verification

- [x] 5.1 Run qrypt task, mobile, control, and upload regression tests including recovery, retry, event-gap, direct, and staging scenarios; verify compatibility tests pass.
- [x] 5.2 Build qrypt-android with regenerated bindings and run the available upload/task unit tests; verify no stale compatibility binding remains in the production upload path.
- [x] 5.3 Perform real-device manual QA for long upload, process restart, resume, event reconnect, and visible error reporting; record environment limitations instead of treating compilation as runtime proof.
