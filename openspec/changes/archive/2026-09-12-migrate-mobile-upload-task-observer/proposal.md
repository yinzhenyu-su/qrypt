## Why

qrypt now exposes a unified task snapshot and resumable task-event interface, but the Android client still creates uploads through compatibility-era transport APIs, re-polls task lists, and reconnects task events from sequence zero. This makes long uploads harder to observe, increases needless work, and can leave a recovered upload without a clear actionable state after process death.

The change aligns the mobile upload flow and developer observer with the current task contract so an app or agent can obtain one snapshot, consume incremental events, recover from an event gap, and distinguish resumable input from a task that requires retry or cannot continue.

## What Changes

- Add a control-plane task-event endpoint backed by the existing Core task event stream with after_seq replay and task_gap responses.
- Migrate Android upload creation to the current operation/task APIs while retaining stream handles for app-owned SAF input.
- Make Android task observation sequence-aware: snapshot first, apply incremental task snapshots, reconnect from the last sequence, and resnapshot on gaps.
- Drive resume actions from item-level capabilities and resume_offset, with explicit user-visible errors when no recovery action is available.
- Update the agent upload observer script and documentation to use task snapshots and task events rather than treating log events as upload state.
- Preserve the existing task schema, task event types, staging/direct fallback behavior, and mobile compatibility fields.

## Capabilities

### New Capabilities

- mobile-upload-task-observer: Exposes task-event replay to control-plane consumers and defines the snapshot-plus-incremental-event observation contract for upload monitoring.

### Modified Capabilities

- None.

## Impact

- qrypt control HTTP API: new task-event route and tests.
- qrypt mobile API consumers: Android binding and upload call sites move to CreateOperationJSON, CreateLocalUploadTaskJSON, and OpenTaskEventsFromJSON.
- qrypt-android: TransferManager, TasksScreen, task JSON models, and generated mobile bindings.
- Agent tooling: scripts/upload-status.sh and docs/for-developer/upload-observer.md.
- No new task state machine or event bus is introduced; existing task.Manager, persistent task store, and task.Subscription remain authoritative.
