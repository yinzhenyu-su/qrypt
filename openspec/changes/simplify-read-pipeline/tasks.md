## 1. Characterization 基线（不改生产代码）

- [ ] 1.1 新增脚本化 `Host` fake，记录每次 `DriverRead` 的 offset/size、最大并发和调用次数；用它覆盖对齐、非对齐、跨块、EOF、未知大小、`size=0` 的 `Read` 行为。验证：`go test ./pkg/vfs/read -run 'TestCharacterizationRead' -count=1` 通过。
- [ ] 1.2 覆盖窗口合并：N 个并发同范围读只触发 1 次 `DriverRead`；bulk 并行窗口与 interactive 单流窗口不共享；每个等待者拿到正确字节。验证：对应 characterization 测试通过，且 fake 记录的调用数符合断言。
- [ ] 1.3 覆盖并行取数：大文件 bulk 拆分、每文件并发上限、全局槽位上限、`CapabilityInternalParallelRead` 保持单流、strict-range `ErrUnsupported` 回退单流且不产生 N 次全量下载。验证：并发计数 fake 的断言通过。
- [ ] 1.4 覆盖取消矩阵：seek、`InvalidateFile`、`ReleaseReadSession`、调用方取消、`CancelReadPrefetch`、VFS `Close` 分别停止对应预取；无关文件/会话不受影响；已完成缓存填充保留；槽位全部释放。验证：取消测试与 `goleak` 通过。
- [ ] 1.5 覆盖缓存准入矩阵：`bulk` / `probe` 不写持久缓存，`interactive` 与 FUSE 自适应 bulk 写入，热块始终写入，`ReadStream` read-ahead 遵守同一计划。验证：`Cache` fake 的写入记录符合矩阵。
- [ ] 1.6 覆盖诊断字段：汇总与明细事件的 `read_intent`、`read_streams`、`prefetch_cancel_reason`；后台预取取消不计为失败，前台取消仍按失败/取消语义记录。验证：诊断断言测试通过。
- [ ] 1.7 记录基线：运行 `go test -race -count=10 ./pkg/vfs/read/... ./pkg/drive/...`、`go test ./pkg/vfs -run 'TestLifecycle|TestGoleak' -count=1` 和 `go test -bench 'BenchmarkVFSRead|BenchmarkFetchChunk' -benchmem ./pkg/vfs ./pkg/vfs/read`，把结果写入提交说明作为后续对比基线。验证：命令退出码为 0，输出已记录。

## 2. 不可变 ReadPlan

- [ ] 2.1 新增 `plan.go`：定义 `ReadPlan`、`LoadKind`、`PlanResolver.Resolve`，用显式默认值替代 `0` / `-1` 哨兵；为四种意图与未知意图写表驱动单测。验证：`go test ./pkg/vfs/read -run TestPlanResolver` 通过。
- [ ] 2.2 用 `PlanResolver` 替换 `planFor`、`readPlan` 及所有 `readPlanFromContext` 调用点；未声明意图的行为与基线一致。验证：全部 characterization 测试通过。
- [ ] 2.3 把 FUSE 自适应升级改为返回新 `ReadPlan` 并一次性写回 context，禁止原地修改；增加“升级后所有阶段读到同一计划”的测试。验证：测试断言窗口、取数、缓存、诊断使用相同 `Streams` / `CacheWrites`。
- [ ] 2.4 删除计划字段的隐式默认分支（如 `windowChunks == 0` 表示沿用旧值），并在 `Read` / `ReadStream` 入口断言计划已解析。验证：`go test ./pkg/vfs/read -count=1` 通过，grep 不再出现哨兵判断。

## 3. WindowRegistry

- [ ] 3.1 新增 `window_registry.go`：定义结构化 `windowKey` 与 `Join` / `Register` / `Find` / `Finish`，覆盖精确匹配、范围覆盖、shape 不兼容和注销。验证：`go test ./pkg/vfs/read -run TestWindowRegistry` 通过。
- [ ] 3.2 用 `WindowRegistry` 替换 `windowState`、`beginWindowLoad`、`findWindow`、`endWindowLoad`；`done` 仍先关闭再注销。验证：窗口合并 characterization 测试通过。
- [ ] 3.3 预取去重改用注册表覆盖判断，删除 `prefetchState.inFlight`；增加“同范围预取只启动一次”的测试。验证：测试通过且 fake 调用数为 1。
- [ ] 3.4 增加注册/注销与查找并发的压力测试。验证：`go test -race -run TestWindowRegistryConcurrent -count=10 ./pkg/vfs/read` 通过。

