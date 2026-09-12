## Why

当前每个挂载的观测数据只有**原始事件列表**：读事件在 `pkg/vfs/read` 的环形缓冲里（`SummaryHistoryLimit`/`DetailHistoryLimit`），driver HTTP 事件在 `pkg/drivers/internal/driverutil` 的 500 条缓冲里，上传在 `pkg/vfs/upload` 的 100 条历史里，健康度只是 `pkg/drive/health_tracker.go` 对最近 200 条事件的窗口计数。**没有任何累计聚合**：没有操作数、没有字节吞吐、没有错误率、没有延迟分位数。`Throughput` 是逐事件算完塞进事件里的字段（`pkg/vfs/diagnostics/read.go:41-43`），P95 只在基准工具里按需现算（`pkg/contracttest/driver_benchmark_summary.go`），运行期不保留。

后果是三类问题无法回答：

1. **"现在有多快"**——必须先把事件列表拉出来再自己聚合，而列表是有界的：事件一被挤出，那段时间的吞吐就永久丢失。读事件历史即使按 change A 提高保留量，也只是"最近 128 次读取"，不是累计值。
2. **"是否在恶化"**——没有累计量就没有可比较的基线；窗口计数（`health_tracker.go` 的 5 分钟 / 200 事件）只能说明"此刻"，且如 change A 的分析，窗口计数会随吞吐自我截断。
3. **"慢在长尾还是普遍慢"**——没有延迟分布，只有被保留的那几条事件的 `DurationMS`。一次典型的"挂载盘卡"，用户想知道的是 p95/p99，而不是最近三次读取各自的耗时。

健康判定的阈值问题（`healthLevel` 用绝对错误数 0/1-4/5+ 划档）之所以难修，正是因为缺少累计基数：`5 次错误 / 100 次操作` 与 `5 次错误 / 100000 次操作` 现在无法区分。先有累计计数与延迟分布，阈值才有意义。

现在做的理由：这是"从事件到指标"缺失的那一层。它独立于日志工作、改动集中在读路径与快照装配，且是后续任何健康模型重构（把绝对阈值换成速率/分位）的前置条件。

## What Changes

- **新增无锁累计计数器类型**（`pkg/drive`）：操作数、错误数、字节数、总耗时，外加**固定桶延迟直方图**（约 13 个边界，从 1ms 到 5s + 溢出桶）。记录路径只做若干次原子加，不取锁、不分配、不存储单次事件；存储占用与操作次数无关（定长）。
- **读路径接入**：`Reader.Read` 完成时记录一次（操作数、字节数、耗时、错误）；经由新的可选 `CounterRecorder` 接口注入，与既有 `HealthRecorder`/`ReadObserver` 同样的"可选 sink、不拓宽 Host 表面"模式（`pkg/vfs/read/host.go:32-56`）。
- **暴露**：每个挂载的快照新增 `counters` 段（累计值 + 直方图 + 由直方图派生的均值与分位数），随 `/v1/state` 一起返回；`qrypt debug collect/watch/bundle` 因为读 `/v1/state` 而自动获得，CLI 零改动。
- **明确的语义边界**：计数器是**累计**的、不从窗口重置，因此能与事件历史共存而不冲突；快照同时给出累计值与保留窗口，读者可自行区分"总量"与"最近一段"。
- **不做**：上传与 driver HTTP 的计数器、健康阈值重构、任何持久化或时间序列。

## Capabilities

### New Capabilities

无。

### Modified Capabilities

- `observability`: 新增两条要求——每个挂载必须提供与操作次数无关的有界存储的累计操作/错误/字节计数；必须提供有界桶的延迟分布并据此可得高分位数。既有要求（读事件历史保留、持久日志流）不变。

## Impact

- `pkg/drive`：新增计数器类型与快照 DTO（含分位派生）。
- `pkg/vfs/read/host.go`：新增可选 `CounterRecorder` 接口与 no-op 实现。
- `pkg/vfs/read/reader.go`：`Reader.Read` 的完成路径记录一次；`ReaderDeps` 增加该 sink。
- `pkg/vfs/vfs.go`、`pkg/vfs/read_host.go`：每个挂载持有计数器并作为 recorder 注入。
- `pkg/vfs/diagnostics/dto.go`、`snapshot.go`：`MountSnapshotRuntime` 增加 counters 段与装配。
- 测试：计数器单元测试（桶边界、分位、并发记录）、读路径接入测试、快照暴露测试。
- 内存：每挂载定长（约 20 个 `uint64`），与读事件历史相比可忽略。
- 无配置项；快照为**追加字段**，`DebugSnapshotSchemaVersion` 不变（形状向后兼容）。
