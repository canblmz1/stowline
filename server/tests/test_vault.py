"""The control plane's optional escrow copy of each computer's backup key.

A computer that dies takes its Restic password with it unless a copy exists
somewhere else. The control plane can hold one, sealed with a key that is not
in the database, shown only after the operator re-enters the admin password,
and every view is audited. These tests pin that: the secret is never returned
by a listing, never written to the database or the audit log in the clear, and
never reachable without the password.
"""
from __future__ import annotations

import pytest
from cryptography.fernet import Fernet
from sqlalchemy import select

from test_api import _enroll, client, login  # noqa: F401  -- must precede any app import: sets the test env before app.config.settings is built

from app import services, vault  # noqa: E402
from app.db import DeviceSecret  # noqa: E402
from app.main import SessionLocal  # noqa: E402

KEY_A = "3f9a1c22b07e5d11" * 4  # synthetic 64-hex, same shape as the real one
KEY_B = "0123456789abcdef" * 4
ADMIN_PW = "test-admin-pass"


@pytest.fixture()
def vault_on(monkeypatch):
    monkeypatch.setattr(services.settings, "escrow_key", Fernet.generate_key().decode())
    vault.reset_throttle()
    yield
    vault.reset_throttle()


def _h(d: dict) -> dict:
    return {"Authorization": f"Bearer {d['control_credential']}"}


def _agent_put(client, d: dict, secret: str = KEY_A):
    return client.put("/api/v1/agent/escrow", headers=_h(d), json={"kind": "restic-password", "secret": secret})


def _reveal(client, device_id: str, password: str):
    return client.post(f"/api/v1/admin/devices/{device_id}/secret/reveal", json={"password": password})


def _row(device_id: str) -> DeviceSecret | None:
    with SessionLocal() as db:
        return db.scalar(select(DeviceSecret).where(DeviceSecret.device_id == device_id))


# --- the pure helpers -----------------------------------------------------------


def test_fingerprint_matches_the_format_the_escrow_tool_prints():
    # scripts/key-escrow Get-KeyFingerprint: SHA-256 of the UTF-8 text, first 12 hex characters, upper case, groups of 4.
    assert vault.fingerprint("abc") == "BA78-16BF-8F01"


@pytest.mark.parametrize("bad", ["", "short", "has a space inside 12345678", "new\nline12345678", "türkçe-şifre-123456", "x" * 257])
def test_secrets_of_the_wrong_shape_are_refused(bad):
    with pytest.raises(vault.VaultError):
        vault.validate_secret(bad)


def test_a_real_shaped_key_is_accepted_and_outer_whitespace_is_dropped():
    assert vault.validate_secret(f"  {KEY_A}\r\n") == KEY_A


def test_sealing_round_trips_and_hides_the_plaintext(vault_on):
    token = vault.seal(KEY_A)
    assert KEY_A not in token
    assert vault.unseal(token) == KEY_A
    assert vault.seal(KEY_A) != token  # fresh nonce every time


def test_nothing_can_be_sealed_without_a_vault_key(monkeypatch):
    monkeypatch.setattr(services.settings, "escrow_key", "")
    assert vault.enabled() is False
    with pytest.raises(vault.VaultNotConfigured):
        vault.seal(KEY_A)
    monkeypatch.setattr(services.settings, "escrow_key", "not-a-fernet-key")
    assert vault.enabled() is False


def test_a_token_sealed_under_another_key_cannot_be_opened(monkeypatch, vault_on):
    token = vault.seal(KEY_A)
    monkeypatch.setattr(services.settings, "escrow_key", Fernet.generate_key().decode())
    with pytest.raises(vault.VaultError):
        vault.unseal(token)


def test_failed_attempts_lock_out_and_expire():
    vault.reset_throttle()
    for i in range(vault.FAILURE_LIMIT):
        assert vault.throttle_remaining("u1", now=1000.0 + i) == 0
        vault.record_failure("u1", now=1000.0 + i)
    assert vault.throttle_remaining("u1", now=1010.0) > 0
    assert vault.throttle_remaining("u2", now=1010.0) == 0  # per user
    assert vault.throttle_remaining("u1", now=1000.0 + vault.FAILURE_WINDOW_SECONDS + 10) == 0
    vault.record_failure("u3", now=1.0)
    vault.clear_failures("u3")
    assert vault.throttle_remaining("u3", now=2.0) == 0
    vault.reset_throttle()


# --- storing a key ----------------------------------------------------------------


def test_the_vault_reports_when_it_is_not_configured(monkeypatch, client):
    monkeypatch.setattr(services.settings, "escrow_key", "")
    login(client)
    d = _enroll(client, "vault-off", "branch")
    r = _agent_put(client, d)
    assert r.status_code == 503
    assert "kasa" in r.json()["detail"].lower()
    assert client.get("/api/v1/admin/vault").json()["enabled"] is False


