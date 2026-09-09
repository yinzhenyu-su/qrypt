## Context

当前任务控制面由 `pkg/task` 的通用 Request/Task/Manager、Core 的任务分发与 stream batch、移动端 JSON wrapper 共同组成。任务状态既用于执行生命周期，也用于移动端输入输出握手；任务快照还混合了聚合进度、任务项结果、动态 Detail 和操作能力。持久化只保存 Task，不保存运行函数和事件游标，因此恢复依赖 Core 按任务类型重建执行器。

## Goals / Non-Goals

**Goals:**

- 让公共任务 API 描述用户操作和稳定的任务生命周期，不暴露 direct/staging 等内部 transport。
- 让任务、任务项、可用操作和进度各自有单一权威来源。
- 让创建、取消、重试、删除和事件订阅具有可判定、可恢复的语义。
- 在移动端兼容期内支持旧 JSON API，并允许按版本逐步移除旧 wrapper。

**Non-Goals:**

- 不重写上传或下载的数据面，不改变 direct、staging、VFS 和 driver capability 的行为。
- 不在本变更中引入远程任务服务或跨设备任务同步。
- 不把所有已有任务一次性改成完全不同的持久化格式；先提供版本化迁移边界。

## Decisions

### 1. 用稳定操作类型替代 transport-specific task type

公共创建请求只表达操作类别、输入来源、目标和策略。上传策略包含 `prefer_direct`、`staging_only` 等值，实际 transport 由 Core 选择并保存在内部执行元数据中。任务对外只暴露稳定的 operation kind 和当前可观察阶段。

备选方案是继续公开 `upload_stream_direct` 和 `upload_stream_batch`，但这会把内部 fallback 和恢复实现持续泄漏给调用方，因此不采用。

### 2. 将任务快照拆成生命周期、进度、任务项和动作集合

任务快照保留聚合字段；任务项快照保存单文件状态、错误、偏移和 item actions；阶段使用受限枚举而不是任意字符串；actions 由当前状态和任务能力派生，不作为可随意修改的持久字段。创建参数和运行结果不再重复保存为两套 item 列表。

备选方案是继续扩展 `Detail map[string]any`，但动态字段无法被编译器、schema 和迁移工具约束，拒绝采用。

### 3. 将任务项操作路由到统一 controller

任务管理器负责任务级查询和生命周期；支持输入/输出流、item cancel 或 commit 的任务实现统一的 item controller。Core 不再通过 `getUploadStream`、`getDownloadStream` 等具体 batch map 在公共入口中分支判断。流句柄仍然是短生命周期、不可持久化的 session 对象。

### 4. 创建使用幂等键，执行更新使用 generation

创建请求允许调用方提供 client operation key；同一作用域内重复请求返回原任务或明确冲突。每次恢复或重试生成新的 execution generation，任务更新必须匹配当前 generation，避免旧 runner 覆盖新 runner 的状态。

### 5. 取消、完成和删除分离

取消首先记录 cancellation requested 并触发 runner context；只有 runner 收尾后才进入 canceled 或 failed。删除只移除已完成任务的历史可见性；对运行中任务只设置隐藏/删除请求，不能立即从 store 删除执行记录。

### 6. 事件采用序列恢复加快照兜底

事件保留单调递增序列；订阅可从指定序列继续读取。检测到缓冲区丢失或序列不连续时，客户端必须重新获取任务快照。事件不是唯一数据源，快照查询是最终一致性兜底。

### 7. 采用版本化兼容迁移

旧任务 journal 继续可读，恢复层把旧 task type、旧 phase 和旧 Detail 映射为新快照。移动端旧入口先成为兼容 wrapper，返回结果保持现有字段；新入口和新快照稳定后再删除旧 wrapper。

## Risks / Trade-offs

- [迁移期间会同时存在旧、新两套模型] -> 先建立单向 adapter，禁止新代码直接写旧动态字段，并为 journal/API 版本增加测试。
- [事件 replay 增加存储和内存成本] -> 只保留有限事件窗口，窗口外统一返回 snapshot-required 信号。
- [generation 校验可能丢弃过期 runner 的最后进度] -> 终态更新优先由当前 generation 提交，旧 runner 只允许记录诊断日志。
- [删除语义改变可能让历史任务短暂可见] -> 增加明确的 deleting/hidden 标志，并在 runner 收尾后发出 removed 事件。

## Migration Plan

1. 为新任务快照、操作类型、阶段和 actions 建立内部结构与适配器，先覆盖现有 upload/download 流程。
2. 为 Manager 增加 generation、幂等键和取消收尾状态，补充持久化 journal 迁移测试。
3. 将 Core 的任务项入口切换到 controller，并保留现有移动端 handle API。
4. 增加带序列恢复的事件订阅，移动端在兼容期同时支持旧轮询和新事件恢复。
5. 将旧 JSON wrapper 标记为兼容入口，确认调用方迁移后再删除；删除前保留完整回滚路径。

## Defaults for Follow-up Work

- 幂等键作用域覆盖同一持久化 state 目录中的任务；无持久化 store 时覆盖当前 Core session。
- 事件 replay 先使用有界内存窗口，不持久化完整事件流；序列超出窗口时统一要求客户端重新获取快照。
