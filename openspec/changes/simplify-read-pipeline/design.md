## Context

动机见 `proposal.md` 的 Why。这里只记录塑造方案的现状约束。

当前读域的实际形状（以 `add-read-intent-policy` 应用后为准）：

- `Reader.Read`（`reader.go`）在一次调用里完成 staging 判定、resolve、计划解析、窗口大小推导、缓存键、前台计数、访问模式判定、seek 取消预取、FUSE 自适应升级、首窗口预取、读窗口、访问记录、相邻预取和诊断上报。`ReadStream`（`stream.go`）重复了前半段。
- `readChunkRangeWithRuntime` 有两条几乎相同的缓存查找链：直接命中一次，`waitWindow` 之后再一次；每条链都包含热块、范围提升、持久范围命中三种分支。
- `loadWindow` 同时负责窗口裁剪、并发合并、槽位获取、取数、关闭 `done` 和注销窗口。
- `fetchChunkWindowWithPlan` 的并行分支负责拆分、`errgroup`、每文件信号量、全局槽位、strict-range 和 `ErrUnsupported` 回退；`drive.ReadParallel` 独立实现了同一组决策。
- `State` 持有 9 个子状态：`cache`、`history`、`prefetch`、`sequence`、`slots`、`fastPath`、`windows`、`parallel`、`scopes`。
- 并发预算分散在 4 处：`slotState`（全局 normal/high）、`parallelState`（每文件）、`prefetchState.sem`（预取名額）、`directReadSlotPool`（直读全局）。
- 取消登记分散在 2 处：`prefetchState.cancels/reasons` 与 `scopeState`。两者由不同代码路径写入，必须手工保持一致。
- `findWindow` 遍历所有在途窗口；`readChunkRange` 的窗口查找因此是 O(在途窗口数)。

约束：

- 必须保持 `read-pipeline` 已定义的行为：意图语义、并行上限、strict-range 回退、缓存准入、取消原因、诊断字段。
- 不改变 `drive.Driver.Read`、`vfs.Host`、移动端 JSON API 和持久缓存格式。
- 允许调整包内类型与测试接缝；跨包只保留真正被使用的接口。
- 仓库要求提交前通过本地 CI，Go 文件必须 gofmt，优先 Go 1.27+ 语法。

## Goals / Non-Goals

**Goals:**

- 把读域收敛为一条可顺序阅读的管线：入口 → 不可变计划 → 缓存查找 → 窗口注册 → 调度 → 取数 → 缓存准入 → 观测。
- 每个可变资源只有一个所有者，删除同一语义的第二份登记或第二份实现。
- 用 characterization 测试锁定现有行为，再逐阶段替换内部结构；每个阶段可独立提交和回滚。
- 让并发、取消和缓存准入的正确性可以由少量不变量和单元测试证明，而不是靠组合推演。

**Non-Goals:**

- 不新增读功能，不调整意图默认值、并行阈值、窗口大小、预取深度或缓存淘汰策略。
- 不把直读路径并入 VFS 缓存，不改变 `ReadRaw`，不新增 FUSE `Seek`。
- 不重写 `readcache.Store`，不改变持久化索引或磁盘布局。
- 不新增用户配置或调试 API；现有诊断字段保持不变。
- 不追求行数指标；拆分以职责边界为准。

## Decisions

### 1. 一次读只解析一次计划，计划不可变

`ReadPlan` 改为值类型，字段显式且完整：

```go
type ReadPlan struct {
    Intent        ReadIntent
    Streams       int
    WindowChunks  int
    PrefetchDepth int
    CacheWrites   bool
}
```

