## 1. 后端存在性探测（pkg/vfs）

- [x] 1.1 在 `pkg/vfs/mutation/rename.go` 的 `Resolver` 接口新增 `RemoteEntry(ctx, path)`，并在 `pkg/vfs/resolve.go` 实现 `remoteEntry`：解析父目录、列出子项、按名字匹配并过滤 pending 投影（ID 等于 staging FID）；未验证：`go build ./...` 通过，且 `go vet ./pkg/vfs/...` 无输出
- [x] 1.2 在 `pkg/vfs/mutation_rename.go` 用 `remoteEntryResolver` 适配 `RemoteEntry`，避免把新方法扩散到 read/upload 共用的 `pathResolver`；验证：`go build ./...` 通过
- [x] 1.3 用「先重命名目录、再重命名临时文件」的用例确认探测绕开了 `view.ResolveWithRuntime` 对最近本地新建目录子路径的 not-found 短路；验证：`go test ./pkg/vfs -run TestVFSRenameTempFileInsideRenamedDirectory` 通过

## 2. 协调器合并分支（pkg/vfs/mutation）

- [x] 2.1 pending 分支先解析目标父，再用 `RemoteEntry` 探测：无后端对象时保持原纯本地 `RenamePending`；有对象时走 `renamePendingWithRemote`（`InvalidateReadCache` → `RenameMove` → `CommitRemoteRename` → `RenamePending`）；验证：`go test ./pkg/vfs/mutation -run TestCoordinatorRenamePending` 通过
- [x] 2.2 `PartialError` 分支提交「旧父 + 新名」的中转状态，并把 pending 记录移到该中转路径且保留原父标识；验证：`go test ./pkg/vfs/mutation -run TestCoordinatorRenamePendingWithRemotePartial` 通过
- [x] 2.3 后端迁移失败时不移动 pending 记录并返回错误；验证：`go test ./pkg/vfs/mutation -run TestCoordinatorRenamePendingWithRemoteFailure` 通过

## 3. pending 重命名接口（pkg/vfs）

- [x] 3.1 把 `PendingRenamer.RenamePending` 的 `parent drive.Entry` 收紧为 `parentID string`（空值保留记录当前父标识），实现侧相应调整；验证：`go test ./pkg/vfs/mutation ./pkg/vfs` 通过
- [x] 3.2 重命名时清空 pending 记录的 `ReplaceUpload`，使上传引擎在新位置重新探测替换目标；验证：`go test ./pkg/vfs -run 'Rename|Upload'` 通过

## 4. 回归测试

- [x] 4.1 新增端到端回归：临时名一代已上传后继续写入再重命名、先重命名目录再重命名临时文件、编辑远端文件后重命名；验证：三个用例在打补丁前分别残留 `a.mp4.qkdownloading`/`f.mp4.qkdownloading`/`old.txt` 而失败，打补丁后通过
- [x] 4.2 新增协调器单测覆盖搬对象、PartialError、远端失败；验证：`go test ./pkg/vfs/mutation -count=1` 通过
- [x] 4.3 确认既有语义未被破坏：纯本地 pending 重命名仍不触发远端提交、目录重命名仍重定位 pending 子孙；验证：`TestPendingRenameDoesNotTriggerRemoteCommit`、`TestDirectoryRenameRebasesPendingChildren`、`TestVFSRenameFlushedPendingUpload` 通过

## 5. 验证与收尾

- [x] 5.1 运行完整 CI 测试目标（`go test -count=1 ./pkg/vfs ./pkg/drive ./pkg/drivers/... ...`）全绿；验证：命令退出码为 0
- [x] 5.2 `gofmt -l pkg/vfs pkg/vfs/mutation` 无输出、`go vet ./...` 无输出、`.cache/qrypt-tools/golangci-lint run ./pkg/vfs/...` 报 0 issues；验证：三条命令均符合预期
- [x] 5.3 rename/pending 用例在 `-race` 下通过；验证：`go test -race ./pkg/vfs/ ./pkg/vfs/mutation -run 'Rename|Pending' -count=1` 通过
- [x] 5.4 同步规范：把 delta 中的新要求合入 `openspec/specs/rename-pipeline/spec.md`；验证：`openspec validate --change fix-rename-pending-remote-counterpart` 通过
