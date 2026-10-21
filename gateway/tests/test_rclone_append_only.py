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
from fastapi.testclient import TestClient

from gw import Registry, create_app_with_store, hash_secret
from gw import store as store_module
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


def test_case_alias_race_preserves_the_confirmed_winner(tmp_path, monkeypatch):
    """Controlled case-insensitive backend, real host-local OS write locks.

    Simulate a backend whose ignore-existing checks are not atomic with its
    uploads. Events force both uploads past their destination check when the
    lock paths split, then let writer 2 mutate writer 1's confirmed bytes.
    This is a deterministic model regression, not an observed rclone race.
    """
    monkeypatch.setenv("STOWLINE_GATEWAY_LOCK_DIR", str(tmp_path / "locks"))
    first_missing, second_lock_opened = threading.Event(), threading.Event()
    second_upload_checked, first_returned = threading.Event(), threading.Event()
    writer = threading.local()
    lock_paths, contents = {}, {}
    bodies = (b"confirmed-first\n\x00", b"later-conflicting\r\n")
    low = "Department/PC--device/generation/data/" + "ab" * 32
    keys = (low, low[:-64] + ("ab" * 32).upper())
    actual_open = open

    def observed_lock_open(path, *args, **kwargs):
        handle = actual_open(path, *args, **kwargs)
        lock_paths[writer.index] = path
        if writer.index == 1:
            second_lock_opened.set()
        return handle

    monkeypatch.setattr(store_module, "open", observed_lock_open, raising=False)
    stores = [RcloneStore("qual:same-root", "rclone") for _ in keys]

    def run_for(index):
        def run(args, **kwargs):
            if args[0] == "lsjson":
                content = contents.get("object")
                if content is None:
                    if index == 0:
                        first_missing.set()
                        assert second_lock_opened.wait(10), "second writer did not attempt its OS lock"
                    return 4, b"", b"not found"
                return 0, json.dumps({"Name": keys[index].rsplit("/", 1)[-1], "IsDir": False, "Size": len(content)}).encode(), b""
            if args[0] == "copyto":
                # Capturing absence here models the non-atomic backend check.
                assert "object" not in contents
                if index == 0 and lock_paths[0] != lock_paths[1]:
                    assert second_upload_checked.wait(10), "alias writer did not reach its independent upload"
                if index == 1:
                    second_upload_checked.set()
                    assert first_returned.wait(10), "first writer did not confirm its bytes"
                contents["object"] = Path(args[1]).read_bytes()
            return 0, b"", b""
        return run

    for index, store in enumerate(stores):
        monkeypatch.setattr(store, "_run", run_for(index))
        monkeypatch.setattr(store, "read_bytes", lambda key: contents["object"])

    def write(index):
        writer.index = index
        try:
            return stores[index].write_exclusive(keys[index], bodies[index])
        except HTTPException as error:
            return error.status_code
        finally:
            if index == 0:
                first_returned.set()

    with ThreadPoolExecutor(2) as pool:
        first = pool.submit(write, 0)
        assert first_missing.wait(10)
        second = pool.submit(write, 1)
        results = [first.result(timeout=15), second.result(timeout=15)]
    assert (results, contents["object"]) == (["created", 403], bodies[0]), "a later alias writer changed confirmed bytes"


def test_lock_identity_coalesces_whole_key_case_and_separators(tmp_path, monkeypatch):
    monkeypatch.setenv("STOWLINE_GATEWAY_LOCK_DIR", str(tmp_path / "locks"))
    lock_paths = []
    actual_open = open

    def observed_lock_open(path, *args, **kwargs):
        lock_paths.append(path)
        return actual_open(path, *args, **kwargs)

    monkeypatch.setattr(store_module, "open", observed_lock_open, raising=False)
    for key in ("Department/PC/generation/data/abcd", "DEPARTMENT/pc/GENERATION/data/ABCD", r"department\pc\generation\data\abcd"):
        with store_module._write_lock("qual:same-root", key):
            pass
    assert len(set(lock_paths)) == 1


@pytest.mark.skipif(os.name != "nt", reason="Windows mandatory byte-range locks")
def test_windows_empty_lock_file_waits_without_writing_a_held_byte(tmp_path, monkeypatch):
    import msvcrt

    locks = tmp_path / "locks"
    monkeypatch.setenv("STOWLINE_GATEWAY_LOCK_DIR", str(locks))
    with store_module._write_lock("qual:same-root", "device/generation/config"):
        pass
    lock_path, = [path for path in locks.rglob("*") if path.is_file()]
    # Locking beyond EOF is supported on Windows. Reproduce an empty file
    # whose first byte is already held by another worker: writing an initial
    # NUL before acquiring that lock fails with PermissionError.
    lock_path.write_bytes(b"")
    attempted_or_finished = threading.Event()
    acquired = threading.Event()
    native_locking = msvcrt.locking

    def observed_locking(fd, mode, count):
        try:
            return native_locking(fd, mode, count)
        finally:
            if mode == msvcrt.LK_NBLCK:
                attempted_or_finished.set()

    monkeypatch.setattr(msvcrt, "locking", observed_locking)

    def competing_writer():
        try:
            with store_module._write_lock("qual:same-root", "device/generation/config"):
                acquired.set()
        finally:
            attempted_or_finished.set()

    with open(lock_path, "r+b") as holder, ThreadPoolExecutor(1) as pool:
        native_locking(holder.fileno(), msvcrt.LK_NBLCK, 1)
        try:
            future = pool.submit(competing_writer)
            assert attempted_or_finished.wait(10), "competing writer did not attempt the held lock"
            assert not acquired.is_set()
        finally:
            holder.seek(0)
            native_locking(holder.fileno(), msvcrt.LK_UNLCK, 1)
        future.result(timeout=10)
    assert acquired.is_set()
    assert lock_path.stat().st_size == 0


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


