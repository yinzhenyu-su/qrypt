## Why

重命名/移动是唯一"成功但不交出结果条目"的变更操作：`drive.Driver.Rename`/`Move` 只返回 `error`，VFS 于是提交 resolve 之前的条目（`pkg/vfs/mutation/remote_rename.go:63-86` 只补 `Name`/`ParentID`，`pkg/vfs/view/commit.go:87-102` 原样写入）。对 ID 由路径派生的后端（`localfs`、`s3`、`webdav`、`sftp`、`baidunetdisk`，以及测试用 `pkg/drive/fake.go`）这个 ID 在操作后必然失效，导致后续 `Read`/写前 staging/`List`/`Remove` 作用在旧路径上；s3 上表现为静默删除失败（对象删不掉，旧名字在 shadow 过期后"复活"）。同一根因还造成 rename shadow 永不收敛（旧路径被永久隐藏，之后在旧路径新建的同名文件在列目录里不可见）、目录重命名的子孙条目身份陈旧，以及刚落地的目录重命名 pending 上传重定位对路径派生后端无效。

现在修的理由：批量 move 与目录重命名工作刚落地（`a4a29b4`/`342cf51`/`4fe969e`），这些能力直接依赖重命名后的身份正确性；且当前 9 个提交尚未推送，趁驱动契约还未在更多调用点扩散时扩展签名，代价与冲突风险最低。

## What Changes

- **BREAKING**（in-tree 驱动源码级）：`drive.Driver.Rename`/`Move` 返回 `(drive.Entry, error)`，与 `Mkdir`/`PutSource`/`ServerSideCopier.Copy` 的既有约定对齐；`UnsupportedOperations`、全部 in-tree 驱动、包装驱动（`pkg/crypt`、`pkg/drivers/scopedfs`、`pkg/mobile` 后端）同步更新。
- 路径派生驱动返回操作后真实条目（复用它们内部已经算出的新 ID，零额外远端调用）；不透明 ID 驱动返回携带新 `Name`/`ParentID` 的条目，并把"provider 必须跨 rename/move 保持 ID 稳定"写成契约条款。
- 驱动契约测试新增可验证断言：rename/move 成功后，`List(dstParentID)` 必须包含 ID 等于返回条目 ID 的对象（对最终一致后端带重试）。
- 视图提交以后端身份为准：目录重命名不再保留携带陈旧身份的子孙条目；重命名覆盖已存在目标时失效目标目录的列表缓存；`localDirs` 标记随重命名迁移。
- rename shadow 收敛判据改为"名字已在目标出现"（ID 仅作提示）并引入兜底 TTL，保证最终一致后端下旧路径不会被永久隐藏。
- 测试保真：修正 `pkg/drive/fake.go` 的 `Rename` rekey 顺序缺陷（先改名再 rekey），新增 VFS 级 `rename → Stat/Read/Readdir/Remove` 回归（localfs + 不稳定 ID fake），并复核被掩盖的既有测试。
- 非目标：不改变任何 provider 的 API 调用方式，不引入新的远端查询能力，不改变重命名/移动对用户可见的语义与错误分类。

## Capabilities

### New Capabilities

- `rename-pipeline`: 重命名/移动从驱动到视图的端到端语义——驱动必须交出操作结果条目，视图必须以后端身份提交并可被重新解析，可见性 shadow 必须在后端收敛后清除。

### Modified Capabilities

无。

## Impact

- 驱动层：`pkg/drive`（`Driver`、`UnsupportedOperations`、`fake.go`）、`pkg/drivers/*` 13 个后端、`pkg/crypt/drive_wrapper.go`、`pkg/drivers/scopedfs`、`pkg/mobile/scopedfs.go`。
- VFS 层：`pkg/vfs/mutation`（coordinator/`remote_rename`/backend 适配）、`pkg/vfs/view`（commit/runtime/visibility/resolve）、`pkg/vfs/upload`（`replace.go`、`target_index.go` 的 rename 路径）、`pkg/vfs/source_upload.go`（现有的 `List`+name 补救可简化）。
- 测试：`pkg/contracttest` 契约矩阵、`pkg/vfs` 端到端回归、`pkg/drive/fake.go` 行为修正后可能暴露的既有红灯需逐条判定归属。
- 风险与代价：签名扩散面约 28 个驱动方法 + ≈10 个适配/接口点 + 相关测试假驱动；需要一次全驱动回归。落地按两段式（先纯重构签名，再逐驱动修复身份）以保持每个提交可独立通过 CI。
