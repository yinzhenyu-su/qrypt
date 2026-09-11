## Context

动机见 `proposal.md`。影响设计的现状：

- 四张互不引用的表：类型常量（`pkg/task/task.go:14-26`，11 个）、type→operation（`pkg/task/operation.go:84-98`）、创建分派（`pkg/core/task.go:236-259`）、operation→type（`pkg/core/task.go:279-311`，含 2 处内联提升）。
- 提升谓词 5 处手写：`core/upload_task.go:36-38`、`core/delete_task.go:39-41`、`core/move.go:52-54`，以及操作路径上的 `core/task.go:298-300`、`core/task.go:304-306`。
- 策略字面量：`Persistent`/`Dismissible` 在 7 个文件里各写一遍（upload/stream/direct/download_stream 恒 true；delete/move 按批量类型；copy/download 按 spec 计算），且 `Persistent` 还会被运行期覆盖（跨挂载 move `core/move.go:106-110`）。
- 恢复注册硬编码 2 处（`core/task.go:33-35`），各自还有自己的类型过滤器与类型判定；`RetryTask` 有 2 个按类型名的分支（`core/task.go:178-198`、`203-208`）后才落到通用路径。
- 派生字段 `Task.Operation` 由 `manager.SubmitIdempotent` 填写（`pkg/task/manager.go:110-112`），并参与持久化幂等键（`pkg/task/persistent_store.go:96-111`）——它是承重的，但当前无人保证"每种类型都能派生"。
- 消费者侧几乎无需改动：`pkg/control` 与 `internal/cli` 不 switch 类型；`pkg/mobile` 只有一处类型硬编码（`mobile/upload.go:48-51`），App 默认列表按 `Scope` 过滤（`mobile/task.go:172-181`）。
- 架构约束：`scripts/check-arch.sh` 规定 `pkg/core → pkg/vfs → pkg/drive` 方向，`pkg/task` 是叶子（`pkg/core` 依赖它，反之不可）。

## Goals / Non-Goals

**Goals:**

- 让"一个任务类型的全部策略"在一处声明，并从该声明派生出 operation、批量归属、持久化默认、App 可见性、可恢复性与重试策略。
- 新增类型时，漏配策略从"静默降级"变为"一致性测试失败"；新增类型所需改动从 5 处（批量型）/12-13 处（可恢复族）降到 1 处声明 + 该族自己的 runner 代码。
- 行为零变化：类型集合、wire 字段、JSON 形状、移动端契约与持久化键格式都不变。

**Non-Goals:**

- 不新增任务类型，不给 copy/download 增加批量类型（现状差异保留，只把它从"偶然"变成"声明"）。
- 不把 runner 函数体、item 能力计算、状态机归一化搬进表：这些是代码与状态，不是类型策略。
- 不重构 `Task.Detail` 的键集合（每族 detail 结构是独立议题，本次不引入 `DetailKeys` 声明）。
- 不改 `pkg/control` 的 HTTP 形状或 `pkg/mobile` 的桥接口。

## Decisions

### D1: 策略表放在 `pkg/task`，只承载数据；行为仍在 `pkg/core`

`pkg/task` 新增描述符与注册表（每类型一行：`Type`、`Creation`、`Operation`、`BatchOf`、`Persistent`、`Dismissible`、`UserVisible`、`Recoverable`、`Retry`），并提供查询：`Describe(type)`、`Descriptors()`、`Promote(type, count)`、`RecoverableTypes()`、`ScopeForType(type)`、`OperationForType(type)`。

- 备选：把创建/恢复函数指针也放进描述符。否决：`pkg/task` 是叶子，不能引用 `pkg/core`；要支持就得加注册回调接口，其机制（初始化顺序、跨包注册、测试隔离）比它消除的重复更重。行为留在 `pkg/core` 的分派处。
- 备选：把表放在 `pkg/core`。否决：`Task.Operation` 的派生发生在 `pkg/task`（`manager.go:110-112`），表必须能被它读到。
- 备选：保持 switch，只加一致性测试。否决：测试能发现漏配，但不能消除 5 处谓词与 7 处字面量的重复，新增类型仍需多处编辑。

### D2: 创建分派按声明的创建路径查表

描述符用 `Creation`（一个 `CreationPath` 枚举）声明该类型由哪条创建路径构建；`pkg/core` 持有 `CreationPath → 实现` 的包级表，`CreateTask` 先查描述符再查该表。这样"声明了类型但没有创建路径"和"实现了创建路径但没有类型使用它"两个方向都可被测试断言，新增类型只需一行声明 + 复用既有路径。

- 备选：在 `pkg/core` 写一个按类型名 switch 的分派。否决：switch 的类型集合不可被测试枚举，反向（多实现了路径）无法断言，且新增类型要同时改 switch 与表。
- 备选：把创建函数指针放进描述符。否决见 D1（跨包引用）。
- 说明：`pkg/task` 的 `Manager.SubmitIdempotent` 保持类型无关（它的测试使用合成类型），因此不在那里拒绝未声明的类型；拒绝发生在真正的创建边界 `Core.CreateTask`，与规格中"该类型不得被创建路径接受"一致。

