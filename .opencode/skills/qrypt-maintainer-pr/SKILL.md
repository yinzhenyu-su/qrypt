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
`git diff --check`，并以 CI 全绿为准。

不要在 PR 分支上直接改贡献者的代码后 force push 到对方 fork，除非对方明确
同意且你有权限。优先用 review comment 要求修改；维护者需要直接修复时，开一个
维护者分支并在 PR 中说明。

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
gh pr merge <N> --squash

# 自己的 PR 被 review 规则挡住时，仅在全部 required checks 通过后：
gh pr merge <N> --squash --admin
```

`--admin` 只用于维护者自己的 PR 或紧急情况，且必须 CI 全绿；不得绕过失败检查。
合并后确认 Issue 因 `Fixes #N` / `Closes #N` 自动关闭，分支已删除。

## 6. 合并后验证

```bash
git fetch origin main
git log -2 --oneline origin/main
gh pr view <N> --json state,mergedAt,mergeCommit

gh run list --branch main --limit 2
gh run watch <run-id> --exit-status

gh issue view <issue-number> --json state
```

`main` CI 失败时不要在 release 或下一个 PR 上继续叠加；先修复或回滚。

## 7. Changelog 与感谢

- squash 后的 PR 标题直接进入 release notes；合并前确认标题准确。
- 外部贡献者做了实质工作时，在 `CHANGELOG.md` 的 `## Unreleased` 下补：
  ```markdown
  ### Thanks
  - @user for contributing ... (#N)
  ```
  发版时 `gen-changelog.sh` 会并入版本，`add-release-thanks.sh` 会追加到
  GitHub Release notes。
- 不要手工编辑已发布版本的 changelog 段；新条目写 `## Unreleased`。

## 8. 安全

- 不在公开 PR/Issue 里贴漏洞利用细节；按 `SECURITY.md` 私下处理。
- 审查日志、截图、配置示例时确认已脱敏。
- 涉及凭证存储、加密、路径解析、安装/升级完整性的 PR，要求作者说明风险和回滚方式。
