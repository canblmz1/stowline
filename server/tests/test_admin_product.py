from __future__ import annotations

from pathlib import Path

from app.version import agent_pin
from test_api import client, login  # noqa: F401  — reuse seeded control-plane client


def test_catalog_and_department_enrollment(client):
    login(client)
    cat = client.get("/api/v1/admin/catalog").json()
    assert "Finance" in cat["departments"]
    assert cat["qualified_agent_sha"] == agent_pin()[0]
    assert "forget" in cat["forbidden_admin_actions"]
    tok = client.post(
        "/api/v1/admin/enrollment-tokens",
        json={"label": "finance-1", "site_id": "hq", "department": "Finance"},
    ).json()
    assert tok["site_id"] == "hq"
    assert tok["department"] == "Finance"
    enrolled = client.post(
        "/api/v1/agent/enrollments",
        json={
            "token": tok["token"],
            "hostname": "finance-pc",
            "agent_version": "0.2.4-full-pilot",
            "capabilities": {"agent_sha": "63253a4d3b9e1addbda8a8473ba645e934c4dd23dabe31ab85f92e409fcbc7a4"},
        },
    ).json()
    assert enrolled["site_id"] == "hq"
    assert enrolled["department"] == "Finance"
    detail = client.get(f"/api/v1/admin/devices/{enrolled['device_id']}").json()
    assert detail["device"]["department"] == "Finance"
    assert detail["device"]["agent_sha"].startswith("63253a4d")
    assert detail["status"]["traffic_light"] in {"RED", "YELLOW"}
    assert "last_error_human" in detail["status"]


def test_selection_schedule_pause_and_forbidden(client, tmp_path: Path, monkeypatch):
    login(client)
    tok = client.post(
        "/api/v1/admin/enrollment-tokens",
        json={"label": "sel", "site_id": "hq", "department": "IT"},
    ).json()["token"]
    enrolled = client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": "it-pc"}).json()
    did = enrolled["device_id"]
    doc = tmp_path / "teklif.pdf"
    doc.write_bytes(b"pdf")
    pst = tmp_path / "mail.pst"
    pst.write_bytes(b"pst")
    ost = tmp_path / "cache.ost"
    ost.write_bytes(b"ost")
    env = tmp_path / ".env"
    env.write_text("x=1", encoding="utf-8")
    bad = client.post(
        f"/api/v1/admin/devices/{did}/selection",
        json={"selected": [{"path": str(env), "kind": "file", "category": "sensitive"}], "preferred_start_hhmm": "14:00"},
    )
    assert bad.status_code == 422
    ok = client.post(
        f"/api/v1/admin/devices/{did}/selection",
        json={
            "preferred_start_hhmm": "14:00",
            "applied_locally": True,
            "selected": [
                {"path": str(doc), "kind": "file", "category": "office_files"},
                {"path": str(pst), "kind": "file", "category": "outlook_pst"},
                {"path": str(ost), "kind": "file", "category": "outlook_ost"},
            ],
        },
    )
    assert ok.status_code == 200, ok.text
    man = ok.json()["manifest"]
    assert any(p.endswith("teklif.pdf") for p in man["source_roots"])
    assert not any(p.endswith(".ost") for p in man["source_roots"])
    patched = client.patch(f"/api/v1/admin/devices/{did}", json={"preferred_start_hhmm": "15:00"}).json()
    assert patched["device"]["preferred_start_hhmm"] == "15:00"
    late = client.patch(f"/api/v1/admin/devices/{did}", json={"preferred_start_hhmm": "17:50"})
    assert late.status_code == 422
    client.post(f"/api/v1/admin/devices/{did}/pause")
    paused = client.get(f"/api/v1/admin/devices/{did}").json()
    assert paused["device"]["pause_new_backups"] is True
    client.post(f"/api/v1/admin/devices/{did}/resume")
    for kind in ("FORGET", "PRUNE", "UNLOCK", "REPAIR", "EXEC"):
        r = client.post(f"/api/v1/admin/devices/{did}/commands", json={"kind": kind, "payload": {}})
        assert r.status_code == 422
    from app import schedule_policy

    # a site may pin its upload limit in the sites file instead of measuring
    monkeypatch.setitem(schedule_policy.SITE_SCHEDULE["hq"], "limit_upload_kib", 878)
    est = client.post(
        "/api/v1/admin/estimate",
        json={"site_id": "hq", "preferred_start_hhmm": "12:00", "logical_bytes": 878 * 1024 * 120},
    ).json()
    assert est["limit_upload_kib"] == 878
    assert est["estimated_seconds"] >= 120
    zero = client.post("/api/v1/admin/estimate", json={"site_id": "branch", "preferred_start_hhmm": "12:00", "logical_bytes": 10})
    assert zero.status_code == 422


def test_offline_device_shows_offline_not_a_stale_attempt_error(client):
    """Regression: a device that is offline RIGHT NOW must show "PC is
    offline", never a disk/network error_class from a past attempt. The
    admin detail view previously recomputed last_error_human a second time
    with offline/service_stopped hardcoded to False whenever any past
    attempt had an error_class, discarding the correct live context."""
    from datetime import timedelta

    from app.db import BackupAttempt, BackupJob, Device, utcnow
    from app.main import SessionLocal

    login(client)
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": "offline-test"}).json()["token"]
    d = client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": "offline-pc"}).json()
    device_id = d["device_id"]

    db = SessionLocal()
    try:
        dev = db.get(Device, device_id)
        dev.last_seen_at = utcnow() - timedelta(days=3)  # long past the offline threshold
        # last_attempt_outcome deliberately left unset: compute_health treats
        # last_attempt_outcome=="FAILED" as a more specific signal than a
        # stale last_seen_at and would report health="FAILED" instead of
        # "OFFLINE" — this test is specifically about the offline case.
        job = BackupJob(id="job-offline-test", device_id=device_id, state="FAILED")
        db.add(job)
        db.flush()
        att = BackupAttempt(
            id="att-offline-test",
            job_id=job.id,
            outcome="FAILED",
            error_class="DISK",  # a real, specific, but STALE reason
            ended_at=utcnow() - timedelta(days=3),
        )
        db.add(att)
        db.commit()
    finally:
        db.close()

    detail = client.get(f"/api/v1/admin/devices/{device_id}").json()
    human = detail["status"]["last_error_human"]
    assert human["code"] == "PC_OFFLINE", human
    assert "çevrimdışı" in human["title"].lower()
