"""How much encrypted data each registered repository holds in the object store.

Listing a Drive folder with thousands of objects takes seconds, so the meter
measures in a background thread and serves the last result; the control plane
polls it. Sizes come from object metadata only: nothing is read or decrypted.
"""

from __future__ import annotations

import threading
import time
from datetime import datetime, timezone
from typing import Callable


def measure(store, registry, resolve_prefix: Callable[[str | None], str]) -> dict:
    devices: dict[str, dict] = {}
    total = 0
    for device_id, rec in registry.load().items():
        generation = rec.get("generation_id") or ""
        if not generation:
            continue
        prefix = resolve_prefix(rec.get("storage_prefix")) or device_id
        infos = store.list_infos(store.repo_path(prefix, generation), recursive=True)
        size = sum(int(i.get("size") or 0) for i in infos)
        devices[device_id] = {"bytes": size, "objects": len(infos), "generation_id": generation}
        total += size
    return {"available": True, "total_bytes": total, "devices": devices, "measured_at": datetime.now(timezone.utc).isoformat()}


class UsageMeter:
    def __init__(self, store, registry, resolve_prefix: Callable[[str | None], str], ttl_seconds: float = 120.0):
        self._store, self._registry, self._resolve = store, registry, resolve_prefix
        self._ttl = ttl_seconds
        self._lock = threading.Lock()
        self._result: dict | None = None
        self._at = 0.0
        self._running = False

    def _refresh(self) -> None:
        try:
            result = measure(self._store, self._registry, self._resolve)
        except Exception:
            result = None
        with self._lock:
            if result is not None:
                self._result, self._at = result, time.monotonic()
            self._running = False

    def _start_refresh(self) -> threading.Thread | None:
        with self._lock:
            if self._running:
                return None
            self._running = True
        t = threading.Thread(target=self._refresh, daemon=True)
        t.start()
        return t

    def snapshot(self, wait: bool = False) -> dict:
        with self._lock:
            fresh = self._result is not None and time.monotonic() - self._at < self._ttl
        if not fresh:
            t = self._start_refresh()
            if wait and t is not None:
                t.join(timeout=60)
        with self._lock:
            if self._result is None:
                return {"available": False, "measuring": self._running}
            return dict(self._result)
