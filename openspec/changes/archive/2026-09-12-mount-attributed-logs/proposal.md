## Why

日志行缺少可过滤的**挂载字段**，而查询面只能靠消息文本猜：`/v1/events` 的 `component` 过滤是从消息里抠 `[...]` 前缀（`pkg/control/server_handlers_health.go:378-386`），`path` 过滤是 `strings.Contains`。同时 `logging.Event` 没有挂载维度，所以多挂载进程的单一日志流无法按盘分离。

先量化了缺口（非测试代码里 `logging.L.*` 共 **195** 处）：

| 范围 | 调用点数 | 说明 |
|---|---|---|
| 全部 | 195 | `path=` 存在的 65 处（33%），无 `path=` 的 130 处（66%） |
| `pkg/vfs` 及其子包 | 92 | 挂载名在构造函数里可得 |
| `pkg/drivers/*` | 约 70 | **挂载名不在驱动手上**，需要另一条注入路径 |
| 无 `path=` 最多的文件 | quark_upload 36、readcache/store 12、upload/store 10、mount/adapter 8 |

这份数据推翻了"注入 scope 就能全覆盖"的假设：drivers 的 60+ 处根本拿不到挂载名（驱动是通用组件，只拿到配置，不拿到挂载名），全量归因的改动面比原来估计的更大，且跨越驱动层。因此本 change 只做**查询面 + 机制 + 最划算的一处落地**，把驱动层与其余调用点的迁移留作后续（见 proposal 的非目标与 `mount-attributed-logs` 的原始设计）。

选 `readcache.Store` 与 `upload.PendingStore` 作为落地点的理由：两者共 28 处调用点、其中 25 处完全没有 `path=`（`[CACHE] compact pending journal failed`、`[CACHE] async put chunk failed` 这类行今天既无路径也无挂载，是最不可归因的一类），且两者由**同一个构造函数** `newStores`（`pkg/vfs/stores.go:27`）创建，注入点只有一个。

## What Changes

- **日志事件带结构化字段**：`logging.Event` 增加 `Mount`；`Component` 在写入时从既有的 `[TAG]` 前缀派生（零调用点改动）。
- **查询面按字段过滤**：`/v1/events` 新增 `mount=` 参数，`component=` 改为按字段比较（语义与今天一致，但不再对消息文本做字符串手术）。
- **引入挂载作用域日志器**：`pkg/logging` 新增 `Scope`（`Logger.WithMount`），持有全局 logger 指针而非拷贝字段，因此 `ReplaceDefault` 之后仍写入新配置；采样键按挂载隔离，避免两个挂载的同一采样键互相抑制。
- **落地两个 store**：`newStores` 把挂载名传给 `readcache.NewStore` 与 `newUploadStore`，两处共 28 个调用点从 `logging.L.*` 改为 scope 调用。
- **不做**（后续独立 change）：`pkg/vfs` 其余 64 处与 `pkg/drivers/*` 约 70 处的迁移；驱动层需要的"驱动如何知道挂载名"注入路径；`*Ctx` 变体与 `op_id` 关联；`component` 派生之外的日志格式改动。

## Capabilities

### New Capabilities

无。

### Modified Capabilities

- `observability`: 新增两条要求——日志行可由带挂载作用域的调用点打上挂载标识并在查询面按字段过滤；日志事件的前缀组件必须作为字段可查而不是在查询时解析消息文本。既有的读事件保留、持久日志流、累计计数要求不变。

## Impact

- `pkg/logging`：`Event.Mount`/`Event.Component`、`Scope`、mount-aware 的内部写入路径与采样键隔离。
- `pkg/control`：`/v1/events` 的 `mount=` 过滤与 `component=` 字段化。
- `pkg/vfs/stores.go`：`newStores` 增加挂载名参数。
- `pkg/vfs/readcache`（16 处调用点 + `Store` 字段）、`pkg/vfs/upload`（`PendingStore` 12 处 + 字段）。
- 测试：`pkg/logging`（字段派生、scope 前缀、替换后仍生效、采样隔离）、`pkg/control`（按字段过滤、文本不误伤）、两个 store 的既有用例。
- 剩余缺口（已量化，另开 change）：`pkg/vfs` 64 处、`pkg/drivers/*` 约 70 处；其中驱动层需要先解决"驱动如何知道挂载名"。
