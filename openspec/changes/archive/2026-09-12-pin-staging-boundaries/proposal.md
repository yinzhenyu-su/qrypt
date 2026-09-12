## Why

`simplify-upload-dead-code`（commit 99e64ec）删掉页缓冲与 `FlushStaging` 后，复核该区域留下的三个空白：

1. `pkg/vfs/upload/staging_store_test.go` 的 `TestStagingSequentialSmallWritesDoNotUseWholeFilePage` 名称仍在描述已删除的页缓冲，其唯一的页断言也随删除消失——现在 body 只断言"顺序小写的返回值与最终 size"，测试名与实际契约不符。
2. `FlushStaging` 删除后 `SyncStaging` 成为 staged 数据的**唯一**持久化点，而它经由 `VFS.Flush` 的错误语义只有 happy path 被覆盖（`pkg/vfs/staging_test.go:165`）。
3. 读 staging 的**打开失败**分支只覆盖了 health 侧（`TestHealthRecordsStagingOpenError`）；同一分支的 observer 契约（恰好一次 finish、一条 `source="staging"` 且携带 error 的记录）与合并后的 phase 轨迹（`resolve → staging_open`，本次删除唯一的可观察变化）没有测试钉住。

此外 staged 数据的**零字节**边界（保存空文件）没有覆盖。

## What Changes

- 重命名上述测试，使其名称与现在真正钉住的行为一致（顺序小写保持偏移与最终大小）；不新增覆盖、不改行为。
- 新增读路径边界测试：pending 记录存在但 staging 文件缺失 → `Read` 返回错误，observer 恰好一次 finish、一条 `source="staging"` 的失败记录，活动操作 phase 轨迹固定为 `resolve → staging_open`。
- 新增 `VFS.Flush` 边界测试：pending 记录存在但 staging 文件被删 → `Flush` 返回可识别的 `os.IsNotExist` 类错误，且 pending 记录零变化（size/hashes 不被部分写入）。
- 新增零字节 staging 探针：`Create` 后不写直接 `Flush` → `pending.Size == 0` 且记录保持可用。**若该探针失败，说明是既有缺陷**（与本次改动无关），按发现单独报告，不在本变更内修补。
- 非目标：不重新引入 `FlushStaging`/页缓冲；不改 `staging_flush` 阶段标记（如需要单独加回）；不覆盖 `pkg/util` 已逐条测过的 `OpenRead` 偏移语义；不重复 `IfUnchanged` 换代不匹配（已由 `pending_store_test.go` 覆盖）。

## Capabilities

### New Capabilities

无。

### Modified Capabilities

无。本变更只增加/重命名测试，不改变任何可观察行为，因此 `.openspec.yaml` 声明 `skip_specs: true`。

## Impact

- `pkg/vfs/upload/staging_store_test.go`（重命名）、`pkg/vfs/read/observer_test.go`（新增用例，复用现有 `missingStagingHost` 式主机）、`pkg/vfs`（新增 Flush 失败用例与零字节探针）。
- 无生产代码改动；无 wire/持久化/配置变化。
