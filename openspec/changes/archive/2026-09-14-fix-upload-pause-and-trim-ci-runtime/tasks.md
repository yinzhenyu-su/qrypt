## 1. 上传失败的暂停契约（pkg/core）

- [x] 1.1 `uploadStreamItem` 新增 `AwaitingReopen`，注释写明它记录调用方的 `Fail` 意图、以及由哪个 handle 清除
- [x] 1.2 `applyRemoteUploadState` 在既有 `isTerminalStreamItem` 守卫之后加同形守卫：置位时只刷新诊断字段（CloudTaskID/CloudState/CloudBytes*/CloudPhase/RemoteID），State 与 Error 留给调用方
- [x] 1.3 `Fail` 置位；`Write`、`Commit`、`OpenUploadStreamItem`（重开）清除；`Close` 注释说明为何故意不置位（同时是 mobile 的 teardown 路径）
- [x] 1.4 两个守卫用例改为等过 `10 * UploadStreamTaskPollInterval` 再复查 item 状态与能力（`assertFailPauseSticky`），成为确定性回归测试

## 2. 可注入 seam（生产默认值不变）

- [x] 2.1 `pkg/vfs/upload/store.go`：压缩水位改为 `compactMaxBytes`/`compactMaxEntries` 字段，构造时取包级默认
- [x] 2.2 `pkg/vfs/upload/store.go`：新增 `skipJournalSync`（零值即生产 fsync）与 `syncJournalFile`，append/compact 两个 journal 写入点改走它；`PruneUploadJournal` 保持始终 fsync 并注明无 receiver
- [x] 2.3 `pkg/contracttest/fixture.go`：`Fixture` 加未导出 `convergenceStep`（默认 1s），两个轮询从它退避并改用响应 ctx 的 `waitConvergence`
- [x] 2.4 `pkg/drive/contract_behavior.go`：导出 `BehaviorConvergenceStep`（默认 1s）+ `convergenceWait`，两处检查退避改走它
- [x] 2.5 `pkg/drivers/onedrive`：`oneDriveRetryWait`（默认 `util.WaitExponential`）统一 `requestRawWithAuth` 与 copy 重试两处调用

## 3. 测试夹具与等待修正

- [x] 3.1 `internal/cli/command_fs_usage_test.go` 夹具补 `upload_delay`/`delete_delay = 10ms`（此前走默认 2s 删除防抖）
- [x] 3.2 `internal/cli/command_fs_check_test.go`：`copyTree` 保留源 mtime；type-conflict 用例的手工镜像文件对齐 mtime
- [x] 3.3 `pkg/core/task_persistence_test.go`：改为轮询 `Detail` 中它真正断言的恢复诊断，而不是读完 item 列表后只读一次
- [x] 3.4 `pkg/vfs/lifecycle_state_test.go` 共享夹具与 `setmodtime_upload_test.go`：防抖 1s → 200ms，注释记录不变量与 10ms 的失败前例
- [x] 3.5 `pkg/drivers/quark/driver_test.go`：限速用例 deadline 1s → 500ms，注释保留 `body/limit ≫ deadline ≫ auth RTT` 的约束
- [x] 3.6 `pkg/crypt/rclone_interop_test.go`：三个子测试 `t.Parallel()`，deadline 建在子测试内（父测试 `defer cancel` 会掐掉并行子测试）
- [x] 3.7 `pkg/control/test_spec_test.go`：三个 spec 请求改为并行子测试
- [x] 3.8 `pkg/vfs/upload/journal_ops_test.go` + `pkg/vfs/stores_test.go`：压缩-on-append 测试下沉到拥有水位的包并降水位（1100 次追加 → 80 次），追加路径断言不变

## 4. 脚本与 CI 工作流

- [x] 4.1 `scripts/test-layers.sh`：vfs-stability 改为 3 个并发 `-count=1` 进程；失败时只回放失败测试输出
- [x] 4.2 `scripts/coverage.sh`：六个 profile 扇出；失败打印该包日志；`-print` 模式下运行失败仍非零退出
- [x] 4.3 `scripts/install-ci-tools.sh`：三个 `go install` 并发，versions 标记仅在全部成功后写入
- [x] 4.4 `ci.yaml` / `nightly.yaml`：FUSE 头文件按需安装
- [x] 4.5 `scripts/ci-check.sh`：保持串行并补注释（为何不做跨层并发、哪些重叠是安全的）

## 5. 验证

- [x] 5.1 负向对照（每条修复都成立）：去 `AwaitingReopen` → 两个用例以 `item state after the pause = running` 确定失败；`skipJournalSync=false` → 1.31s 对 0.09s；去 onedrive seam → 1.23s 对 0.03s；注入 1.2s 跨秒探针 → 三个 mtime 夹具修复前全失败；去 ctx 感知 → 新守卫用例 3.00s 失败
- [x] 5.2 `go test ./...` 全绿；`scripts/test-layers.sh fast` / `race` / `vfs-stability` 多轮全绿
- [x] 5.3 `-race` 压测防抖夹具：30 轮重复 + 2 轮三路并发 vfs-stability 全绿
- [x] 5.4 `scripts/coverage.sh` 冷缓存 12 轮全绿；gofmt / staticcheck / golangci-lint / govulncheck 通过
- [x] 5.5 `scripts/ci-check.sh` 端到端多次绿灯：150.1s → 107–122s
- [x] 5.6 结果登记：≥1s 用例 10 → 2；最慢用例 3.03s → 1.22s；剩余两个（`TestPendingConcurrentWithList`、`TestRcloneInteropFilenames`）与原因记入 proposal 的 Impact
- [x] 5.7 撤回项记录在案：四个检查组跨层并发（空闲 87s / 繁忙 123s 对 126s）因五次尝试暴露五个负载敏感用例而不发布
