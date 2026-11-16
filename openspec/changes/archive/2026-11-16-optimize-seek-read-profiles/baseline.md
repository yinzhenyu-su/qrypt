# Baseline: seek 读取现状与 A/B 实测

本文件固定 `optimize-seek-read-profiles` 的实施基线：代码边界、实际调用路径、窗口与
并发参数，以及 2026-11-15 在真实 Quark 挂载上做的一次交替测量。后续任务在此基础上
改动，不覆盖既有修复。

## 1. 代码边界

| 对象 | 版本 | 说明 |
| --- | --- | --- |
| 实施基线 HEAD | `01a2c34` | `fix(quark): 按 provider 实际耗时判定下载限速与离线根目录歧义` |
| 对照旧版 | `v0.6.1` | `fec7e2c`，用户当前安装的二进制 |
| 未提交修复 | 无 | 实施开始时 `git status` 仅有本变更目录 |

`01a2c34` 已把 Quark 看门狗的速率判定改为 provider busy 时间，本地 `[bandwidth] download`
限速不再计入 provider 速率。该修复**保留**在基线里，本变更在它之上继续修正分类与下限。

## 2. 实际读路径

| 入口 | 意图 | 会话信息 | 窗口来源 |
| --- | --- | --- | --- |
| FUSE 播放器读 | 未声明（unspecified） | `AccessHint`（session + read 序号） | `readWindowChunks(offset, size)`；连续确认后升级为 bulk 建议 |
| mobile `ReadAtInto` | 默认 `interactive` | 句柄级 session | `InteractiveWindowChunks`（未声明则沿用调用方窗口） |
| `fs read` / `download task` / offline | 显式 `bulk` | 可选 | `BulkWindowChunks` |
| `probe test read` | `probe` | 可选 | `ProbeWindowChunks` |

关键常量（HEAD 与 v0.6.1 相同）：

- `ChunkSize = 512 KiB`、`PrefetchChunks = 8`、`SequentialPrefetchChunks = 16`（8 MiB）。
- 窗口：FUSE 小请求 1 块；跨块未对齐的小读合并 2 块；其余 8 块（4 MiB）。
- 并发：`MaxConcurrency = 32`、`HighReserve = 8`、`PrefetchLimit = 4`。
- 前台/预取合计由同一个 `Scheduler`（HEAD）或 `slotState`+`prefetch.sem`（v0.6.1）限制。

播放器实测到的 FUSE 请求粒度：macOS 在 `F_NOCACHE` 下按 16 KiB 下发，普通播放走
`iosize=3145728`（3 MiB，见用户 `qrypt.toml` 的 `[mount.options] darwin`）。3 MiB 读
命中 `readWindowChunks` 的 `> ChunkSize` 分支，窗口 8 块 = 4 MiB；预取窗口 8 MiB。

## 3. 旧版与 HEAD 的读取差异

| 行为 | v0.6.1 | HEAD 基线 |
| --- | --- | --- |
| 驱动内并行 Range | `size >= 4 MiB` 且 `partCount >= 2`（即 > 8 MiB）时 3 路 8 MiB 分片 | 删除，统一交给读域 |
| 实际是否触发 | **播放器路径不触发**：4 MiB 窗口和 8 MiB 预取窗口都只有 1 个分片 | 不适用 |
| 单窗口流数 | 1 | `BulkStreams = 1`（Quark 默认） |
| 吞吐下限看门狗 | 无 | 有：宽限 5 s、每 3 s 采样、低于 `max(256 KiB/s, 最佳速率/16)` 中止 |
| 中止结果 | — | `io.ErrUnexpectedEOF` → 读层重放窗口、惩罚 CDN IP 5 min、轮换连接池 |
| 取消分类 | 无（无惩罚机制） | `context.Canceled` 且 provider busy ≥ 2 s 且速率 < 512 KiB/s 也判为坏连接 |
| 单次 fetch 总期限 | 无（仅整体超时） | `FetchTotalTimeout = 90 s` |
| 生命周期登记 | `prefetchState.cancels/reasons` 一套 | 单一 `ScopeRegistry`，seek 取消同会话预取 |

结论：**“驱动并行被删除”不是播放路径吞吐下降的解释**。v0.6.1 的并行只对 > 8 MiB 的
单次请求生效，而播放器路径的最大单次请求是 8 MiB 预取窗口，`partCount == 1`。

## 4. 交替 A/B 实测（2026-11-15 14:35–14:44 +08:00）

环境：macOS 27.2.1 / arm64，未限速，Quark 账号 `AAQb7cXsX8sb9r5FlxFN6Zt8`，文件
`/Download/【国产剧】武林外传…/01.郭女俠怒砸同福店 佟掌櫃秒點迷路人.mkv`（1 163 898 860 B）。
两个独立实例、独立 `work_dir`、独立挂载点、`read_cache.max_size = "0"`（关闭持久读缓存，
使每次读取都落到 CDN）。测量工具是与版本无关的独立读程序，使用 `F_NOCACHE`，1 MiB 块。

顺序读：64 MiB，交替 3 轮，单位 MB/s（有效吞吐 = 字节/总耗时）。

