## 1. 读事件保留拆分（pkg/vfs/read）

- [x] 1.1 把 `historyState` 的单 ring 拆成两份有界保留：`HistoryLimit` 拆为 `SummaryHistoryLimit = 128` 与 `DetailHistoryLimit = 512`，两个环形缓冲各自惰性增长（沿用从 64 起倍增、仅未达上限时复制的现有行为），`Snapshot()` 按写入序号把两者归并成一份时间序列表；验证 `go test ./pkg/vfs/read/...` 通过，且新增单测证明两份容量互不影响（明细写满不影响汇总长度，反之亦然）
- [x] 1.2 `read.State` 暴露汇总与明细两个追加入口：`AppendHistory` 走汇总 ring，新增 `AppendDetailHistory` 走明细 ring，`HistorySnapshot` 返回合并结果，`ResetHistory` 同时清空两者；验证 `go test ./pkg/vfs/read/...` 通过
- [x] 1.3 在两个容量常量的注释中写明内存上界与语义（明细按最近优先、可能被大文件读取冲掉；汇总不因明细被驱逐）；验证注释中的单事件大小与实测一致——`unsafe.Sizeof(drive.MetricEvent{})` 为 504 B，上界约 323 KiB/挂载

## 2. 路由与消费者适配

- [x] 2.1 `diagnostics.ReadRuntime` 增加 `AppendDetailEvent`，`RecordReadDetail` 改走该入口，`RecordRead` 保持 `AppendEvent`；验证 `go test ./pkg/vfs/diagnostics/...` 通过，且 `git grep -n "AppendDetailEvent" pkg/vfs/diagnostics` 显示汇总与明细各只有一条写入路径
- [x] 2.2 `pkg/vfs` 适配层实现新入口（`vfsDebugReadRuntime.AppendDetailEvent` → `read.State.AppendDetailHistory`）；验证 `go test ./pkg/vfs/... -run 'Debug|Read'` 通过，且 `/v1/reads`、`MountSnapshot.Events.Reads`、`debugReadHistoryLimit` 的 JSON 形状与派生关系未变（`go test ./pkg/vfs/... ./pkg/control/...` 通过）

## 3. 守护测试

- [x] 3.1 新增"明细不挤掉汇总"回归测试：在产生远超明细容量的明细事件之后，先前写入的汇总事件仍全部可见且保持时间序；验证该测试通过，并确认把 `SummaryHistoryLimit` 临时改为 1 时它会失败
- [x] 3.2 新增容量上界测试：持续追加两类事件后，两份历史的长度分别停在上界、且不随追加次数增长；验证测试通过
- [x] 3.3 移除 `pkg/vfs/read_test.go:225` 的 `if vfsread.HistoryLimit < 4 { return }` 提前返回，使该断言真正执行；若暴露 `Extra` 字段缺失按真实缺陷修复（不得重新加回跳过逻辑）；验证该用例通过
- [x] 3.4 把既有长度断言更新到拆分后的容量常量（`pkg/vfs/debug_test.go`、`pkg/vfs/debug_read_runtime_test.go`、`pkg/vfs/bench_debug_read_test.go` 中引用 `read.HistoryLimit`/`debugReadHistoryLimit` 之处）；验证 `go test ./pkg/vfs/...` 通过

## 4. 文档与验证

- [x] 4.1 在 `docs/for-developer/debug.md` 的读事件部分说明两段保留、各自容量与"明细最近优先"语义；验证文档中的容量与常量一致
- [x] 4.2 格式与静态检查：`gofmt -l .` 无输出，`go vet ./...`、staticcheck、golangci-lint 通过
- [x] 4.3 全量与竞态：`go test -count=1 ./...` 与 `go test -race ./pkg/vfs/...` 全绿
- [x] 4.4 本地 CI 门禁：`scripts/ci-check.sh` 退出码 0
