#!/usr/bin/env python3
"""Check CLI translation catalog completeness."""

import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent.parent
CATALOG_DIR = ROOT / "schema" / "i18n"


def load(lang):
    path = CATALOG_DIR / f"cli.{lang}.json"
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except Exception as err:
        raise SystemExit(f"{path}: {err}")


def main():
    en = load("en")
    zh = load("zh-CN")
    errors = []
    for key in sorted(set(en) - set(zh)):
        errors.append(f"zh-CN missing key: {key}")
    for key in sorted(set(zh) - set(en)):
        errors.append(f"en missing key: {key}")
    for lang, messages in (("en", en), ("zh-CN", zh)):
        for key, value in messages.items():
            if not isinstance(value, str) or value == "":
                errors.append(f"{lang} empty value: {key}")

    if errors:
        for error in errors:
            print(f"cli i18n: {error}", file=sys.stderr)
        return 1
    print(f"cli i18n ok: {len(en)} keys")
    return 0


if __name__ == "__main__":
    sys.exit(main())
