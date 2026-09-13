## Why

上传前的 hash 阶段存在重复读取，加密挂载尤甚：

1. `encryptedReadOnlyFile`（`pkg/crypt/encrypt_source.go`）没有块缓存：`readAtNonEmpty` 每次 Read/ReadAt 都把所在的整个 64 KiB 明文块重新读出并重新加密，只返回调用方要的窗口。哈希阶段的 `io.Copy`（`pkg/crypt/drive_wrapper.go:467`、`pkg/drivers/quark/quark_upload.go:338`）与 HTTP 请求体都按 ~32 KiB 读取，跨块边界时一次读触发两个整块。实测（1 MiB 源）顺序读放大：4 KiB → 17×、32 KiB → 3×、64 KiB → 2×；crypt+quark 端到端哈希阶段读 3×、上传阶段再读 3×，合计 6×。1 GiB 文件 = 约 6 GiB 本地 staging 读 + 约 6 倍 EME 加密 CPU。
2. `stageExistingWithDeps`（`pkg/vfs/staging.go`）把远端已有内容复制进 staging 时不计算 hash，随后 `snapshotPending`（`pkg/vfs/upload.go:157`）因为 `pending.SourceHashes` 为空、HashTracker 无记录而整读 staging 只为算 hash。修改一个已存在文件（写入已存在的远端文件）因此多一遍全量本地读。

## What Changes

- **加密源单块记忆**：`encryptedReadOnlyFile` 缓存最近一次加密出的块（块索引 + 密文块），同一块的后续读直接命中；跨块读只重新读取真正越界的那一块。缓存以 mutex 保护，避免同一句柄被并发读时引入数据竞争。输出字节不变。
- **staging 复制顺带哈希**：staging 的两处"把已有内容复制进新 staging 文件"都改为在复制的同时按 `requiredUploadSnapshotHashes(v.driver)` 顺序喂 `HashTracker`，使 `Flush` 时 `pending.SourceHashes` 直接可用，不再在快照阶段整读 staging：
  - `stageExistingWithDeps`（远端已有文件 → staging）；
  - `rotateFrozenGenerationWithStore`（frozen 代际 → 新代际，`upload.CopyStagingContent` 增加分块回调）。
  乱序写 / `Truncate` 仍由 tracker 的 dirty 语义回退到整读，哈希正确性不变；哈希只进内存 tracker，不落盘，因此崩溃恢复后仍是"无 hash → 整读"的安全路径。
- **回归测试**：
  - 加密源顺序读的放大上限（4 KiB / 32 KiB / 64 KiB 读块下 ≤1.1×），并保留现有 `TestEncryptedReadOnlyFileSourceMatchesEncryptingReader` 的逐字节等价断言；
  - `stageExisting` 与 frozen 代际轮转之后 `Flush` 都带出可用的 `pending.SourceHashes`（与直接对 staging 求 hash 一致），以及乱序写后仍回退整读的负向断言。

非目标：

- 不改 quark「源无 hash 时整份扫描」的协议行为，**已用真机验证否定**：省略 `/file/update/hash` 会让 `/file/upload/finish` 返回 `400 / code 43001 request cpp error[complete file failed!]`；同一份内容在提交 hash 的对照路径下上传、回读、删除全部成功。该扫描因此是强制的，`quark_upload.go` 里留了这条证据注释。真正的替代方案是把 hash 挪到分片上传之后、finish 之前提交（相当于边上传边算 hash），这样能省掉上传前那一遍读取，但它只对"随机 nonce、秒传必然 miss"的挂载有意义，且需要再验证 provider 是否接受这种调用顺序，另立变更再做。
- 不改 `Truncate` 在无 pending 时先整份下载远端文件的既有语义。
- 不引入跨进程的 hash 缓存；不改变任何 wire/持久化字段。

## Capabilities

### New Capabilities

无。

### Modified Capabilities

无。读取次数与 CPU 是内部实现细节，可观察契约（上传字节、hash 值、Entry、失败语义）不变，因此 `.openspec.yaml` 声明 `skip_specs: true`。

## Impact

- `pkg/crypt/encrypt_source.go`（块缓存）、`pkg/crypt/encrypt_source_test.go`（放大回归 + 乱序读等价）。
- `pkg/vfs/staging.go`（stageExisting 与代际轮转喂 tracker）、`pkg/vfs/upload/stagingfile.go`（`CopyStagingContent` 分块回调 + 导出 `CopyStagingStream`/`StagingCopyChunkSize`）、`pkg/vfs/staging_hash_test.go`（三处回归）。
- 内存：每个打开的加密源句柄多 1 个块（≤64 KiB + 16 B）；无新增 goroutine。
- 行为等价：相同上传字节、相同 hash 值；复制路径不再走内核 copy_file_range 快路径（这是边复制边哈希的必然代价），读取次数与加密次数显著下降。
