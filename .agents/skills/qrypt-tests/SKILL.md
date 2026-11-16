---
name: qrypt-tests
description: Run qrypt tests manually — local hygiene/unit/race/coverage/fuzz gates, real-netdisk contract and probe tests against configured mounts (local temp mount or a long-lived mount server), and manual GitHub workflow runs (Contract Tests, Nightly Quality Gates). Use when asked to run or rerun tests, run a contract/probe test on a specific mount, refresh the contract secret, or explain a failed contract/nightly run.
---

# Qrypt 手动运行测试

## 先按目标选入口

| 目标 | 命令 |
|---|---|
| 改完代码跑本地全量门禁 | `scripts/ci/check-all.sh` |
| 只跑仓库卫生门禁（不编译） | `scripts/ci/check-hygiene.sh` |
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

- Go 版本单源在 `go.mod`：本地 `check-all.sh` 直接从 `go.mod` 解析要求的
  Go 版本（要求同一 minor 线、不低于其 patch），workflow 用
  `go-version-file: go.mod`。不要在文档或脚本里维护第二份版本号。
- CI 的 pinned 工具（staticcheck、golangci-lint、govulncheck、shellcheck、
  actionlint）版本在 `tools.go`，由 `scripts/ci/install-tools.sh` 安装到
  `$QRYPT_TOOLS_DIR`（默认 `~/.cache/qrypt-tools`）；`check-all.sh` 会先安装并
  把该目录放到 `PATH` 最前。
- CI 操作需要 `gh auth status` 通过且有仓库权限；仓库用
  `gh repo view --json nameWithOwner` 确认。
- 真实网盘测试要求：`qrypt.toml` 中存在该 `[[mounts]]`、`test_enabled = true`、
  凭证有效（内联 params 或引用的 `[[credentials]]`）。

## 1. 本地单元 / 门禁

```bash
# 与 CI check job 等价的本地门禁（提交 main 前必须跑，结束时打印
# "== local CI check passed =="）
scripts/ci/check-all.sh

# 分层跑，避免每次都全量
scripts/ci/test-layers.sh changed        # 工作区 diff 可达的包
scripts/ci/test-layers.sh fast           # go test ./...（约 20s+）
scripts/ci/test-layers.sh race           # 整个 ./pkg/vfs/... 子树 + 其它并发敏感包，-race
scripts/ci/test-layers.sh vfs-stability  # ./pkg/vfs/... 重复 3 次，抓时序 flake
scripts/ci/test-layers.sh smoke          # localfs 挂载冒烟
scripts/ci/test-layers.sh all            # 默认套件 + race(vfs/core) + smoke
```

`-count=1` 用来绕开结果缓存；单个测试仍用 `go test ./pkg/<pkg> -run '^TestName$' -count=1 -v`。

race 层与 vfs-stability 层覆盖的是整个 `./pkg/vfs/...`（含 `read`、`readcache`、
`scheduler`、`upload` 等子包），不只是 `pkg/vfs` 主包。新门禁暴露的时序用例
要按竞争条件修复，不要放松断言强度或加 `t.Skip` 来变绿。

### 1a. 仓库卫生门禁

`scripts/ci/check-hygiene.sh` 是廉价、不依赖 Go 构建的门禁，`check-all.sh` 会
先跑它：

- `go mod tidy -diff`：`go.mod`/`go.sum` 必须 tidy，只打印 diff、不改文件；
- `actionlint`：`.github/workflows/*.yaml|*.yml` 能解析且与本地 composite
  action 一致；
- pinned `shellcheck`：只检查 `scripts/ci/*.sh` 与 `.githooks/*`（`scripts/install/`
  由 `scripts/install/check-install.sh` 负责，两边范围不重叠）；
- `git diff --check`：无行尾空白/冲突标记；CI 用事件 base commit，本地默认看
  未提交改动，可用 `QRYPT_DIFF_BASE=<rev>` 指定。

本地直接跑可能因系统 shellcheck 版本与 `tools.go` 的 `ShellcheckVersion`
（当前 `0.10.0`）不一致而中止，这是环境差异，不是仓库问题：先跑
`scripts/ci/install-tools.sh`，再把 `$QRYPT_TOOLS_DIR` 放到 `PATH` 最前
（`check-hygiene.sh` 自身也会回退到该目录）。确实无法安装时，要明确记录跳过
原因，不要改 `tools.go` 的版本去迁就本机。

