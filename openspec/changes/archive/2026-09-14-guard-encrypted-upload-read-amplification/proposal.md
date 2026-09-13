## Why

`ba52526`（`reduce-upload-read-amplification`）把加密源的顺序读放大从 2–17× 降到约 1×，但它的防回滚用例只覆盖到**加密源本身**：`TestEncryptedReadOnlyFileSourceReadsEachBlockOnce` 直接读 `encryptedReadOnlyFile` 并计数明文读取。当时验证端到端效果（crypt+quark 1 MiB：opens=2、总读取 2×，修复前 6×）用的是临时计数测试，验证后删除了，归档变更的 tasks 3.3 只留下文字记录。

回滚验证时的实测结论：

- 把 `encryptedBlock` 的缓存命中分支改成恒 false → `TestEncryptedReadOnlyFileSourceReadsEachBlockOnce` 失败（4 KiB 窗口下 `read 4456571 plaintext bytes for a 262267 byte source`），**这条能挡住回滚**；
- 同一次回滚下 `TestEncryptedReadOnlyFileSourceReadAtReusesMemoizedBlock` 仍然通过——它逐字节对比参考 `EncryptingReader`，守的是缓存不返回陈旧/错位字节的**正确性**，不守读取量；
- `TestDriverPutSourceContentDedupCachesEncryptedUploadHashes` 只断言 source 的 **opens**（首次 2 次、命中缓存后 1 次），且用的是 43 字节载荷（单块以内），因此每个 pass 内部读了多少字节完全不受约束。

也就是说：整条 wrapper → raw 驱动链路上"每个 pass 只读一遍明文"没有任何常驻断言，只在 crypt 源单测里被间接覆盖，而单测不经过 wrapper 的 hash pass 与 raw 驱动的流式读。把这两段拆开看，任何一段出现额外的块重读都不会被发现。

## What Changes

- 新增常驻端到端读取计数用例 `TestDriverPutSourceReadsEachPlaintextBlockOncePerPass`（`pkg/crypt`）：走真实 `Driver.PutSource`（content_dedup，raw 驱动要求 md5+sha1 因而触发 hash pass），raw 驱动按真实窗口大小（4 KiB / 32 KiB，模拟 io.Copy 与 HTTP 请求体）流式读完整个加密源，断言：
  - source opens == 2（hash pass + 流式 pass，与既有 opens 断言一致的量化基线）；
  - raw 驱动实际流出的密文字节 == 完整加密长度（防止"少读就算通过"的假通过）；
  - 明文读取总量 ≤ 2×源大小 + 2×块大小（每个 pass 一块的余量）。实测 4 KiB 与 32 KiB 窗口都是恰好 2×（524534 / 262267）。
- 扩展现有 fixture `countingSHA256Source`（`pkg/crypt/driver_test.go`）增加字节计数：`Open` 返回的 `ReadOnlyFile` 包一层，统计调用方实际拉取的明文字节。既有两个只断言 `opens` 的用例行为不变。

非目标：

- 不改任何生产代码；本变更只加防护。
- 不给 VFS 侧"快照是否整读 staging"加读取计数：那条路径的三个分支偏好已经由 `TestStageExistingFeedsUploadSnapshotHashes`（改动快路径→落到 tracker，`Incremental=true` 触发失败）与 `TestSnapshotPendingUsesIncrementalHashesForSequentialWrite`（强制整读→`snapshot did not use incremental hashes`）覆盖，强制整读会被后者抓住，因此不需要新的计数钩子。

## Capabilities

### New Capabilities

无。

### Modified Capabilities

无。仅新增/扩展测试，可观察契约不变，`.openspec.yaml` 声明 `skip_specs: true`。

## Impact

- `pkg/crypt/driver_test.go`：新增 `windowedHashDriver`（按窗口大小流式读的 raw 驱动）、`countingReadOnlyFile`（字节计数），扩展 `countingSHA256Source`，新增一个用例。
- 运行成本：262 KB 源 × 2 个窗口 × 2 个 pass 的 EME 加密，实测单测 0.06s（race 层可接受）。
- 防护能力（负向验证）：把 `encryptedBlock` 缓存命中改成恒 false 后，该用例以 `window 4096: read 5243126 plaintext bytes for a 262267 byte source (limit 655606)` 失败（4 KiB 窗口下 20×）。
