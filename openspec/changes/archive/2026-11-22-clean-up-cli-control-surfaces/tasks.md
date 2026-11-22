# Tasks

## 1. 调查与命名

- [x] 1.1 盘点 `probe`/`debug`/`control` 子命令与现有脚本/测试引用，产出旧→新映射表（见 design.md「旧 → 新映射」）
- [x] 1.2 确认隐藏 alias 的命名与弃用提示文案（en/zh）

## 2. 诊断边界（无命令迁移）

- [x] 2.1 保持 `debug`/`probe` 两组及全部现有路径不变；不新增隐藏 alias
- [x] 2.2 更新 `probe` 的 Short/Long 与 `docs/for-user/cli.md` 诊断章节：明确 debug 只读、probe 可能写本地缓存/远端测试数据/注入故障
- [x] 2.3 增加路由枚举测试：debug server（mutable=false）不注册任何 mutable handler；mutable handler 只在 probe/control server
- [x] 2.4 增加帮助测试：`qrypt debug --help` 不出现 cache/test/bench/fault；`qrypt probe --help` 正常展示并带“may write”说明

## 3. Core 状态入口

- [x] 3.1 `core status --json` 增加 `endpoint`/`network`/`run_dir`
- [x] 3.2 `core endpoint` 改为隐藏 alias，输出不变 + 弃用提示
- [x] 3.3 更新 `core status` 人类输出（可选展示 endpoint）

## 4. journal 只读

- [x] 4.1 `fs journal` 仅保留 `inspect`，移除 `replay`/`prune` 命令与文档
- [x] 4.2 对旧调用给出指向 `core ops reconcile`（未来）的明确错误
- [x] 4.3 更新 journal 测试（只读路径保留）

## 5. 删除无实现 flag

- [x] 5.1 删除 `fs --mount` 与 `fs --bwlimit*` flag
- [x] 5.2 删除 `bandwidthOverrideFromFlags`、`WithFSBandwidthFlags` 及 stub 实现，清理 import
- [x] 5.3 更新帮助/UX 测试与文档示例

## 6. crypt 命令归位

- [x] 6.1 新增 `qrypt crypt encode|decode`，复用现有实现
- [x] 6.2 `fs crypt-encode`/`crypt-decode` 保留隐藏 alias + 弃用提示
- [x] 6.3 更新 `fs` 帮助与 cli.md

## 7. 生成物与文档

- [x] 7.1 更新 `schema/i18n/cli.{en,zh-CN}.json`，重新生成 `internal/cli/i18n/catalog_gen.go`
- [x] 7.2 机器可读参考：配置 schema 不含 CLI 命令树；命令树由 `qrypt schema <path>` 提供，已在 surface 测试中断言旧入口未出现（design.md 已注明）
- [x] 7.3 更新 `docs/for-user/cli.md` 与 `docs/for-developer/architecture.md`
- [x] 7.4 `check-cli-i18n.py`、`check-docs.sh`、schema 测试通过

## 8. 验收

- [x] 8.1 `qrypt probe ...` 与 `core endpoint`、`fs crypt-*` 仍可运行且打印弃用提示
- [x] 8.2 帮助、schema、`qrypt help` 机器可读参考中不出现旧入口与已删 flag
- [x] 8.3 `go test ./internal/cli/...`、`go build ./...`、`scripts/ci/check-all.sh`（已知沙箱 FUSE 用例除外）通过
- [x] 8.4 确认没有行为/协议变更，`fs` 数据路径测试全绿
