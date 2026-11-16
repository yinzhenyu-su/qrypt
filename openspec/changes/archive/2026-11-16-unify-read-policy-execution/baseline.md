# 实施基线

- 分支：fix-remain-issue；commit：4574fb7。
- 开始时除本 change 未跟踪目录外，工作区干净；既有修复作为已提交基线保留。
- 模块 toolchain：Go 1.27.4。隔离构建缓存：/private/tmp/qrypt-review-go-cache。
- 本方案允许策略 Go/TOML/诊断 JSON 破坏性迁移；Driver.Read、字节、加密和磁盘缓存契约不变。

## 迁移清单

- schema/qrypt.schema.json
- scripts/schema/gen-driver-docs.py
- scripts/schema/gen-config-docs.py
- pkg/crypt/driver_test.go
- pkg/crypt/read_timeout_test.go
- pkg/crypt/drive_wrapper.go
- pkg/drive/read_profile.go
- pkg/drive/read_profile_test.go
- pkg/drive/read_profile_stage_test.go
- pkg/drive/parallel_read.go
- pkg/config/read_profile_test.go
- pkg/drivers/drime/driver_test.go
- pkg/drive/fetch_timeout.go
- pkg/config/validation.go
- pkg/drive/parallel_read_test.go
- pkg/drivers/drime/read.go
- pkg/config/config.go
- pkg/drivers/p115/read_profile.go
- pkg/drivers/p115open/read_profile.go
- pkg/drivers/p123/driver_test.go
- pkg/drivers/p123/p123_read.go
- pkg/core/config_mgmt_test.go
- pkg/drivers/koofr/driver_test.go
- pkg/drivers/p115/download_client_test.go
- pkg/core/read_profile_test.go
- pkg/core/core.go
- pkg/drivers/koofr/read.go
- pkg/drivers/quark/driver_test.go
- pkg/core/share_fetch.go
- pkg/drivers/capability_test.go
- pkg/drivers/quark/observability.go
- pkg/vfs/debug_runtime.go
- pkg/vfs/drivecopy/directcopy_test.go
- pkg/vfs/drivecopy/directcopy.go
- pkg/vfs/read_profile_config_test.go
- pkg/vfs/read_timeout_snapshot_test.go
- pkg/vfs/diagnostics/dto.go
- pkg/vfs/read/prefetch_wait_test.go
- pkg/vfs/read/parallel_policy_test.go
- pkg/vfs/vfs.go
- pkg/vfs/diagnostics/snapshot.go
- pkg/vfs/namespace.go
- pkg/vfs/read/fetch_timeout_test.go
- pkg/vfs/diagnostics/readprofile.go
- pkg/vfs/read/seek_window_test.go
- pkg/vfs/read/stage_latency_test.go
- pkg/vfs/read/file_budget_test.go
- pkg/vfs/read/plan_override_test.go
- pkg/vfs/read/cache_policy_test.go
- pkg/vfs/read/plan.go
- pkg/vfs/read/scenario_test.go
- pkg/vfs/read/plan_test.go
- pkg/vfs/read/stage_policy_test.go
- pkg/vfs/read/fetch_timeout.go
- pkg/vfs/read/intent_test.go
- pkg/vfs/read/reader.go

## 依赖和冲突

optimize-seek-read-profiles 与 add-read-part-size 已在当前基线实现；其历史保持不动。归档顺序为前置 change 先同步，再同步本 change。simplify-read-pipeline 的共享取数与窗口收敛由本 change 承接，不重复创建实现。

本 change 覆盖旧兼容条款、旧配置表和分片末片吸收规则，全部冲突决议见 design.md。执行 strict validate 确认三份 delta 可解析；不在本次实施前自动归档历史 change。

## 初始基线验证（后续结果见 validation.md）

- go test ./pkg/drive ./pkg/crypt ./pkg/config ./pkg/readbudget ./pkg/vfs/read ./pkg/drivers/quark：全部通过。
- 新增 TestReadPolicyByteContract：无并行、并行、内部并行三种能力下的未对齐/尾部读取字节一致。
- 三个依赖/当前 change strict validate 均通过。
- 实施早期的新完整准入接口 readbudget race 测试通过；当时尚未全部迁移调用方，任务 3.2 保持未完成。
