#!/usr/bin/env bash
# Reject real local absolute paths (the operator's home directory and machine
# specific temp trees) from tracked files and from commits. Repos are public and
# rewritten history is expensive: a path that reaches a commit forces a full
# history rewrite plus a force-push, so the gate runs in pre-commit and pre-push
# and in the local CI check.
#
# Generic example paths (/Users/me, /Users/someone, /home/user, /Volumes/...)
# stay allowed; only this machine's actual home and temp prefixes are denied.
#
# Usage:
#   scripts/ci/check-paths.sh                 # tracked working tree
#   scripts/ci/check-paths.sh --range A..B    # every commit in the range
#   scripts/ci/check-paths.sh --refs          # every commit of every ref (audit)
#   scripts/ci/check-paths.sh --tree --range A..B
#
# Exit 0 clean, 1 violations found, 2 usage error.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

usage() {
  sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
}

mode_tree=true
mode_range=""
mode_refs=false
while [ $# -gt 0 ]; do
  case "$1" in
    --tree)
      mode_tree=true
      ;;
    --range)
      mode_tree=false
      mode_range="${2:-}"
      shift
      ;;
    --range=*)
      mode_tree=false
      mode_range="${1#*=}"
      ;;
    --refs)
      mode_tree=false
      mode_refs=true
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "check-paths: unknown argument: $1" >&2
      exit 2
      ;;
  esac
  shift
done

home_dir="${HOME:-}"
user_name="$(id -un 2>/dev/null || whoami 2>/dev/null || true)"

# Generic CI and documentation home directories are not personal data.
generic_home_re='^(runner|ubuntu|user|me|someone|example|build|builder|node|vscode|appveyor|circleci|travis|jenkins)$'

patterns=()
add_pattern() {
  [ -n "$1" ] || return 0
  local p
  for p in "${patterns[@]:-}"; do
    [ "$p" = "$1" ] && return 0
  done
  patterns+=("$1")
}

if [ -n "$home_dir" ]; then
  home_base="$(basename "$home_dir")"
  if ! printf '%s' "$home_base" | grep -Eq "$generic_home_re"; then
    add_pattern "$home_dir"
  fi
fi
if [ -n "$user_name" ] && ! printf '%s' "$user_name" | grep -Eq "$generic_home_re"; then
  add_pattern "/Users/$user_name"
  add_pattern "/home/$user_name"
fi
add_pattern "/private/tmp/qrypt-"

if [ ${#patterns[@]} -eq 0 ]; then
  echo "check-paths: no machine-specific path patterns; nothing to check"
  exit 0
fi

# Example paths that are intentionally part of the public documentation and CI.
allow_tokens=(
  "/Users/me" "/Users/user" "/Users/someone" "/Users/example"
  "/home/me" "/home/user" "/home/someone" "/home/example"
  "/Volumes/" "/tmp/qrypt-"
)

failures=0

scan_stream() {
  local location="$1" pattern="$2" line cleaned token
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    cleaned="$line"
    for token in "${allow_tokens[@]}"; do
      cleaned="${cleaned//$token/<allowed>}"
    done
    case "$cleaned" in
      *"$pattern"*)
        failures=$((failures + 1))
        printf 'check-paths: %s contains %s\n  %s\n' "$location" "$pattern" "$line" >&2
        ;;
    esac
  done
}

scan_tree() {
  local pattern
  for pattern in "${patterns[@]}"; do
    while IFS= read -r hit; do
      scan_stream "tracked file" "$pattern" <<<"$hit"
    done < <(git grep -n -I -F -e "$pattern" -- . 2>/dev/null || true)
  done
}

scan_commit() {
  local commit="$1" pattern
  for pattern in "${patterns[@]}"; do
    while IFS= read -r hit; do
      scan_stream "commit ${commit:0:12}" "$pattern" <<<"$hit"
    done < <(git grep -n -I -F -e "$pattern" "$commit" -- . 2>/dev/null || true)
  done
}

if [ "$mode_tree" = true ]; then
  scan_tree
fi

if [ -n "$mode_range" ]; then
  if ! git rev-list --max-count=1 "$mode_range" >/dev/null 2>&1; then
    echo "check-paths: invalid range: $mode_range" >&2
    exit 2
  fi
  while IFS= read -r commit; do
    [ -n "$commit" ] || continue
    scan_commit "$commit"
  done < <(git rev-list "$mode_range")
fi

if [ "$mode_refs" = true ]; then
  while IFS= read -r commit; do
    [ -n "$commit" ] || continue
    scan_commit "$commit"
  done < <(git rev-list --all)
fi

if [ "$failures" -gt 0 ]; then
  echo "check-paths: $failures machine-specific path occurrence(s); scrub them with scripts/dev/scrub-paths.sh" >&2
  exit 1
fi
echo "check-paths: no machine-specific paths"
