---
name: qrypt-test-speed
description: Speed up qrypt test layers without losing coverage — local and CI race, vfs-stability, internal/cli and driver suites. Use when a layer exceeds its time budget, CI feedback is too slow, or tests wait on real wall-clock sleeps and polling loops.
---

# Qrypt 测试加速

目标不是让测试变少或变松，而是把“真实等待”换成“可观测状态 + 可控时钟 + 参数注入”，
保持同一套断言强度。参考基线：race 层 55 s → 28–33 s，`internal/cli -race`
8.9 s → 4.5 s，`pkg/vfs/read -race` 20 s → 4.9 s，`pkg/drive -race` 6.2 s → 2.8 s。

## 1. 先量化，别猜

```bash
QRYPT_TEST_TIMINGS=1 scripts/ci/test-layers.sh race   # 每层打印最慢 10 个测试
go test -race -count=1 -json ./pkg/<pkg> | ...        # 需要逐测试数据时
```

按包归类成本来源，只处理占大头的那一类：

| 成本来源 | 特征 | 处置 |
|---|---|---|
| 真实 sleep / 轮询 | 测试体里 `time.Sleep`、`Eventually`、bounded poll | 换事件/状态原语或可控时钟 |
| 加密 KDF | Argon2id/scrypt 派生，CPU 密集，`-race` 更慢 | 参数注入（仅测试） |
| 驱动重试/退避 | 指数退避、分页等待、settle delay | 包级 `TestMain` 零化或 `-tags fasttest` |
| 纯 CPU/JSON | 无 timer，`-race` 下固定开销 | 只能靠选择、分片、并行度 |

结论先写成 before/after 数字（层耗时 + 最慢测试），否则无法判断是否白花钱。

## 2. 可控时钟（优先）

- `pkg/clock` 提供 `Clock`（`Now`/`After`）、`Real`、`NewFake(start)`、`(*Fake).Advance(d)`、`Pending()`。
- 依赖注入的落点（生产代码只接受接口，绝不读全局 fake）：
  - `pkg/drive.WithClock(ctx, clk)`：fetch 超时/退避/`sleepContext`。
  - `mount.Options.Clock`、`upload.ServiceOptions.Clock` / `DomainOptions.Clock`。
  - `readcache.Store` 的 debouncer：包内测试用 `useClockForTest(clk)`。
- 测试写法：`clk := clock.NewFake(start)`，注入后用 `clk.Advance(...)` 推动到期，
  再用 `Quiesce`/`Flush`/`WaitIdle` 收敛，不要 `time.Sleep` 等结果。
- 判断是否该上时钟：等待的是“时间过去”还是“某个事件发生”？后者用状态原语更稳。

## 3. 状态原语（替代轮询）

- `(*read.Reader).Quiesce(ctx)`：等读窗口/在途 fetch 归零，替代“睡 20 ms 再看一次”。
- `readcache.Store.Flush(ctx)`：等异步 PutChunk 落盘；`FlushReadIndex()` 只保索引。
- `(*vfs.VFS).WaitUploadsIdle(ctx)` / `(*Namespace).WaitUploadsIdle` / `upload.Service.WaitIdle`：
  等上传与暂存清空。
- 原则：给测试一个“可以断言静止”的 API，而不是不断猜 sleep 时长。

## 4. 加密与夹具参数注入

- Argon2id：`crypt.Argon2Params{Time, Memory, Threads}` + `DefaultArgon2Params()`（生产 3/65536/4）。
  单元测试用 `{1,1024,1}`；必须保留一条**生产参数**的 golden vector 测试钉住兼容性。
- scrypt（rclone 兼容）：`crypt.SetScryptParamsForTest(crypt.ScryptParams{N:2,R:1,P:1})`，
  只允许测试 `TestMain` 调用；生产默认仍是 16384/8/1。
- 驱动夹具：`pkg/testsupport/testknobs` 的 `Fast()`、`FixtureBytes(n)`、`Pause(d)`；
  `//go:build fasttest` 下自动缩小夹具/零化 pause，`go test -tags fasttest ./pkg/drivers/...`。
- 现有包级接缝沿用 `TestMain` 模式（`retrySleep`、`partRetryWait`、
  `fileSystemIdlePollInterval`、`vfsSmokeIdleInterval`）。

## 5. 选择与分片（CI）

```bash
scripts/ci/test-layers.sh race-changed        # 只跑 diff 覆盖的包（PR 默认）
scripts/ci/test-layers.sh race-shard 1/4      # 单分片，重成本包各占一片
scripts/ci/test-layers.sh race-shard-all 4    # 本地模拟 nightly 分片矩阵
```

`race-shard-all` 默认把每片 `RACE_P` 减半，避免单机过载反而让 timer 敏感用例 flake；
需要时显式设 `RACE_P`。`race-changed` 无法把 diff 映射到包时（纯 docs/CI 改动）会
打印提示并回退全量，这是预期行为。

## 6. 硬约束（踩过的坑）

- 时钟/测试 seam 必须放在 `pkg/`（如 `pkg/clock`），不能放 `internal/`：
  `scripts/ci/checkarch/main.go` 禁止 `pkg/*` 及其测试导入 `internal/*`。
- 不要在测试体里并发改全局可变成本（`t.Parallel` 会互相踩）；用注入或包级 `TestMain`。
- 生产默认值不许为了测试改小；用参数注入而不是改常量。
- `-tags fasttest` 只允许影响测试与测试夹具路径，不能改变生产行为。
- 改完必须复跑：目标包 → `vfs-stability` 多次 → 完整 `scripts/ci/check-all.sh`。
- 打开 debug 日志会显著拖慢测量（历史经验：约 2×），性能对比要静默日志。

## 7. 验收清单

- [ ] 有 before/after 数字（层耗时、最慢测试），且覆盖范围没缩水。
- [ ] 目标包 `go test -race` 通过，`vfs-stability` 连跑 6 次无 flake。
- [ ] `gofmt`、`go vet`、`check-arch.sh`、完整 `check-all.sh` 全绿。
- [ ] 仓库卫生/架构门禁未被绕过，没有新增 `t.Skip`/放宽断言。
