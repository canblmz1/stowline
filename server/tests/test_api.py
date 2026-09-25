from __future__ import annotations

import json
import os
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "gateway"))

os.environ["STOWLINE_DATABASE_URL"] = "sqlite+pysqlite:///" + str(Path("var/test-control.db").resolve()).replace("\\", "/")
os.environ["STOWLINE_LAB_MODE"] = "true"
os.environ["STOWLINE_ADMIN_PASSWORD"] = "test-admin-pass"

Path("var").mkdir(exist_ok=True)
if Path("var/test-control.db").exists():
    Path("var/test-control.db").unlink()

from app.db import Command, DeviceCredential, Installation, Repository, Snapshot  # noqa: E402
from app.main import app, SessionLocal  # noqa: E402
from app.security import new_id, payload_hash, sha256_hex  # noqa: E402
from app.version import RELEASE_VERSION  # noqa: E402
from gw import Registry, create_app as create_gw  # noqa: E402


@pytest.fixture()
def client():
    with TestClient(app) as c:
        yield c


def login(client: TestClient) -> TestClient:
    r = client.post("/api/v1/auth/login", json={"username": "admin", "password": "test-admin-pass"})
    assert r.status_code == 200, r.text
    return client


def test_health(client):
    health = client.get("/health").json()
    assert health["status"] == "ok"
    assert health["version"] == RELEASE_VERSION
    assert client.get("/api/v1/version").json()["version"] == RELEASE_VERSION
    assert client.get("/ready").json()["status"] == "ready"


def test_unauthenticated_admin_denied(client):
    assert client.get("/api/v1/admin/devices").status_code == 401


def test_enroll_heartbeat_backup_isolation(client):
    login(client)
    t1 = client.post("/api/v1/admin/enrollment-tokens", json={"label": "a"}).json()["token"]
    t2 = client.post("/api/v1/admin/enrollment-tokens", json={"label": "b"}).json()["token"]
    a = client.post("/api/v1/agent/enrollments", json={"token": t1, "hostname": "pc-a", "agent_version": "0.2.0"}).json()
    b = client.post("/api/v1/agent/enrollments", json={"token": t2, "hostname": "pc-b", "agent_version": "0.2.0"}).json()
    assert a["device_id"] != b["device_id"]
    assert a["generation_id"] != b["generation_id"]
    ha = client.post("/api/v1/agent/heartbeat", json={"sequence": 1}, headers={"Authorization": f"Bearer {a['control_credential']}"})
    assert ha.status_code == 200
    # B cannot use A's credential after revoke of A
    snap = "a" * 64
    ing = client.post(
        "/api/v1/agent/jobs/events",
        headers={"Authorization": f"Bearer {a['control_credential']}"},
        json={"outcome": "SUCCEEDED", "snapshot_id": snap, "slot_key": "slot-a", "published_not_green": False},
    )
    assert ing.status_code == 200
    assert ing.json()["health"] == "HEALTHY"
    # PARTIAL must not advance success
    client.post(
        "/api/v1/agent/jobs/events",
        headers={"Authorization": f"Bearer {a['control_credential']}"},
        json={"outcome": "PARTIAL", "snapshot_id": "b" * 64, "slot_key": "slot-a2", "published_not_green": True},
    )
    detail = client.get(f"/api/v1/admin/devices/{a['device_id']}").json()
    assert detail["device"]["last_success_snapshot"] == snap
    # B cannot ingest as A
    r = client.post(
        "/api/v1/agent/jobs/events",
        headers={"Authorization": f"Bearer {b['control_credential']}"},
        json={"outcome": "SUCCEEDED", "snapshot_id": "c" * 64, "slot_key": "slot-a"},
    )
    assert r.status_code == 200
    da = client.get(f"/api/v1/admin/devices/{a['device_id']}").json()
    assert da["device"]["last_success_snapshot"] == snap
    # replayed enrollment token
    again = client.post("/api/v1/agent/enrollments", json={"token": t1, "hostname": "pc-a2"})
    assert again.status_code == 401
    # revoke A
    assert client.post(f"/api/v1/admin/devices/{a['device_id']}/revoke").status_code == 200
    denied = client.post("/api/v1/agent/heartbeat", json={"sequence": 2}, headers={"Authorization": f"Bearer {a['control_credential']}"})
    assert denied.status_code == 401


def test_forbidden_command(client):
    login(client)
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": "c"}).json()["token"]
    d = client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": "pc-c"}).json()
    r = client.post(f"/api/v1/admin/devices/{d['device_id']}/commands", json={"kind": "EXEC", "payload": {"cmd": "whoami"}})
    assert r.status_code == 422
    r = client.post(f"/api/v1/admin/devices/{d['device_id']}/commands", json={"kind": "FORGET", "payload": {}})
    assert r.status_code == 422
    ok = client.post(f"/api/v1/admin/devices/{d['device_id']}/commands", json={"kind": "RUN_BACKUP", "payload": {}})
    assert ok.status_code == 200
    claimed = client.post("/api/v1/agent/work/claim", headers={"Authorization": f"Bearer {d['control_credential']}"})
    assert claimed.json()["command"]["kind"] == "RUN_BACKUP"
    # duplicate claim does not duplicate execution id until ack
    claimed2 = client.post("/api/v1/agent/work/claim", headers={"Authorization": f"Bearer {d['control_credential']}"})
    assert claimed2.json()["command"] is None
    ack = client.post(
        f"/api/v1/agent/commands/{ok.json()['command_id']}/ack",
        headers={"Authorization": f"Bearer {d['control_credential']}"},
        json={"state": "SUCCEEDED"},
    )
    assert ack.status_code == 200
    ack2 = client.post(
        f"/api/v1/agent/commands/{ok.json()['command_id']}/ack",
        headers={"Authorization": f"Bearer {d['control_credential']}"},
        json={"state": "FAILED"},
    )
    assert ack2.json()["idempotent"] is True


def test_backup_now_is_deduplicated_and_legacy_queued_copies_are_coalesced(client):
    login(client)
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": "backup-dedupe"}).json()["token"]
    d = client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": "pc-backup-dedupe"}).json()
    url = f"/api/v1/admin/devices/{d['device_id']}/commands"
    headers = {"Authorization": f"Bearer {d['control_credential']}"}

    first = client.post(url, json={"kind": "RUN_BACKUP", "payload": {}}).json()
    duplicate = client.post(url, json={"kind": "RUN_BACKUP", "payload": {}}).json()
    assert duplicate["command_id"] == first["command_id"]
    assert duplicate["deduplicated"] is True

    assert client.post("/api/v1/agent/work/claim", headers=headers).json()["command"]["command_id"] == first["command_id"]
    while_delivered = client.post(url, json={"kind": "RUN_BACKUP", "payload": {}}).json()
    assert while_delivered["command_id"] == first["command_id"]
    assert while_delivered["deduplicated"] is True

    # Simulate copies made by the older server build before deduplication was
    # added. Completing the real run must make those copies terminal without
    # dispatching another backup.
    legacy_id = new_id()
    with SessionLocal() as s:
        first_row = s.get(Command, first["command_id"])
        s.add(
            Command(
                id=legacy_id,
                device_id=first_row.device_id,
                installation_id=first_row.installation_id,
                kind="RUN_BACKUP",
                payload={},
                payload_hash=payload_hash({}),
                expires_at=datetime.now(timezone.utc) + timedelta(minutes=15),
                job_id=new_id(),
            )
        )
        s.commit()

    assert client.post(f"/api/v1/agent/commands/{first['command_id']}/ack", headers=headers, json={"state": "SUCCEEDED"}).status_code == 200
    with SessionLocal() as s:
        legacy = s.get(Command, legacy_id)
        assert legacy.state == "CANCELLED"
        assert legacy.result_json == {"reason": "coalesced_duplicate"}
        assert legacy.acked_at is not None

    fresh = client.post(url, json={"kind": "RUN_BACKUP", "payload": {}}).json()
    assert fresh["command_id"] != first["command_id"]
    assert fresh["deduplicated"] is False


