# mobile-upload-task-observer Specification

## Purpose
为移动端和开发者工具提供一致、可恢复、低开销的上传任务观测流程，使长时间上传、进程重启和事件断档都能收敛到最新任务快照。

## Requirements

### Requirement: Task event consumers can resume from a sequence

The system MUST expose the existing task event stream to control-plane consumers with an optional sequence cursor. A consumer that supplies after_seq MUST receive only events after that sequence when the retained history is sufficient.

#### Scenario: Resume task events after reconnect
- **WHEN** an agent reconnects with after_seq equal to its last processed sequence
- **THEN** the system returns subsequent task events without replaying older events

#### Scenario: Event history is no longer sufficient
- **WHEN** the requested sequence is older than the retained task event history
- **THEN** the system returns a task-gap result with snapshot_required=true and the affected sequence range

### Requirement: Upload observers use snapshots as the recovery source of truth

The system MUST provide upload task snapshots containing task state, item state, progress, capabilities, resume offsets, and structured errors through the existing task query contract. Consumers MUST be able to resynchronize by fetching a snapshot after a gap, process restart, or lost event handle.

#### Scenario: Agent obtains the current upload state
- **WHEN** an agent queries user-visible upload tasks
- **THEN** the response includes the current task and item snapshots needed to classify progress, waiting, retry, failure, and completion

#### Scenario: Android resynchronizes after a gap
- **WHEN** the task event stream reports that a snapshot is required
- **THEN** the client fetches the latest task snapshot before resuming incremental event processing

### Requirement: Resume actions are selected from item capabilities

The system MUST expose item-level actions that distinguish committing already staged input from reopening application input at the persisted resume offset. A client MUST surface an explicit error when neither recovery action is available.

#### Scenario: Commit persisted staged input
- **WHEN** an interrupted upload item reports commit_input capability
- **THEN** the client can commit the staged item without asking the user to select the source file again

#### Scenario: Reopen application input
- **WHEN** an interrupted upload item reports open_input capability and a non-terminal state
- **THEN** the client can reopen the source and continue writing from the reported resume offset

#### Scenario: No resumable action exists
- **WHEN** an upload item is displayed as resumable but exposes neither input action
- **THEN** the client shows an actionable error instead of silently doing nothing

### Requirement: Upload observers do not treat log events as task state

The system MUST keep diagnostic log events separate from task lifecycle events. The agent observer MUST use task snapshots and task events for upload state, and MAY use log events only as supporting failure evidence.

#### Scenario: Observe a stalled upload
- **WHEN** task progress remains unchanged while the observer is watching
- **THEN** the observer reports the task phase, last known progress, error, and resource diagnostics from task/control APIs without interpreting log history as progress
