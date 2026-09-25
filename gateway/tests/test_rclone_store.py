from __future__ import annotations

import os
import shutil
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from gw import Registry, create_app, create_app_with_store, hash_secret
from gw.store import RcloneStore, parse_gateway_root, redact_text, store_from_root

DEV_A = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
GEN1 = "11111111-1111-1111-1111-111111111111"


def auth(user, pw):
    import base64

    token = base64.b64encode(f"{user}:{pw}".encode()).decode()
    return {"Authorization": f"Basic {token}"}


def test_parse_gateway_root_rclone_and_local():
    kind, value = parse_gateway_root("rclone:stowline-drive:repositories/_qualification/run1")
    assert kind == "rclone"
    assert value == "stowline-drive:repositories/_qualification/run1"
    kind, value = parse_gateway_root(r"C:\data\store")
    assert kind == "local"
    with pytest.raises(ValueError):
        parse_gateway_root("rclone:nopath")


def test_redact_config_pass_never_leaks():
    os.environ["RCLONE_CONFIG_PASS"] = "super-secret-unlock-value"
    try:
        msg = redact_text("failed RCLONE_CONFIG_PASS=super-secret-unlock-value boom")
        assert "super-secret-unlock-value" not in msg
        assert "[REDACTED]" in msg
    finally:
        del os.environ["RCLONE_CONFIG_PASS"]


def test_local_store_default_create_app(tmp_path: Path):
    reg = Registry(None)
    reg.put(DEV_A, GEN1, "secret-a")
    app = create_app(tmp_path / "store", reg)
    with TestClient(app) as c:
        assert c.get("/health").json()["store"] == "local"
        r = c.post(f"/restic/{DEV_A}/{GEN1}/config", headers=auth(DEV_A, "secret-a"), content=b'{"id":"aa"}')
        assert r.status_code == 200


@pytest.mark.skipif(shutil.which("rclone") is None and not Path(r"C:\Stowline\bin\rclone.exe").exists(), reason="rclone not installed")
def test_rclone_local_backend_roundtrip(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    conf = tmp_path / "rclone.conf"
    conf.write_text("[qual]\ntype = local\n", encoding="utf-8")
    monkeypatch.setenv("RCLONE_CONFIG", str(conf))
    monkeypatch.delenv("RCLONE_CONFIG_PASS", raising=False)
    remote_root = "qual:" + str(tmp_path / "remote").replace("\\", "/")
    store = RcloneStore.from_env(remote_root)
    reg = Registry(None)
    reg.put(DEV_A, GEN1, "secret-a")
    app = create_app_with_store(store, reg)
    with TestClient(app) as c:
        assert c.get("/health").json()["store"] == "rclone"
        created = c.post(
            f"/restic/{DEV_A}/{GEN1}/?create=true",
            headers=auth(DEV_A, "secret-a"),
            content=b"",
        )
        assert created.status_code == 200, created.text
        r = c.post(f"/restic/{DEV_A}/{GEN1}/config", headers=auth(DEV_A, "secret-a"), content=b'{"id":"aa"}')
        assert r.status_code == 200, r.text
        g = c.get(f"/restic/{DEV_A}/{GEN1}/config", headers=auth(DEV_A, "secret-a"))
        assert g.content == b'{"id":"aa"}'
        r2 = c.post(f"/restic/{DEV_A}/{GEN1}/config", headers=auth(DEV_A, "secret-a"), content=b'{"id":"aa"}')
        assert r2.status_code == 200
        r3 = c.post(f"/restic/{DEV_A}/{GEN1}/config", headers=auth(DEV_A, "secret-a"), content=b'{"id":"bb"}')
        assert r3.status_code == 403
        binary = b"pack\nwith\nnewlines\r\nand\x00null"
        c.post(f"/restic/{DEV_A}/{GEN1}/data/ab/abcdabcd", headers=auth(DEV_A, "secret-a"), content=binary)
        listed = c.get(f"/restic/{DEV_A}/{GEN1}/data/ab/", headers=auth(DEV_A, "secret-a"))
        assert "abcdabcd" in listed.text
        got = c.get(f"/restic/{DEV_A}/{GEN1}/data/ab/abcdabcd", headers=auth(DEV_A, "secret-a"))
        assert got.content == binary
        assert got.headers.get("content-length") == str(len(binary))
        v2 = c.get(
            f"/restic/{DEV_A}/{GEN1}/data/",
            headers={**auth(DEV_A, "secret-a"), "Accept": "application/vnd.x.restic.rest.v2"},
        )
        assert v2.status_code == 200
        assert "ab/abcdabcd" in [item["name"] for item in v2.json()]
        c.post(f"/restic/{DEV_A}/{GEN1}/locks/deadbeef", headers=auth(DEV_A, "secret-a"), content=b"lock")
        assert c.delete(f"/restic/{DEV_A}/{GEN1}/locks/deadbeef", headers=auth(DEV_A, "secret-a")).status_code == 200
        assert c.delete(f"/restic/{DEV_A}/{GEN1}/data/ab/abcdabcd", headers=auth(DEV_A, "secret-a")).status_code == 403


def test_rclone_calls_carry_generous_retry_and_backoff_flags(monkeypatch: pytest.MonkeyPatch):
    """Regression, confirmed live: a burst of "rclone transport error"
    (502) on restic's own lock create/delete churn exhausted rclone's
    default 3 retries with 0s sleep between them -- no gap for a
    transient Drive-side rejection to clear before the next identical
    attempt. Every rclone invocation must ask for real retries with a
    real backoff, not rely on defaults that retry immediately."""
    import gw.store as store_module

    captured: list[list[str]] = []

    class _Completed:
        returncode = 0
        stdout = b"[]"
        stderr = b""

    def fake_run(cmd, **kwargs):
        captured.append(cmd)
        return _Completed()

    monkeypatch.setattr(store_module.subprocess, "run", fake_run)
    store = RcloneStore("remote:path", "rclone")
    store.is_file("some/key")

    assert captured, "subprocess.run was never called"
    cmd = captured[0]
    assert "--retries" in cmd and cmd[cmd.index("--retries") + 1] not in ("0", "1")
    assert "--retries-sleep" in cmd and cmd[cmd.index("--retries-sleep") + 1] != "0s"
    assert "--low-level-retries" in cmd


def test_store_from_root_local(tmp_path: Path):
    store = store_from_root(str(tmp_path / "x"))
    assert store.kind == "local"


def test_health_does_not_include_secrets(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("RCLONE_CONFIG_PASS", "must-not-appear")
    reg = Registry(None)
    app = create_app(tmp_path / "store", reg)
    with TestClient(app) as c:
        body = c.get("/health").text
        assert "must-not-appear" not in body
        assert "RCLONE_CONFIG_PASS" not in body


def test_hash_secret_stable_prefix():
    assert hash_secret("x").startswith("g1$")