def test_browse_local_dir_rejects_traversal_and_enqueues_when_valid(client):
    login(client)
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": "bl"}).json()["token"]
    d = client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": "pc-bl"}).json()

    bad = client.post(f"/api/v1/admin/devices/{d['device_id']}/browse-local-dir", json={"path": r"C:\Users\op\..\..\Windows"})
    assert bad.status_code == 422

    empty = client.post(f"/api/v1/admin/devices/{d['device_id']}/browse-local-dir", json={"path": "  "})
    assert empty.status_code == 422

    ok = client.post(f"/api/v1/admin/devices/{d['device_id']}/browse-local-dir", json={"path": r"C:\Users\op\Desktop", "limit": 5000})
    assert ok.status_code == 200, ok.text
    assert ok.json()["kind"] == "BROWSE_LOCAL_DIR"

    claimed = client.post("/api/v1/agent/work/claim", headers={"Authorization": f"Bearer {d['control_credential']}"})
    payload = claimed.json()["command"]["payload"]
    assert payload["path"] == r"C:\Users\op\Desktop"
    assert payload["limit"] == 1000, "an over-large limit must be clamped server-side, not passed through"

    result = {"path": payload["path"], "parent": r"C:\Users\op", "entries": [{"name": "Synthetic", "type": "directory"}]}
    command_id = claimed.json()["command"]["command_id"]
    ack = client.post(
        f"/api/v1/agent/commands/{command_id}/ack",
        headers={"Authorization": f"Bearer {d['control_credential']}"},
        json={"state": "SUCCEEDED", "extra": result},
    )
    assert ack.status_code == 200
    stored = client.get(f"/api/v1/admin/commands/{command_id}").json()
    assert stored["result"] == result, "handler extra must be exposed directly as command result_json"


def test_browse_requests_deduplicate_only_when_the_exact_listing_is_pending(client):
    login(client)
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": "browse-dedupe"}).json()["token"]
    d = client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": "pc-browse-dedupe"}).json()

    local_url = f"/api/v1/admin/devices/{d['device_id']}/browse-local-dir"
    first = client.post(local_url, json={"path": r"C:\Users\op\Desktop", "cursor": "", "limit": 200}).json()
    duplicate = client.post(local_url, json={"path": r"C:\Users\op\Desktop", "cursor": "", "limit": 200}).json()
    different_cursor = client.post(local_url, json={"path": r"C:\Users\op\Desktop", "cursor": "next", "limit": 200}).json()
    assert duplicate["command_id"] == first["command_id"]
    assert duplicate["deduplicated"] is True
    assert different_cursor["command_id"] != first["command_id"]

    snapshot_url = f"/api/v1/admin/devices/{d['device_id']}/browse"
    # The endpoint validates that the snapshot belongs to the device, so seed
    # a bound snapshot exactly as the existing browse tests do.
    with SessionLocal() as s:
        repo = s.query(Repository).filter(Repository.device_id == d["device_id"]).one()
        s.add(Snapshot(id=new_id(), repository_id=repo.id, engine_snapshot_id="a" * 64))
        s.commit()
    snap = client.post(snapshot_url, json={"snapshot_id": "a" * 64, "prefix": "/C"}).json()
    snap_dup = client.post(snapshot_url, json={"snapshot_id": "a" * 64, "prefix": "/C"}).json()
    snap_other = client.post(snapshot_url, json={"snapshot_id": "a" * 64, "prefix": "/D"}).json()
    assert snap_dup["command_id"] == snap["command_id"]
    assert snap_dup["deduplicated"] is True
    assert snap_other["command_id"] != snap["command_id"]


def test_apply_selection_requires_consent_for_sensitive_paths(client, tmp_path):
    login(client)
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": "as"}).json()["token"]
    d = client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": "pc-as"}).json()

    no_roots = client.post(f"/api/v1/admin/devices/{d['device_id']}/apply-selection", json={"source_roots": []})
    assert no_roots.status_code == 422

    traversal = client.post(
        f"/api/v1/admin/devices/{d['device_id']}/apply-selection",
        json={"source_roots": [r"C:\Users\op\Desktop\..\..\Windows"]},
    )
    assert traversal.status_code == 422

    sensitive_path = str(tmp_path / ".env")
    unconsented = client.post(
        f"/api/v1/admin/devices/{d['device_id']}/apply-selection",
        json={"source_roots": [sensitive_path]},
    )
    assert unconsented.status_code == 422, unconsented.text

    consented = client.post(
        f"/api/v1/admin/devices/{d['device_id']}/apply-selection",
        json={"source_roots": [sensitive_path], "sensitive_consents": [sensitive_path]},
    )
    assert consented.status_code == 200, consented.text
    body = consented.json()
    assert body["kind"] == "APPLY_SELECTION"
    assert body["revision_id"]

    claimed = client.post("/api/v1/agent/work/claim", headers={"Authorization": f"Bearer {d['control_credential']}"})
    payload = claimed.json()["command"]["payload"]
    assert payload["revision_id"] == body["revision_id"]
    assert payload["source_roots"] == [sensitive_path]
    assert payload["sensitive_consents"] == [sensitive_path]

    applied = {"revision_id": body["revision_id"], "source_roots": [sensitive_path], "source_root_count": 1}
    command_id = claimed.json()["command"]["command_id"]
    ack = client.post(
        f"/api/v1/agent/commands/{command_id}/ack",
        headers={"Authorization": f"Bearer {d['control_credential']}"},
        json={"state": "SUCCEEDED", "extra": applied},
    )
    assert ack.status_code == 200
    assert client.get(f"/api/v1/admin/commands/{command_id}").json()["result"] == applied


def test_apply_selection_generates_a_fresh_revision_id_each_call(client, tmp_path):
    login(client)
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": "rev"}).json()["token"]
    d = client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": "pc-rev"}).json()
    root = str(tmp_path / "Documents")

    first = client.post(f"/api/v1/admin/devices/{d['device_id']}/apply-selection", json={"source_roots": [root]})
    second = client.post(f"/api/v1/admin/devices/{d['device_id']}/apply-selection", json={"source_roots": [root]})
    assert first.status_code == 200 and second.status_code == 200
    assert first.json()["revision_id"] != second.json()["revision_id"]


def _age_delivery(command_id: str, seconds_ago: int) -> None:
    """Back-date a command's delivered_at, simulating a delivery whose ack
    never arrived -- without sleeping in the test."""
    with SessionLocal() as s:
        cmd = s.get(Command, command_id)
        cmd.delivered_at = datetime.now(timezone.utc) - timedelta(seconds=seconds_ago)
        s.commit()


