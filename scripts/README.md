# scripts 目录

本目录按用途分类存放开发、CI、发布和安装脚本。新增脚本时先判断它属于哪个
目录，再按下方命名约定取名。

## 目录结构

| 目录 | 职责 | 典型入口 |
| --- | --- | --- |
| `ci/` | 本地与 CI 门禁、测试分层、覆盖率、fuzz、工具安装，以及运行时 smoke | `ci/check-all.sh`、`ci/test-layers.sh` |
| `schema/` | 从 `schema/qrypt.schema.json` 和 `schema/i18n/` 生成代码与文档，并校验生成结果 | `schema/gen-drive-schema.py`、`schema/gen-config-docs.py` |
| `install/` | 首次安装脚本及其检查、端到端 smoke | `install/install.sh`、`install/install.ps1` |
| `release/` | 发版与 changelog | `release/release.sh` |
| `dev/` | 本地调试和辅助工具，不进入 CI 主链路 | `dev/debug-mount.sh`、`dev/build-android-aar.sh` |

安装脚本与 smoke 脚本的分工：

- `install/` 放安装器本身（`install.sh`、`install.ps1`）以及只验证安装器的
  `check-install.sh`、`smoke-install.sh`、`smoke-install.ps1`。
- `ci/` 放 CI/nightly 直接调度的门禁与运行时 smoke（例如
  `smoke-localfs.sh`、`smoke-windows-mount.ps1`）。

## 命名约定

文件名统一使用小写 kebab-case，格式为 `<动词>-<对象>[-<限定>].<扩展名>`：

| 前缀 | 用途 | 示例 |
| --- | --- | --- |
| `check-` | 只读校验或门禁，失败时退出非 0 | `check-arch.sh`、`check-schema.py` |
| `gen-` | 写入生成产物 | `gen-driver-docs.py` |
| `test-` | 运行测试或维护测试约束 | `test-layers.sh`、`test-parallelism.py` |
| `smoke-` | 端到端冒烟 | `smoke-install.sh` |
| `install-` / `build-` / `sync-` / `report-` / `release-` / `debug-` / `fuzz-` | 对应动作 | `install-tools.sh`、`build-android-aar.sh` |

其他约定：

- 使用完整单词，不用 `b64`、`cfg` 这类缩写；确实没有动词的工具按用途命名
  （如 `base64-codec.py`）。
- Python 脚本不要与标准库模块同名，否则脚本目录会遮蔽标准库导入；需要时加
  用途后缀（如 `base64-codec.py` 而不是 `base64.py`）。
- 需要 Go 辅助程序的脚本，把辅助程序放在同名目录中，例如
  `schema/gen-driver-capabilities/`、`dev/sync-contract-config/`。
- `install/install.sh` 和 `install/install.ps1` 的文件名不能改：Goreleaser 以
  这两个路径作为发布资源，用户也按资源名下载。
- 脚本应自行定位仓库根目录（`$(dirname "$0")/../..`），因此可以从任意工作
  目录调用。

## 常用入口

```bash
scripts/ci/check-all.sh                # 本地完整 CI 门禁
scripts/ci/test-layers.sh fast         # 快速测试层
scripts/ci/check-coverage.sh -print    # 查看覆盖率（不 gate）
scripts/schema/gen-driver-docs.py      # 重新生成驱动文档
scripts/schema/gen-config-docs.py      # 重新生成配置文档
scripts/release/release.sh vX.Y.Z      # 生成 changelog、提交并打 tag
```
