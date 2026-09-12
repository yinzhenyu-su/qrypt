## 1. 所有权规则（pkg/logging）

- [x] 1.1 `ReplaceDefault` 只关闭旧日志器的自有 sink（`lj` / `errLj`），删除对任意 `io.Closer` 的关闭；验证 `go test ./pkg/logging/ -run TestReplaceDefault -count=1` 通过
- [x] 1.2 `Logger.Close()` 采用同一规则，删除同一回退分支；验证 `go test ./pkg/logging/ -run TestClose -count=1` 通过
- [x] 1.3 两处都写明所有权说明（谁打开的谁释放，借来的 writer 关掉会让进程无法再向该目标输出）；验证代码复核

## 2. 回归测试（pkg/logging）

- [x] 2.1 借用 writer：`ReplaceDefault` 之后借来的 writer 仍打开、全局对象身份不变、级别跟随新配置；验证该用例在修复前失败（已实测）
- [x] 2.2 自有 sink：替换后旧日志器自己打开的文件仍被释放（重命名行为探针，两个观察点方向相反）；验证该用例在移除 `lj` 关闭时失败（已实测）
- [x] 2.3 子进程探针：默认日志器被文件日志器替换后，进程的标准错误仍可写；验证该用例在修复前以退出码 3 失败、修复后通过
- [x] 2.4 `Close()` 不关闭借来的 writer；验证该用例在修复前失败（已实测）

## 3. 端到端验证

- [x] 3.1 真实二进制 A/B：用同一配置（localfs 挂载 + 显式 `[logging] log_level`，挂载点取只读路径让 `Mount` 失败）对比 HEAD 与修复后的 stderr；验证 HEAD 的 stderr 为空、修复后打印 banner 与 `Error: ...`，且两版的日志文件内容都完整
- [x] 3.2 复核日志文件侧的记录未受影响（`Mounting at ...` / `Mount failed: ...` 仍在文件中）；验证 `tail` 日志文件

## 4. 规格与门禁

- [x] 4.1 修改 observability 既有要求：安装只释放自有 sink，MUST NOT 关闭借来的 writer，并补"安装后仍能向标准错误输出"的场景；验证 `openspec validate fix-logger-borrowed-sinks --strict` 通过
- [x] 4.2 格式与静态检查：`gofmt -l .` 无输出，`go vet ./...`、staticcheck、golangci-lint 通过
- [x] 4.3 全量与竞态：`go test -count=1 ./...` 与 `go test -race ./pkg/logging/... ./pkg/core/...` 全绿
- [x] 4.4 本地 CI 门禁：`scripts/ci-check.sh` 退出码 0
