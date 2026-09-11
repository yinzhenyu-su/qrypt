## ADDED Requirements

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
