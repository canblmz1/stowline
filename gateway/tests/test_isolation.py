from pathlib import Path
import json
import os
import socket
import subprocess
import threading
import time
import urllib.error
import urllib.request

import pytest
from fastapi.testclient import TestClient
from gw import Registry, create_app, hash_secret

DEV_A = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
DEV_B = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
GEN1 = "11111111-1111-1111-1111-111111111111"
GEN2 = "22222222-2222-2222-2222-222222222222"


@pytest.fixture()
def client(tmp_path: Path):
    reg = Registry(None)
    reg.put(DEV_A, GEN1, "secret-a")
    reg.put(DEV_B, GEN1, "secret-b")
    app = create_app(tmp_path / "store", reg)
    with TestClient(app) as c:
        c.reg = reg
        yield c


def auth(user, pw):
    import base64

    token = base64.b64encode(f"{user}:{pw}".encode()).decode()
    return {"Authorization": f"Basic {token}"}


def test_a_can_write_own_repo(client):
    r = client.post(f"/restic/{DEV_A}/{GEN1}/config", headers=auth(DEV_A, "secret-a"), content=b'{"id":"aa"}')
    assert r.status_code == 200
    g = client.get(f"/restic/{DEV_A}/{GEN1}/config", headers=auth(DEV_A, "secret-a"))
    assert g.content == b'{"id":"aa"}'


def test_a_cannot_access_b(client):
    client.post(f"/restic/{DEV_B}/{GEN1}/config", headers=auth(DEV_B, "secret-b"), content=b"b")
    r = client.get(f"/restic/{DEV_B}/{GEN1}/config", headers=auth(DEV_A, "secret-a"))
    assert r.status_code == 403
    r = client.get(f"/restic/{DEV_A}/{GEN1}/config", headers=auth(DEV_B, "secret-b"))
    assert r.status_code == 403


def test_a_cannot_access_other_generation(client):
    r = client.get(f"/restic/{DEV_A}/{GEN2}/config", headers=auth(DEV_A, "secret-a"))
    assert r.status_code == 403


def test_invalid_token(client):
    r = client.get(f"/restic/{DEV_A}/{GEN1}/config", headers=auth(DEV_A, "wrong"))
    assert r.status_code == 401
    r = client.get(f"/restic/{DEV_A}/{GEN1}/config")
    assert r.status_code == 401


def test_traversal_blocked(client):
    r = client.get(f"/restic/{DEV_A}/{GEN1}/../{DEV_B}/{GEN1}/config", headers=auth(DEV_A, "secret-a"))
    assert r.status_code in {400, 403, 404}
    r = client.get(f"/restic/{DEV_A}/{GEN1}/%2e%2e/{DEV_B}/config", headers=auth(DEV_A, "secret-a"))
    assert r.status_code in {400, 403, 404}


def test_unicode_dotdot(client):
    r = client.get(f"/restic/{DEV_A}/{GEN1}/‥/x", headers=auth(DEV_A, "secret-a"))
    assert r.status_code in {400, 404}


def test_delete_data_blocked_lock_allowed(client):
    client.post(f"/restic/{DEV_A}/{GEN1}/data/ab/abcd", headers=auth(DEV_A, "secret-a"), content=b"pack")
    r = client.delete(f"/restic/{DEV_A}/{GEN1}/data/ab/abcd", headers=auth(DEV_A, "secret-a"))
    assert r.status_code == 403
    client.post(f"/restic/{DEV_A}/{GEN1}/locks/deadbeef", headers=auth(DEV_A, "secret-a"), content=b"lock")
    r = client.delete(f"/restic/{DEV_A}/{GEN1}/locks/deadbeef", headers=auth(DEV_A, "secret-a"))
    assert r.status_code == 200


def test_idempotent_same_bytes_allowed(client):
    r1 = client.post(f"/restic/{DEV_A}/{GEN1}/data/cd/cdef", headers=auth(DEV_A, "secret-a"), content=b"pack")
    r2 = client.post(f"/restic/{DEV_A}/{GEN1}/data/cd/cdef", headers=auth(DEV_A, "secret-a"), content=b"pack")
    assert r1.status_code == 200 and r2.status_code == 200
    client.post(f"/restic/{DEV_A}/{GEN1}/index/aa", headers=auth(DEV_A, "secret-a"), content=b"1")
    r = client.post(f"/restic/{DEV_A}/{GEN1}/index/aa", headers=auth(DEV_A, "secret-a"), content=b"2")
    assert r.status_code == 403


def test_revocation(client):
    client.reg.put(DEV_A, GEN1, "secret-a", revoked=True)
    r = client.get(f"/restic/{DEV_A}/{GEN1}/config", headers=auth(DEV_A, "secret-a"))
    assert r.status_code == 401


def test_double_encoded_traversal_blocked(client):
    r = client.get(f"/restic/{DEV_A}/{GEN1}/%252e%252e/{DEV_B}/config", headers=auth(DEV_A, "secret-a"))
    assert r.status_code in {400, 403, 404}


