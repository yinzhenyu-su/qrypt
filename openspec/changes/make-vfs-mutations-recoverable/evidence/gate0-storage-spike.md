# Gate 0 存储引擎 Spike 证据（候选：modernc.org/sqlite v1.61.0）

日期：2026-11-23 首轮本机验证；2026-11-24 CI 平台矩阵验证。
Spike 源码：[`scripts/dev/storage-spike`](../../../../scripts/dev/storage-spike/README.md)（独立 Go module，主模块不依赖该引擎）；CI：[`storage-spike.yaml`](../../../../.github/workflows/storage-spike.yaml)（`workflow_dispatch`，run [45312526536](https://github.com/yinzhenyu-su/qrypt/actions/runs/45312526536)）。
环境：Go 1.27.4、`CGO_ENABLED=0`。

## 1.1 候选与语义证据

- 候选：`modernc.org/sqlite v1.61.0`（纯 Go，无 CGO；直接依赖含 `modernc.org/libc v1.79.0`、`golang.org/x/sys v0.49.0` 等）。
- 许可证：`modernc.org/*`、`golang.org/x/*`、`github.com/google/uuid`、`remyoudompheng/bigfft` 为 BSD-3-Clause；`ncruces/go-strftime`、`mattn/go-isatty` 为 MIT；`hashicorp/golang-lru/v2` 为 MPL-2.0（文件级 copyleft，需在 1.4 记录）。
- 测试语义（`journal_mode=WAL`、`synchronous=FULL`、`foreign_keys=1`）：
  - 事务回滚不留下行；提交后关闭并重开可读到已提交行。
  - `PRAGMA journal_mode` = `wal`，`PRAGMA synchronous` = `2`（FULL）。
  - `Commit()` 返回后进程被强杀（unix `SIGKILL`，Windows `TerminateProcess`），父进程重开仍读到该行。
  - 双进程互斥：进程 A 持有写事务时，进程 B `BEGIN IMMEDIATE` 被阻塞（busy_timeout 后返回错误）。

## 1.2 平台运行矩阵（CI）

全部任务通过；体积为最小程序（打开账本 + 一次读写）`-trimpath -ldflags='-s -w'` 的二进制大小。

| 平台 | Runner | 运行结果 | 二进制大小 |
| --- | --- | --- | --- |
| linux/amd64 | `ubuntu-latest` | 事务/持久性/双进程锁 PASS | 6,729,888 B（6.42 MiB） |
| windows/amd64 | `windows-latest` | 同上，崩溃点用 `TerminateProcess` PASS | 6,500,352 B（6.20 MiB） |
| darwin/arm64 | `macos-15`（arm64） | 同上 PASS | 6,569,314 B（6.27 MiB） |
| darwin/amd64 | `macos-15` + Rosetta 2 | Rosetta 安装成功，amd64 测试二进制与体积程序在 Rosetta 下 PASS | 交叉构建约 6.4 MiB（best effort） |

同一矩阵还运行 1.6 的 owner lock 与启动能力探测测试（见 1.5）；全部通过后本轮取 run [45315070009](https://github.com/yinzhenyu-su/qrypt/actions/runs/45315070009)。

## 1.3 范围决策（维护者 2026-11-24）

- macOS runner 最低为 `macos-15`（arm64）；darwin/amd64 按 best-effort：CI 先安装 Rosetta 2 运行一次 amd64 测试，失败时降级为 build-only 并记录原因，不阻塞 Gate 0。
- mobile（Android API 21 全 ABI AAR、iOS gomobile 绑定）由维护者决定暂缓；任务 1.3 保持未完成，Gate 0 结论按桌面范围给出。恢复 mobile 时补 AAR 体积/性能与真机 smoke，并重新评估 AAR 增量。
- 因此本文件不宣称 1.3 完成，也不覆盖 AAR 体积增量。

## 1.4 打开/提交延迟与预算（引擎层，run [45315070009](https://github.com/yinzhenyu-su/qrypt/actions/runs/45315070009)）

每个平台 20 次 open+close、200 次单事务 INSERT+Commit（WAL+FULL）的 p50/p99：

| 平台 | open p50 | open p99 | commit p50 | commit p99 |
| --- | --- | --- | --- | --- |
| linux/amd64 | 236 µs | 1.24 ms | 244 µs | 445 µs |
| windows/amd64 | 2.10 ms | 29.5 ms | 4.55 ms | 10.68 ms |
| darwin/arm64 | 1.24 ms | 14.9 ms | 273 µs | 1.59 ms |
| darwin/amd64（Rosetta） | 2.81 ms | 6.72 ms | 397 µs | 2.83 ms |

- 预算（`TestLatencyBudget` 的 order-of-magnitude 守卫）：open p99 ≤ 250 ms，commit p99 ≤ 50 ms。Windows runner 的 `synchronous=FULL` fsync 抖动在两次运行里为 10.5/29 ms 与 10.7 ms，因此 CI 预算取平台包络；Slice 2 得到端到端数据后再收紧提交预算。
- 上述是引擎事务延迟，不是端到端 Flush 预算；Flush 需要内容检查点协议，随 Slice 2 测量。AAR 体积增量随 mobile 暂缓。

## 1.5 Gate 0 各项状态

- 1.4 固定生产依赖：按任务文本属于“门槛通过后”的步骤，待 `pkg/vfs/localstate` 首次引入 import 后随 `go mod tidy` 固定，并记录 MPL-2.0 传递依赖。
- 1.5 性能预算：open/commit p50/p99 与预算已记录（1.4 节）；Flush 端到端 p99 与 AAR 增量按 2026-11-24 范围注记延迟到 Slice 2/mobile，恢复时重开本项。
- 1.6 启动能力探测与 OS 排他锁：已在 spike 实现并跨平台验证——Unix `flock(LOCK_EX|LOCK_NB)`、Windows `LockFileEx(FAIL_IMMEDIATELY)`；打开时探测 `journal_mode=wal`、`synchronous=2` 并执行一次真实 WAL 写事务后回滚，任一失败返回 `ErrUnsupported` 并拒绝可写打开（同时释放 owner lock），不静默降级。测试覆盖：第二个 Open 返回 `ErrLocked`；持有者被 kill 后 10s 内可重新获取；probe 失败后锁已释放、后续可正常打开。生产接线（2.4）随 Slice 1 从 spike 移植。

## 结论

候选在桌面发布矩阵（linux/amd64、windows/amd64、darwin/arm64，darwin/amd64 经 Rosetta）上满足事务、提交后强杀持久性、双进程互斥、owner lock/启动探测、体积与延迟预算，Gate 0 按桌面范围通过（1.1/1.2/1.5/1.6 有证据）；mobile（1.3）与 AAR 体积增量明确记为 deferred，恢复 mobile 前不宣称 Gate 0 完整关闭。1.4 固定依赖与 1.7 ADR 属于通过后的步骤，按 tasks.md 顺序在 Slice 1 前完成。
