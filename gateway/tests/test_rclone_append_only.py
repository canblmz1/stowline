"""Append-only is a byte contract, including uncertain metadata and competing writers."""
from __future__ import annotations

import json
import multiprocessing
import os
import shutil
import subprocess
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

import pytest
from fastapi import HTTPException

from gw.store import RcloneStore


@pytest.mark.parametrize("code,out", [
    (1, b""), (2, b""), (5, b""), (6, b""), (7, b""),
    (0, b""), (0, b"not json"), (0, b"\xff"), (0, b"[]"),
    (0, b'{"Name":"other","IsDir":false,"Size":8}'),
    (0, b'{"Name":"config","IsDir":"false","Size":8}'),
])
def test_uncertain_metadata_never_reaches_upload(tmp_path, monkeypatch, code, out):
    monkeypatch.setenv("STOWLINE_GATEWAY_LOCK_DIR", str(tmp_path / "locks"))
    store = RcloneStore("qual:repository", "rclone")
    calls = []

    def run(args, **kwargs):
        calls.append(args)
        return code, out, b"private transport/auth diagnostic"

    monkeypatch.setattr(store, "_run", run)
    with pytest.raises(HTTPException) as error:
        store.write_exclusive("device/generation/config", b"replacement")
    assert error.value.status_code == 502
    assert "private" not in str(error.value.detail)
    assert all(args[0] == "lsjson" for args in calls)


@pytest.mark.parametrize("body,expected", [(b"original", "same"), (b"different", 403)])
def test_existing_bytes_preserved(tmp_path, monkeypatch, body, expected):
    monkeypatch.setenv("STOWLINE_GATEWAY_LOCK_DIR", str(tmp_path / "locks"))
    store = RcloneStore("qual:repository", "rclone")
    calls = []

    def run(args, **kwargs):
        calls.append(args)
        # Both formats make the original implementation reach its byte comparison.
        item = {"Name": "config", "Path": "config", "Size": 8, "IsDir": False}
        return 0, json.dumps(item if "--stat" in args else [item]).encode(), b""

    monkeypatch.setattr(store, "_run", run)
    monkeypatch.setattr(store, "read_bytes", lambda key: b"original")
    if isinstance(expected, int):
        with pytest.raises(HTTPException) as error:
            store.write_exclusive("device/generation/config", body)
        assert error.value.status_code == expected
    else:
        assert store.write_exclusive("device/generation/config", body) == expected
    assert all(args[0] == "lsjson" for args in calls)


def test_competing_store_instances_do_not_both_create(tmp_path, monkeypatch):
    monkeypatch.setenv("STOWLINE_GATEWAY_LOCK_DIR", str(tmp_path / "locks"))
    contents = {}
    state_lock = threading.Lock()

    def run(args, **kwargs):
        if args[0] == "lsjson":
            with state_lock:
                content = contents.get("config")
            if content is None:
                time.sleep(0.15)  # force the old check/write race without a deadlocking barrier
                return 4, b"", b"not found"
            item = {"Name": "config", "Path": "config", "IsDir": False, "Size": len(content)}
            return 0, json.dumps(item if "--stat" in args else [item]).encode(), b""
        if args[0] == "copyto":
            with state_lock:
                contents["config"] = Path(args[1]).read_bytes()
        return 0, b"", b""

    stores = [RcloneStore("qual:repository", "rclone") for _ in range(2)]
    for store in stores:
        monkeypatch.setattr(store, "_run", run)
        monkeypatch.setattr(store, "read_bytes", lambda key: contents["config"])

    def write(i):
        try:
            return stores[i].write_exclusive("device/generation/config", [b"first", b"second"][i])
        except HTTPException as error:
            return error.status_code

    with ThreadPoolExecutor(2) as pool:
        results = list(pool.map(write, range(2)))
    assert sorted(map(str, results)) == ["403", "created"]
    assert contents["config"] in (b"first", b"second")


@pytest.mark.parametrize("code", [1, 5, 7])
def test_listing_errors_are_not_empty_repositories(monkeypatch, code):
    store = RcloneStore("qual:repository", "rclone")
    monkeypatch.setattr(store, "_run", lambda *a, **kw: (code, b"", b"secret"))
    with pytest.raises(HTTPException) as error:
        store.list_infos("device/generation/snapshots")
    assert error.value.status_code == 502


