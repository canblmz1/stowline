"""Server side of the automatic agent upgrade (Phase 2, see
docs/superpowers/specs/2026-09-22-ops-and-desktop-plan.md): heartbeat
exposes the current qualified agent SHA-256, and the binary download
endpoint refuses to serve anything that doesn't verifiably match it.
"""
from __future__ import annotations

import hashlib

from test_api import _enroll, client, login  # noqa: F401

from app.version import RELEASE_VERSION, agent_binary_path, agent_pin  # noqa: E402


def test_agent_pin_matches_the_committed_manifest():
    # qualified legitimately flips false/true across a release's own
    # lifecycle (unqualified while a build awaits its real-device test,
    # true once it passes) -- this only pins the shape, never a specific
    # value, so it does not need updating every time that flag changes.
    sha, qualified = agent_pin()
    assert sha == "" or len(sha) == 64  # empty until scripts/pin-release.py publishes an agent
    assert isinstance(qualified, bool)
    assert RELEASE_VERSION == "0.1.1"


def test_heartbeat_exposes_the_current_agent_pin(client):
    login(client)
    d = _enroll(client, "upgrade-hb-01", "branch")
    h = {"Authorization": f"Bearer {d['control_credential']}"}
    hb = client.post("/api/v1/agent/heartbeat", json={"sequence": 1}, headers=h).json()
    sha, qualified = agent_pin()
    assert hb["agent_sha256"] == sha
    assert hb["agent_qualified"] == qualified


def test_binary_download_refuses_when_unqualified(client, monkeypatch):
    import app.services as services

    login(client)
    d = _enroll(client, "upgrade-bin-01", "branch")
    h = {"Authorization": f"Bearer {d['control_credential']}"}
    monkeypatch.setattr(services, "agent_pin", lambda: ("a" * 64, False))
    r = client.get("/api/v1/agent/binary", headers=h)
    assert r.status_code == 503


def test_binary_download_refuses_when_the_staged_file_is_missing(client, monkeypatch):
    import app.services as services
    from pathlib import Path

    login(client)
    d = _enroll(client, "upgrade-bin-02", "branch")
    h = {"Authorization": f"Bearer {d['control_credential']}"}
    monkeypatch.setattr(services, "agent_pin", lambda: ("a" * 64, True))
    monkeypatch.setattr(services, "agent_binary_path", lambda: Path("/nonexistent/stowline-agent.exe"))
    r = client.get("/api/v1/agent/binary", headers=h)
    assert r.status_code == 503


def test_binary_download_refuses_when_the_staged_file_does_not_match_the_pin(client, monkeypatch, tmp_path):
    import app.services as services

    login(client)
    d = _enroll(client, "upgrade-bin-03", "branch")
    h = {"Authorization": f"Bearer {d['control_credential']}"}
    bogus = tmp_path / "stowline-agent.exe"
    bogus.write_bytes(b"not the real binary")
    monkeypatch.setattr(services, "agent_pin", lambda: ("a" * 64, True))
    monkeypatch.setattr(services, "agent_binary_path", lambda: bogus)
    r = client.get("/api/v1/agent/binary", headers=h)
    assert r.status_code == 503


def test_binary_download_serves_the_exact_bytes_when_everything_matches(client, monkeypatch, tmp_path):
    import app.services as services

    login(client)
    d = _enroll(client, "upgrade-bin-04", "branch")
    h = {"Authorization": f"Bearer {d['control_credential']}"}
    payload = b"pretend this is a real stowline-agent.exe"
    real = tmp_path / "stowline-agent.exe"
    real.write_bytes(payload)
    sha = hashlib.sha256(payload).hexdigest()
    monkeypatch.setattr(services, "agent_pin", lambda: (sha, True))
    monkeypatch.setattr(services, "agent_binary_path", lambda: real)
    r = client.get("/api/v1/agent/binary", headers=h)
    assert r.status_code == 200
    assert r.content == payload


def test_binary_download_requires_agent_auth(client):
    assert client.get("/api/v1/agent/binary").status_code == 401


def test_heartbeat_updates_the_devices_reported_agent_sha(client):
    login(client)
    d = _enroll(client, "upgrade-hb-sha", "branch")
    h = {"Authorization": f"Bearer {d['control_credential']}"}
    sha = "ab" * 32
    assert client.post("/api/v1/agent/heartbeat", json={"sequence": 1, "agent_sha256": sha}, headers=h).status_code == 200
    detail = client.get(f"/api/v1/admin/devices/{d['device_id']}").json()
    assert detail["device"]["agent_sha"] == sha


def test_heartbeat_ignores_a_malformed_agent_sha(client):
    login(client)
    d = _enroll(client, "upgrade-hb-badsha", "branch")
    h = {"Authorization": f"Bearer {d['control_credential']}"}
    before = client.get(f"/api/v1/admin/devices/{d['device_id']}").json()["device"]["agent_sha"]
    client.post("/api/v1/agent/heartbeat", json={"sequence": 1, "agent_sha256": "not-a-sha"}, headers=h)
    after = client.get(f"/api/v1/admin/devices/{d['device_id']}").json()["device"]["agent_sha"]
    assert after == before


