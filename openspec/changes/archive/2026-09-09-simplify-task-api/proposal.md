## Why

当前任务接口用一个通用 Request、多个自由字符串状态、动态 Detail 和 transport-specific task type 同时承载创建、执行、恢复、取消、重试和事件语义，导致调用方必须理解内部实现。现在任务已覆盖移动端上传、下载、VFS 同步和批量操作，有必要在继续扩展前收敛公共契约。

## What Changes

- 用面向操作的创建模型替代按内部 transport 暴露的任务类型，direct 与 staging 成为上传策略而不是公共任务类型。
- 收敛任务、任务项、进度、阶段和可用操作的状态模型，减少 `Detail`、`Progress.Phase`、`Capabilities` 之间的重复和冲突。
- 为任务创建、取消、重试、删除和任务项操作定义明确的生命周期和错误语义。
- 增加任务创建幂等能力，避免移动端请求超时重试产生重复任务。
- 为任务事件增加可恢复的序列边界或快照重同步语义，避免事件丢失后 UI 长时间停留在旧状态。
- 在兼容期保留现有移动端 JSON 入口，通过兼容层迁移调用方，完成后再删除旧的 transport-specific wrapper。

## Capabilities

### New Capabilities

- `task-api`: 提供统一、可恢复、可幂等的任务创建、状态查询、任务项操作和事件订阅契约。

### Modified Capabilities

<!-- No existing task-api capability spec exists; upload-pipeline remains unchanged by this proposal. -->

## Impact

- 影响 `pkg/task` 的任务模型、Manager、Store、事件订阅和持久化格式。
- 影响 `pkg/core/task.go`、stream task 的 item controller、任务恢复和取消/重试流程。
- 影响 `pkg/mobile/task.go`、任务事件和上传/下载 wrapper 的 JSON 接口。
- 需要兼容已有任务 journal、移动端请求格式和现有任务查询结果，必要时提供版本化迁移。
- 不改变文件上传的 direct/staging 数据正确性；本变更只重新定义任务控制面和公共任务契约。