def test_generation_rotation_isolates_old_gen(client):
    client.post(f"/restic/{DEV_A}/{GEN1}/config", headers=auth(DEV_A, "secret-a"), content=b"g1")
    client.reg.put(DEV_A, GEN2, "secret-a2")
    assert client.get(f"/restic/{DEV_A}/{GEN1}/config", headers=auth(DEV_A, "secret-a")).status_code in {401, 403}
    assert client.get(f"/restic/{DEV_A}/{GEN2}/config", headers=auth(DEV_A, "secret-a")).status_code in {401, 403}
    assert client.post(f"/restic/{DEV_A}/{GEN2}/config", headers=auth(DEV_A, "secret-a2"), content=b"g2").status_code == 200
    assert client.get(f"/restic/{DEV_A}/{GEN2}/config", headers=auth(DEV_A, "secret-a2")).status_code == 200
    assert client.get(f"/restic/{DEV_A}/{GEN2}/config", headers=auth(DEV_B, "secret-b")).status_code == 403


def test_method_matrix_isolation(client):
    client.post(f"/restic/{DEV_A}/{GEN1}/data/aa/aaaa", headers=auth(DEV_A, "secret-a"), content=b"pack")
    for method in ("GET", "HEAD", "POST", "DELETE"):
        r = client.request(method, f"/restic/{DEV_B}/{GEN1}/data/aa/aaaa", headers=auth(DEV_A, "secret-a"), content=b"x")
        assert r.status_code in {400, 401, 403, 404, 405}, method
        r = client.request(method, f"/restic/{DEV_A}/{GEN2}/data/aa/aaaa", headers=auth(DEV_A, "secret-a"), content=b"x")
        assert r.status_code in {400, 401, 403, 404, 405}, method


def test_empty_post_rejected(client):
    r = client.post(f"/restic/{DEV_A}/{GEN1}", headers=auth(DEV_A, "secret-a"), content=b"x")
    assert r.status_code == 400


def test_create_true_makes_restic_layout(client, tmp_path: Path):
    r = client.post(
        f"/restic/{DEV_A}/{GEN1}/?create=true",
        headers=auth(DEV_A, "secret-a"),
        content=b"",
    )
    assert r.status_code == 200
    r2 = client.post(
        f"/restic/{DEV_A}/{GEN1}?create=true",
        headers=auth(DEV_A, "secret-a"),
        content=b"",
    )
    assert r2.status_code == 200
    store = tmp_path / "store" / DEV_A / GEN1
    for name in ("data", "index", "keys", "locks", "snapshots"):
        assert (store / name).is_dir()
    cfg = client.post(
        f"/restic/{DEV_A}/{GEN1}/config",
        headers=auth(DEV_A, "secret-a"),
        content=b'{"id":"created"}',
    )
    assert cfg.status_code == 200


def test_restic_rest_v2_empty_list_is_json_array(client):
    assert (
        client.post(
            f"/restic/{DEV_A}/{GEN1}/?create=true",
            headers=auth(DEV_A, "secret-a"),
            content=b"",
        ).status_code
        == 200
    )
    headers = {**auth(DEV_A, "secret-a"), "Accept": "application/vnd.x.restic.rest.v2"}
    listed = client.get(f"/restic/{DEV_A}/{GEN1}/keys/", headers=headers)
    assert listed.status_code == 200
    assert listed.json() == []
    client.post(f"/restic/{DEV_A}/{GEN1}/keys/aa", headers=auth(DEV_A, "secret-a"), content=b"k")
    listed = client.get(f"/restic/{DEV_A}/{GEN1}/keys/", headers=headers)
    assert listed.status_code == 200
    body = listed.json()
    assert body == [{"name": "aa", "size": 1}]
    client.post(
        f"/restic/{DEV_A}/{GEN1}/data/ab/abcdabcd",
        headers=auth(DEV_A, "secret-a"),
        content=b"pack-bytes",
    )
    data_list = client.get(f"/restic/{DEV_A}/{GEN1}/data/", headers=headers)
    assert data_list.status_code == 200
    names = [item["name"] for item in data_list.json()]
    assert "ab/abcdabcd" in names


def test_concurrent_different_bytes_denied(client):
    import threading

    codes = []

    def post(body):
        r = client.post(f"/restic/{DEV_A}/{GEN1}/data/ee/eeee", headers=auth(DEV_A, "secret-a"), content=body)
        codes.append(r.status_code)

    t1 = threading.Thread(target=post, args=(b"one-byte-body-aaaa",))
    t2 = threading.Thread(target=post, args=(b"two-byte-body-bbbb",))
    t1.start()
    t2.start()
    t1.join()
    t2.join()
    assert 200 in codes
    assert 403 in codes or codes.count(200) == 1
    got = client.get(f"/restic/{DEV_A}/{GEN1}/data/ee/eeee", headers=auth(DEV_A, "secret-a"))
    assert got.status_code == 200
    assert got.content in {b"one-byte-body-aaaa", b"two-byte-body-bbbb"}


