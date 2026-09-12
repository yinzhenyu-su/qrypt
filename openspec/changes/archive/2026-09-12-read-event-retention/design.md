## Context

见 proposal.md - Why。设计相关的现状约束：

- 读事件历史是**单**环形缓冲：`pkg/vfs/read/state.go` 的 `historyState`（`events []drive.MetricEvent` + `pos`/`count`），容量常量 `HistoryLimit`。`Append` 已是 O(1)（写槽位，仅在未达上限时倍增复制），所以问题不在写入成本，而在容量取值。
- 两类事件共用一个 ring，但它们的**产生速率量级不同**：汇总事件每次 `Reader.Read` 恰好 1 条（`pkg/vfs/read/reader.go:182`），明细事件随 chunk 与窗口线性增长（`pkg/vfs/read/helpers.go:147` 及其 18 个调用点）。`readRange` 对 `size == 0` 会迭代到文件末尾（`reader.go:190-225`），因此**单次读取的明细数量没有上界**。
- 消费者面对的是同一个 `[]drive.MetricEvent`：`/v1/reads`（`pkg/control/server_handlers_read.go`）、快照 `MountSnapshot.Events.Reads`（`pkg/vfs/diagnostics/dto.go:86-89`）、以及建立在其上的 `debug collect/watch/bundle`。事件顺序（时间序）对这些消费者有意义。
- 明细事件与汇总事件的可区分字段是 `MetricEvent.ParentOpID`（`pkg/vfs/diagnostics/read.go:50-57` 设置），但该字段对汇总事件为空并非该类型的语义保证。

## Goals / Non-Goals

**Goals:**

- 使"最近 N 次读取的汇总事件必然可见"成为**结构性保证**，与单次读取产生多少明细无关。
- 明细事件按最近优先保留，覆盖"刚才那次慢读"的阶段拆解需求。
- 保持 wire 形状与现有消费者零改动：仍然是一份按时间序排列的 `[]drive.MetricEvent`。
- 保留量有界、有文档、可被测试断言。

**Non-Goals:**

- 不引入持久化、时间序列或跨进程的历史（进程退出即丢失，与现状一致）。
- 不改明细事件的**产生**策略（仍然每 chunk 记录，不做采样或开关）；本 change 只改保留。
- 不调整 driver（500）、upload（100）、health（200）的保留量。
- 不引入 `HistoryLimit` 的配置项——它是编译期常量，与同级域一致。

## Decisions

### 1. 拆成两份保留，而不是放大单 ring

`historyState` 持有两个独立的环形缓冲：`summaries`（容量 `SummaryHistoryLimit = 128`）与 `details`（容量 `DetailHistoryLimit = 512`）。`Snapshot()` 把两者按写入序号合并成一份时间序列表。

理由：单 ring 下"明细挤掉汇总"是必然事件而非概率事件——一次 `Read(0,0)` 大文件读取即可产生上千条明细，任何固定容量都会被冲掉。拆开后该风险消失，且两份容量各自对应一个明确的诊断意图（近期读取的概览 / 最近一次慢读的细粒度拆解）。

考虑过的替代方案：

- **放大单 ring 到 512 或 1024**：不满足规格中"任意一次读取的明细不得挤掉更早读取的汇总"；只是把必然事件的触发门槛往后推。否决。
- **明细改为按需开启（debug 开关）**：需要明细的场景恰恰是"这次读变慢了"，用户必须复现才能拿到证据；而且明细当前是无条件产生（`reader.go` 多处直接调用），改成开关属于改变既有行为。否决。
- **限制单次读取产生的明细条数**：会丢掉最需要证据的那类读取（大文件顺序读/整文件读）。否决。

### 2. 用写入序号合并，保持单一时间序列表

两个 ring 各存事件，`historyState` 递增一个 `appendSeq` 并写入每个事件所在位置；`Snapshot()` 按序号归并（同一序号不可能来自两个 ring，因为序号在写入时分配）。

理由：消费者只认一份按时间排列的列表；用序号归并比按 `At` 时间戳排序更严格——同一毫秒内的多条事件不会因时间戳相等而乱序（时间戳在 `util.Now()` 下可能重复）。

### 3. 路由由调用点显式决定，而不是从字段推断

`diagnostics.ReadRuntime` 拆出 `AppendDetailEvent`；`RecordRead` 走 `AppendEvent`，`RecordReadDetail` 走 `AppendDetailEvent`。

理由：`ParentOpID` 今天恰好能区分两者，但它是"事件挂在哪次操作下"的语义，不是"这条事件是明细"的类型标记；未来任何一条带 parent 的汇总事件都会被静默丢进明细 ring。让调用点声明意图，读代码的人和编译器都能看出来。

### 4. 容量取值

汇总 128、明细 512。汇总按"近期读取概览"取值：128 次读取足以覆盖一次 `debug watch` 采样间隔内的活动。明细 512 覆盖典型读取（1 MiB 读取约 3-5 条明细）的数百次，或整文件读取时最后约 256 MiB 的 chunk 证据。上界：`(128 + 512) × 504 B ≈ 323 KiB/挂载`（不含 `Extra` map），惰性增长。

## Risks / Trade-offs

- **[整文件读取会冲掉明细 ring，只留下最后一段 chunk 证据]** → 这是有意取舍：明细按最近优先；要保留更早的 chunk 证据不属于本 change 的目标，且汇总事件保证可见。在 `SummaryHistoryLimit`/`DetailHistoryLimit` 的文档注释里写明这一语义。
- **[内存从 1 KiB 涨到约 323 KiB/挂载]** → 上界固定、按需增长；用测试断言容量上界，并在注释中记录实测的单事件大小与来源。
- **[此前被跳过断言（`pkg/vfs/read_test.go:225` 的 `HistoryLimit < 4` 提前 return）在保留量提高后开始真正执行，可能暴露既有缺陷]** → 该断言检查 `cache_miss_load` / `fetch_window` 事件的 `Extra` 字段与 `prefetch_chunks` 字段的移除。若失败，按真实缺陷处理（修字段或改断言并说明），不得重新加回跳过逻辑。
- **[既有测试直接断言历史长度等于 `HistoryLimit`（`pkg/vfs/debug_test.go:100-105`、`pkg/vfs/debug_read_runtime_test.go:21-58`）]** → 这些测试改为针对拆分后的两个容量分别断言，并新增"明细不挤掉汇总"的回归用例。
