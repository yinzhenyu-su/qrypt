## Context

当前读路径的调用链是 `Reader.Read` / `ReadStream` → `readChunkRange` → `loadWindow` → `fetchChunkWindow`，后者对每个读窗口只发一次 `DriverRead`。窗口大小、预取深度、在途窗口数和并发槽位全部来自 `pkg/vfs/read/state.go` 的编译期常量；配置面只有 `ReadCacheConfig.MaxSize`。预取在 `prefetchWindow` 中用 `context.WithCancel(context.WithoutCancel(ctx))` 脱离请求上下文，取消入口只有 seek。缓存写入在 `fetchChunkWindow` 中无条件发生：`StoreChunk` 同时写内存热块和持久缓存。

已有的可复用机制：`AccessHint`（会话、请求、并发）、`ReadPriority`（低/普通/高）、`acquireReadSlot` 的优先级槽位、`windowLoad` 的并发合并、`prefetch.cancels` 注册表。动机见 `proposal.md`，行为契约见 `specs/read-pipeline/spec.md` 与 `specs/observability/spec.md`。

## Goals / Non-Goals

**Goals:**

- 让前端能声明读意图，并让读调度层按意图选择并行度、窗口、预取和缓存写入。
- `bulk` 在单文件窗口内做有界并行 Range，`interactive` / `probe` 保持单流。
- 驱动默认读策略可预测，不要求用户配置；驱动能力可约束上限。
- 删除、关闭、取消能停止后台预取，并正确释放槽位。
- 一次性批量传输与探测不写持久缓存，避免重复磁盘写和缓存污染。
- 保持现有调用方、配置和驱动契约兼容。

**Non-Goals:**

- 不修改 `read-cache` 的淘汰策略与持久化索引格式。
- 不修改 `drive.Driver.Read` 契约，不要求驱动实现新的读方法。
- 不以服务端复制替代本地读；直读路径的并行不经过 VFS 读缓存，仍按直读语义执行。
- 不新增 FUSE `Seek` 回调；seek 仍表现为带新 offset 的 `Read`。
- `ReadRaw` 保持原样，不接入读意图、并行或缓存策略。

## Decisions

### 1. 读意图放在 context，缺省单流并允许会话自适应

新增 `WithReadIntent` / `ReadIntentFromContext`，与现有 `AccessHint`、`ReadPriority` 同层放在 `pkg/vfs/read/helpers.go`。取值 `bulk`、`interactive`、`probe`；未声明时按单流处理，只有已打开读会话满足“持续顺序 + 大文件 + 单句柄”时才自适应升级。

- 理由：避免为每种意图新增 `Read` 方法；前端只需在调用处附加上下文。
- 备选：按 `size` 推断（拒绝，大小不等于意图）；新增 `ReadBulk` / `ReadProbe` 方法（拒绝，API 膨胀且破坏现有调用方）。

### 2. 并行 Range 在 `fetchChunkWindow` 内完成，驱动不变

对 `bulk` 窗口，将 `[startIndex, endIndex]` 拆成至多 N 个连续子区间，每个子区间用独立读槽位调用 `DriverRead`，按 offset 写入同一个 `windowLoad.data`，全部完成后才 `close(load.done)`。

- 只有前台 `bulk` 窗口并行；后台预取窗口保持单流低优先级，避免投机流量按 N 倍放大。
- 单文件并行上限按该文件**总并发子 Range** 计，由每文件信号量约束，并继续受全局 `MaxConcurrency` / `HighReserve` 约束；窗口数量不得放大该上限。
- 驱动已经内部并行（Quark）时 VFS 保持单流，避免 VFS 4 流 × 驱动 3 worker 叠加并绕过槽位记账。
- 合并窗口只在取数形态兼容时复用；不兼容的等待者自行发起读取。缓存写入按每个等待者自己的策略在收到数据后执行。
- 备选：在驱动内统一并行（拒绝，重复实现、无法统一回退和缓存策略）；把窗口拆成多个独立 `windowLoad`（拒绝，破坏合并与去重）。

### 3. 读策略由驱动默认值决定，不新增用户配置

不扩展 `ReadCacheConfig`，也不新增 `[read]` 段。驱动通过可选接口或内置 profile 提供 `ParallelStreams`、`WindowChunks`、`PrefetchDepth` 的推荐值；VFS 根据 intent、文件大小、`CapabilityParallelRange` / `CapabilityInternalParallelRead` 选择实际值。默认 `bulk` 4 流、`interactive` / `probe` 1 流；未声明能力的驱动 1 流。跨文件 worker 固定 4。用户配置面保持只有 `read_cache.max_size`。

