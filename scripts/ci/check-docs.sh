#!/usr/bin/env bash
# Validate documentation hygiene that the per-file generators cannot see:
# relative links resolve, the generators stay stable, and the diff has no
# whitespace errors.
set -euo pipefail
cd "$(dirname "$0")/../.."

python3 scripts/schema/test-gen-docs.py

python3 - <<'PY'
import pathlib
import re
import sys

ROOTS = [pathlib.Path("docs"), pathlib.Path(".agents/skills")]
ROOT_FILES = sorted(pathlib.Path(".").glob("*.md"))
LINK = re.compile(r"\[[^\]]*\]\(([^)\s]+)\)")
SKIP_PREFIXES = ("http://", "https://", "mailto:", "#", "{{")

files = [p for root in ROOTS if root.exists() for p in root.rglob("*.md")]
files += [p for p in ROOT_FILES if p.exists()]
missing = []
for path in sorted(files):
    text = path.read_text(encoding="utf-8", errors="replace")
    for target in LINK.findall(text):
        if target.startswith(SKIP_PREFIXES):
            continue
        link = target.split("#", 1)[0]
        if not link:
            continue
        if not (path.parent / link).resolve().exists():
            missing.append(f"{path}: {target}")
if missing:
    print("broken relative links:", file=sys.stderr)
    for row in missing[:50]:
        print(f"  {row}", file=sys.stderr)
    sys.exit(1)
print(f"checked relative links in {len(files)} markdown files")
PY

if git rev-parse --git-dir >/dev/null 2>&1; then
  git diff --check
fi

echo "== docs check passed =="
