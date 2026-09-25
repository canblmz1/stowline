"""Setup codes let someone install computers without the admin password, and
the sign-in endpoints refuse to be guessed at full speed."""

from __future__ import annotations

from fastapi.testclient import TestClient

from test_api import client, login  # noqa: F401  — sets the test env; import first
from app import ratelimit  # noqa: E402
from app.main import app  # noqa: E402


def _code(client, **kw) -> dict:
    login(client)
    body = {"label": "reception install", "hours": 24, "max_uses": 3, **kw}
    r = client.post("/api/v1/admin/setup-codes", json=body)
    assert r.status_code == 200, r.text
    return r.json()


def _installer(code: str) -> TestClient:
    c = TestClient(app)
    r = c.post("/api/v1/setup/login", json={"code": code})
    assert r.status_code == 200, r.text
    return c


def _enroll(c: TestClient, site: str = "hq", host: str = "pc") -> str:
    tok = c.post("/api/v1/admin/enrollment-tokens", json={"label": "setup-wizard", "site_id": site, "department": "Finance"})
    assert tok.status_code == 200, tok.text
    d = c.post("/api/v1/agent/enrollments", json={"token": tok.json()["token"], "hostname": host, "agent_version": "0.1.0"})
    assert d.status_code == 200, d.text
    return d.json()["device_id"]


def test_code_is_shown_once_readable_and_listed(client):
    out = _code(client)
    code = out["code"]
    assert len(code) == 23 and code.count("-") == 3
    assert not set(code.replace("-", "")) & set("01OIL")
    listed = client.get("/api/v1/admin/setup-codes").json()["codes"]
    row = next(r for r in listed if r["id"] == out["id"])
    assert row["state"] == "ACTIVE" and row["uses"] == 0
    assert "code" not in row


def test_code_check_accepts_sloppy_typing_and_does_not_use_it_up(client):
    out = _code(client)
    sloppy = out["code"].lower().replace("-", " ")
    r = TestClient(app).post("/api/v1/setup/code-check", json={"code": sloppy})
    assert r.status_code == 200 and r.json()["valid"]
    assert TestClient(app).post("/api/v1/setup/code-check", json={"code": "AAAAA-BBBBB-CCCCC-DDDDD"}).status_code == 401
    row = next(r for r in client.get("/api/v1/admin/setup-codes").json()["codes"] if r["id"] == out["id"])
    assert row["uses"] == 0


def test_installer_session_can_enroll_and_configure_its_own_computer(client):
    code = _code(client)["code"]
    inst = _installer(code)
    dev = _enroll(inst, host="reception-01")
    assert inst.get(f"/api/v1/admin/devices/{dev}").status_code == 200
    r = inst.patch(f"/api/v1/admin/devices/{dev}", json={"display_name": "Reception", "department": "Sales", "preferred_start_hhmm": "12:30"})
    assert r.status_code == 200, r.text
    assert r.json()["device"]["display_name"] == "Reception"
    sel = inst.post(f"/api/v1/admin/devices/{dev}/selection", json={"selected": [], "applied_locally": True})
    assert sel.status_code in (200, 422)  # 422 = empty selection; the point is it is not 401/403/404


def test_installer_session_is_not_an_admin_session(client):
    code = _code(client)["code"]
    inst = _installer(code)
    for path in ("/api/v1/admin/devices", "/api/v1/admin/me", "/api/v1/admin/setup-codes", "/api/v1/admin/vault", "/api/v1/admin/dashboard"):
        assert inst.get(path).status_code == 401, path
    assert inst.post("/api/v1/admin/setup-codes", json={}).status_code == 401


def test_installer_cannot_touch_other_computers_or_change_other_fields(client):
    login(client)
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": "by-admin", "site_id": "hq"}).json()["token"]
    other = client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": "boss-pc", "agent_version": "0.1.0"}).json()["device_id"]
    inst = _installer(_code(client)["code"])
    assert inst.get(f"/api/v1/admin/devices/{other}").status_code == 404
    assert inst.patch(f"/api/v1/admin/devices/{other}", json={"display_name": "x"}).status_code == 404
    assert inst.post(f"/api/v1/admin/devices/{other}/selection", json={"selected": []}).status_code == 404
    mine = _enroll(inst, host="new-pc")
    assert inst.patch(f"/api/v1/admin/devices/{mine}", json={"pause_new_backups": True}).status_code == 403


