"""How much data each registered repository holds (object sizes only)."""

import time
from pathlib import Path

import pytest
from fastapi.testclient import TestClient
from gw import Registry, create_app

HEADERS = {"Authorization": "Bearer correct-horse-battery-staple"}


@pytest.fixture()
def client(tmp_path: Path, monkeypatch):
    monkeypatch.setenv("STOWLINE_GATEWAY_ADMIN_TOKEN", "correct-horse-battery-staple")
    app = create_app(tmp_path / "store", Registry(None))
    with TestClient(app) as c:
        c.store_root = tmp_path / "store"
        yield c


def _register(client, device_id, generation, prefix="", revoked=False):
    body = {"device_id": device_id, "generation_id": generation, "password_hash": "h", "revoked": revoked}
    if prefix:
        body["storage_prefix"] = prefix
    assert client.post("/admin/register", headers=HEADERS, json=body).status_code == 200


def _write(root: Path, rel: str, size: int) -> None:
    f = root / rel
    f.parent.mkdir(parents=True, exist_ok=True)
    f.write_bytes(b"x" * size)


def test_usage_sums_every_object_of_a_registered_repository(client):
    _register(client, "cafefeed", "deadbeef", prefix="Finance/pc--cafefeed")
    root = client.store_root / "Finance" / "pc--cafefeed" / "deadbeef"
    _write(root, "data/aa" * 1 + "/" + "a" * 64, 1000)
    _write(root, "data/" + "b" * 64, 2500)
    _write(root, "index/" + "c" * 64, 300)
    _write(root, "config", 155)
    r = client.get("/admin/usage?wait=1", headers=HEADERS)
    assert r.status_code == 200, r.text
    body = r.json()
    assert body["available"] is True
    assert body["devices"]["cafefeed"] == {"bytes": 3955, "objects": 4, "generation_id": "deadbeef"}
    assert body["total_bytes"] == 3955
    assert body["measured_at"]


def test_usage_uses_the_registered_storage_prefix_not_the_raw_device_id(client):
    _register(client, "cafefeed", "deadbeef", prefix="Finance/pc--cafefeed")
    _write(client.store_root / "cafefeed" / "deadbeef", "data/" + "a" * 64, 999)  # wrong place: must be ignored
    _write(client.store_root / "Finance" / "pc--cafefeed" / "deadbeef", "data/" + "b" * 64, 10)
    assert client.get("/admin/usage?wait=1", headers=HEADERS).json()["devices"]["cafefeed"]["bytes"] == 10


def test_usage_counts_a_revoked_devices_data_and_several_devices_add_up(client):
    _register(client, "aaaaaaaa", "11111111", prefix="Finance/a--aaaaaaaa")
    _register(client, "bbbbbbbb", "22222222", prefix="Finance/b--bbbbbbbb", revoked=True)
    _write(client.store_root / "Finance" / "a--aaaaaaaa" / "11111111", "data/" + "a" * 64, 100)
    _write(client.store_root / "Finance" / "b--bbbbbbbb" / "22222222", "data/" + "b" * 64, 40)
    body = client.get("/admin/usage?wait=1", headers=HEADERS).json()
    assert body["devices"]["aaaaaaaa"]["bytes"] == 100 and body["devices"]["bbbbbbbb"]["bytes"] == 40
    assert body["total_bytes"] == 140


def test_an_empty_or_never_written_repository_counts_zero(client):
    _register(client, "cafefeed", "deadbeef")
    body = client.get("/admin/usage?wait=1", headers=HEADERS).json()
    assert body["devices"]["cafefeed"]["bytes"] == 0 and body["total_bytes"] == 0


def test_the_first_call_without_wait_answers_at_once_and_the_result_follows(client):
    _register(client, "cafefeed", "deadbeef")
    _write(client.store_root / "cafefeed" / "deadbeef", "data/" + "a" * 64, 42)
    first = client.get("/admin/usage", headers=HEADERS).json()
    assert first["available"] in (True, False)
    deadline = time.time() + 5
    body = first
    while not body.get("available") and time.time() < deadline:
        time.sleep(0.05)
        body = client.get("/admin/usage", headers=HEADERS).json()
    assert body["available"] is True and body["total_bytes"] == 42


def test_a_measurement_is_reused_until_it_goes_stale(client):
    _register(client, "cafefeed", "deadbeef")
    _write(client.store_root / "cafefeed" / "deadbeef", "data/" + "a" * 64, 5)
    first = client.get("/admin/usage?wait=1", headers=HEADERS).json()
    _write(client.store_root / "cafefeed" / "deadbeef", "data/" + "b" * 64, 500)  # written after the measurement
    again = client.get("/admin/usage", headers=HEADERS).json()
    assert again["measured_at"] == first["measured_at"] and again["total_bytes"] == 5
