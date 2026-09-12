## 1. 策略表与查询（pkg/task）

- [x] 1.1 在 `pkg/task` 新增类型描述符与注册表（每类型一行：`Creation`、`Operation`、`BatchOf`、`Persistent`、`Dismissible`、`UserVisible`、`Recoverable`、`Retry`）以及查询 `Describe`/`Descriptors`/`Promote`/`RecoverableTypes`/`ScopeForType`/`OperationForType`；验证 `go test ./pkg/task/...` 通过，且完整性测试报告 11 个类型常量各有一行描述符
- [x] 1.2 把 `pkg/task/operation.go` 的 type→operation 映射改为查表（保持函数名与调用点不变，派生仍发生在 `manager.SubmitIdempotent`）；验证 `go test ./pkg/task/... ./pkg/core/... -run 'Operation|Idempot|Persist'` 通过
- [x] 1.3 新增描述符一致性测试：每个类型常量有且仅有一行、`Operation` 非空、`Promote` 对声明了 `BatchOf` 的类型返回表内已知类型；验证 `go test ./pkg/task -run Descriptor -v` 通过，并确认临时删掉一行描述符会让该测试失败
- [x] 1.4 在 `pkg/vfs` 的两个同步生产者（`task.go`、`delete_task.go`）改为从描述符读取类型与操作类别，消除硬编码不一致；验证 `go test ./pkg/vfs/... -run 'Task'` 通过

## 2. 创建路径派生（pkg/core）

- [x] 2.1 删除 `core/upload_task.go`、`core/delete_task.go`、`core/move.go` 的提升谓词与 `core/task.go` 操作路径上的 2 处内联提升，统一改调 `task.Promote`；验证 `go test ./pkg/core -run 'Move|Delete|Upload|TaskOperation'` 通过，且 `git grep -n "len(.*Items) > 1" pkg/core` 不再出现类型提升用法
- [x] 2.2 `Persistent`/`Dismissible` 的创建默认值改为从 `Describe(...)` 读取（删除 7 处字面量），运行期覆盖保留在创建函数内（跨挂载 move 升级、copy/download 按 spec 计算）；验证 `go test ./pkg/core -run 'Persistence|Task'` 通过，且代码中不再出现 `taskType == task.TypeXxx` 形式的持久化表达式
- [x] 2.3 把 `UserVisible` 落到创建处：用户可见类型必须带 `ScopeUser`，同步记账生产者显式声明不可见；验证 `go test ./pkg/core ./pkg/mobile -run 'Scope|Task'` 通过，且 App 默认列表过滤结果不变
- [x] 2.4 新增黄金表断言：`CreateTask` 接受的类型集合 == `Descriptors()` 的类型集合（含显式传入的批量类型）；验证该测试通过，并确认新增一个未接线类型会使其失败

## 3. 恢复与重试分派（pkg/core）

- [x] 3.1 `newTaskManager` 的两次硬编码恢复调用改为遍历表中声明可恢复的类型，各族的类型过滤器与判定函数保持不变；验证 `go test ./pkg/core -run 'Recover|UploadStream|Interrupted'` 通过
- [x] 3.2 `RetryTask` 改为先按 `Describe(typ).Retry` 分派策略，再进入既有实现体（唤醒分支的"批次仍在内存"前置条件与非阻塞发送保留）；验证 `go test ./pkg/core -run 'Retry'` 通过（含 retry wait 唤醒用例）
- [x] 3.3 新增断言：恢复注册的类型集合 == 表中 `Recoverable` 为真的类型集合；验证该测试通过

## 4. 文档与契约

- [x] 4.1 `docs/for-developer/mobile-interface.md` 的任务类型表补"批量提升"与"App 可见性"两列，并写明复制/下载不声明批量类型；验证类型表与 `Descriptors()` 逐行一致（人工比对或脚本检查），且 `openspec validate task-type-descriptor --strict` 通过

## 5. 验证与收尾

- [x] 5.1 格式与静态检查：`gofmt -l .` 无输出，`go vet ./...`、staticcheck、golangci-lint 全部通过
- [x] 5.2 全量与竞态：`go test -count=1 ./...` 与关键包 `go test -race`（`pkg/task`、`pkg/core`、`pkg/vfs`、`pkg/mobile`、`pkg/control`）全绿
- [x] 5.3 本地 CI 门禁：`scripts/ci-check.sh` 退出码 0
