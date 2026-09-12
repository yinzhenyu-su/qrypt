## MODIFIED Requirements

### Requirement: The logger is installed through a single path

日志器的安装 SHALL 只通过一个入口完成。安装时全局日志器 MUST 保持同一对象，使任何已取得该引用的调用点在安装之后继续写入新配置的输出目标。

安装 MUST 只释放**日志器自己打开的**输出 sink（其从路径创建的日志与错误文件），MUST NOT 关闭任何被交进来的 writer。进程级共享的 writer（标准错误、标准输出）不属于日志器所有，关闭它会使进程此后无法再向该目标输出，因此无论它是否实现关闭接口都 MUST 保持打开。`Close` MUST 遵循同一所有权规则。

#### Scenario: Replacing closes the previous output targets

- **WHEN** 一个已经打开自身文件 sink 的日志器被新的日志器替换
- **THEN** 旧日志器自己打开的文件 sink 被关闭（不泄漏文件句柄），且新的输出目标开始接收后续日志行

#### Scenario: A borrowed writer survives replacement

- **WHEN** 全局日志器当前是被交进来的 writer（例如标准错误）且被文件日志器替换
- **THEN** 该 writer 保持打开，替换之后进程仍能向它写出内容

#### Scenario: CLI output reaches the terminal after installing the logger

- **WHEN** 命令在安装文件日志器之后向标准错误写下输出（例如挂载 banner 或 cobra 打印的错误信息）
- **THEN** 该输出出现在终端，而不是因为标准错误已被关闭而静默丢弃

#### Scenario: A captured reference follows the replacement

- **WHEN** 调用方在替换发生前取得了全局日志器的引用，替换后才写入日志
- **THEN** 该引用写入的是替换之后的输出目标与级别配置
