## Why

`add-read-intent-policy` 给读域加上了意图、并行 Range、跨文件 worker、生命周期取消和缓存准入，但每项能力都各自带来一套状态与并发机制。当前 `pkg/vfs/read` 已经同时存在：

- 9 个子状态（`cache`、`history`、`prefetch`、`sequence`、`slots`、`fastPath`、`windows`、`parallel`、`scopes`）；
- 4 套独立并发限制（全局读槽位、每文件并行上限、预取信号量、直读全局池）；
- 2 套预取取消登记（`prefetch.cancels/reasons` 与 `scopeState`）；
- 2 套并行取数实现（VFS 窗口内并行、`drive.ReadParallel`）；
- 4 处重复的缓存查找/提升分支。

这些机制单独看都有测试，组合起来却难以推理：一个窗口可能同时受 3 个信号量约束，一次取消要在 2 个注册表里保持一致，VFS 与直读路径对同一驱动可能做出不同的并行/回退判断。结果是竞态、槽位泄漏、取消遗漏和重复下载难以在 review 阶段发现。继续叠加读功能之前，需要先把读域收敛成一条管线。

## What Changes

- 引入不可变 `ReadPlan`：一次读只解析一次意图、流数、窗口、预取深度、缓存准入和优先级；后续阶段只读该计划。自适应升级产生新计划并通过 context 传递，不再原地修改局部变量。
- 合并并发限制为单一 `Scheduler` 实现：统一管理全局槽位、高优先级预留、每文件并行上限和预取名额。VFS 挂载与直读路径各自持有预算相同的实例，保留现有配额，不改变对外并发行为。
- 合并生命周期登记为单一 `ScopeRegistry`：以 `(cacheKey, sessionID, handle)` 为键，删除 `prefetch.cancels` / `prefetch.reasons` 的第二份登记；seek、失效、会话释放、句柄关闭、调用方取消和 VFS 关闭走同一取消入口。
- 合并窗口合并表为 `WindowRegistry`：使用结构化键 `(cacheKey, start, end, streams)` 做 O(1) 查找与合并，替代遍历 `windows.loads`；取数形态不兼容的等待者仍然各自发起读取。
- 合并缓存准入为 `CachePolicy`：热块与持久缓存是否写入由同一个 `Admit` 决定，前台窗口、并行子 Range、预取和 `ReadStream` read-ahead 不再各自判断。
- 抽取共享取数管线：range 拆分、按 offset 拼接、strict-range 校验、`ErrUnsupported` 单流回退由一份实现提供，VFS 窗口与 `drive.ReadParallel` 只做薄适配。
- 删除测试专用间接层（`readRuntime`、`readWindowBackend`、`stateRuntime`），测试直接用 `Host` fake 驱动；只保留跨包必要的接口。
- 拆分 1000 行级文件：`reader.go` 只保留读入口与编排，`state.go` 只保留状态装配；新增 `plan.go`、`scheduler.go`、`window_registry.go`、`scope.go`、`cache_policy.go`、`fetch.go`。

行为不变：本变更以 characterization 测试先行，所有现有读语义、公开 API、配置面、缓存格式和驱动契约保持不变。

2026-11-04 设计验证后缩小下一阶段范围：只实施不可变 `ReadPlan`、`ScopeRegistry` 和 `CachePolicy`；`Scheduler`、`WindowRegistry`、共享取数层和文件拆分推迟。验证结果、推迟理由和重新评估触发条件见 `design.md` 的 Validation Results。

2026-11-04 后续测试发现 per-file limiter 的 overcommit 风险：同一文件的并发窗口如果 `streams` 不同，`parallelState` 会替换 live channel，旧/新 channel 的 permit 可能同时存在。这触发了把 `Scheduler` 提前到下一阶段：先稳定每文件上限，再实现统一 Scheduler，并把 `ReadPlan`（策略）与 Scheduler 的实际 grant（调度）分离。`WindowRegistry`、共享取数层和文件拆分仍推迟。per-file 修复是恢复既有 `read-pipeline` 的单文件并发上限契约，不新增规格，因此 `.openspec.yaml` 继续保持 `skip_specs: true`。

## Capabilities

### New Capabilities

无。

### Modified Capabilities

无。这是行为保持的结构性收敛：现有 `read-pipeline`（由 `add-read-intent-policy` 引入）描述的行为就是本次必须保住的契约，因此不产生 spec delta，`.openspec.yaml` 声明 `skip_specs: true`。

## Impact

- `pkg/vfs/read`：`reader.go`、`state.go`、`stream.go`、`helpers.go` 重排为按职责拆分的文件；窗口注册、调度、作用域、缓存准入和取数逻辑收敛。
- `pkg/drive`：`parallel_read.go` 改为共享取数管线的薄封装；新增共享 range fetch 实现。
- `pkg/vfs`：`read_host.go` 的装配不变，只跟随内部类型调整；移动端与 mount 的公开调用点不变。
- 测试：新增 characterization 测试套件，覆盖窗口、合并、并行、回退、取消、缓存准入、流预取和诊断字段；现有测试除适配内部类型外不得放宽断言。
- 文档：更新 `docs/for-developer/architecture.md` 的读路径说明；`CHANGELOG.md` 记录内部收敛。
- 不新增用户配置，不改变 `read_cache.max_size`，不改变持久缓存索引格式，不改变 `drive.Driver.Read` 契约。
- 依赖：`add-read-intent-policy` 必须先应用/合并；本变更在其之上做纯结构收敛。
