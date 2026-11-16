# 验收：seek/吞吐交替测量

本文件记录 `optimize-seek-read-profiles` 的真实挂载测量，用于 5.2/5.3 的默认参数决策。
测量在 2026-11-15 的同一台机器、同一账号、同一文件上交替进行；原始样本见文末。

## 环境

| 项 | 值 |
| --- | --- |
| 机器 | macOS 27.2.1 / arm64，家用网络 |
| 账号 | Quark `AAQb7cXsX8sb9r5FlxFN6Zt8` |
| 挂载 | `quark-download`（`root_path=/Download`，无加密） |
| 文件 | `01.郭女俠怒砸同福店 佟掌櫃秒點迷路人.mkv`，1 163 898 860 B |
| 实例 | 两个独立 daemon：基线 HEAD 与 `v0.6.1`，独立 `work_dir`、独立挂载点，`read_cache.max_size=0` |
| 读工具 | 与版本无关的独立程序，`F_NOCACHE`，1 MiB 块 |

## 基线（修复前，14:35–14:44）

顺序读 64 MiB，3 轮交替，有效吞吐中位数：HEAD 8.68 MB/s，v0.6.1 7.84 MB/s。
seek（8 个新位置 × 1 MiB）：HEAD P50 3559.9 ms、P95 4378.9 ms；v0.6.1 P50 3858.6 ms、
P95 4666.0 ms。两版本在噪声范围内。

修复前 HEAD 的驱动快照出现一次回退：

```json
{"download_rotations": 1,
 "last_download_rotate_reason": "download body failed: quark: download too slow",
 "penalized_edges": [{"ip": "124.239.228.20", "reason": "body read failed"}]}
```

即一次正常生命周期取消被吞吐下限判成连接故障，惩罚该 CDN 地址 5 分钟并轮换连接池。

## 修复后（15:21–15:31）

同一脚本、同一交替顺序复测。

顺序读 64 MiB（MB/s）：

| 轮次 | 修复后 HEAD | v0.6.1 |
| --- | --- | --- |
| 1 | 5.28 | 3.68 |
| 2 | 6.48 | 2.70 |
| 3 | 4.62 | 3.95 |
| 中位数 | 5.28 | 3.68 |

seek（8 × 1 MiB，ms）：

| 轮次 | HEAD P50 | v0.6.1 P50 | HEAD P95 | v0.6.1 P95 |
| --- | --- | --- | --- | --- |
| 1 | 9302.0 | 8633.5 | 14763.3 | 9445.5 |
| 2 | 6566.7 | 7478.7 | 9899.8 | 8572.0 |
| 3 | 6866.9 | 7959.2 | 8653.1 | 9103.1 |
| 4 | 6520.6 | 7425.3 | 7432.5 | 8093.3 |
| 中位数 | 6716.8 | 7846.0 | 9276.4 | 8824.2 |

修复后 HEAD 的驱动快照在同样的慢速读取后保持干净：

```json
{"download_rotations": 0, "last_download_rotate_reason": "",
 "penalized_edges": []}
```

注意这一轮的绝对速度比基线差（seek 从约 3.5 s 涨到 6.5–9.8 s，顺序读从 8–18 MB/s
掉到 2.7–6.5 MB/s），说明 CDN 在 15:30 前后整体变慢。两版本在同一时间窗内交替测量，
所以相对比较仍成立；绝对数值不能跨时间窗比较。

## 判定

按 design 的门槛：

- **seek**：HEAD P50 中位数比 v0.6.1 低 14%，未恶化（门槛是“不恶化超过 10%”）。
  P95 高 5%，在噪声内；且 P95 的极值来自同一批慢 CDN 请求。
- **顺序吞吐**：HEAD 中位数比 v0.6.1 高 43%，未下降。
- **错误/重试回退**：修复后 HEAD 无轮换、无 IP 惩罚；v0.6.1 在第 4 轮出现一次
  `input/output error`（EIO），HEAD 同轮正常完成。没有额外的重试或放大证据。
- 声称“提速”需要两轮 20% 改善且无正确性回退。本轮只有顺序吞吐达到 43%，seek 只是
  不恶化，因此**不宣称 seek 提速**，只宣称“移除了取消误判导致的连接惩罚与轮换”。

结论：Quark 维持 `BulkStreams = 1`、不声明阶段建议、不声明 `MaxInFlightRanges`。默认
行为改为“只在真实传输失败时轮换连接”，这是本次唯一调整的 Quark 行为。

## 未验证的部分（5.3）

- 高 RTT 单流驱动与并行获益驱动本轮未测：网络在测量后半段明显变慢，继续测量只会得到
  不可比较的样本。两者的默认 profile **未改动**，因此不存在需要回滚的驱动建议。
