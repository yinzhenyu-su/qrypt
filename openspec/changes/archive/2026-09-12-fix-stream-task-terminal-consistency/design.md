## Context

见 proposal.md - Why（含失败快照与两处缺陷的定位）。设计相关的现状约束：

- `closeDoneIfTerminalLocked`（`upload_stream_task.go:1079`）只在所有 item 终态时关闭 `done`；runner 在 `<-batch.done` 上等待并返回 nil（`upload_stream_task.go:313-330`）。因此"runner 正常退出"与"所有 item 终态"本应等价。
- 该等价会被 `applyRemoteUploadState` 打破：item 已终态并关闭 `done` 之后，ticker 驱动的刷新把 item 改回 running（`default` 与 `StateFailed && Retryable` 两个分支），于是 `finishTask` 看到的 `itemsDone < len(items)`。
- `finishTask` 的两个分支共用一次 `summaryLocked()` + `resultItemsLocked()` 读取（同一把 `b.mu`），所以它发布的 task 状态与 items 来自同一时刻——不一致不是"两次读取"造成的，而是"读到的状态本身矛盾"。
- 下载族（`download_stream_task.go:480`）有同构的 `finishTask`，但没有远端状态回写，因此只有第二处缺陷。

## Goals / Non-Goals

**Goals:**

- 终态 item 不被任何异步观察改写（尤其：取消不被静默撤销）。
- "任务终态 ⇒ 同一快照内 item 全终态"成为结构性保证，并让聚合计数与之一致。
- 让三个已存在的集成用例不再随机失败，恢复门禁可信度。

**Non-Goals:**

- 不改任务状态机与终态集合、不改 `partial_failed` 的语义、不改 `closeDoneIfTerminalLocked` 的判定条件。
- 不改重试、恢复、幂等或 wire 字段。
- 不引入"等待在飞 item 完成"的新等待逻辑（runner 已退出，等待无对象）。

## Decisions

### 1. 终态短路放在 `applyRemoteUploadState` 的入口，而不是逐个分支加判断

在函数开头判定 `item.State` 是否属于终态集合，若是则只更新观测字段并返回 false。替代方案是在 `default` 与 `Retryable` 两个分支各加一次判断——那会漏掉未来新增的分支，而"终态不可改写"是关于整个函数的性质。

返回值保持 false：现有语义里只有"远端成功且需要 dismiss 远端任务"才返回 true，终态短路不产生新的 dismiss 需求（取消场景下远端任务由 `CancelStream` 处理）。

### 2. runner 退出时把在飞 item 收敛为 failed，而不是拒绝发布终态

`finishTask` 在计算分支前，凡非终态 item 一律置为 failed，错误信息说明"任务收尾时该项仍在进行"。考虑过的替代方案：

- **不发布终态**：任务会永久停在 running，App 永远等不到结果，比矛盾快照更糟。否决。
- **把在飞 item 视为成功**：谎报数据已上传，可能丢数据。否决。
- **返回错误让上层重试整个任务**：与"runner 已退出"矛盾，且会重复上传已完成的项。否决。

### 3. 两族一起改，但差异如实记录

下载族没有远端回写，故只改 `finishTask` 的收敛；上传族两处都改。两族的 `finishTask` 保持同构（同一条不变量的两份实现），避免"只有一族保证一致"的隐性差异。

## Risks / Trade-offs

- **[被收敛为 failed 的 item 实际上可能上传成功]** → 收敛只发生在 runner 已退出、没有任何机制会再推进该项时；宁可显式失败也不留永久 running。错误信息写清原因，便于用户重试。
- **[终态短路会掩盖远端真实变化（例如远端在取消后仍完成）]** → 观测字段仍在更新，诊断面能看到远端状态；只是不据此改写本地终态。这是有意选择：用户的取消是权威意图，不应被远端结果反转。
- **[行为可见变化：取消的 item 此前可能悄悄完成]** → 属于修复；在 change 与提交信息中显式记录，便于回溯"为什么这次取消真的停了"。
