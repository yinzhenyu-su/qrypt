# task-api Specification

## Purpose

为移动端、挂载文件系统和内部控制器提供稳定、可恢复、可幂等的任务控制接口，使调用方无需理解任务内部的传输方式和执行器实现。

## Requirements

### Requirement: Task creation exposes stable operation semantics

系统 MUST 让任务创建请求表达稳定的用户操作、输入输出、目标和策略，不要求调用方区分 direct、staging 或其他内部执行 transport。

#### Scenario: Upload strategy is selected internally

- **WHEN** 调用方创建上传任务并选择允许 direct 或要求 staging 的策略
- **THEN** 系统返回同一种稳定的上传操作任务，并由系统根据后端能力选择实际 transport

#### Scenario: Invalid operation input is rejected consistently

- **WHEN** 调用方提交缺少必需输入或与操作类型不匹配的字段
- **THEN** 系统在创建阶段返回结构化错误，不创建可执行的半成品任务

### Requirement: Task creation is idempotent

系统 MUST 支持调用方为一次逻辑操作提供幂等键；同一作用域内重复提交同一幂等键 MUST 返回原任务或明确的参数冲突错误，不得静默创建第二个逻辑任务。

#### Scenario: Client retries after a lost response

- **WHEN** 调用方因超时使用相同幂等键重新创建任务
- **THEN** 系统返回第一次创建的任务标识和当前快照

#### Scenario: Same key is reused with different parameters

- **WHEN** 调用方使用已存在的幂等键提交不同目标或不同输入
- **THEN** 系统拒绝请求并返回可识别的幂等键冲突错误

### Requirement: Task lifecycle has one authoritative state model

系统 MUST 为排队、执行、等待输入输出、重试等待、取消中、终态和删除请求提供明确且互斥的生命周期语义；进度阶段和可用操作不得与任务状态产生互相矛盾的结果。

#### Scenario: Cancellation is observable before cleanup finishes

- **WHEN** 调用方取消一个正在执行的任务
- **THEN** 系统先返回 cancellation requested 或 cancelling 状态，任务完成清理后才进入 canceled 或 failed 终态

#### Scenario: Dismiss does not erase active execution state

- **WHEN** 调用方删除一个仍在执行的任务
- **THEN** 系统不得立即丢弃执行记录，必须在 runner 收尾后再移除任务历史或发出 removed 事件

### Requirement: Task item operations use a common contract

系统 MUST 为支持多文件或流式交互的任务提供一致的任务项查询、取消、输入提交和输出恢复能力；调用方不应根据内部任务实现类型选择不同的 item API。

#### Scenario: Stream item can resume after process recovery

- **WHEN** 进程恢复一个已持久化且拥有部分输入的任务项
- **THEN** 系统返回已有偏移和可用操作，调用方可以继续写入而无需重新上传已确认的数据

#### Scenario: Unsupported item action returns capability error

- **WHEN** 调用方对不支持该操作的任务项发起 commit 或 cancel
- **THEN** 系统返回明确的 capability error，并保持任务项状态不变

### Requirement: Task snapshots have canonical progress and item data

系统 MUST 提供单一权威的任务项快照和聚合进度；创建参数、运行结果和动态扩展字段不得为同一状态维护互相冲突的重复副本。

#### Scenario: Item progress is consistent across query and event

- **WHEN** 调用方通过查询接口和事件接口读取同一任务项
- **THEN** 两者使用相同版本的状态、偏移、错误和可用操作

#### Scenario: Progress fields are meaningful for the operation

- **WHEN** 调用方读取任务进度
- **THEN** 只返回适用于该操作的进度维度，并明确未知、未开始和不适用之间的区别

### Requirement: Task actions are safe under retry and recovery

系统 MUST 防止旧 runner、重复 retry 或恢复 runner 覆盖当前执行结果；任务更新必须属于当前执行代次，且同一任务最多有一个有效执行 runner。

#### Scenario: Immediate retry does not duplicate a runner

- **WHEN** 调用方在 retry wait 状态请求立即重试
- **THEN** 系统唤醒当前 runner 或以受保护的执行代次启动新 runner，不得并发执行同一任务的两个有效 runner

#### Scenario: Old runner finishes after recovery

- **WHEN** 旧进程中的 runner 在恢复 runner 已启动后才提交状态
- **THEN** 系统忽略旧 runner 的过期更新，不覆盖恢复 runner 的状态和结果

### Requirement: Task events can recover from loss

