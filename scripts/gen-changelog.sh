#!/usr/bin/env bash
# 生成 CHANGELOG.md 的版本记录(自动 prepend,不会覆盖已有条目)。
# 兼容 macOS 默认 bash 3.2。
#
# 用法:
#   scripts/gen-changelog.sh [vX.Y.Z]    # 默认推导下一个版本号
#
# 从上一 tag 到 HEAD 收集约定式提交,按类型分组(与 .goreleaser.yaml 一致):
#   feat → Features, fix → Bug fixes, 其余 → Others, 排除 docs:/chore:/merge。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHANGELOG="$ROOT/CHANGELOG.md"

# ── 版本号 ──────────────────────────────────────────────────────────────
if [ -n "${1:-}" ]; then
  version="${1#v}"
  [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "版本格式应为 vX.Y.Z"; exit 1; }
else
  latest="$(git tag --sort=-version:refname | head -1 || echo v0.0.0)"
  latest="${latest#v}"
  ma="${latest%%.*}"; rest="${latest#*.}"; mi="${rest%%.*}"; pa="${rest#*.}"
  version="$ma.$mi.$((pa + 1))"
fi

# ── 上一 tag(排除当前版本) ─────────────────────────────────────────────
prev_tag="$(git tag --sort=-version:refname | grep -v "^v${version}$" | head -1 || echo v0.0.0)"
if ! git log --oneline "$prev_tag"..HEAD >/dev/null 2>&1; then
  echo "从 $prev_tag 到 HEAD 没有提交,无需生成"
  exit 0
fi

# ── 已在 CHANGELOG 中,跳过 ─────────────────────────────────────────────
if grep -q "^## v${version} " "$CHANGELOG" 2>/dev/null; then
  echo "CHANGELOG.md 已包含 v${version},跳过"
  exit 0
fi

# ── 收集提交并按类型分组(存为 \n 分隔字符串) ─────────────────────────
features=""; fixes=""; others=""
while IFS= read -r c; do
  case "$c" in
    docs:*|chore:*|Merge*) continue ;;
  esac
  if printf '%s' "$c" | grep -q '^feat('; then features="${features}- ${c}
"
  elif printf '%s' "$c" | grep -q '^fix('; then fixes="${fixes}- ${c}
"
  else others="${others}- ${c}
"; fi
done < <(git log --pretty="%s" "$prev_tag"..HEAD)

# ── 拼装新版本段 ───────────────────────────────────────────────────────
section="## v${version} ($(date +%Y-%m-%d))"
if [ -n "$features" ]; then
  section="$section

### Features
$(printf '%b' "$features" | sed '$d')"
fi
if [ -n "$fixes" ]; then
  section="$section

### Bug fixes
$(printf '%b' "$fixes" | sed '$d')"
fi
if [ -n "$others" ]; then
  section="$section

### Others
$(printf '%b' "$others" | sed '$d')"
fi

# ── prepend 到 CHANGELOG.md ─────────────────────────────────────────────
tmp="$(mktemp)"
if [ -f "$CHANGELOG" ] && grep -q "^# Changelog" "$CHANGELOG"; then
  {
    echo "# Changelog"
    echo
    echo "$section"
    echo
    echo "---"
    echo
    tail -n +2 "$CHANGELOG" | awk "NR==1 && /^$/ {next} {print}"
  } > "$tmp"
else
  printf '# Changelog\n\n%s\n' "$section" > "$tmp"
fi
mv "$tmp" "$CHANGELOG"
echo "已生成 v${version} 记录到 CHANGELOG.md"
