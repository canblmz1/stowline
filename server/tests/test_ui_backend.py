"""What the redesigned admin panel reads from the API."""
from __future__ import annotations

from test_api import _enroll, client, login  # noqa: F401  -- must precede any app import: sets the test env before app.config.settings is built

from app import schedule_policy  # noqa: E402
from app.db import User  # noqa: E402
from app.main import SessionLocal  # noqa: E402
from app.security import hash_secret  # noqa: E402

ADMIN_PW = "test-admin-pass"


def test_ui_assets_are_not_pinned_in_browser_cache(client):
    for path in ("/", "/ui/js/device.js"):
        response = client.get(path)
        assert response.status_code == 200
        assert response.headers["cache-control"] == "no-store, max-age=0"
        assert response.headers["pragma"] == "no-cache"


def _hdr(d: dict) -> dict:
    return {"Authorization": f"Bearer {d['control_credential']}"}


def _attempt(client, d: dict, outcome: str, slot: str) -> None:
    r = client.post("/api/v1/agent/jobs/events", headers=_hdr(d), json={"outcome": outcome, "slot_key": slot})
    assert r.status_code == 200, r.text


def test_device_list_rows_carry_what_the_panel_needs(client):
    login(client)
    d = _enroll(client, "ui-list", "branch")
    row = next(x for x in client.get("/api/v1/admin/devices").json()["items"] if x["id"] == d["device_id"])
    for key in ("backing_up", "backup_started_at", "last_error_human", "traffic_light", "site_policy", "health", "online", "next_eligible_run"):
        assert key in row, key
    assert row["backing_up"] is False
    assert row["site_policy"]["hard_stop"]


def test_dashboard_recent_attempts_name_the_computer_and_skip_inactive_ones(client):
    login(client)
    live = _enroll(client, "ui-dash-live", "branch")
    gone = _enroll(client, "ui-dash-gone", "branch")
    _attempt(client, live, "SUCCEEDED", "ui-dash-1")
    _attempt(client, gone, "SUCCEEDED", "ui-dash-2")
    client.post(f"/api/v1/admin/devices/{gone['device_id']}/archive")
    recent = client.get("/api/v1/admin/dashboard").json()["recent_attempts"]
    names = {a["hostname"] for a in recent}
    assert "ui-dash-live" in names
    assert "ui-dash-gone" not in names
    first = next(a for a in recent if a["hostname"] == "ui-dash-live")
    assert first["device_id"] == live["device_id"] and first["site_id"] == "branch"


def test_dashboard_sites_carry_their_schedule(client):
    login(client)
    sites = {s["id"]: s for s in client.get("/api/v1/admin/dashboard").json()["sites"]}
    assert sites["hq"]["schedule"]["hard_stop"] == "21:00"
    assert sites["branch"]["schedule"]["eligibility_start"]


def test_backups_list_names_the_computer_and_hides_inactive_ones(client):
    login(client)
    live = _enroll(client, "ui-bk-live", "branch")
    gone = _enroll(client, "ui-bk-gone", "branch")
    _attempt(client, live, "PARTIAL", "ui-bk-1")
    _attempt(client, gone, "PARTIAL", "ui-bk-2")
    client.post(f"/api/v1/admin/devices/{gone['device_id']}/revoke")
    items = client.get("/api/v1/admin/backups").json()["items"]
    assert any(a["hostname"] == "ui-bk-live" and a["device_id"] == live["device_id"] for a in items)
    assert not any(a["hostname"] == "ui-bk-gone" for a in items)


def test_hr_is_a_known_department():
    assert schedule_policy.normalize_department("hr") == "HR"
    assert schedule_policy.normalize_department("Finance") == "Finance"


# --- changing the admin password -------------------------------------------------------


def _restore_admin_password() -> None:
    with SessionLocal() as db:
        db.query(User).filter(User.username == "admin").one().password_hash = hash_secret(ADMIN_PW)
        db.commit()


