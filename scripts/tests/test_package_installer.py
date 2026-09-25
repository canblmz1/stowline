"""Packaging regression: the portable embedded Python on Windows ships with
no IANA tz database at all (unlike Linux, Windows has no OS-level fallback),
so zoneinfo.ZoneInfo(anything) — including "UTC" — raises
ZoneInfoNotFoundError until the `tzdata` package is installed into the
embedded runtime's site-packages. This broke the Setup Wizard's Schedule
screen ("No time zone found with key Europe/Istanbul") on a clean external
Windows PC with no system Python. See scripts/package-installer.py.
"""

from __future__ import annotations

import subprocess
from pathlib import Path

import pytest

REPO = Path(__file__).resolve().parents[2]
RUNTIME_PY = REPO / "releases" / "installer" / "payload" / "runtime" / "python.exe"

pytestmark = pytest.mark.skipif(
    not RUNTIME_PY.is_file(),
    reason="installer not built yet: run scripts/package-installer.py first",
)


def _resolve(key: str) -> subprocess.CompletedProcess:
    return subprocess.run(
        [str(RUNTIME_PY), "-c", f"from zoneinfo import ZoneInfo; ZoneInfo({key!r})"],
        capture_output=True,
        text=True,
        timeout=30,
    )


def test_embedded_runtime_resolves_europe_istanbul():
    """The site policy timezone (schedule_policy.SITE_SCHEDULE) must never
    be changed away from this IANA key — the runtime must support it."""
    proc = _resolve("Europe/Istanbul")
    assert proc.returncode == 0, proc.stderr
    assert "ZoneInfoNotFoundError" not in (proc.stderr or "")


def test_embedded_runtime_resolves_utc():
    """UTC gets no special-casing from zoneinfo: it fails exactly like any
    other key when tzdata is missing, so it is verified separately."""
    proc = _resolve("UTC")
    assert proc.returncode == 0, proc.stderr
    assert "ZoneInfoNotFoundError" not in (proc.stderr or "")


def test_embedded_runtime_has_tzdata_package():
    site_packages = RUNTIME_PY.parent / "Lib" / "site-packages"
    dist_info = list(site_packages.glob("tzdata-*.dist-info"))
    assert dist_info, f"tzdata package not found under {site_packages}"


def test_deploy_json_has_real_production_urls_not_blank():
    """Regression: deploy.json's control_plane_url/gateway_url used to
    have no authoritative source at all -- the only way a real build ever
    got real values was "carry forward the previous build's own
    deploy.json if it already happened to contain an https:// URL". Once
    that file was ever blanked for any reason (a bad build, releases/
    deleted and recreated, a manual edit), every subsequent rebuild
    silently perpetuated the blank forever, and a real install would
    reach the wizard's network preflight and fail with
    "control_plane_url is missing from deploy.json" -- confirmed live.
    package-installer.py must always write real URLs, not depend
    on whatever the last build happened to have lying around."""
    import json

    deploy_path = REPO / "releases" / "installer" / "deploy.json"
    assert deploy_path.is_file(), "deploy.json not built yet: run scripts/package-installer.py first"
    data = json.loads(deploy_path.read_text(encoding="utf-8"))
    # a generic package (no --server) leaves both out; the wizard asks
    if "control_plane_url" in data:
        assert data["control_plane_url"].startswith("https://"), data
        assert data.get("gateway_url", "").startswith("https://"), data
