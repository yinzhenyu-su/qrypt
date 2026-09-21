# 质量门禁（CI Workflows）

qrypt 用三个 GitHub Actions workflow 组成质量体系：每 PR 的快速检查、
每夜的全量门禁、以及真实网盘 contract 套件。本地开发时对应的脚本
见文末。

## Workflow 总览

| Workflow | 触发 | 内容 | 状态徽章 |
| --- | --- | --- | --- |
| `CI` (ci.yaml) | push main / PR / tag | vet、staticcheck、golangci-lint、govulncheck、gofmt、`go test ./...`、VFS 稳定性 ×3、localfs smoke、race、**PR coverage 快照**、**Windows 编译+单测门禁**；tag/dispatch 时构建并发布 | README |
| `Contract Tests` (contract.yaml) | 手动 dispatch 或 nightly cron | 对每个启用的 mount 跑真实网盘 contract 套件（auth/contract/crud/fs/instantupload/resume/multipart），`test_enabled = true` 的 mount 才可测 | README |
| `Nightly Quality Gates` (nightly.yaml) | 每天 18:00 UTC | **fuzz**（每 fuzzer 30s，失败上传 corpus artifact）、**coverage 硬性 floor gate**、race、全量测试、**Windows 真实挂载冒烟**（WinFsp） | README |

## 手动触发

```bash
# Contract Tests：指定 mount（逗号分隔），覆盖 repository variable
gh workflow run "Contract Tests" -f mounts=yun139,115

# Contract Tests：使用 repository variable CONTRACT_MOUNTS_JSON
gh workflow run "Contract Tests"

# Nightly Quality Gates（fuzz + coverage gate + race + all）
gh workflow run "Nightly Quality Gates"
```

## Coverage 门禁

- **nightly 硬性 gate**（`scripts/coverage.sh`）：六包低于 floor 即失败
  （vfs 75 / core 72 / drive 62 / sync 79 / config 74 / crypt 79）。提高
  floor 是刻意行为：改 `FLOOR` map 并提交说明。
- **PR 快照**（`scripts/pr-coverage-report.sh`，ci.yaml 中仅 PR 运行）：
  非阻塞，把六包覆盖率和"本 PR 有变更"的包写进 PR summary，便于在
  合并前观察趋势。`pkg/syncer` 用 `-coverpkg` 对 CLI 集成测试口径
  （单包 ~15% 是假象）。

## Contract 矩阵（两层）

1. **本地（每 PR，零凭证）**：`pkg/drive/contract_matrix_test.go` 用
   `drive.FakeDriver` 统一检查 capability 声明与行为一致、unsupported
   分类、列表稳定性、`RunBehaviorChecks`、debug snapshot 无凭证；
   能力集合本身被 pin，新增能力必须显式。
2. **真实 provider（nightly / 手动）**：Contract Tests workflow 按 mount
   并行跑，每个 job 启动一个共享 mount 服务器（`qrypt mount -s`），
   `test_enabled = true` 的 mount 通过 403 保护之外的入口执行。

## 配置项

- `CONTRACT_CONFIG_TOML_B64`（secret）：Contract Tests 使用的
  `qrypt.toml` 的 base64。内容含真实凭证，只进 secret，绝不进日志。
- `CONTRACT_MOUNTS_JSON`（repository **variable**）：默认跑 contract 的
  mount 列表（JSON 数组）。**必须用 variable 而不是 secret**：GitHub
  会把含 secret 的 job output 脱敏为 `***`，导致
  `fromJson("***")` 展开失败、matrix job 不创建。
  同步本机配置：`gh variable set CONTRACT_MOUNTS_JSON --body '["yun139","115"]'`。

## Fuzz 与 corpus

- nightly fuzz 失败时 `**/testdata/fuzz/**` 上传为 `fuzz-corpus`
  artifact；按 [fuzz-corpus.md](fuzz-corpus.md) 下载并提交最小复现样本，
  之后每次 `go test ./...` 自动回归。
