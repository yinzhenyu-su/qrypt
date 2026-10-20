- 提交前确保通过 ci check 中的测试
- 优先使用 go 1.27+ 的语法

## 文档编写

面向软件用户的文档以“用户完成任务”为目标，按用户的使用顺序组织，而不是按实现结构组织。
面向软件开发者的文档以“专业准确”为目标，要能帮助新的开发者快速定位模块和功能，理解项目结构。

- 首次使用顺序：简介（能做什么）→ 安装脚本 → 最小上手体验。指定版本、手动安装、卸载等放到后面或独立章节。
- README 只做入口页：一句话介绍、安装、最小三步和链接；详细步骤放 `quickstart.md`，避免两处重复维护。
- 按用途分层：`quickstart` 负责首次跑通，feature guide 负责单项功能，`cli`/`full-config` 负责查参数，developer docs 负责实现细节。
- 第一屏不要出现：生成器说明、内部 API/实现细节、能力矩阵、完整配置、调试/probe/journal 细节、性能 benchmark 数字、变更记录。
- 示例只展示必填参数和 1–2 个常用可选参数；完整参数放表格。避免一次列出所有可选参数。
- 命令统一写成安装后的 `qrypt ...`；只有手动安装场景才补充 `./qrypt`（Windows 为 `.\qrypt.exe`）。
- 不要写死版本号。需要示例时用 `<版本>` 或明确写“以 vX.Y.Z 为例”，并链接 releases 页面。
- 语气平和自然，不直接照搬用户原话。优先使用用户能理解的表述；用户文档中的参数说明应中文化，schema 和 CLI 输出仍可保留英文。
- 生成文档（如 `docs/for-user/support-drivers.md`、`docs/for-user/full-config.md`）不要手改；修改生成器后重新生成。运行时事实（如驱动能力）从 Go 代码派生，避免维护第二份真相。
- 拆分长内容：命令语法留在 `cli.md`，完整语义放独立 feature guide；故障处理放 troubleshooting；实现细节放 developer docs。
- 修改后至少重新生成文档并确认稳定、检查相对链接、运行 `git diff --check`；涉及生成器或代码时运行 `scripts/ci/check-all.sh`。

## Agent skills

### Issue tracker

GitHub Issues（`gh` CLI）。See `docs/agents/issue-tracker.md`.

### Domain docs

single-context（根目录 `CONTEXT.md` + `docs/adr/`）。See `docs/agents/domain.md`.
