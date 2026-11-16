## Why

`read_profile.streams` 只能把窗口**均分**：窗口从 4 MiB 放大到 24 MiB 时，每路分片也
同步变大，无法表达“3 路并行、每路 8 MiB”这类经过实测的分片形状；也没有“窗口小于 N 块
就保持单流”的门槛，无法复刻按**请求大小**决定是否并行的策略（现有
`ParallelThreshold` 按文件大小判定）。

## What Changes

- `drive.ReadProfile` 新增两个可选字段：`StreamPartChunks`（固定分片大小，512 KiB 块）
  与 `ParallelMinChunks`（允许拆分的最小窗口，512 KiB 块），零值保持现有行为。
- `[mounts.read_profile]` 新增可选 `part_chunks` 与 `min_split_chunks`，继续只影响
  `bulk` 与“确认连续”的播放窗口，不改变意图门禁与缓存准入。
- 窗口拆分改为：固定分片时片数 = ceil(窗口/分片)，被已授予流数封顶，余量并入最后一片，
  保证窗口仍被完整覆盖；窗口小于 `min_split_chunks` 时保持单流。
- 诊断快照暴露两个有效值。

## Capabilities

### New Capabilities

无。

### Modified Capabilities

- `read-profile-config`：扩展每挂载覆盖字段与校验范围，并明确分片形状与拆分门槛的语义。

## Impact

`pkg/drive/read_profile.go`、`pkg/config`、`pkg/core` 的覆盖转换、`pkg/vfs/read` 的
`ReadPlan` 与 `splitWindow`、`pkg/vfs/diagnostics`，以及 `schema/qrypt.schema.json` 与
生成文档。不新增全局配置，不改变默认值（两字段默认为零，行为与之前一致）。

## Validation

- `pkg/vfs/read/split_window_test.go` 覆盖均分、固定分片、片数被已授予流数封顶（余量并入
  最后一片）、`part_chunks` 大于窗口时保持单流、`min_split_chunks` 拒绝小窗口。
- `pkg/drive` 覆盖两个字段的边界校验、覆盖继承与归一化；`pkg/config` 覆盖 TOML 解析与
  越界报错；`pkg/vfs/read/plan_test.go` 覆盖计划携带这两个值。
- 真实链路未测：本章只把已实测的形状（3 路 × 8 MiB）变成可表达、可校验的配置，不等于
  推荐该形状；慢链路仍应先用 `qrypt probe test read --streams` 实测。
