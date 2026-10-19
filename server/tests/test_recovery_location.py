"""An operator's guide must resolve the namespace used at enrollment."""
from __future__ import annotations

from types import SimpleNamespace

import httpx
import pytest
from fastapi.testclient import TestClient

from test_api import _enroll, client, login  # noqa: F401, E402
from app import services  # noqa: E402
from app.db import Device, Repository  # noqa: E402
from app.main import SessionLocal  # noqa: E402
from app.security import new_id  # noqa: E402
from gw import Registry, create_app_with_store  # noqa: E402


def route(device):
    return f"/api/v1/admin/devices/{device['device_id']}/recovery-location"


def test_real_gateway_namespace_survives_department_and_hostname_changes(client, monkeypatch):
    registry = Registry(None)
    monkeypatch.setattr(services, "notify_gateway", registry.put_record)
    login(client)
    token = client.post("/api/v1/admin/enrollment-tokens", json={"department": "Original / Dept"}).json()["token"]
    device = client.post("/api/v1/agent/enrollments", json={"token": token, "hostname": "PC/old"}).json()
    prefix = registry.load()[device["device_id"]]["storage_prefix"]
    assert prefix.startswith("Original _ Dept/PC_old--")
    client.patch(f"/api/v1/admin/devices/{device['device_id']}", json={"department": "New Dept"})
    with SessionLocal() as db:
        db.get(Device, device["device_id"]).hostname = "NEW-HOST"
        db.commit()
    monkeypatch.setenv("STOWLINE_GATEWAY_ADMIN_TOKEN", "synthetic-gateway-token")
    monkeypatch.setattr(services.settings, "gateway_admin_url", "http://synthetic-gateway")
    monkeypatch.setattr(services.settings, "gateway_admin_token", "synthetic-gateway-token")
    gateway = create_app_with_store(SimpleNamespace(kind="rclone", remote_root="private-archive:custom/root"), registry)
    with TestClient(gateway) as gateway_client:
        def fetch(url, **kwargs):
            assert url == f"http://synthetic-gateway/admin/recovery-location/{device['device_id']}/{device['generation_id']}"
            return gateway_client.get(url, headers=kwargs["headers"])

        monkeypatch.setattr(services.httpx, "get", fetch)
        response = client.get(route(device))
    assert response.status_code == 200
    body = response.json()
    assert body["available"] is True
    assert body["location"] == f"private-archive:custom/root/{prefix}/{device['generation_id']}"
    assert "NEW-HOST" not in response.text and "New Dept" not in response.text
    assert "synthetic-gateway-token" not in response.text
    assert "password_hash" not in response.text
    assert response.headers["cache-control"] == "no-store"


@pytest.mark.parametrize("failure,reason", [
    ("transport", "gateway_unavailable"),
    ("parse", "invalid_gateway_response"),
    ("auth", "gateway_unavailable"),
    ("old_gateway", "gateway_upgrade_required"),
    ("generation", "generation_mismatch"),
    ("wrong_identity", "invalid_gateway_response"),
])
def test_unreliable_location_never_falls_back_to_labels(client, monkeypatch, failure, reason):
    login(client)
    device = _enroll(client, "recovery-missing", "branch")
    monkeypatch.setattr(services.settings, "gateway_admin_url", "http://synthetic-gateway")
    monkeypatch.setattr(services.settings, "gateway_admin_token", "synthetic-token")

    def fetch(*args, **kwargs):
        if failure == "transport":
            raise httpx.ConnectError("synthetic-secret must not be echoed")
        if failure == "parse":
            return httpx.Response(200, text="synthetic-secret invalid json")
        if failure in ("auth", "old_gateway"):
            return httpx.Response(401 if failure == "auth" else 404, text="synthetic-secret")
        if failure == "generation":
            return httpx.Response(200, json={"available": False, "reason": "generation_mismatch"})
        return httpx.Response(200, json={"available": True, "device_id": "other-device", "generation_id": device["generation_id"], "location": "synthetic-secret"})

    monkeypatch.setattr(services.httpx, "get", fetch)
    response = client.get(route(device))
    assert response.json() == {"schema_version": 1, "available": False, "reason": reason}
    assert "synthetic-secret" not in response.text
    assert "location" not in response.json()


def test_missing_configuration_and_repository_are_explicit(client, monkeypatch):
    login(client)
    device = _enroll(client, "recovery-no-config", "branch")
    monkeypatch.setattr(services.settings, "gateway_admin_url", "")
    assert client.get(route(device)).json()["reason"] == "gateway_not_configured"
    with SessionLocal() as db:
        repo = db.query(Repository).filter_by(device_id=device["device_id"]).one()
        db.delete(repo)
        db.commit()
    assert client.get(route(device)).json()["reason"] == "no_repository"


def test_multiple_generations_are_not_silently_chosen(client):
    login(client)
    device = _enroll(client, "recovery-generations", "branch")
    with SessionLocal() as db:
        original = db.query(Repository).filter_by(device_id=device["device_id"]).one()
        db.add(Repository(id=new_id(), device_id=original.device_id, storage_profile_id=original.storage_profile_id,
                          generation_id=new_id(), role="PRIMARY", location="rest:https://synthetic/repo"))
        db.commit()
    assert client.get(route(device)).json()["reason"] == "ambiguous_repository"


def test_gateway_response_only_exposes_approved_location_fields(client, monkeypatch):
    login(client)
    device = _enroll(client, "recovery-allowlist", "branch")
    monkeypatch.setattr(services.settings, "gateway_admin_url", "http://synthetic-gateway")
    monkeypatch.setattr(services.settings, "gateway_admin_token", "synthetic-token")
    allowed = {"available": True, "device_id": device["device_id"], "generation_id": device["generation_id"],
               "storage_kind": "rclone", "location": "named-archive:backup/immutable-prefix/generation", "prefix_source": "registry"}
    monkeypatch.setattr(services.httpx, "get", lambda *a, **kw: httpx.Response(200, json={**allowed, "password_hash": "synthetic-secret", "backup_key": "synthetic-secret"}))
    response = client.get(route(device))
    assert response.json() == {"schema_version": 1, **allowed}
    assert "synthetic-secret" not in response.text
    monkeypatch.setattr(services.httpx, "get", lambda *a, **kw: httpx.Response(200, json={"available": False, "reason": ["malformed"]}))
    assert client.get(route(device)).json()["reason"] == "invalid_gateway_response"


def test_recovery_location_requires_admin_session(client):
    assert client.get("/api/v1/admin/devices/not-known/recovery-location").status_code == 401
    login(client)
    assert client.get("/api/v1/admin/devices/not-known/recovery-location").status_code == 404
    setup = client.post("/api/v1/admin/setup-codes", json={"label": "installer"}).json()
    from app.main import app

    with TestClient(app) as installer:
        assert installer.post("/api/v1/setup/login", json={"code": setup["code"]}).status_code == 200
        assert installer.get("/api/v1/admin/devices/not-known/recovery-location").status_code == 401
