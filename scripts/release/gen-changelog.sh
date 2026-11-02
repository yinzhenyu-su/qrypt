#!/usr/bin/env bash
# 生成 CHANGELOG.md 的版本记录(自动 prepend,不会覆盖已有条目)。
# 兼容 macOS 默认 bash 3.2。
#
# 用法:
#   scripts/release/gen-changelog.sh [vX.Y.Z]    # 默认推导下一个版本号
#
# 从上一 tag 到 HEAD 收集约定式提交,按类型分组(与 .goreleaser.yaml 一致):
#   feat → Features, fix → Bug fixes, 其余 → Others, 排除 docs:/chore:/merge。
# 发版前可把感谢、迁移提示等写在 CHANGELOG 顶部的 ## Unreleased 下,
# 生成新版本时自动并入,并从旧内容中移除。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
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
  if printf '%s' "$c" | grep -Eq '^feat(\([^)]*\))?:'; then features="${features}- ${c}
"
  elif printf '%s' "$c" | grep -Eq '^fix(\([^)]*\))?:'; then fixes="${fixes}- ${c}
"
  else others="${others}- ${c}
"; fi
done < <(git log --pretty="%s" "$prev_tag"..HEAD)

# ── 手工维护的 Unreleased 段落 ─────────────────────────────────────────
# 发版前可把感谢、迁移提示等写在 CHANGELOG 顶部的 ## Unreleased 下。
# 生成新版本时把它并入本次版本段，并从旧内容中移除，避免重复带入。
pending=""
changelog_source="$CHANGELOG"
if [ -f "$CHANGELOG" ]; then
  pending="$(awk '
    /^## Unreleased[[:space:]]*$/ { in_pending = 1; next }
    in_pending && /^## / { exit }
    in_pending { print }
  ' "$CHANGELOG")"
fi
if [ -f "$CHANGELOG" ] && grep -q '^## Unreleased[[:space:]]*$' "$CHANGELOG"; then
  stripped="$(mktemp)"
  awk '
    /^## Unreleased[[:space:]]*$/ { skip = 1; next }
    skip && /^## / { skip = 0 }
    !skip { print }
  ' "$CHANGELOG" > "$stripped"
  changelog_source="$stripped"
fi

# ── 拼装新版本段 ───────────────────────────────────────────────────────
section="## v${version} ($(date +%Y-%m-%d))"
if [ -n "$features" ]; then
  section="$section

### Features
$(printf '%s' "${features%$'\n'}")"
fi
if [ -n "$fixes" ]; then
  section="$section

### Bug fixes
$(printf '%s' "${fixes%$'\n'}")"
fi
if [ -n "$others" ]; then
  section="$section

### Others
$(printf '%s' "${others%$'\n'}")"
fi
if [ -n "$(printf '%s' "$pending" | tr -d '[:space:]')" ]; then
  pending_body="$(printf '%s\n' "$pending" | awk '
    NF { started = 1; last = n + 1 }
    started { lines[++n] = $0 }
    END { for (i = 1; i <= last; i++) print lines[i] }
  ')"
  section="$section

$pending_body"
fi

# ── prepend 到 CHANGELOG.md ─────────────────────────────────────────────
tmp="$(mktemp)"
if [ -f "$changelog_source" ] && grep -q "^# Changelog" "$changelog_source"; then
  {
    echo "# Changelog"
    echo
    echo "$section"
    echo
    echo "---"
    echo
    tail -n +2 "$changelog_source" | awk "NR==1 && /^$/ {next} {print}"
  } > "$tmp"
else
  printf '# Changelog\n\n%s\n' "$section" > "$tmp"
fi
mv "$tmp" "$CHANGELOG"
[ -z "${stripped:-}" ] || rm -f "$stripped"
echo "已生成 v${version} 记录到 CHANGELOG.md"
