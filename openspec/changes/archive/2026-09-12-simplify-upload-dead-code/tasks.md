## 1. staging 写缓冲与 FlushStaging 链路（pkg/vfs/upload、pkg/vfs、pkg/vfs/read）

- [x] 1.1 复核证据：确认全仓 `pages.Store(` 与 `page{` 仍为 0 命中，并列出 `flush(` 的全部调用点；证据不符则停下来说明，不删。验证：记录 grep 输出（数量与行号）
- [x] 1.2 删除 `page` 类型、`stagingStore.pages`、`stagingStore.flush`、`page.flushNow`，以及 `writeAt`/`size`/`truncate`/`remove`/`sync` 中对它们的调用（`sync` 保留 `f.Sync()`），并在 `sync` 注释里点明 staged 数据的持久化经由它。验证：`go build ./...` 通过且 `go test ./pkg/vfs/upload/... ./pkg/vfs/... -count=1` 全绿
- [x] 1.3 删除 `FlushStaging` 链路：`PendingStore.FlushStaging`、`vfsReadHost.FlushStaging`、`read.Host` 接口方法、`read/reader.go` 与 `read/stream.go` 的调用点，以及 `stubHost`/`fakeReadRuntime`/`stagingHost`/`failingStagingHost` 中的实现。验证：`go test ./pkg/vfs/read/... ./pkg/vfs/... -count=1` 全绿，并在提交说明里解释被移除的 flush 失败注入用例（其主题随方法消失，而非断言被丢弃）

## 2. 无效的失败记录器（pkg/vfs/upload、pkg/vfs）

- [x] 2.1 复核无生产调用者后，删除 `PendingStore.RecordUploadFailure` 与 `RecordUploadPermanentFailure`，并把 3 处测试调用迁移为"先 `UploadByPath` 取当前记录，再调用 `RecordFailureIfUnchanged` / `RecordPermanentFailureIfUnchanged`"，保留原有断言。验证：`go test ./pkg/vfs/... -count=1` 全绿，且 `grep -rn "\.RecordUploadFailure(\|\.RecordUploadPermanentFailure(" pkg` 只剩 `...IfUnchanged` 形式

## 3. Service 上的零调用转发（pkg/vfs/upload）

- [x] 3.1 复核调用点后删除 `Service` 上 8 个零调用转发（`SaveUpload`、`SaveUploadExact`、`UploadByPath`、`RemoveUploadsUnder`、`RenameUpload`、`RemoveStagingIfUnreferenced`、`HashRemoveUnder`、`HashRenamePath`）——复核发现候选中的 `RemoveUpload`、`HashRemovePath` 由 `pkg/vfs/task_source.go`（接收者 `s.svc`）真实调用，故保留；`Service.Retry` 内部对已删转发的两处调用改为直接 `s.store.X(...)`。验证：`go build ./...` 与 `go test ./pkg/vfs/... -count=1` 全绿
- [x] 3.2 确认 `StoreAdapter` 与 `PendingStore` 的公开方法命名未改动（它是装配期接缝，不是死代码）；验证：`git diff` 中 `store.go` 的 `StoreAdapter` 段与 `pending_store_test.go` 未变

## 4. internal/cli 转发/别名单文件（internal/cli）

- [x] 4.1 删除 `fs_compat.go`、`fs_copy_compat.go`、`fs_crypt_compat.go`、`fs_read_compat.go`、`pending_compat.go`、`journal_compat.go`、`config_validation.go`、`build_info.go`，并把测试引用改为直接使用 `internal/cli/fs`、`internal/cli/journal` 的符号（`clifs.CopyDirError(cliRuntime{}, …)`、`clifs.ListEntry`、`clifs.CryptResult`、`clijournal.MaintenanceResult`、`clifs.NewCommand(cliRuntime{})` 等）。验证：`go test ./internal/cli/... -count=1` 全绿
- [x] 4.2 内联两个单调用点包装：`config_path.go` 改用 `config.Validate(state.cfg)`，`command_root.go` 改用 `buildinfo.Current()`；验证：`go build ./...` 与 `go test ./internal/cli/...` 全绿，且 `git grep -n "validateConfig\|currentBuildInfo\|buildInfo\b" internal/cli` 无命中

## 5. 验证与收尾

- [x] 5.1 格式与静态检查：`gofmt -l .` 无输出，`go vet ./...`、staticcheck、golangci-lint 全部通过
- [x] 5.2 全量与竞态：`go test -count=1 ./...` 与 `go test -race ./pkg/vfs/... ./pkg/vfs/upload/... ./pkg/vfs/read/... ./internal/cli/...` 全绿
- [x] 5.3 净删除核对：`git diff --stat` 显示净减少，且本次未新增任何公开 API（`git diff` 中只有删除与签名消失，没有新增导出符号）
- [x] 5.4 本地 CI 门禁：`scripts/ci-check.sh` 退出码 0
