## Why

流式任务可以发布**自相矛盾的单份快照**，并且用户取消的 item 会被远端状态刷新复活。这不是理论风险：全量套件里约每 4 次运行就有 1 次命中，失败输出就是证据。

`TestUploadStreamTaskCancelItemRemovesStaging` 的失败快照（同一份任务对象，不是两次请求的差）：

```
task State:partial_failed   Progress:{ItemsDone:0 ItemsTotal:1 ItemsFailed:0}
  └ Result.Items[0] State:running  Phase:queued  StagingBytesDone:7 == Total:7  Cancelable:true
```

任务已终态，它唯一的 item 却是非终态、还宣称可取消，而任务自己的计数是 0 done / 0 failed。`task-api` 的既有要求已经禁止这种结果（"进度阶段和可用操作不得与任务状态产生互相矛盾的结果"、"单一权威的任务项快照"），所以这是实现没有兑现既有契约，不是缺少契约。

两处代码缺陷叠加造成该现象：

1. **远端状态刷新会改写终态 item**（`pkg/core/upload_stream_task.go:838-879` 的 `applyRemoteUploadState`）。它由 ticker 驱动（`refreshUploadStreamCloudProgress`，`upload_stream_task.go:782`，间隔 `UploadStreamTaskPollInterval`），在 `batch.mu` 下取远端上传任务的状态并回写 item：`default` 分支无条件 `item.State = task.StateRunning`，`StateFailed && Retryable` 分支同样把 item 置回 running。用户 `CancelTaskItem` 把 item 置为 canceled（`upload_stream_task.go:558-587`）并关闭 `done` 之后，一次仍在飞行中的刷新就能把它改回 running——**取消被静默撤销**，若远端上传随后成功，item 会变成 succeeded。
2. **`finishTask` 把"还没结束"当成"带着失败结束"**（`upload_stream_task.go:1023-1062`；下载侧同构实现在 `download_stream_task.go:480-520`）。成功分支要求 `itemsDone == len(items) && itemsFailed == 0`，其余一律走失败分支；而失败分支用 `itemsFailed < len(items)` 判定 partial_failed。当 item 仍在运行（`itemsDone < len(items)` 且 `itemsFailed == 0`）时，`0 < 1` 成立，于是**为一个尚未结束的任务发布终态 partial_failed**，同时 `Progress.ItemsDone/ItemsFailed` 保持 0/0。

缺陷 1 是根因（它让 item 在终态之后还能变成 running），缺陷 2 把根因变成"任务已终态而 item 还在跑"的矛盾快照。只修 2 会留下更坏的行为——用户取消的 item 仍会被复活并可能成功；只修 1 则矛盾快照在别的时序下仍可能出现（例如 runner 退出时仍有在飞 item）。

现在修的理由：这是我上一轮诊断出、量化过复现率的缺陷；它同时污染三个已存在的用例，使 `scripts/ci-check.sh` 门禁每 4 次跑就有约 1 次红，掩盖真实回归。

## What Changes

- **终态 item 不再被远端观察改写**：`applyRemoteUploadState` 在 item 已处于终态（succeeded / failed / canceled）时只更新远端观察字段（CloudTaskID、CloudState、CloudWritten、CloudTotal、CloudPhase、RemoteID），不得改写 `State`、`Error` 或可用操作。
- **终态任务必须由终态 item 组成**：两个 `finishTask`（上传与下载）在发布结果前，若仍有 item 处于非终态，则先把这些 item 标记为 failed 并给出明确原因（runner 已退出、该 item 不可能再推进），再按真实终态决定 success / partial_failed / failed。这样"任务终态 ⇒ 同一快照内所有 item 终态"成为结构性保证，也避免任务永久停在 running。
- **补齐可执行的契约**：为上述两条各加一个断言（终态 item 不被刷新改写；终态任务的同快照 item 全部终态），并把三个既有用例保留为集成层面的守护。
- **不改**：任务状态机本身（终态集合、partial_failed 语义）、`closeDoneIfTerminalLocked` 的判定、重试与恢复行为、任何 wire 字段与 App 契约。

## Capabilities

### New Capabilities

无。

### Modified Capabilities

- `task-api`: 新增两条明确契约（既有"单一权威状态模型"要求的具体化）：任务项进入终态后，远端状态观察不得改写其状态或可用操作；任务处于终态时，同一快照内的所有任务项必须处于终态。

## Impact

- `pkg/core/upload_stream_task.go`：`applyRemoteUploadState`（新增终态短路）、`finishTask`（发布前收敛在飞 item）。
- `pkg/core/download_stream_task.go`：`finishTask` 同样的收敛（该族没有远端状态回写，故只有第二处缺陷）。
- 测试：`pkg/core` 新增定向用例——终态 item 经远端刷新后保持不变；`finishTask` 在存在在飞 item 时不发布"终态 + 非终态 item"的组合。既有 `TestUploadStreamTaskCancelItemRemovesStaging`、`TestCommitCompleteStagingWithoutReopeningSource`、`TestCoreRecoversCompleteMutableStagingWithoutSource` 作为集成守护保留不动。
- 行为变化：取消的 item 不会再被复活（此前可能被复活并上传成功）；runner 退出时仍在飞的 item 从此前"被视为成功/未知"变为显式 failed 并带原因。
- 无配置项、无 wire 字段、无 CLI 改动。
