# 质量门禁（CI Workflows）

qrypt 用三个 GitHub Actions workflow 组成质量体系：每 PR 的快速检查、
每夜的全量门禁、以及真实网盘 contract 套件。本地开发时对应的脚本
见文末。

## Workflow 总览

| Workflow | 触发 | 内容 | 状态徽章 |
| --- | --- | --- | --- |
| `CI` (ci.yaml) | push main / PR / tag | vet、staticcheck、golangci-lint、govulncheck、gofmt、`go test ./...`、VFS 稳定性 ×3、localfs smoke、race、**PR coverage 快照**、**Windows 编译+单测门禁**；tag/dispatch 时构建并发布 | README |
| `Contract Tests` (contract.yaml) | 手动 dispatch 或 nightly cron | 对每个启用的 mount 跑真实网盘 contract 套件（auth/contract/crud/fs/instantupload/resume/multipart），`test_enabled = true` 的 mount 才可测；nightly 只跑 `CONTRACT_NIGHTLY_MOUNTS_JSON` 里的无人值守子集 | README |
| `Nightly Quality Gates` (nightly.yaml) | 每天 18:00 UTC | **fuzz**（每 fuzzer 30s，失败上传 corpus artifact）、**coverage 硬性 floor gate**、race、全量测试、**Windows 真实挂载冒烟**（WinFsp） | README |

## 手动触发

```bash
# Contract Tests：指定 mount（逗号分隔），覆盖 repository variable
gh workflow run "Contract Tests" -f mounts=yun139,115

# Contract Tests：不带输入 = 跑 CONTRACT_MOUNTS_JSON 里的全部 mount
gh workflow run "Contract Tests"

# Nightly Quality Gates（fuzz + coverage gate + race + all）
gh workflow run "Nightly Quality Gates"
```

nightly cron 只跑 `CONTRACT_NIGHTLY_MOUNTS_JSON` 里的子集。定时任务是无人值守的，
只能放凭证在两次运行之间仍然有效的 mount：quark 这类 provider 的凭证只存在于
运行期轮换的会话状态里，而每次 CI 都是全新 runner、只能使用配置里的静态 cookie，
该 cookie 几天内就会过期，之后每次定时任务都会以 `mount "X" not found` 失败
（`/v1/health` 仍返回 OK，因为失败的 mount 在启动时就被跳过了）。这类 mount
请手动 dispatch，并在跑之前先按下面的步骤重建 secret。

## Coverage 门禁

- **nightly 硬性 gate**（`scripts/ci/check-coverage.sh`）：六包低于 floor 即失败
  （vfs 81 / core 75 / drive 69 / sync 82 / config 77 / crypt 79）。floor 取
  “实测值 - 1 点”，即门禁只放过 1 点抖动，真实退化会被抓住；提高 floor 是刻意行为：
  先 `-print` 重新测量，再改 `FLOOR` map 并提交说明。
- **PR 快照**（`scripts/ci/report-coverage.sh`，ci.yaml 中仅 PR 运行）：
  非阻塞，把六包覆盖率和"本 PR 有变更"的包写进 PR summary，便于在
  合并前观察趋势。`pkg/syncer` 用 `-coverpkg` 对 CLI 集成测试口径
  （单包 ~15% 是假象）。

## Contract 矩阵（两层）

1. **本地（每 PR，零凭证）**：`pkg/drive/contract_matrix_test.go` 用
   `drive.FakeDriver` 统一检查 capability 声明与行为一致、unsupported
   分类、列表稳定性、`RunBehaviorChecks`、debug snapshot 无凭证；
   能力集合本身被 pin，新增能力必须显式。
2. **真实 provider（nightly 子集 / 手动全量）**：Contract Tests workflow 按
   mount 并行跑，每个 job 启动一个共享 mount 服务器（socket 由配置自动解析），
   `test_enabled = true` 的 mount 通过 `qrypt probe test` 入口执行。启用前会先查
   `/v1/mounts/health`，确认该 mount 真的在服务端注册成功；缺失时重启一次，
   仍缺失则按初始化失败报错，并从运行时错误日志里带出原因。

## 配置项

- `CONTRACT_CONFIG_TOML_B64`（secret）：Contract Tests 使用的
  `qrypt.toml` 的 base64。内容含真实凭证，只进 secret，绝不进日志。
- `CONTRACT_MOUNTS_JSON`（repository **variable**）：默认跑 contract 的
  mount 列表（JSON 数组）。**必须用 variable 而不是 secret**：GitHub
  会把含 secret 的 job output 脱敏为 `***`，导致
  `fromJson("***")` 展开失败、matrix job 不创建。
  同步本机配置：`gh variable set CONTRACT_MOUNTS_JSON --body '["yun139","115"]'`。
  这个列表同时决定 secret 里保留哪些 mount，所以手动 dispatch 用到的 mount
  必须一直在里面。
