"""Regression: gateway admin endpoints must use a constant-time token check.

The original implementation compared the Authorization header with a plain
Python `!=`, which short-circuits on the first differing byte and turns the
shared STOWLINE_GATEWAY_ADMIN_TOKEN into a remote timing oracle. This locks in
the *functional* behavior (right token works, wrong/missing token is
rejected) across the refactor to hmac.compare_digest, and confirms the
comparison function actually used is the constant-time one.
"""

from pathlib import Path

import pytest
from fastapi.testclient import TestClient
from gw import Registry, _admin_token_ok, create_app


@pytest.fixture()
def client(tmp_path: Path, monkeypatch):
    monkeypatch.setenv("STOWLINE_GATEWAY_ADMIN_TOKEN", "correct-horse-battery-staple")
    reg = Registry(None)
    app = create_app(tmp_path / "store", reg)
    with TestClient(app) as c:
        yield c


ADMIN_ROUTES = [
    ("POST", "/admin/register", {"device_id": "d", "generation_id": "g", "password_hash": "h"}),
    ("POST", "/admin/site-limits", {"site_id": "hq", "bytes_per_sec": 1000}),
    ("GET", "/admin/ingress", None),
    ("GET", "/admin/snapshots/aaaaaaaa/bbbbbbbb", None),
    ("GET", "/admin/usage", None),
]


@pytest.mark.parametrize("method,path,body", ADMIN_ROUTES)
def test_admin_route_accepts_correct_token(client, method, path, body):
    headers = {"Authorization": "Bearer correct-horse-battery-staple"}
    r = client.request(method, path, headers=headers, json=body)
    assert r.status_code == 200, r.text


@pytest.mark.parametrize("method,path,body", ADMIN_ROUTES)
def test_admin_route_rejects_wrong_token(client, method, path, body):
    headers = {"Authorization": "Bearer wrong-token-same-ish-length"}
    r = client.request(method, path, headers=headers, json=body)
    assert r.status_code == 401


@pytest.mark.parametrize("method,path,body", ADMIN_ROUTES)
def test_admin_route_rejects_missing_token(client, method, path, body):
    r = client.request(method, path, json=body)
    assert r.status_code == 401


def test_admin_snapshots_lists_real_ids_without_a_restic_password(client, tmp_path):
    """The control plane never holds a repository's restic password, so
    reconciliation has to work off object existence alone. Writes a fake
    snapshot object straight onto the store's disk (bypassing restic
    entirely, the way a real encrypted snapshot object would look to
    anyone without the password: just an opaque blob under a 64-hex-char
    name) and confirms the admin route reports its id back with no
    RESTIC_PASSWORD anywhere in this test."""
    snap_id = "a" * 64
    snap_dir = tmp_path / "store" / "dead1234" / "beef5678" / "snapshots"
    snap_dir.mkdir(parents=True)
    (snap_dir / snap_id).write_bytes(b"opaque-encrypted-bytes")
    (snap_dir / "not-an-id.tmp").write_bytes(b"junk")

    headers = {"Authorization": "Bearer correct-horse-battery-staple"}
    r = client.get("/admin/snapshots/dead1234/beef5678", headers=headers)
    assert r.status_code == 200, r.text
    body = r.json()
    assert body["snapshot_ids"] == [snap_id]


def test_admin_snapshots_uses_the_registered_storage_prefix_not_the_raw_device_id(client, tmp_path):
    """Regression: restic_proxy's handle() resolves the real repo path
    through the registered storage_prefix (e.g.
    "IT-QUALIFICATION/pc01--87de10fa"), not the raw device_id -- this
    route originally skipped that lookup and always used device_id
    directly, so it looked at a path nothing had ever been written to.
    Confirmed live against a real device: it reported all 6 recorded
    snapshots as missing from the repository when they demonstrably
    still existed."""
    headers = {"Authorization": "Bearer correct-horse-battery-staple"}
    client.post(
        "/admin/register",
        headers=headers,
        json={
            "device_id": "cafefeed",
            "generation_id": "deadbeef",
            "password_hash": "h",
            "storage_prefix": "Finance/pc01--cafefeed",
        },
    )
    snap_id = "b" * 64
    snap_dir = tmp_path / "store" / "Finance" / "pc01--cafefeed" / "deadbeef" / "snapshots"
    snap_dir.mkdir(parents=True)
    (snap_dir / snap_id).write_bytes(b"opaque-encrypted-bytes")

    r = client.get("/admin/snapshots/cafefeed/deadbeef", headers=headers)
    assert r.status_code == 200, r.text
    assert r.json()["snapshot_ids"] == [snap_id]


def test_admin_snapshots_empty_repo_is_not_an_error(client):
    headers = {"Authorization": "Bearer correct-horse-battery-staple"}
    r = client.get("/admin/snapshots/00000000/11111111", headers=headers)
    assert r.status_code == 200, r.text
    assert r.json()["snapshot_ids"] == []


def test_admin_token_check_uses_constant_time_compare(monkeypatch):
    """Pin the implementation, not just the behavior: a naive `!=` could
    regress back in without any of the functional tests above noticing."""
    import hmac
    import inspect

    import gw

    src = inspect.getsource(gw._admin_token_ok)
    assert "hmac.compare_digest" in src, "admin token check must use hmac.compare_digest, not =="
    assert " != " not in src.split("compare_digest")[0], "no plain != before the constant-time compare"

    monkeypatch.setenv("STOWLINE_GATEWAY_ADMIN_TOKEN", "tok")

    class FakeHeaders(dict):
        def get(self, key, default=None):
            return super().get(key.lower(), default)

    class FakeRequest:
        headers = FakeHeaders({"authorization": "Bearer tok"})

    assert _admin_token_ok(FakeRequest()) is True
    FakeRequest.headers = FakeHeaders({"authorization": "Bearer nope"})
    assert _admin_token_ok(FakeRequest()) is False
    FakeRequest.headers = FakeHeaders({})
    assert _admin_token_ok(FakeRequest()) is False


def test_admin_token_check_fails_closed_when_env_unset(monkeypatch):
    monkeypatch.delenv("STOWLINE_GATEWAY_ADMIN_TOKEN", raising=False)

    class FakeRequest:
        headers = {"authorization": "Bearer anything"}

    assert _admin_token_ok(FakeRequest()) is False