@pytest.mark.skipif(os.name != "nt", reason="Windows local backend path aliases")
def test_real_windows_rclone_case_aliases_are_the_same_gateway_object(tmp_path, monkeypatch):
    """Actual rclone local backend evidence, separate from the race model."""
    binary = shutil.which("rclone")
    if not binary:
        pytest.skip("rclone not installed")
    conf = tmp_path / "rclone.conf"
    conf.write_text("[qual]\ntype = local\n", encoding="utf-8")
    monkeypatch.setenv("RCLONE_CONFIG", str(conf))
    monkeypatch.delenv("RCLONE_CONFIG_PASS", raising=False)
    monkeypatch.setenv("STOWLINE_GATEWAY_LOCK_DIR", str(tmp_path / "locks"))
    store = RcloneStore("qual:" + (tmp_path / "remote").as_posix(), binary)
    device, generation = "a" * 32, "b" * 32
    prefix = "Finance/PC--persistent-device"
    registry = Registry(None)
    registry.put_record(device, generation, hash_secret("synthetic-password"), storage_prefix=prefix)
    low = "ab" * 32
    upper = low.upper()
    body = b"original\x00\r\n"
    with TestClient(create_app_with_store(store, registry)) as client:
        def post(name, content):
            return client.post(f"/restic/{device}/{generation}/data/{name}", auth=(device, "synthetic-password"), content=content)

        assert post(low, body).status_code == 200
        assert post(upper, body).status_code == 200
        assert post(upper, b"conflicting").status_code == 403
    repo_key = f"{prefix}/{generation}/data/"
    assert store.is_file(repo_key + upper)
    assert store.read_bytes(repo_key + upper) == body
    data_dir = tmp_path / "remote" / prefix / generation / "data"
    assert (data_dir / low).samefile(data_dir / upper)
    assert [p.name for p in data_dir.iterdir()] == [low]  # no path migration
    assert (data_dir / low).read_bytes() == body


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
        results.put((store.write_exclusive(key, body), body))
    except HTTPException as error:
        results.put((error.status_code, body))


@pytest.mark.parametrize("bodies,case_alias,expected", [
    ((b"first\n\x00", b"second\r\n"), False, ["403", "created"]),
    ((b"same\n\x00", b"same\n\x00"), False, ["created", "same"]),
    pytest.param((b"first\n\x00", b"second\r\n"), True, ["403", "created"], marks=pytest.mark.skipif(os.name != "nt", reason="Windows local backend path aliases")),
    pytest.param((b"same\n\x00", b"same\n\x00"), True, ["created", "same"], marks=pytest.mark.skipif(os.name != "nt", reason="Windows local backend path aliases")),
])
def test_real_rclone_multiprocess_creation(tmp_path, monkeypatch, bodies, case_alias, expected):
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
    keys = ["device/generation/data/" + "ab" * 32] * 2 if case_alias else ["device/generation/config"] * 2
    if case_alias:
        keys[1] = keys[1][:-64] + ("ab" * 32).upper()
    processes = [context.Process(target=_process_write, args=(root, binary, key, body, ready, start, results)) for key, body in zip(keys, bodies)]
    try:
        for process in processes:
            process.start()
        for _ in processes:
            assert ready.get(timeout=30)
        start.set()
        outcomes = [results.get(timeout=60) for _ in processes]
        for process in processes:
            process.join(timeout=20)
            assert process.exitcode == 0
        assert sorted(str(status) for status, _body in outcomes) == expected
        original_path = tmp_path / "remote" / keys[0]
        original = original_path.read_bytes()
        # Bind the stored bytes to the writer that actually reported creation,
        # so a later alias upload cannot silently replace its confirmed data.
        created_bodies = [body for status, body in outcomes if status == "created"]
        assert created_bodies == [original]
        store = RcloneStore(root, binary)
        assert store.write_exclusive(keys[1], original) == "same"
        with pytest.raises(HTTPException) as error:
            store.write_exclusive(keys[1], b"overwrite")
        assert error.value.status_code == 403
        assert original_path.read_bytes() == original
    finally:
        for process in processes:
            if process.is_alive():
                process.terminate()
            process.join(timeout=10)
