"""Gateway entrypoint for a systemd or container deployment. Never prints secret values.

Environment:
  STOWLINE_GATEWAY_DATA      data folder (default /data/stowline-gateway)
  RCLONE_CONFIG              rclone.conf path (default <data>/rclone.conf)
  STOWLINE_RCLONE_CONF_B64   optional: base64 of an (encrypted) rclone.conf,
                             written to RCLONE_CONFIG once if it is missing
  STOWLINE_GATEWAY_REGISTRY  device registry path (default <data>/registry.json)
  PORT                       optional port override (PaaS convention)
plus everything gw.run() reads (STOWLINE_GATEWAY_ROOT, _ADMIN_TOKEN, ...).
"""

from __future__ import annotations

import base64
import os
import sys
from pathlib import Path


def main() -> None:
    data = Path(os.environ.get("STOWLINE_GATEWAY_DATA", "/data/stowline-gateway"))
    data.mkdir(parents=True, exist_ok=True)
    conf = Path(os.environ.get("RCLONE_CONFIG") or (data / "rclone.conf"))
    os.environ["RCLONE_CONFIG"] = str(conf)
    os.environ.setdefault("STOWLINE_GATEWAY_REGISTRY", str(data / "registry.json"))
    if os.environ.get("PORT"):
        os.environ["STOWLINE_GATEWAY_PORT"] = os.environ["PORT"]

    b64 = (os.environ.get("STOWLINE_RCLONE_CONF_B64") or "").strip()
    if b64 and not conf.is_file():
        conf.write_bytes(base64.b64decode(b64))
        print("RCLONE_CONF_BOOTSTRAP=written", flush=True)
    root = os.environ.get("STOWLINE_GATEWAY_ROOT", "")
    if root.startswith("rclone:") and (not conf.is_file() or conf.stat().st_size < 32):
        raise SystemExit(f"STOWLINE_GATEWAY_ROOT uses rclone but {conf} is missing")

    sys.path.insert(0, str(Path(__file__).resolve().parent))
    from gw import run

    run()


if __name__ == "__main__":
    main()
