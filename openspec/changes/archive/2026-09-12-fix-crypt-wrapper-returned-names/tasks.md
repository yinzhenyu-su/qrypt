## 1. 包装层返回调用方词汇的名称（pkg/crypt）

- [x] 1.1 `Rename` 成功后映射回明文：`entry.Name = newName`、`entry.Extra = drive.EntryExtraWrapper{RemoteName: encName, Raw: entry.Extra}`；发往后端的参数不变；验证 `go test ./pkg/crypt/...` 通过
- [x] 1.2 `Move` 成功后同样映射：明文名取入参条目的 `Name`，后端名取后端返回条目的名称；验证 `go test ./pkg/crypt/...` 通过
- [x] 1.3 新增契约回归测试（用 `pkg/crypt` 既有的假驱动）：断言后端收到的是加密名、返回条目的名称是明文名、`Extra.RemoteName` 是加密名；验证该测试在修复前失败（已实测）、修复后通过
- [x] 1.4 复核同文件其它返回条目的方法是否都已遵循该约定（`List`/`Stat`/`PutSource`/`Mkdir`/`Get`）；验证 `git grep -n "return d.raw\." pkg/crypt/drive_wrapper.go` 不再出现"返回条目但未映射"的方法

## 2. 视图条目按路径命名（pkg/vfs/view）

- [x] 2.1 新增包内助手：给定路径与条目，名称与 `path.Base(path)` 不一致时按路径归一化并记 `WarnfEvery`（含路径、原名称、id）；验证 `go test ./pkg/vfs/...` 通过
- [x] 2.2 `CommitUploadedEntry` 与 `CommitRemoteRename` 在写入前使用该助手；验证两处 `entries.Set` 之前都经过归一化（代码复核）
- [x] 2.3 新增用例：提交一个名称与路径不一致的条目后，`view.Entry(path)` 的名称等于路径基名且其它字段不变；验证该用例通过，并确认去掉归一化后它会失败
- [x] 2.4 既有提交用例（`TestCommitUploadedEntryWritesViewState` 等）保持通过，证明一致条目不发生变化；验证 `go test ./pkg/vfs/... -run 'Commit'` 通过

## 3. 端到端验证

- [x] 3.1 组合用例：模拟一次"替换上传 + 重命名"的返回条目被提交进视图后，目录列表中的名称与可寻址路径一致，不出现后端名条目；验证该用例通过
- [x] 3.2 复核 `pkg/vfs/upload/replace.go` 与 `pkg/vfs/mutation/rename.go` 两条消费 `Rename`/`Move` 返回值的路径在修复后行为不变（除名称词汇外）；验证相关包测试全绿

## 4. 文档与验证

- [x] 4.1 在 `docs/for-developer/driver-development.md` 写明包装层的返回契约：返回条目必须使用调用方词汇的名称，后端名经 `Extra`（`drive.EntryExtraWrapper`）暴露
- [x] 4.2 格式与静态检查：`gofmt -l .` 无输出，`go vet ./...`、staticcheck、golangci-lint 通过
- [x] 4.3 全量与竞态：`go test -count=1 ./...` 与 `go test -race ./pkg/crypt/... ./pkg/vfs/...` 全绿
- [x] 4.4 本地 CI 门禁：`scripts/ci-check.sh` 退出码 0
