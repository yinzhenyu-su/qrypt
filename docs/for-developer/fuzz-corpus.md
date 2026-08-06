# Fuzz Corpus 管理

qrypt 用 Go 原生 property fuzzing 加固可靠性敏感逻辑。失败输入（corpus 条目）
必须**提交入库**，这样一次 nightly 发现的问题会在此后每次普通 `go test ./...`
中自动回归。

## 流程

1. **发现**：`scripts/fuzz-nightly.sh`（本地手动或 nightly workflow）对每个 fuzzer
   跑固定预算。任何一个 fuzzer 失败，脚本退出码为 1。
   - 本地：`./scripts/fuzz-nightly.sh`（默认每 fuzzer 30s）或
     `./scripts/fuzz-nightly.sh 10s`（自定义预算）
   - CI：`.github/workflows/nightly.yaml` 的 `fuzz` job，失败时把整个
     `**/testdata/fuzz/**` 上传为 `fuzz-corpus` artifact

2. **下载**（CI 失败时）：从 run 页面下载 `fuzz-corpus` artifact，或命令行：

   ```bash
   gh run download <run-id> -n fuzz-corpus -D /tmp/fuzz-corpus
   ```

3. **提交最小复现样本**：把新出现的 corpus 文件复制回对应包的
   `testdata/fuzz/<FuzzName>/` 目录并入库：

   ```bash
   mkdir -p pkg/crypt/testdata/fuzz/FuzzCipherSegmentRoundtrip
   cp /tmp/fuzz-corpus/pkg/crypt/testdata/fuzz/FuzzCipherSegmentRoundtrip/<hash> \
      pkg/crypt/testdata/fuzz/FuzzCipherSegmentRoundtrip/
   git add pkg/crypt/testdata
   git commit -m "test(crypt): add fuzz regression corpus for <hash>"
   ```

   提交信息里说明该样本复现的问题（例如 EME 分段 panic、ParseSize NaN）。

4. **回归**：入库后无需额外配置。`go test ./...` 会把
   `testdata/fuzz/<FuzzName>/` 下的每个文件作为 seed 跑一遍对应 fuzzer 的
   `FuzzXxx/seed` 测试。

## 已保留的真实失败样本

| 包 | Fuzzer | 样本数 | 对应问题 |
|---|---|---|---|
| `pkg/crypt` | `FuzzCipherSegmentRoundtrip` | 1 | 长文件名在 `eme.Transform` 128 块上限处 panic → 分段加密 |
| `internal/config` | `FuzzParseSizeAndDuration` | 1 | `ParseSize("nAn")` 接受 NaN → `int64(NaN)` 未定义行为 |

## 新增 fuzzer 的规则

- fuzzer 放被测逻辑所属包，命名 `Fuzz<Area><Property>`（如 `FuzzCleanVirtualPath`）
- 断言只做**性质不变式**（往返、非负、稳定分类），不依赖具体实现细节
- 加入 `scripts/fuzz-nightly.sh` 的 `FUZZERS` map
- 普通 `go test ./...` 必须能在 seed 模式下通过（默认 corpus 为空时秒过）

## 优先级

加密文件名和路径解析是最值得长期保留样本的领域（已发生过真实 panic）。
`pkg/crypt/FuzzCipherSegmentRoundtrip` 和 `pkg/vfs/FuzzCleanVirtualPath`
的 corpus 优先维护。
