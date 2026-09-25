"""Server side of the end-user local desktop client's self-service restore
(Phase 4, see docs/superpowers/specs/2026-09-22-ops-and-desktop-plan.md):
the restore itself always happens locally on the device; this endpoint is
audit-only, so an admin can see, in Olaylar, that it happened and where.
"""
from __future__ import annotations

from test_api import _enroll, client, login  # noqa: F401


def _headers(d: dict) -> dict:
    return {"Authorization": f"Bearer {d['control_credential']}"}


def test_self_service_restore_is_recorded_in_the_audit_trail(client):
    login(client)
    d = _enroll(client, "self-restore-01", "branch")
    r = client.post(
        "/api/v1/agent/self-service-restores",
        headers=_headers(d),
        json={"snapshot_id": "a" * 64, "selections": ["/C/Belgeler/rapor.docx"], "destination": r"C:\Stowline-Recovery\r1\rapor.docx"},
    )
    assert r.status_code == 200, r.text

    events = client.get("/api/v1/admin/audit-events").json()["items"]
    matches = [e for e in events if e["action"] == "self_service_restore" and e["device_id"] == d["device_id"]]
    assert len(matches) == 1


def test_self_service_restore_requires_agent_auth(client):
    r = client.post("/api/v1/agent/self-service-restores", json={"snapshot_id": "a" * 64, "selections": [], "destination": ""})
    assert r.status_code == 401


def test_self_service_restore_defaults_are_permissive_about_empty_selections(client):
    login(client)
    d = _enroll(client, "self-restore-02", "branch")
    r = client.post("/api/v1/agent/self-service-restores", headers=_headers(d), json={"snapshot_id": "b" * 64})
    assert r.status_code == 200, r.text


def test_self_service_selection_becomes_the_selection_the_admin_panel_shows(client):
    login(client)
    d = _enroll(client, "self-select-01", "branch")
    r = client.post(
        "/api/v1/agent/self-service-selection",
        headers=_headers(d),
        json={"source_roots": [r"C:\Users\ahmet\Belgeler", r"C:\Users\ahmet\Masaustu"]},
    )
    assert r.status_code == 200, r.text
    detail = client.get(f"/api/v1/admin/devices/{d['device_id']}").json()
    sel = detail["selection"]
    assert sel["manifest"]["source_roots"] == [r"C:\Users\ahmet\Belgeler", r"C:\Users\ahmet\Masaustu"]
    assert sel["manifest"]["source"] == "self_service"
    assert sel["applied_locally"] is True
    events = [e for e in client.get("/api/v1/admin/audit-events").json()["items"] if e["action"] == "selection_save" and e["device_id"] == d["device_id"]]
    assert events and events[0]["actor_type"] == "device"


def test_self_service_selection_rejects_an_empty_list(client):
    login(client)
    d = _enroll(client, "self-select-02", "branch")
    r = client.post("/api/v1/agent/self-service-selection", headers=_headers(d), json={"source_roots": []})
    assert r.status_code == 422


def test_self_service_selection_requires_agent_auth(client):
    assert client.post("/api/v1/agent/self-service-selection", json={"source_roots": [r"C:\Users\x"]}).status_code == 401


def _selection_versions(client, device_id: str) -> list[dict]:
    return client.get(f"/api/v1/admin/devices/{device_id}").json()["selection"]


def test_device_sync_records_what_the_pc_backs_up_when_the_server_knows_nothing(client):
    # Folders discovered at install land only in pilot.json; without this the
    # admin panel showed "0 klasör" for a PC that was backing up fine.
    login(client)
    d = _enroll(client, "sync-select-01", "branch")
    r = client.post("/api/v1/agent/self-service-selection", headers=_headers(d), json={"source_roots": [r"C:\Users\ali\Belgeler"], "source": "device_sync"})
    assert r.status_code == 200, r.text
    assert r.json()["recorded"] is True
    sel = _selection_versions(client, d["device_id"])
    assert sel["manifest"]["source_roots"] == [r"C:\Users\ali\Belgeler"]
    assert sel["manifest"]["source"] == "device_sync"
    assert sel["applied_locally"] is True


def test_device_sync_with_an_unchanged_list_does_not_create_a_new_version(client):
    login(client)
    d = _enroll(client, "sync-select-02", "branch")
    body = {"source_roots": [r"C:\Users\ali\Belgeler"], "source": "device_sync"}
    first = client.post("/api/v1/agent/self-service-selection", headers=_headers(d), json=body).json()
    again = client.post("/api/v1/agent/self-service-selection", headers=_headers(d), json=body).json()
    assert again["recorded"] is False
    assert again["version"] == first["version"]


def test_device_sync_never_overwrites_an_admin_selection_the_pc_has_not_applied_yet(client):
    from app.db import Device
    from app.main import SessionLocal
    from app import services

    login(client)
    d = _enroll(client, "sync-select-03", "branch")
    with SessionLocal() as db:
        dev = db.get(Device, d["device_id"])
        services.save_device_selection(db, None, dev, {"source_roots": [r"D:\Muhasebe"]}, applied_locally=False)
        db.commit()
    r = client.post("/api/v1/agent/self-service-selection", headers=_headers(d), json={"source_roots": [r"C:\Users\ali\Belgeler"], "source": "device_sync"})
    assert r.status_code == 200, r.text
    assert r.json()["recorded"] is False
    sel = _selection_versions(client, d["device_id"])
    assert sel["manifest"]["source_roots"] == [r"D:\Muhasebe"]
    assert sel["applied_locally"] is False


def test_selection_report_rejects_an_unknown_source(client):
    login(client)
    d = _enroll(client, "sync-select-04", "branch")
    r = client.post("/api/v1/agent/self-service-selection", headers=_headers(d), json={"source_roots": [r"C:\x"], "source": "admin"})
    assert r.status_code == 422


def test_self_service_backup_queues_a_run_backup_the_agent_then_claims(client):
    login(client)
    d = _enroll(client, "self-backup-01", "branch")
    r = client.post("/api/v1/agent/self-service-backup", headers=_headers(d), json={})
    assert r.status_code == 200, r.text
    assert r.json()["queued"] is True
    again = client.post("/api/v1/agent/self-service-backup", headers=_headers(d), json={})
    assert again.json()["command_id"] == r.json()["command_id"], "a second click while one is pending must not queue another"
    events = [e for e in client.get("/api/v1/admin/audit-events").json()["items"] if e["action"] == "command_create" and e["device_id"] == d["device_id"]]
    assert events and events[0]["actor_type"] == "device"


def test_self_service_backup_requires_agent_auth(client):
    assert client.post("/api/v1/agent/self-service-backup", json={}).status_code == 401


def test_a_user_message_reaches_olaylar_with_its_text(client):
    login(client)
    d = _enroll(client, "user-msg-01", "branch")
    r = client.post("/api/v1/agent/user-messages", headers=_headers(d), json={"message": "Dün sildiğim Excel dosyasını bulamıyorum."})
    assert r.status_code == 200, r.text
    events = [e for e in client.get("/api/v1/admin/audit-events").json()["items"] if e["action"] == "user_message" and e["device_id"] == d["device_id"]]
    assert len(events) == 1
    assert "Excel" in str(events[0])


def test_a_user_message_must_not_be_empty_or_huge(client):
    login(client)
    d = _enroll(client, "user-msg-02", "branch")
    assert client.post("/api/v1/agent/user-messages", headers=_headers(d), json={"message": "   "}).status_code == 422
    assert client.post("/api/v1/agent/user-messages", headers=_headers(d), json={"message": "x" * 2001}).status_code == 422
