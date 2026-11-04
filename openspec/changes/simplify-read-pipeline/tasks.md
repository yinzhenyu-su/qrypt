# Tasks

> 2026-11-04 设计验证后缩小范围：下一阶段只做不可变 `ReadPlan`、`ScopeRegistry` 和 `CachePolicy`。`Scheduler`、`WindowRegistry`、共享取数层、文件拆分/测试接缝删除推迟到证据变化后再评估；理由见 `design.md` 的 Validation Results。

## 0. 设计验证（已完成）

- [x] 0.1 取消登记一致性：覆盖 invalidate / session-release / legacy seek / close，断言两套登记同时清空且 driver 读全部取消。验证：`go test ./pkg/vfs/read -run TestPrefetchRegistriesDrainTogether` 通过。
- [x] 0.2 调度不变量：覆盖全局槽位、每文件许可、预取名額和 32×100 并发 acquire/release，断言不超限、不泄漏。验证：`go test ./pkg/vfs/read -run TestSchedulerCapacityInvariants` 通过。
- [x] 0.3 `findWindow` 基准：1/10/100 个在途窗口约 31/80/450 ns/op、0 allocs。验证：`go test -run '^$' -bench BenchmarkFindWindow -benchmem ./pkg/vfs/read`。
- [x] 0.4 双并行策略矩阵：parallel / no-capability / internal-parallel / strict-unsupported 四种情况下 VFS 与 `drive.ReadParallel` 判定一致。验证：`go test ./pkg/vfs/read -run TestParallelPolicyMatchesDirectRead` 通过。
- [x] 0.5 范围决策：采用最小可行收敛，推迟项和重新评估触发条件记录在 `design.md`。验证：`openspec validate simplify-read-pipeline --type change --strict` 通过。

## 1. 不可变 ReadPlan（已完成）

- [x] 1.1 新增 `plan.go`：定义 `ReadPlan`、`LoadKind`、`PlanResolver.Resolve`，用显式默认值替代 `0` / `-1` 哨兵；为四种意图与未知意图写表驱动单测。验证：`plan_test.go` 与 `go test ./pkg/vfs/read -run TestPlanResolver` 通过。
- [x] 1.2 用 `PlanResolver` 替换 `planFor`、`readPlan` 及所有 `readPlanFromContext` 调用点；未声明意图的行为与基线一致。验证：`go test ./pkg/vfs/read ./pkg/vfs ./pkg/mobile ./pkg/core ./pkg/mount ./pkg/syncer -count=1` 通过。
- [x] 1.3 把 FUSE 自适应升级改为返回新 `ReadPlan` 并一次性写回 context，禁止原地修改；增加“升级后所有阶段读到同一计划”的测试。验证：`TestHintedSequentialLargeReadUpgradesToBulk` 断言 bulk/4/cache 且保留 FUSE 窗口。
- [x] 1.4 删除计划字段的隐式默认分支（如 `windowChunks == 0` 表示沿用旧值），并在 `Read` / `ReadStream` 入口断言计划已解析。验证：`TestPlanResolverUnspecifiedUsesConcreteDefaults` 与 `TestPlanResolverFallsBackWhenProfileWindowIsZero` 覆盖；grep 不再出现哨兵判断。

## 2. ScopeRegistry

- [ ] 2.1 新增 `scope.go`：定义 `(cacheKey, sessionID)` 键、引用计数、取消原因和 `CancelFile` / `CancelSession` / `CancelAll`；单测覆盖 refs 归零、重复取消和原因读取。验证：`go test ./pkg/vfs/read -run TestScopeRegistry` 通过。
- [ ] 2.2 用 `ScopeRegistry` 替换 `scopeState` 与 `prefetchState.cancels/reasons`；诊断从 registry 读取取消原因。验证：`TestPrefetchRegistriesDrainTogether` 与现有取消矩阵通过。
- [ ] 2.3 接线 `InvalidateFile`、`ReleaseReadSession`、`CancelReadPrefetch`、移动端关闭/取消和 VFS `Close`，确保按文件、按会话、全量取消各自精确。验证：VFS、FUSE adapter、移动端生命周期测试全部通过。
- [ ] 2.4 删除 `prefetchState` 的取消登记与 `inFlight`；前台读计数暂时保留为独立小状态，等待后续调度器评估。验证：编译通过，grep 不再出现 `prefetch.cancels` / `prefetch.reasons`。

