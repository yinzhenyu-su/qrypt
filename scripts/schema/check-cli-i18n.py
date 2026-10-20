#!/usr/bin/env python3
"""Check CLI translation catalog completeness."""

import json
import re
import sys
from collections import Counter
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent.parent
CATALOG_DIR = ROOT / "schema" / "i18n"

FORMAT_VERB = re.compile(r"%(?:\[[0-9]+\])?[-+# 0]*[0-9]*(?:\.[0-9]+)?[a-zA-Z%]")


def load(lang):
    path = CATALOG_DIR / f"cli.{lang}.json"
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except Exception as err:
        raise SystemExit(f"{path}: {err}")


def format_verbs(value):
    return Counter(verb for verb in FORMAT_VERB.findall(value) if verb != "%%")


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
    translated = 0
    examples = 0
    for key, english in en.items():
        if key not in zh:
            continue
        chinese = zh[key]
        if key.endswith(".example"):
            examples += 1
            if chinese != english:
                errors.append(f"zh-CN example changed: {key}")
            continue
        if chinese == english:
            errors.append(f"zh-CN untranslated: {key}")
            continue
        translated += 1
        if format_verbs(chinese) != format_verbs(english):
            errors.append(
                "zh-CN format verbs differ: "
                f"{key} ({format_verbs(english)} != {format_verbs(chinese)})"
            )

    if errors:
        for error in errors:
            print(f"cli i18n: {error}", file=sys.stderr)
        return 1
    print(f"cli i18n ok: {len(en)} keys ({translated} translated, {examples} examples)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