def test_old_agents_without_agent_sha_are_still_accepted(client):
    login(client)
    d = _enroll(client, "upgrade-hb-oldagent", "branch")
    h = {"Authorization": f"Bearer {d['control_credential']}"}
    assert client.post("/api/v1/agent/heartbeat", json={"sequence": 1}, headers=h).status_code == 200


def test_release_dir_env_wins_for_an_installed_package(tmp_path, monkeypatch):
    """An installed package lives in site-packages, where the relative
    candidates never reach release/: the pin came back ('', False) live."""
    import json as _json

    from app import version as v

    rel = tmp_path / "release"
    (rel / "bin").mkdir(parents=True)
    (rel / "manifest.json").write_text(_json.dumps({"agent_sha256": "a" * 64, "agent_qualified": True}), encoding="utf-8")
    (rel / "bin" / "stowline-agent.exe").write_bytes(b"x")
    monkeypatch.setenv("STOWLINE_RELEASE_DIR", str(rel))
    assert v.agent_pin() == ("a" * 64, True)
    assert v.agent_binary_path() == rel / "bin" / "stowline-agent.exe"


def test_byte_and_file_count_columns_are_64_bit():
    """Live: a 2 GB+ backup's lease renewal hit Postgres "integer out of
    range" on progress_bytes_done; the lease expired mid-backup."""
    from sqlalchemy import BigInteger

    from app.db import Base
    from app.migrate import BIGINT_COLUMNS

    for table, column in BIGINT_COLUMNS:
        assert isinstance(Base.metadata.tables[table].c[column].type, BigInteger), f"{table}.{column}"


def test_worker_resends_every_measured_site_limit_to_the_gateway(monkeypatch):
    """The gateway keeps site caps only in memory; after a restart it had
    none, so the worker now re-sends them on a timer."""
    from test_api import client as _c  # noqa: F401  (test env already set up by the import at the top)

    from app import services, worker
    from app.db import Site
    from app.main import SessionLocal

    sent = []
    monkeypatch.setattr(services, "notify_gateway_site_limit", lambda site_id, bps: sent.append((site_id, bps)))
    with SessionLocal() as db:
        for s in db.query(Site).all():
            s.measured_upload_mbps = 100
            s.business_backup_budget_percent = 40
        db.commit()
        assert worker.push_site_limits(db) >= 2
    assert {"hq", "branch"} <= {s for s, _ in sent}
    assert all(bps > 0 for _, bps in sent)


def test_a_canary_computer_gets_the_canary_build_and_others_keep_the_pin(client, monkeypatch, tmp_path):
    import app.services as services

    login(client)
    canary = _enroll(client, "upgrade-canary-01", "branch")
    other = _enroll(client, "upgrade-canary-02", "branch")
    exe = tmp_path / "stowline-agent-canary.exe"
    exe.write_bytes(b"canary build")
    sha = hashlib.sha256(exe.read_bytes()).hexdigest()
    monkeypatch.setattr(services, "agent_pin", lambda: ("b" * 64, True))
    monkeypatch.setattr(services, "canary_pin", lambda device_id: (sha, exe) if device_id == canary["device_id"] else None)

    def hb(d):
        return client.post("/api/v1/agent/heartbeat", json={"sequence": 1}, headers={"Authorization": f"Bearer {d['control_credential']}"}).json()

    assert (hb(canary)["agent_sha256"], hb(canary)["agent_qualified"]) == (sha, True)
    assert hb(other)["agent_sha256"] == "b" * 64
    got = client.get("/api/v1/agent/binary", headers={"Authorization": f"Bearer {canary['control_credential']}"})
    assert got.status_code == 200 and got.content == b"canary build"


def test_a_canary_binary_that_does_not_match_its_pin_is_refused(client, monkeypatch, tmp_path):
    import app.services as services

    login(client)
    canary = _enroll(client, "upgrade-canary-03", "branch")
    exe = tmp_path / "stowline-agent-canary.exe"
    exe.write_bytes(b"tampered")
    monkeypatch.setattr(services, "canary_pin", lambda device_id: ("c" * 64, exe))
    got = client.get("/api/v1/agent/binary", headers={"Authorization": f"Bearer {canary['control_credential']}"})
    assert got.status_code == 503


def test_canary_pin_reads_the_manifest(monkeypatch, tmp_path):
    import json

    import app.version as v

    (tmp_path / "bin").mkdir()
    (tmp_path / "bin" / "stowline-agent-canary.exe").write_bytes(b"x")
    (tmp_path / "manifest.json").write_text(json.dumps({"canary": {"agent_sha256": "D" * 64, "device_ids": ["dev-1"]}}), encoding="utf-8")
    monkeypatch.setenv("STOWLINE_RELEASE_DIR", str(tmp_path))
    assert v.canary_pin("dev-1") == ("d" * 64, tmp_path / "bin" / "stowline-agent-canary.exe")
    assert v.canary_pin("dev-2") is None
    assert v.canary_pin("") is None