## 4. ScopeRegistry

- [ ] 4.1 新增 `scope.go`：定义 `(cacheKey, sessionID)` 键、引用计数、取消原因和 `CancelFile` / `CancelSession` / `CancelAll`；单测覆盖 refs 归零、重复取消和原因读取。验证：`go test ./pkg/vfs/read -run TestScopeRegistry` 通过。
- [ ] 4.2 用 `ScopeRegistry` 替换 `scopeState` 与 `prefetchState.cancels/reasons`；诊断从 registry 读取取消原因。验证：取消矩阵 characterization 测试通过。
- [ ] 4.3 接线 `InvalidateFile`、`ReleaseReadSession`、`CancelReadPrefetch`、移动端关闭/取消和 VFS `Close`，确保按文件、按会话、全量取消各自精确。验证：对应测试通过，且“取消一个文件不影响另一个文件”断言成立。
- [ ] 4.4 删除 `prefetchState` 的取消登记与 `inFlight`；前台读计数暂时保留为独立小状态，等待阶段 5 迁入调度器。验证：编译通过，`grep -R "prefetch.cancels\\|prefetch.reasons" pkg/vfs/read` 无结果。

## 5. Scheduler

- [ ] 5.1 新增 `scheduler.go`：定义 `Scheduler`、`Request`、`LoadKind` 和容量配置；单测覆盖全局上限、高优先级预留、每文件上限、预取名額与优先级。验证：`go test ./pkg/vfs/read -run TestScheduler` 通过。
- [ ] 5.2 前台单流窗口与并行子 Range 改用 `Scheduler.Acquire`；删除 `slotState` 与 `parallelState` 的直接使用。验证：并行与公平性 characterization 测试通过。
- [ ] 5.3 预取名額与前台读计数迁入 `Scheduler`；删除 `prefetchState.sem` 与临时前台计数状态。验证：预取行为测试通过，`grep -R "slots.normal\\|parallel.files\\|prefetch.sem" pkg/vfs/read` 无结果。
- [ ] 5.4 为直读路径提供进程级 `Scheduler` 实例，替换 `directReadSlotPool`，容量仍为 16；增加直读并发上限测试。验证：`go test ./pkg/drive -run TestReadParallelSlots` 通过。
- [ ] 5.5 增加取消等待、重复释放和完成/取消竞争测试。验证：`go test -race -run TestScheduler -count=10 ./pkg/vfs/read ./pkg/drive` 通过，无 goroutine 泄漏。

## 6. CachePolicy

- [ ] 6.1 新增 `cache_policy.go`：定义 `Admit` 与 `Lookup`，热块始终写入，持久缓存由 `ReadPlan.CacheWrites` 决定；单测覆盖准入矩阵。验证：`go test ./pkg/vfs/read -run TestCachePolicy` 通过。
- [ ] 6.2 用 `CachePolicy.Admit` 替换 `readerBackend.StoreChunk` 与 `durableCacheWrites`；前台窗口、并行子 Range、预取、`ReadStream` read-ahead 全部走同一入口。验证：缓存准入 characterization 测试通过，grep 不再出现分散的 `cacheWrites` 判断。
- [ ] 6.3 把 `readChunkRange` 中直接命中与 `waitWindow` 后的重复查找分支收敛为一次 `Lookup` 调用。验证：缓存命中/提升测试通过，行为与基线一致。
- [ ] 6.4 增加“并行子 Range 不写持久缓存但热块可用”的回归测试。验证：`go test ./pkg/vfs/read -run TestParallelCacheAdmission` 通过。

## 7. 共享取数层

