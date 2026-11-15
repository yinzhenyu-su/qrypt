## ADDED Requirements

### Requirement: 缓存准入由读意图决定

持久读缓存的准入 SHALL 由读意图决定：显式 `bulk` 与 `probe` 读 MUST NOT 写入持久缓存；`interactive` 读 MUST 按现有策略写入。本能力只约束已准入内容的保留与淘汰；未准入内容 MUST NOT 参与容量、配额或晋升统计。缓存容量为零时仍按既有要求全部短路。

本能力中的“缓存”“查询”与“命中”均指持久读缓存；内存热块缓存由 `read-pipeline` 管理，不属于本能力。缓存容量为零时，持久缓存 MUST NOT 写入任何块、MUST NOT 持久化索引，持久缓存查询 MUST 报告未命中；热块缓存仍可由 `read-pipeline` 用于当前读取，且不改变本能力的禁用语义。

#### Scenario: bulk 不产生缓存内容

- **WHEN** 一次显式 `bulk` 读完成
- **THEN** 持久缓存中没有该次读取引入的块或索引条目，容量与配额统计不变

#### Scenario: probe 不产生缓存内容

- **WHEN** 一次 `probe` 读完成
- **THEN** 持久缓存中没有该次读取引入的块

#### Scenario: interactive 仍按现有淘汰策略

- **WHEN** 一次 `interactive` 读引入内容并使缓存超出容量上限
- **THEN** 既有的顺序配额、类别隔离与淘汰要求继续生效

#### Scenario: 未准入内容不计入配额

- **WHEN** 缓存同时经历 `bulk` 与 `interactive` 读取
- **THEN** 顺序配额只统计实际准入的内容，`bulk` 读取不改变配额占用

#### Scenario: 热块缓存不属于本能力

- **WHEN** 持久缓存容量为零，但读路径仍保留当前读取所需的内存热块
- **THEN** 持久缓存不写入任何块或索引，持久缓存查询报告未命中；热块命中不改变本能力的禁用语义
