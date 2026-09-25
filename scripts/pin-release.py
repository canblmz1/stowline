"""Publish an agent build for automatic upgrade.

    python scripts/pin-release.py [--agent path\\to\\stowline-agent.exe] [--qualify]

Copies the agent to release/bin/stowline-agent.exe and writes its SHA-256
into release/manifest.json. Deploy the server afterwards: every enrolled
computer then compares its own agent with this pin on each heartbeat.

Only --qualify turns automatic upgrade on (agent_qualified: true). Test the
build on one real computer first; an unqualified pin is published but never
installed anywhere.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import shutil
from pathlib import Path

REPO = Path(__file__).resolve().parents[1]
MANIFEST = REPO / "release" / "manifest.json"
STAGED = REPO / "release" / "bin" / "stowline-agent.exe"


def sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--agent", default="", help="agent to publish (default: the one already in release/bin/)")
    ap.add_argument("--qualify", action="store_true", help="let enrolled computers upgrade to it")
    args = ap.parse_args()
    if args.agent:
        STAGED.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(args.agent, STAGED)
    if not STAGED.is_file():
        raise SystemExit(f"no agent at {STAGED}; pass --agent")
    data = json.loads(MANIFEST.read_text(encoding="utf-8"))
    data["agent_sha256"] = sha256(STAGED)
    data["agent_qualified"] = bool(args.qualify)
    MANIFEST.write_text(json.dumps(data, indent=2) + "\n", encoding="utf-8", newline="\n")
    print(f"agent_sha256={data['agent_sha256']} agent_qualified={data['agent_qualified']}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
