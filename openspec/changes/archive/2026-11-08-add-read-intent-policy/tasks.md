## 1. 意图基础

- [x] 1.1 在 `pkg/vfs/read/helpers.go` 增加 `ReadIntent` 与 `WithReadIntent` / `ReadIntentFromContext`，验证：单元测试覆盖未声明、`bulk`、`interactive`、`probe` 四种取值。
- [x] 1.2 将意图沿 `Read`、`ReadStream`、`readChunkRange`、`loadWindow`、`fetchChunkWindow` 传递，验证：意图在每一层可读取的传递测试。
- [x] 1.3 确认未声明意图时现有读行为不变，验证：现有读路径测试全部保持通过。

## 2. 驱动默认策略与能力

- [x] 2.1 在 `pkg/drive` 定义驱动默认读策略（并行流数、窗口块数、预取深度），并为支持并行的驱动提供默认值，验证：表驱动测试覆盖默认值与能力缺失。
- [x] 2.2 在 `pkg/drive` 增加 `CapabilityParallelRange`、可选的 `ParallelReadLimiter` 与 `CapabilityInternalParallelRead`，为能保证子 Range 返回 206 的驱动声明前者，为 Quark 声明后者，验证：能力矩阵与契约测试。
- [x] 2.3 增加 `readPlan`（意图、并行流数、窗口块数、预取深度、是否写缓存），从意图与驱动默认策略派生，验证：表驱动测试覆盖默认值、驱动能力缺失与内部并行驱动。
- [x] 2.4 将驱动默认策略注入 `read.ReaderDeps`，验证：不同驱动到读层的接线测试。
- [x] 2.5 确认不新增用户可调配置，`ReadCacheConfig` 仍只有 `max_size`，验证：配置解析/schema 测试与文档检查。
- [x] 2.6 为测试/调试保留非用户配置的注入点，验证：测试可覆盖策略，生产配置无法设置。

## 3. 缓存准入

- [x] 3.1 在 `readPlan` 中加入缓存写入开关，并让 `StoreChunk` 按开关跳过持久缓存（热块保留当前读取所需），验证：显式 `bulk` 与 `probe` 读取后持久缓存目录无新块与索引。
- [x] 3.2 保持 `interactive` 与 FUSE 自适应 bulk 的缓存写入，验证：FUSE/移动端交互读仍产生缓存命中，顺序升级后重读仍命中。
- [x] 3.3 让 `ReadStream` 的显式 bulk 计划跳过缓存写入，验证：`fs get`/`fs sync` 路径不产生读缓存文件。
- [x] 3.4 确认 staging read-modify-write 继续通过 `driver.Read(0,0)` 绕过 VFS 缓存，不被新策略拉回缓存，验证：部分写路径不产生整文件缓存块。
- [x] 3.5 确认缓存容量为零时所有意图继续短路，验证：现有缓存禁用测试通过且无缓存文件。

## 4. 并行 Range

- [x] 4.1 在 `fetchChunkWindow` 内实现窗口拆分与并发子 Range 拉取，按 offset 拼接，验证：单元测试覆盖 2/4 路拆分、乱序完成与部分失败。
- [x] 4.2 为单文件增加“总并发子 Range”信号量（跨窗口共享），并与全局读槽位共同限流，验证：并发计数测试证明不超过单文件总计与全局上限。
- [x] 4.3 增加并行阈值（暂定 8 MiB），低于阈值保持单 Range，验证：小文件测试只观察到一次 `DriverRead`。
- [x] 4.4 确认后台预取窗口不并行，验证：预取测试中 `DriverRead` 并发数始终为 1。
- [x] 4.5 定义部分子 Range 失败的整窗语义：取消其余子 Range、释放槽位并让窗口失败，验证：一个子 Range 失败时不会返回部分窗口数据，重试可成功。
- [x] 4.6 明确 `windowLoad` 合并规则：只合并取数形态兼容的读，不兼容的等待者自行加载；等待者按自己的策略写缓存，验证：bulk loader + interactive waiter 的缓存写入与加载次数测试。

