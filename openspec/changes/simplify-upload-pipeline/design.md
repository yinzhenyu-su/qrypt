## Context

当前移动端 direct upload 在 `pkg/core/upload_stream_direct_task.go` 中同时负责 task 生命周期、source provider、hash 预扫描、重试、进度、direct transport 和 staging fallback；移动端 staging upload 则通过 `pkg/core/upload_stream_task.go` 调用 `pkg/vfs/staging.go`，最终由 `pkg/vfs/upload/engine.go` 上传。两条路径共享目标解析和 driver 上传语义，但维护了重复的读写循环和状态推进。

本设计必须保持移动端 JSON API、driver capability contract、FUSE `Create/WriteAt/Flush` 行为和持久化恢复兼容。不得把 FUSE 的随机写生命周期强行暴露给移动端调用方。

## Goals / Non-Goals

**Goals:**

- 用一个共享 task runner 管理上传 item 的状态、进度、取消、重试和完成。
- 用统一的 source 抽象支持应用 source、普通本地文件和 staging snapshot。
- 用统一的 source-to-writer 复制逻辑实现 direct fallback 和本地文件上传。
- 让 direct transport 在能力满足时绕过 qrypt 本地 staging。
- 保留 staging generation、pending journal、覆盖替换和恢复语义。
- 将 transport 选择限制在一个明确的 coordinator 边界。

**Non-Goals:**

- 不修改 `pkg/drive` 的 driver 接口或 provider-specific multipart/resumable 实现。
- 不修改移动端公开 JSON 请求格式。
- 不移除 FUSE staging；FUSE 的随机写和异步可恢复语义仍以 staging 为基础。
- 不在本次 change 中重写所有 driver 的上传 session。

## Decisions

### 1. 使用统一的内部 `UploadSource`，而不是让 task 直接依赖 provider

内部 source 直接复用已有的 `drive.ReadOnlyFileSource`，只在 Core 内增加 writer 边界；mobile provider、本地文件和 staging snapshot 继续通过已有 source 适配。hash 能力通过现有可选的窄接口提供，只有加密 content-dedup 场景才执行预扫描。

选择该方案是为了让 direct、fallback 和 retry 都使用同一套可重新打开的 source。相比让 task 直接持有 `UploadSourceProvider`，它不会把 Android/JNI 细节泄漏到 Core。

### 2. 将上传执行分成输入阶段和远端执行阶段

显式 staging upload 先通过 staging session 接受 `WriteAt`，commit 后生成持久化 pending upload；direct upload 则直接生成 source 并交给 remote executor。两者共享 task runner 和最终结果模型，但不共享不适用的写入操作。

```text
TaskRunner
  -> UploadCoordinator
       +-> DirectSource -> DirectRemoteExecutor
       `-> StagingSession -> StagedRemoteExecutor
```

### 3. direct fallback 使用同一个 writer 复制器

将 `uploadSourceViaStaging` 和 `UploadLocalFileResult` 中重复的 `Read -> WriteAt -> Finish` 循环收敛为 source-to-writer helper。helper 只负责字节复制和 short write/error 处理，不负责 task 轮询或 task 状态。

### 4. 将 direct fallback 的完成等待隔离为 completion adapter

当前 VFS 对 Core 暴露的是按路径查询 remote task，尚未提供稳定的 pending-to-task ID 完成信号。本次先把完成等待隔离为 Core 内部 completion adapter，使 source copy 不依赖 task 查询细节；后续若 VFS 提供稳定 completion ID，可以替换 adapter 而不改变 direct task 和 writer 接口。这样避免为了本次整理扩大 VFS 任务持久化范围。

### 5. 保留 VFS upload engine 作为可靠性边界

pending generation、snapshot、目标租约、替换旧对象、失败重试、view commit 和缓存失效仍由 VFS upload engine 负责。Core 只负责选择输入和提交 task，避免把 VFS 持久化细节复制到移动端 task。

## Risks / Trade-offs

- [Risk] 统一 runner 改变 task 状态更新顺序。→ 先保留现有 task 状态和 detail 字段，增加 direct/staging 行为测试，再逐步删除重复实现。
- [Risk] direct fallback 的等待结果改造可能影响已有 task persistence。→ 在迁移期间保留旧 task 类型和恢复读取逻辑，完成一次旧记录恢复测试后再删除路径轮询。
- [Risk] source 按 offset 重开要求移动端 provider 支持 seek。→ 在 source adapter 边界明确检测 offset 能力；失败时返回可识别错误，不静默重复或截断上传。
- [Risk] direct 和 staging 的覆盖语义目前位于不同 VFS 入口。→ 先抽取共享目标/提交测试，再移动实现；不直接删除 `VFS.UploadSource` 或 `Engine.Execute` 的现有回滚逻辑。
- [Risk] 大文件 hash 预扫描增加移动端读取次数。→ 普通 mount 不预扫描；仅在 content-dedup 明确需要时启用，并继续支持 raw FD 快速路径。

## Migration Plan

1. 增加 source、writer、target 和结果的内部类型，以及 source-to-writer 的行为测试，不改变现有入口。
2. 将本地文件上传和 direct fallback 改为使用统一复制器。
3. 抽出共享 task runner，先让 staging stream 接入，再让 direct stream 接入。
4. 将 direct transport 选择和 fallback 移入 coordinator，保留旧 task 类型和持久化字段。
5. 用集成测试验证 direct 无本地 staging、fallback 有 staging、staging 恢复、覆盖替换和取消重试。
6. 通过 CI check 后再删除不再使用的旧 helper；若迁移失败，可逐步回退到原 task runner，不改变外部 API。

## Open Questions

无。当前方案已确定接口边界、兼容范围和迁移顺序，剩余选择属于实现细节，不改变规范或架构。
