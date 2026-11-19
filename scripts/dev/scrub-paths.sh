#!/usr/bin/env bash
# Scrub machine-specific absolute paths from tracked files, every commit, and
# every ref, then optionally force-push the rewritten refs.
#
# The gate in scripts/ci/check-paths.sh should stop new leaks; this script is the
# recovery path for history that already contains them. It rewrites public
# history, so it always writes a bundle backup first and prints the force-push
# commands it would run.
#
# Usage:
#   scripts/dev/scrub-paths.sh --dry-run     # scan only, no rewrite
#   scripts/dev/scrub-paths.sh               # rewrite locally, print push plan
#   scripts/dev/scrub-paths.sh --push        # rewrite and force-push changed refs
#   scripts/dev/scrub-paths.sh --no-backup   # skip the bundle (not recommended)
#
# After a push, collaborators must re-fetch; GitHub keeps refs/pull/* and cached
# objects addressable by SHA, so old commits can stay reachable that way.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

push=false
backup=true
dry_run=false
while [ $# -gt 0 ]; do
  case "$1" in
    --push) push=true ;;
    --no-backup) backup=false ;;
    --dry-run) dry_run=true ;;
    -h|--help)
      sed -n '2,22p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *)
      echo "scrub-paths: unknown argument: $1" >&2
      exit 2
      ;;
  esac
  shift
done

home_dir="${HOME:-}"
user_name="$(id -un 2>/dev/null || whoami 2>/dev/null || true)"
if [ -z "$home_dir" ] || [ -z "$user_name" ]; then
  echo "scrub-paths: HOME and user name are required" >&2
  exit 2
fi

if [ "$dry_run" = true ]; then
  scripts/ci/check-paths.sh --refs || true
  exit 0
fi

stamp="$(date +%Y%m%d-%H%M%S)"
workdir="${TMPDIR:-/tmp}/qrypt-scrub-$stamp"
bundle_dir="${TMPDIR:-/tmp}"
mkdir -p "$workdir"
trap 'rm -rf "$workdir"' EXIT

scrub_py="$workdir/scrub.py"
refs_before="$workdir/refs-before.txt"
git for-each-ref --format='%(refname) %(objectname)' >"$refs_before"

# The replacement table is machine specific and deliberately narrow: the
# operator's home, the repository checkout under it, and the resolved macOS
# temp prefix used by probe runs. Generic documentation paths must stay.
cat >"$scrub_py" <<PY
import os

home = ${home_dir@Q}
replacements = [
    (home + "/Code/github/qrypt-org/qrypt", "\$REPO_ROOT"),
    (home, "/home/user"),
    ("/private/tmp/qrypt-", "/tmp/qrypt-"),
]

for root, dirs, files in os.walk(b"."):
    if b".git" in dirs:
        dirs.remove(b".git")
    for name in files:
        path = os.path.join(root, name)
        if not os.path.isfile(path) or os.path.islink(path):
            continue
        try:
            with open(path, "rb") as fh:
                data = fh.read()
        except OSError:
            continue
        new = data
        for old, repl in replacements:
            new = new.replace(old.encode(), repl.encode())
        if new != data:
            with open(path, "wb") as fh:
                fh.write(new)
PY

if [ "$backup" = true ]; then
  bundle="$bundle_dir/qrypt-pre-scrub-$stamp.bundle"
  git bundle create "$bundle" --all >/dev/null
  echo "scrub-paths: backup bundle: $bundle ($(du -h "$bundle" | cut -f1))"
fi

echo "scrub-paths: rewriting all refs with a tree filter"
FILTER_BRANCH_SQUELCH_WARNING=1 git filter-branch -f --tree-filter "python3 '$scrub_py'" --tag-name-filter cat -- --all >/dev/null

git for-each-ref refs/original --format='delete %(refname)' | git update-ref --stdin
git reflog expire --expire=now --all
git gc --prune=now --quiet

echo "scrub-paths: verifying rewritten refs"
scripts/ci/check-paths.sh --tree --refs

git for-each-ref --format='%(refname) %(objectname)' >"$workdir/refs-after.txt"
changed="$(join -j 1 <(sort "$refs_before") <(sort "$workdir/refs-after.txt") | awk '$2 != $3 {print $1}')"
if [ -z "$changed" ]; then
  echo "scrub-paths: no refs changed"
  exit 0
fi
echo "scrub-paths: changed refs:"
# shellcheck disable=SC2001
echo "$changed" | sed 's/^/  /'

changed_branches=""
changed_tags=""
while IFS= read -r ref; do
  [ -n "$ref" ] || continue
  case "$ref" in
    refs/heads/*)
      remote="$(git ls-remote --heads origin "$ref" | awk '{print $1}')"
      [ -n "$remote" ] && changed_branches+=" ${ref#refs/heads/}"
      ;;
    refs/tags/*)
      remote="$(git ls-remote --tags origin "$ref" | awk '{print $1}')"
      [ -n "$remote" ] && changed_tags+=" ${ref#refs/tags/}"
      ;;
  esac
done <<<"$changed"

if [ -z "$changed_branches" ] && [ -z "$changed_tags" ]; then
  echo "scrub-paths: no remote refs need updating"
  exit 0
fi

echo "scrub-paths: remote refs to force-push:"
[ -n "$changed_branches" ] && echo "  branches:$changed_branches"
[ -n "$changed_tags" ] && echo "  tags:$changed_tags"

if [ "$push" = false ]; then
  echo "scrub-paths: re-run with --push (or push manually) to update origin"
  exit 0
fi

# The rewritten refs were just verified, and the pre-push hook would re-run the
# whole local CI for main and for every tag; skip it explicitly.
if [ -n "$changed_branches" ]; then
  # shellcheck disable=SC2086
  git push --no-verify --force origin $changed_branches
fi
if [ -n "$changed_tags" ]; then
  # shellcheck disable=SC2086
  git push --no-verify --force origin $changed_tags
fi
git fetch --all --prune --tags >/dev/null 2>&1 || true
echo "scrub-paths: done; collaborators must re-fetch and reset their branches"