- 理由：这些参数影响稳定性，应该由驱动作者按 provider 特性调好；暴露给用户会让错误配置变成支持负担。
- 备选：`[read_cache]` 扩展（拒绝，语义混杂且增加用户误配面）；新增 `[read]` 段（拒绝，同样把稳定性参数交给用户）。
- 内部测试/调试可通过非用户配置的注入点覆盖（例如测试用 `ReaderDeps`），不进入用户文档。

### 4. 驱动能力与数值上限，回退责任在驱动

新增可选 `CapabilityParallelRange` 和可选接口 `ParallelReadLimiter { MaxParallelReads() int }`。声明能力的驱动才允许 `bulk` 并行；未声明时按单流处理。驱动 MUST 保证子 Range 返回 206；无法满足时 MUST 返回明确的不支持错误（例如 `ErrUnsupported` 或 typed error），而不是把 200 全量 body 当作子 Range 结果。系统收到该错误后对该文件回退单流。

- 理由：`Driver.Read` 只返回 `io.ReadCloser`，VFS 没有状态码或 `Content-Range`，无法通过 body 长度可靠检测 200 全量；回退责任必须放在能看到响应的驱动层。
- 并行子 Range 会携带 strict-range 上下文；驱动收到 200 全量时必须在 strict 模式下返回 `ErrUnsupported`，由 VFS 回退单流。不能在 strict 模式下继续跳过 offset，否则一个并行窗口会退化为 N 次全量下载。
- 已内部并行的驱动（Quark）声明 `CapabilityInternalParallelRead`，VFS 不再叠加并行。
- 备选：VFS 读取额外字节猜测 200（拒绝，破坏流语义且可能多读整文件）；对所有驱动先并行再回退（拒绝，最坏情况浪费 N 倍带宽）。

### 5. 预取生命周期用 per-file / per-session scope 管理

新增读作用域注册表：键为 `(cacheKey, sessionID)`，值为可取消的 context。预取上下文从该 scope 派生，而不是 `WithoutCancel(request ctx)`。取消来源：

- `InvalidateFile(cacheKey)`：取消该文件的全部预取作用域。
- `ReleaseReadSession(sessionID)`：取消该会话注册过的 `(cacheKey, sessionID)` 作用域。
- 前台读返回时 `ctx.Err() != nil`：视为调用方取消，取消对应文件/会话的预取。
- 移动端 `CloseFileJSON` / `CancelFileReadJSON`：通过显式的 VFS 取消入口按 `cacheKey` 停止预取；移动端不走 `ReleaseReadSession`，不能只依赖前台 context。
- VFS `Close`：取消所有作用域。

取消后必须关闭对应 `windowLoad.done`、释放 `prefetch.sem` 和读槽位；`waitWindow` 已把 `context.Canceled` 视为“窗口不可用”，等待者可重试或返回。取消只针对未完成的后台预取；已完成的缓存填充保留，`ReleaseReadSession` 的现有注释需要同步更新。

- 理由：正常请求完成不应取消预取，只有显式取消或生命周期事件才应取消；scope 能区分这两者，并能把移动端句柄生命周期纳入同一入口。
- 备选：保留 `WithoutCancel` 并在 `InvalidateFile` / `ReleaseReadSession` 里补调用（拒绝，无法覆盖调用方取消与移动端关闭，也难以按会话精确取消）。

### 6. 读意图决定持久缓存写入

显式 `bulk` 与 `probe` 默认跳过持久读缓存写入；`interactive` 保留现有写入。FUSE 读会话自适应升级为 `bulk` 后仍保留缓存写入，避免挂载内重读全部 miss。热块缓存仍保留当前读取所需的块：它是有界内存，不写磁盘，跳过它会让同一个读窗口被逐块重复拉取；跳过的是持久缓存。

判断依据是“内容是否会被复用”，而不是“是否读到了”：

- `fs get` / `fs sync` / `offline fetch` / `download` / `download_stream`：整文件一次性落地，内容已经写到本地目标，缓存只会造成第二份磁盘写并挤占可复用内容。
- `probe`（MP4 头、缩略图探测、哈希/元数据小读）：一次性小读，缓存块粒度远大于请求，写缓存纯属浪费。
- `fs cat`：一次性输出，通常不会再次读取；`fs cat --raw` 走 `ReadRaw`，本来就不经过 VFS 缓存。
- `interactive`（FUSE 常规读、移动端预览、媒体回放）：存在回看、重读和 seek 复用，保留缓存。
- staging read-modify-write 已经通过 `driver.Read(0,0)` 绕过 VFS 缓存（`pkg/vfs/staging.go:404` → `pkg/vfs/upload_runtime.go:90`），本变更只需确认该路径不被新策略拉回缓存，不需要额外实现。

