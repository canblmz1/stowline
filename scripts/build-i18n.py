"""Build the UI language layer: i18n/i18n.template.js + i18n/tr-en.json ->
the i18n.js copy each UI serves. Run after editing either source file;
tests fail if a copy is stale.

    python scripts/build-i18n.py          # write the copies
    python scripts/build-i18n.py --check  # exit 1 if any copy is stale
"""
from __future__ import annotations

import json
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[1]
TEMPLATE = REPO / "i18n" / "i18n.template.js"
DICT = REPO / "i18n" / "tr-en.json"
TARGETS = [
    REPO / "server" / "app" / "static" / "js" / "i18n.js",  # admin panel
    REPO / "tools" / "stowline-setup" / "static" / "i18n.js",  # setup wizard
    REPO / "agent" / "cmd" / "stowline-agent" / "i18n.js",  # local user UI (embedded)
]


def render() -> str:
    data = json.loads(DICT.read_text(encoding="utf-8"))
    data = {k: v for k, v in data.items() if not k.startswith("_")}
    blob = json.dumps(data, ensure_ascii=False, separators=(",", ":"), sort_keys=True)
    return TEMPLATE.read_text(encoding="utf-8").replace("/*__DICT__*/ {}", blob)


def main() -> int:
    want = render()
    stale = [t for t in TARGETS if not t.is_file() or t.read_text(encoding="utf-8") != want]
    if "--check" in sys.argv:
        for t in stale:
            print(f"stale: {t.relative_to(REPO)}")
        return 1 if stale else 0
    for t in TARGETS:
        t.write_text(want, encoding="utf-8", newline="\n")
        print(f"wrote {t.relative_to(REPO)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
