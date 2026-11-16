# 迁移读取配置

升级后，挂载、复制和分享下载会共享文件的在途请求预算。未配置读取覆盖的挂载可直接使用驱动建议。

## 替换旧表

将 `[mounts.read_profile]` 替换为 `[mounts.read.bulk]`。例如，旧的 `window_chunks = 16` 对应 `window = "8MiB"`：

```toml
[mounts.read.bulk]
streams = 4
window = "8MiB"
```

保留 `[[mounts]]` 及其名称、类型、参数。存在旧表时，启动会明确报错并提示迁移；新旧表不能混用。

## 转换可选参数

| 旧参数 | 新参数 | 转换方法 |
|---|---|---|
| `streams` | `read.bulk.streams` | 正数沿用；旧值 0 改为省略 |
| `window_chunks` | `read.bulk.window` | 块数乘以 512 KiB；旧值 0 改为省略 |
| `part_chunks` | `read.bulk.part_size` | 块数乘以 512 KiB；旧值 0 改为 `"auto"` |
| `min_split_chunks` | `read.bulk.min_parallel_size` | 块数乘以 512 KiB；旧值 0 改为 `"0"` |

尺寸接受字节数和 KiB/MiB，最大为 32 MiB。省略字段继承驱动建议。窗口和固定分片大小必须大于 0。

如需限制同一文件的总请求数，设置 `[mounts.read]` 下的 `max_in_flight`（1–32）。它同时约束前台、预取和复制；驱动硬限制始终优先。预算为 1 时，空闲阶段仍可预取。

## 检查迁移结果

启动后查看诊断的 `read_policy`：`recommended` 是驱动建议，`effective` 是实际策略，`limits` 是硬限制，`origins` 说明字段来源。旧的 `read_profile` JSON 字段已删除。

先用 `qrypt probe test read` 比较单流和多流，再调整参数。配置正确不代表某个网络下必然更快；播放器首帧需在播放器侧测量。
