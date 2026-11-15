## Context

见 `proposal.md` 的 Why。与方案直接相关的现状：

- 读层已经在 `readWindowRange` / `readWindowRangeAttempt` 为每次 Range 取数派生总上限，并把驱动返回的 body 交给 `drive.ReadBodyLimited` 做空闲检测。空闲检测只能观察**已经返回的 body**。
- 夸克驱动当前声明 `internal_parallel_read`，`Read` 在 `size >= 4 MiB` 时按 8 MiB 分片、3 worker 并行，并用 `io.ReadAll` 在返回前组装整段。加密挂载的块补齐会把 8 MiB 窗口变成 `8 MiB + 2 KiB`，因此预取窗口几乎总走内部并行路径。
- `file/download` 的 URL 获取用 `singleflight.Group` 合并并发请求，共享请求使用**第一个调用方**的 context。预取窗口在 seek 时被取消，会把正在进行的共享请求一起取消。
- 挂载健康度由 `drive.HealthTracker` 与最近 5 分钟的驱动指标合并而成，`ErrorCategory` 已有 `cancelled` 分类，但健康度没有使用它。
- `2026-11-08-migrate-quark-parallel-read` 曾把夸克迁移到 `parallel_range`（`BulkStreams: 3`）并实测顺序冷读比单流慢 25–30%，随后回退。该结论针对的是“4 MiB 窗口拆成 3 个约 1.3 MiB 子 Range”的形态。

## Goals / Non-Goals

**Goals:**

- 让读层的空闲/总超时对夸克的有界 Range 读真实生效，最坏停顿从 5 分钟降到空闲上限（默认 30 s）。
- 把截断 body 的恢复能力放进共享读层，所有驱动一致。
- 消除“共享 URL 请求被别人的取消带走”导致的前台 EIO。
- 让挂载健康度只表达 provider 状态。

**Non-Goals:**

- 不改变窗口大小、预取深度、读缓存格式或 `read_cache` 配置面。
- 不为夸克新增专用超时/并发配置。
- 不追求夸克 CDN 的并行加速；本变更不把并行小 Range 引入读取热路径。
- 不重新设计 `internal_parallel_read` 能力本身；第三方驱动仍可使用，但需自行满足超时契约。

## Decisions

### 1. 删除夸克内部并行，改用 `parallel_range`

读层无法观测驱动在返回前完成的缓冲，这是 5 分钟停顿的根因。两种修法：给驱动再加一套空闲检测，或让驱动只返回单段 body、由读层统一管理。

选择后者：单一超时实现、复用读预算与取消登记、与直读路径一致。代价是驱动不再自己并行，但既有基准显示夸克 CDN 的并发小 Range 反而更慢，所以这不是性能损失的方向。同时删除内部实现而不是保留“只对 >8 MiB 直读生效”的分支：该分支在加密补齐下会被预取窗口意外触发，正是本次故障路径。

### 2. 截断重放放在共享读层

原实现只在夸克内部对截断分片重试。删除内部并行后，把同样的语义上移到 `readWindowRange`：识别 `io.ErrUnexpectedEOF`（包括“干净结束但长度不足”），在尚未向调用方返回字节时重放一次。直读路径的 `readDirectPart` 做同样校验与重放。这样其他驱动也获得该恢复能力，且不再需要每个驱动自己实现。

### 3. 短读必须由长度校验识别

HTTP 层只在响应带 `Content-Length` 时把短 body 变成 `io.ErrUnexpectedEOF`；fake 驱动、部分 provider 与 200 回退都可能给出干净 EOF。因此在 `readWindowRangeAttempt` 里显式比较读取长度与声明长度（仅在声明的 Range 完整落在已知文件大小内时），避免把短数据写进窗口。

### 4. URL singleflight 只对“别人取消”重试

不把共享请求改成脱离调用方 context：那样 seek/关闭句柄时无人能立即停止 URL 请求，挂载退出也要等请求超时。改为保留现有共享语义，只在“返回 `context.Canceled` 且本调用方 ctx 仍存活”时重新发起一次 `Do`。重试最多一次，避免取消风暴。

### 5. 健康度忽略取消

取消是调用方行为，不代表 provider 状态。`HealthTracker.RecordResult` 与 `HealthStatusFromMetrics` 两处都过滤：前者按 `errors.Is(err, context.Canceled)`，后者按事件自带的 `ErrorCategory`（缺失时回退按消息分类）。取消事件仍保留在读事件与驱动指标里，只是不进入健康度统计。

### 6. 默认并行度回到 1，mount override 保留

真机复测（`probe bench read`，原始驱动并发读，同账号）：16 MiB 样本下 1 路 5.7/13.7 MiB/s，4 路聚合 5.8/15.1 MiB/s，两者都被账号约 15 MiB/s 的总上限压住；64 MiB 的 4 路长读还会因为 per-stream 速率低而触发慢读看门狗。结论：4 路没有更快，反而引入误判，因此驱动默认 `BulkStreams = 1`（与 Drime/Koofr/123 的结论一致），`parallel_range` 能力保留，挂载可以用 `[mounts.read_profile] streams = N` 显式开启。

### 7. 限制单次取数总预算并轮换下载连接

实测中夸克 CDN 会把单次 Range 拖到"每秒几十 KB 但一直有字节"的状态：空闲上限永远不会触发，只能等总上限。把夸克的 `FetchTotalTimeout` 从默认 5 分钟降到 **90 s**（驱动读配置允许覆盖），让读层在还有预算时放弃这次取数。