@pytest.mark.parametrize("code,out", [(3, b""), (4, b""), (0, b"null")])
def test_explicit_file_absence_is_distinct_from_failure(monkeypatch, code, out):
    store = RcloneStore("qual:repository", "rclone")
    monkeypatch.setattr(store, "_run", lambda *a, **kw: (code, out, b""))
    assert store.is_file("device/generation/config") is False


def test_real_ignore_existing_success_does_not_mean_created(tmp_path, monkeypatch):
    binary = shutil.which("rclone")
    if not binary:
        pytest.skip("rclone not installed")
    conf = tmp_path / "rclone.conf"
    conf.write_text("[qual]\ntype = local\n", encoding="utf-8")
    monkeypatch.setenv("RCLONE_CONFIG", str(conf))
    monkeypatch.delenv("RCLONE_CONFIG_PASS", raising=False)
    monkeypatch.setenv("STOWLINE_GATEWAY_LOCK_DIR", str(tmp_path / "locks"))
    source, destination = tmp_path / "source", tmp_path / "original"
    source.write_bytes(b"replacement")
    destination.write_bytes(b"original")
    result = subprocess.run([binary, "--config", str(conf), "copyto", str(source),
                             "qual:" + destination.as_posix(), "--ignore-existing"],
                            capture_output=True, timeout=30)
    assert result.returncode == 0  # exit success alone is not a create outcome
    assert destination.read_bytes() == b"original"
    store = RcloneStore("qual:" + tmp_path.as_posix(), binary)
    assert store.is_file("definitely-absent") is False
    # An invalid remote/config is an actual rclone error, never absence.
    conf.write_text("[different]\ntype = local\n", encoding="utf-8")
    with pytest.raises(HTTPException) as error:
        store.write_exclusive("original", b"replacement")
    assert error.value.status_code == 502
    assert destination.read_bytes() == b"original"


def _process_write(root, binary, key, body, ready, start, results):
    store = RcloneStore(root, binary)
    original_run = store._run

    def delayed_stat(args, **kwargs):
        result = original_run(args, **kwargs)
        if args[0] == "lsjson":
            time.sleep(0.3)
        return result

    store._run = delayed_stat
    ready.put(True)
    if not start.wait(20):
        raise RuntimeError("writer start timed out")
    try:
        results.put(store.write_exclusive(key, body))
    except HTTPException as error:
        results.put(error.status_code)


@pytest.mark.parametrize("bodies,expected", [
    ((b"first\n\x00", b"second\r\n"), ["403", "created"]),
    ((b"same\n\x00", b"same\n\x00"), ["created", "same"]),
])
def test_real_rclone_multiprocess_creation(tmp_path, monkeypatch, bodies, expected):
    binary = shutil.which("rclone")
    if not binary:
        if os.environ.get("STOWLINE_REQUIRE_REAL_ENGINES") == "1":
            pytest.fail("required rclone binary is missing")
        pytest.skip("rclone not installed")
    conf = tmp_path / "rclone.conf"
    conf.write_text("[qual]\ntype = local\n", encoding="utf-8")
    monkeypatch.setenv("RCLONE_CONFIG", str(conf))
    monkeypatch.delenv("RCLONE_CONFIG_PASS", raising=False)
    monkeypatch.setenv("STOWLINE_GATEWAY_LOCK_DIR", str(tmp_path / "write-locks"))
    root = "qual:" + (tmp_path / "remote").as_posix()
    context = multiprocessing.get_context("spawn")
    ready, results, start = context.Queue(), context.Queue(), context.Event()
    processes = [context.Process(target=_process_write, args=(root, binary, "device/generation/config", body, ready, start, results)) for body in bodies]
    try:
        for process in processes:
            process.start()
        for _ in processes:
            assert ready.get(timeout=30)
        start.set()
        actual = [results.get(timeout=60) for _ in processes]
        for process in processes:
            process.join(timeout=20)
            assert process.exitcode == 0
        assert sorted(map(str, actual)) == expected
        original = (tmp_path / "remote/device/generation/config").read_bytes()
        assert original in bodies
        store = RcloneStore(root, binary)
        assert store.write_exclusive("device/generation/config", original) == "same"
        with pytest.raises(HTTPException) as error:
            store.write_exclusive("device/generation/config", b"overwrite")
        assert error.value.status_code == 403
        assert (tmp_path / "remote/device/generation/config").read_bytes() == original
    finally:
        for process in processes:
            if process.is_alive():
                process.terminate()
            process.join(timeout=10)