def test_the_admin_can_change_the_password_and_the_old_one_stops_working(client):
    login(client)
    try:
        r = client.post("/api/v1/admin/me/password", json={"current_password": ADMIN_PW, "new_password": "another-strong-pass-1"})
        assert r.status_code == 200, r.text
        client.post("/api/v1/auth/logout")
        assert client.post("/api/v1/auth/login", json={"username": "admin", "password": ADMIN_PW}).status_code != 200
        assert client.post("/api/v1/auth/login", json={"username": "admin", "password": "another-strong-pass-1"}).status_code == 200
        events = client.get("/api/v1/admin/audit-events").json()["items"]
        assert any(e["action"] == "password_change" and e["result"] == "OK" for e in events)
        assert "another-strong-pass-1" not in client.get("/api/v1/admin/audit-events").text
    finally:
        _restore_admin_password()


def test_a_wrong_current_password_is_refused_and_audited(client):
    login(client)
    r = client.post("/api/v1/admin/me/password", json={"current_password": "nope-nope-nope", "new_password": "another-strong-pass-1"})
    assert r.status_code == 403
    events = client.get("/api/v1/admin/audit-events").json()["items"]
    assert any(e["action"] == "password_change" and e["result"] == "DENIED" for e in events)


def test_a_weak_or_unchanged_new_password_is_refused(client):
    login(client)
    short = client.post("/api/v1/admin/me/password", json={"current_password": ADMIN_PW, "new_password": "short"})
    same = client.post("/api/v1/admin/me/password", json={"current_password": ADMIN_PW, "new_password": ADMIN_PW})
    assert short.status_code == 422 and "12" in short.json()["detail"]
    assert same.status_code == 422
    assert client.post("/api/v1/auth/login", json={"username": "admin", "password": ADMIN_PW}).status_code == 200


def test_changing_the_password_needs_a_login(client):
    assert client.post("/api/v1/admin/me/password", json={"current_password": ADMIN_PW, "new_password": "another-strong-pass-1"}).status_code == 401


# --- Drive usage (measured by the gateway) ------------------------------------------------


def test_drive_usage_counts_only_active_computers_in_the_total(monkeypatch, client):
    from app import services

    login(client)
    live = _enroll(client, "ui-use-live", "branch")
    gone = _enroll(client, "ui-use-gone", "branch")
    client.post(f"/api/v1/admin/devices/{gone['device_id']}/archive")
    monkeypatch.setattr(
        services,
        "fetch_gateway_usage",
        lambda: {
            "available": True,
            "measured_at": "2026-09-19T07:00:00+00:00",
            "total_bytes": 700,
            "devices": {live["device_id"]: {"bytes": 500, "objects": 5}, gone["device_id"]: {"bytes": 200, "objects": 2}},
        },
    )
    body = client.get("/api/v1/admin/usage").json()
    assert body["available"] is True
    assert body["total_bytes"] == 500 and body["all_bytes"] == 700
    assert body["devices"][live["device_id"]] == {"bytes": 500, "objects": 5}
    assert body["measured_at"] == "2026-09-19T07:00:00+00:00"


def test_drive_usage_reports_when_the_gateway_has_nothing_yet(monkeypatch, client):
    from app import services

    login(client)
    monkeypatch.setattr(services, "fetch_gateway_usage", lambda: {"available": False, "measuring": True})
    assert client.get("/api/v1/admin/usage").json() == {"schema_version": 1, "available": False, "measuring": True}
    monkeypatch.setattr(services, "fetch_gateway_usage", lambda: {})
    assert client.get("/api/v1/admin/usage").json()["available"] is False


def test_drive_usage_needs_a_login(client):
    assert client.get("/api/v1/admin/usage").status_code == 401


