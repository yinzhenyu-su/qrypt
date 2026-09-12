## Context

见 proposal.md - Why（含实测证据链）。设计相关的现状约束：

- 包装层已有既定的返回约定：`pkg/crypt/drive_wrapper.go` 的 `PutSource:344-349` 与 `Mkdir:285-292` 在成功后写 `entry.Name = <明文>` 并设 `entry.Extra = drive.EntryExtraWrapper{RemoteName: <后端名>, Raw: <原 Extra>}`；`List:139-149` 解密每个名称、失败则跳过。`drive.EntryRemoteName(entry)`（`pkg/drive/drive.go:72-83`）是读取后端名的唯一入口，在无附加信息时回退到 `entry.Name`。
- 驱动不使用 `entry.Name` 做重命名/移动：`pkg/drivers/quark/quark_list.go:131-143` 用 `entry.ID`（`fid`）与显式的 `newName` 参数。因此把返回值的名称改成明文不会影响任何驱动调用。
- 视图条目以路径为键存储（`pkg/vfs/view/commit.go` 的 `entries.Set(path, entry)`），列表渲染使用条目的 `Name` 字段，因此键与名称不一致时会出现"按名称寻址不到、按路径才找得到"的条目。
- 视图的两个按路径写入点是 `CommitUploadedEntry`（`:57-68`）与 `CommitRemoteRename`（`:91-120`），分别对应上传完成与远端重命名完成；`pkg/vfs/source_upload.go:84` 也走前者。
- 远端列表**不穷尽**：`crypt.List` 会跳过解密失败与非法明文名的条目（`drive_wrapper.go:139-149`），这些条目由 `ForeignEntries` 单独报告。因此"条目不在远端列表里"不能推出"条目已删除"。

## Goals / Non-Goals

**Goals:**

- 消除密文名以文件形式出现在挂载中的现象，从两个层面：包装层不再产出这种返回条目；视图不再存储名称与其键不一致的条目。
- 让上游同类契约破坏在发生时可观测（一条点名路径、给定名称与 id 的 warn），而不是靠用户发现假文件。
- 用可失败的测试固定两条契约。

**Non-Goals:**

- 不改重命名/替换上传的语义、rename shadow、pending 迁移或 foreign entry 报告。
- 不做视图与远端的自动对账删除：列表非穷尽，据此删条目会误删合法项；需要更可靠的"该对象已不存在"信号（例如读取时明确的 not-found），属独立设计。
- 不改任何驱动或 `EntryExtraWrapper` 的既有形状。
- 不新增 debug 接口（视图条目表仍不可从 socket 导出；本次改动的可观测性来自 warn 日志）。

## Decisions

### 1. 修复放在包装层的返回路径，而不是在视图层"擦屁股"

在 `Rename`/`Move` 成功后映射回明文并填 `Extra.RemoteName`（与 `PutSource`/`Mkdir` 完全同形），使"包装层返回调用方词汇的名称"成为统一契约。替代方案是在视图提交点直接改写名称——那会把上游的 bug 静默吞掉，且其它直接消费 `Rename` 返回值的调用方（`pkg/vfs/upload/replace.go` 的替换上传、`pkg/vfs/mutation/rename.go` 的重命名协调）仍会拿到密文名。两处都要有：根因在包装层，视图层只做不变量兜底。

### 2. `Rename` 用显式的新名，`Move` 用入参条目的明文名，后端名取自各自已知来源

`Rename` 知道它发往后端的 `encName` 与调用方要的 `newName`，二者直接对应，无需臆测。`Move` 不改名，所以明文名取入参条目的 `Name`（调用方的权威值），后端名取后端返回条目的名称（那正是后端词汇的名称）。两者都不做"解密返回名称再猜"——解密会引入"解不开怎么办"的分支，而这里根本不需要解密。

### 3. 视图层归一化并告警，而不是只告警或只归一化

只告警则假条目照旧出现；只归一化则上游 bug 永远不可见。两者同时做：按路径基名写入（保证用户看不到不可寻址的名字），并记一条 `WarnfEvery`（去重键按路径，避免热路径刷日志）。归一化方向以路径为准，因为路径是视图的键、也是所有调用方唯一的寻址方式。

### 4. 不做残留条目清理

修复后新写入都正确，但已经写进内存视图的条目仍是旧数据；清理它们需要与远端对账（Non-Goals 中说明其风险）。重启挂载即可清除，且远端数据未受影响——这一点写进文档与提交说明，避免用户以为丢了东西。

## Risks / Trade-offs

- **[改 `Rename`/`Move` 的返回值可能影响依赖旧行为的调用方]** → 已核对两类调用方：驱动按 `entry.ID` 操作（不看 `Name`）；VFS 侧只把返回条目用于视图提交、pending 记录与 rename 覆盖层，全都按"明文命名"假设工作。`Extra.RemoteName` 的消费方（`drive.EntryRemoteName`）在修复后拿到的仍是后端名，语义不变。
- **[归一化会掩盖上游契约破坏]** → 由同点的 warn 抵消；且 warn 带 id，足以定位是哪个提交点、哪个对象。
- **[warn 在热路径刷日志]** → 使用 `WarnfEvery` 并按路径去重；正常情况不会触发（触发即为 bug）。
- **[已存在的残留条目需要重启挂载才消失]** → 在提交说明与文档中写明；不引入有风险的自动对账。
