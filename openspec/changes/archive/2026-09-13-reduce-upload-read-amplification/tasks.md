## 1. 加密源单块记忆（pkg/crypt）

- [x] 1.1 `encryptedReadOnlyFile` 增加"最近块"缓存（块索引 + 密文字节 + 有效位），`readAtNonEmpty` 命中直接切片返回，未命中才 `readFullAt` + `EncryptBlock` 并覆盖缓存；缓存字段用 mutex 保护。验证：`go test ./pkg/crypt/ -run TestEncryptedReadOnlyFileSource` 通过（逐字节等价断言仍成立）
- [x] 1.2 新增放大回归测试：262 KB 源（4 个块 + 部分尾块）+ 计数源，4 KiB / 32 KiB / 64 KiB 读块顺序读完整个加密流，断言底层明文读取 ≤ 1.1× + 一个跨界块（修复前 17× / 3× / 2×；fixture 刻意取小，避免给 race 层加负载）。验证：临时把缓存命中分支改成恒 false，测试以 `read 4456571 plaintext bytes for a 262267 byte source` 失败
- [x] 1.3 覆盖越界与乱序读：`ReadAt` 跨块、回退到更早块、末尾部分块、200 次随机读，断言字节与 `EncryptingReader` 一致且缓存不会返回陈旧内容

## 2. staging 复制顺带哈希（pkg/vfs）

- [x] 2.1 `stageExistingWithDeps` 在复制远端内容进 staging 时按顺序喂 `HashTracker`（算法取 `requiredUploadSnapshotHashes(v.driver)`），offset 与实际写入字节对齐；复制失败时照旧 drop staging。验证：`go test ./pkg/vfs/ -run TestStageExisting` 全绿
- [x] 2.2 新增测试：staging 一个已存在远端文件后 `Flush`，断言 `pending.SourceHashes` 覆盖所需算法且与直接对 staging 求 hash 一致，快照返回同一组 hash 且非 incremental。验证：把复制改回 `io.Copy` 后测试以 `pending source hashes incomplete after staging: map[]` 失败
- [x] 2.3 负向测试：stageExisting 后对 offset 0 原地改写 → `Flush` 不保留增量 hash（tracker dirty），快照回退整读并给出改动后内容的 hash（与改动前内容不同）
- [x] 2.4 frozen 代际轮转同样顺带哈希：`upload.CopyStagingContent` 增加分块回调（新增 `CopyStagingStream`/`StagingCopyChunkSize`），`rotateFrozenGenerationWithStore` 用 `stagingHashFeed` 喂 tracker；验证：新增 `TestRotateFrozenGenerationFeedsHashes` 通过（对 frozen pending 写入触发轮转后 Flush 带出完整 `SourceHashes`）

## 3. 验证

- [x] 3.1 `gofmt -l .` 无输出、`go vet ./...` 通过
- [x] 3.2 `scripts/ci-check.sh` 全绿（vet / staticcheck / golangci-lint / arch / vulncheck / format / docs in sync / fast 48s / race 49s / vfs-stability 58s / localfs smoke 6s）。第一次 race 层出现 `pkg/drivers/quark` 的 `TestDriverPutMultipartUploadResumesPersistedParts` 失败（其自带的 150ms 上下文超时先于 part 1 的 PUT 完成），该用例不经过改动路径；经复现确认是既有 timing flake，已由独立变更 `defuse-quark-resume-test-flake` 修掉（本变更不再涉及）
- [x] 3.3 端到端复查：临时计数测试确认 crypt+quark（content_dedup）1 MiB 上传的 opens=2、总读取 2×（修复前 6×；哈希阶段 3× + 上传阶段 3×），验证后删除该临时测试
- [x] 3.4 真机确认「省略 hash 调用」不可行（用 quark-test 的 cookie 上传文本文件）：实现"随机 nonce 源 → 跳过扫描与 update/hash"后用真实 API 验证，`/file/upload/finish` 返回 `400 / code 43001 complete file failed!`；对照路径（content_dedup 打开、提交 hash）上传、下载校验、删除全部成功。结论：该扫描是 provider 强制的，已回退这次尝试并在 `quark_upload.go` 留下证据注释；"边上传边算 hash、finish 前提交"作为后继方案记录在非目标里
