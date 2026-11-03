#!/usr/bin/env bash
# Read-only context collector for the issue-to-openspec workflow.
# Usage: scripts/collect-issue-context.sh <issue-number>
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <issue-number>" >&2
  exit 2
fi

issue="$1"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
cd "$root"

echo "== repository =="
git branch --show-current
git status --short

echo
echo "== openspec root =="
openspec context --json

echo
echo "== issue #${issue} =="
gh issue view "$issue" \
  --json number,title,state,author,createdAt,updatedAt,labels,body,comments

echo
echo "== open issues =="
gh issue list --state open --limit 100 \
  --json number,title,author,createdAt,labels

echo
echo "== specs =="
openspec list --specs

echo
echo "== changes =="
openspec list