同时，驱动在 body 读失败（超时取消、连接错误）后标记当前下载连接失效：`client.downloadClient` 变成可原子轮换的一代，旧的只关闭空闲连接，新请求走新连接。轮换在**下一次请求进入时**惰性执行，这样读层紧随其后的重试不会和连接替换竞速。轮换有 5 s 节流，因为 seek 会频繁取消预取，不能每次都丢弃健康连接池。

失败信号的过滤很关键：seek、关闭句柄、释放预取都会通过 context 取消 body 读，如果一律轮换，每次 seek 都要付一次 TLS 握手并可能换到冷边缘，反而拉高 seek 延迟、压低峰值带宽。因此只有"取消时已经跑了 ≥2 s 且速率 <512 KiB/s"或明确的网络错误才判为坏连接；快速取消和正常速度的取消不轮换。

被否决的方案：按吞吐下限（例如 <1 MiB/s 持续 10 s 即中止）主动切换连接。实现前专门做了对照测量，发现慢速时段是 provider 全局的——同一时段所有连接都慢，而快的时候新老连接都能到 5–10 MiB/s；下限看门狗只会把合法的慢速读打断并重试到同样慢的边缘。因此只保留"失败后轮换"这一个安全路径，稳态吞吐仍由 provider 决定。

### 8. 慢读看门狗与加密截断修复

连接轮换只在“失败”时发生，但实测存在“读得极慢却一直有字节”的边缘（512 KiB 窗口 20 s 以上、整段 0.2 MiB/s），永远不会触发失败。为此在驱动的 body 读取上加自适应下限：

- 记录最近 3 分钟内最快的一次完整 body 速率（只有读满声明长度的 body 才计入）；
- 下限 = max(256 KiB/s, 最近最佳速率 / 8)，重新挂载后尚无样本时用 256 KiB/s；
- 超过 5 s 仍低于下限就关闭 body、标记连接、返回 `io.ErrUnexpectedEOF`，由读层幂等重放一次（重放使用已轮换的新连接）；
- 每 60 s 最多主动中止一次：重放仍慢说明是 provider 全局慢，继续中止只会变成连续失败，此时接受慢速流。

同时把下载客户端的响应头等待从 30 s 降到 **10 s**，并在超时后轮换连接：旧版一次 Range 打开可能连续 4 次各等 30 s，真机看到 `driver_open_ms=119506`（约 2 分钟）才拿到首包；现在单次最多等 10 s，重试换新连接/边缘。

这条路径暴露了一个加密层旧缺陷：`DecryptingReader` 把“块内提前结束”的密文直接拿去解密，导致认证失败（不可重试）而不是可重放的截断。现在它接收调用方已知的密文长度（`NewDecryptingReaderAtLimit` / `NewDecryptingReaderLimit`），当实际长度不足时返回 `io.ErrUnexpectedEOF`；长度未知时仍接受 rclone 的短尾块。读层同时把“response body closed”这类被关闭的 body 也视为可重放的截断。

### 9. 边缘惩罚列表

夸克 DNS 会给出多个 CDN IP，质量差异很大（实测同一时段 `124.239.228.x` 单连 6–7.7 MB/s，`119.167.175.27` 只有 60 KB/s）。读超时或慢读看门狗判定某条连接坏掉时，把该远端 IP 记入 5 分钟惩罚表；下载客户端的 `DialContext` 在存在惩罚项时重新解析主机并跳过惩罚 IP，全部被惩罚时回退到正常解析，保证不会完全失去连接。

只在已经出现失败信号时启用候选过滤，不做主动测速：零额外流量、不会因探活噪声误判；代价是第一次遇到坏边缘仍会吃一次超时。切换沿用现有的 `rotateDownload`（旧池只关空闲连接）。

## Risks / Trade-offs

- [直读路径并发] → 已复测：4 路不比 1 路快且触发看门狗，默认降为 1；mount override 保留。
- [CDN 对合法子 Range 返回 200] → 驱动在 strict range 下返回 `ErrUnsupported`，读层按文件回退单流；不会拼接错位数据。
- [截断重放放大请求量] → 每个 Range 最多重放一次，且只在尚未返回字节时；重放仍失败立即整窗失败。
- [URL 重试与并发刷新叠加] → 重试仍走 `singleflight`，同一 fid 的并发刷新会被合并，最多多一次 API 调用。
- [健康度忽略取消可能掩盖真实故障] → 只忽略明确的 `cancelled` 分类；超时、5xx、鉴权、网络错误仍然计入。
- [超时更早暴露 CDN 停滞] → 行为从“卡 5 分钟”变成“30 s 后失败并重放一次”，用户可能更早看到一次 I/O 错误；这是预期行为。

## Migration Plan

- 无配置迁移：现有配置与 `read_profile` 继续有效；夸克挂载从此允许 `streams > 1`。
- 部署后需要重启挂载进程，旧挂载进程仍使用内部并行实现。
- 回滚：恢复 `internal_parallel_read` 并还原 `quark_read.go` 的内部并行实现；读层截断重放与健康度过滤可以独立保留。

## Validation

- 单元测试：夸克大 Range 单流、能力矩阵、URL 共享取消重试；读层分片窗口空闲超时、截断重放与预算耗尽；直读分片截断重放；健康度忽略取消。
- 真实挂载（`quark-encrypt`，只读）：重启后读之前卡住的 11.4 GB 区域，确认读完成、事件出现 `driver_body_ms`（说明 body 由读层观测）、`read_timeouts=0`、`prefetch_timeouts=0`；连续“顺序读 → 远距离 seek”不再出现 EIO；`/v1/mounts/health` 在取消预取后仍为 `ok`。
- 全量本地 CI：`scripts/ci/check-all.sh`。