## 5. 驱动回退与内部并行

- [x] 5.1 实现驱动返回明确不支持错误时的单流回退，验证：fake 驱动返回 `ErrUnsupported`/typed error 时该文件后续窗口保持单流。
- [x] 5.2 回退后同一文件不再并行，验证：后续读窗口的 `DriverRead` 次数与单流一致。
- [x] 5.3 确认 VFS 不通过读取 body 长度或额外字节猜测 200 全量，验证：代码审查与 fake 驱动测试。
- [x] 5.4 确认 Quark 等内部并行驱动在 VFS 层保持单流，验证：该驱动上 `bulk` 读的 VFS 子 Range 数为 1，内部并发由驱动测试覆盖。

## 6. 生命周期取消

- [x] 6.1 增加按 `(cacheKey, sessionID)` 管理的读作用域注册表，验证：作用域创建、复用与取消的单元测试。
- [x] 6.2 让预取上下文从读作用域派生，替代 `WithoutCancel`，验证：取消作用域后预取 goroutine 退出。
- [x] 6.3 在读缓存失效时取消对应文件的预取作用域，验证：删除文件后不再产生新的远端读。
- [x] 6.4 在读会话释放时取消该会话注册的作用域，保留已完成的缓存填充，验证：关闭句柄后该文件的预取停止，已完成块仍可命中。
- [x] 6.5 在 VFS 关闭时取消全部作用域，验证：关闭后无遗留预取 goroutine（goleak/竞态测试）。
- [x] 6.6 保证取消路径关闭 `load.done` 并释放槽位与预取名额，验证：等待者不会永久阻塞，槽位计数回到零。
- [x] 6.7 为移动端 `CloseFileJSON` / `CancelFileReadJSON` 增加显式 VFS 预取取消入口，验证：没有前台读在途时关闭句柄也能停止预取。
- [x] 6.8 在前台读以取消错误返回时取消对应作用域，验证：CLI 中断后预取停止。

## 7. 公平性与优先级

- [x] 7.1 `bulk` 默认低优先级、`interactive` 默认高优先级，显式 `WithReadPriority` 覆盖意图默认值，验证：槽位竞争测试中交互读优先获得槽位，显式优先级测试中调用方设置生效。
- [x] 7.2 单文件总并发子 Range（跨窗口）不超过配置上限，多个 bulk 文件公平共享全局槽位，验证：双文件并发测试。
- [x] 7.3 bulk 不占用高优先级预留槽位，验证：高优先级槽位计数在 bulk 读取期间保持不变。
- [x] 7.4 并行子 Range 走驱动现有的请求限流/重试路径，验证：429 场景下 mount 的 rate budget 被共享，不因并行而绕过。

## 8. 前端接线

- [x] 8.1 FUSE 按 `AccessHint` 与顺序判定选择策略：持续顺序大文件升级 bulk，seek/随机/多句柄保持 interactive，验证：挂载适配器与读域联动测试。
- [x] 8.2 CLI 为 `fs get`、`fs sync`、`fs cat`、`offline fetch` 声明 bulk；只对真正经过 VFS 读的元数据路径声明 probe（`fs check` 通常只取声明 hash，不作为 probe 调用点），验证：CLI 测试断言意图进入读路径。
- [x] 8.3 移动端为 `ReadAtInto`、媒体虚拟读声明 interactive，为 `ReadDownloadItemInto` 声明 bulk，并在 `openFileOptions` 增加可选 `intent`；`CloseFileJSON` / `CancelFileReadJSON` 调用显式预取取消，验证：移动端读路径与关闭句柄测试。
- [x] 8.4 后台任务为 `download`、`download_stream`、`offline fetch` 声明 bulk，验证：任务执行测试断言并行与缓存策略。

## 9. 可观测性

