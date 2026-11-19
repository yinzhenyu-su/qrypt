---
name: qrypt-path-scrub
description: Detect and remove real local absolute paths (operator home directories, machine temp subtrees) from a git repository, including already-published history, and force-push the rewritten refs. Use when a commit or the public history contains paths like /Users/<name>/..., when a path leak must be scrubbed before release, or when the pre-commit/pre-push path gate fires.
---

# 真实路径清理

公开仓库里出现操作者本机路径（`/Users/<名字>/...`、`/private/tmp/qrypt-*` 等）后，**只在工作区改掉不够**：只要旧提交被 push 过，就要重写历史并强推。仓库用两道防线：提交时拦截 + 泄漏后脚本化清理。

## 1. 先检测

```bash
scripts/ci/check-paths.sh                 # 工作区（pre-commit / check-all 使用）
scripts/ci/check-paths.sh --range A..B    # 推送范围（pre-push 使用）
scripts/ci/check-paths.sh --refs          # 全部 refs 的全部提交（审计）
```

- 命中会输出 `check-paths: <位置> contains <前缀>` 并 exit 1；`--refs` 审计慢（按提交数 × 前缀数跑 `git grep`），只做审计不做日常门禁。
- 允许的通用示例路径：`/Users/me`、`/Users/user`、`/Users/someone`、`/home/user`、`/Volumes/...`、`/tmp/qrypt-*`；CI 通用 home（`runner`、`ubuntu`、`node` 等）也会被自动跳过。
- 前缀来自 `$HOME`、`/Users/$(id -un)`、`/home/$(id -un)` 和 `/private/tmp/qrypt-`。在别的机器上审计别人的泄漏时临时 `HOME=/Users/<对方>` 覆盖。

## 2. 清理工作区

把真实路径换成可公开的写法，并确认不是误伤：

- 仓库自身路径 → `$REPO_ROOT`；
- home 下普通路径 → `/home/user`（或文档里已有的 `/Users/me` 风格）；
- `/private/tmp/qrypt-*` → `/tmp/qrypt-*`；
- 测试夹具里的真实媒体文件名/挂载名也要换掉（例如 `Linus.mp4` → `movie.mp4`、`drime` → `cloud`）；
- **保留**：`github.com/<user>/<repo>`（模块路径/仓库 URL 不是本机路径）、CI 通用示例、`/Users/me` 之类占位符。

## 3. 重写历史

```bash
scripts/dev/scrub-paths.sh --dry-run    # 先看有哪些
scripts/dev/scrub-paths.sh              # 本地重写 + 打印 force-push 计划 + 备份 bundle
scripts/dev/scrub-paths.sh --push       # 重写并强推改动的远端分支/tag
```

脚本固定做这些事，不要手工复制流程：

1. `git bundle create` 全量备份（`/tmp/qrypt-pre-scrub-<时间>.bundle`），失败不要继续；
2. `git filter-branch -f --tree-filter <python>` + `--tag-name-filter cat -- --all` 重写所有分支、tag 和 remote-tracking refs；
3. 删除 `refs/original`、`git reflog expire --expire=now --all`、`git gc --prune=now`；
4. 用 `check-paths.sh --refs` 复检，仍然命中就停止（备份 bundle 可用于恢复）；
5. 对比改写前后的 refs，只强推远端存在的分支和 tag；`--push` 用 `--no-verify`，因为重写内容已通过复检，而 pre-push 会为 main 和每个 tag 重跑完整本地 CI。

## 4. 强推后的后果（必须告知用户）

- 所有协作者要 `git fetch --all --prune --tags` 后重置分支；旧 SHA 的分支不能直接 pull；
- GitHub 的 `refs/pull/*` 和按 SHA 缓存的对象不受 force-push 影响，旧提交仍可能通过 PR 页面或直接 SHA 访问；要彻底清除需删除对应 PR 或联系 GitHub Support；
- 如果远端有 required status checks，强推 main 会触发新一轮检查；必要时说明这是历史重写而非代码变更。

## 5. 预防

- 提交前门禁：`.githooks/pre-commit` 跑 `check-paths.sh --tree`；推送前 `.githooks/pre-push` 跑 `--range <remote-tip>..HEAD`；`scripts/ci/check-all.sh` 也有独立 step。
- 新增文档/夹具时不要贴本机绝对路径；`probe` 输出写进文档前先替换 temp 前缀。
- 发现泄漏先停手：不要在同一个分支继续提交，先清理再继续，避免重写范围扩大。

## 验证清单

- `check-paths.sh --tree --refs` 均 exit 0；
- 输出里不再出现真实路径，但允许的通用示例仍在；
- 远端 `git ls-remote` 的哈希与本地一致；
- 协作者/CI 用新历史 clone 后能正常 build/test。