实现上由 `readPlan` 携带 `CacheWrites`，`fetchChunkWindow` 在 `StoreChunk` 前检查；`ReadStream` 的显式 bulk 计划关闭写入。`read-cache` 的淘汰策略不变，但需要补一个 delta 明确“准入由读意图决定，淘汰只约束已准入内容”。

### 7. 公平性与优先级

`bulk` 默认 `PriorityLow`，`interactive` 默认 `PriorityHigh`，CLI 默认 `PriorityNormal`；调用方显式 `WithReadPriority` 优先于意图默认值。每文件总并发子 Range 由信号量限制；全局槽位继续使用现有 `MaxConcurrency` 与 `HighReserve`。`bulk` 不得占满高优先级预留槽位。并行子 Range 必须经过驱动现有的请求限流/重试路径，不得绕过 mount 的 rate budget。

- 理由：一个后台大文件下载不应饿死 FUSE 前台读或移动端预览；显式优先级是调用方已有的契约，不能被意图覆盖。
- 备选：只靠全局槽位（拒绝，单文件并行会挤占其他文件）；让意图强制覆盖显式优先级（拒绝，破坏移动端 `priority` 选项的语义）。

### 8. FUSE 自适应，CLI / 移动端显式声明

- FUSE：不声明固定意图，使用 `AccessHint` 和 `observeReadAccess` 的顺序判定；持续顺序且大文件时升级 `bulk`，seek/随机/多句柄时保持 `interactive`。自适应升级只改变并行度与窗口，缓存写入仍保留。
- CLI：`fs get`、`fs sync`、`fs cat`、`offline fetch` 声明 `bulk`；只有真正经过 VFS 读的元数据路径声明 `probe`（`fs check` 通常只取驱动声明 hash，不作为 probe 调用点）；`fs cat --raw` 走 `ReadRaw`，保持原样。
- 移动端：`ReadAtInto` 与媒体虚拟读默认 `interactive`；`ReadDownloadItemInto` 声明 `bulk`；`openFileOptions` 增加可选 `intent`，缺省 `interactive`。`CloseFileJSON` / `CancelFileReadJSON` 增加显式 VFS 预取取消调用。
- 后台任务：`download`、`download_stream`、`offline fetch` 声明 `bulk`；`share fetch` 的直读回退、`fs copy` / `copy` 任务的 drivecopy 使用同一并行策略（见决策 11）。
- 跨文件并发（`fs get` 目录、`fs sync` 的 worker 池）与 `ReadStream` 跨窗口预取见决策 10。

### 9. 诊断字段追加

在 `drive.MetricEvent` 与 `vfstypes.DebugActiveOp` 上追加 `read_intent`、`read_streams`、`prefetch_cancel_reason`（JSON 字段名以最终实现为准），由 `RecordChunkDetail` / `DebugRecordRead` 填充。汇总事件的 `read_streams` 记录本次读的最大并行流数，窗口/阶段明细事件记录该窗口实际流数；未声明意图的旧调用方记录为空值。后台预取取消记录为取消结果并携带原因，不计为前台读取失败；前台调用方取消继续按现有错误/取消语义记录。追加字段可为空，旧消费方忽略即可。

### 10. 跨文件 worker 与 ReadStream 跨窗口预取

- `fs get` 目录与 `fs sync` 在 CLI/syncer 层增加有界 worker 池，固定默认 4，不新增用户配置；`download` 任务既有的 `Concurrency` 选项保持不变。每个 worker 使用 `bulk` 意图；跨文件并发与单文件并行共享全局读槽位；进度与取消沿用现有 progress/context。
- `ReadStream` 在 `chunkedStreamReader` 中增加下一窗口预取，深度来自驱动默认策略（默认 1）；当前窗口消费完前预取完成；内存上界为深度 × 窗口大小；取消走同一 scope。
- 跨文件并发放在 CLI/syncer 而不是 VFS：VFS 不知道文件列表；`ReadStream` 预取放在 VFS 读域内。
- 备选：把跨文件并发放进 VFS（拒绝，职责错位）；无限 read-ahead（拒绝，内存不可控）。

### 11. 直读路径复用同一并行策略