### 1b. 覆盖率门禁

```bash
scripts/ci/check-coverage.sh          # 判定
scripts/ci/check-coverage.sh -print   # 只打印，查看现值
```

覆盖率带测量噪声容差：同一提交在不同调度下可能低几分之一个点，
`check-coverage.sh` 的 `MARGIN=0.2` 允许低于 floor 0.2 个百分点（只警告），
低于 `floor - 0.2` 才失败。当前基线是 **2026-11-16**（darwin/arm64、go1.27.4）
测得的数据；抬 floor 要在专门 PR 里改 `FLOOR` 映射并在注释里记录新增覆盖的内容，
降 floor 需要可测量的回归理由，不能为了让 PR 变绿而下调。

### 1c. 时间敏感测试与 TestMain 接缝

测试加速依赖在包级 `TestMain`（`m.Run()` 之前）设置这些 seam：

- `retrySleep`（`pkg/drivers/quark`）
- `partRetryWait`（`pkg/drivers/p123`）
- `fileSystemIdlePollInterval`（`internal/cli`）
- `vfsSmokeIdleInterval`（`pkg/contracttest`）

它们只在 `TestMain` 里改一次、不参与测试体并行竞争。新增时间敏感测试请沿用
这个模式：在包级 `TestMain` 设置、在 `t.Cleanup` 里恢复（需要按测试恢复时），
不要在测试体内并发改全局变量，否则 race 层会随机失败。

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

这些测试会写远端数据并自行清理。`probe test fs` 的清理使用独立于请求的 context：
失败或取消时会先丢弃测试目录下的 pending 上传，再删除文件和目录，并等待延迟
删除排空；清理失败会作为 `cleanup` step 记录。`probe test fs` 遇到
`permanent_fail` 会直接报告失败路径，不再等满 `wait_upload` 超时。

进程被 `kill -9`、崩溃，或清理本身因权限/网络失败时，仍可能留下 fixture；
cleanup 失败会出现在报告里，不要只看 `pass`。测试目录前缀：

- `__qrypt_fs_test_*`
- `__qrypt_resume_test_*`
- `__qrypt_batchupload_test_*`
- `__qrypt_batchmove_test_*`
- `__qrypt_read_test_*`
- `__qrypt_xfer_test_*`

确认不是用户数据后，只删除这些前缀。上传队列卡住、`permanent_fail`、
staging 缺失/大小不匹配的排查与恢复，见 `vfs-upload-recovery` skill。

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

- **上传一直不排空 / `wait_upload` 超时 / `dirty=1 pending=1`**：先看
  `qrypt fs pending --verbose` 和 `qrypt fs journal`。`permanent_fail` 是终态，
  不会自动重试；probe 现在会立即报出失败路径。按情况在**卸载后**执行
  `qrypt fs journal prune`、`prune --failed` 或 `replay`，不要手删 journal。
  完整决策树见 `vfs-upload-recovery` skill。
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
- **Windows 上 `upload_task_test.go: task upload-... did not finish, last state=running`**：
  已知的 Windows 环境 flake（docs-only PR 也可能出现）。先用
  `gh run rerun <run-id> --failed` 重跑一次；重复出现才需要调查代码。
- **本地 `check-hygiene` 报 ShellCheck 版本不符**：见 §1a，装 pinned 工具而不是
  改版本。

## 6. 约束

- 绝不打印 secret、cookie、token；`sync-contract-config.sh` 只把 base64 经 stdin
  交给 `gh secret set`，不要改成文件或命令行参数。
- 真实网盘测试会消耗配额并写远端，只跑 `test_enabled = true` 的 mount。
- Windows/WinFsp 相关验证只在 CI（`ci.yaml` `test-windows`、`nightly.yaml`
  `windows-mount`）。`test-windows` 的单元测试包清单是显式枚举的，新增包时要
  同时加到 `ci.yaml` 的 Windows 清单和 `test-layers.sh` 的 race 层清单，两边保持
  同步，否则新包在 Windows 上永远不被测到。
- 改了 Go 代码再提交：先 `"$(go env GOROOT)/bin/gofmt" -w` 相关文件，再跑
  `scripts/ci/check-all.sh`（详见仓库 `AGENTS.md`）。
- 相关文档：`docs/for-developer/debug.md`（probe/debug 全量命令）、
  `docs/for-developer/quality-gates.md`（workflow 与变量）。
