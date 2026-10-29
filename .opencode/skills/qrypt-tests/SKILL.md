---
name: qrypt-tests
description: Run qrypt tests manually — local unit/race/coverage/fuzz gates, real-netdisk contract and probe tests against configured mounts (local temp mount or a long-lived mount server), and manual GitHub workflow runs (Contract Tests, Nightly Quality Gates). Use when asked to run or rerun tests, run a contract/probe test on a specific mount, refresh the contract secret, or explain a failed contract/nightly run.
---

# Qrypt 手动运行测试

## 先按目标选入口

| 目标 | 命令 |
|---|---|
| 改完代码跑本地全量门禁 | `scripts/ci/check-all.sh` |
| 只跑工作区 diff 能覆盖的包 | `scripts/ci/test-layers.sh changed` |
| 单包 / 单个测试 | `go test ./pkg/vfs -run TestName -count=1` |
| 并发敏感包 | `scripts/ci/test-layers.sh race` |
| 覆盖率门禁（只查看不判定） | `scripts/ci/check-coverage.sh -print` |
| fuzz | `scripts/ci/fuzz-nightly.sh 10s` |
| 真实网盘单次测试（隔离临时挂载） | `scripts/dev/debug-mount.sh <mount> <test>` |
| 真实网盘 contract（本机常驻挂载） | `QRYPT_CONTRACT_CONFIG=./qrypt.toml scripts/ci/test-layers.sh integration <mount>...` |
| CI 上跑指定 mount 的 contract | `gh workflow run contract.yaml -f mounts=<a>,<b>` |
| CI 上跑 nightly 门禁 | `gh workflow run nightly.yaml` |

## 0. 前置检查

- Go 版本必须与 `.github/workflows/ci.yaml` 的 `go-version` 一致（当前 `1.27.3`）：`go version`。
- CI 操作需要 `gh auth status` 通过且有仓库权限；仓库用 `gh repo view --json nameWithOwner` 确认。
- 真实网盘测试要求：`qrypt.toml` 中存在该 `[[mounts]]`、`test_enabled = true`、凭证有效（内联 params 或引用的 `[[credentials]]`）。

## 1. 本地单元 / 门禁

```bash
# 与 CI check job 等价的本地门禁（提交 main 前必须跑，结束时打印
# "== local CI check passed =="）
scripts/ci/check-all.sh

# 分层跑，避免每次都全量
scripts/ci/test-layers.sh changed        # 工作区 diff 可达的包
scripts/ci/test-layers.sh fast           # go test ./...（约 20s+）
scripts/ci/test-layers.sh race
scripts/ci/test-layers.sh vfs-stability  # 3 次重复，抓时序 flake
scripts/ci/test-layers.sh smoke          # localfs 挂载冒烟
scripts/ci/test-layers.sh all            # 默认套件 + race(vfs/core) + smoke
```

`-count=1` 用来绕开结果缓存；单个测试仍用 `go test ./pkg/<pkg> -run '^TestName$' -count=1 -v`。

## 2. 真实网盘测试（本机）

### 2a. 隔离临时挂载（推荐，一次一个测试）

```bash
./scripts/dev/debug-mount.sh quark-test contract
./scripts/dev/debug-mount.sh quark-test batchmove --count 50 --size 4k
./scripts/dev/debug-mount.sh --config ./path/to/qrypt.toml quark-test fs --size 30m
```

脚本会编译 qrypt、在临时目录挂载、等 debug socket 就绪、跑一个 `probe test`，
然后卸载并清理本地缓存/暂存/状态/日志；stdout 只有测试结果，可直接比对。
仅支持单 mount 测试。

### 2b. 常驻挂载（多个测试复用，或跨 mount）

```bash
# 终端 1：前台挂载，Ctrl+C 退出
qrypt mount --config ./qrypt.toml

# 终端 2：命令必须用同一个 --config（endpoint 由配置路径派生，没有 --socket）
qrypt probe test contract --config ./qrypt.toml --mount quark-test
qrypt probe test auth     --config ./qrypt.toml --mount quark-test

# 一次跑多个 mount 的 contract 套件
QRYPT_CONTRACT_CONFIG=./qrypt.toml scripts/ci/test-layers.sh integration quark-test jianguoyun
```

测试类型（`qrypt probe test <test>`）：

