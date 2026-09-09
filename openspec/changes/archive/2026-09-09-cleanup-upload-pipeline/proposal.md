## Why

上传管线重构后仍保留少量过渡性包装和重复入口，增加阅读成本，但这些清理不应改变 direct、staging、恢复或 FUSE 行为。本 change 只做行为等价的旧代码整理。

## What Changes

- 统一 Core 中 remote upload completion 的调用入口。
- 将 upload copy 的 chunk size 常量移动到实际使用它的 copy 模块。
- 评估并清理不再需要的 stream 转发包装，只有在调用关系和测试证明安全时才删除。
- 删除由上述整理产生的无用 import、helper 和注释。
- 保留 VFS staging、generation、snapshot、恢复、direct source hash/offset 校验和 driver contract。

## Capabilities

### New Capabilities

无。本 change 是行为等价的内部清理，已通过 `skip_specs: true` 标记。

### Modified Capabilities

无。

## Impact

- 影响范围限定在 `pkg/core` 上传实现及其测试。
- 不改变移动端 JSON API、driver API、task 持久化字段或 FUSE 语义。
- 需要通过上传相关测试、race、完整 CI check 和 OpenSpec 校验。
