## 1. 终态 item 不被远端观察改写（pkg/core）

- [x] 1.1 `applyRemoteUploadState` 入口判定终态（succeeded / failed / canceled）：终态时只更新远端观测字段（CloudTaskID、CloudState、CloudWritten、CloudTotal、CloudPhase、RemoteID）并返回 false，不改写 State / Error；验证 `go test ./pkg/core/...` 通过
- [x] 1.2 新增定向用例：item 置为 canceled 后应用一次"远端仍在排队/运行中"的观察，断言 item 仍为 canceled、Error 与可用操作不变，且观测字段已更新；验证该用例通过，并确认去掉终态短路后它会失败
- [x] 1.3 新增定向用例：item 置为 canceled 后应用一次"远端成功"的观察，断言 item 仍为 canceled（不被复活为 succeeded）；验证该用例通过

## 2. 终态任务由终态 item 组成（pkg/core）

- [x] 2.1 上传族 `finishTask`：发布结果前先把仍非终态的 item 收敛为 failed（错误信息说明 runner 收尾时该项仍在进行），再按真实终态决定完整成功 / partial_failed / failed；验证 `go test ./pkg/core/...` 通过
- [x] 2.2 下载族 `finishTask`（`download_stream_task.go`）做同样的收敛，保持两族同构；验证 `go test ./pkg/core/...` 通过
- [x] 2.3 新增定向用例：构造一个仍有在飞 item 的批次并触发收尾，断言发布的快照里所有 item 均为终态、任务状态与聚合计数一致（不出现"终态 + 全零计数 + 非终态 item"）；验证该用例通过，并确认去掉收敛后它会失败
- [x] 2.4 回归守护：既有 `TestUploadStreamTaskCancelItemRemovesStaging`、`TestCommitCompleteStagingWithoutReopeningSource`、`TestCoreRecoversCompleteMutableStagingWithoutSource` 保持不变并稳定通过；验证 `go test ./pkg/core/ -run 'UploadStream|RecoversCompleteMutable' -count=20` 通过

## 3. 稳定性验证

- [x] 3.1 复现率验证：连续运行全量 fast suite 4 次（`go test -count=1 ./...`），确认不再出现"终态任务 + 非终态 item"形态的失败；验证 4 次全部通过
- [x] 3.2 并发验证：`go test -race -count=1 ./pkg/core` 全绿（覆盖 ticker 刷新与取消的并发路径）

## 4. 文档与验证

- [x] 4.1 在 `docs/for-developer/` 的任务/上传相关文档中写明两条不变量（终态项不被远端观察改写、终态任务只含终态项）；验证文档与实现一致
- [x] 4.2 格式与静态检查：`gofmt -l .` 无输出，`go vet ./...`、staticcheck、golangci-lint 通过
- [x] 4.3 全量与竞态：`go test -count=1 ./...` 与 `go test -race ./pkg/core/...` 全绿
- [x] 4.4 本地 CI 门禁：`scripts/ci-check.sh` 退出码 0