系统 MUST 为任务事件提供单调递增序列和从指定序列继续读取或重新获取快照的机制；事件丢失不得导致调用方永久停留在过期状态。

#### Scenario: Event sequence has a gap

- **WHEN** 客户端发现收到的事件序列不连续或订阅缓冲区已溢出
- **THEN** 系统返回可识别的 gap/snapshot-required 信号，客户端可以重新获取任务快照

#### Scenario: Reconnect resumes task observation

- **WHEN** 客户端携带最后确认的事件序列重新订阅
- **THEN** 系统返回该序列之后的可用事件，或明确告知事件窗口已过期并提供快照兜底

### Requirement: Existing task clients remain migratable

系统 MUST 在迁移期间继续接受现有移动端任务入口和已持久化任务记录，并通过兼容层将旧任务类型、状态和字段映射到新的任务快照。

#### Scenario: Existing JSON wrapper remains usable during migration

- **WHEN** 移动端继续调用现有任务创建、查询、取消和事件 JSON 入口
- **THEN** 系统保持现有请求和主要结果字段兼容，并映射到新的内部任务模型

#### Scenario: Existing journal is recovered after upgrade

- **WHEN** 新版本读取旧版本写入的任务 journal
- **THEN** 系统能够恢复任务的可观察状态，无法恢复的运行上下文以明确的 interrupted/recoverable 状态呈现

### Requirement: Every task type declares its operation identity

系统 MUST 为每一种任务类型确定唯一的操作类别；任何被创建并持久化的任务 MUST 携带该操作类别，使同一作用域内的幂等键在进程重启与任务重放后仍能识别同一逻辑操作。新增任务类型 MUST 在同一处声明其操作类别，不得存在未声明操作类别的类型。

#### Scenario: Idempotency survives a restart for any type

- **WHEN** 一个已持久化任务（任意类型）在进程重启后被重放，调用方以相同幂等键再次提交同一逻辑操作
- **THEN** 系统返回原任务，而不是创建第二个逻辑任务

#### Scenario: A type without a declared operation is rejected by the build-up

- **WHEN** 新增的任务类型没有声明操作类别
- **THEN** 一致性测试失败，且该类型不得被创建路径接受

### Requirement: Batch representation is an explicit family property

多项请求的批量表示 MUST 是该操作族显式声明的属性，而不是各创建路径各自判断的结果。声明了批量类型的操作族（上传、删除、移动）MUST 在多项目请求时产生该族的批量类型，并保持"显式传入的批量类型保持自身身份"的既有语义；未声明批量类型的操作族（复制、下载）MUST 保持其单一类型，这一差异 MUST 能被读者从声明处直接看出。

#### Scenario: Multi-item move produces the batch type

- **WHEN** 调用方创建一个包含多个项目的移动请求
- **THEN** 系统返回移动族的批量类型；显式传入批量类型时不得被降级为单项类型

#### Scenario: Family without a batch type stays single-typed

- **WHEN** 调用方创建一个包含多个项目的复制（或下载）请求
- **THEN** 系统仍返回该族的单一类型，且该行为与其声明一致

#### Scenario: Operation-level creation agrees with type-level creation

- **WHEN** 调用方通过操作类别（而非直接类型）创建多项目请求
- **THEN** 得到的任务类型与直接按类型创建同一请求所得一致

### Requirement: Type policy is explicit for durability, visibility and retry

每种任务类型的持久化、默认 App 可见性与重试策略 MUST 在类型声明处显式确定，不得依赖未书写的默认值。其可观察结果：声明为持久化的类型在进程重启后仍可恢复；同步记账类类型 MUST NOT 出现在 App 的默认任务列表中；重试 MUST NOT 为同一任务启动第二个有效 runner，处于重试等待的类型 MUST 通过唤醒既有 runner 继续，而不是新建执行。

#### Scenario: Durable types survive restart

- **WHEN** 一个声明为持久化的任务在完成前进程重启
- **THEN** 重启后该任务仍可被查询并按其类型声明恢复或重试

#### Scenario: Sync-scope types stay out of the app default list

- **WHEN** App 以默认过滤条件列取任务
- **THEN** 同步记账类任务不在结果中，用户可见类型在其创建时即被标记为可见

#### Scenario: Retry of a waiting type does not start a second runner

- **WHEN** 一个处于重试等待的任务被请求立即重试
- **THEN** 系统按该类型声明的重试策略继续：唤醒既有 runner 或以受保护代次重启，且任一时刻只有一个有效 runner

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