def test_stale_delivered_command_is_redelivered_to_same_device(client):
    login(client)
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": "redeliv"}).json()["token"]
    d = client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": "pc-redeliv"}).json()
    headers = {"Authorization": f"Bearer {d['control_credential']}"}
    cmd_id = client.post(f"/api/v1/admin/devices/{d['device_id']}/commands", json={"kind": "RUN_CANARY", "payload": {}}).json()["command_id"]

    first = client.post("/api/v1/agent/work/claim", headers=headers)
    assert first.json()["command"]["command_id"] == cmd_id
    with SessionLocal() as s:
        delivered = s.get(Command, cmd_id)
        assert delivered.state == "DELIVERED"
        assert delivered.delivered_at is not None
        assert delivered.acked_at is None, "delivery must not masquerade as a terminal ACK"
        assert delivered.delivery_attempts == 1

    # Immediately after delivery, a second claim must NOT redeliver -- the
    # lease has not expired and the agent may still be working on it.
    immediate = client.post("/api/v1/agent/work/claim", headers=headers)
    assert immediate.json()["command"] is None

    # The ack never arrives (crash, dropped connection, bug). Once the
    # bounded lease is stale, the SAME command must be offered again.
    _age_delivery(cmd_id, seconds_ago=25)
    redelivered = client.post("/api/v1/agent/work/claim", headers=headers)
    assert redelivered.json()["command"]["command_id"] == cmd_id
    with SessionLocal() as s:
        assert s.get(Command, cmd_id).delivery_attempts == 2

    ack = client.post(f"/api/v1/agent/commands/{cmd_id}/ack", headers=headers, json={"state": "SUCCEEDED"})
    assert ack.status_code == 200
    with SessionLocal() as s:
        assert s.get(Command, cmd_id).acked_at is not None

    # Once terminally acked, no amount of staleness brings it back.
    _age_delivery(cmd_id, seconds_ago=999)
    after_ack = client.post("/api/v1/agent/work/claim", headers=headers)
    assert after_ack.json()["command"] is None


def test_stale_delivered_command_is_not_offered_to_a_different_device(client):
    login(client)
    tok_a = client.post("/api/v1/admin/enrollment-tokens", json={"label": "iso-a"}).json()["token"]
    tok_b = client.post("/api/v1/admin/enrollment-tokens", json={"label": "iso-b"}).json()["token"]
    a = client.post("/api/v1/agent/enrollments", json={"token": tok_a, "hostname": "pc-iso-a"}).json()
    b = client.post("/api/v1/agent/enrollments", json={"token": tok_b, "hostname": "pc-iso-b"}).json()
    cmd_id = client.post(f"/api/v1/admin/devices/{a['device_id']}/commands", json={"kind": "RUN_CANARY", "payload": {}}).json()["command_id"]

    client.post("/api/v1/agent/work/claim", headers={"Authorization": f"Bearer {a['control_credential']}"})
    _age_delivery(cmd_id, seconds_ago=25)

    stolen = client.post("/api/v1/agent/work/claim", headers={"Authorization": f"Bearer {b['control_credential']}"})
    assert stolen.json()["command"] is None


def test_command_is_bound_to_issuing_installation_for_claim_and_ack(client):
    login(client)
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": "inst-iso"}).json()["token"]
    d = client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": "pc-inst-iso"}).json()
    original_headers = {"Authorization": f"Bearer {d['control_credential']}"}
    cmd_id = client.post(f"/api/v1/admin/devices/{d['device_id']}/commands", json={"kind": "RUN_CANARY", "payload": {}}).json()["command_id"]

    other_secret = "other-installation-control-secret"
    with SessionLocal() as s:
        other_inst = Installation(id=new_id(), device_id=d["device_id"], lifecycle="ACTIVE", agent_version="test", capabilities={})
        s.add(other_inst)
        s.flush()
        s.add(DeviceCredential(id=new_id(), installation_id=other_inst.id, purpose="control", secret_hash=sha256_hex(other_secret)))
        s.commit()

    other_headers = {"Authorization": f"Bearer {other_secret}"}
    assert client.post("/api/v1/agent/work/claim", headers=other_headers).json()["command"] is None
    claimed = client.post("/api/v1/agent/work/claim", headers=original_headers).json()["command"]
    assert claimed["command_id"] == cmd_id
    wrong_ack = client.post(f"/api/v1/agent/commands/{cmd_id}/ack", headers=other_headers, json={"state": "SUCCEEDED"})
    assert wrong_ack.status_code == 404
    assert client.post(f"/api/v1/agent/commands/{cmd_id}/ack", headers=original_headers, json={"state": "SUCCEEDED"}).status_code == 200


def test_simultaneous_claims_cannot_both_receive_same_command(client):
    import threading

    login(client)
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": "claim-race"}).json()["token"]
    d = client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": "pc-claim-race"}).json()
    cmd_id = client.post(f"/api/v1/admin/devices/{d['device_id']}/commands", json={"kind": "RUN_CANARY", "payload": {}}).json()["command_id"]
    barrier = threading.Barrier(3)
    received = []

    def claim_once():
        with TestClient(app) as parallel_client:
            barrier.wait()
            response = parallel_client.post(
                "/api/v1/agent/work/claim",
                headers={"Authorization": f"Bearer {d['control_credential']}"},
            )
            received.append(response.json()["command"])

    threads = [threading.Thread(target=claim_once) for _ in range(2)]
    for thread in threads:
        thread.start()
    barrier.wait()
    for thread in threads:
        thread.join()

    claimed_ids = [item["command_id"] for item in received if item is not None]
    assert claimed_ids == [cmd_id], received
    with SessionLocal() as s:
        assert s.get(Command, cmd_id).delivery_attempts == 1


def test_expired_stale_delivered_command_is_not_redelivered(client):
    login(client)
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": "exp"}).json()["token"]
    d = client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": "pc-exp"}).json()
    headers = {"Authorization": f"Bearer {d['control_credential']}"}
    cmd_id = client.post(f"/api/v1/admin/devices/{d['device_id']}/commands", json={"kind": "RUN_CANARY", "payload": {}}).json()["command_id"]
    client.post("/api/v1/agent/work/claim", headers=headers)

    with SessionLocal() as s:
        cmd = s.get(Command, cmd_id)
        cmd.delivered_at = datetime.now(timezone.utc) - timedelta(seconds=25)
        cmd.expires_at = datetime.now(timezone.utc) - timedelta(seconds=1)
        s.commit()

    assert client.post("/api/v1/agent/work/claim", headers=headers).json()["command"] is None


def test_restore_rejects_traversal(client):
    login(client)
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": "r"}).json()["token"]
    d = client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": "pc-r"}).json()
    bad = client.post(
        "/api/v1/admin/restore-requests",
        json={"device_id": d["device_id"], "snapshot_id": "a" * 64, "selections": ["../Windows"]},
    )
    assert bad.status_code == 422


def test_wrong_password_not_success_semantics(client):
    login(client)
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": "f"}).json()["token"]
    d = client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": "pc-f"}).json()
    r = client.post(
        "/api/v1/agent/jobs/events",
        headers={"Authorization": f"Bearer {d['control_credential']}"},
        json={"outcome": "FAILED", "error_class": "AUTH", "slot_key": "s1"},
    )
    assert r.json()["health"] == "FAILED"
    detail = client.get(f"/api/v1/admin/devices/{d['device_id']}").json()
    assert detail["device"]["last_success_snapshot"] == ""


