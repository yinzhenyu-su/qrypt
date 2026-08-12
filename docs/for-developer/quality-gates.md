# 质量门禁（CI Workflows）

qrypt 用三个 GitHub Actions workflow 组成质量体系：每 PR 的快速检查、
每夜的全量门禁、以及真实网盘 contract 套件。本地开发时对应的脚本
见文末。

## Workflow 总览

| Workflow | 触发 | 内容 | 状态徽章 |
| --- | --- | --- | --- |
| `CI` (ci.yaml) | push main / PR / tag | vet、staticcheck、golangci-lint、govulncheck、gofmt、`go test ./...`、VFS 稳定性 ×3、localfs smoke、race、**PR coverage 快照**；tag/dispatch 时构建并发布 | README |
| `Contract Tests` (contract.yaml) | 手动 dispatch 或 nightly cron | 对每个启用的 mount 跑真实网盘 contract 套件（auth/contract/crud/fs/instantupload/resume/multipart），`test_enabled = true` 的 mount 才可测 | README |
| `Nightly Quality Gates` (nightly.yaml) | 每天 18:00 UTC | **fuzz**（每 fuzzer 30s，失败上传 corpus artifact）、**coverage 硬性 floor gate**、race、全量测试 | README |

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

## 本地等价命令

```bash
./scripts/test-layers.sh              # 分层测试（fast/contract/race 等）
./scripts/coverage.sh -print          # 覆盖率快照（不 gate）
./scripts/fuzz-nightly.sh 10s         # 本地 fuzz（每 fuzzer 10s）
./scripts/smoke-localfs.sh            # localfs 挂载冒烟
```

## 版本固定策略

为保证同一提交在不同时间得到相同结果，CI 的全部质量工具和 GitHub
Action 都固定版本：

- `staticcheck@v0.7.0`、`golangci-lint@v1.64.8`、`govulncheck@v1.6.0`
  （`go run module@version` 精确固定）
- 所有 GitHub Action 用 commit SHA 固定，行尾注释标明对应 tag
  （如 `actions/checkout@3d3c42e… # v7`），升级时改 SHA + 注释
- 严禁引入 `@latest` 或未固定的 tag

工具版本升级是刻意行为：更新版本号/SHA 并提交，让 CI 结果可追溯。

