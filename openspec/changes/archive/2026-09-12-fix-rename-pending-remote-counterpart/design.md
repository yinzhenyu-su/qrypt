## Context

见 proposal.md - Why。设计相关的现状约束：

- `mutation.Coordinator.Rename` 的 pending 分支只判断 `IsPending(oldPath)`，命中后直接调用 `PendingRenamer.RenamePending`，不触碰远端（`pkg/vfs/mutation/rename.go`）。
- 冻结代可以继续上传而新的可变代同时积累（`pkg/vfs/staging.go` 的 `rotateFrozenGeneration` + `Flush`），所以「后端已有旧代对象」与「本地仍有 pending」可以同时成立。
- pending 在列目录投影里的合成条目 `ID` 就是 staging FID（`pkg/vfs/listing_host.go` 的 `mergePendingChildren`），可据此区分「后端对象」与「pending 投影」。
- `view.ResolveWithRuntime` 对「最近本地新建目录」下的子路径直接返回 not found（`pkg/vfs/view/resolve.go`），因此用解析子路径来判断后端对象存在性并不可靠——刚被 `Mkdir`/目录重命名过的目录正是这种状态。

## Goals / Non-Goals

**Goals:**

- 重命名后旧路径不出现在后端，正式路径承载最新本地内容。
- 纯本地 pending（后端无对象）保持原有的零后端依赖重命名语义。
- 覆盖文件重命名与目录先重命名两种情况，两者共用同一后端探测。

**Non-Goals:**

- 不改变无 pending 的普通远端重命名路径。
- 不引入延迟删除、回收站或后台清理。
- 不改变上传引擎的替换/去重流程与驱动契约。

## Decisions

### 用独立的 RemoteEntry 探测而不是解析源路径

在 `mutation.Resolver` 上新增 `RemoteEntry(ctx, path) (drive.Entry, bool, error)`，VFS 适配器实现为「解析父目录 → 列出其子项 → 按名字匹配并过滤掉 pending 投影」。

备选：直接 `Resolve(oldPath)` 后比较返回 ID 是否等于 pending FID。被否：目录重命名会按身份变化丢弃子树缓存，之后 `Resolve` 可能既拿不到缓存条目、又因「最近本地新建目录」短路而返回 not found，从而漏判存在性。

### 探测直接列父目录，而不是解析子路径

`view.ResolveWithRuntime` 会在父目录为「最近本地新建目录」时对子路径返回 not found。列表路径（`listChildren(parentPath, parent.ID)`）不走该短路，能看到后端真实子项。目录先重命名的场景只有这样才能正确判定。

### 先迁移后端对象，再移动 pending 记录

顺序为 `InvalidateReadCache` → `RenameMove` → `CommitRemoteRename` → `RenamePending`。

备选：先移动 pending 记录再迁移后端对象。被否：后端迁移失败时 pending 已指向新路径，旧后端对象残留且 pending 会在新路径重新创建对象，形成更差的分叉；而「远端先成功、pending 迁移失败」在调用方重试时可自愈（旧路径已无 pending，重试落到远端分支）。

### 探测错误语义：not-found 视为不存在，其余错误向上传播

父目录列表返回 `drive.ErrNotFound`（后端尚未收敛）时按「无对象」处理，保持本地重命名的可用性；其他错误让重命名失败而不是静默跳过迁移。

备选：所有列表错误都视为不存在。被否：会在后端瞬时错误时静默留下永久可见的孤儿对象，正是本次要修的问题形态。代价是纯本地 pending 重命名在父目录列表瞬时失败时也会失败——调用方可重试，且不会留下残留。

### RenamePending 收紧为 parentID 并在重命名时清空 ReplaceUpload

接口参数从 `parent drive.Entry` 改为 `parentID string`，空值表示保留记录当前父标识（PartialError 落在「旧父 + 新名」时需要）。同时清空 `ReplaceUpload`：它记录的替换目标属于重命名前的位置，上传引擎会在新位置按 target index 重新探测。

## Risks / Trade-offs

- [pending 重命名从「完全不碰后端」变成会列一次父目录] → 列表通常已被解析/列目录预热；纯本地路径判断为不存在后行为与原来一致。已接受这次行为变化。
- [父目录列表瞬时错误会让重命名失败] → 选择暴露可重试错误而非静默残留孤儿；相比残留，调用方重试代价更低。
- [PartialError 下 pending 记录跟随到中转路径] → 与既有视图提交语义一致（旧父 + 新名），并保留原父标识，上传仍指向正确的父对象。
- [探测与 RenamePending 之间 pending 可能被上传消费] → RenamePending 在路径锁内重新读取记录，缺失时返回错误；调用方重试会落到远端分支，最终收敛。

## Migration Plan

无持久化或 wire 变化：`RemoteEntry` 是纯查询，`RenamePending` 是包内接口，`ReplaceUpload` 清空只影响下一次上传的目标探测。无需数据迁移或回滚步骤。

## Open Questions

无。