| test | 内容 |
|---|---|
| `auth` | 只读认证 + 列根目录 |
| `contract` | 驱动行为契约套件（CI 用的就是它） |
| `crud` | 创建/读/改/删远端对象 |
| `fs` | VFS 文件系统冒烟（`--size`） |
| `instantupload` | 秒传 |
| `multipart` | 分片/分块上传 |
| `resume` | 断点续传恢复 |
| `batchupload` / `batchmove` | 批量上传 / 批量移动（`--count`、`--size`） |
| `read` | 经 FUSE 的读性能（需 `--mount-point`，可选 `--cache-mode`/`--pattern`/`--samples`） |
| `xfer` | 跨 mount 传输（`--source`/`--dest`） |

这些测试会写远端数据并自行清理；失败时可能留下 fixture，必要时手动清理。

## 3. 在 CI 上手动跑

```bash
# 指定 mount（逗号分隔），覆盖 repository variable
gh workflow run contract.yaml -f mounts=quark-test
gh workflow run contract.yaml -f 'mounts=quark-test, jianguoyun'

# 不带输入 = CONTRACT_MOUNTS_JSON 里的全部 mount
gh workflow run contract.yaml

gh workflow run nightly.yaml

# 跟进结果
gh run list -w contract.yaml -L 3
gh run watch <run-id> --exit-status
gh run view <run-id> --log-failed
```

- `schedule`（nightly cron）只跑 `CONTRACT_NIGHTLY_MOUNTS_JSON`（当前 `["jianguoyun"]`）。
  凭证只存在于运行期轮换会话里的 mount（quark 类）必须手动 dispatch。
- 手动 dispatch 可用的 mount 必须仍在 `CONTRACT_MOUNTS_JSON` 里 —— 它也决定 secret
  里保留哪些 mount。详见 `docs/for-developer/quality-gates.md`。

## 4. contract secret 过期（典型的 `mount "X" not found`）

顺序执行，不要跳步：

```bash
# 1) 先确认共享凭据是否还有效（会刷新/轮换会话）
qrypt credential test quark

# 2) 验证选择但不写 secret
bash scripts/dev/sync-contract-config.sh --dry-run

# 3) 写入 CONTRACT_CONFIG_TOML_B64（内容只经 stdin 传给 gh）
bash scripts/dev/sync-contract-config.sh

# 4) 重跑该 mount
gh workflow run contract.yaml -f mounts=quark-test
```

如果该 mount 还在用内联 cookie（`[mounts.params] cookie`），先切到共享凭据，
否则同步出去的仍是会过期的副本：

```bash
qrypt mount set <mount> --credential <credential> --remove-param cookie
```

用空状态目录模拟 fresh runner（CI 没有本地轮换状态）验证：

```bash
QRYPT_HOME=/tmp/qrypt-fresh qrypt fs df <mount> --config ./qrypt.toml
# 期望打印容量；出现 mount "<mount>" not found 说明 init 失败
```

## 5. 失败排查

- **`probe test failed: mount "X" not found`**：该 mount 在启动时 `Init()` 失败，
  被 `core.buildNamespace` 跳过（只记一条 warning），服务照常启动，
  `/v1/health` 仍是 OK。真正原因在这里：

  ```bash
  # 路径由 storage.log_dir 或 <storage.work_dir>/logs 决定；两者都没配时只在 stderr
  grep 'config: mount .* failed' ~/.qrypt/logs/qrypt-error.log | tail -n 20
  ```

  CI 日志里对应 `== mount init failures in ... ==` 段（contract workflow 的失败步骤会打印）。
- 单 mount 未注册时可直接问服务端确认：
  `qrypt debug raw "/v1/mounts/health?mount=<mount>" --config ./qrypt.toml`
  （缺失时返回 502 `vfs: mount "X" not found`）。
- 认证类错误不要靠肉眼比对 cookie：用 `qrypt credential test <credential>`，
  并检查 mount 是否已切到共享凭据。
- 命令连不上服务：确认 `mount`/`probe`/`debug` 用的是同一个 `--config`
  （endpoint 从配置路径派生；`QRYPT_HOME` 也会改变 run 目录）。

## 6. 约束

- 绝不打印 secret、cookie、token；`sync-contract-config.sh` 只把 base64 经 stdin
  交给 `gh secret set`，不要改成文件或命令行参数。
- 真实网盘测试会消耗配额并写远端，只跑 `test_enabled = true` 的 mount。
- Windows/WinFsp 相关验证只在 CI（`ci.yaml` test-windows、`nightly.yaml` windows-mount）。
- 改了 Go 代码再提交：先 `"$(go env GOROOT)/bin/gofmt" -w` 相关文件，再跑
  `scripts/ci/check-all.sh`（详见仓库 `AGENTS.md`）。
- 相关文档：`docs/for-developer/debug.md`（probe/debug 全量命令）、
  `docs/for-developer/quality-gates.md`（workflow 与变量）。
