## 1. 策略基础（pkg/vfs/readcache）

- [x] 1.1 在 `types.go` 新增容量类与分段的具名常量（小文件保底份额 `readCacheSmallReserveDiv` 沿用现值，新增大文件类未证实子配额 `readCacheStreamShare`、protected 份额 `readCacheProtectedShare`、低水位），并新增 `Access{Run uint64}` 类型；验证 `go build ./pkg/vfs/...` 通过，且 `ReadCacheLargeFileBytes` 的值与导出性未变（`pkg/vfs/upload.go:223,237` 仍编译通过）
- [x] 1.2 给 `chunkInfo` 增加仅内存的 `admitRun uint64` 与 `state chunkState`，并保持 `readCacheIndexChunk` 的 JSON 结构一字不改；验证新增用例断言 `readCacheIndexVersion` 仍为 1，且 `index.json` 中不出现 `admit_run`/`segment`/`proven`/`state`（`TestReadCacheIndexFormatIsUnchanged`）
- [x] 1.3 新增类归类函数 `classOf`（沿用 `readCacheFileLarge` 的现有语义：`fileSize ≥ 16MiB`，或 `fileSize == 0 && cachedBytes ≥ 16MiB`）；验证 `TestEvictionTreatsUnknownSizeCachedFileAsLarge` 覆盖三档：已知小文件、已知大文件、size 未知且已缓存量达到门槛

## 2. 策略本体（pkg/vfs/readcache/eviction.go）

- [x] 2.1 新增视图构建 `buildView`：一次遍历 16 个分片，按 `(类, 配额, 分段)` 分桶并统计每个 batch 文件的被引用块数，全部派生不做增量维护；验证 `checkInvariants` 断言各桶字节数与 `readBytes`、`DebugSnapshot().Bytes` 一致，并在所有淘汰与生命周期用例中被调用
- [x] 2.2 实现 `planEviction`：先压回各类配额（未证实 → probationary → 降级 protected 尾部），再放水到低水位（large 优先于 small），桶内按 `accessAt` 升序、同段同年龄时优先选所在 batch 被引用块数最少的块；验证 `TestEvictionCapsUnprovenSubBudget`（子配额上限）、`TestEvictionDrainsToTheLowWatermark`（低水位落点与滞后带）、`TestEvictionPrefersTheBatchThatIsNearlyEmpty`（批次凑满优先）通过
- [x] 2.3 `evictIfNeeded` 收缩为「早退 → 建视图 → `planEviction` → `applyEviction`」，保留每 10 秒的磁盘余量复核，并去掉已无失败路径的 error 返回；验证 `go test ./pkg/vfs/readcache/...` 全绿

## 3. 准入与晋升接线（pkg/vfs/readcache/store.go）

- [x] 3.1 三个准入点（`putChunk`、`PutReader`/`replaceFileChunks`、`writeReadCacheChunk`）记录 `admitRun` 并一律以 unproven 准入；验证 `TestEvictionPromotesOnlyOnALaterRun` 断言新写入的块在跨轮次命中前保持未证实
- [x] 3.2 两条命中路径（`GetChunk`、`getChunkRange`）经 `touchReadChunk` 在 `access.Run != admitRun` 时晋升为 protected，同轮次命中只刷新 `accessAt`（刷新仍按 `cacheAccessWriteInterval` 合并，但晋升不受合并窗口影响）；验证 `TestEvictionPromotesOnlyOnALaterRun` 覆盖「同轮次不晋升」「跨轮次晋升」「未证实晋升后释放子配额」，`TestEvictionTreatsAnUnknownRunAsReuse` 覆盖零轮次回退
- [x] 3.3 四处生命周期（`dropReadChunkIndex`、`InvalidateFile`、`ClearReadCache`、`loadReadIndex`）不需要清理分段状态——策略统计全部派生而非增量维护；`loadReadIndex` 显式注释其中的块按零值（未证实）重建；验证 `TestReadCacheRestartStartsContentUnproven` 断言重启后全部未证实且首个跨轮次访问即晋升，`checkInvariants` 在生命周期用例中通过
- [x] 3.4 新增不变量断言 helper `checkInvariants`（`Σchunk.size == readBytes`、`DebugSnapshot().Bytes` 与块求和的交叉校验、`未证实 + 已证实 == 总量`、`probationary + protected == 已证实`、`large + small == 总量`、`stream ≤ unproven`、占用 ≤ 上限）；验证该 helper 在所有淘汰与生命周期用例中被调用且全部通过

## 4. 读域轮次信号（pkg/vfs/read）

