## ADDED Requirements

### Requirement: Log lines can be attributed to a mount

带挂载作用域的调用点产生的日志行 SHALL 携带该挂载的标识，该标识 MUST 在写入任何输出目标之前成为行的一部分，且 MUST NOT 依赖消息文本中的路径反推。挂载标识与行的其余部分 MUST 互不干扰：既有消息内容、级别与采样语义保持不变。

#### Scenario: Attributed lines carry their mount

- **WHEN** 一个带挂载作用域的调用点写入一条日志行
- **THEN** 该行携带对应挂载的标识，能在日志事件查询中作为字段读出

#### Scenario: Attribution survives logger replacement

- **WHEN** 日志器被替换为新的输出目标或级别配置，随后一个带作用域的调用点写入日志
- **THEN** 该行写入替换之后的输出目标，并仍携带该挂载标识

#### Scenario: Unattributed lines are unchanged

- **WHEN** 一个尚未接入作用域的调用点写入日志
- **THEN** 该行照常写入且不带挂载标识，不因缺少作用域而丢行或改变格式

### Requirement: Sampling state is isolated per mount

采样 MUST 按挂载隔离：两个挂载的相同调用路径使用同一采样键时，各自的采样状态 MUST 独立计算，一个挂载的抑制 MUST NOT 吞掉另一个挂载的行或把它的行计入 `suppressed`。

#### Scenario: One mount's suppression does not swallow another's line

- **WHEN** 两个挂载的同一采样键在采样间隔内各产生一条日志
- **THEN** 两个挂载各自输出自己的第一条，互不抑制

### Requirement: The log query surface filters on fields, not message text

日志事件查询 SHALL 支持按挂载标识与组件标识做服务端过滤，且这两个过滤 MUST 基于事件的字段而不是对消息文本的匹配。过滤结果 MUST 与该事件被写入时的字段值一致。

#### Scenario: Filtering by mount returns only that mount's lines

- **WHEN** 消费方按挂载标识查询日志事件
- **THEN** 结果只包含该挂载的行，其他挂载的行不出现

#### Scenario: A message that merely mentions another mount is not returned

- **WHEN** 一条由挂载 A 产生、消息文本恰好包含挂载 B 名称的日志行被写入
- **THEN** 按挂载 B 过滤时该行不出现

#### Scenario: Component filtering keeps its meaning

- **WHEN** 消费方按组件标识查询日志事件
- **THEN** 返回的是消息前缀组件等于该标识的行，与按消息文本解析前缀的结果一致，且没有前缀的行不被匹配