- `CONTRACT_NIGHTLY_MOUNTS_JSON`（repository **variable**）：nightly cron
  实际执行的子集，必须是 `CONTRACT_MOUNTS_JSON` 的子集（否则 setup job 直接
  报错）。只放无人值守安全的 mount，目前是
  `gh variable set CONTRACT_NIGHTLY_MOUNTS_JSON --body '["jianguoyun"]'`。
  没列进去的 mount 会被跳过，setup 日志会打印被排除的清单，避免新增 mount
  悄悄漏跑。

从本地配置更新 contract secret（需要 Go 和已登录且有仓库 secrets 写权限的 `gh`）：

```bash
# 先读取仓库 variable 并检查选择，不写入 secret
bash scripts/dev/sync-contract-config.sh --dry-run
# 默认读取当前目录 ./qrypt.toml，更新当前仓库 secret
bash scripts/dev/sync-contract-config.sh
# 可显式指定配置和目标仓库
bash scripts/dev/sync-contract-config.sh --config ./qrypt.toml --repo OWNER/REPO
```

脚本按 variable 中的 mount `name` 精确筛选，保留全局设置和选中 mount 的完整配置
（包括 params、加密设置），顺序与 variable 一致。缺失、重复或未设置
`test_enabled = true` 的 mount 会使更新失败。若全局 `upload.default_mount`
不在选择中，会移除它及 `default_path`，保留其他上传设置。
base64 内容仅通过标准输入传给 `gh secret set CONTRACT_CONFIG_TOML_B64`，
不打印配置、不创建明文或 base64 临时文件，也不修改本地配置或仓库 variable。

## Fuzz 与 corpus

- nightly fuzz 失败时 `**/testdata/fuzz/**` 上传为 `fuzz-corpus`
  artifact；按 [fuzz-corpus.md](fuzz-corpus.md) 下载并提交最小复现样本，
  之后每次 `go test ./...` 自动回归。
- 已保留样本：`pkg/crypt`（EME 分段 panic）、`pkg/config`
  （ParseSize NaN）。

## Windows 验证

发布产物含 windows/amd64 与 windows/arm64，但平台代码（`*_windows.go`、
纯 Go nocgo 宿主 `host_nocgo_windows.go`、WinFsp DLL 加载）需要 CI 验证，
并且发布链路必须等待 Windows 门禁通过，分两级：

1. **每 PR**（ci.yaml `test-windows` job，windows-latest）：`CGO_ENABLED=0`
   `go build -tags nocgo`（与发布产物同款标签）、`go vet ./...`、主模块
   单测（只用主模块包列表，不用 `go test ./...`——vendored cgofuse 的
   Windows 宿主测试需要真实 WinFsp）、localfs fs 冒烟
   （`scripts/ci/smoke-localfs.sh`，仅 `qrypt fs` 命令、不挂载）、以及
   `install.ps1` 的命名漂移检查与安装冒烟（`scripts/install/smoke-install.ps1`）。
   无 WinFsp 依赖，跑得快。
2. **每夜**（nightly.yaml `windows-mount` job）：安装 WinFsp（choco），
   以 localfs + 加密配置真实挂载（`scripts/ci/smoke-windows-mount.ps1`），
   经 FUSE 路径写文件，校验 `qrypt fs cat` 解密读回、后端只存加密乱码
   文件名、再经 FUSE 路径读回。这是 nocgo 宿主 + WinFsp 集成唯一覆盖
   的路径。

发布时，`ci.yaml` 还会在 Windows runner 上解压并运行实际的 amd64 发布包，
通过同一份 WinFsp 挂载冒烟后才进入 release；Windows arm64 目前仍只做交叉编译
验证。

注意：Windows 下 race 检测需要 cgo 工具链（runner 无 gcc），所以
`-race` 只在 Linux 跑。

## 离线下载冒烟（手动，需要网盘账号）

离线下载的自动化覆盖是确定性的假 provider 集成测试
（`pkg/core/offline_runner_test.go`，覆盖提交、首次观察、轮询、完成对账、取消、
重试、重启恢复、去重、fetch 暂存+传输、URL 刷新、清理与终态保护）和 Quark 请求
加密 golden 测试（`pkg/drivers/quark/offline_crypto_test.go`），两者都不需要任何
外部凭据。