def test_a_device_stores_its_own_key_and_the_admin_only_ever_sees_the_fingerprint(client, vault_on):
    login(client)
    d = _enroll(client, "vault-a", "branch")
    r = _agent_put(client, d)
    assert r.status_code == 200, r.text
    assert r.json()["fingerprint"] == vault.fingerprint(KEY_A)

    listing = client.get("/api/v1/admin/vault")
    detail = client.get(f"/api/v1/admin/devices/{d['device_id']}")
    audit_events = client.get("/api/v1/admin/audit-events")
    for response in (listing, detail, audit_events):
        assert KEY_A not in response.text
    item = next(x for x in listing.json()["items"] if x["device_id"] == d["device_id"])
    assert item["has_key"] is True and item["fingerprint"] == vault.fingerprint(KEY_A) and item["source"] == "device"
    assert detail.json()["vault"]["has_key"] is True

    row = _row(d["device_id"])
    assert row is not None and KEY_A not in row.ciphertext


def test_the_agent_status_endpoint_never_returns_the_secret(client, vault_on):
    login(client)
    d = _enroll(client, "vault-status", "branch")
    assert client.get("/api/v1/agent/escrow", headers=_h(d)).json()["has_key"] is False
    _agent_put(client, d)
    r = client.get("/api/v1/agent/escrow", headers=_h(d))
    assert r.json() == {**r.json(), "has_key": True, "fingerprint": vault.fingerprint(KEY_A)}
    assert KEY_A not in r.text


def test_a_device_can_only_write_its_own_key(client, vault_on):
    login(client)
    a = _enroll(client, "vault-own-a", "branch")
    b = _enroll(client, "vault-own-b", "branch")
    _agent_put(client, a)
    assert client.get("/api/v1/agent/escrow", headers=_h(b)).json()["has_key"] is False
    assert _row(b["device_id"]) is None


def test_storing_again_replaces_the_key_and_keeps_the_first_date(client, vault_on):
    login(client)
    d = _enroll(client, "vault-replace", "branch")
    _agent_put(client, d, KEY_A)
    first = _row(d["device_id"])
    _agent_put(client, d, KEY_B)
    second = _row(d["device_id"])
    assert second.fingerprint == vault.fingerprint(KEY_B) != first.fingerprint
    assert second.created_at == first.created_at
    assert _reveal(client, d["device_id"], ADMIN_PW).json()["secret"] == KEY_B


def test_the_operator_can_store_a_key_by_hand(client, vault_on):
    login(client)
    d = _enroll(client, "vault-hand", "branch")
    r = client.put(f"/api/v1/admin/devices/{d['device_id']}/secret", json={"secret": f" {KEY_A} \n"})
    assert r.status_code == 200, r.text
    assert r.json()["source"] == "operator"
    assert _reveal(client, d["device_id"], ADMIN_PW).json()["secret"] == KEY_A


@pytest.mark.parametrize("bad", ["", "short", "has space 1234567890"])
def test_an_unusable_secret_is_refused_with_422(client, vault_on, bad):
    login(client)
    d = _enroll(client, "vault-bad", "branch")
    assert _agent_put(client, d, bad).status_code == 422
    assert client.put(f"/api/v1/admin/devices/{d['device_id']}/secret", json={"secret": bad}).status_code == 422
    assert _row(d["device_id"]) is None


def test_only_the_restic_password_kind_exists(client, vault_on):
    login(client)
    d = _enroll(client, "vault-kind", "branch")
    r = client.put("/api/v1/agent/escrow", headers=_h(d), json={"kind": "ssh-key", "secret": KEY_A})
    assert r.status_code == 422


# --- showing a key ---------------------------------------------------------------------


def test_showing_a_key_needs_the_admin_password_and_the_refusal_is_audited(client, vault_on):
    login(client)
    d = _enroll(client, "vault-wrong", "branch")
    _agent_put(client, d)
    r = _reveal(client, d["device_id"], "not-the-password")
    assert r.status_code == 403
    assert KEY_A not in r.text
    assert _row(d["device_id"]).reveal_count in (0, None)
    events = client.get("/api/v1/admin/audit-events").json()["items"]
    denied = [e for e in events if e["action"] == "secret_reveal" and e["resource_id"] == d["device_id"]]
    assert denied and denied[0]["result"] == "DENIED"


def test_a_shown_key_is_audited_without_the_key_and_never_cached(client, vault_on):
    login(client)
    d = _enroll(client, "vault-show", "branch")
    _agent_put(client, d)
    r = _reveal(client, d["device_id"], ADMIN_PW)
    assert r.status_code == 200, r.text
    assert r.json() == {"secret": KEY_A, "fingerprint": vault.fingerprint(KEY_A)}
    assert r.headers["cache-control"] == "no-store"
    row = _row(d["device_id"])
    assert row.reveal_count == 1 and row.last_revealed_at is not None
    events = client.get("/api/v1/admin/audit-events")
    assert KEY_A not in events.text
    ok = [e for e in events.json()["items"] if e["action"] == "secret_reveal" and e["resource_id"] == d["device_id"] and e["result"] == "OK"]
    assert ok


