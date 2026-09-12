## Why

上传链路里有一批**从未被使用**的代码与转发层：staging store 的内存写缓冲（`page`/`pages`/`flush`/`flushNow`）没有任何生产者——全仓 `pages.Store(` 与 `page{` 命中数为 0，因此每次 staging I/O 都在一个恒空的 `sync.Map` 上多做一次查找，`FlushStaging` 这个公开方法实际上什么都不做；`PendingStore` 有两个仅被测试调用的失败记录器，而且它们比同名的 `...IfUnchanged` 版本更不安全（无代际校验，会覆盖新一代）；`Service` 上有 10 个 store/hash 转发方法生产调用点为 0（调用方一律走 `Store()`）；`internal/cli` 里 8 个文件只是把已迁移到 `internal/cli/fs`、`internal/cli/journal` 的符号重新起名，其中 11 个符号只被测试引用、2 个是单调用点包装、1 个别名完全无引用。

这些残留不改变行为、不影响正确性，但持续增加阅读与改动成本：改 staging 要先排除一套不存在的缓冲语义，读 `Service` 要分辨哪些方法真的有人用，改 CLI 测试要先搞清同名函数属于哪一层。审计（本次变更前的逐项复核）确认它们都可以删除，因此现在一次清掉，把 Tier 1–3 的结构性收敛留给各自的变更。

## What Changes

- 删除 staging store 的死写缓冲：`page` 类型、`stagingStore.pages`、`flush`、`flushNow`，以及 `writeAt`/`size`/`truncate`/`sync`/`remove` 里对它们的调用（`sync` 保留真正的 fsync）。
- **BREAKING（in-tree API）** 删除随之失去意义的 `FlushStaging`：`upload/stagingStore`、`upload.PendingStore.FlushStaging`、`pkg/vfs` 的 `vfsReadHost`、`read.Host` 接口方法，以及读路径的两处调用（`read/reader.go`、`read/stream.go`）和相应测试桩（含只用于注入 flush 失败的 `failingStagingHost`）。这些调用此前是空操作，删除后行为不变；同时移除随之失去意义的 `staging_flush` 调试阶段标记（无任何消费者引用它，其存在的唯一理由是描述这一步）。
- 删除两个仅测试调用的失败记录器 `PendingStore.RecordUploadFailure` / `RecordUploadPermanentFailure`，并把 3 处测试调用迁移到 `...IfUnchanged` 版本（更安全：带代际校验）。
- 删除 `upload.Service` 上 **8** 个零调用者的 store/hash 转发方法（`SaveUpload`、`SaveUploadExact`、`UploadByPath`、`RemoveUploadsUnder`、`RenameUpload`、`RemoveStagingIfUnreferenced`、`HashRemoveUnder`、`HashRenamePath`）；调用方已统一走 `Service.Store()`。（实施期复核修正：候选中的 `RemoveUpload` 与 `HashRemovePath` 各有一个真实调用者，接收者是 `pkg/vfs/task_source.go` 里名为 `s.svc` 的字段，保留。）
- **BREAKING（in-tree API）** 删除 `internal/cli` 的 8 个转发/别名单文件（`fs_compat.go`、`fs_copy_compat.go`、`fs_crypt_compat.go`、`fs_read_compat.go`、`pending_compat.go`、`journal_compat.go`、`config_validation.go`、`build_info.go`），把 30 处测试引用改为直接使用 `internal/cli/fs`、`internal/cli/journal` 的符号，并把 `validateConfig`、`currentBuildInfo` 两个单调用点包装内联。
- 行为零变化：不删除任何**可达**路径、不改任何 wire 字段、不改持久化格式、不改错误分类；删除后必须有测试证明原有覆盖面不被削弱（迁移而非丢弃）。

## Capabilities

### New Capabilities

无。

### Modified Capabilities

无。本次是纯删除（无行为变化），因此 `.openspec.yaml` 声明 `skip_specs: true`，不产生 spec delta——规格描述行为，行为不变就不应该改规格。

## Impact

- `pkg/vfs/upload`：`store.go`（缓冲、记录器、10 个转发、`FlushStaging`）、`service.go`；相关测试（`stores_test.go`、`pending_atomic_test.go`、`staging_store_test.go` 等）需要迁移。
- `pkg/vfs`：`upload.go`/`read_host.go`（`FlushStaging` 链路）、`stores.go` 视引用情况一并清理。
- `pkg/vfs/read`：`host.go`（接口方法）、`reader.go`、`stream.go`、`runtime_test.go`、`test_helpers_test.go`、`health_test.go`、`observer_test.go`。
- `internal/cli`：8 个文件删除 + 约 30 处测试引用与两个包装的迁移。
- 风险：`FlushStaging` 的语义是"读取 staging 前把数据落盘"，其**意图**（读取未上传的 staged 字节前保证持久化）值得保留，但今天它不产生 fsync；把它改成真正的 `sync` 属于行为变化（每次 staged 读取多一次 fsync），不在本次范围（见 design Open Question）。
- 明确不做：`StoreAdapter` 的方法改名（它是 `pkg/vfs` 真正使用的 store 接缝，不是死代码）、`TargetIndex` 与 listing 缓存二合一、上传记录状态机/`Revision`、可观测性 DTO 收敛、`RemoveStagingIfUnreferenced` 的 O(n) 扫描加固、`pkg/vfs` 根包适配层收敛。