- `pkg/vfs/drivecopy` 与 `pkg/core/share_fetch.go` 的 `driver.Read(0,0)` 改为通过共享的 `drive` 并行 Range helper：按驱动能力/默认值拆子 Range，按 offset 拼接，单文件总并发上限与 VFS 读一致。
- 直读路径不使用 VFS 槽位（没有 mount reader），因此使用独立的进程级/任务级有界信号量，默认与 VFS 的单文件上限一致；Quark 等内部并行驱动保持单流。
- 直读路径本来就不写 VFS 缓存，保持该行为；取消通过任务/调用方 context 传播。
- 备选：让 drivecopy 走 VFS Read（拒绝，跨挂载/直读语义和临时文件路径会改变）。

## Risks / Trade-offs

- [并行触发 provider 速率限制] → 驱动能力声明 + 默认 4 流 + 每文件总并发上限；驱动可在代码中回退到 1，并行子 Range 继续走驱动共享的请求限流。
- [驱动违反 Range 契约返回 200 全量] → 回退责任在驱动：驱动必须返回明确的不支持错误，VFS 不通过 body 长度猜测。
- [Quark 已有驱动内并行，VFS 再并行会叠加] → 区分 `CapabilityInternalParallelRead`，该能力下 VFS 保持单流，并加回归测试防止 4×3 叠加。
- [并行增加内存占用] → 子 Range 写入预分配窗口缓冲，内存上界为窗口大小 × 并行流数；流数受全局槽位约束。
- [bulk 不写缓存导致中断后无法从缓存续传] → 本地目标与任务层已有断点续传；缓存不是批量传输的恢复机制。
- [FUSE 顺序判定误升级] → 只在超过阈值、单句柄、持续顺序时升级；自适应 bulk 仍保留缓存，驱动默认策略可在代码中关闭并行。
- [取消与窗口完成竞争] → 用 `sync.Once` 关闭 `load.done`，取消路径统一释放槽位与预取名额；等待者按取消结果重试。
- [一个会话读多个文件时按会话取消过宽] → 作用域键包含 `cacheKey`，只取消该会话实际读过的文件。
- [移动端关闭句柄后预取继续] → `CloseFileJSON` / `CancelFileReadJSON` 调用显式 VFS 取消入口，不依赖前台 context。
- [合并窗口让不同意图串味] → 只合并取数形态兼容的读；缓存写入按每个等待者自己的策略执行。
- [读策略不可由用户调整] → 驱动默认值需要充分基准测试与契约测试；问题通过驱动版本/代码修复，而不是让用户误配稳定性参数。
- [默认 4 流与 bulk 跳过缓存改变用户可见行为] → 在 changelog/release notes 说明；回退通过驱动默认值或代码，不提供用户配置开关。
- [跨文件 worker 与单文件并行争抢槽位] → 两者共享全局槽位，单文件总并发上限独立约束，worker 数固定为 4。
- [ReadStream read-ahead 增加内存] → 深度默认 1，内存上界为深度 × 窗口大小，且受全局槽位约束。
- [跨文件 worker 导致进度乱序或本地磁盘抖动] → 每个文件内部保持顺序，进度只按完成字节单调累加，worker 数固定为 4。
- [直读路径与 VFS 读同时并行导致全局超限] → 直读使用独立有界信号量，默认与 VFS 单文件上限一致；驱动共享请求限流。

## Migration Plan

1. 先落地意图传播、驱动默认读策略、诊断字段；此阶段没有任何调用方声明 `bulk`，未声明意图仍走现有单流，确保无行为变化。
2. 为显式 `bulk` 的前端与支持 `CapabilityParallelRange` 的驱动启用并行 Range；默认 4 流，回退通过驱动默认值/代码。已内部并行的驱动保持 VFS 单流。
3. 接入缓存准入策略：显式 `bulk` / `probe` 跳过持久缓存，`interactive` 与 FUSE 自适应 bulk 保留。
4. 接入生命周期取消（含移动端显式取消）与 FUSE 自适应；取消修复独立于并行开关，回滚并行不会回滚取消。
5. 接入跨文件 worker（`fs get` 目录、`fs sync`，固定 4）与 `ReadStream` 跨窗口预取（默认深度 1）；两者共享全局槽位与单文件上限。
6. 接入直读路径并行（drivecopy、`share fetch` 回退），复用驱动默认策略与独立有界信号量。
7. 更新 `docs/for-user/full-config.md`、`docs/for-developer/architecture.md`、schema、changelog 与驱动能力文档。

## Open Questions

- 并行阈值暂定 8 MiB；是否需要在配置中暴露，留待基准测试后再定，不影响本变更的规格与任务拆分。
