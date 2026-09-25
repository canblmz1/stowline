"""Regression: the gateway ran a single uvicorn worker whose async route
handlers called rclone's blocking subprocess.run() directly on the event
loop. Under concurrent restic traffic this monopolized the only event loop
-- even /health queued behind it -- and real "502 Bad Gateway" errors were
observed live under load. Fixed by (1) routing every store call through
run_in_threadpool (gw/__init__.py) so the event loop is never blocked, and
(2) bounding concurrent rclone subprocesses with a configurable semaphore
(gw/store.py's _RCLONE_GATE) instead of the plain mutex that used to allow
exactly one at a time -- or, if simply removed, could have let an
unbounded fleet spawn unlimited rclone processes.

These tests use a real rclone binary against a local-backend remote (no
network/Drive dependency, matching test_rclone_store.py's existing
pattern), with subprocess.run monkeypatched to a controlled sleep so
concurrency behavior is deterministic instead of depending on real disk
or network timing.
"""

from __future__ import annotations

import shutil
import threading
import time
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

import gw.store as store_module
from gw import Registry, create_app_with_store
from gw.store import RcloneStore, _RcloneGate

DEV_A = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
GEN1 = "11111111-1111-1111-1111-111111111111"
OBJ = "a" * 64


def auth(user, pw):
    import base64

    token = base64.b64encode(f"{user}:{pw}".encode()).decode()
    return {"Authorization": f"Basic {token}"}


def _slow_run_factory(delay_sec: float, calls: list):
    """A subprocess.run replacement: records call start/end wall-clock
    times and sleeps for delay_sec instead of touching disk or network --
    isolates the test from real rclone/filesystem speed."""

    class _Completed:
        returncode = 0
        stdout = b"[]"
        stderr = b""

    def fake_run(cmd, **kwargs):
        start = time.monotonic()
        time.sleep(delay_sec)
        end = time.monotonic()
        calls.append((start, end))
        return _Completed()

    return fake_run


@pytest.fixture()
def slow_rclone_app(monkeypatch, tmp_path: Path):
    """A real RcloneStore (local-backend rclone binary) whose actual
    subprocess call is replaced with a controlled sleep, wired into a real
    FastAPI app via the real ASGI route handlers -- this exercises the
    genuine event-loop/threadpool/semaphore behavior, not a mock of it.

    Skips instead of hard-failing when rclone isn't available, matching
    test_rclone_store.py's existing guard -- confirmed live: this fixture
    had no such guard, so a machine where C:\\Stowline\\bin\\rclone.exe
    doesn't currently exist (any repair/reset cycle on the live pilot
    machine this suite also runs on) turned "rclone not installed" into a
    hard error for every test here instead of a clean skip."""
    if shutil.which("rclone") is None and not Path(r"C:\Stowline\bin\rclone.exe").exists():
        pytest.skip("rclone not installed")
    calls: list = []
    monkeypatch.setattr(store_module.subprocess, "run", _slow_run_factory(0.4, calls))
    conf = tmp_path / "rclone.conf"
    conf.write_text("[qual]\ntype = local\n", encoding="utf-8")
    monkeypatch.setenv("RCLONE_CONFIG", str(conf))
    monkeypatch.delenv("RCLONE_CONFIG_PASS", raising=False)
    remote_root = "qual:" + str(tmp_path / "remote").replace("\\", "/")
    store = RcloneStore.from_env(remote_root)
    reg = Registry(None)
    reg.put(DEV_A, GEN1, "secret-a")
    app = create_app_with_store(store, reg)
    return app, calls


def _get_object(client: TestClient):
    return client.get(f"/restic/{DEV_A}/{GEN1}/data/{OBJ}", headers=auth(DEV_A, "secret-a"))


def test_health_stays_responsive_during_a_slow_rclone_request(slow_rclone_app, monkeypatch):
    monkeypatch.setenv("STOWLINE_GATEWAY_RCLONE_MAX_CONCURRENCY", "2")
    app, _calls = slow_rclone_app
    with TestClient(app) as c:
        baseline_t0 = time.monotonic()
        c.get("/health")
        baseline = time.monotonic() - baseline_t0

        results = {}

        def slow_request():
            results["slow_status"] = _get_object(c).status_code

        t = threading.Thread(target=slow_request)
        t.start()
        time.sleep(0.05)  # let the slow request actually enter the handler first

        t0 = time.monotonic()
        health_status = c.get("/health").status_code
        health_during_load = time.monotonic() - t0
        t.join(timeout=5)

    assert results["slow_status"] == 404  # empty local remote: object genuinely absent
    assert health_status == 200
    # The key regression check: /health must not be forced to wait for the
    # slow rclone call to finish. A blocked event loop would make this
    # roughly 0.4s (the slow call's sleep); a healthy one stays near baseline.
    assert health_during_load < 0.35, f"/health took {health_during_load:.3f}s during an active slow rclone call (baseline {baseline:.3f}s) -- event loop appears blocked"