真实 Quark 账号的冒烟测试**不在自动化 CI 内**，因为它需要一个已登录的 Quark
账号 cookie（并可能触发风控）。在有账号的机器上手动执行：

```bash
# 需要一个配置了 quark mount 的 qrypt.toml（cookie 有效）
qrypt mount &                                   # 离线命令需要运行中的 Core
qrypt offline add 'magnet:?xt=urn:btih:...' --parse-to /quark/downloads
qrypt offline fetch 'https://example.com/small.iso' \
  --parse-to /quark/downloads --download-to ./small.iso --wait   # 加速与非加速两条路径
qrypt offline show <task-id> --json             # 确认 staging/transfer 两个阶段
qrypt fs get /quark/downloads/small.iso ./small.iso
```

前提：账号 cookie（`mounts.params.cookie`）有效、账号未触达下载配额/策略限制；
未登录或缺少 cookie 时会返回 `offline_auth_required`。用一个直链小文件（例如一张
PNG）作为来源；把 HTML 页面 URL 当作文件来源时 Quark 会返回 provider code `25009`
（未映射的通用失败），这不是 qrypt 的问题。

该冒烟已在本机用真实账号手动执行并通过：`offline add`（云端暂存）+ `fs get`、
`offline fetch`（默认后台返回、`--wait` 阻塞并显示进度；加速会话缺失时回退到普通下载 URL、
本地传输、按网盘侧相对路径镜像目录结构、成功后清理云端副本）、
`--keep-cloud` 保留、`--download-to /加密挂载/...` 经 VFS 加密上传并 `fs get` 解密取回
（哈希一致）、`--mkdir` 新建暂存目录、`list` 默认视图、`show`、`wait --timeout`、
`cancel`（运行中任务）、`retry`（失败任务原地重排）。

### 115 / 115_open

115 的离线冒烟同样不在 CI 内，需要有效凭据：`115` 用 `mounts.params.cookie`，`115_open`
用 `mounts.params.refresh_token`；账号需要有云下载配额。分别对两个驱动执行：

```bash
qrypt mount &                                   # 离线命令需要运行中的 Core

# 单文件 + 只给 mount：走 provider 下载 URL
qrypt offline fetch 'https://example.com/small.bin' \
  --mount 115 --download-to ./small.bin --wait
qrypt offline show <task-id> --json

# 多文件磁力 + 显式暂存目录：走 VFS 读取并镜像资源文件夹
qrypt offline add 'magnet:?xt=urn:btih:...' --parse-to /115/downloads
qrypt offline fetch 'magnet:?xt=urn:btih:...' \
  --parse-to /115/downloads --download-to ./out --wait
find ./out -type f                              # 确认 <resource>/... 结构
qrypt offline show <task-id> --json             # 确认 staging -> transfer -> complete

# 保留云副本
qrypt offline fetch 'https://example.com/small.bin' \
  --mount 115 --download-to ./keep.bin --keep-cloud --wait
```

检查点：115 侧资源文件夹在本任务文件全部删除后消失；带无关条目的文件夹保留；
`offline inspect` 返回 `offline_inspect_unsupported`；`--select largest:1` 在创建任务前
被拒绝；失败任务重试时若 115 仍保留同一来源，返回 `offline_duplicate`，按文档先删除
115 任务再重试。`115_open` 用同一个流程，只把 mount 名换成 `115_open`、凭据换成
`refresh_token`。

### drime

Drime 的冒烟需要个人访问令牌（Settings → Developers → API access tokens，Files 读写权限）
和一个 `test_enabled = true` 的 mount。测试账号如果没有团队工作空间，先用
`workspace_id = 0` 跑个人空间，再用一个有 `files.view/create/update/delete` 权限的
TestWorkSpace 跑 `workspace_id = <id>`。

```bash
qrypt mount &

# 列目录、范围读、写入
qrypt fs list /drime
qrypt fs get /drime/large.bin ./large.bin
qrypt fs mkdir /drime/qrypt-smoke
qrypt fs put ./small.bin /drime/qrypt-smoke/small.bin

# 小文件（<5 MiB）与 multipart（>=5 MiB，含中断续传）分别跑一次 probe 的
# contract/crud/fs/resume/multipart 套件；测试后用 fs rm 清理 qrypt-smoke
qrypt probe test --mount drime --suite contract
qrypt probe test --mount drime --suite crud
qrypt probe test --mount drime --suite fs
qrypt probe test --mount drime --suite resume
qrypt probe test --mount drime --suite multipart
```

