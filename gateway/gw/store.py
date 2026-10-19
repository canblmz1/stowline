"""Gateway object stores.

Local filesystem is the lab default. When STOWLINE_GATEWAY_ROOT starts with
``rclone:``, objects are written through rclone using RCLONE_CONFIG and
RCLONE_CONFIG_PASS from the process environment.

The remote root is owned by the gateway process. Callers cannot select it.
rclone/Drive credentials are never copied into HTTP responses.
"""

from __future__ import annotations

import hashlib
import json
import os
import shutil
import subprocess
import tempfile
import threading
import time
from contextlib import contextmanager
from pathlib import Path

from fastapi import HTTPException

WINDOWS_RCLONE_SHA256 = "8be30f02266a6eaad9d481941ef287b9744bb7140034b097ad862a2ccff3e24c"
WINDOWS_RCLONE_PIN = Path(r"C:\Stowline\bin\rclone.exe")
RCLONE_TIMEOUT_SEC = 900


def _rclone_max_concurrency() -> int:
    """Aggregate ceiling on concurrent rclone subprocesses, across every
    device/request -- not per-device. Configurable because the right
    number depends on the remote's own rate limits and the box's CPU/network,
    not something to hardcode; a fleet of many endpoints must never be able
    to spawn unbounded rclone processes against it."""
    raw = os.environ.get("STOWLINE_GATEWAY_RCLONE_MAX_CONCURRENCY", "2").strip()
    try:
        n = int(raw)
    except ValueError:
        n = 2
    return max(1, n)


