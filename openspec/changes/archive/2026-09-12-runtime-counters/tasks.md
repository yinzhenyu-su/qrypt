## 1. 计数器类型（pkg/drive）

- [x] 1.1 新增无锁累计计数器：操作数、错误数、字节数、总耗时（atomic），记录接口 `RecordRead(bytes int64, elapsed time.Duration, err error)` 语义与读事件一致（失败计入错误数且不带字节）；验证 `go test ./pkg/drive/...` 通过，且新增用例断言记录路径 `testing.AllocsPerRun` 为 0
- [x] 1.2 新增固定桶延迟直方图（编译期常量边界，1ms 起以 2/5 序列到 5s，含溢出桶）与快照 DTO；验证桶边界测试通过，且超出最大边界的样本落入溢出桶
- [x] 1.3 新增分位派生：`Quantile(q)` 走桶累计返回桶上界，均值为总耗时/操作数；验证用例断言 p50 ≤ p95 ≤ p99、溢出样本计入总量、零操作时返回零值不除零
- [x] 1.4 并发记录测试：多 goroutine 并发 Record 后快照，操作数与字节数精确等于记录次数（`go test -race ./pkg/drive/...` 通过）

## 2. 读路径接入（pkg/vfs/read）

- [x] 2.1 `pkg/vfs/read/host.go` 新增可选 `CounterRecorder` 接口与 no-op 实现（与 `HealthRecorder`/`ReadObserver` 并列，不拓宽 Host 表面）；验证 `go test ./pkg/vfs/read/...` 通过
- [x] 2.2 `ReaderDeps` 增加计数器 sink，`Reader.Read` 用单一 defer 记录一次（覆盖成功与全部提前返回路径），bytes 为本次调用物化的字节数（远端为 `len(data)`、staging 直通为 0）；验证新增用例覆盖"成功记一次""失败记一次且错误数 +1""提前返回也记一次"
- [x] 2.3 新增用例断言每个 `Read` 调用恰好记录一次（不因内部 chunk/window 循环重复计数）；验证该用例通过

## 3. 装配与暴露（pkg/vfs、pkg/vfs/diagnostics）

- [x] 3.1 每个挂载持有计数器实例并在 `ReaderDeps` 注入；验证 `go test ./pkg/vfs/... -run 'Read|Debug'` 通过
- [x] 3.2 `MountSnapshotRuntime` 增加 counters 段并装配（累计值 + 直方图 + 派生均值/分位）；验证快照测试断言 counters 出现且数值与直接读取计数器一致
- [x] 3.3 确认追加字段不破坏既有消费方：`DebugSnapshotSchemaVersion` 不变，`go test ./pkg/vfs/... ./pkg/control/... ./internal/cli/...` 全绿
- [x] 3.4 零活动挂载用例：从未读取的挂载 counters 全零、直方图为空、快照可解析；验证该用例通过

## 4. 文档与验证

- [x] 4.1 在 `docs/for-developer/debug.md` 说明 counters 段的读法、bytes 口径（staging 直通为 0）、以及"累计值 ≠ 保留窗口"是正常的；验证文档与实现字段一致
- [x] 4.2 格式与静态检查：`gofmt -l .` 无输出，`go vet ./...`、staticcheck、golangci-lint 通过
- [x] 4.3 全量与竞态：`go test -count=1 ./...` 与 `go test -race ./pkg/drive/... ./pkg/vfs/...` 全绿
- [x] 4.4 本地 CI 门禁：`scripts/ci-check.sh` 退出码 0（注意 `docs/for-user/` 的生成文档需与索引一致）
