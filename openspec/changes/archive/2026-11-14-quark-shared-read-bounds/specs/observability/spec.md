## ADDED Requirements

### Requirement: 操作健康度只统计 provider 失败

挂载操作健康度（`/v1/mounts/health`）SHALL 只把 provider 侧失败计入错误计数与健康等级。调用方取消（`context.Canceled`）与生命周期取消（seek、关闭句柄、会话释放、进程退出）MUST NOT 增加错误计数，MUST NOT 降低健康等级。取消事件 MUST 继续保留在读事件与驱动指标历史中，供诊断使用。

#### Scenario: seek 取消预取不降低健康度

- **WHEN** 一次顺序读完成后用户立即 seek 到远端 offset，触发的后台预取请求被取消
- **THEN** 这些取消不计入挂载健康度错误，健康等级保持 `ok`

#### Scenario: 前台读被调用方取消不计错

- **WHEN** 调用方在读取过程中取消请求，驱动请求随之返回 `context.Canceled`
- **THEN** 该操作不计为 provider 失败

#### Scenario: 取消事件仍可诊断

- **WHEN** 后台预取因取消而停止
- **THEN** 读事件仍记录取消状态与取消原因，驱动指标仍保留该次请求，只是不进入健康度统计

#### Scenario: provider 失败仍然计入

- **WHEN** 驱动请求因超时、5xx、鉴权或网络错误失败
- **THEN** 该失败仍计入对应操作的错误计数并影响健康等级
