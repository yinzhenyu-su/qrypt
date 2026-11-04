## Why

真实云盘测试（115 挂载，64 MiB 冷读）暴露了一个健壮性缺口：下载连接在读到约 17.5 MiB 后停住，`Reader.waitWindow` 无限等待，FUSE `Read` 一直阻塞，随后挂载点上的其他操作也不再响应，只能强杀进程恢复；探针本身也没有整体超时，测试挂了 20 分钟以上。

根因是读路径上没有任何取数超时：

- FUSE 回调传入的 context 没有 deadline。
- 驱动 HTTP client 没有响应头超时，也没有 body 空闲超时。
- 读层等待窗口加载（`waitWindow`）和预取 goroutine 都只依赖 context 取消，调用方不取消就永远等待。
- 一个停住的 provider 连接会占住读槽位和窗口加载状态，拖住同文件后续读，并让 cgofuse 的读回调堆积。

这与已完成的读管线简化是同一类问题：读的「生命周期」只有取消，没有「失败边界」。取消解决“不需要了”，超时解决“不会来了”。

## What Changes

- 为前台窗口取数和后台预取增加**取数超时**：body 空闲超时（主）+ 单次 fetch 总上限（兜底）。
- 超时通过 context 中止 body 读取，释放调度槽位/预取许可，并让窗口等待者得到超时错误，而不是继续等待。
- 超时后允许一次幂等重试（Range GET 可安全重放）；已开始拼接的部分窗口不重试，整窗失败。
- 超时错误归入现有 `drive.ErrorCategoryTimeout`；FUSE 返回 EIO；CLI/探针报告 timeout 而不是挂起。
- 预取超时静默处理，只记事件，不向调用方返回错误；前台读超时返回错误。
- 诊断事件记录取消原因 `timeout`、等待耗时、取数耗时和超时次数。
- 不新增用户可调配置；默认值由驱动读配置（`drive.ReadProfile`）派生，慢速驱动可以覆盖。
- 非目标：不改变上传超时；不引入通用重试框架；不改变成功读的语义和缓存准入；不改 `drive.Driver.Read` 契约。

## Capabilities

### New Capabilities

无。

### Modified Capabilities

- `read-pipeline`: 增加窗口取数的超时、重试与失败语义，以及预取超时的静默取消。
- `observability`: 读事件与调试快照增加超时取消原因、等待/取数耗时和超时计数。

## Impact

- `pkg/vfs/read`: `reader.go` 的 `fetchChunkWindowWithPlan`、`readWindowRange`、`ReadAllLimited`、`waitWindow`、`prefetchWindow`；`stream.go` 的窗口 refill。
- `pkg/drive`: `ReadProfile` 增加超时字段；错误分类复用 `ErrorCategoryTimeout`。
- `pkg/drivers`: 驱动 HTTP client 的响应头超时与 body 空闲超时；慢速驱动覆盖默认值。
- `pkg/vfs/drivecopy`、`pkg/core`：直读路径的超时与取消一致性。
- `pkg/contracttest`：读探针在超时后返回结构化失败，而不是无限等待。
- 配置：不新增用户可调项。
- 文档与测试：`docs/for-developer/architecture.md`、读域测试、fake 驱动的停住 body 场景、真实云盘回归（115）。
