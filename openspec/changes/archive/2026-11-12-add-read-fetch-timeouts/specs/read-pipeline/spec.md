## ADDED Requirements

### Requirement: 窗口取数必须有超时上限

系统 SHALL 为每次前台窗口取数和后台预取设置超时：连续无新字节达到空闲上限，或单次取数达到总上限，都必须中止 body 读取并释放资源。超时 MUST NOT 依赖调用方取消；即使 FUSE 传入的 context 没有 deadline，取数也必须自行结束。

#### Scenario: 停住的 body 在空闲超时后中止

- **WHEN** 驱动返回的 body 在空闲上限内没有产生新字节
- **THEN** 该窗口取数被中止，等待者收到超时错误，读槽位与预取许可被释放

#### Scenario: 慢速但持续前进的 body 不被误杀

- **WHEN** body 持续产生新字节，但整体速率低于平均吞吐预期
- **THEN** 只要每次空闲间隔低于上限，取数继续，不因总耗时触发空闲超时

#### Scenario: 持续滴流受总上限约束

- **WHEN** body 持续产生少量字节但永不完成
- **THEN** 单次取数在总上限到达时失败，不无限占用槽位

#### Scenario: 预取超时静默取消

- **WHEN** 后台预取的窗口取数超时
- **THEN** 预取以取消原因 `timeout` 结束，调用方读不受影响，诊断记录该原因

### Requirement: 超时错误传播给所有等待者

同一个窗口的加载失败或超时 MUST 传播给所有等待该窗口的读；等待者 MUST NOT 重新发起加载或继续无限等待。前台读超时 MUST 归类为 `drive.ErrorCategoryTimeout`，并由 FUSE 映射为明确的 I/O 错误。

#### Scenario: 加载者超时后等待者立即返回

- **WHEN** 一个读正在加载窗口，其他读在等待同一窗口，加载因超时失败
- **THEN** 所有等待者立即收到超时错误

#### Scenario: 前台读超时映射为 I/O 错误

- **WHEN** 前台窗口取数超时
- **THEN** FUSE 读返回 I/O 错误，而不是继续阻塞

### Requirement: 超时允许一次幂等重试

窗口取数超时后，系统 MAY 对同一 Range 请求重试一次；重试 MUST 仅发生在尚未向调用方返回字节时。重试仍失败时 MUST 让整个窗口失败，MUST NOT 返回部分窗口数据。

#### Scenario: 首次超时、重试成功

- **WHEN** 第一次 Range 取数超时，第二次成功
- **THEN** 窗口正常完成，调用方无感知

#### Scenario: 重试仍失败则整窗失败

- **WHEN** 重试后仍超时
- **THEN** 整个窗口失败，不返回部分数据

### Requirement: 超时值由驱动读配置决定

超时默认值 MUST 由驱动读配置派生，MUST NOT 新增用户可调配置。驱动 MAY 覆盖默认值以适配慢速 provider；未覆盖时 MUST 使用保守默认值。

#### Scenario: 慢速驱动覆盖默认值

- **WHEN** 驱动声明更长的空闲或总超时
- **THEN** 该驱动上的读使用驱动值，其他驱动不受影响

#### Scenario: 未声明时使用默认值

- **WHEN** 驱动没有声明超时
- **THEN** 读层使用保守默认值