def test_pinned_restic_init_backup_restore_through_gateway(tmp_path: Path):
    import hashlib
    import os
    import socket
    import subprocess
    import threading
    import time
    import urllib.error
    import urllib.request

    import uvicorn

    restic = Path(r"C:\Stowline\bin\restic.exe")
    if not restic.exists():
        pytest.skip("pinned restic missing")
    reg = Registry(None)
    reg.put(DEV_A, GEN1, "secret-a")
    app = create_app(tmp_path / "store", reg)
    sock = socket.socket()
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]
    sock.close()
    server = uvicorn.Server(uvicorn.Config(app, host="127.0.0.1", port=port, log_level="warning"))
    thread = threading.Thread(target=server.run, daemon=True)
    thread.start()
    health = f"http://127.0.0.1:{port}/health"
    for _ in range(50):
        try:
            urllib.request.urlopen(health, timeout=0.2)
            break
        except (urllib.error.URLError, TimeoutError, ConnectionError, OSError):
            time.sleep(0.1)
    else:
        pytest.fail("local gateway thread did not start")
    env = os.environ.copy()
    env["RESTIC_PASSWORD"] = "qual-test-password"
    env["RESTIC_REST_USERNAME"] = DEV_A
    env["RESTIC_REST_PASSWORD"] = "secret-a"
    env["RESTIC_CACHE_DIR"] = str(tmp_path / "cache")
    repo = f"rest:http://127.0.0.1:{port}/restic/{DEV_A}/{GEN1}"
    try:
        proc = subprocess.run(
            [str(restic), "init", "--repo", repo],
            env=env,
            capture_output=True,
            timeout=60,
            check=False,
        )
        assert proc.returncode == 0, proc.stderr.decode("utf-8", errors="replace") + proc.stdout.decode(
            "utf-8", errors="replace"
        )
        assert (tmp_path / "store" / DEV_A / GEN1 / "config").is_file()
        src = tmp_path / "corpus"
        src.mkdir()
        payload = b"stowline-gateway-range-restore\n" * 200
        (src / "canary.txt").write_bytes(payload)
        digest = hashlib.sha256(payload).hexdigest()
        backup = subprocess.run(
            [str(restic), "backup", "--no-cache", "corpus", "--repo", repo, "--tag", "stowline-range"],
            cwd=str(tmp_path),
            env=env,
            capture_output=True,
            timeout=120,
            check=False,
        )
        assert backup.returncode == 0, backup.stderr.decode("utf-8", errors="replace") + backup.stdout.decode(
            "utf-8", errors="replace"
        )
        repo_root = tmp_path / "store" / DEV_A / GEN1
        mismatches = []
        for folder in ("index", "snapshots", "data"):
            if not (repo_root / folder).exists():
                continue
            for blob in (repo_root / folder).rglob("*"):
                if not blob.is_file():
                    continue
                name = blob.name
                if len(name) != 64:
                    continue
                blob_digest = hashlib.sha256(blob.read_bytes()).hexdigest()
                if blob_digest != name:
                    mismatches.append(f"{blob.relative_to(repo_root)} disk={blob_digest}")
        assert not mismatches, "POST stored bytes that are not content-addressed: " + "; ".join(mismatches)
        auth_header = auth(DEV_A, "secret-a")
        for blob in (repo_root / "index").glob("*"):
            if not blob.is_file() or len(blob.name) != 64:
                continue
            url = f"http://127.0.0.1:{port}/restic/{DEV_A}/{GEN1}/index/{blob.name}"
            req = urllib.request.Request(url, headers={**auth_header, "Range": "bytes=0-", "Accept": "application/vnd.x.restic.rest.v2"})
            with urllib.request.urlopen(req, timeout=10) as resp:
                status = resp.status
                body = resp.read()
            assert hashlib.sha256(body).hexdigest() == blob.name, (
                f"GET Range bytes=0- of index/{blob.name} != SHA-256 name (status={status})"
            )
        check = subprocess.run(
            [str(restic), "check", "--no-cache", "--repo", repo],
            env=env,
            capture_output=True,
            timeout=120,
            check=False,
        )
        assert check.returncode == 0, check.stderr.decode("utf-8", errors="replace") + check.stdout.decode(
            "utf-8", errors="replace"
        )
        dest = tmp_path / "restore"
        dest.mkdir()
        restore = subprocess.run(
            [str(restic), "restore", "--no-cache", "latest", "--target", str(dest), "--repo", repo],
            env=env,
            capture_output=True,
            timeout=120,
            check=False,
        )
        assert restore.returncode == 0, restore.stderr.decode("utf-8", errors="replace") + restore.stdout.decode(
            "utf-8", errors="replace"
        )
        found = list(dest.rglob("canary.txt"))
        assert found, "staged restore missing canary.txt"
        got = found[0].read_bytes()
        assert got == payload
        assert hashlib.sha256(got).hexdigest() == digest
    finally:
        server.should_exit = True


