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

## 1.3 范围决策（维护者 2026-11-24）

- macOS runner 最低为 `macos-15`（arm64）；darwin/amd64 按 best-effort：CI 先安装 Rosetta 2 运行一次 amd64 测试，失败时降级为 build-only 并记录原因，不阻塞 Gate 0。
- mobile（Android API 21 全 ABI AAR、iOS gomobile 绑定）由维护者决定暂缓；任务 1.3 保持未完成，Gate 0 结论按桌面范围给出。恢复 mobile 时补 AAR 体积/性能与真机 smoke，并重新评估 AAR 增量。
- 因此本文件不宣称 1.3 完成，也不覆盖 AAR 体积增量。

## 1.4 性能与体积初步数据

- darwin/arm64 本机（WAL+FULL，300 次迭代）：单事务 INSERT+Commit 约 90.7 µs/op；关闭后重开 + 查询约 438 µs/op。
- 上述为引擎层小样本，不是端到端 Flush 预算；Flush p99 与 AAR 增量随 1.5/mobile 后续补充。

## 1.5 尚未关闭的 Gate 0 项

- 1.4 固定生产依赖：需要 `pkg/vfs/localstate` 首次引入 import 后随 `go mod tidy` 固定（未使用的依赖会被 tidy 移除），留到 Slice 1。
- 1.5 性能预算：打开/提交 p50/p99 与 Flush 端到端 p99、AAR 体积增量仍未给出；AAR 部分随 mobile 暂缓。
- 1.6 启动能力探测：WAL/FULL 与双进程锁行为已验证，但生产启动探测（WAL/mmap/共享内存能力、失败时拒绝可写打开、持锁进程终止后可重取）待 Slice 1 实现。

## 结论

候选在桌面发布矩阵（linux/amd64、windows/amd64、darwin/arm64，darwin/amd64 经 Rosetta）上满足事务、提交后强杀持久性、双进程互斥与体积初步预期，Gate 0 按桌面范围条件通过；mobile 相关证据（1.3、1.5 的 AAR 部分）明确记为 deferred，恢复 mobile 前不宣称 Gate 0 完整关闭。