- 已保留样本：`pkg/crypt`（EME 分段 panic）、`pkg/config`
  （ParseSize NaN）。

## Windows 验证

发布产物含 windows/amd64 与 windows/arm64，但平台代码（`*_windows.go`、
纯 Go nocgo 宿主 `host_nocgo_windows.go`、WinFsp DLL 加载）需要 CI 验证，
分两级：

1. **每 PR**（ci.yaml `test-windows` job，windows-latest）：`CGO_ENABLED=0`
   `go build -tags nocgo`（与发布产物同款标签）、`go vet ./...`、主模块
   单测（只用主模块包列表，不用 `go test ./...`——vendored cgofuse 的
   Windows 宿主测试需要真实 WinFsp）、以及 localfs fs 冒烟
   （`scripts/smoke-localfs.sh`，仅 `qrypt fs` 命令、不挂载）。无 WinFsp
   依赖，跑得快。
2. **每夜**（nightly.yaml `windows-mount` job）：安装 WinFsp（choco），
   以 localfs + 加密配置真实挂载（`scripts/smoke-windows-mount.ps1`），
   经 FUSE 路径写文件，校验 `qrypt fs cat` 解密读回、后端只存加密乱码
   文件名、再经 FUSE 路径读回。这是 nocgo 宿主 + WinFsp 集成唯一覆盖
   的路径。

注意：Windows 下 race 检测需要 cgo 工具链（runner 无 gcc），所以
`-race` 只在 Linux 跑。

## 本地等价命令

```bash
./scripts/test-layers.sh              # 分层测试（fast/contract/race 等）
./scripts/coverage.sh -print          # 覆盖率快照（不 gate）
./scripts/fuzz-nightly.sh 10s         # 本地 fuzz（每 fuzzer 10s）
./scripts/smoke-localfs.sh            # localfs 挂载冒烟
./scripts/smoke-windows-mount.ps1     # Windows 真实挂载冒烟（需 WinFsp）
```

## 测试并行度

除少数例外，测试都带 `t.Parallel()`。`pkg/vfs` 的 281 个测试串行需 14.5s，
并行后 5.0s；`pkg/core` 8.2s → 2.8s。fast 层墙钟因此从约 21.5s 降到约 12s。

两类进程级状态让测试不能并行，且都看不出自测试自身：

- **testing 拒绝的调用与全进程视角**：`t.Setenv`/`t.Chdir` 在 `t.Parallel()`
  之后调用会 panic；`goleak.Find` 扫描进程内全部 goroutine，只要有别的测试在跑
  就永远等不到「无泄漏」，用它的测试需要独占本包。
- **被当作测试接缝的生产包级变量**：`pkg/core` 的 `UploadStreamTaskPollInterval`
  和 `DirectUploadRetryBaseDelay`、`pkg/vfs/upload` 写 `pkg/logging` 的 `logging.L`。
  哪个测试在跑就写哪个，两个并行就在赋值本身上竞争。

只把写入者标为串行就够了：go 的 testing 会先把所有非并行测试跑完再恢复并行的
那些，所以串行写入者必定早于并行读取者结束。

`scripts/test-parallelism.py` 维护这件事，它顺调用图传播（`pkg/core` 的
`newTaskBoundaryCore` 写接缝，调用它的测试即便自身没提过也要串行），也识别跨包
赋值：

```bash
./scripts/test-parallelism.py audit ./pkg/vfs   # 只报告，有待改动时退出 1
./scripts/test-parallelism.py apply ./pkg/vfs   # 落地
```

改完**必须**跑 `test-layers.sh race`。静态分析看不到「依赖另一个测试先把接缝
设成特定状态」这类顺序耦合（`pkg/mobile` 的直传测试即如此），只有 race 层兜得住。

## 版本固定策略

为保证同一提交在不同时间得到相同结果，CI 的全部质量工具和 GitHub
Action 都固定版本。
工具版本升级是刻意行为：更新版本号/SHA 并提交，让 CI 结果可追溯。
