## Why

夸克驱动的 `Read` 在内部把大 Range 拆成 8 MiB 分片，并用 `io.ReadAll` 在返回前把整段读完。读层只能看到驱动返回之后的 body，因此 `fetch_idle_timeout=30s` 对这类读完全失效：CDN 响应停住时，前台读必须等 `fetch_total_timeout=5min` 才失败，播放器上表现为连续 5 分钟 0 网速，随后才重试。同一次挂载里还观察到两个放大问题：后台预取被 seek 取消时，会把共享的 `file/download` singleflight 请求一起取消，前台读可能继承 `context canceled` 并返回 EIO；这些取消还被计入挂载健康度，把正常 seek 显示成 provider 故障。

## What Changes

- 夸克驱动用 `CapabilityParallelRange` 替换 `CapabilityInternalParallelRead`，删除内部的 8 MiB 分片组装；一条有界 Range 请求只读取自己那一段，body 直接流式返回给读层。
- 读层把“body 在声明范围之前干净结束”识别为截断，按幂等 Range 重放一次；禁止用部分数据组装窗口。直读路径（`drive.ReadParallel`）同样校验长度并重放。
- 夸克 `file/download` 的共享 singleflight 请求被其他调用方的 context 取消时，仍存活的调用方重新取一次 URL，不再继承别人的取消。
- 挂载健康度只统计 provider 失败：调用方取消（`context.Canceled`、`error_category=cancelled`）不再计入错误或降低健康等级。
- 行为变化：夸克挂载的 `bulk` 读现在由读层按 `read_profile.streams`（默认 4）决定并发；前台 1 块窗口与预取窗口在当前实现下仍保持单流。

## Capabilities

### New Capabilities

无。

### Modified Capabilities

- `read-pipeline`: 超时上限必须覆盖驱动内部的读缓冲；截断 body 的重放语义从“仅超时重试”扩展为“截断也重放，且不得返回部分窗口”。
- `observability`: 挂载操作健康度只反映 provider 失败，调用方与生命周期取消不计入。

## Impact

- `pkg/drivers/quark/quark_read.go`：删除内部并行读；`downloadURL` 增加共享取消后的重试。
- `pkg/drivers/quark/observability.go`：能力对调。
- `pkg/vfs/read/reader.go`：短读检测与截断重放。
- `pkg/drive/parallel_read.go`：直读分片的短读检测与重放。
- `pkg/drive/health_tracker.go`：取消不计入健康度。
- 测试与生成文档：quark 能力矩阵、读层截断/超时用例、健康度用例；`docs/for-user/support-drivers.md` 重新生成。
- 兼容性：不新增用户配置；已有 `[mounts.read_profile]` 配置继续生效，夸克从此允许 `streams > 1`。
- 性能风险：`2026-11-08-migrate-quark-parallel-read` 的基准显示夸克 CDN 的并发小 Range 比单流慢 25–30%，因此本变更保留默认单流的读取热路径，并对直读路径的默认并发做后续复测。
