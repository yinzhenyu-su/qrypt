# Design

## Context

单 owner Core 落地后，CLI 已不再自己执行文件操作；`core serve` 是唯一状态所有者。剩余的重叠入口是历史
遗留：probe 服务器最初是测试/诊断通道，debug 服务器是快照/收集通道，两者能力被逐步混用；`control task`
是 task API 的早期 shell 界面；journal 命令是 Core 拥有 journal 之前的产物。

## Goals / Non-Goals

**Goals**
- 每个用户可见命令和 flag 都有真实行为、唯一归属和唯一文档位置。
- 诊断能力不减少，但只通过一个公开组暴露；内部/危险操作隐藏。
- 删除/移动有明确的弃用窗口，不破坏现有脚本。

**Non-Goals**
- 不新增 `core ops` 或对账命令（由 durability change 提供）。
- 不改变 `fs` 数据路径、缓存语义或 Core 协议。
- 不重排整个命令树（`mount`/`fs`/`offline`/`share` 等保持不变）。

## Decisions

### D1. 诊断分组保持不变

`qrypt debug` 与 `qrypt probe` 继续作为两个公开组并存，不合并、不隐藏：合并会让 `debug` 组在语义上包含
写能力（`probe` 的 cache 维护、test/bench/fault 都会改本地或远端状态），即使子命令隐藏也破坏了用户依赖的
“debug 只读”约束；反过来把 `probe` 整体隐藏，又会降低排障/维护入口的发现性，而风险并没有真正降低。

因此本 change 对诊断面只做一件事：把边界写清楚并保持可观测。

- `debug`：只读诊断（快照/事件/操作视图/原始 GET/内存视图）。
- `probe`：活动诊断，可能写本地缓存（`cache clear|invalidate|fill`）、写远端测试数据（`test`/`bench`）
  或注入故障（`fault`）；其 Short/Long 与文档必须明确这一点。
- 服务器端划分不变：debug server（`control.NewServer`，`mutable=false`）只注册只读端点；probe/control
  server 才注册 mutable 端点。本 change 只补一条路由枚举测试，防止未来把 mutable handler 误注册到 debug
  server。
- 不引入 effect annotation、不重排命令、不新增隐藏 alias：没有命令移动就没有迁移成本。

### D2. `core endpoint` 并入 `core status --json`

`status --json` 增加 `endpoint`、`network`、`run_dir` 字段（无论 Core 是否运行都能给出派生路径）；
`endpoint` 命令保留隐藏 alias，输出与原先一致并提示新入口。

理由：endpoint 是客户端实现细节；状态命令本来就需要在 Core 未运行时解释“为什么不可达”。

### D3. journal 只读

`fs journal` 只保留 `inspect`（读出 pending 记录、状态与摘要）；`replay`/`prune` 暂时移除，错误信息指向
后续的 `core ops reconcile`。理由：写 journal 必须由持有 ownerlock 的 Core 执行，CLI 直接改文件与单写者
模型冲突；真正的恢复入口属于 durability change。

### D4. 删除无实现 flag

`fs --mount` 在 lazy mounts 后不再有意义；`fs --bwlimit*` 注册了但没有调用方（`bandwidthOverrideFromFlags`
无引用）。删除两个 flag、死函数、`WithFSBandwidthFlags` 接口方法及 stub 实现。限速继续由配置提供，未来若需要
按请求限速，应作为 Core RPC 参数而不是 CLI 本地 flag。

### D5. `fs crypt-*` 提升为 `qrypt crypt`

`crypt encode|decode` 是纯本地 rclone 兼容名工具，不访问 Core。移动到顶层 `qrypt crypt`，`fs crypt-*` 保留
隐藏 alias 一个 minor。

### D6. 弃用策略

所有 alias：隐藏于帮助/schema、可运行、向 stderr 打印一条弃用提示、在下一个 minor 删除。新增回归测试保证
alias 仍能工作且提示只出现一次；帮助与文档测试保证旧入口不再出现。

## 旧 → 新映射

| 旧入口 | 新入口 | 处理 |
| --- | --- | --- |
| `fs --bwlimit` / `--bwlimit-download` / `--bwlimit-upload` | 无（带宽由配置 `[bandwidth]` 决定） | 删除 flag、`bandwidthOverrideFromFlags`、`WithFSBandwidthFlags` |
| `fs --mount` | 无（Core 按路径按需初始化 mount） | 删除 flag |
| `fs crypt-encode` / `fs crypt-decode` | `qrypt crypt encode` / `qrypt crypt decode` | 隐藏 alias + 弃用提示，下个 minor 删除 |
| `fs journal replay` / `fs journal prune` | 未来 `qrypt core ops reconcile` | 移除；旧路径返回指向新命令的错误 |
| `core endpoint` | `core status --json`（`endpoint`/`network`/`run_dir`） | 隐藏 alias + 弃用提示 |
| `debug` 中的 mutable 命令 | `probe`（路径不变） | 不迁移；文档明确 debug 只读、probe 可能写 |

## Risks / Trade-offs

- 脚本依赖 `probe`/`endpoint`：隐藏 alias + 弃用提示降低风险，一个 minor 后移除。
- 帮助测试与 i18n 覆盖率测试依赖命令树：需要同步更新 `TestCommandTextCatalogCoverage`、help ladder 测试与
  `qrypt schema <path>` 提供的机器可读命令树（`schema/qrypt.schema.json` 只描述配置，不包含 CLI 命令树）。
- 隐藏 `faults/test/bench` 可能影响现有调试流程：文档给出 `debug internal` 的用法；CI 使用内部路径不受影响。

## Migration Plan

1. 移动命令并加隐藏 alias（行为不变）。
2. 删除 dead flags 与接口方法，更新 stub 与测试。
3. 更新 i18n、帮助、docs 与 schema。
4. 移除 alias 的独立 follow-up（下一个 minor），本 change 不含。
