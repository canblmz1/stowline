from __future__ import annotations

import asyncio
import time

from fastapi.testclient import TestClient

from gw import Registry, create_app
from gw.limiter import SiteLimiter, TokenBucket, read_limited_body

DEV_A = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
DEV_B = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
GEN1 = "11111111-1111-1111-1111-111111111111"


def auth(user, pw):
    import base64

    token = base64.b64encode(f"{user}:{pw}".encode()).decode()
    return {"Authorization": f"Basic {token}"}


def _client(tmp_path, limits):
    reg = Registry(None)
    reg.put(DEV_A, GEN1, "secret-a", site_id="hq")
    reg.put(DEV_B, GEN1, "secret-b", site_id="branch")
    app = create_app(tmp_path / "store", reg, site_limits=limits)
    return TestClient(app), app


def test_site_buckets_do_not_share_tokens(tmp_path):
    client, app = _client(tmp_path, {"hq": 1_000_000, "branch": 4_000_000})
    payload = b"x" * 32_000
    assert client.post(f"/restic/{DEV_A}/{GEN1}/data/aa/aaaa", headers=auth(DEV_A, "secret-a"), content=payload).status_code == 200
    assert client.post(f"/restic/{DEV_B}/{GEN1}/data/bb/bbbb", headers=auth(DEV_B, "secret-b"), content=payload).status_code == 200
    snap = app.state.limiter.snapshot()
    assert snap["sites"]["hq"]["bytes"] == 32_000
    assert snap["sites"]["branch"]["bytes"] == 32_000
    assert snap["sites"]["hq"]["rate_bps_configured"] == 1_000_000
    assert snap["sites"]["branch"]["rate_bps_configured"] == 4_000_000


def test_x_site_header_is_ignored(tmp_path):
    client, app = _client(tmp_path, {"hq": 1_000_000, "branch": 4_000_000})
    headers = {**auth(DEV_A, "secret-a"), "X-Site": "branch"}
    assert client.post(f"/restic/{DEV_A}/{GEN1}/data/cc/cccc", headers=headers, content=b"n" * 8000).status_code == 200
    snap = app.state.limiter.snapshot()
    assert snap["sites"]["hq"]["bytes"] == 8000
    assert "branch" not in snap["sites"]


def test_two_senders_share_one_site_bucket(tmp_path):
    # 50_000 B/s with ~64KiB total from two POSTs. Burst is 16384, so wall time is measurable.
    rate = 50_000
    client, app = _client(tmp_path, {"hq": rate})
    chunk = b"z" * 32_000
    started = time.monotonic()
    codes = []

    import threading

    def send(name):
        r = client.post(f"/restic/{DEV_A}/{GEN1}/data/dd/{name}", headers=auth(DEV_A, "secret-a"), content=chunk)
        codes.append(r.status_code)

    t1 = threading.Thread(target=send, args=("dddd",))
    t2 = threading.Thread(target=send, args=("eeee",))
    t1.start()
    t2.start()
    t1.join()
    t2.join()
    elapsed = time.monotonic() - started
    assert codes == [200, 200] or set(codes) <= {200, 403}
    snap = app.state.limiter.snapshot()
    taken = snap["sites"]["hq"]["bytes"]
    assert taken >= 32_000
    observed = taken / max(elapsed, 1e-3)
    # Short tests include burst; stay within 60% of configured rate after the first burst.
    assert observed < rate * 2.5
    assert elapsed > (taken - 65536) / rate * 0.4


def test_streaming_limiter_consumes_before_body_complete():
    events: list[str] = []

    class State:
        pass

    class FakeRequest:
        def __init__(self):
            self.state = State()
            self._n = 0

        async def _receive(self):
            if self._n < 4:
                self._n += 1
                events.append(f"arrive-{self._n - 1}")
                return {"type": "http.request", "body": b"abcd" * 1024, "more_body": self._n < 4}
            return {"type": "http.request", "body": b"", "more_body": False}

        async def body(self):
            parts = []
            while True:
                message = await self._receive()
                parts.append(message.get("body") or b"")
                if not message.get("more_body"):
                    break
            return b"".join(parts)

    limiter = SiteLimiter({"hq": 10_000_000})

    async def run():
        return await read_limited_body(FakeRequest(), limiter, "hq", DEV_A)

    body = asyncio.run(run())
    assert len(body) == 4 * 4096
    assert events == ["arrive-0", "arrive-1", "arrive-2", "arrive-3"]


def test_streaming_events_interleave_tokens():
    class State:
        limit_events = None

    class FakeRequest:
        def __init__(self):
            self.state = State()
            self._n = 0

        async def _receive(self):
            seq = [b"one", b"two"]
            if self._n < len(seq):
                chunk = seq[self._n]
                self._n += 1
                return {"type": "http.request", "body": chunk, "more_body": self._n < len(seq)}
            return {"type": "http.request", "body": b"", "more_body": False}

        async def body(self):
            parts = []
            while True:
                message = await self._receive()
                parts.append(message.get("body") or b"")
                if not message.get("more_body"):
                    break
            return b"".join(parts)

    async def run():
        req = FakeRequest()
        limiter = SiteLimiter({"hq": 10_000_000})
        await read_limited_body(req, limiter, "hq", DEV_A)
        return req.state.limit_events

    ev = asyncio.run(run())
    assert ev == ["chunk", "token", "chunk", "token"]


def test_token_bucket_rate_within_tolerance():
    bucket = TokenBucket(20_000, burst=1024)
    n = 40_000

    async def run():
        t0 = time.monotonic()
        await bucket.take(n)
        return time.monotonic() - t0

    elapsed = asyncio.run(run())
    observed = n / max(elapsed, 1e-6)
    assert 12_000 <= observed <= 40_000
