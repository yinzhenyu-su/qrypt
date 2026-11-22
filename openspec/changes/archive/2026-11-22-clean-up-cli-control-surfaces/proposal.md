# Proposal

## Why

CLI 现在有三套彼此重叠的控制/诊断入口：`qrypt probe`、`qrypt debug`、`qrypt control task`；`core endpoint`
把本地传输路径当作公开信息；`fs journal replay/prune` 直接改写 Core 持有的 journal；同时 `fs` 组注册了
`--mount` 与 `--bwlimit*` 等已无实现的 flag。这些入口与“单 owner Core + 薄客户端”的方向相反：它们扩大了
需要长期兼容的公开面，并把内部状态、故障注入和不稳定协议暴露给用户。

## What Changes

- **不改变诊断命令分组**：`qrypt debug` 继续是公开只读诊断组，`qrypt probe` 继续是公开的活动诊断组
  （可能写远端测试数据、注入故障和压测）；两者保持独立、命令路径不变。合并会让 `debug` 组包含写能力，
  隐藏 `probe` 又会降低维护入口的发现性，因此本 change 不做诊断分组合并或隐藏，只修正帮助/文档中对两者
  边界的表述（`debug` 只读、`probe` 可能写远端测试数据）。
- `core endpoint` 的信息并入 `core status --json`（`endpoint`、`network`、`run_dir`）；命令保留隐藏 alias。
- `fs journal` 只保留只读 inspect；`replay`/`prune` 移除，提示由未来的 operation 对账入口接管。
- 删除没有实现的 `fs --mount` 与 `fs --bwlimit*` flag，以及随之无用的 `bandwidthOverrideFromFlags`、
  `WithFSBandwidthFlags` 接口方法和测试桩。
- `fs crypt-encode`/`crypt-decode` 提升为 `qrypt crypt encode`/`decode`（它们不打开文件系统）。
- 被移动/合并的命令在至少一个 minor 内以隐藏 alias 可用；帮助、schema、机器可读参考和文档只展示新入口。
- 不改变任何 `fs` 数据路径、缓存、Core 协议或挂载行为。

## Capabilities

### New Capabilities

（无）

### Modified Capabilities

- `cli-help`: 命令/flag 有实际行为且唯一归属；被移动/隐藏的入口（endpoint、crypt、journal 写命令）不进入帮助与机器可读参考。

## Impact

- 代码：`internal/cli/debug`、`internal/cli/core`、`internal/cli/fs`、`internal/cli/journal`、
  `internal/cli/command_root.go`、`internal/cli/runtime`（接口与 stub）、`internal/cli/fs_runtime.go`。
- 生成物：`schema/i18n/cli.{en,zh-CN}.json` 与 `internal/cli/i18n/catalog_gen.go`（帮助文本变更）。
- 文档：`docs/for-user/cli.md` 的诊断/核心章节、`docs/for-developer/architecture.md` 的入口说明。
- 兼容：隐藏 alias 保证现有脚本在一个 minor 内继续工作；清理完成后不存在第二套公开控制面。
