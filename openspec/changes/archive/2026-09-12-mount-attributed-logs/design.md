## Context

见 proposal.md - Why（含量化数据）。设计相关的现状约束：

- 全局 `logging.L` 是 `*Logger`，`ReplaceDefault` 原地改写其字段（change `log-stream-integrity` 已统一安装路径），因此持有该指针的 scope 在替换后仍指向新配置。
- 采样状态 `samples map[string]sampleState` 以调用点传入的字符串键为索引（`log.go` 的 `logfEvery`/`logEveryFunc`），当前没有挂载维度——两个挂载跑同一代码路径会共享采样状态并互相抑制。
- `Event` 目前有 `ID/Time/Level/Message/Suppressed`，没有组件字段；组件信息只存在于消息文本的 `[TAG]` 前缀里，查询时由 `eventComponent`（`pkg/control/server_handlers_health.go:378-386`）用字符串定位抠出。
- `newStores`（`pkg/vfs/stores.go:27`）是 `readcache.Store` 与 `upload.PendingStore` 的唯一构造点，`VFS.New` 在其中手上有 `opts.Name`。
- 驱动层（`pkg/drivers/*`，约 70 处调用点、其中 60+ 处无 `path=`）拿不到挂载名：驱动是通用组件，只接收配置。全量归因需要先设计"挂载名如何进入驱动"，属于后续 change。

## Goals / Non-Goals

**Goals:**

- 让带作用域的调用点写出的行可在查询面按挂载字段过滤，且过滤基于字段而非消息文本。
- 组件过滤从"查询时解析消息"变为"写入时派生字段"，语义不变。
- 机制本身（`Scope`、替换安全、采样隔离）在本 change 内被测试钉住，后续迁移只需机械替换调用点。
- 用最小注入点覆盖最不可归因的一批行（两个 store 的 28 处、其中 25 处无 `path=`）。

**Non-Goals:**

- `pkg/vfs` 其余 64 处与 `pkg/drivers/*` 约 70 处的迁移（后者依赖驱动层挂载名注入的独立设计）。
- `*Ctx` 变体与 `op_id` 关联（等 op 上下文在更多路径可用后再做）。
- 行格式改造（时间戳/级别前缀不变）、JSON 日志、`path` 文本过滤的替换。

## Decisions

### 1. `Component` 在写入时派生，`Mount` 由作用域提供

`Component` 可以零调用点改动地得到：写入前解析消息的 `[TAG]` 前缀并存入字段。这样 `component=` 过滤变成字段比较，且**不改变任何行的内容**。

`Mount` 无法派生（`pkg/vfs` 内部路径是挂载相对的，见 why），必须由调用点提供，因此需要 `Scope`。两者放同一个 change 是因为它们共同构成"查询面按字段过滤"这一能力：一个字段零成本、一个字段需要机制。

### 2. `Scope` 持有全局 logger 指针，不做字段拷贝

拷贝字段会让 scope 在 `ReplaceDefault` 之后继续写旧 writer（change `log-stream-integrity` 刚修过的同类问题）。持指针依赖"原地改写"契约，并用测试钉住"替换后 scope 写入新 writer 与新级别"。

### 3. 采样键按挂载隔离

当前键为裸字符串；两个挂载共享同一键时，一个挂载的抑制会把另一个挂载的行计入 `suppressed` 并吞掉。改为 `mount + "\x00" + key`（未接入作用域的调用点键不变）。这会让已接入的两个 store 在不同挂载上各自采样——是修正，不是回归；影响面仅限本 change 接入的调用点，并在文档中写明。

### 4. 首期落地点选两个 store，而不是 VFS 主干

`readcache.Store` 与 `upload.PendingStore` 共 28 处调用点、25 处无 `path=`，是"既无路径也无挂载"的最不可归因一类（缓存写队列满、索引保存失败、pending journal 压缩失败等），且共享唯一构造点 `newStores`——注入改动只有一个函数签名。

`pkg/vfs` 主干（staging/upload/mutation 等 27 处）只有 2 处无 `path=`，性价比低于驱动层，故与驱动层一起留到后续 change。

### 5. `/v1/events` 的 `mount=`/`component=` 过滤在服务端完成

与既有 `path`/`level`/`limit` 一致，在 handler 内对已取回的事件列表过滤，不改变事件环形缓冲或快照装配。多个 `mount` 值按"任一匹配"处理，与 `mounts` 查询参数在别处的语义一致。

## Risks / Trade-offs

- **[归因覆盖不完整，`/v1/events?mount=` 只能过滤已接入的 28 处]** → 这是有意的首期切分；文档写明覆盖范围，避免读者以为"没返回"等于"没有该挂载的日志"。剩余缺口与量化数据记在 proposal。
- **[采样键隔离会改变已接入调用点的输出节奏]** → 修正而非回归（此前跨挂载互相抑制）；仅影响两个 store 的采样行，用测试与文档固定语义。
- **[`Component` 派生依赖 `[TAG]` 前缀这一既有约定]** → 约定今天已被 `eventComponent` 依赖，本 change 只是把它从查询时移到写入时；无前缀的行组件为空且不被 `component=` 匹配，与今天一致。
- **[`Scope` 与全局 logger 的指针关系是隐式契约]** → 由"替换后写入新 writer"的测试钉住。
