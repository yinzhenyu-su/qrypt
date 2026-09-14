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

调用方请求的暂停 MUST 保持权威：当一个输入项因为调用方的失败（`Fail`）而进入 `waiting_input` 时，该状态与随附错误 MUST 一直保留到调用方自己动作（重开写入、提交或取消）为止。后台的进度观测——查询云端任务并刷新诊断字段的轮询——MUST NOT 把它改回运行态、MUST NOT 清除其错误，也 MUST NOT 因此收回它隐含的 `commit_input` / `open_input` 能力。理由与该 spec 中"终端项不被重写"的既有约定一致：观测节拍与调用方并发运行，调用方意图权威。

#### Scenario: Commit persisted staged input

- **WHEN** an interrupted upload item reports commit_input capability
- **THEN** the client can commit the staged item without asking the user to select the source file again

#### Scenario: Reopen application input

- **WHEN** an interrupted upload item reports open_input capability and a non-terminal state
- **THEN** the client can reopen the source and continue writing from the reported resume offset

#### Scenario: No resumable action exists

- **WHEN** an upload item is displayed as resumable but exposes neither input action
- **THEN** the client shows an actionable error instead of silently doing nothing

#### Scenario: Background progress does not resume a paused item

- **WHEN** an input item has been failed by the caller and at least one background progress tick has since observed the item's cloud task
- **THEN** the item still reports `waiting_input` with its original error, and still offers the capability matching how much input it has staged — so the caller's next commit or reopen succeeds instead of failing with "item is running"

#### Scenario: The pause is released by the caller, not by time

- **WHEN** the caller reopens or commits the paused item
- **THEN** the item resumes from the pause and subsequent background progress observations may drive it forward again

### Requirement: Upload observers do not treat log events as task state

The system MUST keep diagnostic log events separate from task lifecycle events. The agent observer MUST use task snapshots and task events for upload state, and MAY use log events only as supporting failure evidence.

#### Scenario: Observe a stalled upload
- **WHEN** task progress remains unchanged while the observer is watching
- **THEN** the observer reports the task phase, last known progress, error, and resource diagnostics from task/control APIs without interpreting log history as progress