- [x] 4.1 `sequentialRead` 增加 `run`，`sequentialState` 增加受同一把锁保护的进程内全局自增计数器，`discontinuous` 时分配新轮次、否则沿用上一轮次，并通过 `accessDecision.run` 暴露；`nextRunFor` 是「什么算新一轮次」的唯一实现，记录路径与预测路径共用；验证 `TestStateAccessRunsIdentifyOneContinuousPass`、`TestStateAccessRunsDoNotRepeatAfterPruning`
- [x] 4.2 `helpers.go` 按既有 `WithAccessHint`/`WithoutReadPrefetch` 的写法新增 `WithCacheAccess`/`cacheAccess`；验证 `TestCacheAccessContextRoundTrip` 断言往返一致、未设置时返回零值 `Access`
- [x] 4.3 `host.go` 的 `Cache` 接口三个方法（`GetChunkRange`、`GetChunkWithRange`、`PutChunkAsync`）增加 `access readcache.Access` 参数，`readcache.Store` 实现与两个测试 fake（`read/runtime_test.go`、`read/backend_test.go`）同步；验证 `go build ./...` 与 `go vet ./...` 通过
- [x] 4.4 `Reader.Read` 在 hinted 分支用已算出的 `decision.run` 设置 ctx，在 un-hinted 分支用只读预测 `readRunHint`（注释写明保守性与并发读者下的有界影响），命中与写入边界从 ctx 取出 `Access`；验证 `TestStoreChunkCarriesTheRunFromContext`（写入侧）与 `TestReadChunkRangeCarriesTheRunFromContext`（命中侧）钉住两个取值点，`TestStoreChunkWithoutCacheAccessAdmitsAsUnknownRun` 钉住漏接时的降级行为，`TestStateReadRunHintPredictsTheRecordedRun` 钉住预测与记录一致
- [x] 4.5 确认 `PutReader`/`PutLocalFile`/`InvalidateFile` 签名未变、上传播种路径行为未变；验证 `go test ./pkg/vfs -run 'SeedReadCache|UploadSource'` 通过

## 5. 快照可观测性（pkg/vfs/readcache/debug.go）

- [x] 5.1 追加派生字段与事件计数：`unproven_bytes`/`proven_bytes`（状态维度）、`stream_bytes`（配额维度，只统计大文件类的未证实内容）、`protected_bytes`/`probationary_bytes`（分段维度）、`promotions`/`evicted_bytes`；全部为追加、不改名不改语义，不 bump `DebugSnapshotSchemaVersion`，且 `DebugReadCacheFile.Large` 与策略共用 `classOf` 避免归类分叉；验证 `TestEvictionCapsUnprovenSubBudget` 断言 `stream_bytes` 落在配额内，`checkInvariants` 断言各维度自洽
- [x] 5.2 确认既有字段语义未变且消费方不受影响；验证 `go test ./pkg/vfs/... ./pkg/control/... ./internal/cli/...` 全绿

## 6. 测试与基准

- [x] 6.1 按新契约重写 `TestEvictionKeepsSmallClassWhileLargeClassIsOverBudget` 与 `TestEvictionTreatsUnknownSizeCachedFileAsLarge`，保留原意图（小文件内容在大文件类超限时存活、size 未知的大文件按大文件归类）；验证两条用例通过
- [x] 6.2 新增策略单测：`TestEvictionCapsUnprovenSubBudget`、`TestEvictionPromotesOnlyOnALaterRun`、`TestEvictionTreatsAnUnknownRunAsReuse`、`TestEvictionDrainsToTheLowWatermark`、`TestEvictionPrefersTheBatchThatIsNearlyEmpty`、`TestEvictionReleasesBatchFileWithItsLastChunk`、`TestReadCacheIndexFormatIsUnchanged`、`TestReadCacheRestartStartsContentUnproven`；验证全部通过
- [x] 6.3 新增并发用例 `TestEvictionConcurrentWritersKeepInvariants`（8 个写入者 × 64 块，远小于容量的缓存迫使持续淘汰）并断言确实发生了淘汰；验证 `go test -race ./pkg/vfs/...` 通过
- [x] 6.4 新增轨迹驱动验证 `bench_eviction_test.go`：合成「可复用大文件 + 小文件热点 + 100MiB 单遍扫描」的混合轨迹，在 16MiB 缓存上回放，用生产代码（`touchReadChunk`/`planEviction`/`applyEviction`）做决策、只跳过不存在的文件 unlink；**实测结果：替换后 可复用集合 8MiB 命中 / 0 缺失；替换前 0 命中 / 8MiB 缺失**（扫描把可复用大文件整份淘汰，并顺带淘汰了 3/8 的小文件块）。同时新增 `BenchmarkEvictionPlan` 与 `BenchmarkEvictionPlanPreviousPolicy` 量化决策成本
- [x] 6.5 用基准测量 batch 凑满次级排序键：生产路径的 `accessAt` 来自 `time.Now()`，同龄候选主要出现在 `PutReader` 整文件播种，因此该键在回放轨迹中不生效，其价值集中在播种路径（由 `TestEvictionPrefersTheBatchThatIsNearlyEmpty` 覆盖），代价是每个候选一次字符串键 map 自增；同时实测出桶划分相对旧策略的决策成本只快约 15%（59µs 对 69µs，240 块），主导成本是索引遍历而非排序。结论：保留该键，并把「不再遍历整个索引」明确留作后续独立变更；已记入 design.md 决策 6 与 Risks

## 7. 文档与门禁

- [x] 7.1 修正 `docs/for-developer/architecture.md` 指向已不存在的 `read_cache_store.go`/`read_cache_writer.go`/`read_cache_index.go`/`read_cache_eviction.go` 的过时描述，并把 `readcache` 包的职责说明更新为「索引持久化、异步写队列、准入/替换策略」
- [x] 7.2 在 `docs/for-developer/debug.md` 新增「Reading The Read-Cache Replacement State」小节：说明状态维度与配额维度的区别、分段字段的读法，以及磁盘占用分布为何与旧版本不同（只做流式读取的缓存会稳定在 `max_size` 以下）
- [x] 7.3 格式与静态检查：`gofmt -l .` 无输出，`go vet ./...`、staticcheck、golangci-lint 通过
- [x] 7.4 全量与竞态：`go test -count=1 ./...` 与 `go test -race -count=1 ./pkg/vfs/...` 全绿
- [x] 7.5 本地 CI 门禁：`scripts/ci-check.sh` 退出码 0（未改配置，`docs/for-user/` 生成文档无差异）