- [x] 9.1 在 `drive.MetricEvent` 与 `vfstypes.DebugActiveOp` 追加意图、并行流数、预取取消原因字段，验证：JSON 序列化测试。
- [x] 9.2 在读事件与阶段明细中填充新增字段：汇总事件记录最大流数，窗口明细记录该窗口实际流数；未声明意图记录空值，验证：bulk/interactive/probe 与取消事件各自携带预期值。
- [x] 9.3 区分后台预取取消与前台读失败：预取取消记录取消结果与原因，前台取消保持现有错误/取消语义，验证：事件状态与 health/counter 测试。
- [x] 9.4 确认 `/v1/reads` 与调试快照暴露新增字段且旧字段不变，验证：control 与 debug 快照测试。

## 10. 跨文件并发

- [x] 10.1 在 `fs get` 目录路径增加有界 worker 池（固定默认 4，不新增用户配置），验证：多文件目录测试证明并发数不超过 4 且进度单调。
- [x] 10.2 在 `fs sync` 执行器增加有界 worker 池，验证：同步多文件测试证明并发与顺序语义正确。
- [x] 10.3 跨文件 worker 与单文件并行共享全局槽位、单文件总并发上限独立生效，验证：大文件 + 多小文件并发测试。
- [x] 10.4 取消时停止未开始的 worker 并释放在途槽位，验证：取消测试中不再启动新文件且槽位归零。
- [x] 10.5 固定默认 worker 数为 4，不新增用户配置，验证：默认值测试与配置/schema 检查。

## 11. ReadStream 跨窗口预取

- [x] 11.1 在 `chunkedStreamReader` 中预取下一个窗口，验证：窗口边界无停顿的吞吐测试。
- [x] 11.2 预取深度有界（默认 1），内存不随文件大小增长，验证：大文件流式读取的内存上界测试。
- [x] 11.3 取消或关闭读会话时停止在途预取并释放槽位，验证：取消流测试。
- [x] 11.4 流缓存写入遵循读意图：显式 bulk 不写、interactive 写，验证：缓存目录断言。

## 12. 直读路径并行

- [x] 12.1 在 `pkg/drive` 提供共享的并行 Range helper（驱动能力、默认流数、单文件总上限、取消），验证：helper 单元测试。
- [x] 12.2 让 drivecopy（`fs copy` / `copy` 任务）使用该 helper，验证：大文件并行、按 offset 拼接、不写 VFS 缓存。
- [x] 12.3 让 `share fetch` 的 `driver.Read` 回退使用该 helper，验证：回退路径并行与取消。
- [x] 12.4 直读路径使用独立有界信号量，默认与 VFS 单文件上限一致，验证：并发计数测试不超过上限。
- [x] 12.5 Quark 等内部并行驱动的直读保持单流，验证：VFS 子 Range 数为 1，不出现叠加。

## 13. 文档与整体验证

- [x] 13.1 更新 `docs/for-user/full-config.md`、`docs/for-developer/architecture.md`、`docs/for-developer/driver-development.md` 的能力与读策略说明，并在 changelog/release notes 说明默认并行与缓存行为变化，验证：相对链接有效、`git diff --check` 通过。
- [x] 13.2 增加一个端到端测试：bulk 下载触发并行 Range、跳过持久缓存、取消后预取停止，验证：集成测试在冷缓存下通过。
- [x] 13.3 增加 Quark 内部并行与 VFS 并行的互斥回归测试，验证：Quark 上 VFS 子 Range 数为 1，不出现 4×3 叠加。
- [x] 13.4 使用 CI 相同的 Go 版本运行 `scripts/ci/check-all.sh`，验证：退出码为 0 且输出 `== local CI check passed ==`。
- [x] 13.5 对所有改动的 Go 文件运行 `GOFMT="$(go env GOROOT)/bin/gofmt"` 的 `-w` 与 `-l`，验证：`gofmt -l` 无输出。
- [x] 13.6 重新运行 `openspec validate "add-read-intent-policy" --type change --strict`，验证：输出 `Change 'add-read-intent-policy' is valid`。
