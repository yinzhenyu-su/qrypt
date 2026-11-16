## Why

视频播放同时需要 seek 后尽快获得数据与连续读取的稳定吞吐；当前 ReadProfile 将前台窗口、预取深度和 bulk 流数耦合，无法分别表达这些目标。Quark 的新增连接恢复策略还可能将正常取消或本地限速误判为 CDN 故障，需要以可测量的共享策略恢复性能，而非将单个账号的参数推广到所有驱动。

## What Changes

- 在现有意图之下引入会话访问阶段：首次读取、seek 恢复、连续读取；独立解析前台窗口与后台预取窗口。
- 扩展驱动 ReadProfile 的阶段建议，分离单窗口流数与单文件并发预算；保留现有字段和挂载 bulk 覆盖语义。
- FUSE 未声明意图与移动端 interactive 共享 seek 优化；interactive/probe 单窗口保持单流，不以访问阶段偷偷改写意图。
- 修正 Quark 主动取消、限速与网络故障的分类，避免正常 seek 导致连接轮换、IP 惩罚或重放。
- 增加计划与延迟分解观测，以及同文件交替性能验收；按驱动能力保守回退。

## Capabilities

### New Capabilities

无。

### Modified Capabilities

- `read-pipeline`: 增加按会话阶段选择前台/预取策略、独立并发预算、正常取消不惩罚连接及性能诊断要求。
- `read-profile-config`: 明确扩展后的驱动默认值、兼容性与有效策略来源观测；不增加用户配置字段。

## Impact

涉及 pkg/drive/read_profile.go、pkg/readbudget、pkg/vfs/read、Quark 读取与连接恢复、加密驱动 profile 透传、mobile/FUSE 会话、probe 指标与测试。无新依赖；不改变 Driver.Read、缓存格式或用户配置。与 [simplify-read-pipeline](../simplify-read-pipeline/proposal.md) 共用 ReadPlan/Scheduler/ScopeRegistry，不另建调度器或生命周期注册表；实施前以当前 HEAD 与未提交修复重新核实基线。本变更仅制定方案，不实施代码。
