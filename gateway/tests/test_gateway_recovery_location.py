"""Recovery uses the gateway's real root and durable registry, never labels."""
from __future__ import annotations

from types import SimpleNamespace

import pytest
from fastapi.testclient import TestClient

from gw import Registry, create_app, create_app_with_store

DEVICE = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
GENERATION = "11111111-1111-1111-1111-111111111111"
OTHER_GENERATION = "22222222-2222-2222-2222-222222222222"
HEADERS = {"Authorization": "Bearer synthetic-admin-token"}
ROUTE = f"/admin/recovery-location/{DEVICE}/{GENERATION}"


def remote_app(root="company-archive:backups/office west", prefix="Finance/PC_sanitized--aaaaaaaa"):
    registry = Registry(None)
    registry.put_record(DEVICE, GENERATION, "synthetic-password-hash", storage_prefix=prefix)
    # No rclone process is needed to resolve a registered location. These
    # tests intentionally check configuration/registry behavior, not I/O.
    store = SimpleNamespace(kind="rclone", remote_root=root)
    return create_app_with_store(store, registry), registry


@pytest.fixture(autouse=True)
def admin_token(monkeypatch):
    monkeypatch.setenv("STOWLINE_GATEWAY_ADMIN_TOKEN", "synthetic-admin-token")


def test_custom_root_uses_registered_sanitized_prefix_and_generation():
    app, registry = remote_app()
    # Revoking a device leaves its backups recoverable by an administrator.
    registry.put_record(DEVICE, GENERATION, "changed-hash", revoked=True)
    with TestClient(app) as client:
        response = client.get(ROUTE, headers=HEADERS)
    assert response.status_code == 200
    assert response.json() == {
        "available": True,
        "device_id": DEVICE,
        "generation_id": GENERATION,
        "storage_kind": "rclone",
        "location": f"company-archive:backups/office west/Finance/PC_sanitized--aaaaaaaa/{GENERATION}",
        "prefix_source": "registry",
    }
    assert "hash" not in response.text
    assert "synthetic-admin-token" not in response.text
    assert response.headers["cache-control"] == "no-store"


def test_legacy_registration_uses_immutable_device_id():
    app, _ = remote_app(prefix="")
    with TestClient(app) as client:
        body = client.get(ROUTE, headers=HEADERS).json()
    assert body["prefix_source"] == "legacy_device_id"
    assert body["location"].endswith(f"/{DEVICE}/{GENERATION}")


def test_local_store_reports_gateway_filesystem_location_without_creating_repository(tmp_path):
    registry = Registry(None)
    registry.put_record(DEVICE, GENERATION, "hash", storage_prefix="Old Dept/PC--aaaaaaaa")
    root = tmp_path / "actual-store"
    with TestClient(create_app(root, registry)) as client:
        body = client.get(ROUTE, headers=HEADERS).json()
    assert body["storage_kind"] == "local"
    assert body["location"] == str(root.resolve() / "Old Dept" / "PC--aaaaaaaa" / GENERATION)
    assert not (root / "Old Dept").exists()


@pytest.mark.parametrize("headers", [{}, {"Authorization": "Bearer wrong"}, {"Authorization": "Basic x"}])
def test_location_is_admin_only(headers):
    app, _ = remote_app()
    with TestClient(app) as client:
        response = client.get(ROUTE, headers=headers)
    assert response.status_code == 401
    assert "company-archive" not in response.text


@pytest.mark.parametrize("prefix", ["../../escape", "Finance\\PC", "Finance/\nPC", "Finance/C:PC"])
def test_invalid_registry_prefix_does_not_guess_another_location(prefix):
    app, _ = remote_app(prefix=prefix)
    with TestClient(app) as client:
        assert client.get(ROUTE, headers=HEADERS).json() == {"available": False, "reason": "invalid_storage_prefix"}


def test_missing_record_and_generation_mismatch_are_explicit():
    app, registry = remote_app()
    with TestClient(app) as client:
        body = client.get(f"/admin/recovery-location/{DEVICE}/{OTHER_GENERATION}", headers=HEADERS).json()
        assert body == {"available": False, "reason": "generation_mismatch"}
        registry.memory = {}
        assert client.get(ROUTE, headers=HEADERS).json() == {"available": False, "reason": "not_registered"}


@pytest.mark.parametrize("root", [":s3,access_key_id=hidden,secret_access_key=secret:bucket", "remote\nsecret:root"])
def test_inline_remote_credentials_or_invalid_root_are_never_disclosed(root):
    app, _ = remote_app(root=root)
    with TestClient(app) as client:
        response = client.get(ROUTE, headers=HEADERS)
    assert response.json() == {"available": False, "reason": "unsupported_storage_root"}
    assert "secret" not in response.text
