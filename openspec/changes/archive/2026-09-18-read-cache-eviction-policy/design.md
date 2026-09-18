## Context

见 proposal.md - Why。设计与现状相关的约束：

- **淘汰已经是单独的一段逻辑，但内联在 `evictIfNeeded` 里**（`readcache/store.go:737-826`）：跨 16 个分片加读锁收集全部 chunk、`sort.Slice` 全局排序、两遍淘汰（先大文件池、再全局到 70%）。它没有可测试的边界——断言当前策略只能通过真实文件系统间接进行（`readcache/read_cache_test.go:250,283`）。
- **准入点有三个，且都在写路径上同步触发淘汰**：`putChunk`（`store.go:270`，同步）、`PutReader`（`store.go:367`，整文件播种）、`handleReadCacheWrites`（`store.go:989`，异步写批，最多 `readCacheWriteBatchLimit=16` 条共享一次淘汰）。因此策略的每次调用成本按批摊销，但仍在写路径上。
- **`chunkInfo` 已经带一个按时间合并的 `accessAt`**（命中时以 `cacheAccessWriteInterval=1s` 为粒度刷新，`store.go:70,167`），并持久化进 `index.json`（`store.go:654-659`）。它是现有的唯一"热度"信息，也是纯 LRU 的全部依据。
- **`readCacheIndexVersion` 不符时的行为是清空**：`loadReadIndex` 会删掉 index 并清理全部 `.batch` 文件（`store.go:540-546`）。所以任何持久化格式变更都是一次破坏性升级，这是"索引保持 v1"这个目标的由来。
- **物理回收单位是整个 batch 文件**：`cacheBatchBlocks=16` × `readChunkSize=512KiB`，`removeReadChunk` 只在批内最后一个引用消失时 `os.Remove`（`store.go:848-856`）。
- **读域已经能判断访问是否连续**：`observeReadAccess`（`read/state.go:327-368`）用 `discontinuous`（`offset != previous.end`）与 `confirmed`（连续前进）描述一次顺序读取，目前只用于决定预取深度（`read/reader.go:161,179`）。这个信号已经存在，只是没有进入缓存。
- **读域的 hint 走 context 传递**：`WithAccessHint`/`AccessHintFromContext`（`read/helpers.go:36-44`）与 `WithoutReadPrefetch`/`readPrefetchEnabled`（`read/helpers.go:16-23`）已经确立了"在 ctx 上挂一个可选读取属性、边界处取出"的惯例。
- **`readcache` 不 import 任何 vfs 域内包**，也不被 vfs 域内包反向依赖（`docs/for-developer/architecture.md:86-88`，`scripts/check-arch.sh` 守边界）；`read` 已经 import `readcache`（`read/host.go`），因此把共享的访问类型放在 `readcache` 不会引入新依赖。
- **`readCacheLargeFileBytes = 16MiB` 有双重用途**：既是淘汰的大文件门槛（`store.go:827`），也是上传播种的门槛（`pkg/vfs/upload.go:223,237`）。播种路径只对 <16MiB 的文件调用 `PutReader`/`PutLocalFile`。

## Goals / Non-Goals

**Goals:**

- 把淘汰策略做成一个**无副作用、可直接单测的决策步骤**：输入是缓存内容视图与容量上限，输出是 victim 列表；文件删除仍走现有的 `removeReadChunk`。
- 用一条可解释的预算树覆盖三个已确认的失效模式（大文件挤小文件、流式内容污染、散点淘汰不释放磁盘）。
- 复用判定基于读域已有的连续性判断，不引入第二套顺序语义。
- 持久化格式与配置面零变更，升级与回滚都不需要迁移。
- 策略只对需要放水的桶排序，而不是对全量 chunk 全局排序（实测这项收益有限，见决策 6；主要收益是行为上的）。

**Non-Goals:**

- 不做全局侵入式淘汰链、不引入新锁、不改 16 分片结构与 `removeReadChunk` 的 re-check + 引用计数语义。写路径的一次全量遍历按已确定的取舍保留。
- 不动内存热缓存的定长 LRU（`read/state.go:443-453`）。
- 不引入每块频次计数、Count-Min sketch、ghost list 等需要持久化或额外内存结构的算法。
- 不把份额/水位线暴露为配置。
- 不改 `ReadCacheLargeFileBytes` 的值与导出性。

## Decisions

### 1. 策略抽成 `planEviction`：视图 + 决策，删除仍由 Store 执行

新增 `readcache/eviction.go`，`evictIfNeeded` 收缩为「早退 → 建视图 → `planEviction` → 逐个 `removeReadChunk` → 日志与索引保存」。

