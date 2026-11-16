---
name: qrypt-maintainer-pr
description: Triage, review, and merge qrypt pull requests as a maintainer. Use when handling a fork PR, approving a gated CI run, enforcing PR title or ruleset requirements, squash merging, or verifying post-merge state and contributor thanks.
---

# Qrypt Maintainer PR

## 当前仓库规则

`main` 的 ruleset（以 GitHub 实际配置为准）：

- 只允许 **squash merge**，要求 linear history；
- 至少 1 个 approval，push 后旧 approval 失效；
- 所有 review conversation 必须 resolve；
- 必需检查：`Check (static)`、`Check (fast)`、`Check (race)`、
  `Check (stability)`、`Test (windows)`、`PR title`；
- 分支必须与 `main` 保持最新（strict required checks）；
- 禁止删除分支和 force push。

PR 标题就是最终的 squash commit 和 changelog 条目，必须符合
Conventional Commits。`docs`/`chore` 不进 release notes；`feat`/`fix` 会进。

### CI 触发模型

- `.github/workflows/ci.yaml` 只负责测试：`push` 只对 `main`、`pull_request`
  只对 `main`。**分支上的 push 不再跑 CI**（已去掉 `feat/*`），不要等一个不会
  出现的 run。
- 开发分支的验证靠两层：`.githooks/pre-push` 对普通分支只跑
  `scripts/ci/test-layers.sh changed`（相对远端 tip 的改动包），推上去之后由
  PR 上的 CI 覆盖全量；需要提前全量自测时用 `QRYPT_FULL_PUSH_CHECK=1` 或手动
  `scripts/ci/check-all.sh`。
- 发布相关 job 已从 `ci.yaml` 移到 `.github/workflows/release.yaml`（tag push
  或 `gh workflow run release.yaml -f version=vX.Y.Z`）。发布流程见
  `qrypt-release` skill。
- `main` 上的 run 不会被后续同 workflow 的 run 或发布 dispatch 自动取消：
  `ci.yaml` 的 `cancel-in-progress` 只对 PR 生效，`release.yaml` 有独立的
  concurrency group。看到 cancelled 时用 `gh run view <run-id> --json
  event,conclusion` 查原因，别默认是正常现象。

## 1. 分诊

```bash
gh pr view <N> --json number,title,state,author,authorAssociation,isDraft,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup,body,files
gh pr diff <N>
gh pr checks <N>
```

检查：

- 标题格式：`type(scope): subject`；不对就
  `gh pr edit <N> --title 'fix(vfs): ...'`，标题检查会重跑。
- 是否关联 Issue；没有就要求写 `N/A` 和原因。
- 一个 PR 是否只做一件事；非生成代码超过约 800 行 / 20 个文件时要求设计摘要或拆分。
- 是否手改了生成物（`schema/`、i18n catalog、`full-config.md`、
  `support-drivers.md`、驱动文档）；要求改生成器。
- 是否包含真实凭证、cookie、token、签名 URL、未脱敏日志。
- 驱动 PR 额外走 `qrypt-driver-review` skill。

## 2. 外部 fork PR 的 CI 审批

首次贡献者的 PR 可能出现 `action_required`，此时检查不会自动跑：

```bash
gh run list --branch <head-branch> --limit 5 \
  --json databaseId,status,conclusion,event,headSha

gh api --method POST \
  repos/<owner>/<repo>/actions/runs/<run-id>/approve

gh run watch <run-id> --exit-status
```

只审批你愿意审查的代码；不要对来源不明的 PR 直接批准运行。

## 3. 本地验证

```bash
gh pr checkout <N>
git fetch origin main
git merge origin/main        # 让分支满足 strict up-to-date
scripts/ci/check-all.sh
```

必须看到 `== local CI check passed ==`。如果只改文档/生成物，也至少跑
`git diff --check`，并以 CI 全绿为准。`check-all.sh` 的 Go 版本要求来自
`go.mod`（单源），本地 Go 版本与 `go.mod` 不在同一 minor 线时会直接失败。

## 4. Review

```bash
gh pr review <N> --approve --body '...'
gh pr review <N> --request-changes --body '...'
gh pr comment <N> --body '...'
```