class _RcloneGate:
    """Holds the bounded rclone-concurrency semaphore. Re-reads the
    configured ceiling lazily rather than fixing it at import time, so
    tests can exercise different concurrency limits with monkeypatch.setenv
    before the first call. In production the env var is set once at
    process start and never changes, so this never actually re-sizes."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._size = 0
        self._sem: threading.Semaphore | None = None

    def acquire(self):
        with self._lock:
            size = _rclone_max_concurrency()
            if self._sem is None or size != self._size:
                self._size = size
                self._sem = threading.Semaphore(size)
            sem = self._sem
        sem.acquire()
        return sem

    def release(self, sem) -> None:
        sem.release()


_RCLONE_GATE = _RcloneGate()


@contextmanager
def _write_lock(remote_root: str, key: str):
    """Host-local OS lock, shared by threads/processes using the same lock dir.

    Never unlink lock files: replacing an inode would split the lock domain.
    Fixed stripes bound disk usage. This is NOT a distributed/backend lock.
    """
    root = Path(os.environ.get("STOWLINE_GATEWAY_LOCK_DIR") or
                str(Path(tempfile.gettempdir()) / "stowline-gateway-locks"))
    namespace = hashlib.sha256(remote_root.encode()).hexdigest()
    stripe = hashlib.sha256(key.encode()).hexdigest()[:2]
    directory = root / namespace
    directory.mkdir(parents=True, exist_ok=True)
    with open(directory / stripe, "a+b") as lock:
        if os.name == "nt":
            import msvcrt
            lock.seek(0, os.SEEK_END)
            if lock.tell() == 0:
                lock.write(b"\0")
                lock.flush()

            def acquire():
                lock.seek(0)
                msvcrt.locking(lock.fileno(), msvcrt.LK_NBLCK, 1)

            def release():
                lock.seek(0)
                msvcrt.locking(lock.fileno(), msvcrt.LK_UNLCK, 1)
        else:
            import fcntl

            def acquire():
                fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)

            def release():
                fcntl.flock(lock.fileno(), fcntl.LOCK_UN)

        deadline = time.monotonic() + RCLONE_TIMEOUT_SEC
        while True:
            try:
                acquire()
                break
            except OSError as exc:
                if time.monotonic() >= deadline:
                    raise HTTPException(503, "gateway write lock unavailable") from exc
                time.sleep(0.05)
        try:
            yield
        finally:
            release()


def redact_text(value: str) -> str:
    if not value:
        return value
    out = value
    for key in ("RCLONE_CONFIG_PASS", "RCLONE_PASSWORD", "RESTIC_PASSWORD", "RESTIC_REST_PASSWORD"):
        secret = os.environ.get(key)
        if secret:
            out = out.replace(secret, "[REDACTED]")
    return out


def parse_gateway_root(raw: str) -> tuple[str, str]:
    raw = (raw or "").strip()
    if raw.startswith("rclone:"):
        remote = raw[len("rclone:") :].strip()
        if ":" not in remote:
            raise ValueError("STOWLINE_GATEWAY_ROOT rclone: location must be rclone:<remote>:<path>")
        return "rclone", remote
    return "local", raw or "./var/gateway-store"


class LocalStore:
    def __init__(self, root: Path):
        self.root = Path(root)
        self.root.mkdir(parents=True, exist_ok=True)
        self.kind = "local"

    def repo_path(self, device_id: str, generation_id: str):
        return self.root / device_id / generation_id

    def prepare_repo(self, device_id: str, generation_id: str):
        repo = self.root / device_id / generation_id
        repo.mkdir(parents=True, exist_ok=True)
        return repo.resolve()

    def resolve(self, repo, rest_path: str):
        if not rest_path:
            return repo
        target = (repo / rest_path).resolve()
        try:
            target.relative_to(repo)
        except ValueError as exc:
            raise HTTPException(400, "path rejected") from exc
        return target

    def exists(self, target) -> bool:
        return Path(target).exists()

    def is_dir(self, target) -> bool:
        return Path(target).is_dir()

    def is_file(self, target) -> bool:
        return Path(target).is_file()

    def read_bytes(self, target) -> bytes:
        return Path(target).read_bytes()

    def file_size(self, target) -> int:
        return Path(target).stat().st_size

    def read_range(self, target, start: int, end: int) -> bytes:
        length = end - start + 1
        with open(target, "rb") as fh:
            fh.seek(start)
            return fh.read(length)

    def mkdir(self, target) -> None:
        Path(target).mkdir(parents=True, exist_ok=True)

    def list_names(self, target) -> list[str]:
        return [item["name"] for item in self.list_infos(target, recursive=False)]

    def list_infos(self, target, recursive: bool = False) -> list[dict]:
        p = Path(target)
        if not p.exists() or not p.is_dir():
            return []
        infos = []
        if recursive:
            files = [f for f in p.rglob("*") if f.is_file()]
            for f in files:
                name = f.relative_to(p).as_posix()
                if name.endswith(".tmp-stowline") or name.startswith("."):
                    continue
                infos.append({"name": name, "size": f.stat().st_size})
        else:
            for child in p.iterdir():
                if child.is_file():
                    if child.name.endswith(".tmp-stowline") or child.name.startswith("."):
                        continue
                    infos.append({"name": child.name, "size": child.stat().st_size})
        return sorted(infos, key=lambda item: item["name"])

    def write_exclusive(self, target, body: bytes) -> str:
        p = Path(target)
        if p.exists():
            if p.is_file() and p.read_bytes() == body:
                return "same"
            raise HTTPException(403, "append-only: overwrite blocked")
        p.parent.mkdir(parents=True, exist_ok=True)
        # "xb" is exclusive + binary. os.open without O_BINARY on Windows
        # translates LF→CRLF and breaks restic content-addressed objects.
        try:
            with open(p, "xb") as fh:
                fh.write(body)
                fh.flush()
                os.fsync(fh.fileno())
        except FileExistsError:
            if p.is_file() and p.read_bytes() == body:
                return "same"
            raise HTTPException(403, "append-only: overwrite blocked")
        except Exception:
            p.unlink(missing_ok=True)
            raise
        return "created"

    def delete_file(self, target) -> bool:
        p = Path(target)
        if p.exists() and p.is_file():
            p.unlink()
            return True
        return False


class RcloneStore:
    """Single-host writer backed by rclone; remote is never caller-chosen.

    All writers must share a local STOWLINE_GATEWAY_LOCK_DIR and identical
    remote_root. Multiple hosts/containers with independent locks, remote
    aliases and external writers are outside the append-only guarantee.
    """

    def __init__(self, remote_root: str, binary: str):
        self.remote_root = remote_root.rstrip("/")
        self.binary = binary
        self.kind = "rclone"
        self._verify_pin()

    @classmethod
    def from_env(cls, remote_root: str) -> "RcloneStore":
        return cls(remote_root, _rclone_binary())

    def _verify_pin(self) -> None:
        path = Path(self.binary)
        if path.exists() and path.resolve() == WINDOWS_RCLONE_PIN.resolve():
            digest = hashlib.sha256(path.read_bytes()).hexdigest()
            if digest != WINDOWS_RCLONE_SHA256:
                raise RuntimeError("pinned rclone digest mismatch")

    def repo_path(self, device_id: str, generation_id: str) -> str:
        return f"{device_id}/{generation_id}"

    def prepare_repo(self, device_id: str, generation_id: str) -> str:
        key = f"{device_id}/{generation_id}"
        self.mkdir(key)
        return key

    def resolve(self, repo: str, rest_path: str) -> str:
        if not rest_path:
            return repo
        parts = [p for p in rest_path.replace("\\", "/").split("/") if p]
        if any(p in (".", "..") or ".." in p or ":" in p for p in parts):
            raise HTTPException(400, "path rejected")
        return repo + "/" + "/".join(parts)

    def exists(self, key: str) -> bool:
        return self.is_file(key) or self.is_dir(key)

    def is_dir(self, key: str) -> bool:
        if self.is_file(key):
            return False
        code, _out, _err = self._run(["lsf", self._dest(key)], check=False)
        if code not in (0, 3, 4):
            raise HTTPException(502, "rclone metadata error")
        return code == 0

    def is_file(self, key: str) -> bool:
        return self._file_info(key) is not None

    def read_bytes(self, key: str) -> bytes:
        tmp = tempfile.NamedTemporaryFile(delete=False)
        tmp.close()
        try:
            code, _out, _err = self._run(["copyto", self._dest(key), tmp.name], check=False)
            if code != 0:
                raise HTTPException(404 if code in (3, 4) else 502, "rclone read failed")
            return Path(tmp.name).read_bytes()
        finally:
            Path(tmp.name).unlink(missing_ok=True)

    def read_range(self, key: str, start: int, end: int) -> bytes:
        count = end - start + 1
        code, out, _err = self._run(
            ["cat", "--offset", str(start), "--count", str(count), self._dest(key)],
            check=False,
        )
        if code != 0:
            raise HTTPException(404 if code in (3, 4) else 502, "rclone read failed")
        return out

    def file_size(self, key: str) -> int:
        item = self._file_info(key)
        if item is None:
            raise HTTPException(404, "not found")
        return item["Size"]

    def mkdir(self, key: str) -> None:
        self._run(["mkdir", self._dest(key)], check=True)

    def list_names(self, key: str) -> list[str]:
        return [item["name"] for item in self.list_infos(key, recursive=False)]

    def list_infos(self, key: str, recursive: bool = False) -> list[dict]:
        args = ["lsjson", "--files-only"]
        if recursive:
            args.append("--recursive")
        args.append(self._dest(key))
        code, out, _err = self._run(args, check=False)
        if code in (3, 4):
            return []
        if code != 0:
            raise HTTPException(502, "rclone listing error")
        try:
            data = json.loads(out.decode("utf-8"))
        except (ValueError, UnicodeError) as exc:
            raise HTTPException(502, "rclone invalid listing") from exc
        if not isinstance(data, list) or any(not isinstance(item, dict) for item in data):
            raise HTTPException(502, "rclone invalid listing")
        infos = []
        for item in data:
            if item.get("IsDir"):
                continue
            if recursive:
                name = str(item.get("Path") or item.get("Name") or "")
            else:
                name = str(item.get("Name") or "")
            name = name.replace("\\", "/").lstrip("/")
            if not name or any(part in (".", "..") for part in name.split("/")):
                continue
            size = item.get("Size")
            infos.append({"name": name, "size": size if isinstance(size, int) else 0})
        return sorted(infos, key=lambda item: item["name"])

    def write_exclusive(self, key: str, body: bytes) -> str:
        with _write_lock(self.remote_root, key):
            if self.is_file(key):
                if self.read_bytes(key) == body:
                    return "same"
                raise HTTPException(403, "append-only: overwrite blocked")
            parent = key.rsplit("/", 1)[0] if "/" in key else ""
            if parent:
                self.mkdir(parent)
            tmp = tempfile.NamedTemporaryFile(delete=False)
            try:
                tmp.write(body)
                tmp.close()
                # Defense in depth only: --ignore-existing is not an atomic
                # create on general rclone backends. The OS lock is essential.
                code, _out, _err = self._run(["copyto", tmp.name, self._dest(key), "--ignore-existing"], check=False)
            finally:
                tmp.close()
                Path(tmp.name).unlink(missing_ok=True)
            if code != 0 or self.read_bytes(key) != body:
                raise HTTPException(502, "rclone write verification failed")
            return "created"

    def delete_file(self, key: str) -> bool:
        with _write_lock(self.remote_root, key):
            if not self.is_file(key):
                return False
            code, _out, _err = self._run(["deletefile", self._dest(key)], check=False)
            if code not in (0, 3, 4):
                raise HTTPException(502, "rclone delete failed")
            return code == 0

    def _file_info(self, key: str) -> dict | None:
        # In pinned rclone, --stat --files-only queries NewObject without
        # falling back from an object/auth error to a directory listing.
        # A successful JSON null explicitly means no file (also directories).
        code, out, _err = self._run(["lsjson", "--stat", "--files-only", self._dest(key)], check=False)
        if code in (3, 4):
            return None
        if code != 0:
            raise HTTPException(502, "rclone metadata error")
        try:
            data = json.loads(out.decode("utf-8"))
        except (ValueError, UnicodeError) as exc:
            raise HTTPException(502, "rclone invalid metadata") from exc
        if data is None:
            return None
        if (not isinstance(data, dict) or data.get("IsDir") is not False
                or data.get("Name") != key.rsplit("/", 1)[-1]
                or type(data.get("Size")) is not int or data["Size"] < 0):
            raise HTTPException(502, "rclone invalid metadata")
        return data

    def _dest(self, key: str) -> str:
        if not key:
            return self.remote_root
        return self.remote_root + "/" + key.lstrip("/")

    def _run(self, args: list[str], stdin: bytes | None = None, check: bool = True) -> tuple[int, bytes, bytes]:
        """Synchronous and blocking by design: callers (gw/__init__.py's
        route handlers) always invoke this via run_in_threadpool, never
        directly on the event loop. The semaphore below is the actual
        concurrency ceiling -- bounded, not unbounded threads or
        subprocesses -- shared across every device and request, so no
        fleet size can spawn more than STOWLINE_GATEWAY_RCLONE_MAX_CONCURRENCY
        rclone processes at once."""
        cmd = [self.binary]
        cfg = os.environ.get("RCLONE_CONFIG")
        if cfg:
            cmd.extend(["--config", cfg])
        cmd.extend(["--transfers", "1", "--checkers", "1"])
        # Confirmed live: a burst of "rclone transport error" (502) on
        # restic's own lock create/delete churn -- rclone's default
        # --retries=3 exhausted with --retries-sleep=0s between attempts,
        # i.e. it hammered the same transient Drive-side rejection three
        # times back to back with no gap for it to clear. --low-level-
        # retries is already rclone's default (10); pinning it explicitly
        # documents that this is deliberate, not an oversight.
        cmd.extend(["--low-level-retries", "10", "--retries", "5", "--retries-sleep", "2s"])
        cmd.extend(args)
        sem = _RCLONE_GATE.acquire()
        try:
            proc = subprocess.run(
                cmd,
                input=stdin,
                capture_output=True,
                env=os.environ.copy(),
                timeout=RCLONE_TIMEOUT_SEC,
                check=False,
            )
        except subprocess.TimeoutExpired as exc:
            raise HTTPException(504, "rclone timeout") from exc
        finally:
            _RCLONE_GATE.release(sem)
        if check and proc.returncode != 0:
            raise HTTPException(502, "rclone transport error")
        return proc.returncode, proc.stdout, proc.stderr


def _rclone_binary() -> str:
    pinned = WINDOWS_RCLONE_PIN
    if pinned.exists():
        digest = hashlib.sha256(pinned.read_bytes()).hexdigest()
        if digest != WINDOWS_RCLONE_SHA256:
            raise RuntimeError("pinned rclone digest mismatch")
        return str(pinned)
    found = shutil.which("rclone")
    if not found:
        raise RuntimeError("rclone binary not found")
    expected = (os.environ.get("STOWLINE_RCLONE_SHA256") or "").strip().lower()
    if expected:
        digest = hashlib.sha256(Path(found).read_bytes()).hexdigest()
        if digest != expected:
            raise RuntimeError("pinned rclone digest mismatch")
    required = (os.environ.get("STOWLINE_RCLONE_VERSION") or "").strip()
    if required:
        ver = subprocess.run([found, "version"], capture_output=True, text=True, timeout=10, check=False)
        first = (ver.stdout or "").splitlines()[0] if ver.stdout else ""
        if required not in first:
            raise RuntimeError("rclone version pin mismatch")
    return found


def store_from_root(raw: str):
    kind, value = parse_gateway_root(raw)
    if kind == "rclone":
        return RcloneStore.from_env(value)
    return LocalStore(Path(value))
