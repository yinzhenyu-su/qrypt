## 1. 代际领取与原地复用（pkg/vfs/upload）

- [x] 1.1 `vfstypes.PendingUpload` 增加 `UploadStarted bool`（`json:"-"`，仅内存），字段注释写清为什么不持久化（崩溃后不存在在跑的上传，读回"未领取"仍安全）以及为什么不参与 `SameUploadRecord`（重排队/重试的同一代际必须仍被识别为同一代际）
- [x] 1.2 `PendingStore.MarkUploadStarted(p) (PendingUpload, bool)`：txMu+mu 下校验记录未变（`sameUploadRecord`）后置位；记录已变则返回 false。验证：`TestMarkUploadStartedRejectsReusedGeneration`（先复用后领取必须失败）、`TestMarkUploadStartedKeepsGenerationIdentity`（重复领取幂等且不改变同一代际判定）
- [x] 1.3 `PendingStore.TryReuseFrozenStaging(p) (PendingUpload, bool)`：txMu+mu 下要求 FID/LocalPath 匹配、当前记录仍 `Frozen` 且 `!UploadStarted`，成功则清 `Frozen`、清 `SourceHashes`、bump `UpdatedAt`（让排队中的旧记录在 worker 的 `SameUploadRecord` 检查处被丢弃）。验证：`TestTryReuseFrozenStagingRevivesUnclaimedGeneration` / `TestTryReuseFrozenStagingRejectsClaimedGeneration`
- [x] 1.4 `Store` 接口与 `StoreAdapter` 增加 `MarkUploadStarted` 转发
- [x] 1.5 `Engine.Execute` 在读任何字节之前领取；领取失败时按既有 supersede 语义 `RemoveStagingIfUnreferenced` + `RequeueIfFrozen(latest)`，不读文件。验证：既有 `TestVFSUploadUsesStableSnapshotWhenFileChangesDuringUpload`、`TestVFSWriteAfterFlushPreservesStagedContent`、`TestVFSUploadDoesNotClearNewerPending` 全绿

## 2. 写路径（pkg/vfs）

- [x] 2.1 `WriteAt` / `Truncate` 的 frozen 分支改为"先尝试原地复用，失败再 `rotateFrozenGeneration`"（新增 `writeTarget`），复用命中打 debug 日志
- [x] 2.2 复用后 hash 行为不变：顺序追加继续走增量 tracker（`TestWriteAfterFlushReusesStagingGeneration` 断言 `SourceHashes` 覆盖全部算法且与直接对 staging 求 hash 一致），`Truncate` 仍 `hashes.Dirty` 回退整读

## 3. 测试

- [x] 3.1 `TestWriteAfterFlushReusesStagingGeneration`：写 → Flush → 再写，断言 FID/LocalPath 不变、`Frozen` 清除、staging 内容为两段拼接、staging 目录只剩一个文件、第二次 Flush 的 `SourceHashes` 与整读一致
- [x] 3.2 `TestRotateFrozenGenerationFeedsHashes` 与 `TestFlushRetiresGenerationDisplacedBeforeTimerFire` 改为先 `MarkUploadStarted` 再写，仍断言轮转发生、旧代际字节保留、被替换代际的 staging 立即回收
- [x] 3.3 `pkg/vfs/upload`：领取/复用的互斥顺序、`SourceHashes` 清除、`UpdatedAt` 推进、以及标记不改变 `SameUploadRecord` 判定
- [x] 3.4 量化对比（同一临时脚本，16 KiB 分块、`UploadDelay=1h` 隔离上传，测完删除）：16 轮 write+flush `8.50× → 1.00×`、64 轮 `32.50× → 1.00×`、代际数 `16/64 → 1`，写完再 flush 的对照仍是 1.00×。负向验证：把 `TryReuseFrozenStaging` 改成恒 false 后 `TestWriteAfterFlushReusesStagingGeneration` / `TestTruncateAfterFlushReusesStagingGeneration` / `TestTryReuseFrozenStagingRevivesUnclaimedGeneration` / `TestMarkUploadStartedRejectsReusedGeneration` 全部按预期失败（报"rotated it"/"not reusable"）
- [x] 3.5 端到端：`TestReusedGenerationUploadsCombinedContent` 在复用的代际上跑一次真实上传，远端内容为两次写入的拼接

## 4. 验证

- [x] 4.1 `gofmt -l .` 无输出、`go vet ./...` 通过
- [x] 4.2 `scripts/ci-check.sh` 全绿（vet / staticcheck / golangci-lint / arch / vulncheck / format / docs in sync / fast 45s / race 58s / vfs-stability 61s / localfs smoke 8s）
- [x] 4.3 `openspec validate --specs` 通过（5 passed / 0 failed），`openspec status --change` 显示 proposal/tasks done、specs skipped（`skip_specs: true`）、design 未写（与既有两次变更一致）
