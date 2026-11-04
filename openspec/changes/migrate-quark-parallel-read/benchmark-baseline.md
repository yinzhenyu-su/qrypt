# Quark 并行读迁移：基准性能记录

记录时间：2026-11-04
代码版本：`b8d6949`（包含 `probe test read --path` 只读测量支持）
目的：在把 Quark 从 `internal_parallel_read` 迁移到 `parallel_range` 之前，记录同一文件、同一命令的读取基准，迁移后用相同命令复测对比。

## 测试对象与环境

- 挂载：`quark-encrypt`（加密挂载，root `/Encrypt`），测试期间临时设置 `test_enabled = true`，测完已恢复为 `false`。
- 文件：`/Linus.mp4`，198,299,859 字节（约 189 MiB），只读测试，未创建、修改或删除任何云盘内容。
- 环境：macOS arm64，macFUSE，1 MiB 读块，`cache-mode=both`，`pattern=both`，3 次采样，10 个 seek 点、每次 1 MiB。
- 校验：3 次冷读和 3 次热读的内容 SHA-256 完全一致（`4f1bc3ae…a215a57`），确认读取内容稳定。

## 命令

```sh
qrypt probe test read --config ./qrypt.toml --mount quark-encrypt \
  --mount-point /tmp/qrypt-real/qe/mnt --path "/Linus.mp4" \
  --samples 3 --cache-mode both --pattern both \
  --seek-count 10 --seek-size 1m --block-size 1m
```

`--path` 是本次为基准测量新增的只读模式：不创建测试文件、不执行清理，顺序读的哈希在样本间交叉校验，seek 不校验 fixture 字节模式。

## 基准结果

### 顺序读（整个文件，198,299,859 字节）

| 模式 | 样本 1 | 样本 2 | 样本 3 | 中位 | p95 TTFB |
| --- | --- | --- | --- | --- | --- |
| 冷读 | 18.96 MB/s | 13.16 MB/s | 27.18 MB/s | **18.96 MB/s** | 1.31 s |
| 热读 | 196.5 MB/s | 202.7 MB/s | 202.5 MB/s | **202.5 MB/s** | 6.2 ms |

冷读中位 TTFB 242.8 ms，峰值 1 秒窗口 312.2 MB/s（缓存命中导致峰值虚高）。

### Seek（10 个 offset × 3 次采样）

| 模式 | 中位加载 | p95 加载 | p99 加载 | 中位加载吞吐 |
| --- | --- | --- | --- | --- |
| 冷 seek | **1.50 s** | 2.37 s | 2.53 s | 0.70 MB/s |
| 热 seek | **4.76 ms** | 6.24 ms | 6.53 ms | 218.9 MB/s |

seek 本身（`lseek`）中位 3 µs。

### 当前并发形态

事件统计（本次测量）：

```
intent=bulk streams=1 phase=fetch_window          30
intent=bulk streams=1 phase=prefetch_window      209
intent=bulk streams=1 phase=read                 3720
```

Quark 当前声明 `internal_parallel_read`，`ReadProfileOf` 把 `BulkStreams` 强制为 1，所以 VFS 层面是单流。4 MiB 的窗口低于 Quark 内部并行的 8 MiB 分片阈值，实际上大部分窗口也是单 HTTP Range。

## 迁移后的对比方式

迁移完成后用完全相同的命令复测，对比：

- 冷读中位吞吐（主要指标）
- 冷 seek 中位加载时间
- 热读中位吞吐（应基本不变，受本地缓存限制）
- 事件中的 `read_streams`（预期从 1 变为 3 或配置值）
- 是否出现 `ErrUnsupported` 回退（出现则说明 CDN 没有稳定返回 206）

注意：Quark 的带宽波动较大，单次样本不足以判断收益；建议迁移前后各跑 3 次采样，并至少重复一轮。

## 迁移后复测（2026-11-04，未采用）

迁移实现了 capability 对调、严格 Range 和 `BulkStreams: 3`，并修复了加密包装层不转发读能力的问题（事件确认 `read_streams` 从 1 变为 3，无 `ErrUnsupported` 回退）。但同一文件上的顺序冷读明显变慢。

控制实验（同一文件、同一时段、每次 3 个采样，`--cache-mode cold --pattern sequential`）：

| 配置 | 冷读中位 | 单次样本 |
| --- | --- | --- |
| 1 流（对照） | **11.30 MB/s** | 11.06 / 11.41 / 11.30 |
| 2 流 | 9.19 MB/s | — |
| 3 流 | 8.30 / 10.58 MB/s | 首次 10.20 / 10.88 / 10.58 |
| 1 流（再次对照） | 12.13 MB/s | — |

迁移前基准那轮是 18.96 MB/s，但同一时段内单流对照只有 11–12 MB/s，说明 provider 带宽本身在漂移；用同时段对照比较，3 流比单流低约 **25–30%**。

冷 seek 在 3 流下为 1.04 s，单流对照为 1.50–3.05 s，差异在 provider 波动范围内，不能证明并行带来收益。

结论：Quark CDN 对同一签名 URL 的并发小 Range 不随并发数线性扩展；把 4 MiB 窗口拆成 3 个约 1.3 MiB 子 Range 增加请求开销和尾部延迟，顺序冷读反而变慢。迁移已回退，Quark 保持 `internal_parallel_read`（VFS 单流）。

保留的改动：

- 严格 Range 实现（200 → `ErrUnsupported`）保留，作为驱动契约的正确实现。
- 加密包装层转发 `parallel_range` / `internal_parallel_read` 和 `ReadProfile` 保留；这对其他位于加密挂载后的并行驱动（如 p123、drime）是实际修复。

回退后复测：`quark-test` 与 `quark-download` 的 `probe test read --size 64m` 均通过，冷读中位 17.7 / 17.4 MB/s，回到迁移前基准区间；两者的 contract 也都通过。
