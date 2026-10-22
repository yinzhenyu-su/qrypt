# Spec Delta

## MODIFIED Requirements

### Requirement: Task item operations use a common contract

系统 MUST 为多文件或流式任务提供一致的任务项查询、取消、输入提交和恢复能力，调用方不应按内部 transport 选择不同入口。上传项恢复偏移 MUST 区分活跃会话可继续写入的位置与重开后可靠恢复的位置：`resume_offset` 在活跃会话可表示已接受输入，`durable_offset` MUST 只表示已持久确认的连续内容；没有活跃写入时两者相等。普通写入成功不等同于检查点确认；完整提交前的检查点 MUST 不隐式提交输入。

#### Scenario: Stream item can resume after process recovery
- **WHEN** 进程恢复一个已持久化且拥有部分输入检查点的任务项
- **THEN** 系统返回可靠恢复偏移和可用操作，调用方无需重传已持久确认的数据；检查点之外的尾部不被虚报为可靠输入，`resume_offset` 等于 `durable_offset`

#### Scenario: Unsupported item action returns capability error
- **WHEN** 调用方对不支持该操作的任务项发起 commit 或 cancel
- **THEN** 系统返回明确的 capability error，并保持任务项状态不变

#### Scenario: Accepted progress exceeds the durable checkpoint
- **WHEN** 任务当前已接受的输入多于最近持久检查点所确认的输入
- **THEN** 活跃会话的 `resume_offset` 可以反映已接受字节，但 `durable_offset` 不得超过检查点；重启后 `resume_offset` 降为可靠内容位置

#### Scenario: Input is checkpointed without being committed
- **WHEN** 流式输入建立检查点后尚未执行完整提交
- **THEN** 任务仍允许按其生命周期继续输入，不因检查点存在而显示输入已提交或开始上传未完整内容

### Requirement: Task snapshots have canonical progress and item data

系统 MUST 提供单一权威的任务项快照和聚合进度，同一快照的状态、恢复偏移、持久偏移与可用动作 MUST 一致。上传内容事实 MUST 从已提交的内容/操作状态对账，通用 Task 生命周期仍由任务模块维护。已接受、已持久确认和已远端发布的进度 MUST 不混淆；事件或展示更新不得成为恢复字节的唯一依据。

#### Scenario: Item progress is consistent across query and event
- **WHEN** 调用方通过查询接口和事件接口读取同一版本的任务项
- **THEN** 两者提供一致的状态、偏移、持久偏移、错误和可用操作

#### Scenario: Progress fields are meaningful for the operation
- **WHEN** 调用方读取任务进度
- **THEN** 只返回适用的进度维度，明确未知、未开始与不适用，不把已写入本地字节当作已持久或已上传字节

#### Scenario: A live item exposes both offsets
- **WHEN** 活跃输入项已接受尚未检查点的字节
- **THEN** 快照同时给出可继续写入的 `resume_offset` 与不高于检查点的 `durable_offset`，二者不互相冒充

#### Scenario: Task presentation lags a durable content commit
- **WHEN** 内容检查点已可靠提交，但进程在任务展示快照更新前退出
- **THEN** 恢复可从内容事实重建正确偏移和动作，不要求依靠进度事件再次证明已持久内容

#### Scenario: Terminal and caller-paused items observe remote changes
- **WHEN** 远端对账取得新结果，而任务项已终态或因调用方失败而等待输入
- **THEN** 系统更新允许的诊断观测，不绕过终态与调用方暂停规则改写状态、错误或可用动作

## ADDED Requirements

### Requirement: Reconciliation and missing-source conditions use additive encoding

对账和缺源 MUST 通过追加式 display phase、稳定错误码和既有 action/state 表达，不得复用 `retrying` 或 `uploading` 表示“结果待对账”，也不得让旧客户端把未知 phase 当成成功或可安全盲重试。现有任务状态、终态和调用方暂停语义保持权威。

#### Scenario: Remote outcome is uncertain
- **WHEN** 替换请求超时后可能已生效
- **THEN** 任务项显示可识别的对账 phase 与稳定错误码，不宣称远端已完成，也不提供会盲目重复破坏性操作的 action

#### Scenario: Direct source must be supplied again
- **WHEN** 未完成直传没有可复用候选且源无法重新打开
- **THEN** 任务项显示缺源 phase/错误和有效恢复 action，不伪造本地暂存

#### Scenario: An older client reads a new phase
- **WHEN** 旧客户端收到未知的对账/缺源 phase
- **THEN** 其依据现有 state、错误码和 action 仍能区分失败、等待和不可盲目重试，不会把任务误判为成功

