"""Failed-attempt limiter for the sign-in style endpoints.

The admin panel is on the internet, so its password (and the setup codes)
must not be guessable at full speed. Every failure is counted per client IP
and per account name over a sliding window; once a key has too many, further
attempts are refused with 429 until the oldest failure ages out. A success
clears the account's count. Counts live in this process only: a restart
forgets them, which is fine for slowing down guessing.

The client IP comes from the reverse proxy's X-Forwarded-For (uvicorn runs
with proxy_headers), which is why the control plane must never be reachable
except through the proxy. The per-account limit is higher than the per-IP
one and still applies when an attacker spreads guesses over many addresses.
"""

from __future__ import annotations

import math
import threading
import time
from collections import deque

from fastapi import HTTPException

WINDOW_SECONDS = 15 * 60
MAX_FAILURES_PER_IP = 10
MAX_FAILURES_PER_ACCOUNT = 20


class FailureLimiter:
    def __init__(self, window: float = WINDOW_SECONDS, clock=time.monotonic):
        self.window = window
        self.clock = clock
        self._lock = threading.Lock()
        self._failures: dict[str, deque[float]] = {}

    def _recent(self, key: str, now: float) -> deque[float]:
        q = self._failures.get(key)
        if q is None:
            return deque()
        while q and now - q[0] >= self.window:
            q.popleft()
        if not q:
            self._failures.pop(key, None)
        return q

    def retry_after(self, limits: dict[str, int]) -> int:
        """Seconds until every key is below its limit again; 0 = allowed."""
        now = self.clock()
        wait = 0.0
        with self._lock:
            for key, limit in limits.items():
                q = self._recent(key, now)
                if len(q) >= limit:
                    wait = max(wait, self.window - (now - q[len(q) - limit]))
        return int(math.ceil(wait)) if wait > 0 else 0

    def fail(self, *keys: str) -> None:
        now = self.clock()
        with self._lock:
            for key in keys:
                self._failures.setdefault(key, deque()).append(now)

    def clear(self, *keys: str) -> None:
        with self._lock:
            for key in keys:
                self._failures.pop(key, None)

    def reset(self) -> None:
        with self._lock:
            self._failures.clear()


limiter = FailureLimiter()


def _keys(kind: str, ip: str, account: str) -> dict[str, int]:
    keys = {f"{kind}:ip:{ip or '?'}": MAX_FAILURES_PER_IP}
    if account:
        keys[f"{kind}:account:{account.lower()}"] = MAX_FAILURES_PER_ACCOUNT
    return keys


def check(kind: str, ip: str, account: str = "") -> None:
    """Raise 429 if this IP or account has failed too often recently."""
    wait = limiter.retry_after(_keys(kind, ip, account))
    if wait:
        raise HTTPException(429, "too many failed attempts; try again later", headers={"Retry-After": str(wait)})


def failed(kind: str, ip: str, account: str = "") -> None:
    limiter.fail(*_keys(kind, ip, account))


def succeeded(kind: str, account: str) -> None:
    if account:
        limiter.clear(f"{kind}:account:{account.lower()}")