```go
// victim 是一次淘汰决策选中的块；expected 用于删除时在文件锁下 re-check。
type victim struct {
    fid      string
    index    int64
    expected chunkInfo
}

func (c *Store) planEviction(maxSize int64) []victim
```

替代方案：把策略做成 `evictor` 接口 + 多实现（为将来的策略留插槽）。否决——当前只有一个算法，接口会带来无收益的间接层；而"决策与执行分离"已经给出了全部可测试性。将来要加第二种策略时，`planEviction` 本身就是那个插槽。

### 2. 三级预算树，每一级对应一个失效模式

```
maxSize
├── small 类              保底 maxSize / readCacheSmallReserveDiv(=4)
└── large 类              上限 maxSize - maxSize/4
      ├── 未证实子配额     ≤ large 上限 / readCacheStreamShare(=2)
      └── 已证实配额       其余
      每个配额内部：protected ≤ 80%，其余 probationary
```

- **容量类**：`readCacheFileLarge(fileSize, cachedBytes)` 保留现有语义（`fileSize ≥ 16MiB`，或 `fileSize == 0 && cachedBytes ≥ 16MiB`），只是从"淘汰顺序的分组键"升级为"预算归属"。
- **未证实子配额**：large 类的新准入块一律记为未证实；只有被另一读轮次命中过的块转为已证实并释放该配额。这是"顺序流不得挤占可复用内容"这条要求的实现。
- **SLRU 分段**：每个配额内 protected ≤ 80%。命中来自另一轮次时晋升；protected 超份额时把其 LRU 尾部降级回 probationary。淘汰总是从 probationary 取，取空了才降级 protected。

**配额只在容量吃紧时生效。** 淘汰由「占用 > maxSize」触发；占用仍在上限内时预算树不淘汰任何内容。这是有意的取舍：配额要回答的是"容量吃紧时谁先让出空间"，而不是"空闲时允许保留什么"。提前执行配额会在缓存还有空闲时丢弃可用内容，且不会带来任何额外保护——只要没有淘汰发生，可复用内容就没有被挤占。规格里把这一点写成了显式场景（"空闲容量不触发淘汰"）。

替代方案：
- **纯 LRU + 现有两池**（不动）：无法区分复用与一次性读取，第 1、3 个失效模式还在。
- **LFU / LFUDA / GDSF**：需要每块频次并持久化（否则重启后全丢），且经典 LFU 的旧热点永不过期，需要额外的老化机制。对本地读缓存属于过度设计。
- **TinyLFU / W-TinyLFU**：命中率最好，但需要 Count-Min sketch 与准入过滤器，是为内存缓存设计的结构；这里每块 512KiB、决策频率低，收益不抵复杂度。
- **ARC**：自适应 recency/frequency 配比、免调水位线，但 ghost list 必须为已淘汰项保留元数据，与"策略状态不持久化"的目标直接冲突。
- **CLOCK / 2Q**：把全量排序换成 O(1) 结构，目标是写路径开销；本次已确定保留一次全量遍历（见决策 7），且环形结构需要改造分片布局。

### 3. 复用判定用「读轮次」，而不是访问次数或顺序布尔

`chunkInfo` 增加仅内存的 `admitRun uint64`（准入时所在轮次）与 `segment`（probationary/protected）。命中时：`access.Run != admitRun` 才晋升。

理由：读域已经在算 `discontinuous`/`confirmed`，一次"轮次"就是一次连续前进的读取过程。它与"访问次数"的区别是关键——顺序流会**命中自己预读进来的块**，按次数计会把流式内容判为热数据；而按轮次计，同一轮次内的命中只是流在回读自己的预读。

替代方案：
- **只用 `decision.sequential` 布尔**：无法区分"首轮消费"与"重新观看"。重看视频时第二轮仍是顺序读，块永远不会晋升，视频内容永远进不了已证实配额。
- **用每块命中次数（≥2 才晋升）**：与布尔方案有同样的缺陷——首轮消费计 1 次，第二轮消费计第 2 次才晋升，而那之前它已经被流配额淘汰了。
- **在 store 内自建顺序探测器**（按 fid 记录上次索引与前进方向）：会把 `readcache` 变成第二个顺序语义来源。`runtime-counters` 的 design 已经明确否决过同一语义两套口径的做法（"避免出现'事件里是 0、计数里是别的值'这种两套口径"）。读域已经是这个语义的归属方，沿用它。

轮次 ID 的分配：`sequentialRead` 增加 `run`，`sequentialState` 增加一个受同一把锁保护的全局自增计数器；`discontinuous` 为真时取下一个值，否则沿用上一轮次的 `run`。用进程内全局自增（而非 per-key 计数）保证被 `SequentialLimit` 淘汰后重建条目不会与历史 `admitRun` 撞号。

### 4. 轮次信号经 context 传递，不逐个改函数签名