def test_attempts_carry_a_readable_error_title_next_to_the_raw_class(client):
    login(client)
    d = _enroll(client, "ui-err-title", "branch")
    for cls, slot in (("VSS", "ui-err-1"), ("TRANSPORT", "ui-err-2"), ("CANCELLED", "ui-err-3")):
        r = client.post("/api/v1/agent/jobs/events", headers=_hdr(d), json={"outcome": "FAILED", "error_class": cls, "slot_key": slot})
        assert r.status_code == 200, r.text
    _attempt(client, d, "SUCCEEDED", "ui-err-4")
    rows = [x for x in client.get("/api/v1/admin/backups").json()["items"] if x["device_id"] == d["device_id"]]
    titles = {x["error_class"]: x["error_title"] for x in rows}
    assert titles == {
        "VSS": "VSS anlık görüntüsü alınamadı",
        "TRANSPORT": "Yedekleme başarısız",
        "CANCELLED": "Yedekleme iptal edildi",
        "": "",
    }
    detail = client.get(f"/api/v1/admin/devices/{d['device_id']}").json()
    assert {a["error_title"] for a in detail["attempts"]} == set(titles.values())


def test_restore_requests_name_the_computer_and_hide_inactive_ones(client):
    login(client)
    live = _enroll(client, "ui-rs-live", "branch")
    gone = _enroll(client, "ui-rs-gone", "branch")
    snap = "e" * 64
    for d, slot in ((live, "ui-rs-1"), (gone, "ui-rs-2")):
        client.post("/api/v1/agent/jobs/events", headers=_hdr(d), json={"outcome": "SUCCEEDED", "snapshot_id": snap, "slot_key": slot})
        ok = client.post("/api/v1/admin/restore-requests", json={"device_id": d["device_id"], "snapshot_id": snap, "selections": ["/C/data"]})
        assert ok.status_code == 200, ok.text
    assert client.post(f"/api/v1/admin/devices/{gone['device_id']}/archive").status_code == 200
    items = client.get("/api/v1/admin/restore-requests").json()["items"]
    names = {x["hostname"] for x in items}  # the database is shared with other tests, so look only at ours
    assert "ui-rs-live" in names and "ui-rs-gone" not in names
    assert next(x for x in items if x["hostname"] == "ui-rs-live")["device_id"] == live["device_id"]


def test_audit_events_name_the_computer_they_are_about(client):
    login(client)
    d = _enroll(client, "ui-audit-name", "branch")
    cmd = client.post(f"/api/v1/admin/devices/{d['device_id']}/commands", json={"kind": "RUN_BACKUP", "payload": {}})
    assert cmd.status_code == 200, cmd.text
    items = client.get("/api/v1/admin/audit-events").json()["items"]
    made = next(e for e in items if e["action"] == "command_create" and e["device_id"] == d["device_id"])
    assert made["hostname"] == "ui-audit-name" and made["detail"] == "RUN_BACKUP"
    enrolled = next(e for e in items if e["action"] == "enroll" and e["resource_id"] == d["device_id"])
    assert enrolled["device_id"] == d["device_id"] and enrolled["hostname"] == "ui-audit-name" and enrolled["detail"] == ""
    signed_in = next(e for e in items if e["action"] == "login")
    assert signed_in["device_id"] == "" and signed_in["hostname"] == ""
    # nothing secret rides along: the raw safe_diff is never exposed
    assert "safe_diff" not in made and "fingerprint" not in client.get("/api/v1/admin/audit-events").text


def test_the_setup_catalog_is_public_and_carries_sites_and_departments(client):
    """A generic installer reads this before anyone signs in."""
    r = client.get("/api/v1/setup/catalog")
    assert r.status_code == 200
    body = r.json()
    ids = {s["id"] for s in body["sites"]}
    assert {"hq", "branch"} <= ids
    assert all("display_name" in s and "eligibility_start" in s for s in body["sites"])
    assert body["departments"]
    assert "org_name" in body
    assert "devices" not in body
