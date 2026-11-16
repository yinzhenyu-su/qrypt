## Why

慢链路修复后的 SFTP 短会话实测仍为 16 MiB 有用读取、50.39 MiB 执行器明文流量（3.149×）。当前滑动预取虽受消费游标约束，但空闲容量可使短暂的连续读取迅速铺开较远窗口；seek 和关闭时的未消费尾部仍然较大，需要在保留长播放吞吐的同时减少提前下载。

## What Changes

- 为已知长度、有效挂载读会话的 adaptive 预取增加按唯一成功消费字节推进的有界扩张，从一个小窗口启动；seek 开启新读取趟并重置扩张信用，不能继承上一趟的大窗口。
- 分别计算预取字节边界、单次 Range 尺寸和活动窗口数；驱动建议、阶段计划、共享预算继续构成上限，后台完成、空闲槽位和重复页均不能授予扩张信用。
- 保留 fixed/disabled、显式意图、无会话、未知长度与 ReadStream 路径的现有语义；不新增用户配置，不调整驱动默认参数或超时。
- 补充消费与预取范围的可核对观测、确定性边界测试，以及短 seek / 长连续读取的交替实测与联合性能门槛。

## Capabilities

### New Capabilities

无。

### Modified Capabilities

- `read-pipeline`: 增加会话 adaptive 预取的消费信用、小步启动、渐进扩张、重置和观测要求；继承现有滑动窗口与慢链路目标等待约束。

## Impact

- 主要修改 `pkg/vfs/read/plan.go`、`prefetch_session.go`、`reader.go`、`state.go`，复用现有作用域、块就绪与共享准入；如需调整诊断适配，仅增加可解释的调试数值/枚举。
- Go 公共读取接口、TOML、加密和缓存磁盘格式不变；fixed 策略保持固定尺寸/深度，adaptive 的内部计划携带其模式与上限。
- 前置为 [unify-read-policy-execution](../unify-read-policy-execution/proposal.md) 当前实现及慢链路进展复用修复；该 change 的完整性能验收尚未完成，本提案不替它勾选任务。
- 本次仅产出规划文件；原始测试 JSON、日志和配置保留在仓库外，不提交。本 change 独立验收，不声称任意驱动均有实测收益。
- 接手所需代码、前置工作树修复、实验复现与环境条件见 [实施上下文](implementation-context.md)。
