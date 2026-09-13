## Why

`Flush` 无条件把 pending 标成 `Frozen`（`pkg/vfs/staging.go:176`），之后任何一个落在同一路径上的写都会走 `rotateFrozenGeneration`，把整个 staging 文件复制成一个新代际（`staging.go:91` 的 `CopyStagingContent`）。复制的唯一目的是让"已开始读取 staging 的上传"看到不可变字节；但在上传尚未开始读取时复制是纯浪费——被复制的旧代际往往在上传真正开始前就被替换掉并删除。

触发面很宽，因为 mount 层把 `Fsync` 直接当作 `Flush`（`pkg/mount/adapter_fuse_file.go:190`）：FUSE 在每次 `close(2)` 可写 fd 时调 Flush，写大文件途中周期 `fsync()` 的程序同样会冻结代际。

实测（真实 VFS + localfs，16 KiB 分块追加，每轮写后 flush，`UploadDelay` 设为 1h 以隔离上传）：

| 负载 | 逻辑字节 | staging 实际写入 | 放大 |
|---|---|---|---|
| 16 轮 write+flush | 256 KiB | 2.23 MiB | 8.50×（16 代际） |
| 64 轮 write+flush | 1 MiB | 34.1 MiB | 32.50×（64 代际） |
| 16 轮写完后单次 flush | 256 KiB | 256 KiB | 1.00×（1 代际） |

前两行精确等于 (K+1)/2（K = flush 次数），即总写入量 O(K²)。磁盘占用不放大（旧代际在上传被替换后删除），放大的全是写带宽与 IOPS。

## What Changes

- **代际领取（claim）**：`PendingUpload` 增加仅存在于内存的 `UploadStarted` 标记（`json:"-"`，不进 journal、不跨重启），表示"上传已经开始读取这一代际的 staging 文件"。
- **上传侧先领取再读**：`Engine.Execute` 在读任何字节之前（`freezeSnapshot` 之前）把记录标成已领取；记录在等待期间被改写（写路径原地复用、代际轮转、重命名等）时放弃本次上传并按既有 supersede 语义处理。
- **写侧原地复用**：`WriteAt` / `Truncate` 遇到 frozen 代际时，若该代际尚未被领取，则原地解冻继续写（清 `Frozen`、清 `SourceHashes`、清除的 `SourceHashes` 由下一次 Flush 用增量 tracker 重新计算），不再复制整个文件；已被领取才轮转，行为与现在完全一致。
- **两侧在同一把锁上定序**：领取与复用都走 `PendingStore` 的 `txMu`+`mu`，因此"领取成功"与"复用成功"互斥：
  - 写路径先复用 → 记录被改写（`Frozen=false`、`UpdatedAt` 变化）→ 上传侧领取失败 → 本次不读文件、不上传，内容由下一次 Flush 发布；
  - 上传侧先领取 → 写路径复用失败 → 走原有轮转复制。
  两条路径都不会出现"上传正在读、写路径同时改同一个文件"。
- **回归测试**：
  - 写后 Flush 再写：断言仍是同一个 FID/LocalPath（未复制）、staging 目录字节数不增长、最终内容与 hash 正确；
  - 领取后写：断言发生轮转、旧代际字节不变、新代际内容与 hash 正确（把现有 `TestRotateFrozenGenerationFeedsHashes` 改为先领取再写）；
  - store 层语义：`TryReuseFrozenStaging` 与 `MarkUploadStarted` 的先后顺序互斥，且 `UploadStarted` 不参与 `SameUploadRecord`（重排队/重试的同一代际仍被识别为同一代际）。

非目标：

- **不改 `Fsync → Flush` 的语义**。当前 fsync 会冻结代际并发布一次上传；改成"fsync 只做本地持久化"能省掉更多复制，但它改变的是可观察行为（远端发布时机、编辑器的 write+fsync+rename 序列），风险高于收益，留待单独评估。
- 不改 `stagingStore.writeAt` 每次写 `openat/pwrite/close` 的 syscall 开销（非字节放大，且需要按代际持有文件句柄，生命周期管理风险更高）。
- 不改 `stageExisting`（首次写已存在远端文件时整份下载进 staging）——那是全量上传架构的固有读-改-写代价，不是这次要消除的重复写。
- 不改变取消/失败重试语义：领取标记不清除，失败后的重试仍走"已领取"分支，即与今天完全相同的轮转行为（保守但不会退化）。

## Capabilities

### New Capabilities

无。

### Modified Capabilities

无。staging 写入字节数是内部实现细节；可观察契约（上传字节、hash 值、Entry、失败/重试语义、磁盘占用）不变，因此 `.openspec.yaml` 声明 `skip_specs: true`。

## Impact

- `pkg/vfs/vfstypes/vfstypes.go`（`UploadStarted` 内存字段）、`pkg/vfs/upload/store.go`（`MarkUploadStarted` / `TryReuseFrozenStaging` / adapter 转发）、`pkg/vfs/upload/interfaces.go`（`Store` 接口新增领取方法）、`pkg/vfs/upload/engine.go`（Execute 起始处领取）、`pkg/vfs/staging.go`（写路径复用优先于轮转）。
- 收益：写后 flush 的常见模式从每轮一次全量复制降为 0 次（上传未启动时），只在"上传确实在跑 + 又发生写"时保留一次复制（这是不可变快照的必要成本）。上面 64 轮用例的 32.5× 降到 1.00×。
- 行为差异：写后 flush 再写时，内容的落地文件从"新代际文件"变成"同一个 staging 文件继续追加"，发布时机不变——两种情况下的当前代际都是 mutable，都要等下一次 Flush（close 必然发生）才入队；旧行为还会多创建一个文件并让被取代的调度任务删掉旧文件，也就是每次都要付一次全量复制加一对 create/remove。写后 fsync 的交替模式不受影响，因为每次 fsync 都会重新冻结并重新发布。
- 崩溃恢复：`UploadStarted` 只存内存，重启后记录读回必然是"未领取"，而崩溃后不存在在跑的上传，因此复用判定仍安全；staging 文件里多出来的字节会被重新冻结并整份上传，内容正确。
