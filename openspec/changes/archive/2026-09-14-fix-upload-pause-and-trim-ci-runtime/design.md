## Context

见 proposal.md - Why。设计相关的现状约束：

- `applyRemoteUploadState`（`pkg/core/upload_stream_task.go`）由进度 ticker 驱动（生产 500ms，测试里被改成 5ms），按云端任务状态回写 item。
- 已归档的 `2026-09-12-fix-stream-task-terminal-consistency` 在同一函数入口加了"终态 item 短路"，理由写在函数上方注释里：**"调用方的 cancel 是权威意图，而驱动这次调用的 ticker 与它并发"**。那条原则覆盖了终态，但没覆盖"调用方请求的暂停"——`Fail` 置位的是 `waiting_input`（非终态），于是仍会被 ticker 改回 `running` 并清掉错误。
- `CommitStagedUploadItem` 要求 `item.State == StateWaitingInput`，因此上述改写不是"状态显示不符"，而是**能力被收回**：客户端走"中断 → 提交已暂存字节"会拿到 `core: upload stream item "x" is running`。能力口径另见 `mobile-upload-task-observer` 的 `Resume actions are selected from item capabilities`。
- 两个守卫用例此前靠赢下约 5ms 的窗口偶尔通过（隔离 20 次 0 失败，`race` 层包级并发下约 1/7 失败），所以它们既没真正验证契约，又让门禁看起来在随机变红。

## Goals / Non-Goals

**Goals:**

- 调用方请求的暂停在调用方动作之前保持权威；ticker 不得清除它、不得因此收回能力。
- 让两个既有守卫用例变成确定性回归测试（修复前必然失败）。
- 把"为何慢"拆到每层、每个热点用例上，按"不削弱测试目标"的原则收紧；所有生产改动以 seam 形式引入且默认值不变。
- 记录被撤回的方案，使后继者不必重新踩一遍。

**Non-Goals:**

- 不改任务状态机、不改 `waiting_input` 的语义、不改 wire 字段与 persisted 快照格式。
- 不改 `Close` 的语义（见决策 1）。
- 不做四个检查组的跨层并发（见决策 6）。
- 不改任何生产默认值。

## Decisions

### 1. 守卫加在 `applyRemoteUploadState`，`AwaitingReopen` 只由 `Fail` 置位

与已归档的终态短路同形：在 `isTerminalStreamItem` 判断之后加一个 `item.AwaitingReopen` 判断，只刷新诊断字段。替代方案是在 `StateFailed && Retryable` 与 `default` 两个分支各加判断——同前一份 design 里的理由，那会漏掉未来新增的分支，而"调用方意图不可被并发观测改写"是关于整个函数的性质。

`Close` **故意不置位**，尽管它也会把 `running` 移入 `waiting_input`，看起来是同一状态。原因：`Close` 同时是 teardown 路径（`pkg/mobile/mobile.go` 的 `closeCollectedHandles` 在关停时逐个关闭收集到的 handle），把关闭当作"等待重开"会让被关闭的 item 永久挂住，任务永不收敛。语义重叠处优先保证关停可收敛。

清除点选了三个"调用方把 item 收回"的动作：`Write`（继续喂数据）、`Commit`（收尾）、`OpenUploadStreamItem`（重开）。它们都在持有 `batch.mu` 时写入，与 ticker 的读取串行化。

不持久化该标记是**有意**的：它是进程内的调用方意图，调用方消失后没有任何主体能把暂停解除。进程重启后按持久化的 `waiting_input` 状态恢复时，标记自然为 false，即"没有挂起的暂停"——这是本次明确划出的边界，若将来要求跨重启保持暂停，需要连同主体与超时一起设计。

### 2. 一律加 seam，而不是减少迭代次数或弱化注入的失败

六处等待（journal fsync、两处退避、两处防抖、一处限速 deadline）统一用"可注入的 pace/水位"处理，理由是同一条：这些等待的**次数与断言**是测试的价值所在，而**时长**不是。仓库已有此形态（`UploadStreamTaskPollInterval`、`journalFail`/`compactFail`），本次只是补齐缺口。

明确否决的替代方案：

- **减少迭代次数**（如 100 → 20 次并发 Save/Remove）：省 80%，但交错数量随之减少，`-race` 的检出能力被削弱。只有一个例外被保留（`TestPendingConcurrentWithList` 未动，见决策 7）。
- **把注入的可重试 5xx 改成 4xx** 让驱动立即失败：省掉退避，但不再覆盖"重试耗尽后才失败"这条更难的路径。
- **改 `util.ExponentialBackoff` 的全局 base**：会影响 `quark_read` 等无关调用点。

### 3. `skipJournalSync` 用"零值即生产行为"的极性

字段命名刻意选 `skipJournalSync` 而不是 `syncJournal`：后者零值为 false 会让一个未初始化的 store 静默失去持久性保证，而前者零值就是"照常 fsync"。`NewPendingStore` 是唯一构造点、且不设置它，所以生产的持久性语义只能由代码显式改变。