- [ ] 7.1 在 `pkg/drive` 新增 `SplitRanges`、`ReadSpan`、`IsUnsupported`；单测覆盖均分、余数、单 span、strict 错误和超限读取。验证：`go test ./pkg/drive -run TestSplitRanges|TestReadSpan` 通过。
- [ ] 7.2 VFS `fetchChunkWindow` 改用共享拆分与 span 读取，保留按 chunk 缓存准入与诊断 extra。验证：并行 characterization 与诊断测试通过。
- [ ] 7.3 `drive.ReadParallel` 改用共享层，保留 8 MiB part、16 槽位和流式写出顺序。验证：`go test ./pkg/drive -run TestReadParallel` 通过。
- [ ] 7.4 增加一致性测试：同一 driver profile 下，VFS 与直读路径对单流/并行/回退做出相同决策。验证：测试断言两条路径的 `DriverRead` 调用数与是否 strict 一致。

## 8. 测试接缝与入口前置阶段

- [ ] 8.1 删除 `readRuntime` 与 `stateRuntime`，把使用它们的测试改为 `Host` fake。验证：`go test ./pkg/vfs/read -count=1` 通过，grep 无残留。
- [ ] 8.2 删除 `readWindowBackend`，把取数测试改为 `Host` + `Cache` fake。验证：取数与缓存测试通过。
- [ ] 8.3 抽取 `openRead(ctx, kind, path)`，让 `Read` 与 `ReadStream` 共享 staging、resolve、目录检查、计划解析、优先级和诊断开始；保留各自缓冲语义。验证：两个入口的 characterization 测试通过。
- [ ] 8.4 迁移而非删除现有故障注入测试，确认 `Host` / `Cache` fake 仍能注入读取失败、缓存失败和取消。验证：故障测试全部通过，覆盖率不下降。

## 9. 文件拆分

- [ ] 9.1 把 `helpers.go` 拆为 `plan.go`、`context.go`、`keys.go`。验证：编译与 `go test ./pkg/vfs/read -count=1` 通过。
- [ ] 9.2 把 `state.go` 的 history、hot chunk、access tracker 拆为独立文件，`state.go` 只保留装配与生命周期。验证：编译通过，`go test ./pkg/vfs -run TestLifecycle` 通过。
- [ ] 9.3 把 `reader.go` 拆为 `chunk.go`、`fetch.go`、`prefetch.go`、`read.go`，`reader.go` 只保留入口编排与依赖装配。验证：编译与全部读测试通过。
- [ ] 9.4 确认没有新增子包或循环依赖。验证：`go list -deps ./pkg/vfs/read` 正常，`go build ./...` 通过。
- [ ] 9.5 逐文件复核职责：每个可变资源只有一个所有者，不存在第二份取消/并发/缓存登记。验证：review checklist 全部打勾并记录在提交说明。

## 10. 文档与最终门禁

- [ ] 10.1 更新 `docs/for-developer/architecture.md` 的读路径章节，描述新管线、资源所有权和阶段边界。验证：文档中每个组件与代码文件对应，相对链接有效。
- [ ] 10.2 如共享取数层改变驱动作者的并行说明，同步更新 `docs/for-developer/driver-development.md`。验证：文档描述与 `drive.SplitRanges` / `ReadSpan` 实际契约一致。
- [ ] 10.3 在 `CHANGELOG.md` 的 Unreleased 记录内部读管线收敛（无用户可见行为变化）。验证：`git diff --check` 通过。
- [ ] 10.4 对所有改动的 Go 文件运行 `GOFMT="$(go env GOROOT)/bin/gofmt"; "$GOFMT" -w <files>`，并确认 `"$GOFMT" -l <files>` 无输出。验证：命令无输出。
- [ ] 10.5 运行 `go vet ./...` 与仓库使用的 staticcheck 命令。验证：两者退出码为 0。
- [ ] 10.6 运行 `go test -race -count=10 ./pkg/vfs/read/... ./pkg/drive/...` 与 goleak 覆盖的 VFS 生命周期测试。验证：全部通过，无泄漏。
- [ ] 10.7 重跑阶段 0 的基准并对比；若吞吐或分配回归超过 10%，在提交说明中给出原因或修复。验证：基准结果已记录。
- [ ] 10.8 使用与 `.github/workflows/ci.yaml` 相同的 Go 版本运行 `scripts/ci/check-all.sh`。验证：退出码为 0 且输出包含 `== local CI check passed ==`。
- [ ] 10.9 运行 `openspec validate simplify-read-pipeline --type change --strict`。验证：输出 valid。
