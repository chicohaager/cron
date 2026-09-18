#!/usr/bin/env python3
"""Fail when a translation key is missing in any language, or when the UI
references a key no language defines.

Reads i18n.js (a plain object literal), index.html (data-i18n* attributes)
and app.js (t('...') calls). Prints the offending KEYS, not just counts —
a number difference is not a finding until the elements are named.
"""
import json
import re
import sys
from pathlib import Path

WEB = Path(__file__).resolve().parent.parent / "raw/usr/share/casaos/www/modules/cron"


def load_i18n():
    src = (WEB / "i18n.js").read_text(encoding="utf-8")
    langs = {}
    for m in re.finditer(r"^\s{2}([a-z]{2}): \{(.*?)^\s{2}\},", src, re.S | re.M):
        lang, body = m.group(1), m.group(2)
        keys = re.findall(r"^\s*'([^']+)':", body, re.M)
        langs[lang] = keys
    return langs


def used_keys():
    html = (WEB / "index.html").read_text(encoding="utf-8")
    js = (WEB / "app.js").read_text(encoding="utf-8")
    keys = set(re.findall(r'data-i18n(?:-ph|-title)?="([^"]+)"', html))
    keys |= set(re.findall(r"\bt\('([^']+)'", js))
    return keys


def main():
    langs = load_i18n()
    if len(langs) < 2:
        print(f"i18n.js: only {len(langs)} language block(s) parsed — parser broken?")
        return 1
    problems = 0
    reference = set(langs["en"])
    for lang, keys in langs.items():
        dup = {k for k in keys if keys.count(k) > 1}
        missing = reference - set(keys)
        extra = set(keys) - reference
        if dup or missing or extra:
            problems += 1
            print(f"[{lang}] missing={sorted(missing)} extra={sorted(extra)} duplicate={sorted(dup)}")
    used = used_keys()
    dynamic_prefixes = ("result.", "error.", "tpl.", "webhookFormat.", "schedule.")
    unknown = {k for k in used if k not in reference and not k.startswith(dynamic_prefixes)}
    if unknown:
        problems += 1
        print(f"[ui] keys used but not translated: {sorted(unknown)}")
    print(f"languages: {', '.join(f'{l}={len(k)}' for l, k in langs.items())}; keys used in UI: {len(used)}")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
