"""What the admin list shows, and how a running backup is reported.

While a backup runs the agent neither heartbeats nor polls, so the panel used
to show OFFLINE/red for the whole run. The WAN lease it renews every 30
seconds is the one signal the control plane still gets, so a live lease means
"backing up". The device list should also show only computers that are being
backed up, with a reversible way to take one off it.
"""
from __future__ import annotations

from datetime import datetime, timedelta, timezone

from sqlalchemy import select

from test_api import _enroll, _measure_sites, client, login  # noqa: F401  -- must precede any app import: sets the test env before app.config.settings is built

from app.db import Device, WanAdmissionLease  # noqa: E402
from app.main import SessionLocal  # noqa: E402
from app.security import new_id  # noqa: E402


def _headers(d: dict) -> dict:
    return {"Authorization": f"Bearer {d['control_credential']}"}


def _acquire(client, d: dict, attempt: str, wan_class: str = "NORMAL_BACKUP") -> dict:
    r = client.post("/api/v1/agent/wan/leases/acquire", headers=_headers(d), json={"attempt_id": attempt, "class": wan_class, "job_id": f"job-{attempt[-4:]}"})
    assert r.status_code == 200, r.text
    assert r.json()["status"] == "GRANTED", r.json()
    return r.json()


def _status(client, device_id: str) -> dict:
    return client.get(f"/api/v1/admin/devices/{device_id}").json()["status"]


def _set_last_seen(device_id: str, when: datetime) -> None:
    with SessionLocal() as db:
        db.get(Device, device_id).last_seen_at = when
        db.commit()


def _insert_lease(device_id: str, *, wan_class: str, expires_in: timedelta, released: bool = False) -> None:
    now = datetime.now(timezone.utc)
    with SessionLocal() as db:
        db.add(
            WanAdmissionLease(
                id=new_id(),
                site_id="branch",
                device_id=device_id,
                attempt_id=new_id(),
                wan_class=wan_class,
                hold_key=new_id(),
                device_hold_key=new_id(),
                acquired_at=now - timedelta(minutes=5),
                last_renewed_at=now - timedelta(seconds=20),
                expires_at=now + expires_in,
                released_at=now if released else None,
            )
        )
        db.commit()


# --- a running backup is not OFFLINE ------------------------------------------


def test_a_device_holding_a_live_lease_is_backing_up_and_online(client):
    _measure_sites(client)
    d = _enroll(client, "live-pc-1", "branch")
    lease = _acquire(client, d, new_id())
    _set_last_seen(d["device_id"], datetime.now(timezone.utc) - timedelta(hours=2))  # heartbeats stopped when the run began
    r = client.post(f"/api/v1/agent/wan/leases/{lease['lease_id']}/renew", headers=_headers(d))
    assert r.status_code == 200, r.text
    st = _status(client, d["device_id"])
    assert st["backing_up"] is True
    assert st["online"] is True
    assert st["current_operation"] == "backup_running"
    # This renewal carried no progress fields at all, so the derived phase
    # is PREPARING (no real sample yet) -- see test_backup_progress.py for
    # the PREPARING/BACKING_UP derivation itself.
    assert st["current_progress"]["phase"] == "PREPARING"
    assert st["backup_started_at"]
    assert st["health"] != "OFFLINE"


def test_renewing_a_lease_refreshes_last_seen(client):
    _measure_sites(client)
    d = _enroll(client, "live-pc-2", "branch")
    lease = _acquire(client, d, new_id())
    _set_last_seen(d["device_id"], datetime.now(timezone.utc) - timedelta(hours=3))
    client.post(f"/api/v1/agent/wan/leases/{lease['lease_id']}/renew", headers=_headers(d))
    seen = datetime.fromisoformat(client.get(f"/api/v1/admin/devices/{d['device_id']}").json()["device"]["last_seen_at"])
    if seen.tzinfo is None:
        seen = seen.replace(tzinfo=timezone.utc)
    assert datetime.now(timezone.utc) - seen < timedelta(minutes=1)


def test_lease_renewal_surfaces_sanitized_numeric_backup_progress(client):
    _measure_sites(client)
    d = _enroll(client, "live-progress", "branch")
    lease = _acquire(client, d, new_id())
    progress = {"percent_done": 37.5, "files_done": 3, "total_files": 8, "bytes_done": 375, "total_bytes": 1000}
    r = client.post(f"/api/v1/agent/wan/leases/{lease['lease_id']}/renew", headers=_headers(d), json=progress)
    assert r.status_code == 200, r.text
    current = _status(client, d["device_id"])["current_progress"]
    assert current["percent_done"] == 37.5
    assert current["files_done"] == 3
    assert current["total_files"] == 8
    assert current["bytes_done"] == 375
    assert current["total_bytes"] == 1000
    assert current["updated_at"]
    assert client.post(f"/api/v1/agent/wan/leases/{lease['lease_id']}/renew", headers=_headers(d), json={"percent_done": 101}).status_code == 422


