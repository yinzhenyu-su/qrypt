# Contributing to qrypt

感谢参与。qrypt 是云盘加密挂载工具，改动会直接影响用户数据和凭证安全，所以提交前请先确认下面的约定。

## 开始之前

- 小修复（typo、文档、明确的 bug）可以直接开 PR。
- 新功能、驱动、协议变更、跨包重构先开 Issue 说明目标和范围，避免大改后返工。
- 使用 AI 辅助不禁止，但提交者要对正确性、测试和许可证负责；PR 里请说明验证范围（例如真实账号验证 / 仅 fake provider）。

## 开发环境

- Go 1.27.x（CI 使用 1.27.4）。
- 只有 `qrypt mount` 需要 FUSE：macOS 用 macFUSE，Linux 用 libfuse3/fuse3，Windows 用 WinFsp；`qrypt fs` 不需要。
- 安装本地 hooks：

```bash
git config core.hooksPath .githooks
```

pre-commit 会跑 gofmt、vet 和受影响测试；pre-push 对 main/tag 跑完整本地 CI，对其他分支跑变更包。

## 分支

从最新 `main` 开短期分支：

- `feat/<slug>`
- `fix/<slug>`
- `docs/<slug>`
- `test/<slug>`
- `refactor/<slug>`
- `ci/<slug>`

一个分支只做一个逻辑变更。合并后分支会自动删除。

## 提交与 PR 标题

统一使用 Conventional Commits：

```text
type(scope): subject
```

- type：`feat` / `fix` / `docs` / `refactor` / `test` / `perf` / `ci` / `build` / `chore`
- scope 建议填写：`cli`、`vfs`、`crypt`、`config`、`drivers`、`sync`、`task`、`mount`、`docs`、`ci`
- subject 用英文时小写开头，不加句号；中文可以直接写
- 破坏性变更用 `!`，例如 `feat(config)!: ...`

示例：

```text
feat(drivers): add Koofr backend
fix(vfs): clear permanent_fail pending entries
docs(drivers): document Koofr capabilities
```

仓库只允许 squash merge，**PR 标题就是最终提交标题和 changelog 条目**，所以 PR 标题必须符合上面的格式。CI 会校验 PR 标题。

## 本地验证

提交前至少运行：

```bash
scripts/ci/check-all.sh
```

Linux 缺少 FUSE 依赖时：

```bash
scripts/ci/check-all.sh --install-system-deps
```

只改某个包时，可以用 `scripts/ci/test-layers.sh changed` 快速验证，但合并前仍以完整 `check-all.sh` 或 CI 全绿为准。

## PR 要求

- 关联 Issue；确实没有时写 `N/A` 并说明原因。
- 说明改了什么、为什么改、验证了什么。
- 新功能有测试；bug fix 有回归测试；并发/生命周期变更补 race 或稳定性测试。
- 行为变更同 PR 更新用户文档和开发者文档。
- 生成物不要手改：`schema/`、i18n、驱动文档、`full-config.md` 等由脚本生成。
- 不要提交真实凭证、cookie、token、私钥或未脱敏日志。
- 非生成代码超过约 800 行或 20 个文件时，先在 PR 里给设计摘要，必要时拆分。

评审约定：

- 外部 PR 至少 1 个 approval。
- 维护者自己的 PR 在 CI 全绿后可自合。
- 所有 review conversation 需要 resolve。
- CI 失败先区分代码失败和环境/网络失败，不要用“重跑”掩盖真实回归。

## 文档与领域语言

- 用户文档面向“完成任务”，按使用顺序组织；开发者文档面向“快速定位模块”。
- 领域术语以根目录 `CONTEXT.md` 为准，不要引入同义新词。
- 架构边界和迁移规则见 `docs/for-developer/package-boundaries.md`。
- 质量门禁和测试分层见 `docs/for-developer/quality-gates.md`。
- 驱动开发见 `docs/for-developer/driver-development.md`。

## 安全

- 漏洞请按 `SECURITY.md` 私下报告，不要在公开 Issue 里贴利用细节或凭证。
- 日志、配置示例、PR 截图都必须脱敏。
- 任何涉及凭证存储、加密、路径解析、安装/升级完整性的改动，PR 里要单独说明风险和回滚方式。

## 发布

发布由维护者执行：

```bash
scripts/release/release.sh vX.Y.Z
git push origin main
git push origin vX.Y.Z
```

- 遵循 SemVer。
- `CHANGELOG.md` 只由脚本生成。
- 待发布说明写在 `## Unreleased`，发版时自动并入。
- 用 `release.sh` 发版时，CHANGELOG 会随 tag 一起提交；如果直接打 tag，CI 会在 tag 缺少该版本记录时创建 changelog PR，合并后 `main` 上的 CHANGELOG 才更新。
- `v*` tag 受保护，只有维护者能发布。

## 问题与讨论

- Bug / 功能请求：开 GitHub Issue。
- 安全漏洞：不要开公开 Issue，见 `SECURITY.md`。
- 大型改动：先开 Issue 对齐范围，再写代码。
