## Why

当前 VFS 读路径把「读多少、并发多少、预取多深、何时停」固化为 `pkg/vfs/read` 的编译期常量，所有前端共用一套单流窗口策略。两个方向都不可控：

- 单流限速的网盘（Koofr 实测约 300KB/s）无法通过同一文件的多 Range 并发跑满带宽；预取窗口之间虽有并发，但拉的是不重叠区段，不是对同一请求的并行加速。
- 文件被删除或句柄关闭后，脱离调用方上下文的预取仍会继续下载最多约 32MB；取消入口只有 seek。

两者是同一缺失的两面：读 I/O 缺少显式的「意图 + 生命周期」边界。只增加一个全局并行开关会同时影响交互读延迟和小文件探测开销，因此需要按场景选择策略，而不是按读 API 一刀切。

## What Changes

- 引入读意图 `bulk` / `interactive` / `probe`，由 FUSE、CLI、移动端和后台任务在发起读时声明，并沿 context 传播到读调度层。
- `bulk` 意图在单个读窗口内做有界并行 Range；`interactive` / `probe` 保持单流、小窗口、有界预取。
- 为 `fs get` 目录和 `fs sync` 增加有界跨文件 worker 池；为 `ReadStream` 增加跨窗口 read-ahead，使单文件持续吞吐不被窗口边界打断。跨文件 worker 固定默认 4，不新增用户配置。
- 读策略默认由驱动层决定（并行流数、窗口块数、预取深度），不要求用户配置；驱动声明并行 Range 能力与推荐值，已实现内部并行的驱动（如 Quark）由 VFS 保持单流，避免叠加。后续 `add-read-profile-override` 在不改变默认值的前提下提供可选的 per-mount override。
- 为 `fs copy` / `copy` 任务 / `share fetch` 的 `driver.Read` 直读路径增加有界并行 Range，复用同一驱动能力与默认值，并保持绕过 VFS 缓存。
- 并行 Range 的语义由驱动保证：声明能力的驱动必须让子 Range 返回 206，无法满足时返回明确的不支持错误；VFS 不通过读取 body 猜测 200 全量。
- 预取取消接入生命周期：读缓存失效、读会话释放、调用方取消都会停止该文件的后台预取；seek 取消保留。已完成的缓存填充不因会话释放而丢弃。
- 读意图决定持久读缓存写入：显式 `bulk` 与 `probe` 默认跳过持久缓存写入，`interactive` 保留；FUSE 顺序会话自适应升级后仍保留缓存，避免挂载内重读全部 miss。
- 公平性：`bulk` 默认低优先级，`interactive` 默认高优先级，但调用方显式 `ReadPriority` 优先；单文件并行上限按该文件总并发子 Range 计，不按窗口计，并继续复用全局读槽位。
- 诊断事件记录读意图、实际流数与预取取消原因。
- 非目标：不改变读缓存淘汰策略；不改变 `drive.Driver.Read` 契约；不以服务端复制替代本地读；`ReadRaw` 保持原样。

## Capabilities

### New Capabilities

- `read-pipeline`: 读意图、驱动默认读策略、窗口内并行 Range、直读路径并行、Range 回退、生命周期取消、跨文件并发、流预取与优先级公平性。

### Modified Capabilities

- `observability`: 读事件与调试快照追加读意图、并行流数和预取取消原因，并区分后台预取取消与前台读失败。
- `read-cache`: 明确缓存准入由读意图决定，淘汰策略只约束已准入内容；`bulk` / `probe` 不进入持久缓存。

## Impact

- `pkg/vfs/read`: `reader.go`、`state.go`、`stream.go`、`helpers.go`；窗口加载、预取注册/取消、槽位与意图传播。
- `pkg/vfs/readcache`: 持久缓存准入路径，按意图决定是否写入。
- `pkg/vfs`: `read_host.go`、`vfs.go` 读缓存失效、`namespace_read.go` 会话释放。
- `pkg/drive` / `pkg/drivers`: 并行 Range 能力声明、驱动默认读策略、可选的驱动上限接口，以及 Quark 等已内部并行驱动的互斥处理。
- `pkg/vfs/drivecopy` / `pkg/core/share_fetch.go`: 直读路径的有界并行 Range 与取消。
- 配置：默认不新增用户可调读策略配置；`ReadCacheConfig` 继续只承载 `max_size`。后续 `add-read-profile-override` 增加可选的 per-mount override，默认值仍由驱动拥有。
- `pkg/mount`: FUSE 读意图与顺序/随机判定接线。
- `internal/cli/fs` / `pkg/syncer`: `fs get` 目录与 `fs sync` 的有界跨文件 worker 池、进度与取消。
- `pkg/core` / `pkg/mobile`: `ReadAtInto`、媒体虚拟读、下载任务、下载流任务的意图声明，以及移动端句柄关闭/取消对预取的显式停止。
- 文档与测试：`docs/for-user/full-config.md`、`docs/for-developer/architecture.md`、`openspec/specs/read-cache` 的准入说明，以及读域/挂载/移动端/配置测试。