| 轮次 | HEAD | v0.6.1 |
| --- | --- | --- |
| 1 | 10.49 | 17.97 |
| 2 | 6.10 | 6.89 |
| 3 | 8.68 | 7.84 |
| 中位数 | 8.68 | 7.84 |

seek：先顺序读 8 MiB 建立播放，再跳到 8 个新位置各读 1 MiB（每次对齐到不同区间，
避免缓存复用），交替 4 轮，单位 ms（= 一次 seek 后 1 MiB 完全可交付）。

| 轮次 | HEAD P50 | v0.6.1 P50 | HEAD P95 | v0.6.1 P95 |
| --- | --- | --- | --- | --- |
| 1 | 3574.9 | 4079.3 | 4154.7 | 12415.7 |
| 2 | 3544.9 | 3750.4 | 4248.0 | 4714.6 |
| 3 | 4013.1 | 3966.8 | 4509.9 | 4617.4 |
| 4 | 3479.1 | 3436.4 | 7067.1 | 3718.3 |
| 中位数 | 3559.9 | 3858.6 | 4378.9 | 4666.0 |

解读：

- 两个版本在这台机器、这个时间窗内**吞吐与 seek 延迟都在噪声范围内**（CDN 单次请求
  3.5–4 s，轮间吞吐 6–18 MB/s）。不能据此宣称 HEAD 普遍更慢，也不能据此宣称并行有用。
- seek 延迟由 CDN 对随机区间的响应主导，而不是由读域窗口选择主导。这使“小 seek 窗口”
  成为**实验性候选**，必须按 design 的门槛验收，不能先验假设它会提速。

## 5. 已确认的 HEAD 独有回退证据

同一次 seek 测量后，HEAD 实例的驱动调试快照：

```json
{
  "download_rotations": 1,
  "last_download_rotate_reason": "download body failed: quark: download too slow",
  "penalized_edges": [
    {"ip": "124.239.228.20", "reason": "body read failed", "expires_in_ms": 240625}
  ]
}
```

对应的读事件为一次 8 MiB `prefetch_window`，`driver_body_ms = 3068`、
`prefetch_cancel_reason = session_release`。即：一次正常的播放生命周期结束/seek 取消，
在 HEAD 上可能被吞吐下限判为连接故障，进而惩罚 CDN 地址 5 分钟并轮换连接池；轮换后
下一次请求需要重新建连（TLS + 冷 CDN 路径），并可能重放已下载的窗口数据。

v0.6.1 没有该机制。这解释了“拖动后更久才出画面”和“峰值下降”，但只在本次测量中偶发
一次（约 9 分钟探测窗口），所以它还不足以解释用户感知的整体回退；需要按 4.1/4.2 修掉，
再按 5.2 的矩阵复测。

## 6. 复现方式

```sh
# 两个独立实例（独立 work_dir、mount_point、关闭持久读缓存）
qrypt mount quark-download --config /tmp/qrypt-ab/head/qrypt.toml
qrypt mount quark-download --config /tmp/qrypt-ab/v061/qrypt.toml

# 交错顺序读
readbench -path "<mount>/<file>" -mode seq -length 67108864

# 交错 seek（每次换 shift，避免命中 48 MiB 内存热块）
readbench -path "<mount>/<file>" -mode seek -warmup 8388608 -seeks 8 -shift $((N*134217728))

# 驱动恢复状态
qrypt debug raw /v1/driver --config /tmp/qrypt-ab/head/qrypt.toml
```

未覆盖：未在晚间拥塞时段、未在启用持久读缓存（用户默认 10 GiB）时、未在加密挂载上
复测；这些在 5.2 的验收矩阵中补齐。

## 7. 确定性阶段的红/绿证据（任务 1.2 / 2.2）

`pkg/vfs/read/stage_latency_test.go` 提供可控的 provider：每个 Range 可以按 offset
被测试用 channel 阻塞，因此“seek 目标先于预取完成”“会话 A 的取消不影响会话 B”这类
断言不依赖 sleep 竞争。`pkg/vfs/read/stage_policy_test.go` 用同一套 harness 断言阶段
策略。

把 `Reader.Read` 的阶段解析临时改回 `StageInitial`（即实施前行为）后的结果：

```
--- FAIL: TestStageSeekSuggestionShrinksFuseForegroundWindow
    seek range plan = {Stage:initial WindowChunks:2 ...}, want the seek stage
--- FAIL: TestStageSeekSuggestionAppliesToInteractive
    interactive seek range size = 1048576, want at most one chunk
--- FAIL: TestStageSequentialSuggestionDrivesLookahead
    no 1048576-byte lookahead Range issued
--- FAIL: TestStageExplicitZeroDepthDisablesLookahead
    lookahead ran despite depth zero: {Offset:2621440 Size:8388608 ...}
```

四个失败分别对应：seek 前台窗口未缩小、interactive 未使用 seek 策略、连续阶段未使用
驱动声明的预取窗口、显式零深度未阻止旧版 8 MiB 预取。恢复阶段解析后全部通过；兼容
用例（1 块窗口、跨块合并、16 块预取、缓存命中、多会话隔离、禁用预取）在两种实现下
均通过。