def test_a_released_lease_no_longer_counts_as_running(client):
    _measure_sites(client)
    d = _enroll(client, "live-pc-3", "branch")
    lease = _acquire(client, d, new_id())
    assert _status(client, d["device_id"])["backing_up"] is True
    client.post(f"/api/v1/agent/wan/leases/{lease['lease_id']}/release", headers=_headers(d), json={"reason": "complete"})
    st = _status(client, d["device_id"])
    assert st["backing_up"] is False
    assert st["current_operation"] != "backup_running"


def test_an_expired_lease_does_not_count_as_running(client):
    login(client)
    d = _enroll(client, "live-pc-4", "branch")
    _insert_lease(d["device_id"], wan_class="NORMAL_BACKUP", expires_in=timedelta(minutes=-1))
    assert _status(client, d["device_id"])["backing_up"] is False


def test_a_restore_lease_is_not_reported_as_a_backup(client):
    login(client)
    d = _enroll(client, "live-pc-5", "branch")
    _insert_lease(d["device_id"], wan_class="RESTORE", expires_in=timedelta(minutes=2))
    assert _status(client, d["device_id"])["backing_up"] is False


def test_the_previous_error_is_hidden_while_a_backup_is_running(client, gateway_ready):
    _measure_sites(client)
    d = _enroll(client, "live-pc-6", "branch")
    client.post("/api/v1/agent/jobs/events", headers=_headers(d), json={"outcome": "CANCELLED", "error_class": "CANCELLED", "slot_key": "live-6"})
    assert _status(client, d["device_id"])["last_error_human"]["code"] == "CANCELLED"
    _acquire(client, d, new_id())
    st = _status(client, d["device_id"])
    assert st["backing_up"] is True
    assert st["last_error_human"]["code"] == ""


# --- the list shows only computers that are being backed up ---------------------


def _listed_ids(client, **params) -> set[str]:
    return {x["id"] for x in client.get("/api/v1/admin/devices", params=params).json()["items"]}


def test_the_default_list_hides_revoked_and_archived_devices(client):
    login(client)
    keep = _enroll(client, "scope-keep", "branch")["device_id"]
    revoked = _enroll(client, "scope-revoked", "branch")["device_id"]
    archived = _enroll(client, "scope-archived", "branch")["device_id"]
    assert client.post(f"/api/v1/admin/devices/{revoked}/revoke").status_code == 200
    assert client.post(f"/api/v1/admin/devices/{archived}/archive").status_code == 200
    default = _listed_ids(client)
    assert keep in default
    assert revoked not in default and archived not in default
    assert {revoked, archived} <= _listed_ids(client, scope="archived")
    assert keep not in _listed_ids(client, scope="archived")
    assert {keep, revoked, archived} <= _listed_ids(client, scope="all")


def test_archiving_blocks_the_device_and_unarchiving_restores_it(client):
    login(client)
    d = _enroll(client, "scope-roundtrip", "branch")
    assert client.post("/api/v1/agent/heartbeat", json={"sequence": 1}, headers=_headers(d)).status_code == 200
    assert client.post(f"/api/v1/admin/devices/{d['device_id']}/archive").status_code == 200
    assert client.post("/api/v1/agent/heartbeat", json={"sequence": 2}, headers=_headers(d)).status_code == 401
    assert client.post(f"/api/v1/admin/devices/{d['device_id']}/unarchive").status_code == 200
    assert client.post("/api/v1/agent/heartbeat", json={"sequence": 3}, headers=_headers(d)).status_code == 200
    assert d["device_id"] in _listed_ids(client)


def test_archive_and_unarchive_only_apply_to_the_right_lifecycle(client):
    login(client)
    d = _enroll(client, "scope-guard", "branch")["device_id"]
    assert client.post(f"/api/v1/admin/devices/{d}/unarchive").status_code == 409  # active, nothing to undo
    assert client.post(f"/api/v1/admin/devices/{d}/revoke").status_code == 200
    assert client.post(f"/api/v1/admin/devices/{d}/archive").status_code == 409  # revoked stays revoked
    assert client.post(f"/api/v1/admin/devices/{d}/unarchive").status_code == 409  # never resurrect a revoked device
    assert client.post("/api/v1/admin/devices/does-not-exist/archive").status_code == 404


def test_the_dashboard_counts_only_active_devices(client):
    login(client)
    before = client.get("/api/v1/admin/dashboard").json()
    d = _enroll(client, "scope-dash", "branch")["device_id"]
    mid = client.get("/api/v1/admin/dashboard").json()
    assert mid["total_devices"] == before["total_devices"] + 1
    client.post(f"/api/v1/admin/devices/{d}/archive")
    after = client.get("/api/v1/admin/dashboard").json()
    assert after["total_devices"] == before["total_devices"]
    assert after["archived_count"] == before["archived_count"] + 1


