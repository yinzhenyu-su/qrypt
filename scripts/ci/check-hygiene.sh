#!/usr/bin/env bash
# Cheap repository hygiene gates that do not depend on the Go build or the
# test suites:
#
#   go mod tidy -diff   go.mod/go.sum are tidy (reports a diff, never rewrites)
#   actionlint          every workflow under .github/workflows parses and is
#                       consistent with the local composite actions it uses
#   ShellCheck          scripts/ci/*.sh and .githooks/* at the pinned version
#   git diff --check    no trailing whitespace / conflict markers in the change
#
# ShellCheck deliberately stops at scripts/ci and .githooks: scripts/install/*.sh
# are linted by scripts/install/check-install.sh, which also owns the installer
# Go tests. Keeping the two scopes disjoint avoids linting the same file twice.
#
# actionlint and shellcheck are pinned in tools.go and installed by
# scripts/ci/install-tools.sh into $QRYPT_TOOLS_DIR (~/.cache/qrypt-tools by
# default). ci.yaml and check-all.sh put that directory first on PATH; this
# script also falls back to it so it works when called directly.
#
# Environment:
#   QRYPT_TOOLS_DIR   tool cache to fall back to (default ~/.cache/qrypt-tools)
#   QRYPT_DIFF_BASE   revision to diff against for the whitespace check; CI
#                     passes the event's base commit. Unset = uncommitted work.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

TOOLS_DIR="${QRYPT_TOOLS_DIR:-${HOME}/.cache/qrypt-tools}"

need_tool() {
  local name="$1"
  command -v "$name" >/dev/null 2>&1 && return 0
  if [ -x "$TOOLS_DIR/$name" ]; then
    export PATH="$TOOLS_DIR:$PATH"
    return 0
  fi
  echo "check-hygiene: $name is required; run scripts/ci/install-tools.sh" >&2
  exit 1
}

need_tool actionlint
need_tool shellcheck

echo "== go mod tidy =="
# -diff is the non-mutating form: it prints the diff it would apply and exits
# non-zero when go.mod/go.sum need regeneration.
if ! tidy_diff="$(go mod tidy -diff)"; then
  printf '%s\n' "$tidy_diff"
  echo "go.mod/go.sum are not tidy; run 'go mod tidy' and commit the result" >&2
  exit 1
fi

echo "== actionlint =="
shopt -s nullglob
workflows=(.github/workflows/*.yaml .github/workflows/*.yml)
shopt -u nullglob
if [ "${#workflows[@]}" -eq 0 ]; then
  echo "check-hygiene: no workflow files found under .github/workflows" >&2
  exit 1
fi
actionlint "${workflows[@]}"

echo "== shellcheck =="
expected_shellcheck=$(sed -nE 's/^[[:space:]]*ShellcheckVersion[[:space:]]*=[[:space:]]*"([^"]+)".*/\1/p' tools.go)
actual_shellcheck=$(shellcheck --version | sed -nE 's/^version: ([^[:space:]]+).*/\1/p')
if [ -z "$expected_shellcheck" ] || [ "$actual_shellcheck" != "$expected_shellcheck" ]; then
  echo "check-hygiene: ShellCheck $expected_shellcheck is required; found ${actual_shellcheck:-unknown}" >&2
  echo "update ShellcheckVersion in tools.go together with CI when upgrading" >&2
  exit 1
fi
shopt -s nullglob
lint_scripts=(scripts/ci/*.sh .githooks/*)
shopt -u nullglob
if [ "${#lint_scripts[@]}" -eq 0 ]; then
  echo "check-hygiene: no CI scripts or git hooks found" >&2
  exit 1
fi
shellcheck "${lint_scripts[@]}"

echo "== whitespace and conflict markers =="
# In CI the checkout is a clean tree, where a bare `git diff --check` has
# nothing to inspect: diff against the event base instead. A shallow checkout
# may not contain that commit, so fetch just it and fall back honestly.
diff_scope=()
if [ -n "${QRYPT_DIFF_BASE:-}" ]; then
  if ! git rev-parse --verify -q "${QRYPT_DIFF_BASE}^{commit}" >/dev/null 2>&1; then
    git fetch --quiet --no-tags origin "$QRYPT_DIFF_BASE" >/dev/null 2>&1 || true
  fi
  if git rev-parse --verify -q "${QRYPT_DIFF_BASE}^{commit}" >/dev/null 2>&1; then
    diff_scope=("$QRYPT_DIFF_BASE" --)
  else
    echo "check-hygiene: cannot resolve QRYPT_DIFF_BASE=$QRYPT_DIFF_BASE; checking uncommitted changes only" >&2
  fi
fi
# --check reports trailing whitespace, space-before-tab and blank lines at EOF.
git diff --check "${diff_scope[@]}"
git diff --cached --check
# --check does not flag leftover merge-conflict markers (they are ordinary
# text), so scan the added lines of the same diff for them.
if conflict_lines="$(git diff -U0 "${diff_scope[@]}" | grep -E '^\+(<<<<<<< |=======$|>>>>>>> )' || true)" && [ -n "$conflict_lines" ]; then
  echo "leftover merge-conflict markers in the diff:" >&2
  printf '%s\n' "$conflict_lines" | sed 's/^+//' >&2
  exit 1
fi

echo "== repo hygiene checks passed =="