安全性的依据（也是敢把它开在并发测试里的原因）：`f.Sync()` 只影响**机器崩溃**后的持久性，不影响同进程内随后打开该文件的读取。这些测试断言的是锁与交错，不涉及崩溃恢复，因此跳过 fsync 不改变任何可观察结果——这一点已用"翻回 false → 1.31s 对 0.09s"的对照确认。

`PruneUploadJournal` 保持始终 fsync：它是包级函数、没有 receiver 可读该标记，且只在启动时执行一次，不在热点上。

### 4. 防抖夹具取 200ms，而不是我最初按"微秒级间隙"推导出的更小值

我起初的推理是：断言距 `Flush` 只有两次进程内调用，间隙是微秒级，因此可以把 1s 压到很小。写改动时读到共享夹具的既有注释，记录着**前人试过 10ms，在慢速 `-race` 机器上会竞速失败**——说明真实间隙是一次 goroutine 调度延迟，而非微秒。

据此改取 200ms：是该失败值的 20 倍，同时把两处等待从 1s 降到 0.2s。验证方式是 30 轮 `-race` 重复（`-count=10` × 3）加 2 轮三路并发的 vfs-stability，全绿。这是本次唯一带残余时序敏感度的改动，值得后继者在 CI 上留意。

### 5. quark 限速用例保持比例、缩短绝对值

该用例的注释已写明约束：body 必须大到"限速下必然跨过 deadline"，而 deadline 必须明显高于 auth/TLS 往返（曾经因为 body 太小与墙钟竞速）。64KiB @ 1B/s = 18 小时，所以 deadline 只需压住 auth 往返即可，1s → 500ms 保持 `body/limit ≫ deadline ≫ auth RTT`。没有改成"断言有界进展"，因为那要重写断言并需要中途停读，收益（再省 0.3s）不值得增加这份复杂度。

### 6. 撤回四个检查组的跨层并发，并记录其代价

实测：空闲机器上 87s 对 126s，繁忙机器上（8 核 load 26）123s 对 126s——收益完全依赖空闲核心，而开发机通常没有。更关键的是它**顺序暴露出五个负载敏感用例**（本次修掉其中三个：上传暂停契约、mtime 夹具两处、诊断投影；另一个是人写镜像的 mtime）：

1. `pkg/core` 上传暂停契约（真实产品缺陷，本次修复）
2. `TestFsCheckIdenticalTrees`（mtime 夹具）
3. `TestFsCheckCompareMtimeOnlyDetectsMtimeChange`（mtime 夹具）
4. `TestFsCheckDetectsTypeConflict`（手工镜像 mtime）
5. `TestCoreRecoversCompleteMutableStagingWithoutSource`（诊断投影滞后）

第 4 次尝试时它连绿 4 轮，第 5 次又红了。结论：该套件没有被并发加固过，一个间歇变红、且每次红在不同用例上的开关比一个慢但确定的关卡更糟。因此 `ci-check.sh` 保持串行，并补注释说明——重叠只保留在"本质在等待"的两处（vfs-stability 的重复、coverage 的 profile 扇出），它们不依赖空闲核心。

### 7. 两个未解决的 ≥1s 用例如实留档

- `TestPendingConcurrentWithList`（1.22s）：同属 journal fsync 家族，但它在 `package vfs`，而 `skipJournalSync` 是 `pkg/vfs/upload` 的未导出字段——跨包够不到。两个可选出路（导出测试钩子 / 减少迭代次数）都各有代价，本次不单方面决定。
- `TestRcloneInteropFilenames`（1.08s）：单次真实 rclone 调用，已是最少进程数（11 个文件名一次 copy），且在未安装 rclone 的机器上 `t.Skip`。

另记一个仍未取用的优化点：`TestRcloneInteropQryptToRcloneData` 的每个子测试内部对每个 size 各起一次 rclone（6 次/子测试），改用一次 `rclone copy` 整目录解密可再省约一半，但需要重写校验方式。

## Risks / Trade-offs

- **`AwaitingReopen` 不持久化**：进程重启后挂起的暂停不被保持（见决策 1）。这是本次明确划出的边界。
- **200ms 防抖**：余量是 10ms 失败前例的 20 倍，但仍是本次唯一带时序敏感度的改动（见决策 4）。
- **`skipJournalSync` 触及持久化路径**：靠"零值即生产行为"的极性 + 唯一构造点不设置来约束；任何把该标记接到配置或生产入口的改动都必须在 review 中说明理由。
- **新增的 seam 是包级可变状态**：`pkg/drive`、`pkg/contracttest`、`pkg/drivers/onedrive` 三处测试通过替换它来提速。已确认这些包的测试没有 `t.Parallel()`，且都通过 `t.Cleanup` 还原；`BehaviorConvergenceStep` 因测试在 `package drive_test`（外部包）而必须导出，形态对齐 `UploadStreamTaskPollInterval`。