def test_cancelled_published_not_green(client):
    login(client)
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": "k"}).json()["token"]
    d = client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": "pc-k"}).json()
    client.post(
        "/api/v1/agent/jobs/events",
        headers={"Authorization": f"Bearer {d['control_credential']}"},
        json={"outcome": "CANCELLED", "snapshot_id": "d" * 64, "published_not_green": True, "slot_key": "c1"},
    )
    detail = client.get(f"/api/v1/admin/devices/{d['device_id']}").json()
    assert detail["device"]["last_success_snapshot"] == ""
    assert detail["device"]["health"] in {"NEVER_BACKED_UP", "OFFLINE", "PARTIAL"}


def test_twenty_device_sim(client):
    login(client)
    ids = []
    for i in range(20):
        tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": f"n{i}"}).json()["token"]
        d = client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": f"sim-{i:02d}"}).json()
        ids.append(d)
        client.post("/api/v1/agent/heartbeat", json={"sequence": 1}, headers={"Authorization": f"Bearer {d['control_credential']}"})
    dash = client.get("/api/v1/admin/dashboard").json()
    assert dash["total_devices"] >= 20
    # isolation of gateway locations
    locs = {x["gateway_location"] for x in ids}
    assert len(locs) == 20


def test_duplicate_attempt_idempotent(client):
    login(client)
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": "dup"}).json()["token"]
    d = client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": "pc-dup"}).json()
    body = {"attempt_id": "11111111-1111-1111-1111-111111111111", "outcome": "SUCCEEDED", "snapshot_id": "e" * 64, "slot_key": "dup"}
    h = {"Authorization": f"Bearer {d['control_credential']}"}
    a = client.post("/api/v1/agent/jobs/events", headers=h, json=body)
    b = client.post("/api/v1/agent/jobs/events", headers=h, json=body)
    assert a.status_code == 200 and b.json()["idempotent"] is True


# --- Regression: a delayed/backlogged outbox report must not move a
# device's last-success state backward. Reproduces the real incident: a
# schema-width bug blocked ~20 outbox reports (some over a week old) from
# ever being ingested; once fixed, they all landed in one burst and the
# last one processed silently overwrote last_success_at/last_success_snapshot
# with whichever report happened to be ingested last -- regardless of which
# one actually happened most recently. attempt_ended_at (the agent's own
# outbox CreatedAt, see agent/internal/control/execute.go outboxToAttempt)
# is the ordering signal that fixes this; see services.py ingest_attempt.


def _enroll_stale_order(client, hostname):
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": hostname}).json()["token"]
    return client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": hostname}).json()


def test_delayed_old_success_does_not_override_a_newer_attempt(client):
    login(client)
    d = _enroll_stale_order(client, "pc-stale1")
    h = {"Authorization": f"Bearer {d['control_credential']}"}

    newer = client.post(
        "/api/v1/agent/jobs/events",
        headers=h,
        json={"outcome": "SUCCEEDED", "snapshot_id": "b" * 64, "slot_key": "s-newer", "attempt_ended_at": "2026-09-16T07:47:13Z"},
    )
    assert newer.status_code == 200

    # A backlogged report for an OLDER attempt, delivered AFTER the newer
    # one -- e.g. a week-stuck outbox entry finally getting through.
    delayed_old = client.post(
        "/api/v1/agent/jobs/events",
        headers=h,
        json={"outcome": "SUCCEEDED", "snapshot_id": "a" * 64, "slot_key": "s-older", "attempt_ended_at": "2026-09-09T18:54:26Z"},
    )
    assert delayed_old.status_code == 200

    detail = client.get(f"/api/v1/admin/devices/{d['device_id']}").json()
    assert detail["device"]["last_success_snapshot"] == "b" * 64, "the newer attempt's snapshot must still be authoritative"


def test_out_of_order_success_reports_converge_on_the_chronologically_latest(client):
    login(client)
    d = _enroll_stale_order(client, "pc-stale2")
    h = {"Authorization": f"Bearer {d['control_credential']}"}

    # Arrival order is scrambled relative to when each attempt actually
    # happened (middle, oldest, newest) -- the device state must end up
    # reflecting the newest one regardless of delivery order.
    reports = [
        ("mid", "2026-09-12T09:55:37Z"),
        ("oldest", "2026-09-09T19:22:05Z"),
        ("newest", "2026-09-16T07:47:13Z"),
    ]
    for tag, ended_at in reports:
        r = client.post(
            "/api/v1/agent/jobs/events",
            headers=h,
            json={"outcome": "SUCCEEDED", "snapshot_id": (tag[0] * 64), "slot_key": f"s-{tag}", "attempt_ended_at": ended_at},
        )
        assert r.status_code == 200

    detail = client.get(f"/api/v1/admin/devices/{d['device_id']}").json()
    assert detail["device"]["last_success_snapshot"] == "n" * 64, "must converge on the newest attempt, not the last one delivered"


def test_scheduler_freshness_is_not_fooled_by_an_old_delayed_attempt(client):
    """device.last_success_at drives the scheduler's own 'already backed up
    today' decision (agent-side skip_until_due). A stale delayed report
    must not bump it to now(), or the scheduler would wrongly believe a
    fresh backup just happened and skip a real one that's actually due."""
    login(client)
    d = _enroll_stale_order(client, "pc-stale3")
    h = {"Authorization": f"Bearer {d['control_credential']}"}

    client.post(
        "/api/v1/agent/jobs/events",
        headers=h,
        json={"outcome": "SUCCEEDED", "snapshot_id": "c" * 64, "slot_key": "s-real", "attempt_ended_at": "2026-09-13T09:00:00Z"},
    )
    before = client.get(f"/api/v1/admin/devices/{d['device_id']}").json()["device"]["last_success_at"]
    assert before is not None

    # A much older report, delivered well after the real one above.
    stale = client.post(
        "/api/v1/agent/jobs/events",
        headers=h,
        json={"outcome": "SUCCEEDED", "snapshot_id": "d" * 64, "slot_key": "s-ancient", "attempt_ended_at": "2026-09-01T00:00:00Z"},
    )
    assert stale.status_code == 200

    after = client.get(f"/api/v1/admin/devices/{d['device_id']}").json()["device"]["last_success_at"]
    assert after == before, "last_success_at must not move at all when the incoming report is stale"


def test_missing_attempt_ended_at_falls_back_to_prior_unordered_behavior(client):
    """Older, not-yet-updated agents never send attempt_ended_at. Their
    reports must keep working exactly as before this fix -- no regression,
    just no new protection until they upgrade."""
    login(client)
    d = _enroll_stale_order(client, "pc-noord")
    h = {"Authorization": f"Bearer {d['control_credential']}"}

    client.post("/api/v1/agent/jobs/events", headers=h, json={"outcome": "SUCCEEDED", "snapshot_id": "e" * 64, "slot_key": "s1"})
    r2 = client.post("/api/v1/agent/jobs/events", headers=h, json={"outcome": "SUCCEEDED", "snapshot_id": "f" * 64, "slot_key": "s2"})
    assert r2.status_code == 200

    detail = client.get(f"/api/v1/admin/devices/{d['device_id']}").json()
    assert detail["device"]["last_success_snapshot"] == "f" * 64, "without ordering info, the latest-delivered report still wins (unchanged prior behavior)"


