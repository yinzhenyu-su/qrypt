## Context

动机见 `proposal.md`。判定"死代码"的证据（实施前逐项复核过一次，实施时须再复核——这类结论会随时间失效）：

- **staging 写缓冲**：`pkg/vfs/upload/store.go:122-135`（`page` 类型与 `stagingStore.pages`）、`:206`/`:221`（`pages.Delete`）、`:228-233`（`flush` 只做 `pages.Load`）、`:247-262`（`flushNow`）。生产者为 0：全仓 `pages.Store(` 与 `page{` 命中 0；`writeAt`/`size`/`truncate`/`sync` 直接对文件 `WriteAt`/`Stat`/`Truncate`/`Sync`（`:174-245`），所以 `flush` 恒为 no-op。
- **`FlushStaging` 链路**：`store.go:1275` → `stagingStore.flush`（no-op）→ `pkg/vfs/read_host.go:50-51` → `read.Host.FlushStaging`（`read/host.go:28`）→ 调用点 `read/reader.go:95`、`read/stream.go:74`（读带 pending 上传的文件前调用）；另有 `readRuntime`/`stateRuntime`（`read/reader.go:254,297`）与 **`pkg/vfs/staging.go:164`（`VFS.Flush`，紧接 `SyncStaging`）** 两处实施期发现的调用点。今天它不产生任何 I/O；`read/health_test.go:164-175` 的 `failingStagingHost` 是唯一注入其失败的测试桩，`read/observer_test.go` 的 `stagingHost` 则借它物化 staging 文件。
- **两个失败记录器**：`store.go:343`（`RecordUploadFailure`）、`:399`（`RecordUploadPermanentFailure`）。生产调用者 0；只有 `pkg/vfs/stores_test.go:35,130`、`pending_atomic_test.go:269`。同名的 `...IfUnchanged`（`:431`、`:465`）多一道 `sameUploadRecord` 代际校验。
- **`Service` 转发**：`service.go:463-495` 的 store/hash 转发中，`SaveUpload`、`SaveUploadExact`、`UploadByPath`、`RemoveUploadsUnder`、`RenameUpload`、`RemoveStagingIfUnreferenced`、`HashRemoveUnder`、`HashRenamePath` 的调用点数为 0（store 调用方一律走 `uploads.Store().X(`／`store.X(`）。**实施期修正**：候选里的 `RemoveUpload`、`HashRemovePath` 各有一个真实调用者，接收者是 `pkg/vfs/task_source.go:121,124` 的 `s.svc` 字段——因此本轮删除 8 个而非 10 个。教训：按接收者计数必须覆盖该类型在仓库里的**所有**持有者名（`v.uploads`、`s.svc`），否则会把活代码判成死代码。其余转发（`Queue`/`Enqueue`/`Close`/`ScheduledDeadlines`/`DebugState`/`CancelUpload`/`PendingByID`/`PendingUploads`/`TryAcquire`/`Release` 等）有真实调用者，保留。
- **`internal/cli` 转发/别名**：8 个文件（见 proposal）。逐符号核对：11 个符号仅测试引用（`newFsCmd`、`runMkdir`、`runPut`、`runRm`、`runMv`、`runGet`、`fsCopyDirError`、`fsCopyDryRunResult`、`fsCryptResult`、`fsListEntry`、`printPendingVerbose`、`journalMaintenanceResult`），2 个是单调用点生产包装（`validateConfig`→`config_path.go:87`、`currentBuildInfo`→`command_root.go:16`），1 个别名（`buildInfo`）无引用。
- **`pkg/vfs/upload` 的消费者**：只有 `pkg/vfs`（生产与测试），没有 `pkg/mobile`/`pkg/control`/`internal/cli` 引用，因此删除其公开方法不会波及桥接层。
- **不是死代码**：`StoreAdapter`（`store.go:1128-1163`）由 `pkg/vfs/upload.go:82` 在装配时使用，并有专门测试（`pending_store_test.go:10,70`），它是 `pkg/vfs` 与 store 之间的接缝。

## Goals / Non-Goals

**Goals:**

- 删除"从未被使用"的代码与转发层，使读 staging 与读 `Service` 时不再需要区分"真实路径"与"历史残留"。
- 删除过程**不改任何可达行为**：每条删除都要么有无生产者/无调用者的证据，要么其被删调用在当前实现下等价于空操作。
- 用测试迁移而非删除来保住覆盖面：迁移后的断言必须验证同样的语义，且优先落到更安全的 API（`IfUnchanged`）。

**Non-Goals:**

- 不补上页缓冲的生产者（那等于新增一个没有需求的批写机制）。
- 不把 `FlushStaging` 改成真正的 fsync（行为变化：每次 staged 读取多一次 I/O + 需要性能评估）。
- 不改 `StoreAdapter`/`PendingStore` 的方法命名（公开 API 改名不是死代码清理）。
- 不做审计里的 Tier 1–3：`TargetIndex` 与 listing 缓存二合一、上传记录状态机 + `Revision`、可观测性 DTO 收敛、`RemoveStagingIfUnreferenced` 的 O(n) 加固、`pkg/vfs` 根包适配层收敛。

