## Why

当重命名的源路径既有后端对象、又有更新的本地 pending 代时，`mutation.Coordinator` 只看本地 pending 就整体走纯本地重命名，从不处理后端旧名对象：`pkg/vfs/mutation/rename.go` 的 pending 分支直接 `RenamePending` 返回，跳过了 `Resolve`→`RenameMove`→`CommitRemoteRename`。

这条路径在两种真实场景下都会发生：

1. 下载器（如夸克浏览器）先写 `name.qkdownloading`，某一代在重命名前就已经上传到后端（close/fsync 触发 `Flush` 冻结、debounce 过后上传完成，随后又有新写入），随后把临时名重命名为正式名；
2. 编辑一个已同步文件后直接重命名它。

两者的结果相同：后端在正式名之外永久残留旧名对象（`.qkdownloading` 或旧文件名），本地列目录同时看到两个对象，且重挂载后依然存在。`pkg/vfs/upload/commit.go` 的 `rollbackUploadedEntry` 只覆盖「上传仍在飞行中被换代顶掉」，已经提交完成的旧代无人回收。

## What Changes

- 重命名协调器在 pending 分支新增后端存在性探测：源路径在后端确有对象时，先把该对象 `RenameMove` 到目标，提交视图，再把 pending 记录重指到目标，使更新的本地内容在上传时覆盖被移动的对象；后端无对象时保持原来的纯本地重命名（不触碰远端）。
- `PartialError`（已改名未移动）下镜像既有语义：提交「旧父 + 新名」的中转状态，并让 pending 记录跟随到该中转路径且保留原父标识。
- 后端探测直接解析父目录并列出其子项，而不是解析子路径本身：刚创建目录下的子路径会被「最近本地新建目录」短路判为不存在，绕开该短路才能看到目录重命名后真实存在的后端对象。
- pending 重命名接口把 `parent drive.Entry` 收紧为 `parentID string`（空值表示保留记录当前父标识），并在重命名时清空 `ReplaceUpload`——它记录的替换目标属于重命名前的位置，上传引擎会在新位置重新探测。
- 非目标：不改变无 pending 的普通远端重命名语义；不引入新的远端清理时机或延迟删除；不改变上传引擎的替换/去重流程。

## Capabilities

### New Capabilities

无。

### Modified Capabilities

- `rename-pipeline`: 新增要求——源路径同时存在后端对象与 pending 本地修改时，重命名 MUST 把后端对象一并迁移，使旧名在后端不再残留。

## Impact

- 代码：`pkg/vfs/resolve.go`（新增后端存在性探测）、`pkg/vfs/mutation_rename.go`（`RemoteEntry` 适配、`RenamePending` 签名与 `ReplaceUpload` 清空）、`pkg/vfs/mutation/rename.go`（协调器合并分支与中转处理）。
- 测试：`pkg/vfs/mutation_test.go` 新增三个端到端回归（临时名一代已上传后再重命名、先重命名目录再重命名临时文件、编辑远端文件后重命名）；`pkg/vfs/mutation/rename_test.go` 新增协调器单测（搬对象、PartialError、远端失败）。已验证三个集成用例在修复前失败、修复后通过。
- 行为变化与代价：pending 重命名现在会多一次后端父目录列表（原先完全不碰后端）；该列表的非 not-found 错误会让重命名失败而不是静默残留孤儿对象。这是有意取舍——宁可暴露可重试错误，也不留下对用户永久可见的残留。
