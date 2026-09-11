## Context

The qrypt task model already contains full task and item snapshots, persistent recovery, bounded event replay, sequence cursors, and explicit gap events. Core exposes these events through OpenTaskEventsFrom, and the mobile bridge exposes OpenTaskEventsFromJSON. The missing consumer-facing piece is the control HTTP route and migration of qrypt-android from compatibility-era upload creation and full-list polling.

The Android app receives SAF/content URI input, so it must retain stream-handle APIs for staging uploads. The new task contract chooses the upload policy at task creation; direct versus staging fallback remains a Core decision.

## Goals / Non-Goals

**Goals:**

- Expose Core task events over the control API using the existing event and gap types.
- Make Android task observation snapshot-first and sequence-aware.
- Migrate app-owned uploads to the current operation/task creation APIs without changing the persisted task schema.
- Make resume behavior depend on item capabilities and resume_offset.
- Give the agent observer one stable snapshot-plus-events workflow.

**Non-Goals:**

- Adding another task state machine, event bus, upload snapshot type, or retry runner.
- Changing direct upload, staging fallback, persistence, or driver capability semantics.
- Making Android own upload truth or exposing SAF concepts in Core.
- Treating /v1/events log history as a replacement for task events.

## Decisions

### Reuse the existing task event model

The control endpoint will call the same Core subscription path used by the mobile bridge and return task.Event values, including task_gap. This keeps seq, replay, and overflow behavior in one implementation.

### Use snapshot plus incremental events

Consumers first call the existing task query endpoint, record the last processed sequence, and then subscribe with after_seq. On a gap or uncertain reconnect, they fetch a new snapshot and reopen from that sequence boundary.

### Keep stream handles only at the app-input boundary

Android uses the operation/task creation API with staging_only for SAF input, then uses OpenUploadItemJSON, WriteUploadItem, and CommitUploadItemJSON to supply bytes. Core owns staging persistence, cloud upload, recovery, and progress. Stable filesystem paths use CreateLocalUploadTaskJSON; reopenable source tokens use prefer_direct.

### Add an HTTP task-event route, not a mode on /v1/events

The control API gets a distinct task-event route such as /v1/task-events with after_seq and task filters. /v1/events remains the log diagnostic endpoint.

### Test observable boundaries

Core tests cover cursor replay and gap responses. Android tests cover event replacement, gap resnapshot, capability-based resume selection, and explicit error display. Agent checks cover snapshot output and cursor handling where a live Core endpoint is available.

## Risks / Trade-offs

- [Risk] A consumer can still miss events between snapshot and subscription. → [Mitigation] Open from the snapshot sequence and resnapshot on task_gap.
- [Risk] A long-lived event connection can outlive an agent or screen. → [Mitigation] Bound reads, close handles on disposal, and reconnect from the last sequence.
- [Risk] Generated Android bindings may not contain new mobile entry points. → [Mitigation] Regenerate bindings before migrating call sites and retain compatibility exports until verification passes.
- [Risk] High-frequency progress events can increase UI work. → [Mitigation] Batch events per read, replace rows by task id, and remove redundant full polling after synchronization is reliable.
