# Gate 0 存储引擎 Spike 证据（候选：modernc.org/sqlite v1.61.0）

日期：2026-11-23。Spike 位于仓库外隔离模块（本文件只记录结果），未向生产 `go.mod` 增加依赖。
环境：macOS 26 / Apple M1 Pro / Go 1.27.4 / CGO_ENABLED=0；Linux 运行证据用 OrbStack 的 `golang:1.27` 容器（linux/amd64 与 linux/arm64 各一次）。

## 1.1 候选与事务/锁证据（Linux + Darwin arm64 运行）

- 候选：`modernc.org/sqlite v1.61.0`（纯 Go，无 CGO；直接依赖含 `modernc.org/libc v1.79.0`、`golang.org/x/sys v0.49.0` 等）。
- 许可证：`modernc.org/*` 为 BSD-3-Clause；`golang.org/x/*` BSD-3-Clause；`github.com/google/uuid`、`remyoudompheng/bigfft` BSD-3-Clause；`ncruces/go-strftime`、`mattn/go-isatty` MIT；`hashicorp/golang-lru/v2` MPL-2.0（文件级 copyleft，需在 1.4 记录）。
- 事务与语义测试（`journal_mode=WAL`、`synchronous=FULL`、`foreign_keys=1`）：
  - 事务回滚不留下行；提交后关闭并重开可读到已提交行。
  - `PRAGMA journal_mode` = `wal`，`PRAGMA synchronous` = `2`（FULL）。
  - `Commit()` 返回后立即 SIGKILL 子进程，父进程重开仍读到该行（WAL+FULL 的持久性契约成立）。
  - 双进程互斥：进程 A 持有写事务时，进程 B `BEGIN IMMEDIATE` 被阻塞（busy_timeout 后返回错误），不会并发写入。
- 运行结果：darwin/arm64（原生）、linux/amd64、linux/arm64 全部 PASS。

## 1.2 构建目标（跨编译证据，不代表运行）

`CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' ./cmd/smoke` 均成功：

| 目标 | 二进制大小 |
| --- | --- |
| darwin/arm64 | 6.3 MiB |
| darwin/amd64 | 6.4 MiB |
| linux/amd64 | 6.4 MiB |
| linux/arm64 | 6.3 MiB |
| windows/amd64 | 6.2 MiB |

以上为最小程序（打开账本 + 一次读写）的体积，非完整 qrypt 二进制；全 ABI AAR 增量未测。

## 1.5 初步性能（darwin/arm64，WAL+FULL，300 次迭代）

| 指标 | 结果 |
| --- | --- |
| 单事务 INSERT+Commit | ~90.7 µs/op |
| 关闭后重开 + 查询 | ~438 µs/op |

## 环境阻塞（Gate 0 未通过的原因）

- Windows 运行：无 Windows 主机/Windows 容器运行时，仅有交叉编译证据；按任务要求不能用跨编译冒充运行测试 → 阻塞。
- darwin/amd64 运行：本机未安装 Rosetta 2，无法运行 x86_64 二进制 → 阻塞（构建通过）。
- Android（1.3）：本机有 Android SDK（platforms/build-tools/emulator/system-images android-33）与 NDK 27.2.12479018，但未安装 `gomobile`、未创建 AVD，因此 API 21 全 ABI 绑定与设备/模拟器账本事务 smoke 未执行 → 阻塞。
- iOS（1.3）：有 Xcode 命令行工具（xcrun 72），但未安装 `gomobile`、没有可用的绑定与模拟器 smoke 工程 → 阻塞。

结论：候选在已覆盖的 Linux/Darwin arm64 运行目标上满足事务、持久性、互斥与体积初步预期；但 Gate 0 的必需平台目标（Windows 运行、darwin/amd64 运行、Android API 21 全 ABI、iOS gomobile）未验证，门槛保持关闭，不固定生产依赖、不进入 Slice 1，P0 0.3–0.5 继续等待。