## 3. CachePolicy

- [ ] 3.1 新增 `cache_policy.go`：定义 `Admit` 与 `Lookup`，热块始终写入，持久缓存由 `ReadPlan.CacheWrites` 决定；单测覆盖准入矩阵。验证：`go test ./pkg/vfs/read -run TestCachePolicy` 通过。
- [ ] 3.2 用 `CachePolicy.Admit` 替换 `readerBackend.StoreChunk` 与 `durableCacheWrites`；前台窗口、并行子 Range、预取、`ReadStream` read-ahead 全部走同一入口。验证：`cache_admission_test.go` 全部通过。
- [ ] 3.3 把 `readChunkRange` 中直接命中与 `waitWindow` 后的重复查找分支收敛为一次 `Lookup` 调用。验证：缓存命中/提升测试通过，行为与基线一致。
- [ ] 3.4 增加“并行子 Range 不写持久缓存但热块可用”的回归测试。验证：`go test ./pkg/vfs/read -run TestParallelCacheAdmission` 通过。

## 4. 文档与最终门禁

- [ ] 4.1 更新 `docs/for-developer/architecture.md` 的读路径章节，描述本阶段后的计划解析、作用域和缓存准入边界。验证：文档中每个组件与代码文件对应，相对链接有效。
- [ ] 4.2 如缓存准入或作用域边界改变驱动作者可见的行为，同步更新 `docs/for-developer/driver-development.md`。验证：文档描述与实际契约一致。
- [ ] 4.3 在 `CHANGELOG.md` 的 Unreleased 记录读管线收敛（无用户可见行为变化）。验证：`git diff --check` 通过。
- [ ] 4.4 对所有改动的 Go 文件运行 `GOFMT="$(go env GOROOT)/bin/gofmt"; "$GOFMT" -w <files>`，并确认 `"$GOFMT" -l <files>` 无输出。验证：命令无输出。
- [ ] 4.5 运行 `go vet ./...` 与仓库使用的 staticcheck 命令。验证：两者退出码为 0。
- [ ] 4.6 运行 `go test -race -count=10 ./pkg/vfs/read/... ./pkg/drive/...` 与 goleak 覆盖的 VFS 生命周期测试。验证：全部通过，无泄漏。
- [ ] 4.7 重跑 `BenchmarkFindWindow` 与阶段 0 的基准；若吞吐或分配回归超过 10%，在提交说明中给出原因或修复。验证：基准结果已记录。
- [ ] 4.8 使用与 `.github/workflows/ci.yaml` 相同的 Go 版本运行 `scripts/ci/check-all.sh`。验证：退出码为 0 且输出包含 `== local CI check passed ==`。
- [ ] 4.9 运行 `openspec validate simplify-read-pipeline --type change --strict`。验证：输出 valid。

## 5. Deferred（证据变化后再评估）

- [ ] 5.1 `WindowRegistry`：`findWindow` 基准显示 100 个在途窗口约 450 ns/op；只有真实负载超过 10 µs/op 或出现重复下载时才实现 O(1) 注册表。
- [ ] 5.2 `Scheduler`：不变量测试未发现超限/泄漏；只有出现跨池超限、公平性问题或新的并发预算需求时才合并四套预算。
- [ ] 5.3 共享取数层：VFS 与 `drive.ReadParallel` 当前策略一致；只有出现策略漂移或需要同时修改时才抽取 `SplitRanges` / `ReadSpan` / `IsUnsupported`。
- [ ] 5.4 测试接缝删除与文件拆分：属于阅读性/维护性收益；等不可变计划、作用域和缓存准入稳定后单独做。
- [ ] 5.5 `Read` / `ReadStream` 共享前置阶段：与文件拆分一起评估，避免在逻辑未稳定时引入新的入口抽象。