def test_archiving_is_audited(client):
    login(client)
    d = _enroll(client, "scope-audit", "branch")["device_id"]
    client.post(f"/api/v1/admin/devices/{d}/archive")
    client.post(f"/api/v1/admin/devices/{d}/unarchive")
    events = client.get("/api/v1/admin/audit-events").json()["items"]
    actions = {(e["action"], e["resource_id"]) for e in events}
    assert ("device_archive", d) in actions and ("device_unarchive", d) in actions


# --- a running backup keeps the limit it started with -------------------------


def _list_row(client, device_id: str) -> dict:
    return next(x for x in client.get("/api/v1/admin/devices").json()["items"] if x["id"] == device_id)


def test_a_running_backup_reports_the_limit_it_started_with_after_the_site_limit_changes(client):
    _measure_sites(client)  # branch: 20 Mbps at 20% -> 439 KiB/s
    d = _enroll(client, "live-pc-limit", "branch")
    lease = _acquire(client, d, new_id())
    assert lease["bandwidth_kibps"] == 439
    running = _list_row(client, d["device_id"])
    assert running["backing_up"] is True and running["run_limit_kibps"] == 439
    try:
        # the operator raises the branch's limit while the backup runs: restic was started with the old one
        assert client.patch("/api/v1/admin/sites/branch", json={"measured_upload_mbps": 80, "business_backup_budget_percent": 50}).status_code == 200
        assert _list_row(client, d["device_id"])["run_limit_kibps"] == 439
        site = next(s for s in client.get("/api/v1/admin/dashboard").json()["sites"] if s["id"] == "branch")
        assert site["compiled_agent_kibps"] == 4394  # the next lease gets the new limit
    finally:
        client.patch("/api/v1/admin/sites/branch", json={"measured_upload_mbps": 20, "business_backup_budget_percent": 20})
    idle = _enroll(client, "live-pc-limit-idle", "branch")
    assert _list_row(client, idle["device_id"])["run_limit_kibps"] is None


def _expire_and_reap(lease_id: str) -> None:
    from app.admission import reap_expired_leases

    with SessionLocal() as db:
        db.get(WanAdmissionLease, lease_id).expires_at = datetime.now(timezone.utc) - timedelta(seconds=1)
        db.commit()
        assert reap_expired_leases(db) == 1


def test_a_lease_that_lapsed_mid_backup_is_taken_back_by_its_next_renewal(client):
    """Live: renewals failed for 80 s, the lease expired, and every later
    renewal was DENIED while the PC kept uploading -- the panel showed it
    OFFLINE with no progress until the backup ended."""
    _measure_sites(client)
    d = _enroll(client, "lapsed-pc", "branch")
    lease = _acquire(client, d, new_id())
    _expire_and_reap(lease["lease_id"])
    assert _status(client, d["device_id"])["backing_up"] is False
    r = client.post(f"/api/v1/agent/wan/leases/{lease['lease_id']}/renew", headers=_headers(d), json={"percent_done": 40, "bytes_done": 3_000_000_000, "total_bytes": 7_500_000_000})
    assert r.json()["status"] == "GRANTED", r.json()
    st = _status(client, d["device_id"])
    assert st["backing_up"] is True and st["online"] is True
    assert st["current_progress"]["bytes_done"] == 3_000_000_000


def test_a_lapsed_lease_whose_slot_was_taken_stays_denied_but_the_pc_is_seen(client):
    _measure_sites(client)
    a = _enroll(client, "lapsed-a", "branch")
    b = _enroll(client, "lapsed-b", "branch")
    lease = _acquire(client, a, new_id())
    _expire_and_reap(lease["lease_id"])
    _acquire(client, b, new_id())  # b took the site's only slot meanwhile
    _set_last_seen(a["device_id"], datetime.now(timezone.utc) - timedelta(hours=2))
    r = client.post(f"/api/v1/agent/wan/leases/{lease['lease_id']}/renew", headers=_headers(a))
    assert r.json()["status"] == "DENIED"
    seen = datetime.fromisoformat(client.get(f"/api/v1/admin/devices/{a['device_id']}").json()["device"]["last_seen_at"])
    if seen.tzinfo is None:
        seen = seen.replace(tzinfo=timezone.utc)
    assert datetime.now(timezone.utc) - seen < timedelta(minutes=1)


def test_the_overview_lists_user_messages_with_the_computer_they_came_from(client):
    login(client)
    d = _enroll(client, "msg-pc", "hq")
    client.patch(f"/api/v1/admin/devices/{d['device_id']}", json={"display_name": "reception pc"})
    r = client.post("/api/v1/agent/user-messages", headers=_headers(d), json={"message": "yedek başlamıyor"})
    assert r.status_code in (200, 201, 202), r.text
    msgs = client.get("/api/v1/admin/dashboard").json()["user_messages"]
    m = next(x for x in msgs if x["device_id"] == d["device_id"])
    assert m["message"] == "yedek başlamıyor"
    assert m["hostname"] == "msg-pc"
    assert m["site_id"] == "hq"
