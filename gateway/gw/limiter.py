"""Per-site token bucket. Tokens are bytes. Never trust X-Site."""

from __future__ import annotations

import asyncio
import time


class TokenBucket:
    def __init__(self, rate_bps: float, burst: float | None = None):
        if rate_bps <= 0:
            raise ValueError("rate_bps must be positive")
        self.rate = float(rate_bps)
        self.burst = float(burst if burst is not None else min(max(rate_bps * 0.25, 16384), 65536))
        self.tokens = self.burst
        self.updated = time.monotonic()
        self.lock = asyncio.Lock()
        self.taken = 0
        self.first_take_at: float | None = None

    def _refill(self) -> None:
        now = time.monotonic()
        elapsed = now - self.updated
        self.updated = now
        self.tokens = min(self.burst, self.tokens + elapsed * self.rate)

    async def take(self, n: int) -> None:
        remaining = int(n)
        while remaining > 0:
            async with self.lock:
                self._refill()
                if self.tokens >= 1:
                    use = min(self.tokens, remaining)
                    self.tokens -= use
                    self.taken += int(use)
                    if self.first_take_at is None:
                        self.first_take_at = time.monotonic()
                    remaining -= int(use)
                    if remaining <= 0:
                        return
                    continue
                wait = (1.0 - self.tokens) / self.rate if self.rate > 0 else 0.05
            await asyncio.sleep(min(max(wait, 0.001), 0.25))


class SiteLimiter:
    def __init__(self, rates: dict[str, float] | None = None):
        self.rates = dict(rates or {})
        self.buckets: dict[str, TokenBucket] = {}
        self.lock = asyncio.Lock()
        self.bytes_by_device: dict[str, int] = {}

    def set_rate(self, site_id: str, bytes_per_sec: float) -> None:
        self.rates[site_id] = bytes_per_sec
        if site_id in self.buckets:
            self.buckets[site_id].rate = bytes_per_sec

    async def bucket_for(self, site_id: str) -> TokenBucket | None:
        if not site_id or site_id not in self.rates:
            return None
        async with self.lock:
            b = self.buckets.get(site_id)
            if b is None:
                b = TokenBucket(self.rates[site_id])
                self.buckets[site_id] = b
            return b

    async def take(self, site_id: str, device_id: str, n: int) -> None:
        bucket = await self.bucket_for(site_id)
        if bucket is None:
            return
        await bucket.take(n)
        self.bytes_by_device[device_id] = self.bytes_by_device.get(device_id, 0) + n

    def snapshot(self) -> dict:
        sites = {}
        for sid, bucket in self.buckets.items():
            elapsed = 0.0
            if bucket.first_take_at is not None:
                elapsed = max(time.monotonic() - bucket.first_take_at, 1e-6)
            sites[sid] = {
                "bytes": bucket.taken,
                "rate_bps_configured": self.rates.get(sid),
                "observed_bps": bucket.taken / elapsed if elapsed else 0,
            }
        return {"sites": sites, "devices": dict(self.bytes_by_device)}


async def read_limited_body(request, limiter: SiteLimiter | None, site_id: str, device_id: str) -> bytes:
    """Throttle while ASGI body messages arrive. Unthrottled requests use request.body()."""
    events: list[str] = []
    bucket = None
    if limiter is not None and site_id:
        bucket = await limiter.bucket_for(site_id)
    if bucket is None:
        request.state.limit_events = events
        return await request.body()

    original = request._receive

    async def limited_receive():
        message = await original()
        if message.get("type") == "http.request":
            chunk = message.get("body") or b""
            if chunk:
                events.append("chunk")
                await limiter.take(site_id, device_id, len(chunk))
                events.append("token")
        return message

    request._receive = limited_receive
    body = await request.body()
    request.state.limit_events = events
    return body
