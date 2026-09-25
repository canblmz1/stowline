from __future__ import annotations

from pathlib import Path

from fastapi.testclient import TestClient

from gw import Registry, create_app

DEV_A = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
GEN1 = "11111111-1111-1111-1111-111111111111"
PAYLOAD = b"ABCDEFGHIJKLMNOPQRSTUVWXYZ012345"  # 32 bytes


def auth(user="secret-a"):
    import base64

    token = base64.b64encode(f"{DEV_A}:{user}".encode()).decode()
    return {"Authorization": f"Basic {token}"}


def client(tmp_path: Path) -> TestClient:
    reg = Registry(None)
    reg.put(DEV_A, GEN1, "secret-a")
    app = create_app(tmp_path / "store", reg)
    return TestClient(app)


def test_get_full_head_404_and_ranges(tmp_path):
    c = client(tmp_path)
    path = f"/restic/{DEV_A}/{GEN1}/data/ab/abcdabcd"
    assert c.get(path, headers=auth()).status_code == 404
    assert c.head(path, headers=auth()).status_code == 404
    assert c.post(path, headers=auth(), content=PAYLOAD).status_code == 200

    full = c.get(path, headers=auth())
    assert full.status_code == 200
    assert full.content == PAYLOAD
    assert full.headers["content-length"] == str(len(PAYLOAD))
    assert full.headers.get("accept-ranges") == "bytes"

    head = c.head(path, headers=auth())
    assert head.status_code == 200
    assert head.content in {b"", PAYLOAD}  # httpx/starlette may omit body
    assert head.headers["content-length"] == str(len(PAYLOAD))
    assert head.headers.get("accept-ranges") == "bytes"

    first = c.get(path, headers={**auth(), "Range": "bytes=0-9"})
    assert first.status_code == 206
    assert first.content == PAYLOAD[:10]
    assert first.headers["content-range"] == f"bytes 0-9/{len(PAYLOAD)}"
    assert first.headers["content-length"] == "10"

    mid = c.get(path, headers={**auth(), "Range": "bytes=10-19"})
    assert mid.status_code == 206
    assert mid.content == PAYLOAD[10:20]
    assert mid.headers["content-range"] == f"bytes 10-19/{len(PAYLOAD)}"

    suffix = c.get(path, headers={**auth(), "Range": "bytes=-6"})
    assert suffix.status_code == 206
    assert suffix.content == PAYLOAD[-6:]
    assert suffix.headers["content-range"] == f"bytes 26-31/{len(PAYLOAD)}"

    open_end = c.get(path, headers={**auth(), "Range": "bytes=30-"})
    assert open_end.status_code == 206
    assert open_end.content == PAYLOAD[30:]

    whole = c.get(path, headers={**auth(), "Range": "bytes=0-"})
    assert whole.status_code in {200, 206}
    assert whole.content == PAYLOAD
    if whole.status_code == 206:
        assert whole.headers["content-range"] == f"bytes 0-{len(PAYLOAD) - 1}/{len(PAYLOAD)}"

    unsat = c.get(path, headers={**auth(), "Range": "bytes=99-120"})
    assert unsat.status_code == 416
    assert unsat.headers["content-range"] == f"bytes */{len(PAYLOAD)}"


def test_content_addressed_index_roundtrip(tmp_path):
    import hashlib
    import os

    c = client(tmp_path)
    payload = os.urandom(4096)
    # Windows text-mode writes would expand these and break SHA-256 names.
    payload = payload[:100] + b"\n\n\r\n" + payload[100:]
    digest = hashlib.sha256(payload).hexdigest()
    path = f"/restic/{DEV_A}/{GEN1}/index/{digest}"
    assert c.post(path, headers=auth(), content=payload).status_code == 200
    on_disk = tmp_path / "store" / DEV_A / GEN1 / "index" / digest
    assert on_disk.read_bytes() == payload
    assert hashlib.sha256(on_disk.read_bytes()).hexdigest() == digest
    got = c.get(path, headers={**auth(), "Range": "bytes=0-"})
    assert got.status_code in {200, 206}
    assert got.content == payload
    assert hashlib.sha256(got.content).hexdigest() == digest
    head = c.head(path, headers=auth())
    assert head.status_code == 200
    assert head.headers["content-length"] == str(len(payload))