def test_bad_policy_timezone(client):
    login(client)
    r = client.post(
        "/api/v1/admin/policies",
        json={"name": "bad-tz", "config": {"schema_version": 1, "source_roots": [r"C:\\Stowline\\TestCorpus"], "vss_mode": "required", "schedule": {"timezone": "Not/AZone", "window_start": "12:00", "window_end": "16:00"}, "retention": {"keep_daily": 7, "keep_weekly": 5, "keep_monthly": 12, "endpoint_may_execute": False}}},
    )
    assert r.status_code == 422


def test_endpoint_retention_forbidden_in_policy(client):
    login(client)
    r = client.post(
        "/api/v1/admin/policies",
        json={"name": "bad-ret", "config": {"schema_version": 1, "source_roots": [r"C:\\Stowline\\TestCorpus"], "vss_mode": "required", "schedule": {"timezone": "Europe/Istanbul", "window_start": "12:00", "window_end": "16:00"}, "retention": {"keep_daily": 7, "keep_weekly": 5, "keep_monthly": 12, "endpoint_may_execute": True}}},
    )
    assert r.status_code == 422


def test_oversized_body(client):
    login(client)
    r = client.post("/api/v1/admin/policies", content=b"x" * (300 * 1024), headers={"Content-Type": "application/json", "Content-Length": str(300 * 1024)})
    assert r.status_code == 413


def test_sql_injection_filter_is_not_sql(client):
    login(client)
    r = client.get("/api/v1/admin/devices", params={"q": "'; DROP TABLE devices;--"})
    assert r.status_code == 200
    assert client.get("/api/v1/admin/dashboard").status_code == 200


def test_lab_gateway_restore_recovery(client, tmp_path):
    login(client)
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": "lab"}).json()["token"]
    d = client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": "lab-pc", "agent_version": "0.2.0-full-pilot"}).json()
    reg = Registry(tmp_path / "reg.json")
    gw = create_gw(tmp_path / "store", reg)
    with TestClient(gw) as gwc:
        reg.put(d["device_id"], d["generation_id"], d["gateway_credential"])
        import base64

        token = base64.b64encode(f"{d['device_id']}:{d['gateway_credential']}".encode()).decode()
        auth = {"Authorization": f"Basic {token}"}
        assert gwc.post(f"/restic/{d['device_id']}/{d['generation_id']}/config", headers=auth, content=b'{"id":"labrepo"}').status_code == 200
        other = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
        assert gwc.get(f"/restic/{other}/{d['generation_id']}/config", headers=auth).status_code in {400, 401, 403}
        src = tmp_path / "store" / d["device_id"] / d["generation_id"] / "config"
        dst = tmp_path / "recovery-copy"
        dst.mkdir()
        (dst / "config").write_bytes(src.read_bytes())
        assert (dst / "config").read_bytes() == b'{"id":"labrepo"}'
    h = {"Authorization": f"Bearer {d['control_credential']}"}
    snap = "f" * 64
    assert client.post("/api/v1/agent/jobs/events", headers=h, json={"outcome": "SUCCEEDED", "snapshot_id": snap, "slot_key": "lab-1"}).json()["health"] == "HEALTHY"
    rr = client.post("/api/v1/admin/restore-requests", json={"device_id": d["device_id"], "snapshot_id": snap, "selections": ["/C/Stowline/TestCorpus"]})
    assert rr.status_code == 200
    claimed = client.post("/api/v1/agent/work/claim", headers=h).json()
    assert claimed["command"]["kind"] == "RESTORE_TO_STAGING"
    assert client.post(f"/api/v1/agent/commands/{claimed['command']['command_id']}/ack", headers=h, json={"state": "SUCCEEDED"}).status_code == 200
    assert client.get("/api/v1/admin/restore-requests").json()["items"][0]["state"] == "READY"
    client.post("/api/v1/agent/jobs/events", headers=h, json={"attempt_id": "22222222-2222-2222-2222-222222222222", "outcome": "FAILED", "error_class": "TRANSPORT", "slot_key": "gw-down"})
    detail = client.get(f"/api/v1/admin/devices/{d['device_id']}").json()
    assert detail["device"]["last_success_snapshot"] == snap
    assert detail["device"]["health"] == "FAILED"


def test_enrollment_hostname_newlines_stripped(client):
    login(client)
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": "nl"}).json()["token"]
    d = client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": "pc-nl\r\nFAKE-SEVERITY"}).json()
    detail = client.get(f"/api/v1/admin/devices/{d['device_id']}").json()
    assert "\n" not in detail["device"]["hostname"]
    assert "\r" not in detail["device"]["hostname"]


