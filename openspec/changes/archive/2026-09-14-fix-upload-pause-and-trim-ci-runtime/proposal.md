## Why

两件事被同一条线索串起来：本地 CI 关卡太慢，而想让关卡变快的每一种并发尝试，都被测试套件里几个"等待型"缺陷挡了回来。

**关卡耗时**：本地 `scripts/ci-check.sh` 端到端 150.1s，其中三个测试层占 88%。这些层是定时器驱动的而不是 CPU 驱动的——`pkg/vfs` 有 283 个测试、零个 `t.Parallel()`，21s 墙钟只烧 0.9s CPU；20 个用例（6.6%）吃掉 67% 的时间。也就是说，耗时几乎全在等待上。

**顺带查出的真实缺陷**：为定位"为什么并发跑就红"而追查 `pkg/core` 的 flake 时，发现 `UploadStreamItemHandle.Fail()` 把 item 置为 `waiting_input` 并写入一个可重试错误，但下一个进度节拍（生产 500ms、测试 5ms）在 `applyRemoteUploadState` 里会把它改回 `running` 并清掉错误。后果不是"测试偶尔红"，而是**契约被破坏**：`CommitStagedUploadItem` 要求 `item.State == StateWaitingInput`，于是客户端走文档化的"中断 → 提交已暂存字节"恢复路径时会拿到 `core: upload stream item "x" is running`。两个守卫用例（`TestUploadStreamItemFailWaitsForReopen`、`TestCommitCompleteStagingWithoutReopeningSource`）此前只是**赢下了那个约 5ms 的窗口**才偶尔通过（隔离跑 20 次 0 失败，在 `race` 层的包级并发下约 1/7 失败），并没有真正验证契约。

**同一次追查还暴露了一批潜在 flake**，它们同样只在负载下显形：检查夹具假设"两次写入落在同一秒"（`copyTree` 不保留 mtime，`TestFsCheckDetectsTypeConflict` 手工镜像文件），两处"等一个投影、断言另一个投影"（`pkg/core` 的上传 item 与诊断 Detail），以及 `ScanResidual` 最坏 63s 且**完全不响应 ctx 取消**的退避。

## What Changes

### 1. 生产契约修复（这是本变更的实质部分）

- `pkg/core/upload_stream_task.go`：新增 `AwaitingReopen` 意图标记。`Fail` 置位，`applyRemoteUploadState` 遇到它就只刷新诊断字段、不碰 `State`/`Error`，与文件里既有的"终端 item 永不重写"原则同形并附同一条理由（ticker 与调用方并发，调用方意图权威）。`Write`/`Commit`/`OpenUploadStreamItem`（重开）清除它。`Close` 故意不置位——它同时是 mobile 的 teardown 路径（`closeCollectedHandles`），把关闭当成"等待重开"会让被关闭的 item 永久挂住。
- 两个守卫用例改为在 `Fail` 之后**等过多个轮询周期再断言**，成为确定性的回归测试。

### 2. 可注入的 seam（生产默认值一律不变）

统一采用仓库既有形态（对标 `UploadStreamTaskPollInterval`、`journalFail`/`compactFail`）：

- `pkg/vfs/upload/store.go`：journal 压缩水位从常量改为 `PendingStore` 字段（构造时取默认值）；新增 `skipJournalSync`（**零值即生产行为**，所以漏设也不会削弱持久性）与 `syncJournalFile` 辅助方法，三个 journal 写入点统一走它。`PruneUploadJournal` 是包级函数无 receiver，保持始终 fsync 并注明原因。
- `pkg/contracttest/fixture.go`：`Fixture` 加未导出 `convergenceStep`（默认 1s），`VerifyList`/`ScanResidual` 从它起步退避，并改用响应 ctx 的等待。
- `pkg/drive/contract_behavior.go`：导出 `BehaviorConvergenceStep`（默认 1s），两个行为检查的退避基于它，同样响应 ctx。
- `pkg/drivers/onedrive`：`oneDriveRetryWait`（默认 `util.WaitExponential`）统一两处重试等待。

### 3. 等待型与脆弱夹具的修正

