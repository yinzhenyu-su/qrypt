## Why

读路径的调试事件环形缓冲只有 **2** 个槽位（`pkg/vfs/read/state.go:39` 的 `HistoryLimit = 2`），而一次读取产生的事件远不止 2 条：1 条汇总事件（`pkg/vfs/read/reader.go:182`，在读取结束时最后写入）+ 每 chunk 1 条明细（`pkg/vfs/read/helpers.go:147`，`ChunkSize = 512 KiB`）+ 每窗口 1 条 `fetch_window`（`pkg/vfs/read/reader.go:558`）+ 预取窗口事件（`reader.go:803,823`）。一次 1 MiB 的读取就会写入 4 条以上事件。

后果是 `/v1/reads`（`pkg/control/server_handlers_read.go`）和调试快照的 `MountSnapshot.Events.Reads`（`pkg/vfs/diagnostics/dto.go:86-89`）实际只能装下"最近一次读取的汇总 + 1 条明细"，更早的读取全部不可见，明细事件几乎必然被下一条挤掉。而明细正是把读延迟拆到 `wait_window` / `cache_miss_load` / `fetch_window` 各阶段的唯一依据——也就是说，读慢时唯一能解释原因的信号在读路径上留不住。`qrypt debug watch/collect/bundle` 拿到的是同一份历史，因此同样受限。

保留量与同级域严重不成比例：driver HTTP 事件 500（`pkg/drivers/internal/driverutil/trace.go:20-25`）、上传历史 100（`pkg/vfs/upload/service.go:19`）、health 事件 200（`pkg/drive/health_tracker.go:9-10`）、日志事件 500（`pkg/logging/log.go:130`）——被挂载盘上最高频、用户体感最强的读路径反而是 2。

而这个缺陷在代码里已经留下了痕迹：`pkg/vfs/read_test.go:225` 直接以 `if vfsread.HistoryLimit < 4 { return }` 跳过断言，注释写着"a small history ring is valid; it may retain too few events"。测试承认历史小到断言无意义，但没有把它当成缺陷处理。

现在做的理由：`/v1/reads` 与快照是 `debug collect/watch/bundle` 三个诊断入口的共同数据源，读路径不可观测会让这三个入口在"盘变慢"这个最常见场景下同时失效；先把数据留下，后续的聚合与告警才有输入。

## What Changes

- 把读事件历史拆成**两份有界保留**：汇总事件（每次读取 1 条，保留 128 条）与阶段明细事件（保留 512 条），读取时按写入顺序合并成一份时间序列表。单环形缓冲无论放大到多少都不能满足要求——`fs.Read(ctx, path, 0, 0)` 会按 512 KiB 逐 chunk 迭代整个文件（`pkg/vfs/read/reader.go:190-225`），一次大文件读取产生的明细事件数没有上界，足以冲掉任何单 ring，包括该次读取自己的汇总事件；拆开保留后"明细挤掉汇总"在结构上不可能发生。
- 内存上界明确且惰性：事件结构 504 B（实测 `unsafe.Sizeof(drive.MetricEvent{})`），两份环形数组合计约 323 KiB/挂载，且沿用既有的惰性增长（从 64 起倍增，只在未达上限时复制），未发生读取的挂载不预付这份内存。
- 把"保留量必须装下多次完整读取"变成可执行约束：新增测试断言在连续多次读取（含每 chunk 明细）之后，这些读取的汇总事件仍全部可见；并让 `pkg/vfs/read_test.go:225` 那条此前被跳过的断言真正生效。
- 不改任何 wire 形状与消费者契约：`/v1/reads`、`MountSnapshot.Events.Reads`、以及 `pkg/vfs/debug_all.go` 里从读域常量派生保留量的关系都保持不变，只是内容从"最近一条"变为"最近一段"；`pkg/contracttest` 中依据保留量判断指标窗口是否被截断的逻辑改为以汇总 ring 为准。

## Capabilities

### New Capabilities

- `observability`: 运行期观测面必须留下的信号与其有界性——本 change 只规定读事件历史这一条：读事件历史的保留量必须足以同时容纳多次完整读取的汇总与阶段明细，并且保留量有明确上界。

### Modified Capabilities

无。

## Impact

- `pkg/vfs/read/state.go`：`HistoryLimit` 取值与其文档注释（内存上界、增长方式）。
- `pkg/vfs/read_test.go`：移除 `< 4` 的提前 return，改为断言汇总事件在多次读取后仍可见。
- 新增保留量测试：`pkg/vfs/read/`（环形缓冲层面）与 `pkg/vfs/`（快照/`/v1/reads` 层面各一处），覆盖"明细不挤掉汇总"。
- 内存影响：每个真正发生过读取的挂载约 +252 KiB 上界（外加被保留明细事件的 `Extra` map），空闲挂载不变。
- 无 API、配置、wire 字段改动；`pkg/control`、`internal/cli/debug` 零改动。