- `PlanResolver.Resolve(intent, fileSize, defaultWindowChunks)` 返回完整计划，不再用 `0` / `-1` 表示“沿用旧策略”；未声明意图的默认值在 resolver 里显式写出，测试可直接断言。
- 本阶段优先级仍由 `applyIntentPriority` 单独应用，缓存 run 仍走 `WithCacheAccess`；等 `Scheduler` 阶段再并入计划，避免在本阶段改变优先级语义。
- `Read` / `ReadStream` 在入口解析一次，之后把计划作为显式参数传给窗口、取数、缓存和观测函数；只有跨 goroutine 的预取需要从 context 取回同一个计划。
- FUSE 自适应升级通过 `PlanResolver.Resolve(ReadIntentBulk, ...)` 生成新值，设置 `CacheWrites=true` 并保留当前 FUSE 窗口，再用 `WithReadPlan` 替换 context 中的计划；不再修改局部 `plan` 变量后遗漏某些阶段。
- 备选：保留可变结构体并加注释约束（拒绝，正是当前“某阶段读到旧计划”的缺陷来源）；把计划拆成多个 context key（拒绝，增加隐式依赖）。

### 2. 单一调度器实现，预算实例化而不是复制逻辑

新增 `Scheduler`：

```go
type Request struct {
    Kind        LoadKind // foreground | prefetch | parallel
    Priority    Priority
    FileKey     string
    PerFileMax  int
    PrefetchCap bool
}

type Scheduler interface {
    Acquire(ctx context.Context, req Request) (release func(), err error)
    ForegroundCount() int
}
```

- 实现内部保留现有三组容量：全局 normal/high 槽位（`MaxConcurrency` / `HighReserve`）、每文件并行许可（`PerFileMax`）、预取名額（`PrefetchLimit`）。
- 获取顺序固定为「准入许可（预取名額或每文件许可）→ 全局槽位」，释放逆序；所有 release 用 `sync.Once` 保护，避免取消与完成竞争时重复释放。
- 优先级语义保持现状：high 先试 normal 再试 high reserve；low 只使用 normal；normal 使用 normal。
- VFS 挂载持有一个 mount scheduler；直读路径持有进程级 scheduler 实例，容量仍为 `directReadSlots`。两者共用实现，但预算独立，因此不改变“直读与 VFS 并发互不挤占”的现有行为。
- `prefetchState.foregroundReads` 迁入 scheduler；`prefetchState.sem` 删除。
- 备选：把直读并入 mount scheduler（拒绝，行为变化且直读没有 mount）；保留四套信号量只抽公共函数（拒绝，锁顺序仍分散）。

### 3. 单一生命周期作用域

新增 `ScopeRegistry`，键为 `(cacheKey, sessionID)`（与现有 `AccessHint` 对齐），值为：

```go
type scope struct {
    ctx    context.Context
    cancel context.CancelFunc
    reason string
    refs   int
}
```

- 预取上下文保留调用方的 values（`context.WithoutCancel(ctx)`），并用 `AfterFunc(scope.ctx, cancel)` 把 scope 的取消桥接到预取；scope 是唯一的取消所有者。调用方取消由 `Read` 在错误路径显式调用 `Cancel(ctx, cacheKey, "caller")`，保持“正常完成不取消预取”的语义。
- 取消入口为 `CancelFile(cacheKey, reason)`、`CancelSession(sessionID, reason)`、`CancelAll(reason)` 和按 `(cacheKey, sessionID)` 精确取消的 `Cancel(ctx, cacheKey, reason)`。`InvalidateFile`、`ReleaseReadSession`、seek、移动端关闭句柄、VFS `Close` 都调用它们。
- 取消原因只存在 scope 上，`prefetchState.cancels/reasons` 删除；诊断读取原因时从 registry 取，避免两份状态不同步。
- 取消时 scope 立即从 registry 注销并取消，refs 归零时释放对象；之后的新预取会创建新的 scope，不会继承已取消的 context。已完成的缓存填充不受影响。
- 备选：保留两套登记并加一致性测试（拒绝，测试只能覆盖已知路径）；按 goroutine 取消（拒绝，无法按文件/会话聚合）。

### 4. 单一窗口注册表，结构化键与 O(1) 查找

`WindowRegistry` 用结构化键：

