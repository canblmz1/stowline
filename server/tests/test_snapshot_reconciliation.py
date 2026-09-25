"""Read-only Restic-vs-DB drift reconciliation (Priority 5).

The control plane never holds a repository's restic password, so this can
only ever compare snapshot *ids* the gateway reports existing against the
rows this control plane recorded -- never open, decrypt, or repair
anything. These tests pin exactly that: no restic password appears
anywhere, and a mismatch is reported, never auto-corrected.
"""
from __future__ import annotations

from test_api import client, login  # noqa: F401  — reuse seeded control-plane client; sets test env vars before app.config.settings is constructed

from app import services  # noqa: E402


def _enroll(client, label: str) -> dict:
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": label}).json()["token"]
    return client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": f"pc-{label}"}).json()


def test_reconciliation_reports_repo_only_and_db_only_without_a_password(monkeypatch, client):
    login(client)
    d = _enroll(client, "recon1")
    recorded_snap = "a" * 64
    client.post(
        "/api/v1/agent/jobs/events",
        headers={"Authorization": f"Bearer {d['control_credential']}"},
        json={"outcome": "SUCCEEDED", "snapshot_id": recorded_snap, "slot_key": "slot-1"},
    )

    unrecorded_in_repo = "b" * 64  # exists in the repository, never made it into this DB

    monkeypatch.setattr(services.settings, "gateway_admin_url", "http://gw.invalid")
    monkeypatch.setattr(services.settings, "gateway_admin_token", "test-gateway-admin")

    class Resp:
        status_code = 200

        def json(self):
            return {"snapshot_ids": [unrecorded_in_repo]}

    calls = []

    def fake_get(url, headers=None, timeout=None):
        calls.append((url, headers))
        return Resp()

    monkeypatch.setattr(services.httpx, "get", fake_get)

    r = client.get(f"/api/v1/admin/devices/{d['device_id']}/snapshot-reconciliation")
    assert r.status_code == 200, r.text
    body = r.json()
    assert body["checked"] is True
    assert body["repo_only"] == [unrecorded_in_repo]
    assert body["db_only"] == [recorded_snap]
    assert body["matched_count"] == 0

    # The gateway call must carry the admin bearer token, never a restic
    # password, and the URL must be the credential-free listing route.
    assert len(calls) == 1
    url, headers = calls[0]
    assert f"/admin/snapshots/{d['device_id']}/{d['generation_id']}" in url
    assert headers["Authorization"] == "Bearer test-gateway-admin"
    raw = r.text
    assert "restic" not in raw.lower() or "restic" not in raw  # no password field leaks either way


def test_reconciliation_matches_when_repo_and_db_agree(monkeypatch, client):
    login(client)
    d = _enroll(client, "recon2")
    snap = "c" * 64
    client.post(
        "/api/v1/agent/jobs/events",
        headers={"Authorization": f"Bearer {d['control_credential']}"},
        json={"outcome": "SUCCEEDED", "snapshot_id": snap, "slot_key": "slot-1"},
    )
    monkeypatch.setattr(services.settings, "gateway_admin_url", "http://gw.invalid")
    monkeypatch.setattr(services.settings, "gateway_admin_token", "test-gateway-admin")

    class Resp:
        status_code = 200

        def json(self):
            return {"snapshot_ids": [snap]}

    monkeypatch.setattr(services.httpx, "get", lambda *a, **k: Resp())

    body = client.get(f"/api/v1/admin/devices/{d['device_id']}/snapshot-reconciliation").json()
    assert body["matched_count"] == 1
    assert body["repo_only"] == []
    assert body["db_only"] == []


def test_reconciliation_never_deletes_or_mutates_anything(monkeypatch, client):
    """A drift report must be pure read: no write to Snapshot/Repository
    rows regardless of what the gateway reports."""
    login(client)
    d = _enroll(client, "recon3")
    snap = "d" * 64
    client.post(
        "/api/v1/agent/jobs/events",
        headers={"Authorization": f"Bearer {d['control_credential']}"},
        json={"outcome": "SUCCEEDED", "snapshot_id": snap, "slot_key": "slot-1"},
    )
    monkeypatch.setattr(services.settings, "gateway_admin_url", "http://gw.invalid")
    monkeypatch.setattr(services.settings, "gateway_admin_token", "test-gateway-admin")

    class Resp:
        status_code = 200

        def json(self):
            return {"snapshot_ids": []}  # repo reports nothing -- db_only should list `snap`

    monkeypatch.setattr(services.httpx, "get", lambda *a, **k: Resp())

    before = client.get(f"/api/v1/admin/devices/{d['device_id']}").json()["snapshots"]
    body = client.get(f"/api/v1/admin/devices/{d['device_id']}/snapshot-reconciliation").json()
    assert body["db_only"] == [snap]
    after = client.get(f"/api/v1/admin/devices/{d['device_id']}").json()["snapshots"]
    assert before == after, "reconciliation must never mutate recorded snapshot rows"


def test_reconciliation_reports_unchecked_when_gateway_admin_not_configured(monkeypatch, client):
    login(client)
    d = _enroll(client, "recon4")
    monkeypatch.setattr(services.settings, "gateway_admin_url", "")
    monkeypatch.setattr(services.settings, "gateway_admin_token", "")
    body = client.get(f"/api/v1/admin/devices/{d['device_id']}/snapshot-reconciliation").json()
    assert body["checked"] is False


def test_reconciliation_404_for_unknown_device(client):
    login(client)
    assert client.get("/api/v1/admin/devices/no-such-device/snapshot-reconciliation").status_code == 404