检查点：`root_path` 不存在时 mount 必须失败而不是回落到账号根；目录改名后子项仍可列出；
`share list` 必须返回明确的 unsupported 错误，`share create/show/update/remove` 正常；
`share receive` 能匿名打开 `https://dri.me/<hash>`、校验密码、浏览子目录并下载一个文件；
上传的 `file_hash` 和 `client_last_modified` 与本地一致；测试结束后 drime 根目录只剩
测试前已有的对象。

## 本地等价命令

```bash
./scripts/ci/test-layers.sh                # 分层测试（fast/contract/race 等）
./scripts/ci/check-coverage.sh -print      # 覆盖率快照（不 gate）
./scripts/ci/fuzz-nightly.sh 10s           # 本地 fuzz（每 fuzzer 10s）
./scripts/ci/smoke-localfs.sh              # localfs 挂载冒烟
./scripts/ci/smoke-windows-mount.ps1       # Windows 真实挂载冒烟（需 WinFsp）
./scripts/install/check-install.sh         # 安装脚本：shellcheck + 与 updater 的发布命名漂移检查（含 install.ps1，需 pwsh）
./scripts/install/smoke-install.sh         # 安装脚本端到端冒烟（加 --docker 在容器里跑一遍）
pwsh ./scripts/install/smoke-install.ps1   # Windows 安装脚本端到端冒烟（需在 Windows 上运行）
```

脚本按用途分目录（`ci/`、`schema/`、`install/`、`release/`、`dev/`），
命名约定和每个目录的职责见 [`scripts/README.md`](../../scripts/README.md)。

`check-install.sh` 在 `ci.yaml` 的 `static` 任务中运行；`smoke-install.sh --docker`
在 `stability` 任务中运行。`scripts/install/install.sh`（Linux / macOS）与
`scripts/install/install.ps1`（Windows）是首次安装脚本：下载并校验 release，把二进制装到
用户可写目录并配置 `PATH`（Unix 用 `~/.local/bin` 符号链接 + rc 片段，Windows 用
`%USERPROFILE%\.qrypt\bin` + 用户级注册表 `PATH`）。`pkg/install` 固定两侧安装布局，
并有 Go 测试比对 `install.sh`/`install.ps1` 与 `pkg/selfupdate` 的资源命名，
不一致时 CI 失败（PowerShell 侧在 Windows 任务或任何装有 `pwsh` 的环境运行）。
`smoke-install.ps1` 在 `test-windows` 任务中运行。

## 测试并行度

除少数例外，测试都带 `t.Parallel()`。`pkg/vfs` 的 281 个测试串行需 14.5s，
并行后 5.0s；`pkg/core` 8.2s → 2.8s。fast 层墙钟因此从约 21.5s 降到约 12s。

两类进程级状态让测试不能并行，且都看不出自测试自身：

- **testing 拒绝的调用与全进程视角**：`t.Setenv`/`t.Chdir` 在 `t.Parallel()`
  之后调用会 panic；`goleak.Find` 扫描进程内全部 goroutine，只要有别的测试在跑
  就永远等不到「无泄漏」，用它的测试需要独占本包。
- **被当作测试接缝的生产包级变量**：`pkg/core` 的 `DirectUploadRetryBaseDelay`、
  `pkg/vfs/upload` 写 `pkg/logging` 的 `logging.L`。
  哪个测试在跑就写哪个，两个并行就在赋值本身上竞争。
  （目标形态是实例注入：`pkg/core` 的 stream poll interval 已从包级变量改为
  per-`Core` 字段，测试按实例设定，这类接缝随之消失。）

只把写入者标为串行就够了：go 的 testing 会先把所有非并行测试跑完再恢复并行的
那些，所以串行写入者必定早于并行读取者结束。

`scripts/ci/test-parallelism.py` 维护这件事，它顺调用图传播（`pkg/core` 的
`newTaskBoundaryCore` 曾写接缝，当时调用它的测试即便自身没提过也要串行；该接缝
已实例注入化，这类传播点随之消失），也识别跨包赋值：

```bash
./scripts/ci/test-parallelism.py audit ./pkg/vfs   # 只报告，有待改动时退出 1
./scripts/ci/test-parallelism.py apply ./pkg/vfs   # 落地
```

改完**必须**跑 `scripts/ci/test-layers.sh race`。静态分析看不到「依赖另一个测试先把接缝
设成特定状态」这类顺序耦合（`pkg/mobile` 的直传测试即如此），只有 race 层兜得住。

## 版本固定策略

为保证同一提交在不同时间得到相同结果，CI 的全部质量工具和 GitHub
Action 都固定版本。
工具版本升级是刻意行为：更新版本号/SHA 并提交，让 CI 结果可追溯。