### D2b: `Operation` 由表派生，且是"每类型必有"

`operationKindForType` 改为查表；描述符里 `Operation` 为空即视为声明缺失，由一致性测试拒绝。派生点仍在 `manager.SubmitIdempotent`（不改派生位置与调用顺序），因此持久化幂等键的字节格式不变。

备选：在创建函数里显式设置 `Task.Operation`。否决：那会新增 7 处写入点，正是本次要消除的模式；而且绕过 `SubmitIdempotent` 的创建路径（`pkg/vfs` 的两个 sync 生产者）会漏写。

### D3: 批量提升用单一 `Promote` 助手，`BatchOf` 为空表示"不提升"

`task.Promote(typ, count)` 读 `BatchOf`：`count > 1` 且声明了 `BatchOf` 时返回该批量类型，否则原样返回。创建函数与 `taskRequestForOperation` 都改为调用它，删除 5 处谓词。copy/download 显式声明 `BatchOf: ""`。

- 备选：给 copy 增加 `copy_batch` 以"统一"行为。否决：那是破坏性 wire 变化（任务类型列表是移动端契约，`docs/for-developer/mobile-interface.md` 有类型表），属于独立变更；本次只让差异显式。
- 注意：`core/move.go:56-58` 的"空类型回落到 `move_remote`"是对畸形请求的兜底，不属于类型策略，保留在创建处。

### D4: 持久化/可见性默认值来自表，运行期覆盖留在创建函数

描述符给出 `Persistent`/`Dismissible`/`UserVisible` 的创建默认值；`pkg/core` 仍在解析出请求后覆盖它需要覆盖的（跨挂载 move 升级为持久化、按 spec 计算的 copy/download），因为该判断依赖解析后的 items/recursive/挂载关系。`Persistent` 的最终权威仍在任务本身（journaling 与 replay 归一化读的是任务能力位，不是表）。

`UserVisible` 不改变 wire：`Scope` 仍写在任务上，App 默认过滤仍按 `Scope`（`mobile/task.go:172-181`）；描述符的字段用于在创建处要求"用户可见类型必须带 `ScopeUser`"，并给一致性测试一个可断言的声明。

### D5: 恢复与重试按声明分派

`newTaskManager` 的两次硬编码调用改为遍历表中声明可恢复的类型；`RetryTask` 先按 `Describe(typ).Retry` 分派三种策略（通用 manager retry / 恢复重建 / 唤醒既有 runner），再进入既有实现体。

- 保留：唤醒分支的"仅当批次仍在内存中"前置条件与非阻塞发送（`core/task.go:201-208`）属于运行期状态，继续写在实现里。
- 备选：把恢复函数搬进描述符。否决同 D1。

### D6: 一致性由测试强制，覆盖"表 ↔ 代码"两侧

新增测试：每个 `task.Type` 常量有且仅有一行描述符；每行 `Operation` 非空且与 `pkg/task` 的派生一致；`Promote` 对每个声明了 `BatchOf` 的类型返回的是已知类型；创建路径产生的类型都在表内（黄金列表断言：`CreateTask` 支持的类型集合 == `Descriptors()` 的类型集合）；`pkg/core` 的恢复注册集合 == 表中 `Recoverable` 集合。

## Risks / Trade-offs

- [描述符与创建函数实际写入的策略漂移] → 策略改为从表读取而不是重复书写；黄金表断言把"表与创建分派不一致"变成测试失败。
- [`Task.Operation` 参与持久化幂等键] → 不改派生位置与顺序；测试覆盖"重启后同幂等键返回同一任务"（`pkg/core` 现有幂等/持久化测试）。
- [`Promote` 被误用到 copy/download 上会改变任务类型] → copy/download 显式声明 `BatchOf: ""`，并用类型断言测试钉住现状。
- [恢复改为遍历后顺序/条件变化] → 保持每个族的类型过滤器与判定函数不变，只把"调用哪两个函数"改为遍历；恢复相关测试（含 streaming 两族）必须不改动即通过。
- [表被当作"权威持久化来源"] → 文档写明：`Persistent` 只是创建默认值，任务上的能力位与 replay 归一化仍是权威。
- [跨包注册的诱惑] → 明令本次不引入函数指针注册（见 D1 备选），避免引入初始化顺序与测试隔离问题。

## Migration Plan

一个提交即可（策略表与派生同时落地），但按 `tasks.md` 分四段推进以便逐段验证：表与查询 → 创建路径派生 → 恢复/重试分派 → 文档与一致性测试。无 wire 变化、无持久化格式变化、无配置变化；回滚即恢复直接开关，持久化任务记录仍可被读取（`Task.Operation` 语义未变）。

## Open Questions

无。
