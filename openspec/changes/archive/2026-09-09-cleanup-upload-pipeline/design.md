## Context

上一个上传管线 change 已将本地文件上传和 direct fallback 的字节复制统一，并抽取了 stream 生命周期 runner。当前仍有两个可见的整理点：completion adapter 只包裹现有路径查询，且 `uploadCopyChunkSize` 位于 `UploadService` 文件而非 copy 模块。另有 Core stream 转发函数可能可以合并，但必须先确认其错误边界和取消路径不会改变。

## Goals / Non-Goals

**Goals:**

- 删除确定无必要的过渡性重复代码。
- 保持所有外部行为、task 状态、恢复字段和错误语义不变。
- 用调用关系和回归测试证明每个删除是安全的。

**Non-Goals:**

- 不改 VFS 上传 engine、pending store、staging generation 或 recovery 算法。
- 不替换 `waitUploadTaskForPath` 的 polling 机制；那需要独立的 VFS completion 设计。
- 不重写 direct source 的 hash、offset、sample 校验和 provider adapter。
- 不为了减少文件数量而合并职责不同的模块。

## Decisions

### 1. 先做无行为变化的机械清理

先统一调用入口和移动常量，再考虑删除 Core 转发函数。任何删除都必须先通过 `rg` 确认调用方，并由原有上传测试覆盖。

### 2. completion adapter 的处理

`uploadCompletion` 当前被 direct fallback 使用，而本地 task 仍直接调用 `waitUploadTaskForPath`。优先让两个调用方使用同一入口；只有确认 adapter 没有未来替换价值时，才整体删除 adapter。不能只删除 wrapper 而保留两套调用路径。

### 3. stream 转发函数按生命周期处理

`begin/write/finish/cancel` 不能仅按“函数很短”删除，因为它们集中处理 Core closed 检查和 service 获取。若清理，使用一个明确的 stream session 持有 service，确保 handle、cancel 和恢复路径共享同一生命周期。

### 4. 保留复杂但有业务含义的旧代码

`stageExisting`、`rotateFrozenGeneration`、direct source hash/offset 校验、VFS `Engine.Execute` 和 replacement rollback 都是可靠性语义，不属于本次旧代码清理目标。

## Risks / Trade-offs

- [Risk] completion 入口调整影响 parent task 和 direct fallback 的结果合并。→ 保持 `task.Task`、`UploadResult` 和 `result_remote_id` 字段不变，并运行 core/mobile 上传测试。
- [Risk] 删除 stream wrapper 改变 closed/error 行为。→ 若不能证明 session 等价，保留 wrapper，不为了减少代码强行删除。
- [Risk] 常量移动导致测试或其他包依赖位置。→ 先搜索全部引用，保持包内名字不变。

## Migration Plan

1. 记录当前引用关系并运行上传基线测试。
2. 完成 completion 入口和常量整理。
3. 仅在调用关系清晰且测试覆盖后清理 stream wrapper。
4. 运行 gofmt、go vet、staticcheck、golangci-lint、race、stability、smoke 和完整测试。
5. OpenSpec change 全部任务完成后再提交或归档。
