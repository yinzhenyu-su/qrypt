#!/usr/bin/env bash
# Append the CHANGELOG "### Thanks" block for a release to its GitHub Release
# notes. GoReleaser builds the release body from commits, so this keeps the
# contributor acknowledgement visible on the release page as well.
#
# Usage:
#   scripts/release/add-release-thanks.sh vX.Y.Z [--dry-run]
#
# The script reads the version section first, then falls back to "## Unreleased"
# for tags created before the changelog was generated. It is idempotent: if the
# release already contains the same first thanks line, it does nothing.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CHANGELOG="${QRYPT_CHANGELOG:-$ROOT/CHANGELOG.md}"

tag="${1:-}"
dry_run="${2:-}"
[[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || {
  echo "用法: scripts/release/add-release-thanks.sh vX.Y.Z [--dry-run]" >&2
  exit 1
}
[ -f "$CHANGELOG" ] || {
  echo "找不到 CHANGELOG: $CHANGELOG" >&2
  exit 1
}

extract_thanks() {
  local heading="$1"
  awk -v heading="$heading" '
    ($0 == "## " heading) || (index($0, "## " heading " ") == 1) { in_version = 1; next }
    in_version && /^## / { exit }
    in_version && /^### Thanks[[:space:]]*$/ { in_thanks = 1; next }
    in_thanks && /^### / { exit }
    in_thanks && /^## / { exit }
    in_thanks && /^---[[:space:]]*$/ { exit }
    in_thanks { print }
  ' "$CHANGELOG"
}

trim_block() {
  awk '
    NF { started = 1; last = n + 1 }
    started { lines[++n] = $0 }
    END { for (i = 1; i <= last; i++) print lines[i] }
  '
}

thanks="$(extract_thanks "$tag")"
if [ -z "$(printf '%s' "$thanks" | tr -d '[:space:]')" ]; then
  thanks="$(extract_thanks "Unreleased")"
fi
if [ -z "$(printf '%s' "$thanks" | tr -d '[:space:]')" ]; then
  echo "CHANGELOG 中没有 $tag 或 Unreleased 的 Thanks 段,跳过"
  exit 0
fi
thanks="$(printf '%s\n' "$thanks" | trim_block)"
block="### Thanks
$thanks"

if [ "$dry_run" = "--dry-run" ]; then
  printf '%s\n' "$block"
  exit 0
fi

body="$(gh release view "$tag" --json body --jq .body)"
first_line="$(printf '%s\n' "$thanks" | awk 'NF { print; exit }')"
if printf '%s\n' "$body" | grep -Fq -- "### Thanks" &&
  printf '%s\n' "$body" | grep -Fq -- "$first_line"; then
  echo "release $tag 已包含 Thanks 段,跳过"
  exit 0
fi

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
{
  if [ -n "$body" ]; then
    printf '%s\n\n' "$body"
  fi
  printf '%s\n' "$block"
} > "$tmp"

gh release edit "$tag" --notes-file "$tmp"
echo "已把 Thanks 段追加到 release $tag"
