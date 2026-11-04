## ADDED Requirements

### Requirement: 读超时可观测

读事件与调试快照 SHALL 记录取数超时：取消原因 `timeout`、等待耗时、取数耗时，以及超时计数。预取超时与前台读超时 MUST 可区分。

#### Scenario: 前台读超时记录事件

- **WHEN** 前台窗口取数超时
- **THEN** 读事件包含 `cancel_reason=timeout`、等待耗时和取数耗时

#### Scenario: 预取超时记录为取消

- **WHEN** 后台预取超时
- **THEN** 诊断记录取消原因 `timeout`，且不产生前台读失败事件

#### Scenario: 调试快照包含超时计数

- **WHEN** 读取调试快照
- **THEN** runtime 中包含超时次数与最近一次超时的窗口信息
