#!/usr/bin/env bash
# Validate a pull request title against the repository's Conventional Commits
# policy. Squash merges use the PR title as the changelog entry, so this runs as
# a required check before merge.
set -euo pipefail

title="${1:-}"
pattern='^(feat|fix|docs|refactor|test|perf|ci|build|chore)(\([^)]+\))?!?: .+'

if [[ "$title" =~ $pattern ]]; then
  exit 0
fi

cat >&2 <<'EOF'
PR 标题不符合 Conventional Commits。

格式: type(scope): subject
type: feat / fix / docs / refactor / test / perf / ci / build / chore
示例: feat(drivers): add Koofr backend
      fix(vfs): clear permanent_fail pending entries
      docs: update changelog

破坏性变更使用 !，例如: feat(config)!: change the config schema
EOF
exit 1