从 `Reader.Read` 到 `readerBackend.StoreChunk` 之间要经过 `readRange` → `readChunkRange` → `loadWindow` → `fetchChunkWindow` 五层，逐层加参数会改动 5 个内部签名。改为在边界处把已算出的 `Access{Run}` 放进读取 ctx（`readCtx`），缓存调用点用 `cacheAccess(ctx)` 取出——与既有的 `WithAccessHint`/`WithoutReadPrefetch` 完全同构。

`Reader.Read` 的 hinted 分支在读之前就拿到了 `decision`（`read/reader.go:146`），可以直接用；un-hinted 分支 `decision` 在读之后才算（`reader.go:174-175`），此时用一个只读 peek 返回该文件上一次已知的轮次。把这次读取归到"上一次轮次"而不是开新轮次，使同一顺序流不会被误判为新轮次——保守且方向正确，代码里注释说明这一点。

### 5. 策略状态只在内存，索引保持 v1

`segment` 与 `admitRun` 不写入 `index.json`：

- 写入任何新字段都要 bump `readCacheIndexVersion`，而版本不符会走清空路径（`store.go:540-546`），升级即丢缓存。
- 内存态可以自愈：`loadReadIndex` 把加载回来的块一律置为 probationary（`admitRun` 置 0，与任何真实轮次都不等），因此**任何被再次访问的历史内容立刻晋升**。这比持久化分段状态更符合"缓存"的语义，且 `accessAt` 仍然持久化，LRU 顺序不因重启而丢失。

`readCacheIndexChunk` 的 JSON 结构一字不改。这同时给出了一条干净的回滚路径：旧二进制仍能读新二进制写下的索引。

### 6. 淘汰顺序：按需放水 + batch 凑满

视图构建时顺便按 `(类, 配额, 分段)` 分桶，并统计每个 batch 文件当前被引用的块数。淘汰按顺序放水：

1. large 类压回上限：未证实桶 → probationary 桶 → 降级 protected 尾部后继续；
2. 总量仍高于低水位（保留 `maxSize * 7 / 10`）时继续放水 large，再放水 small（large 始终优先，保持现有测试的意图）；
3. 桶内按 `accessAt` 升序，**同段同年龄时优先选所在 batch 已被引用块数最少的块**——即"淘汰它能凑满并删除一个物理单元"。

只对真正要抽干的桶排序，通常是一两个桶，而不是全量 chunk。**实测（`BenchmarkEvictionPlan` 对 `BenchmarkEvictionPlanPreviousPolicy`，240 块、混合桶）两者相差仅约 15%（59µs 对 69µs），且新实现多分配约 10%**：主导成本是遍历索引并物化候选，而不是排序。因此"去掉全局排序"不是这次改动的主要收益——主要收益是行为上的（见轨迹测试）。若将来要继续降低单次淘汰成本，杠杆是"不再遍历整个索引"，那是被有意排除在本次之外的另一件事（见 Non-Goals，以及用户已确认的取舍）。

第 3 条是次级排序键，与主策略解耦：它只在候选年龄相同时生效。生产路径上 `accessAt` 来自 `time.Now()`，年龄相同的候选主要出现在 `PutReader` 的整文件播种（同一文件所有块共用一个 `now`）。它给每个候选加一次按字符串键的 map 自增，是上面那 10% 额外开销的一部分。保留它的理由是整文件播种是常见路径，而它在那条路径上决定"是收尾一个批次还是留下两个半空批次"；删掉它不需要改动任何其他决策或需求。它的有效性由 `TestEvictionPrefersTheBatchThatIsNearlyEmpty`（手工构造同年龄候选）覆盖。

### 7. 不维护新的增量计数器：视图全部派生

类字节、配额字节、分段字节都在建视图的那一次遍历里算出来，不做对称的增量维护。理由：`InvalidateFile`、`ClearReadCache`、`dropReadChunkIndex`、`replaceFileChunks`、`loadReadIndex` 五处都会改变内容，任何一处漏改增量都会造成静默漂移；而 `evictIfNeeded` 本来就需要这次遍历。`readBytes` 保持为唯一的原子计数器，只用于"是否超限"的早退。

### 8. 快照追加字段，不动 `DebugSnapshotSchemaVersion`

新增派生字段与事件计数。派生字段分两个维度，因为它们回答不同的问题：`unproven_bytes`/`proven_bytes` 是**状态**维度（跨容量类，未证实内容就是"没有另一个轮次回来读过它"）；`stream_bytes` 是**配额**维度（只统计大文件类的未证实内容，也就是顺序子配额此刻在计费的量，它必须不超过配额上限）。分段维度是 `protected_bytes`/`probationary_bytes`（同属已证实内容）。事件计数是 `promotions` 与 `evicted_bytes`。

