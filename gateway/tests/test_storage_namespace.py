"""Phase 1 storage namespace: a registered device's data lands under a
human-readable Drive prefix (<department>/<hostname>--<short-id>) instead
of the raw device_id, without changing what the device authenticates as
or what URL the agent talks to. See services.py's compute_storage_prefix
and gw/__init__.py's handle()/_safe_storage_prefix/Registry.put_record.
"""

from __future__ import annotations

import base64
from pathlib import Path

import pytest
from fastapi.testclient import TestClient
from gw import Registry, _safe_storage_prefix, create_app

DEV_A = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
DEV_B = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
GEN_A = "11111111-1111-1111-1111-111111111111"
GEN_B = "22222222-2222-2222-2222-222222222222"


def auth(user, pw):
    token = base64.b64encode(f"{user}:{pw}".encode()).decode()
    return {"Authorization": f"Basic {token}"}


@pytest.fixture()
def app_and_store(tmp_path: Path):
    reg = Registry(None)
    store_root = tmp_path / "store"
    app = create_app(store_root, reg)
    return app, store_root, reg


def _register(client, device_id, generation_id, password_hash, **extra):
    body = {"device_id": device_id, "generation_id": generation_id, "password_hash": password_hash}
    body.update(extra)
    r = client.post("/admin/register", json=body, headers={"Authorization": "Bearer test-admin-token"})
    assert r.status_code == 200, r.text


@pytest.fixture(autouse=True)
def admin_token(monkeypatch):
    monkeypatch.setenv("STOWLINE_GATEWAY_ADMIN_TOKEN", "test-admin-token")


def test_a_device_writes_under_its_assigned_department_prefix(app_and_store):
    from gw import hash_secret

    app, store_root, _reg = app_and_store
    with TestClient(app) as c:
        _register(c, DEV_A, GEN_A, hash_secret("secret-a"), storage_prefix="Finance/FIN-PC01--a31f82c4")
        r = c.post(f"/restic/{DEV_A}/{GEN_A}/config", headers=auth(DEV_A, "secret-a"), content=b'{"id":"aa"}')
        assert r.status_code == 200, r.text

    assert (store_root / "Finance" / "FIN-PC01--a31f82c4" / GEN_A / "config").is_file()
    assert not (store_root / DEV_A).exists(), "must not ALSO land under the raw device_id once a prefix is assigned"


def test_one_device_cannot_request_another_devices_path(app_and_store):
    from gw import hash_secret

    app, _store_root, _reg = app_and_store
    with TestClient(app) as c:
        _register(c, DEV_A, GEN_A, hash_secret("secret-a"), storage_prefix="Finance/FIN-PC01--a31f82c4")
        _register(c, DEV_B, GEN_B, hash_secret("secret-b"), storage_prefix="Muhasebe/MUHASEBE-PC01--b2c3d4e5")

        # Device A's own credentials, but B's identity in the URL.
        r = c.post(f"/restic/{DEV_B}/{GEN_B}/config", headers=auth(DEV_A, "secret-a"), content=b'{"id":"evil"}')
        assert r.status_code == 403, r.text

        # A's credentials against A's device_id but B's generation_id.
        r = c.post(f"/restic/{DEV_A}/{GEN_B}/config", headers=auth(DEV_A, "secret-a"), content=b'{"id":"evil"}')
        assert r.status_code == 403, r.text


def test_traversal_in_a_stored_prefix_is_rejected_not_followed():
    """Defense in depth on the registry value itself (services.py already
    sanitizes at computation time; this covers registry.json being hand-
    edited or written by something else)."""
    assert _safe_storage_prefix("../../etc") == ""
    assert _safe_storage_prefix("Finance/../../etc") == ""
    assert _safe_storage_prefix("Finance\\..\\..\\etc") == ""
    assert _safe_storage_prefix("") == ""
    assert _safe_storage_prefix(None) == ""
    assert _safe_storage_prefix("Finance/FIN-PC01--a31f82c4") == "Finance/FIN-PC01--a31f82c4"


def test_changing_department_does_not_relocate_an_existing_repository(app_and_store):
    """department is a control-plane-only concept (Device.department);
    the gateway registry's storage_prefix is set once, at enrollment, and
    nothing re-sends it afterward -- confirmed here at the gateway's own
    boundary: registering again without storage_prefix must carry the
    existing one forward, not blank it or leave it open to a later,
    unrelated call silently moving it."""
    from gw import hash_secret

    app, store_root, _reg = app_and_store
    with TestClient(app) as c:
        _register(c, DEV_A, GEN_A, hash_secret("secret-a"), storage_prefix="Finance/FIN-PC01--a31f82c4")
        r = c.post(f"/restic/{DEV_A}/{GEN_A}/config", headers=auth(DEV_A, "secret-a"), content=b'{"id":"aa"}')
        assert r.status_code == 200, r.text

        # e.g. a credential renewal/revocation cycle re-registering the
        # device without ever mentioning storage_prefix again.
        _register(c, DEV_A, GEN_A, hash_secret("secret-a-2"))
        r = c.get(f"/restic/{DEV_A}/{GEN_A}/config", headers=auth(DEV_A, "secret-a-2"))
        assert r.status_code == 200, r.text

    assert (store_root / "Finance" / "FIN-PC01--a31f82c4" / GEN_A / "config").is_file()


def test_old_device_with_no_storage_prefix_still_resolves_by_device_id(app_and_store):
    """Backwards compatibility: a device registered before this feature
    existed has no storage_prefix in the registry at all -- it must keep
    resolving exactly where it always has."""
    from gw import hash_secret

    app, store_root, _reg = app_and_store
    with TestClient(app) as c:
        _register(c, DEV_A, GEN_A, hash_secret("secret-a"))  # no storage_prefix, like a pre-existing record
        r = c.post(f"/restic/{DEV_A}/{GEN_A}/config", headers=auth(DEV_A, "secret-a"), content=b'{"id":"aa"}')
        assert r.status_code == 200, r.text

    assert (store_root / DEV_A / GEN_A / "config").is_file()