def test_enrollment_token_parallel_one_winner(client):
    login(client)
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": "race"}).json()["token"]
    import threading

    codes = []

    def enroll():
        r = client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": "race-pc"})
        codes.append(r.status_code)

    threads = [threading.Thread(target=enroll) for _ in range(2)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    assert codes.count(200) == 1
    assert codes.count(401) == 1


def test_ingest_idor_attempt_and_job(client):
    login(client)
    t1 = client.post("/api/v1/admin/enrollment-tokens", json={"label": "ia"}).json()["token"]
    t2 = client.post("/api/v1/admin/enrollment-tokens", json={"label": "ib"}).json()["token"]
    a = client.post("/api/v1/agent/enrollments", json={"token": t1, "hostname": "idor-a"}).json()
    b = client.post("/api/v1/agent/enrollments", json={"token": t2, "hostname": "idor-b"}).json()
    body = {"attempt_id": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "outcome": "SUCCEEDED", "snapshot_id": "a" * 64, "slot_key": "idor"}
    ha = {"Authorization": f"Bearer {a['control_credential']}"}
    hb = {"Authorization": f"Bearer {b['control_credential']}"}
    assert client.post("/api/v1/agent/jobs/events", headers=ha, json=body).status_code == 200
    stolen = client.post("/api/v1/agent/jobs/events", headers=hb, json=body)
    assert stolen.status_code == 403
    job_id = client.post("/api/v1/agent/jobs/events", headers=ha, json={"outcome": "FAILED", "slot_key": "idor"}).json()["job_id"]
    r = client.post("/api/v1/agent/jobs/events", headers=hb, json={"job_id": job_id, "outcome": "SUCCEEDED", "snapshot_id": "b" * 64})
    assert r.status_code == 403


def test_restore_requires_bound_snapshot(client):
    login(client)
    t1 = client.post("/api/v1/admin/enrollment-tokens", json={"label": "ra"}).json()["token"]
    t2 = client.post("/api/v1/admin/enrollment-tokens", json={"label": "rb"}).json()["token"]
    a = client.post("/api/v1/agent/enrollments", json={"token": t1, "hostname": "rst-a"}).json()
    b = client.post("/api/v1/agent/enrollments", json={"token": t2, "hostname": "rst-b"}).json()
    snap = "c" * 64
    client.post(
        "/api/v1/agent/jobs/events",
        headers={"Authorization": f"Bearer {a['control_credential']}"},
        json={"outcome": "SUCCEEDED", "snapshot_id": snap, "slot_key": "rst"},
    )
    missing = client.post("/api/v1/admin/restore-requests", json={"device_id": a["device_id"], "snapshot_id": "d" * 64, "selections": ["/C/data"]})
    assert missing.status_code == 422
    cross = client.post("/api/v1/admin/restore-requests", json={"device_id": b["device_id"], "snapshot_id": snap, "selections": ["/C/data"]})
    assert cross.status_code == 422
    drive = client.post("/api/v1/admin/restore-requests", json={"device_id": a["device_id"], "snapshot_id": snap, "selections": ["C:relative"]})
    assert drive.status_code == 422
    ok = client.post("/api/v1/admin/restore-requests", json={"device_id": a["device_id"], "snapshot_id": snap, "selections": ["/C/data"]})
    assert ok.status_code == 200


def test_browse_requires_bound_snapshot(client):
    login(client)
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": "br"}).json()["token"]
    d = client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": "br-pc"}).json()
    bad = client.post(f"/api/v1/admin/devices/{d['device_id']}/browse", json={"snapshot_id": "e" * 64, "prefix": ""})
    assert bad.status_code == 422
    snap = "f" * 64
    client.post(
        "/api/v1/agent/jobs/events",
        headers={"Authorization": f"Bearer {d['control_credential']}"},
        json={"outcome": "SUCCEEDED", "snapshot_id": snap, "slot_key": "br"},
    )
    ok = client.post(f"/api/v1/admin/devices/{d['device_id']}/browse", json={"snapshot_id": snap, "prefix": "/C"})
    assert ok.status_code == 200
    trav = client.post(f"/api/v1/admin/devices/{d['device_id']}/browse", json={"snapshot_id": snap, "prefix": "../"})
    assert trav.status_code == 422


def test_expire_does_not_clobber_succeeded(client):
    from datetime import timedelta

    from app.db import Command, utcnow
    from app.main import SessionLocal
    from app.services import expire_commands

    login(client)
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": "ex"}).json()["token"]
    d = client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": "ex-pc"}).json()
    created = client.post(f"/api/v1/admin/devices/{d['device_id']}/commands", json={"kind": "RUN_BACKUP", "payload": {}})
    cid = created.json()["command_id"]
    h = {"Authorization": f"Bearer {d['control_credential']}"}
    assert client.post("/api/v1/agent/work/claim", headers=h).json()["command"]["kind"] == "RUN_BACKUP"
    assert client.post(f"/api/v1/agent/commands/{cid}/ack", headers=h, json={"state": "SUCCEEDED"}).status_code == 200
    db = SessionLocal()
    try:
        cmd = db.get(Command, cid)
        cmd.expires_at = utcnow() - timedelta(minutes=5)
        db.commit()
        expire_commands(db)
        cmd = db.get(Command, cid)
        assert cmd.state == "SUCCEEDED"
    finally:
        db.close()


def test_bad_login_and_malformed(client):
    assert client.post("/api/v1/auth/login", json={"username": "admin", "password": "nope"}).status_code == 401
    assert client.post("/api/v1/auth/login", content=b"{", headers={"Content-Type": "application/json"}).status_code == 422
    login(client)
    assert client.post("/api/v1/admin/enrollment-tokens", json={"label": "x", "extra": 1}).status_code == 422


def test_worker_lease_second_skipped(client):
    from app.worker import loop_once

    login(client)
    first = loop_once()
    second = loop_once()
    assert first.get("skipped") is not True
    assert second.get("skipped") is True


def test_device_auth_wrong_and_empty(client):
    login(client)
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": "au"}).json()["token"]
    d = client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": "au-pc"}).json()
    assert client.post("/api/v1/agent/heartbeat", json={"sequence": 1}).status_code == 401
    assert client.post("/api/v1/agent/heartbeat", json={"sequence": 1}, headers={"Authorization": "Bearer "}).status_code == 401
    assert client.post("/api/v1/agent/heartbeat", json={"sequence": 1}, headers={"Authorization": "Bearer not-a-real-token"}).status_code == 401
    t2 = client.post("/api/v1/admin/enrollment-tokens", json={"label": "au2"}).json()["token"]
    b = client.post("/api/v1/agent/enrollments", json={"token": t2, "hostname": "au-b"}).json()
    r = client.post("/api/v1/agent/heartbeat", json={"sequence": 1}, headers={"Authorization": f"Bearer {b['control_credential']}"})
    assert r.status_code == 200
    # B token must not heartbeat as a different identity in the response policy binding
    assert client.post("/api/v1/agent/heartbeat", json={"sequence": 9}, headers={"Authorization": f"Bearer {d['control_credential']}"}).status_code == 200


def _measure_sites(client):
    login(client)
    from app import admission as wan
    from app.db import Site, WanAdmissionLease
    from app.main import SessionLocal
    from sqlalchemy import select

    db = SessionLocal()
    try:
        for row in db.scalars(select(WanAdmissionLease).where(WanAdmissionLease.released_at.is_(None))).all():
            wan._release_row(row, "test-reset")
        for site_id, mbps in (("hq", 5), ("branch", 20)):
            site = db.get(Site, site_id)
            site.measured_upload_mbps = mbps
            site.business_backup_budget_percent = 20
            site.max_concurrent_wan_backups = 1
            site.pause_new_wan_admissions = False
            site.provider_unavailable_until = None
            site.emergency_restore_until = None
            compiled = wan.compile_for_site(site, 1)
            if compiled["ok"]:
                site.max_site_backup_bps = compiled["max_site_backup_bps"]
        db.commit()
    finally:
        db.close()
    for site_id, mbps in (("hq", 5), ("branch", 20)):
        r = client.patch(
            f"/api/v1/admin/sites/{site_id}",
            json={"measured_upload_mbps": mbps, "business_backup_budget_percent": 20, "max_concurrent_wan_backups": 1, "pause_new_wan_admissions": False},
        )
        assert r.status_code == 200, r.text


def _enroll(client, hostname, site_id):
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": hostname, "site_id": site_id}).json()["token"]
    return client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": hostname, "agent_version": "0.2.2"}).json()


def test_bandwidth_compile_20mbps_20pct(client):
    from app.bandwidth import compile_site

    compiled = compile_site(20, 20, 1)
    assert compiled["ok"] is True
    assert compiled["max_site_backup_bps"] == 4_000_000
    assert compiled["gateway_bytes_per_sec"] == 500_000
    assert abs(compiled["restic_kibps"] - 488.28125) < 1e-9
    assert compiled["agent_kibps"] == 439
    _measure_sites(client)
    isk = client.get("/api/v1/admin/sites").json()["items"]
    by_id = {s["id"]: s for s in isk}
    assert by_id["branch"]["compiled_agent_kibps"] == 439
    assert abs(by_id["branch"]["hard_site_ceiling_mbps"] - 4.0) < 1e-9
    assert by_id["hq"]["compiled_agent_kibps"] == 109


def test_unmeasured_site_refuses_lease(client):
    login(client)
    d = _enroll(client, "unmeasured-pc", "hq")
    # hq may already be measured by a previous test; clear it.
    from app.db import Site
    from app.main import SessionLocal

    db = SessionLocal()
    try:
        site = db.get(Site, "hq")
        site.measured_upload_mbps = None
        site.max_site_backup_bps = None
        site.pause_new_wan_admissions = False
        site.provider_unavailable_until = None
        db.commit()
    finally:
        db.close()
    r = client.post(
        "/api/v1/agent/wan/leases/acquire",
        headers={"Authorization": f"Bearer {d['control_credential']}"},
        json={"attempt_id": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaa0001", "class": "NORMAL_BACKUP"},
    )
    assert r.status_code == 200
    assert r.json()["status"] == "SITE_UNMEASURED"


def test_pause_blocks_new_lease(client):
    _measure_sites(client)
    d = _enroll(client, "pause-pc", "branch")
    assert client.post("/api/v1/admin/sites/branch/pause").status_code == 200
    r = client.post(
        "/api/v1/agent/wan/leases/acquire",
        headers={"Authorization": f"Bearer {d['control_credential']}"},
        json={"attempt_id": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaa0002", "class": "NORMAL_BACKUP"},
    )
    assert r.json()["status"] == "PAUSED"
    assert client.post("/api/v1/admin/sites/branch/resume").status_code == 200


def test_provider_breaker_429(client):
    _measure_sites(client)
    d = _enroll(client, "breaker-pc", "hq")
    h = {"Authorization": f"Bearer {d['control_credential']}"}
    client.post("/api/v1/agent/jobs/events", headers=h, json={"outcome": "FAILED", "error_class": "PROVIDER_RATE_LIMIT", "slot_key": "brk", "retry_after_seconds": 900})
    r = client.post("/api/v1/agent/wan/leases/acquire", headers=h, json={"attempt_id": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaa0003", "class": "NORMAL_BACKUP"})
    assert r.json()["status"] == "PROVIDER_UNAVAILABLE"
    hb = client.post("/api/v1/agent/heartbeat", json={"sequence": 3}, headers=h).json()
    assert hb["provider_unavailable"] is True
    _measure_sites(client)


def test_idempotent_same_attempt_lease(client):
    _measure_sites(client)
    d = _enroll(client, "idem-pc", "branch")
    h = {"Authorization": f"Bearer {d['control_credential']}"}
    body = {"attempt_id": "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbb0001", "class": "NORMAL_BACKUP", "job_id": "job-1"}
    a = client.post("/api/v1/agent/wan/leases/acquire", headers=h, json=body).json()
    b = client.post("/api/v1/agent/wan/leases/acquire", headers=h, json=body).json()
    assert a["status"] == "GRANTED" and b["status"] == "GRANTED"
    assert a["lease_id"] == b["lease_id"]
    client.post(f"/api/v1/agent/wan/leases/{a['lease_id']}/release", headers=h, json={"reason": "done"})


def test_lease_expiration_reaper(client):
    from datetime import timedelta

    from app import admission as wan
    from app.db import WanAdmissionLease
    from app.main import SessionLocal
    from app.security import utcnow

    _measure_sites(client)
    d = _enroll(client, "expire-pc", "branch")
    h = {"Authorization": f"Bearer {d['control_credential']}"}
    a = client.post("/api/v1/agent/wan/leases/acquire", headers=h, json={"attempt_id": "cccccccc-cccc-cccc-cccc-cccccccc0001", "class": "NORMAL_BACKUP"}).json()
    assert a["status"] == "GRANTED"
    db = SessionLocal()
    try:
        row = db.get(WanAdmissionLease, a["lease_id"])
        row.expires_at = utcnow() - timedelta(seconds=5)
        db.commit()
        assert wan.reap_expired_leases(db) >= 1
    finally:
        db.close()
    d2 = _enroll(client, "expire-pc-2", "branch")
    b = client.post(
        "/api/v1/agent/wan/leases/acquire",
        headers={"Authorization": f"Bearer {d2['control_credential']}"},
        json={"attempt_id": "cccccccc-cccc-cccc-cccc-cccccccc0002", "class": "NORMAL_BACKUP"},
    ).json()
    assert b["status"] == "GRANTED"
    client.post(f"/api/v1/agent/wan/leases/{b['lease_id']}/release", headers={"Authorization": f"Bearer {d2['control_credential']}"}, json={"reason": "done"})


def test_cross_site_isolation_and_nineteen_way_race(client):
    import threading
    import uuid

    _measure_sites(client)
    hq = [_enroll(client, f"ant-{i:02d}", "hq") for i in range(19)]
    branch = [_enroll(client, f"isk-{i:02d}", "branch") for i in range(19)]

    def race(devices):
        winners = []
        statuses = []

        def acquire(device):
            r = client.post(
                "/api/v1/agent/wan/leases/acquire",
                headers={"Authorization": f"Bearer {device['control_credential']}"},
                json={"attempt_id": str(uuid.uuid4()), "class": "NORMAL_BACKUP"},
            )
            body = r.json()
            statuses.append(body["status"])
            if body["status"] == "GRANTED":
                winners.append((body["lease_id"], device))

        threads = [threading.Thread(target=acquire, args=(d,)) for d in devices]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
        return winners, statuses

    def release_all(winners):
        for lease_id, device in winners:
            client.post(
                f"/api/v1/agent/wan/leases/{lease_id}/release",
                headers={"Authorization": f"Bearer {device['control_credential']}"},
                json={"reason": "loop"},
            )

    for _ in range(8):
        aw, ast = race(hq)
        iw, ist = race(branch)
        assert len(aw) == 1, ast
        assert len(iw) == 1, ist
        assert ast.count("GRANTED") == 1
        assert ist.count("GRANTED") == 1
        assert "SITE_CAPACITY_WAIT" in ast
        assert "SITE_CAPACITY_WAIT" in ist
        assert aw[0][0] != iw[0][0]
        release_all(aw + iw)


def test_heartbeat_compiled_policy_and_dashboard_sites(client):
    _measure_sites(client)
    d = _enroll(client, "hb-policy-pc", "branch")
    hb = client.post("/api/v1/agent/heartbeat", json={"sequence": 11}, headers={"Authorization": f"Bearer {d['control_credential']}"}).json()
    assert hb["compiled_upload_kibps"] == 439
    assert hb["policy"]["bandwidth"]["upload_limit_kibps"] == 439
    assert hb["pause_new_wan_admissions"] is False
    pol = client.get("/api/v1/agent/policy", headers={"Authorization": f"Bearer {d['control_credential']}"}).json()
    assert pol["config"]["bandwidth"]["upload_limit_kibps"] == 439
    dash = client.get("/api/v1/admin/dashboard").json()
    assert {s["id"] for s in dash["sites"]} == {"hq", "branch"}


def test_emergency_restore_is_not_default(client):
    _measure_sites(client)
    r = client.post(
        "/api/v1/admin/sites/branch/emergency-restore",
        json={"hours": 1, "restore_download_kibps": 5000, "pause_new_backups": True},
    )
    assert r.status_code == 200
    site = r.json()["site"]
    assert site["pause_new_wan_admissions"] is True
    assert site["emergency_restore_until"]
    client.post("/api/v1/admin/sites/branch/resume")


def test_invalid_measured_upload_rejected(client):
    login(client)
    for bad in (0, -1, 1e12):
        r = client.patch("/api/v1/admin/sites/hq", json={"measured_upload_mbps": bad})
        assert r.status_code == 422, bad
    clear = client.patch("/api/v1/admin/sites/hq", json={"measured_upload_mbps": None})
    assert clear.status_code == 200
    assert clear.json()["site"]["measured_upload_mbps"] is None
    assert clear.json()["site"]["compiled_agent_kibps"] is None


def test_ready_reports_gateway_unconfigured(client):
    from app import services

    services._gateway_admin_ok = None
    services._gateway_admin_reason = ""
    body = client.get("/ready").json()
    assert body["status"] == "ready"
    assert body["database"] == "ok"
    assert body["wan_ready"] is False
    assert body["gateway_registration"] == "unconfigured"
    blob = json.dumps(body)
    assert "STOWLINE_GATEWAY_ADMIN_TOKEN" in body["gateway_registration_reason"]
    assert "Bearer" not in blob


def test_enroll_abort_does_not_leave_active_orphan(client):
    login(client)
    d = _enroll(client, "abort-pc", "hq")
    other = _enroll(client, "keep-pc", "branch")
    cred = d["control_credential"]
    gw = d["gateway_credential"]
    assert client.get(f"/api/v1/admin/devices/{d['device_id']}").json()["device"]["lifecycle"] == "ACTIVE"
    r = client.post("/api/v1/agent/enrollments/abort", headers={"Authorization": f"Bearer {cred}"})
    assert r.status_code == 200, r.text
    body = r.json()
    assert body["lifecycle"] == "QUARANTINED"
    assert body["device_id"] == d["device_id"]
    assert "control_credential" not in body
    assert cred not in r.text
    assert gw not in r.text
    assert client.get(f"/api/v1/admin/devices/{d['device_id']}").json()["device"]["lifecycle"] == "QUARANTINED"
    hb = client.post("/api/v1/agent/heartbeat", json={"sequence": 1}, headers={"Authorization": f"Bearer {cred}"})
    assert hb.status_code == 401
    keep = client.post(
        "/api/v1/agent/heartbeat",
        json={"sequence": 1},
        headers={"Authorization": f"Bearer {other['control_credential']}"},
    )
    assert keep.status_code == 200
    assert client.get(f"/api/v1/admin/devices/{other['device_id']}").json()["device"]["lifecycle"] == "ACTIVE"


def test_notify_gateway_http_error_marks_registration_error(monkeypatch, client):
    from app import services

    monkeypatch.setattr(services.settings, "gateway_admin_url", "http://127.0.0.1:9")
    monkeypatch.setattr(services.settings, "gateway_admin_token", "test-gateway-admin")
    monkeypatch.setattr(services, "_gateway_admin_ok", None)
    monkeypatch.setattr(services, "_gateway_admin_reason", "")

    class Resp:
        status_code = 401

    monkeypatch.setattr(services.httpx, "post", lambda *a, **k: Resp())
    try:
        services.notify_gateway("dev", "gen", "hash")
        st = services.gateway_registration_state()
        assert st["gateway_registration"] == "error"
        assert st["wan_ready"] is False
        blob = json.dumps(st)
        assert "test-gateway-admin" not in blob
        login(client)
        dash = client.get("/api/v1/admin/dashboard").json()
        assert dash["wan_ready"] is False
        assert "test-gateway-admin" not in json.dumps(dash)
        health = client.get("/health").json()
        assert health["wan_ready"] is False
        assert health["gateway_registration"] == "error"
    finally:
        services._gateway_admin_ok = None
        services._gateway_admin_reason = ""


def test_compute_storage_prefix_maps_to_canonical_department_folder():
    from app import services

    assert services.compute_storage_prefix("finance", "FIN-PC01", "a31f82c4-1111-2222-3333-444455556666") == "Finance/FIN-PC01--a31f82c4"
    # Unknown/custom department: kept as-is (sanitized), not forced into
    # one of the fixed folders -- matches normalize_department's own
    # "known name wins, otherwise pass the custom one through" behavior.
    assert services.compute_storage_prefix("Depo Ekibi", "PC1", "deadbeef-0000-0000-0000-000000000000") == "Depo Ekibi/PC1--deadbeef"


def test_compute_storage_prefix_sanitizes_traversal_and_empty_input():
    from app import services

    assert services.compute_storage_prefix("", "", "a31f82c4-0000-0000-0000-000000000000") == "Other/PC--a31f82c4"
    # The exact folder name a messy input collapses to is not the
    # contract -- a "PC_.._.._etc_passwd--..." segment is a perfectly
    # safe (if ugly) single directory name, not a traversal, because it
    # is one segment, not several. What must never happen, checked here
    # the same way gw/__init__.py's independent _safe_storage_prefix
    # checks it on the gateway side of this same value: no "/"-split
    # segment of the result is ever exactly "", ".", or "..".
    for department, hostname in [("../../etc", ".."), ("Finance", "PC/../../etc\\passwd"), ("..", "..")]:
        prefix = services.compute_storage_prefix(department, hostname, "a31f82c4-0000-0000-0000-000000000000")
        segments = prefix.split("/")
        assert len(segments) == 2, prefix
        assert all(seg not in ("", ".", "..") for seg in segments), prefix


def test_enrollment_computes_and_forwards_a_department_storage_prefix(monkeypatch, client):
    """The actual integration point: enrolling with department=Finance
    must call notify_gateway with a storage_prefix under Finance/, built
    from the real hostname and the real (server-minted) device_id -- not
    something the client supplied."""
    from app import services

    login(client)
    calls = []
    monkeypatch.setattr(services, "notify_gateway", lambda *a, **k: calls.append((a, k)))

    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": "g", "department": "Finance"}).json()["token"]
    out = client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": "FIN-PC01", "agent_version": "0.2.4"}).json()

    assert len(calls) == 1
    args, kwargs = calls[0]
    assert args[0] == out["device_id"]
    assert args[1] == out["generation_id"]
    prefix = kwargs["storage_prefix"]
    assert prefix.startswith("Finance/FIN-PC01--")
    assert prefix.endswith(out["device_id"][:8])


def test_changing_department_after_enrollment_never_calls_notify_gateway(monkeypatch, client):
    """department is edited via PATCH /admin/devices/{id}, purely a
    control-plane label (Device.department) -- it must never trigger a
    gateway re-registration, which is exactly what would let an edit
    silently relocate an already-enrolled device's repository."""
    from app import services

    login(client)
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": "g", "department": "Finance"}).json()["token"]
    out = client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": "FIN-PC01", "agent_version": "0.2.4"}).json()

    calls = []
    monkeypatch.setattr(services, "notify_gateway", lambda *a, **k: calls.append((a, k)))
    r = client.patch(f"/api/v1/admin/devices/{out['device_id']}", json={"department": "Finance"})
    assert r.status_code == 200, r.text
    assert calls == []




def test_a_long_backup_can_still_ack_after_its_command_expired(client):
    """Live: a 16-minute backup outlived the RUN_BACKUP command's 15-minute
    window; expire_commands marked it EXPIRED and the agent's final ack got
    409, so the panel never saw the command's real outcome."""
    from datetime import timedelta

    from app import services

    login(client)
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": "long"}).json()["token"]
    d = client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": "pc-long"}).json()
    h = {"Authorization": f"Bearer {d['control_credential']}"}
    cid = client.post(f"/api/v1/admin/devices/{d['device_id']}/commands", json={"kind": "RUN_BACKUP", "payload": {}}).json()["command_id"]
    assert client.post("/api/v1/agent/work/claim", headers=h).json()["command"]["command_id"] == cid
    with SessionLocal() as s:
        cmd = s.get(Command, cid)
        cmd.expires_at = services.now() - timedelta(minutes=1)
        s.commit()
        services.expire_commands(s)
        assert s.get(Command, cid).state == "EXPIRED"
    ack = client.post(f"/api/v1/agent/commands/{cid}/ack", headers=h, json={"state": "SUCCEEDED"})
    assert ack.status_code == 200, ack.text
    with SessionLocal() as s:
        assert s.get(Command, cid).state == "SUCCEEDED"
