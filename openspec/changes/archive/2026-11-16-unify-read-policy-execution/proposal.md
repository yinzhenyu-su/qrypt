## Why

读取策略的可选输入、有效计划、硬限制及运行时预算目前分散在 VFS、直读、探测与预取路径中解释，增加了遗漏和并发缺陷。用户已明确允许 Go、TOML 和诊断 JSON 破坏性迁移，应一次收敛职责与语义，再按真实测量选择驱动参数。

## What Changes

- **BREAKING**：替换平铺 ReadProfile、可选阶段补丁及接受原始 profile 的直读入口。驱动返回完整默认策略；用户配置使用独立的稀疏覆盖类型；执行端仅接受完整计划。
- **BREAKING**：将 mounts.read_profile 替换为 mounts.read，尺寸使用字节单位；旧表必须报错并给出迁移提示，不设置别名或长期双接口。
- **BREAKING**：诊断输出升级为 read_policy，分别展示建议、硬限制、有效策略、每次计划与实际执行，不兼容旧 JSON 字段。
- 以一个读取源统一 VFS、直读、流式读取、探测、重试和预取的许可、分片、超时、回退与生命周期；文件总预算不按 reader 或调用路径重置。
- 将窗口与预取分别解析，保留意图门禁；固定分片大小改为真正的单分片上限，不再由末片吸收超大余量。
- 保留 Driver.Read 字节契约、加密格式与缓存磁盘格式；迁移全部内置驱动和消费者，完成正确性、性能与泄漏验收。

## Capabilities

### New Capabilities

无。

### Modified Capabilities

- `read-pipeline`：统一读取源、完整计划、文件总预算、分片执行和可靠接管。
- `read-profile-config`：新配置结构、明确覆盖语义、破坏性迁移与诊断输出。

## Impact

影响 pkg/drive、pkg/readbudget、pkg/vfs/read、配置/schema/生成器、驱动建议、加密包装、core/mobile/CLI/control 与调优工具。无新增依赖，不修改上传、加密格式或缓存磁盘格式。

承接 [optimize-seek-read-profiles](../optimize-seek-read-profiles/proposal.md) 与 [add-read-part-size](../add-read-part-size/proposal.md)，保留其历史与修复；本变更取代它们的接口兼容、配置和分片表达。与 [simplify-read-pipeline](../simplify-read-pipeline/proposal.md) 复用现有调度和作用域对象。实施进度和未完成的验收见 tasks.md 与 validation.md。
