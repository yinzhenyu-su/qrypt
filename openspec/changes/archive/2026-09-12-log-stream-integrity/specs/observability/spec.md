## ADDED Requirements

### Requirement: The durable log contains every level

落盘的主日志 SHALL 包含所有通过级别过滤的日志行，使单独读取主日志即可获得按时间顺序的完整记录。错误日志 SHALL 是主日志中 warn 及以上行的子集，而不是这些行唯一的去处。当主日志与错误日志指向同一输出目标时，每行 MUST 只写入一次。

#### Scenario: Errors are readable in the main log

- **WHEN** 一条 error 级日志被写入
- **THEN** 主日志包含该行，且其时间戳与错误日志中同一行一致

#### Scenario: The error log stays a filtered view

- **WHEN** 一条 info 级日志被写入，随后一条 warn 级日志被写入
- **THEN** 错误日志不包含该 info 行，且包含该 warn 行；主日志同时包含两者

#### Scenario: A single output target is written once

- **WHEN** 主日志与错误日志指向同一个输出目标（例如都回退到标准错误）
- **THEN** 每一条日志行在该输出中只出现一次，不因级别产生重复

### Requirement: The logger is installed through a single path

日志器的安装 SHALL 只通过一个入口完成。安装 MUST 释放被替换的日志器的输出目标，且全局日志器在安装前后 MUST 保持同一对象，使任何已取得该引用的调用点在安装之后继续写入新配置的输出目标。

#### Scenario: Replacing closes the previous output targets

- **WHEN** 一个已经打开文件输出的日志器被新的日志器替换
- **THEN** 旧日志器的文件输出被关闭，且新的输出目标开始接收后续日志行

#### Scenario: A captured reference follows the replacement

- **WHEN** 调用方在替换发生前取得了全局日志器的引用，替换后才写入日志
- **THEN** 该引用写入的是替换之后的输出目标与级别配置

### Requirement: Known credential parameters are masked before output

写入任何输出目标之前，已知令牌参数名的值 SHALL 被掩码。掩码 MUST 在级别过滤之后、写入之前对每一条日志行执行，因此主日志与错误日志同等受保护。已知令牌参数名 MUST 至少覆盖：断点续传的会话标识、访问令牌与刷新令牌、通用令牌参数、授权头，以及带签名的对象存储访问凭据参数。

#### Scenario: An upload resume credential is masked at info level

- **WHEN** 一条 info 级日志包含 multipart 断点续传的会话标识参数
- **THEN** 主日志与错误日志中出现的是掩码值而不是该标识

#### Scenario: Token parameters and authorization headers are masked

- **WHEN** 一条日志行包含访问令牌、刷新令牌、通用令牌参数或授权头
- **THEN** 这些值在写入时被掩码，且掩码不改变行内其余字段

#### Scenario: Masking applies to both sinks

- **WHEN** 一条包含上述参数的 warn 及以上日志被写入
- **THEN** 主日志与错误日志中的该行都已完成掩码，不存在只有一侧受保护的情况