```go
type windowKey struct {
    cacheKey string
    start    int64
    end      int64
    streams  int
}
```

- 精确加入用 `windowKey`；覆盖查找用 `(cacheKey, chunkIndex, streams)` 索引，窗口注册时把覆盖的每个 chunk 写入索引，注销时删除。窗口大小有界（1–16 块），索引内存与在途窗口块数成正比。
- 形态兼容规则保持现状：只有 `streams` 相同才共享；bulk 并行窗口与 interactive 单流窗口不互相继承。
- `beginWindowLoad` / `findWindow` / `endWindowLoad` 合并为 `Join` / `Register` / `Finish`；`done` 仍先关闭再注销，保证已拿到指针的等待者可以安全等待。
- 预取去重直接使用注册表：若目标范围已被任何兼容窗口覆盖，则不启动预取；`prefetchState.inFlight` 删除。
- 备选：按文件保存有序区间并二分（可行但实现更复杂）；保留线性扫描（拒绝，在途窗口多时退化为 O(n)）。

### 5. 单一缓存准入策略

新增 `CachePolicy`：

```go
type CachePolicy struct {
    hot     *State
    durable Cache
}

func (p *CachePolicy) Admit(ctx context.Context, entry drive.Entry, cacheKey string, index int64, chunk []byte)
```

- 热块始终写入（有界内存，供本次读取的后续 chunk 复用）；持久缓存只在 `ReadPlan.CacheWrites` 为真时写入。计划从 context 读取；没有计划的直接调用方保持旧的“允许持久写入”行为。
- 前台窗口、并行子 Range、预取窗口和 `ReadStream` read-ahead 都通过 `readerBackend.StoreChunk` 调用同一个 `Admit`；`durableCacheWrites(ctx)` 和分散判断已删除。
- 查找侧保留现有三条路径（热块、范围提升、持久范围命中），但收敛为 `lookupCachedChunk`；直接命中与 `waitWindow` 后的命中共享同一实现，只通过 phase/source 前缀保留原有诊断名称。
- 备选：把准入下沉到 `readcache.Store`（拒绝，缓存层不应理解读意图）；只保留热块并关闭持久缓存（拒绝，行为变化）。

### 6. 共享取数决策与 span 读取，装配留在各自调用方

新增 `drive` 内的共享层：

```go
func SplitRanges(start, end int64, streams int) []Range
func ReadSpan(ctx context.Context, d Driver, entry Entry, span Range) ([]byte, error) // strict + limit
func IsUnsupported(err error) bool
```

- VFS 与直读路径共用拆分、strict-range span 读取和 `ErrUnsupported` 判定；VFS 仍需按 chunk 做缓存准入和诊断，直读仍需按 offset 流式写出，因此两者的装配层保持独立。
- 回退策略只实现一次：任一 span 返回 `ErrUnsupported` 时，对该文件标记 non-parallel 并整体重试单流；直读路径本次调用直接单流。
- `RangeSupport`（non-parallel 记忆）由 VFS 的取数器持有；直读路径保持每次探测，避免为接口值设计进程级键。
- 备选：抽取完整的“读整个 range 返回 buffer”（拒绝，直读需要流式，会多一份整文件内存）；让直读走 VFS（拒绝，语义和临时文件路径都会改变）。

### 7. 删除测试专用间接层

- 删除 `readRuntime`、`readWindowBackend`、`stateRuntime`。测试直接构造 `Host` fake（`Resolve`、`PendingRead`、`DriverRead`、`ReadCacheKey`）和 `Cache` fake。
- 保留 `Host`、`Cache`、`ReadObserver`、`HealthRecorder`、`CounterRecorder`：它们跨越 `pkg/vfs` 与 `pkg/vfs/read`，是真实边界。
- 故障注入继续通过 `Host` / `Cache` fake 完成，不因删除接缝而丢失覆盖。
- 备选：保留接缝用于单测（拒绝，接缝已经和真实实现同步演化，测试价值低于维护成本）。