## Decisions

### D1: 删除死缓冲，而不是让它工作

`page`/`pages`/`flush`/`flushNow` 没有生产者，说明这个批写设计从未落地。备选：实现一个真正的写缓冲（合并小写入）——否决：没有需求或性能证据，且会引入新的失效与持久化语义（当前 `writeAt` 是直接 `WriteAt` + 特定路径 `Sync`）。

### D2: 连带删除 `FlushStaging`，而不是留空实现或改成 fsync

三种备选：**(a)** 保留 `FlushStaging` 但实现为空——留下一个说谎的公开 API（名字承诺落盘，实际不做事）；**(b)** 把它实现为 `sync(path)`——行为变化（每次 staged 读取 fsync），且需要性能评估，属独立变更；**(c)** 删除它及其调用点与桩——当前调用是空操作，删除后行为不变。选 (c)，并把"读取 staged 字节前是否需要 fsync"记入 Open Questions。实施期确认一处**可观察的细节变化**：`read/reader.go` 与 `read/stream.go` 里为 flush 准备的两段 `DebugUpdateActive` 合并为一次（`staging_open` + `RemoteID`），因此调试活动操作里少了一个中间阶段标记 `staging_flush`。仓库内没有任何消费者引用该标记（grep 仅命中这两处赋值），保留它等于保留一段描述已删步骤的谎话，故随删除一并移除，并在此记录为本次唯一的可观察差异。

### D3: 删除零调用转发，`Service.Store()` 保持唯一直达路径

保留 `Store()` 访问（47 处调用）并删除 8 个零调用转发（实施期从 10 修正，见上文证据）。备选：删掉 `Store()` 让调用方统一走转发——否决：22 处调用点的大改，收益只是形式统一，且转发层与 store 同名方法并存本身就是混淆源。

### D4: 两个死记录器删除，3 处测试迁移到 `IfUnchanged`

迁移后的测试通过 `UploadByPath` 取出当前记录再调用 `RecordUploadFailureIfUnchanged(p, ...)`——语义是同一个"记录失败"，但走的是生产使用的、带代际校验的 API。**实施期注意**：必须传 store 里那份记录（`SaveUpload` 会补 `UpdatedAt` 等字段），直接用测试构造的结构体会因 `sameUploadRecord` 不匹配而返回 `ok=false`。备选：保留死方法以便测试直接按路径写——否决：它们缺少代际校验，注释本身就警告会覆盖新一代（`store.go:428-430`）。

### D5: `internal/cli` 转发/别名单文件删除，测试改用子包符号

测试改为直接使用 `clifs.CopyDirError(cliRuntime{}, result)`、`clifs.ListEntry`、`clijournal.MaintenanceResult` 等；两个单调用点包装内联为 `config.Validate(...)` 与 `buildinfo.Current()`。备选：保留别名——继续付"同名符号属于哪一层"的阅读成本，且这些别名没有一个生产消费者。

### D6: 判定标准（本次与后续清理共用）

一段代码算"死"当且仅当：(1) grep 与编译器证据表明无生产者或无调用者；(2) 删除后行为能被既有测试（或迁移后的测试）覆盖证明不变。任一条不满足 → 不放进"纯删除"变更，改为独立提案（如 `FlushStaging` → fsync 属于行为变更）。

## Risks / Trade-offs

- [死代码结论失效（并行改动可能新增生产者/调用者）] → 每个删除任务的第一步是重跑对应 grep 与 `go build`；证据不符时停下来说明，不硬删。
- [测试迁移顺带削弱覆盖面] → 迁移必须保留同一断言（记录失败并返回更新后的记录），除 `failingStagingHost` 那条——它的主题（flush 失败）随 `FlushStaging` 一起消失，属于"主题被删"而非"断言被丢"，需在任务里显式说明。
- [`FlushStaging` 的意图消失后，将来需要"读取前落盘"时无人记得] → 记入 Open Questions 与 design 正文；实现时也在 store 的 `sync` 注释里点明"staged 读取的持久化需求应经由 sync"。
- [删除公开方法影响仓库外消费者] → 已核对 `pkg/vfs/upload` 与 `internal/cli` 仅被仓库内引用；AAR 走 `pkg/mobile`，不引用这些符号。

## Migration Plan

一个提交，按 proposal 的 A→D 四段执行，每段结束跑相关包测试：A staging 缓冲 + `FlushStaging` 链路（`pkg/vfs/upload`、`pkg/vfs`、`pkg/vfs/read`）→ B 死记录器与测试迁移 → C `Service` 转发 → D `internal/cli` 文件与测试迁移。无持久化/wire/配置变化；回滚即 revert（不存在数据迁移）。

## Open Questions

- 读取带 pending 上传的文件前是否需要先 fsync staged 字节（即把 `FlushStaging` 的意图实现为 `sync`）？需要性能与崩溃一致性评估，独立变更处理。