def test_a_retry_with_the_same_code_can_still_reach_the_computer_it_enrolled(client):
    code = _code(client)["code"]
    dev = _enroll(_installer(code), host="retry-pc")
    again = _installer(code)
    assert again.get(f"/api/v1/admin/devices/{dev}").status_code == 200
    other_code = _installer(_code(client)["code"])
    assert other_code.get(f"/api/v1/admin/devices/{dev}").status_code == 404


def test_site_bound_code_only_enrolls_into_its_site(client):
    inst = _installer(_code(client, site_id="branch")["code"])
    r = inst.post("/api/v1/admin/enrollment-tokens", json={"label": "setup-wizard", "site_id": "hq"})
    assert r.status_code == 403
    assert inst.post("/api/v1/admin/enrollment-tokens", json={"label": "setup-wizard", "site_id": "branch"}).status_code == 200


def test_uses_run_out_and_revoke_stops_running_installs(client):
    out = _code(client, max_uses=1)
    inst = _installer(out["code"])
    assert TestClient(app).post("/api/v1/setup/login", json={"code": out["code"]}).status_code == 401
    listed = {r["id"]: r for r in client.get("/api/v1/admin/setup-codes").json()["codes"]}
    assert listed[out["id"]]["state"] == "USED_UP"
    assert client.post(f"/api/v1/admin/setup-codes/{out['id']}/revoke").json()["state"] == "REVOKED"
    assert inst.post("/api/v1/admin/enrollment-tokens", json={"label": "x", "site_id": "hq"}).status_code == 401


def test_login_is_rate_limited_per_ip_and_success_still_works_after(client):
    ratelimit.limiter.reset()
    c = TestClient(app)
    for _ in range(ratelimit.MAX_FAILURES_PER_IP):
        assert c.post("/api/v1/auth/login", json={"username": "admin", "password": "wrong"}).status_code == 401
    blocked = c.post("/api/v1/auth/login", json={"username": "admin", "password": "test-admin-pass"})
    assert blocked.status_code == 429
    assert int(blocked.headers["Retry-After"]) > 0
    ratelimit.limiter.reset()
    assert c.post("/api/v1/auth/login", json={"username": "admin", "password": "test-admin-pass"}).status_code == 200


def test_setup_code_guessing_is_rate_limited(client):
    ratelimit.limiter.reset()
    c = TestClient(app)
    for _ in range(ratelimit.MAX_FAILURES_PER_IP):
        assert c.post("/api/v1/setup/login", json={"code": "AAAAA-BBBBB-CCCCC-DDDDD"}).status_code == 401
    assert c.post("/api/v1/setup/code-check", json={"code": "AAAAA-BBBBB-CCCCC-DDDDD"}).status_code == 429
    ratelimit.limiter.reset()


def test_limiter_window_and_account_limit():
    now = [1000.0]
    lim = ratelimit.FailureLimiter(window=60, clock=lambda: now[0])
    for _ in range(3):
        lim.fail("k")
    assert lim.retry_after({"k": 3}) == 60
    now[0] += 30
    assert lim.retry_after({"k": 3}) == 30
    now[0] += 30
    assert lim.retry_after({"k": 3}) == 0
    lim.fail("a")
    lim.clear("a")
    assert lim.retry_after({"a": 1}) == 0


def test_cross_site_origin_is_refused_even_if_it_contains_the_host(client, monkeypatch):
    from app import main

    login(client)
    monkeypatch.setattr(main.settings, "lab_mode", False)
    bad = client.post("/api/v1/admin/setup-codes", json={}, headers={"Origin": "http://testserver.evil.example", "Host": "testserver"})
    assert bad.status_code == 403
    ok = client.post("/api/v1/admin/setup-codes", json={}, headers={"Origin": "http://testserver", "Host": "testserver"})
    assert ok.status_code == 200
