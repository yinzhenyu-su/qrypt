## ADDED Requirements

### Requirement: Terminal task items are not rewritten by remote observations

任务项进入终态（succeeded / failed / canceled）之后，系统 MUST NOT 因远端状态观察而改写它的状态、错误或可用操作。远端观察 MUST 只更新用于诊断的观测字段（远端任务标识与状态、云端字节数、云端阶段），不得使已取消的项重新变为运行中，也不得使已取消的项因远端成功而变为成功。

#### Scenario: A canceled item stays canceled

- **WHEN** 调用方取消一个任务项，随后一次远端状态观察取回该上传仍在排队或运行中
- **THEN** 该任务项保持 canceled，其错误与可用操作不变

#### Scenario: A canceled item is not resurrected into success

- **WHEN** 调用方取消一个任务项，而对应的远端上传随后成功
- **THEN** 该任务项仍为 canceled，不因远端成功而变为 succeeded

#### Scenario: Remote observations still reach diagnostics for terminal items

- **WHEN** 一个已处于终态的任务项其远端状态发生变化
- **THEN** 观测字段（远端任务标识、远端状态、云端字节数与阶段）照常更新，快照仍可读到最新的远端信息

### Requirement: A terminal task snapshot contains only terminal items

任务进入终态时，同一份任务快照内的所有任务项 MUST 处于终态；系统 MUST NOT 发布"任务已终态而某项仍在运行"的组合。若 runner 退出时仍有任务项处于非终态，系统 MUST 把这些项收敛为 failed 并记录可读原因，再据此判定任务的终态。

#### Scenario: Runner exit with in-flight items

- **WHEN** 流式任务的 runner 退出而某个任务项尚未进入终态
- **THEN** 该任务项被标记为 failed 并带有说明原因的错误，任务终态与该结果一致，且不存在非终态的项

#### Scenario: Progress counters agree with the terminal state

- **WHEN** 任务处于终态
- **THEN** 同一快照的聚合计数（已完成项数与失败项数）与项状态一致，不得出现终态却全部计数为零的组合

#### Scenario: Canceled items are honored when the task finalizes

- **WHEN** 一个任务项已被取消，任务随后收尾
- **THEN** 任务按"存在被取消项"收尾，该项在快照中仍为 canceled，不会被当成仍在运行