### 8. 入口共享前置阶段，但不合并缓冲语义

- 抽取 `openRead(ctx, kind, path)`：staging 判定、resolve、目录检查、计划解析、优先级、诊断开始。`Read` 和 `ReadStream` 各自决定后续是整段缓冲还是按窗口流式。
- 观测字段由 `RecordRead` / `RecordChunkDetail` 从一个 `plan` 统一填充；`Read` 与 `ReadStream` 的 kind 不同，但字段集合相同。
- 备选：让 `ReadStream` 复用 `Read` 再切片（拒绝，会整文件入内存）。

### 9. 文件按职责拆分

目标布局（仍在 `package read`）：

- `read.go`：`Read` 入口与整段读取。
- `stream.go`：`ReadStream` 与 `chunkedStreamReader`。
- `plan.go`：`ReadPlan`、`PlanResolver`、意图与优先级默认值。
- `context.go`：intent / access hint / priority / prefetch switch 的 context 传播。
- `keys.go`：chunk / window / cache key。
- `chunk.go`：`readChunkRange` 与缓存查找。
- `window_registry.go`：窗口注册与合并。
- `fetch.go`：窗口取数、拆分、strict 回退、诊断 extra。
- `scheduler.go`：调度器。
- `scope.go`：生命周期作用域。
- `cache_policy.go`：热块与持久缓存准入。
- `prefetch.go`：相邻预取与 read-ahead 调度。
- `state.go`：只做装配与生命周期，不再承载实现细节。
- `history.go`、`hot_cache.go`、`access_tracker.go`：从 `state.go` 拆出的独立状态。

- 备选：保持大文件只加注释（拒绝，阅读成本不降）；拆成子包（拒绝，内部类型与测试成本上升，收益不足）。

### 10. 诊断字段保持不变

- `read_intent`、`read_streams`、`prefetch_cancel_reason` 的名称、取值和“取消不计失败”的语义不变。
- 只把填充逻辑收敛到 `Observer` 包装器，避免每个阶段各自判断 plan 是否存在。
- 备选：新增调度器等待时间等字段（拒绝，超出本次范围）。

## Validation Results (2026-11-04)

设计验证以测试/基准形式落地在 `pkg/vfs/read`，不改生产代码：

| 实验 | 位置 | 结果 |
|---|---|---|
| 取消登记一致性 | `registry_consistency_test.go` | invalidate / session-release / legacy seek / close 四条路径都会让两套登记同时清空；测试矩阵内没有观察到分歧 |
| 调度不变量 | `scheduler_invariant_test.go` | 全局槽位、每文件许可、预取名額均未超限；32×100 并发 acquire/release 后全部归零，无泄漏 |
| `findWindow` 成本 | `bench_window_test.go` | 1/10/100 个在途窗口分别约 31/80/450 ns/op，0 allocs；相对 512 KiB 读窗口可忽略 |
| 双并行策略 | `parallel_policy_test.go` | parallel / no-capability / internal-parallel / strict-unsupported 四种情况下，VFS 与 `drive.ReadParallel` 的 parallel/single/fallback 判定一致 |

结论：测试矩阵内没有证据表明重复机制正在造成取消分歧、并发超限、泄漏或两条并行路径漂移。`findWindow` 的线性扫描也不是性能瓶颈。因此下一步采用**最小可行收敛**，而不是一次性完成全部阶段：

- 保留并优先实施：不可变 `ReadPlan`、`ScopeRegistry`、`CachePolicy`。
- 推迟：`Scheduler`、`WindowRegistry`、共享取数层、文件拆分/测试接缝删除。它们仍有维护价值，但当前证据不支持把它们作为稳定性修复。
- 重新评估触发条件：出现取消登记分歧、槽位超限/泄漏、`findWindow` 在真实负载下超过 10 µs/op、VFS 与直读策略漂移，或有新功能必须同时修改这些机制。

## Risks / Trade-offs

