## Why

移动端 direct upload、移动端 staging upload 和 VFS 后台上传分别维护输入读取、重试、进度、staging fallback 与远端提交逻辑，导致 direct 路径与 staging 路径重复且难以验证。需要在不改变现有上传语义的前提下收敛这些边界，降低后续新增输入源和 driver 能力时的维护成本。

## What Changes

- 定义统一的上传 source、target、staging writer 和远端执行边界。
- 将上传 task 的状态、取消、重试、进度和完成收敛到共享 runner。
- 保留 driver 支持时 direct upload 不写入 qrypt 本地 staging 的行为。
- 保留 driver 不支持 direct source upload 时自动 fallback 到 staging 的行为。
- 统一 source 到 staging writer 的复制逻辑，移除 direct fallback 和本地文件上传中的重复读写循环。
- 保留移动端 staging 分块写入、FUSE 随机写入、断点恢复、覆盖策略、加密 hash 预扫描和远端提交语义。
- 不改变移动端公开 API、driver 接口和 FUSE 对外行为。

## Capabilities

### New Capabilities

- `upload-pipeline`: 统一 direct source、staging source、上传任务生命周期和远端提交的行为契约。

### Modified Capabilities

无。当前 OpenSpec 根目录没有已有上传 capability spec；本 change 的行为要求由新 capability 记录。

## Impact

- 主要影响 `pkg/core` 的 upload task/service 实现、`pkg/vfs` 的 source/staging/engine 适配和相关测试。
- 移动端 `pkg/mobile` 保持公开 JSON API 兼容，仅调整其内部调用的 Core 实现。
- 不新增第三方依赖，不改变 driver 的 capability contract。
- 需要通过上传相关单测、race test、完整 Go 测试和 CI check。