def _max_pairwise_overlap(calls: list[tuple[float, float]]) -> float:
    """Largest time window during which at least two recorded calls were
    both in flight -- 0 or negative means every call ran fully alone."""
    best = 0.0
    for i, (s1, e1) in enumerate(calls):
        for s2, e2 in calls[i + 1 :]:
            best = max(best, min(e1, e2) - max(s1, s2))
    return best


def test_two_simultaneous_clients_make_progress_and_can_overlap(slow_rclone_app, monkeypatch):
    # Each GET makes 2 sequential rclone calls (prepare_repo's mkdir, then
    # is_file's lsjson) -- 2 concurrent requests make 4 calls total. Fully
    # serial (ceiling=1) would take 4 * 0.4s = 1.6s; a ceiling of 2 lets
    # each request's pair overlap with the other's, roughly halving that.
    monkeypatch.setenv("STOWLINE_GATEWAY_RCLONE_MAX_CONCURRENCY", "2")
    app, calls = slow_rclone_app
    with TestClient(app) as c:
        results = [None, None]

        def worker(i):
            results[i] = _get_object(c).status_code

        threads = [threading.Thread(target=worker, args=(i,)) for i in range(2)]
        wall_t0 = time.monotonic()
        for t in threads:
            t.start()
        for t in threads:
            t.join(timeout=5)
        wall_elapsed = time.monotonic() - wall_t0

    assert results == [404, 404]
    assert len(calls) == 4
    assert _max_pairwise_overlap(calls) > 0, "two clients under a concurrency-2 ceiling never actually overlapped"
    # Comfortably below the ~1.6s+ fully-serial time, with headroom for
    # thread-scheduling jitter -- the overlap assertion above is the precise
    # check; this is just a sanity margin on wall-clock time.
    assert wall_elapsed < 1.5, f"wall_elapsed={wall_elapsed:.3f}s -- looks fully serialized (~1.6s), not concurrent"


def test_concurrency_ceiling_is_enforced_and_configurable(slow_rclone_app, monkeypatch):
    """With the ceiling set to 1, two simultaneous requests for the same
    device must still both succeed (bounded queuing, not rejection) but
    every rclone call -- across both requests -- must run strictly alone,
    proving the limit is real, not just documentation."""
    monkeypatch.setenv("STOWLINE_GATEWAY_RCLONE_MAX_CONCURRENCY", "1")
    app, calls = slow_rclone_app
    with TestClient(app) as c:
        results = [None, None]

        def worker(i):
            results[i] = _get_object(c).status_code

        threads = [threading.Thread(target=worker, args=(i,)) for i in range(2)]
        wall_t0 = time.monotonic()
        for t in threads:
            t.start()
        for t in threads:
            t.join(timeout=5)
        wall_elapsed = time.monotonic() - wall_t0

    assert results == [404, 404], "both requests must still complete -- bounding must queue, not drop"
    assert len(calls) == 4
    assert _max_pairwise_overlap(calls) <= 0, "ceiling=1 must fully serialize every rclone call"
    assert wall_elapsed > 1.4, f"wall_elapsed={wall_elapsed:.3f}s -- four 0.4s calls under ceiling=1 must take roughly their serial sum (~1.6s)"


def test_gate_rebuilds_its_semaphore_when_the_configured_ceiling_changes():
    gate = _RcloneGate()
    import os

    os.environ["STOWLINE_GATEWAY_RCLONE_MAX_CONCURRENCY"] = "3"
    try:
        s1 = gate.acquire()
        gate.release(s1)
        os.environ["STOWLINE_GATEWAY_RCLONE_MAX_CONCURRENCY"] = "1"
        s2 = gate.acquire()
        gate.release(s2)
        assert gate._size == 1
    finally:
        del os.environ["STOWLINE_GATEWAY_RCLONE_MAX_CONCURRENCY"]


def test_default_ceiling_is_a_small_positive_number_not_unbounded():
    from gw.store import _rclone_max_concurrency

    assert 1 <= _rclone_max_concurrency() <= 8
