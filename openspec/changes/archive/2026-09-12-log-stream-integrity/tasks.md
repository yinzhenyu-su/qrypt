## 1. 主日志完整性（pkg/logging）

- [x] 1.1 三处写行分支（`logf`、`logfEvery`、`logEveryFunc`）改为：先写主日志，再在级别 >= warn 且错误 sink 与主 sink 不同一时追加写错误日志；验证 `go test ./pkg/logging/...` 通过，且新增用例断言 error 行出现在主日志中
- [x] 1.2 新增用例覆盖单 sink 不重复：`writer == errWriter` 时同一行只出现一次；验证该用例通过，且把去重条件临时改为恒真会使它失败
- [x] 1.3 更新既有分流断言（`pkg/logging/log_test.go` 中依赖 warn 只进 `errWriter` 的断言）到"主日志全量 + 错误日志为 warn+ 子集"的语义；验证 `go test ./pkg/logging/...` 通过

## 2. 安装路径唯一化

- [x] 2.1 `internal/cli/runtime_config.go` 的 `logging.L = newLogger` 改为 `logging.ReplaceDefault(newLogger)`；验证 `go test ./internal/cli/... ./pkg/core/...` 通过，且 `git grep -n "logging.L = " -- '*.go'` 不再出现在非测试代码
- [x] 2.2 新增用例断言安装契约：替换后全局日志器与替换前是同一对象，且旧输出目标被关闭、新目标开始接收日志；验证该用例通过（可在 `pkg/logging` 内直接调用 `ReplaceDefault` 构造旧/新 logger）

## 3. 脱敏覆盖（pkg/logging）

- [x] 3.1 扩展 `sensitivePatterns`，覆盖 `upload_id`/`uploadId`、`access_token`、`refresh_token`、`token`、`Authorization:`、`OSSAccessKeyId`、`Signature` 的 `name=value` 与 `name="value"` 形态；验证 `go test ./pkg/logging/...` 通过，并逐条断言掩码结果
- [x] 3.2 新增用例断言掩码作用于两个 sink：一条 warn 级含凭据行在主日志与错误日志中都已掩码；验证该用例通过
- [x] 3.3 确认 `pkg/drivers/quark` 的 Info 级续传凭据行（`quark_upload.go:63`）经日志器后不再出现原始值；验证方式为构造该格式的消息过 `sanitize` 的用例，不改动驱动代码

## 4. 文档与验证

- [x] 4.1 更新 `docs/for-developer/debug.md` 或配置文档中关于 `logging.log_file` / `logging.error_file` 的说明，写明主日志包含全部级别、错误日志是 warn+ 子集；验证文档描述与实现一致
- [x] 4.2 格式与静态检查：`gofmt -l .` 无输出，`go vet ./...`、staticcheck、golangci-lint 通过
- [x] 4.3 全量与竞态：`go test -count=1 ./...` 与 `go test -race ./pkg/logging/... ./internal/cli/...` 全绿（后者覆盖安装路径的并发写入）
- [x] 4.4 本地 CI 门禁：`scripts/ci-check.sh` 退出码 0
