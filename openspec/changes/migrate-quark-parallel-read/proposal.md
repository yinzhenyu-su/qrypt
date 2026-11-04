## Why

Quark 目前声明 `internal_parallel_read`，由驱动自己在 `Read` 内部拆 3 路并行。这带来两个问题：

- 并发绕过了 VFS 的读预算：全局读槽位、单文件并发上限、优先级和预取许可都管不到驱动内部的 worker。
- Quark 的 VFS 窗口是 4 MiB，低于内部并行的 8 MiB 分片阈值，所以实际大部分窗口仍是单 HTTP Range；内部并行主要只对整文件读生效。

读层已经具备统一的并行 Range 能力和调度预算（`CapabilityParallelRange`、`ReadProfile`、`readbudget`）。把 Quark 迁移过去，可以让它的并发纳入同一套预算和观测，并与直读路径行为一致。

## What Changes

- Quark 用 `CapabilityParallelRange` 替换 `CapabilityInternalParallelRead`。
- Quark 实现严格 Range：`drive.StrictRangeFromContext` 为真且服务端没有返回 206 时，返回 `drive.ErrUnsupported`，由读层回退单流；不再把 200 全量 body 当子 Range 交出。
- `ReadProfile()` 设置 `BulkStreams = 3`，与现有内部并行 worker 数对齐；其余窗口/阈值沿用默认。
- 保留整文件读（`size <= 0`）的内部并行，避免迁移后整文件下载退化为单流。
- 读取性能基准已记录：`benchmark-baseline.md`；迁移后用同一命令、同一文件复测对比。

## Capabilities

### New Capabilities

无。

### Modified Capabilities

无（实现层迁移，不改变读管线或驱动契约的外部语义）。

## Impact

- `pkg/drivers/quark/observability.go`：capability 对调。
- `pkg/drivers/quark/quark_read.go`：严格 Range 检查、整文件内部并行保留、`ReadProfile()`。
- `pkg/drivers/capability_test.go`：Quark 能力矩阵期望。
- `pkg/drivers/quark/driver_test.go`：200 严格模式返回 `ErrUnsupported`、整文件仍走内部并行。
- 真实云盘：`quark-encrypt` 只读基准复测，确认 `streams=3`、无回退、冷读吞吐不低于基线。
