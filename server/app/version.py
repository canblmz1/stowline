"""Single runtime version label for operator-facing control-plane APIs.

Reads release/manifest.json (the repo's single release source of truth,
see that file's own "notes" field) when it is present next to this
package's deployment root; falls back to a literal so the server still
starts if the manifest was not shipped into this specific deployment.
"""

from __future__ import annotations

import json
import os
from pathlib import Path

_FALLBACK_VERSION = "0.1.1"


def _release_dirs() -> list[Path]:
    """STOWLINE_RELEASE_DIR first: an installed (non-editable) package lives in
    site-packages, where neither path relative to this file reaches the
    release directory. Confirmed live on a systemd install: the heartbeat sent an
    empty pin with agent_qualified false, so no agent ever upgraded."""
    dirs = []
    env = os.environ.get("STOWLINE_RELEASE_DIR", "").strip()
    if env:
        dirs.append(Path(env))
    dirs += [Path(__file__).resolve().parent.parent / "release", Path(__file__).resolve().parents[2] / "release"]
    return dirs


def _manifest() -> dict:
    for candidate in (d / "manifest.json" for d in _release_dirs()):
        try:
            return json.loads(candidate.read_text(encoding="utf-8"))
        except (OSError, ValueError):
            continue
    return {}


def _load_release_version() -> str:
    version = _manifest().get("version")
    return str(version) if version else _FALLBACK_VERSION


RELEASE_VERSION = _load_release_version()


def agent_binary_path() -> Path:
    """Where the current qualified agent binary lives in this deployment,
    for the auto-upgrade download endpoint to serve. Mirrors manifest.json's
    own dual candidate roots (deployment root vs. repo root under tests)."""
    for candidate in (d / "bin" / "stowline-agent.exe" for d in _release_dirs()):
        if candidate.exists():
            return candidate
    return Path(__file__).resolve().parents[2] / "release" / "bin" / "stowline-agent.exe"


def agent_pin() -> tuple[str, bool]:
    """The agent SHA-256/qualified flag this control plane currently wants
    endpoints running, read fresh from release/manifest.json every call so
    a qualification flip (false -> true) takes effect without a redeploy of
    this module's import-time cache. Used by heartbeat() (agent-side
    upgrade check) and the binary-download endpoint (which must refuse to
    serve an unqualified build)."""
    data = _manifest()
    sha = str(data.get("agent_sha256") or "")
    qualified = bool(data.get("agent_qualified"))
    return sha, qualified


def canary_pin(device_id: str) -> tuple[str, Path] | None:
    """A new agent build tried on a few named computers before everyone gets
    it. manifest.json "canary": {"agent_sha256": ..., "device_ids": [...]},
    binary at release/bin/stowline-agent-canary.exe. Read fresh every call, so
    adding or removing a computer needs no redeploy. None = not a canary."""
    c = _manifest().get("canary") or {}
    sha = str(c.get("agent_sha256") or "").lower()
    ids = [str(x) for x in (c.get("device_ids") or [])]
    if len(sha) != 64 or not device_id or device_id not in ids:
        return None
    for d in _release_dirs():
        path = d / "bin" / "stowline-agent-canary.exe"
        if path.exists():
            return sha, path
    return None
