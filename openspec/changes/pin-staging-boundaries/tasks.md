## 1. 测试名与契约对齐（pkg/vfs/upload）

- [x] 1.1 重命名 `TestStagingSequentialSmallWritesDoNotUseWholeFilePage` 为反映其实际断言的名称（顺序小写保持偏移与最终大小），并在注释里说明大偏移顺序写这一边界；验证：`go test ./pkg/vfs/upload/... -run TestStagingSequential -v` 通过且名称不再引用页缓冲

## 2. 读路径：staging 打开失败的调试契约（pkg/vfs/read）

- [x] 2.1 在 `pkg/vfs/read/observer_test.go` 新增用例：pending 记录存在、staging 文件缺失 → `Read` 返回错误，断言 observer 恰好一次 finish、一条 `source="staging"` 且携带 error 的 read 记录，并且活动操作 phase 轨迹为 `resolve → staging_open`（把 `simplify-upload-dead-code` 留下的唯一可观察变化固定住）；验证：该用例通过，且临时把 phase 改回 `staging_flush` 会让它失败
- [x] 2.2 `ReadStream` 走同一分支的等价断言（同文件，复用主机）；验证：用例通过

## 3. 持久化失败路径（pkg/vfs）

- [x] 3.1 新增用例：pending 记录存在但 staging 文件被删除 → `VFS.Flush` 返回 `os.IsNotExist` 类错误，且 `UploadByPath` 取回的记录在 size/hashes/时间戳上零变化；验证：用例通过（若当前实现吞掉错误则失败，说明是既有缺陷，需停下报告）
- [x] 3.2 零字节 staging 探针：`Create` 后不写直接 `Flush` → `pending.Size == 0`、记录仍可用（可被读出/上传路径接受）；验证：用例通过或如实记录失败（既有缺陷）并单独报告

## 4. 验证与收尾

- [x] 4.1 `gofmt -l .` 无输出、`go vet ./...`、staticcheck、golangci-lint 通过
- [x] 4.2 `go test -count=1 ./pkg/vfs/...` 全绿；新增/重命名用例单独跑一次确认断言生效（含上述"故意改坏即失败"的负向确认）
- [x] 4.3 确认无生产代码改动：`git diff --stat` 只涉及 `_test.go` 文件