之所以需要 `stream_bytes` 而不只是 `unproven_bytes`：小文件类的未证实内容不受任何子配额约束，把它并进"配额占用"会让"配额被用满"这个数字失真。事件计数用 atomic（`stats` 已有先例），派生字节数在快照遍历里算（`DebugReadCacheFile.Large` 的归类与策略共用 `classOf`，避免两处判断分叉）。全部是追加，不改名不改语义，符合 `observability` 既有要求的"新增暴露 MUST 是快照的追加字段"；按 `runtime-counters` 的先例不 bump `DebugSnapshotSchemaVersion`。

### 9. 不新增配置键

份额、水位线、子配额都是具名常量并在文档里说明依据。理由：新增配置键要同时改 `config.go`、`validation.go`、`qrypt.schema.json`、`template.go` 并重跑 `scripts/gen-config-docs.py`，还要处理 `[thumbnail_cache]` 与 `[read_cache]` 共用 `ReadCacheConfig` 的问题——而目前没有任何证据表明这些份额需要按用户调。`read_cache.max_size` 仍然是唯一开关（`0` = 禁用）。

## Risks / Trade-offs

- **[磁盘占用分布明显变化，依赖"流式内容长期驻留"的用户会感到差异]** → 未证实块在跨轮次命中后晋升，重看/回看的视频会逐步转为已证实并长期保留；同时保护了此前被流式内容挤掉的小文件与可复用大文件内容。行为变化写入 `docs/for-developer/debug.md`。
- **[预取块由同一次读取轮次写入，因此大文件的顺序预读全部落在未证实配额内]** → 这正是预期行为；未证实配额按大文件类上限的一半计算，足以覆盖一次连续播放的回看范围，超出的部分按最早引入淘汰。
- **[只做流式读取的工作负载只能用到大文件类上限的一半]** → 这是未证实配额的直接后果，也是本次改动最可能想调的一个常量。它只在缓存吃紧时生效，且重看/回看会把内容转为已证实并释放配额，所以长期流行的内容最终能用满。若实测显示偏紧，`readCacheStreamShare` 是唯一需要改的地方。
- **[un-hinted 读取只能拿到上一次已知轮次，可能把一次新顺序读的前几块归入旧轮次]** → 保守方向：不晋升意味着这些块留在未证实配额，代价是多等一轮才转为已证实；不会造成错误晋升。命中路径的 `readRunHint` 用与记录路径同一个 `nextRunFor` 规则来预测，因此两个并发读者在同一文件上可能预测到同一个 id；影响被限制在一个窗口被当作复用。
- **[batch 凑满优先可能让"更旧但所在 batch 很空"的块排在"稍新但能凑满 batch"的块之后]** → 只在同段同年龄区间内生效；且它换来的是磁盘空间的真实释放。它给每个候选加一次按字符串键的 map 自增，是基准里那份额外开销的来源之一，已实测（见决策 6）。
- **[`Cache` 接口三个方法签名变更]** → 触及 `pkg/vfs/read` 的两个测试 fake（`read/runtime_test.go:56,60`、`read/backend_test.go:32`）与 `readcache` 内部测试调用；属机械改动，需一次性改完才能编译。
- **[缓存在接近容量上限时淘汰更频繁]** → 保留 70% 低水位滞后，单次淘汰释放到低水位而非恰好压回上限。
- **[单次淘汰的成本仍与缓存规模线性相关]** → 本次已确认不动（见 Non-Goals）。已量化：240 块时约 59µs/次，且按异步写批（最多 16 条）摊销。若它在剖析里变得显著，下一步是不再遍历整个索引（例如按类/分段维护侵入式链），而不是继续微调排序。
- **[`maxSize` 很小时类份额可能退化]** → 份额按除法派生（`maxSize/4`、`large 上限/2`），在现有淘汰测试的 2MiB 量级下仍能划分出有效配额；策略对 `maxSize` 为零或负直接早退。

## Migration Plan

无磁盘迁移：

- 索引格式保持 v1，`accessAt` 语义不变，升级时既有缓存内容继续命中，不清空、不重写。
- 策略状态（`chunkState`/`admitRun`）在 `loadReadIndex` 时统一重置为未证实，无需迁移步骤。
- 回滚：直接回退提交即可。旧二进制能读新二进制写下的索引（结构未变）；新二进制在旧缓存目录上启用也无需重建。
- 无新增配置键，因此不存在配置向后兼容问题：`max_size` 语义与 `0`=禁用行为不变。

## Open Questions

无。batch 凑满次级键的收益已实测（决策 6）：它在生产时间戳下基本不生效，价值集中在整文件播种路径，并有单测覆盖；保留它是净收益为正的最小选择，删掉也不需要改动其他决策或需求。
