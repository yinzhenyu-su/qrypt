## Why

一种任务类型的"策略"今天分散在四个互不相干的表和多处字面量里：`pkg/task/task.go:14-26` 的常量、`pkg/task/operation.go:84-98` 的 type→operation 映射、`pkg/core/task.go:236-259` 的创建分派、以及 `pkg/core/task.go:279-311` 的 operation→type 映射；再加上 5 处手写的批量提升谓词（`core/upload_task.go:36-38`、`core/delete_task.go:39-41`、`core/move.go:52-54`、`core/task.go:298-300,304-306`）、7 个文件各写一遍的 `Persistent`/`Dismissible` 字面量、2 处硬编码的恢复注册（`core/task.go:33-35`）与 2 个按类型分派的 retry 分支（`core/task.go:178-208`）。

后果不是"多写几行"，而是**失败是静默的**：漏掉 operation 映射时类型仍能编译运行，但 `Task.Operation` 为空（`manager.go:110-112` 派生）——它会进入持久化幂等键（`persistent_store.go:96-111`），于是重启后的幂等语义悄悄降级；漏掉提升谓词时该批量类型永远不产生；漏掉 `Persistent` 时任务变成仅内存、且不可 dismiss。最近一次加 `move_batch` 就实际改动了 5 处代码 + 1 处文档，而这已经是最便宜的一种类型（只有提升差异，没有新 runner、没有恢复、没有 retry 策略）；加一个可恢复的流式族需要 12-13 处。

现在做的理由：任务类型数量与恢复路径仍在增长（streaming/direct 两族刚落地），而消费者侧其实已经很干净——`pkg/control` 与 `internal/cli` 对类型零改动，`pkg/mobile` 只有一处硬编码——把策略收敛到一张表的成本被限制在 `pkg/task` + `pkg/core`，是当前性价比最高的一步。

## What Changes

- 在 `pkg/task` 引入**类型描述符单表**：每个类型一行，声明它的创建路径（`Creation`）、操作类别、批量归属（`BatchOf`）、持久化/可 dismiss 默认值、App 可见性、可恢复性与重试策略。type→operation 与各处的提升谓词改为查表与单一 `Promote` 助手；创建分派改为按声明的创建路径查表，使"声明了类型但没有创建路径"与"实现了路径但没有类型声明"都成为测试失败。
- 创建路径从表中读取策略：`core/upload_task.go`、`core/delete_task.go`、`core/move.go`、`core/copy.go`、`core/download_task.go`、两个 streaming/direct 家族不再各自写 `Persistent`/`Dismissible` 字面量与提升判断；**运行期才知道的策略覆盖保留在创建函数内**（跨挂载 move 的持久化升级 `core/move.go:106-110`、按 spec 计算的 copy/download 持久化 `core/copy.go:51`、`core/download_task.go:71-73`），因为它们取决于解析后的请求，而非类型。
- 启动恢复改为遍历表中声明可恢复的类型；`RetryTask` 按表中声明的重试策略分派，而不是按类型名写分支。
- **不改行为**：不新增任务类型、不改任何 wire 字段与 JSON 形状、不改移动端契约；`copy`/`download` 保持"无批量类型"的既有语义，但这两种差异从"偶然缺失"变为**显式声明**（并在 `docs/for-developer/mobile-interface.md` 的类型表中写清）。
- 增加可执行的一致性约束：每个类型常量必须有且仅有一行描述符；描述符与 `pkg/task` 的 operation 映射、`pkg/core` 的创建/恢复路径必须一致（新增测试 + 黄金表断言），使"新增类型漏配策略"从静默降级变成测试失败。

## Capabilities

### New Capabilities

无。

### Modified Capabilities

- `task-api`: 新增三类可验证的行为契约——（1）每种任务类型都有确定的操作类别，创建并持久化的任务必须携带它，使幂等键在重启后仍识别同一逻辑操作；（2）批量表示是该操作族的显式声明属性，已声明批量类型的族（upload/delete/move）多项请求必须产生批量类型，未声明者（copy/download）必须保持单一类型；（3）每种类型的持久化、默认 App 可见性与重试策略必须显式确定，不得依赖"忘了写"的默认。

## Impact

- `pkg/task`：新增描述符与查询（`Describe`/`Descriptors`/`Promote`/可恢复类型集合），`operation.go` 的 type→operation switch 变成查表。
- `pkg/core`：`task.go`（创建分派、恢复注册、`RetryTask`、`taskRequestForOperation`）、`upload_task.go`、`upload_stream_task.go`、`upload_stream_direct_task.go`、`download_task.go`、`download_stream_task.go`、`delete_task.go`、`copy.go`、`move.go`。
- `pkg/vfs`：两处 sync 作用域生产者（`task.go`、`delete_task.go`）硬编码 type+operation 且不过 `CreateTask`，需要与描述符保持一致（不引入新的消费者接口）。
- 文档：`docs/for-developer/mobile-interface.md` 的任务类型表补上"批量提升/App 可见性"两列。
- 测试：`pkg/task`（operation/类型集合）、`pkg/core`（task operation、persistence、move/delete/upload 的批量与类型断言）、新增描述符完整性测试。
- 风险与代价：描述符可能与创建函数实际写入的策略漂移（缓解：策略由表派生而非重复书写，加黄金表断言）；`Task.Operation` 参与持久化幂等键，其派生位置变更必须保持字节级一致（`persistent_store.go:96-111`）。