- 防抖/退避按不变量收紧：两个 `pkg/vfs` 夹具 1s → 200ms（约束只是"断言发生时定时器尚未触发"，而共享夹具注释记录过 10ms 在慢速 `-race` 上会竞速，故取该值的 20 倍）；quark 限速用例 deadline 1s → 500ms（保持 `body/limit ≫ deadline ≫ auth RTT` 的比例关系）。
- 去掉真实缺陷级 flake：`copyTree` 与 type-conflict 夹具改为携带源 mtime；`pkg/core/task_persistence_test.go` 轮询它真正断言的诊断字段；debug 夹具补上 `upload_delay`/`delete_delay`。
- 并行化独立工作：crypt 互操作三个子测试 `t.Parallel()`（各持自己的 vault/config，deadline 必须建在子测试内，否则父测试返回时的 `cancel` 会掐掉仍在跑的子测试）、`pkg/control` 三个 spec 请求并行（每个 spec 各有随机命名的 fixture 目录，互不共享路径）。

### 4. 脚本与 CI 工作流

- `scripts/test-layers.sh`：vfs-stability 从串行 `-count=3` 改为 3 个并发的 `-count=1` 进程（每测试自带 `t.TempDir()`，无共享状态）；失败时只回放失败测试自己的输出。
- `scripts/coverage.sh`：六个 profile 扇出执行；失败时打印该包日志；`-print` 模式下"测试跑不起来"仍非零退出。
- `scripts/install-ci-tools.sh`：三个工具并发安装，versions 标记仅在全部成功后写入（失败即下次重试）。
- `ci.yaml` / `nightly.yaml`：FUSE 头文件仅在 `pkg-config --exists fuse` 失败时才 `apt-get`。
- `scripts/ci-check.sh`：行为不变（各层串行），补注释说明为何不做跨层并发，以及哪些抢占式重叠是安全的。

非目标：

- **不改变任何生产默认值**。本变更新增的四个 seam 全部默认等于既有行为；`skipJournalSync` 的零值就是"照常 fsync"。
- **不做四个检查组的跨层并发**。实测空闲机器 87s 对 126s、繁忙机器 123s 对 126s，但五次尝试冒出五个不同的负载敏感用例（本变更修掉了其中三个），因此不发布这条路径；`ci-check.sh` 保持串行。
- **不动 Windows job**（`Test (windows)` 约 135s，是 CI 墙钟的关键路径），本变更对 CI 端到端总时长的影响因此有限。
- 不改 `util.ExponentialBackoff` 的全局 base（`quark_read` 等也在用）。

## Capabilities

### New Capabilities

无。

### Modified Capabilities

- `mobile-upload-task-observer`：`Resume actions are selected from item capabilities` 一条强化——调用方请求的暂停（失败的输入，等待客户端重开或提交）在调用方动作之前 MUST 保持权威，后台进度观测 MUST NOT 清除它，也 MUST NOT 收回它隐含的 `commit_input`/`open_input` 能力。

## Impact

- 生产代码 3 处：`pkg/core/upload_stream_task.go`（契约修复）、`pkg/vfs/upload/store.go`、`pkg/drivers/onedrive/{driver,onedrive_auth,onedrive_copy}.go`。
- 测试与脚本 26 个文件；`openspec/changes/archive/2026-09-14-…` 之外无契约变化。
- 实测结果：
  - 本地 `scripts/ci-check.sh`：**150.1s → 107–122s**（连续多次绿灯，区间随机器环境负载浮动）。
  - ≥1s 的用例：**10 → 2**；最慢用例 **3.03s → 1.22s**。
  - 包级：`pkg/vfs/upload` 6.0 → 2.0s、`pkg/vfs` 17 → 13.5s、`pkg/crypt` 6.2 → 4.8s、`pkg/drivers/onedrive` → 0.38s、`pkg/drivers/quark` → 1.45s、`internal/cli` 6–12 → 4.1s、`pkg/contracttest` 5.5 → 1.9s、`pkg/drive` 4.7 → 0.92s、`pkg/control` → 1.16s。
  - coverage 关卡：冷缓存 50–57s → 19–24s（12 轮冷缓存全绿）。
  - 剩余 2 个 ≥1s：`TestPendingConcurrentWithList`（1.22s，同属 fsync 家族，但在 `package vfs` 够不到 `upload` 的未导出字段，提速需导出测试钩子或减少迭代，本次未做）；`TestRcloneInteropFilenames`（1.08s，单次真实 rclone 调用，无 rclone 的机器上直接 skip）。
- 负向验证（每条修复都做了确定性对照）：`skipJournalSync` 翻回 false → 1.31s 对 0.09s；去掉 onedrive seam → 1.23s 对 0.03s；去掉 `AwaitingReopen` 守卫 → 两个用例以 `item state after the pause = running` 失败；注入 1.2s 跨秒探针 → 三个 mtime 夹具在修复前全失败；去掉 ctx 感知 → 新守卫用例 3.00s 失败。
