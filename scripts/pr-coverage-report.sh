#!/usr/bin/env bash
# PR coverage report. Measures the six core packages with the same scope as
# the nightly gate (scripts/coverage.sh), highlights packages whose files
# changed in this PR, and writes a markdown table to $GITHUB_STEP_SUMMARY.
#
# Deliberately non-blocking: the hard floor gate stays in the nightly
# workflow so PR iteration speed is not dragged down. Run with the base
# commit as $1:
#
#   bash scripts/pr-coverage-report.sh <base-sha>
set -euo pipefail
cd "$(dirname "$0")/.."

base="${1:?usage: pr-coverage-report.sh <base-sha>}"

# Same package set as scripts/coverage.sh. Path -> display name.
declare -A PKGS=(
  [pkg/vfs]=pkg/vfs
  [pkg/core]=pkg/core
  [pkg/drive]=pkg/drive
  [internal/sync]=internal/sync
  [internal/config]=internal/config
  [pkg/crypt]=pkg/crypt
)
# coverpkg-measured packages (measured through other packages' tests).
declare -A COVERPKG=([internal/sync]=1)

git fetch --quiet origin "$base" 2>/dev/null || true

changed=()
for pkg in "${!PKGS[@]}"; do
  if git diff --name-only "$base...HEAD" | grep -q "^$pkg/"; then
    changed+=("$pkg")
  fi
done

# Parse scripts/coverage.sh -print output lines:
#   "  pkg/vfs          75.4%  (floor 75%)"
out=$(bash scripts/coverage.sh -print)

summary="$GITHUB_STEP_SUMMARY"
if [ -n "$summary" ]; then
  [ -f "$summary" ] || touch "$summary"
  {
    echo "## 核心包覆盖率（PR 快照）"
    echo ""
    echo "硬性 floor gate 在 nightly workflow 中运行；本报告仅用于 PR 阶段观察，不阻塞合并。"
    echo ""
    echo "| 包 | 覆盖率 | Floor | 本 PR 变更 |"
    echo "|---|--------|-------|-----------|"
    for pkg in "${!PKGS[@]}"; do
      line=$(printf '%s\n' "$out" | awk -v p="$pkg" '$1==p {print}')
      pct=$(printf '%s\n' "$line" | awk '{print $2}' | tr -d '%')
      floor=$(printf '%s\n' "$line" | awk '{print $4}' | tr -d '%)')
      marker="-"
      for c in "${changed[@]}"; do
        [ "$c" = "$pkg" ] && marker="**有变更**"
      done
      note=""
      [ -n "${COVERPKG[$pkg]:-}" ] && note="（coverpkg 口径）"
      echo "| $pkg$note | $pct% | ${floor}% | $marker |"
    done
    echo ""
    echo "对比基线（2026-08-06）：vfs 75.4% / core 72.2% / drive 64% / sync 79.8% / config 74% / crypt 79.5%。"
  } >> "$summary"
fi

# Always print for the workflow log too.
echo "== core package coverage snapshot =="
printf '%s\n' "$out" | grep -E "pkg/vfs|pkg/core|pkg/drive|internal/sync|internal/config|pkg/crypt" || true
echo "changed in this PR: ${changed[*]:-none}"