- 每个 blocker 给文件/行号和可复现命令。
- 外部 PR 至少 1 个 approval；维护者自己的 PR 不能自己 approve。
- 所有 conversation resolve 后才能合并。
- 不要用“重跑”掩盖真实代码失败；先区分环境/网络与回归。

## 5. 合并

```bash
# 查看是否可合并
gh pr view <N> --json mergeable,mergeStateStatus,reviewDecision

# 正常 squash merge
gh pr merge <N> --squash --delete-branch

# 自己的 PR 被 review 规则挡住时，仅在全部 required checks 通过后：
gh pr merge <N> --squash --delete-branch --admin
```

`--admin` 只用于维护者自己的 PR 或紧急情况，且必须 CI 全绿；不得绕过失败检查。
合并后确认 Issue 因 `Fixes #N` / `Closes #N` 自动关闭，分支已删除。

## 6. 合并后验证

```bash
git fetch origin main
git log -2 --oneline origin/main
gh pr view <N> --json state,mergedAt,mergeCommit

# 只看 main 的 CI；用 workflow 过滤，避免把 release run 混进来
gh run list --branch main -w ci.yaml -L 3 \
  --json databaseId,event,headSha,status,conclusion
gh run watch <run-id> --exit-status

gh issue view <issue-number> --json state
```

合并后的 `main` push CI 有一种正常的“非绿”：

- **skipped**：`detect` 判定 squash 后的 tree 与已绿的 PR head 完全一致时，
  `check` 矩阵和 `test-windows` 被跳过（每次 push 的 run 本身仍会成功完成
  `detect`）。不是失败：原 PR head 上的六项检查才是这次 tree 的结论；需要证据时
  用 `gh pr view <N> --json statusCheckRollup` 看 PR head 的检查。
- **cancelled**：`main` push 的 run 不会因为同 workflow 的新 run 或
  `release.yaml` 的 dispatch 被自动取消——`ci.yaml` 的 `cancel-in-progress`
  只对 `pull_request` 生效，`release.yaml` 也有独立的 concurrency group。看到
  cancelled 时先用 `gh run view <run-id> --json event,conclusion` 查原因；
  确认没有更晚的 run 覆盖该提交后，按需要重跑。

真正的 `failure` 才需要处理：不要在 release 或下一个 PR 上继续叠加；先修复或
回滚。

## 7. Dependabot PR

`.github/dependabot.yml` 配置为周更，自动开 PR：

- `github-actions`：所有 actions 更新合并成一个 PR，标题/commit 前缀 `ci`；
- `gomod`：仅 minor/patch 合并成一个 PR，前缀 `chore`；major 单独成 PR，
  需要单独评审（可能含 breaking change）。

Dependabot 标题已符合 `check-pr-title.sh`（`ci:`/`chore:` 前缀），走普通 PR
流程：看 diff、等六项必需检查、squash merge。`ci`/`chore` 不进 release notes，
所以 actions/依赖升级不会污染 changelog。若 CI 因缓存 key（`tools.go` 或
`go.sum`）变化而失败，先看是不是新版本工具本身不兼容。

## 8. Changelog 与感谢

- squash 后的 PR 标题直接进入 release notes；合并前确认标题准确。
- 外部贡献者做了实质工作时，在 `CHANGELOG.md` 的 `## Unreleased` 下补：
  ```markdown
  ### Thanks
  - @user for contributing ... (#N)
  ```
  发版时 `gen-changelog.sh` 会并入版本，`add-release-thanks.sh` 会追加到
  GitHub Release notes。新发布流程要求 changelog 先经过 PR 合并到 `main`，
  再打 tag（见 `qrypt-release` skill），所以这类补充要在发版 PR 之前完成。
- 不要手工编辑已发布版本的 changelog 段；新条目写 `## Unreleased`。

## 9. 安全

- 不在公开 PR/Issue 里贴漏洞利用细节；按 `SECURITY.md` 私下处理。
- 审查日志、截图、配置示例时确认已脱敏。
- 涉及凭证存储、加密、路径解析、安装/升级完整性的 PR，要求作者说明风险和回滚方式。
