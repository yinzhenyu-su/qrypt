## 1. 用例同步改为协议事件（pkg/drivers/quark）

- [x] 1.1 OSS 假服务器在收到 part 2 PUT 时（挂起前）用 `sync.Once` 关闭 `part2Started`；合并 PUT 分支重复的加锁。验证：`go test ./pkg/drivers/quark/ -run TestDriverPutMultipartUploadResumesPersistedParts -count=3` 通过
- [x] 1.2 第一次上传改为 goroutine + 可取消 context：等 `part2Started` 后取消，再断言返回错误；附带"part 2 之前就结束"与"30 s 未到 part 2"两条诊断分支，避免失败时挂死或只报超时。验证：用例通过且断言保持原语义
- [x] 1.3 慢环境验证：给假服务器每个请求加 50 ms 延迟 → 新同步两次运行都通过（0.69s / 0.66s）；同样延迟下把等待改回 `WithTimeout(150ms)` + 同步调用 → 两次都以 `part 1 uploads after first attempt = 0, want 1` 失败（复现 CI 现象），之后恢复最终版本
- [x] 1.4 竞争压力验证：`go test -race -count=5 ./pkg/drivers/quark/ -run TestDriverPutMultipartUploadResumesPersistedParts` 5/5 通过

## 2. 验证

- [x] 2.1 `gofmt -l .` 无输出、`go vet ./...` 通过
- [x] 2.2 `scripts/ci-check.sh` 全绿（含 race 层与 vfs-stability）
- [x] 2.3 确认无生产代码改动：只有 `pkg/drivers/quark/driver_test.go` 被修改