- [并发预算合并导致公平性变化] → 保留现有容量常量与获取顺序；用计数 fake 断言同一场景下前后并发数一致；阶段 4 单独提交。
- [锁顺序引入死锁] → 固定「准入许可 → 全局槽位」顺序；所有路径 `defer release`；`-race -count=10` 覆盖取消与完成竞争。
- [窗口索引遗漏导致重复下载] → 注册与注销在同一把锁内维护精确键和 chunk 索引；增加“N 个并发同范围读只触发 1 次驱动读”的 characterization 测试。
- [作用域取消过宽或过窄] → 键包含 `cacheKey` 与 `sessionID`；分别测试按文件、按会话、按句柄和全量取消；测试“无关文件不受影响”。
- [删除测试接缝降低故障注入能力] → 用 `Host` / `Cache` fake 保留失败注入；迁移现有故障测试而不是删除。
- [文件拆分引入循环依赖] → 全部留在 `package read`，编译期即可发现；不新增子包。
- [重构期间行为漂移难以定位] → 阶段 0 先建立 characterization 基线；每阶段结束跑同一套测试；每阶段独立提交，可单独回滚。
- [直读路径行为变化] → 保留独立预算实例和现有 part size；只替换实现，不改变容量与顺序。
- [推迟 Scheduler/WindowRegistry 后重复机制继续存在] → 已用不变量测试和基准锁定当前行为；重新评估触发条件明确，出现证据再启动。

## Migration Plan

每个阶段都必须编译、通过现有测试和验证测试，并作为独立提交。任一步失败可回滚该提交。

0. **设计验证（已完成）**：四项实验落地为测试/基准；结论见上。
1. **不可变计划**：引入 `ReadPlan` 与 `PlanResolver`，替换 `readPlan` 和哨兵值；入口只解析一次。
2. **作用域注册表**：用 `ScopeRegistry` 替换 `prefetch.cancels/reasons` 与 `scopes`；删除 `prefetchState` 中已迁移字段。
3. **缓存准入**：引入 `CachePolicy`，收敛热块/持久缓存判断和重复查找分支。
4. **文档与最终门禁**：更新 `docs/for-developer/architecture.md` 与 `CHANGELOG.md`；跑完整本地 CI、race、goleak、基准对比。

推迟项（不阻塞上述阶段）：

- `WindowRegistry`：`findWindow` 基准显示当前成本可忽略；只有在真实负载超过阈值时再做。
- `Scheduler`：不变量测试未发现超限/泄漏；只有出现跨池超限或公平性问题时再做。
- 共享取数层：两条路径当前策略一致；只有出现漂移或需要同时修改时再抽取。
- 文件拆分与测试接缝删除：属于阅读性/维护性收益，等逻辑稳定后单独做。

回滚策略：按阶段 revert；无持久化格式变化，无数据迁移，无需版本兼容处理。

## Stability Gates

以下门禁在阶段 0 建立、每阶段结束重跑：

- `go test -race -count=10 ./pkg/vfs/read/... ./pkg/drive/...`
- 读域与 VFS 生命周期测试启用 goleak；不新增 `time.Sleep`，用 channel / fake 驱动同步。
- 并发断言使用计数 fake（最大并发、总调用数、每文件并发），不依赖时序。
- 基准 `BenchmarkRead*` 前后对比；若吞吐或分配回归超过 10%，必须在阶段提交说明原因。
- `gofmt -l` 对改动文件无输出；`go vet`、staticcheck、`scripts/ci/check-all.sh` 全部通过。
- `openspec validate simplify-read-pipeline --type change --strict` 通过。

## Open Questions

- `Scheduler` 是否需要在调试快照中暴露等待时间与拒绝次数？当前不影响行为与任务拆分，可后续以新增字段方式加入。
- 直读路径是否也值得共享 non-parallel 记忆？当前保留每次探测，避免为接口值设计全局键；若基准显示探测成本显著，再单独评估。
- 热块缓存是否应迁入 `readcache`？属于更大的缓存边界调整，不在本次范围。