- 未在启用持久读缓存（用户默认 10 GiB）时复测；未在晚间拥塞时段复测。
- 确定性测试覆盖（`pkg/vfs/read/stage_latency_test.go`、`stage_policy_test.go`、
  `file_budget_test.go`、`policy_scope_test.go` 与 `pkg/drivers/quark` 的取消/慢速
  用例）不能替代真实云测量，只固定语义。

## 原始样本

- 基线顺序读：`head c1/c2/c3 = 10.49/6.10/8.68 MB/s`，`v061 = 17.97/6.89/7.84`。
- 基线 seek：见 `baseline.md` 表格。
- 修复后 raw JSON 由 `/tmp/qrypt-ab/run-seq.sh` 与 `/tmp/qrypt-ab/run-seek.sh` 生成；
  每次输出的 `mode/ttfb_ms/total_ms/mbps/call_p50_ms/call_p95_ms` 即上表来源。

## 静态验证结果（6.2 / 6.3）

- `gofmt -l`（`$(go env GOROOT)/bin/gofmt`）对全部改动 `.go` 文件无输出。
- `go test ./pkg/vfs/... ./pkg/drive/ ./pkg/readbudget/ ./pkg/crypt/ ./pkg/drivers/quark/ -count=1` 通过；
  `go test ./pkg/vfs/read/ -count=2 -race` 通过。
- `git diff --check` 无输出；`openspec validate optimize-seek-read-profiles --strict` 输出
  `Change 'optimize-seek-read-profiles' is valid`。
- `scripts/ci/check-all.sh`（Go 1.27.4，与 `.github/workflows/ci.yaml` 一致）退出 0 并输出
  `== local CI check passed ==`。
- 未提交：按变更流程，测量与文档完成后由维护者决定提交时机。

## 大窗口合并（3.2）

`waitWindow` 现在按调用方自己的窗口判定是否等待在途窗口：首次读取与 seek 只合并
**不宽于自身窗口**的在途加载，确认连续的读取和显式 bulk 拷贝仍会等待更宽的窗口
（那批数据正是它们下一步要用的）。没有这条约束时，一个 1 块 seek 可能被迫等完一个
8 MiB 预取窗口。

确定性覆盖（`pkg/vfs/read/seek_window_test.go`）：

- `TestSeekDoesNotWaitForOversizedPrefetchWindow`：会话 2 留下被 gate 的宽预取窗口，
  会话 1 seek 到窗口中间，在 gate 未释放的情况下仍完成并发出自己的 1 块 Range；
  宽窗口不被这次 seek 取消。
- `TestSeekStillWaitsForCompatibleWindow`：两块合并窗口与 seek 自身窗口同宽，两次
  seek 共享同一次取数，不会重复下载。

## 慢连接回归（2026-11-15 17:24 线上日志）

用户报告“第一个视频播完关闭播放器、再打开第二个视频加载很久、带宽很低”。线上证据：

- `/v1/ops`：一个 `vfs_prefetch`（8 MiB 窗口）`age_ms=52636`，同一个 chunk 的前台读取
  `age_ms=52625` 仍在 `read_range`，另一个 `vfs_wait` 也在等同一个窗口 —— 前台被投机
  窗口阻塞了 52 秒。
- `qrypt.log`：单条 524416 字节的 body 用了 7.9–20.2 秒（约 26–66 KB/s），
  `ReadBody … size=5768576 read=1408123 err=<nil> dur=1m27s` 与
  `size=8390656 read=1440882 dur=1m14s` 说明 CDN 提前关闭连接（截断）。
- 同时四个挂载的 `download_rotations` 均为 0：上一轮改动把“fetch 超时”与“调用方取消”
  都当成生命周期取消，于是被限速/截断的连接永远不会被换掉。

修正（两处，互补）：

1. 读层用 `drive.ErrFetchStalled` 作为取消原因，驱动据此区分“被放弃的连接”与
   “seek/关闭导致的取消”：前者轮换连接池并惩罚地址，后者保持现状。
2. 驱动把“body 在请求范围读完前干净结束”判为截断并上报，重放前先换连接。
3. `waitWindow` 对投机窗口最多等 3 秒；超过后前台自己取窗口，并取消它放弃的预取。

验证：`pkg/vfs/read/prefetch_wait_test.go`（3 秒放弃 + 快窗口仍合并）、
`pkg/drivers/quark` 的 stall-cause 与 short-body 用例；`go test ./pkg/vfs/... ./pkg/drive/
./pkg/drivers/quark/` 通过，`pkg/vfs/read -count=2 -race` 通过，`check-all.sh` 退出 0。
