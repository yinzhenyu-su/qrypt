# 写操作由单 owner Core 持久化，并按 applied/absent/unknown 收口

创建型与破坏型写操作（`mkdir`、`remove(file)`、`rename`、`truncate`、`put` 等）不再由客户端尽力重试与补偿，而是经单 owner Core 执行：`pkg/core/ownerlock` 保证同一 state 目录只有一个可写运行时，客户端（fs/FUSE/HTTP）不再各自持有可写 VFS，记录的判定与收口也只发生在 Core 内。每次写尝试携带稳定的 operation ID（wire 名称沿用 `X-Qrypt-Operation-Id`，即 `CONTEXT.md` 的幂等键）；Core 在调用远端前持久化 **intent**（operation ID、请求指纹、目标路径与对象身份、操作意图、成功后的后置条件），远端返回后持久化 **result**，两段都落盘并 fsync。同 ID 且同指纹的重试只回放已记录结果，不再次执行远端写；同 ID 不同指纹返回冲突（`ErrConflict`）；同一 ID 已在途时不得并发发起第二次远端写；ID 永不复用于不同意图。幂等窗口不短于结果保留期：未收口记录（尤其 `unknown`）永不自动清理，终态结果只能由显式维护动作清理，且维护不得删除待对账记录。进程重启后，未完成记录只按可验证的远端事实分类：`applied`（后置条件成立：对象存在且身份/内容匹配）写回记录结果，不重写；`absent`（可证明无效果：对象不存在且无部分效果）允许同 ID 重试；`unknown`（超时、断开等无法证明）标记 `reconciliation_required`，永不自动再写，只能由 `core ops reconcile` 显式处理。重复恢复必须幂等。跨对象原子性不作为承诺：单对象操作原子；目录 copy、递归删除按项记 journal，聚合状态可为 `partial`，同 ID 续传，失败与部分成功都作为记录保留。账本的存储引擎由 Gate 0 决定；Gate 0 通过前本契约只落 ADR、故障注入 harness 与失败测试，不引入生产存储依赖或生产接线。

## Considered Options

- **客户端各自持有可写 VFS 并做失败补偿**（现状）：客户端无法区分“重复写”和“首次执行”，只能追加补偿分支；与 ownerlock 的单写者前提冲突。拒绝。
- **只保留现有进程内 operationMemo（10 分钟 TTL、最多 1024 条）**：崩溃即丢记录，重启后无法判定超时写是否生效；它只能作为进程内快路径缓存，不能作权威记录。拒绝作为持久契约。
- **超时即视为失败并允许重试**：破坏性操作可能执行两次（覆盖、删除、重命名）。拒绝。
- **unknown 自动重试直到成功**：以正确性换收敛，可能产生重复远端对象或覆盖更新后的状态。拒绝。
- **对目录 copy/递归删除做跨对象原子事务**：远端 API 无法参与本地事务，失败必然部分成功；改为按项 journal、聚合 `partial` 与同 ID 续传。
- **把操作记录放进通用 task journal**：task journal 不承担字节级恢复事实，且会让内容事实与 Task 生命周期耦合；内容/操作事实必须独立持久化，Task 只做投影。

## Consequences

- 数据面契约：写 RPC 强制携带 operation ID（缺失时由入口生成并显式返回，调用方重试不得更换）；新增按 ID 查询 operation 的入口（`GET /v1/operations/{id}` 或等价）与稳定错误码 `outcome_unknown`；`core ops list/show` 提供只读入口；`core status` 暴露待对账计数与 ID。
- `pkg/control/operation.go` 的进程内 memo 降级为快路径缓存：跨重启不再有权威；`pkg/control/fs.go` 的写 handler 不得在远端结果未知时返回成功，超时/断连要返回 `outcome_unknown` 并附 operation ID。
- 恢复：Core 启动时加载未完成记录并分类；`applied` 回放、`absent` 允许同 ID 重试、`unknown` 保持可见且不自动重放；恢复流程必须可多轮幂等执行。
- 测试：`QRYPT_TEST_CRASH_AT` 的 kill -9 矩阵（intent 落盘后、远端调用中、远端成功后、result 落盘前/后）先以失败测试引入，覆盖 `mkdir`/`remove(file)`/`rename`/`truncate`/`put`；nightly 故障注入连续通过后才记录基线。
- 词表：`CONTEXT.md` 的 `Operation` 是 task 操作类别（upload/download/delete/copy/move/share）；本文的“操作记录”是 durable write operation record，两者不得混用；wire 上的 `X-Qrypt-Operation-Id` 是既有幂等键名，为兼容保持。
- 不做跨对象原子是长期承诺；目录 copy/递归删除的 API 与用户文档必须表达部分成功与续传，不得宣传全有或全无。
- 本契约不选择存储引擎：Gate 0 通过后由任务 1.7 的 ADR 记录 `pkg/vfs/localstate` 基础层、嵌入式事务账本、磁盘格式 BREAKING 与不回退策略。
