## Why

`pkg/vfs/readcache` 的淘汰策略无法区分「复用」与「读一次」，也没有把磁盘的物理回收单位纳入决策，因此存在三个已确认的失效模式：

1. **顺序流把可复用内容挤掉。** 读取窗口抓到的每个块都会经 `PutChunkAsync` 落盘（`pkg/vfs/read/reader.go:365-373`），而命中时无条件刷新 `accessAt`（`readcache/store.go:70`）——顺序流消费自己的预读块也算一次「命中」。于是流式内容在 LRU 里看起来是热数据。唯一的缓解是硬编码的 75% 大文件池上限（`store.go:796`），而且大文件池内部没有任何 per-file 公平性：顺序读一个大文件时，它自己尚未被消费的预读块正是最旧的一批，会**优先被自己挤掉**（大文件池内自我抖动）。
2. **小文件的保护是不可配置的常量，而不是一条策略。** `readCacheFileLarge`（`store.go:827`）把「文件 ≥16MiB，或 size 未知但已缓存 ≥16MiB」判为大文件，`largeBudget = maxSize - maxSize/4`（`store.go:796`）给小文件留 25%，超限后按 `accessAt` 全局淘汰到 `maxSize*7/10`（`store.go:795`）。这三条一起构成了一个只能用常量表达的近似，且「未知大小的文件」这一遗留分支说明它已经在承担兼容性负担。
3. **淘汰常常一个字节磁盘都没释放。** 物理回收单位是整个 8MiB batch 文件（`cacheBatchBlocks=16` × `readChunkSize=512KiB`），但 victim 选择是 512KiB 块粒度、只按 `accessAt` 排序（`store.go:793`）；`removeReadChunk` 只在批内最后一个引用消失时才 unlink（`store.go:848-856`）。因此按年龄散点淘汰一半的块，磁盘占用可以几乎不变。

此外，每次超限写入都要跨 16 个分片加读锁收集全部 chunk 再全局 `sort.Slice`：2G 容量约 4096 个块、10G 约 2 万个，而该路径由同步 `putChunk`（`store.go:270`）、整文件播种（`store.go:367`）与异步写批（`store.go:989`）三处触发。

## What Changes

- **引入「读轮次」信号，淘汰策略据此区分复用与一次性读取。** 读域已经能判断访问是否连续（`pkg/vfs/read/state.go:327` 的 `observeReadAccess` 计算 `discontinuous`/`confirmed`，目前只用于决定预取深度），现在把它升级为一个轮次标识并随缓存操作传入：同一轮次内的命中只是流在回读自己的预读，**不计为复用**；只有来自另一轮次的命中才把块晋升为「已证实」。
- **用三级预算树替换硬编码的两池划分。** 容量类（小文件保底 `maxSize/4`、大文件上限 `maxSize - maxSize/4`）→ 大文件类内的「未证实」子配额（替代流式内容污染）→ 每个配额内部的 probationary/protected 分段（替代纯 LRU）。每一级对应上面一个失效模式，份额改为具名常量。
- **淘汰顺序改为按需放水，去掉全局排序。** 一次遍历构建紧凑视图后，只对需要放水的桶按年龄升序抽干，不再对全量 chunk 排序；同段同年龄时优先选择**能凑满一个 batch 文件**的块，使淘汰真正转化为磁盘回收。
- **准入策略化**：新块一律以 probationary 准入，大文件类的新块同时计入未证实子配额。
- **持久化格式与配置面不变**：分段与轮次状态只在内存，`readCacheIndexVersion` 保持 1，升级不触发既有的「版本不符即清空缓存」路径；不新增 `read_cache` 配置键（份额、水位线、子配额为具名常量 + 文档，后续可纯增量暴露）。
- **快照追加策略状态**：按 `observability` 既有要求的追加字段方式（不改名、不改语义、不动 `DebugSnapshotSchemaVersion`）暴露未证实/已证实/分段字节数与晋升、释放字节数计数。

非目标：

- 不动内存热缓存（`pkg/vfs/read/state.go` 的 `HotChunkLimit` 定长 LRU）。
- 不动上传路径的播种门槛（`pkg/vfs/upload.go:223,237` 依赖 `ReadCacheLargeFileBytes`）与 `ReadCacheLargeFileBytes = 16MiB` 本身。
- 不引入全局侵入式淘汰链或新锁：分片结构、锁序与 `removeReadChunk` 的 re-check + 引用计数语义保持。
- 不改虚拟文件系统的可观察语义：读到的字节、命中/未命中口径、`max_size="0"` 的禁用短路行为不变。

## Capabilities

### New Capabilities

- `read-cache`: 持久读块缓存的准入与替换策略——容量类与配额隔离、跨轮次才晋升的复用判定、淘汰必须转化为磁盘回收、索引格式稳定性、策略状态的可观测性，以及缓存禁用时的短路契约。

### Modified Capabilities

无。快照新增字段是对 `observability` 既有「Counters are exposed per mount in the debug snapshot」要求的追加字段实现，该要求本身不变，因此不产生 delta spec。

## Impact

- `pkg/vfs/readcache`：`types.go`（`chunkInfo` 加仅内存的 `segment`/`admitRun`，新增类/分段常量与 `Access` 类型）、新增 `eviction.go`（策略本体，无副作用、可单测）、`store.go`（`evictIfNeeded` 改为「早退 → 建视图 → `planEviction` → 逐个删除」，三个准入点记录轮次，两条命中路径按轮次晋升，四处生命周期清理分段状态）、`debug.go`（追加派生字段与计数）。
- `pkg/vfs/read`：`host.go` 的 `Cache` 接口三个方法加 `access readcache.Access` 参数；`helpers.go` 加 `WithCacheAccess`/`cacheAccess`（沿用既有 context 传递惯例，不逐个改函数签名）；`state.go` 给 `sequentialRead` 加 `run` 与自增计数器；`reader.go` 在命中与写入边界取用，并补一个只读 peek 供未携带 open-file hint 的读取使用。`PutReader`/`PutLocalFile`/`InvalidateFile` 签名不变。
- 测试：两条现有淘汰测试（`readcache/read_cache_test.go:250,283`）按新契约重写但保留原意图；新增策略单测、不变量断言 helper、并发 `-race` 用例与轨迹驱动命中率基准；`pkg/vfs/read` 的两个测试 fake 需同步签名。
- 文档：修正 `docs/for-developer/architecture.md:226-228` 指向已不存在的 `read_cache_store.go`/`read_cache_writer.go`/`read_cache_index.go`/`read_cache_eviction.go` 的过时描述，并在 `docs/for-developer/debug.md` 说明新增的快照字段。
- 磁盘占用分布会明显变化：顺序流占用被压到未证实子配额内。重看/回看的视频会因跨轮次命中而逐步转为已证实并长期保留。
