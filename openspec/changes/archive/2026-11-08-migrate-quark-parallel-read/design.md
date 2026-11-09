## Context

Quark 的 `Read(offset, size)` 已支持任意 Range：`downloadRange` 发 `Range` 头，正常返回 206；越界请求 CDN 会返回 200 全量（已在 `Read` 入口按 `entry.Size` 截断并回答 EOF）。驱动内部在 `size >= 4 MiB` 时按 8 MiB 分片、3 worker 并行。

读层现在的并行契约：

- `CapabilityParallelRange`：驱动保证合法子 Range 返回 206。
- `drive.WithStrictRange`：子 Range 请求带严格标记；驱动无法保证 206 时必须返回 `ErrUnsupported`。
- `ReadProfileOf`：`InternalParallelRead` 会把 `BulkStreams` 强制为 1。
- 直读路径 `ReadParallel` 先用 1 字节严格 Range 探测，`ErrUnsupported` 时回退单流。

基准（`benchmark-baseline.md`，`quark-encrypt` / `Linus.mp4`，198 MB）：

- 冷读中位 18.96 MB/s，热读 202.5 MB/s，冷 seek 中位加载 1.50 s。
- 事件显示 `intent=bulk, streams=1`，即当前 VFS 单流。

## Decisions

### 1. capability 对调

去掉 `CapabilityInternalParallelRead`，加 `CapabilityParallelRange`。不同时保留两者：`ReadProfileOf` 看到 `InternalParallelRead` 就会把 `BulkStreams` 压回 1，迁移不会生效。

### 2. 严格 Range 语义

在 `downloadRange` 的响应处理里：

- `drive.StrictRangeFromContext(ctx)` 为真时，只接受 206。
- 收到 200 时关闭 body，返回 `fmt.Errorf("quark: read: %w", drive.ErrUnsupported)`。
- 非严格模式保留现有 200 回退，整文件读和旧调用方不受影响。

这样即使 CDN 对某个子 Range 退化为全量，读层也会把整个文件标记为非并行并回退单流，不会拼接错位。

### 3. BulkStreams = 3

`ReadProfile()` 返回 `BulkStreams: 3`，与现有内部并行的 worker 数一致。默认值 4 没有在 Quark CDN 上验证过，先从已验证的并发度开始；后续可调。

### 4. 保留大 Range 的内部并行

驱动内部的 8 MiB / 3 worker 并行只在单次 `Read` 的 `size > 8 MiB` 时触发。迁移后：

- VFS 子 Range 约 1 MiB，低于内部并行阈值，不会叠加。
- 直读路径的 8 MiB 分片 `partCount == 1`，也不会触发。
- 直接向驱动请求超过 8 MiB 单段读的调用方仍走内部并行。

因此不需要删掉内部并行实现，两条路径在现有窗口/分片尺寸下不会相乘。

### 5. 不新增用户配置

沿用现有设计：并行度由驱动读配置决定，不新增 `read_cache.parallel_streams`。

## Risks

- **Quark 限流**：3 路是现有实现验证过的并发；若迁移后出现 429/403 增多，回退到 1 路或降低 `BulkStreams`。
- **严格模式误判**：CDN 对合法子 Range 返回 200 会导致整文件回退单流，性能退化但数据正确；用真实基准观察回退次数。
- **URL 刷新风暴**：3 个子 Range 共享缓存下载 URL，403 时各自刷新一次；URL 缓存有互斥，需要在真实云盘确认。
- **整文件读双重实现**：保留内部并行意味着两套路径；只在 `size <= 0` 时使用内部并行，边界清晰，后续可以再统一。

## Validation

- 单测：严格模式 200 → `ErrUnsupported`；非严格模式 200 仍可用；整文件读仍调用内部并行。
- 读层：子 Range 返回 `ErrUnsupported` 后文件转单流，后续窗口不再并行。
- 能力矩阵：Quark 期望 `parallel_range`，不再有 `internal_parallel_read`。
- 真实基准：同一命令、同一文件复测，记录冷读中位、冷 seek、事件 `read_streams`、回退次数，与 `benchmark-baseline.md` 对比。

## Outcome

迁移已实现并按上述方式验证，然后回退。

控制实验（同一文件、同一时段，3 个采样，`cold` + `sequential`）：

| 配置 | 冷读中位 |
| --- | --- |
| 单流对照 | 11.30 / 12.13 MB/s |
| 2 流 | 9.19 MB/s |
| 3 流 | 8.30 / 10.58 MB/s（另有完整 both/seek 轮 8.74） |

3 流比单流低约 25–30%；冷 seek 差异在 provider 波动范围内。事件确认迁移后确实使用了 `streams=3`，且没有 `ErrUnsupported` 回退，所以不是严格 Range 误判导致的退化。

回退内容：Quark 恢复 `CapabilityInternalParallelRead`，删除 `ReadProfile()`。

保留内容：

- 严格 Range：`checkQuarkReadStatus` 在严格模式遇到 200 返回 `ErrUnsupported`。Quark 当前不声明 `parallel_range`，该路径不会被 VFS 触发，但契约实现是正确且可测试的。
- 加密包装层转发 `parallel_range` / `internal_parallel_read` 与 `ReadProfile`：修复了加密挂载看不到底层读能力的问题，p123、drime 等驱动位于加密挂载后时可以正常并行了。
