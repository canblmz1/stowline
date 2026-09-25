"""SI Mbps ↔ restic KiB/s. 0 is never unlimited."""

from __future__ import annotations

import math

AGENT_SAFETY_MARGIN = 0.90
MAX_RESTIC_KIBPS = 100_000
MIN_BUDGET_PERCENT = 5
MAX_BUDGET_PERCENT = 50


class BandwidthError(ValueError):
    pass


def site_budget_bps(measured_upload_mbps: float, budget_percent: int) -> int:
    if measured_upload_mbps is None or float(measured_upload_mbps) <= 0:
        raise BandwidthError("measured_upload_mbps is required")
    pct = int(budget_percent)
    if pct < MIN_BUDGET_PERCENT or pct > MAX_BUDGET_PERCENT:
        raise BandwidthError("business_backup_budget_percent must be 5-50")
    return int(round(float(measured_upload_mbps) * 1_000_000 * pct / 100))


def restic_kibps_from_bps(bps: int) -> float:
    if bps <= 0:
        raise BandwidthError("bit rate must be positive")
    return (bps / 8) / 1024


def agent_ceiling_kibps(site_budget_bps_value: int, active_jobs: int = 1) -> int:
    if active_jobs < 1:
        raise BandwidthError("active_jobs must be >= 1")
    raw = restic_kibps_from_bps(site_budget_bps_value)
    share = raw * AGENT_SAFETY_MARGIN / active_jobs
    if share > MAX_RESTIC_KIBPS:
        raise BandwidthError("compiled KiB/s overflow")
    out = math.floor(share)
    if out < 1:
        raise BandwidthError("compiled agent ceiling is below 1 KiB/s")
    return int(out)


def compile_site(measured_upload_mbps: float | None, budget_percent: int, active_jobs: int = 1) -> dict:
    if measured_upload_mbps is None:
        return {
            "ok": False,
            "max_site_backup_bps": None,
            "gateway_bytes_per_sec": None,
            "restic_kibps": None,
            "agent_kibps": None,
        }
    bps = site_budget_bps(measured_upload_mbps, budget_percent)
    raw = restic_kibps_from_bps(bps)
    agent = agent_ceiling_kibps(bps, active_jobs)
    return {
        "ok": True,
        "max_site_backup_bps": bps,
        "gateway_bytes_per_sec": bps // 8,
        "restic_kibps": raw,
        "agent_kibps": agent,
    }