def test_showing_a_key_that_was_never_stored_is_404(client, vault_on):
    login(client)
    d = _enroll(client, "vault-none", "branch")
    assert _reveal(client, d["device_id"], ADMIN_PW).status_code == 404
    assert _reveal(client, "no-such-device", ADMIN_PW).status_code == 404


def test_repeated_wrong_passwords_lock_the_vault_even_for_the_right_one(client, vault_on):
    login(client)
    d = _enroll(client, "vault-lock", "branch")
    _agent_put(client, d)
    for _ in range(vault.FAILURE_LIMIT):
        assert _reveal(client, d["device_id"], "nope").status_code == 403
    locked = _reveal(client, d["device_id"], ADMIN_PW)
    assert locked.status_code == 429
    assert KEY_A not in locked.text


def test_a_successful_show_clears_earlier_failures(client, vault_on):
    login(client)
    d = _enroll(client, "vault-clear", "branch")
    _agent_put(client, d)
    for _ in range(vault.FAILURE_LIMIT - 1):
        _reveal(client, d["device_id"], "nope")
    assert _reveal(client, d["device_id"], ADMIN_PW).status_code == 200
    for _ in range(vault.FAILURE_LIMIT - 1):
        assert _reveal(client, d["device_id"], "nope").status_code == 403


def test_a_key_sealed_under_a_lost_vault_key_reports_a_clear_error(monkeypatch, client, vault_on):
    login(client)
    d = _enroll(client, "vault-lost", "branch")
    _agent_put(client, d)
    monkeypatch.setattr(services.settings, "escrow_key", Fernet.generate_key().decode())
    r = _reveal(client, d["device_id"], ADMIN_PW)
    assert r.status_code == 409
    assert "çözülemedi" in r.json()["detail"]


# --- who can reach it ---------------------------------------------------------------------


def test_nothing_is_reachable_without_credentials(client, vault_on):
    assert client.get("/api/v1/admin/vault").status_code == 401
    assert client.put("/api/v1/admin/devices/x/secret", json={"secret": KEY_A}).status_code == 401
    assert client.post("/api/v1/admin/devices/x/secret/reveal", json={"password": ADMIN_PW}).status_code == 401
    assert client.put("/api/v1/agent/escrow", json={"kind": "restic-password", "secret": KEY_A}).status_code == 401
    assert client.get("/api/v1/agent/escrow").status_code == 401


def test_a_revoked_device_can_no_longer_write_a_key(client, vault_on):
    login(client)
    d = _enroll(client, "vault-revoked", "branch")
    client.post(f"/api/v1/admin/devices/{d['device_id']}/revoke")
    assert _agent_put(client, d).status_code == 401


# --- the overview -----------------------------------------------------------------------------


def test_the_overview_lists_active_devices_without_a_key_as_missing(client, vault_on):
    login(client)
    d = _enroll(client, "vault-missing", "branch")
    item = next(x for x in client.get("/api/v1/admin/vault").json()["items"] if x["device_id"] == d["device_id"])
    assert item["has_key"] is False and item["fingerprint"] == ""
    assert item["hostname"] == "vault-missing"


def test_an_archived_device_keeps_its_key_in_the_overview(client, vault_on):
    login(client)
    d = _enroll(client, "vault-archived", "branch")
    _agent_put(client, d)
    client.post(f"/api/v1/admin/devices/{d['device_id']}/archive")
    item = next(x for x in client.get("/api/v1/admin/vault").json()["items"] if x["device_id"] == d["device_id"])
    assert item["has_key"] is True and item["lifecycle"] == "ARCHIVED"


def test_an_archived_device_without_a_key_is_left_out(client, vault_on):
    login(client)
    d = _enroll(client, "vault-archived-empty", "branch")
    client.post(f"/api/v1/admin/devices/{d['device_id']}/archive")
    assert d["device_id"] not in {x["device_id"] for x in client.get("/api/v1/admin/vault").json()["items"]}


def test_the_agent_pushing_the_same_key_again_on_restart_is_a_silent_no_op(client, vault_on):
    # The agent pushes its key on every service start; an unchanged key must
    # not flood Olaylar with "Anahtar kaydedildi" or move the saved date.
    login(client)
    d = _enroll(client, "vault-same-again", "branch")
    assert _agent_put(client, d, KEY_A).status_code == 200
    first = _row(d["device_id"])
    assert _agent_put(client, d, KEY_A).status_code == 200
    again = _row(d["device_id"])
    assert again.updated_at == first.updated_at
    events = [e for e in client.get("/api/v1/admin/audit-events").json()["items"] if e["action"] == "secret_store" and e["device_id"] == d["device_id"]]
    assert len(events) == 1
