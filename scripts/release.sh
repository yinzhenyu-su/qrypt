#!/usr/bin/env bash
# 发版辅助:生成 CHANGELOG 记录 → 提交 → 打 tag。
#
# 用法:
#   scripts/release.sh v0.4.0
#
# 不会自动 push —— 发布由 CI 驱动(tag push 触发构建 + 自动发布):
#   git push origin main
#   git push origin v0.4.0
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

version="${1:-}"
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "用法: scripts/release.sh vX.Y.Z"; exit 1; }

[ -z "$(git status --porcelain)" ] || { echo "工作区不干净,先提交或暂存改动"; exit 1; }
[ -z "$(git tag -l "$version")" ] || { echo "tag $version 已存在"; exit 1; }
[ "$(git branch --show-current)" = "main" ] || { echo "请在 main 分支上发版"; exit 1; }

# 生成 changelog 记录并提交
scripts/gen-changelog.sh "$version"
if git status --porcelain CHANGELOG.md | grep -q .; then
  git add CHANGELOG.md
  git commit -m "docs: update changelog for $version"
fi

# 打 tag
git tag "$version"
echo
echo "✓ CHANGELOG 已更新,tag $version 已打"
echo
echo "下一步(发布由 CI 全自动):"
echo "  git push origin main"
echo "  git push origin $version"
