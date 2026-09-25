"""Site schedule bounds and first-backup duration estimates. Zero is never unlimited."""

from __future__ import annotations

import json
import math
import os
from pathlib import Path
from datetime import datetime, timedelta
from zoneinfo import ZoneInfo

from app.bandwidth import BandwidthError, compile_site

_DEFAULT_SITES = {
    "hq": {
        "display_name": "HQ",
        "timezone": "Europe/Istanbul",
        "eligibility_start": "08:30",
        "preferred_latest_start": "16:30",
        "window_end": "16:30",
        "hard_stop": "21:00",
        "max_concurrent_wan_backups": 1,
        "limit_upload_kib": None,  # compiled from measurement; never treat missing as unlimited
        "catch_up": True,
    },
    "branch": {
        "display_name": "Branch",
        "timezone": "Europe/Istanbul",
        "eligibility_start": "08:30",
        "preferred_latest_start": "16:30",
        "window_end": "16:30",
        "hard_stop": "21:00",
        "max_concurrent_wan_backups": 1,
        "limit_upload_kib": None,
        "catch_up": True,
    },
}

_DEFAULT_DEPARTMENTS = ["IT", "Finance", "HR", "Sales", "Service", "Management", "Other"]


def _load_org_config() -> tuple[dict, list[str]]:
    """Sites and departments come from the JSON file named by
    STOWLINE_SITES_FILE (see deployments/sites.example.json), else the two
    example sites above. Each site may set display_name, timezone, the
    schedule bounds, max_concurrent_wan_backups and, optionally,
    measured_upload_mbps / budget_percent seeded into the database once."""
    path = os.environ.get("STOWLINE_SITES_FILE", "").strip()
    bundled = Path(__file__).resolve().parent / "sites.json"  # packaged installer
    if not path and bundled.is_file():
        path = str(bundled)
    if not path:
        return {k: dict(v) for k, v in _DEFAULT_SITES.items()}, list(_DEFAULT_DEPARTMENTS)
    data = json.loads(Path(path).read_text(encoding="utf-8"))
    sites = {}
    for sid, cfg in (data.get("sites") or {}).items():
        key = str(sid).strip().lower()
        if not key:
            continue
        merged = dict(_DEFAULT_SITES["hq"])
        merged["display_name"] = key.title()
        merged.update(cfg or {})
        sites[key] = merged
    if not sites:
        raise ValueError(f"{path}: at least one site is required")
    departments = [str(d) for d in (data.get("departments") or _DEFAULT_DEPARTMENTS) if str(d).strip()]
    return sites, departments


SITE_SCHEDULE, DEPARTMENTS = _load_org_config()


class ScheduleError(ValueError):
    pass


def parse_hhmm(value: str) -> tuple[int, int]:
    raw = (value or "").strip()
    if len(raw) >= 8 and raw[2] == ":" and raw[5] == ":":
        raw = raw[:5]
    if len(raw) != 5 or raw[2] != ":":
        raise ScheduleError("time must be HH:MM")
    hh, mm = int(raw[0:2]), int(raw[3:5])
    if hh < 0 or hh > 23 or mm < 0 or mm > 59:
        raise ScheduleError("time must be HH:MM")
    return hh, mm


def _minutes(hhmm: str) -> int:
    h, m = parse_hhmm(hhmm)
    return h * 60 + m


def site_policy(site_id: str) -> dict:
    key = (site_id or "").strip().lower()
    if key not in SITE_SCHEDULE:
        raise ScheduleError("unknown site")
    return dict(SITE_SCHEDULE[key])


def validate_preferred_start(site_id: str, preferred_hhmm: str) -> str:
    pol = site_policy(site_id)
    parse_hhmm(preferred_hhmm)
    pref = _minutes(preferred_hhmm)
    if pref < _minutes(pol["eligibility_start"]) or pref > _minutes(pol["preferred_latest_start"]):
        raise ScheduleError(
            f"preferred start must be between {pol['eligibility_start']} and {pol['preferred_latest_start']} {pol['timezone']}"
        )
    return preferred_hhmm


def effective_limit_kib(site_id: str, measured_upload_mbps: float | None = None, budget_percent: int = 20) -> int:
    pol = site_policy(site_id)
    pinned = pol.get("limit_upload_kib")
    if pinned:
        kib = int(pinned)
        if kib <= 0:
            raise ScheduleError("upload limit is unavailable")
        return kib
    if measured_upload_mbps is None or float(measured_upload_mbps) <= 0:
        raise ScheduleError("measured capacity is required; zero is never unlimited")
    compiled = compile_site(measured_upload_mbps, budget_percent, 1)
    if not compiled.get("ok") or not compiled.get("agent_kibps"):
        raise BandwidthError("compiled upload limit unavailable")
    kib = int(compiled["agent_kibps"])
    if kib <= 0:
        raise ScheduleError("upload limit is unavailable")
    return kib


def estimate_duration_seconds(logical_bytes: int, limit_kib: int) -> int:
    if limit_kib is None or int(limit_kib) <= 0:
        raise ScheduleError("upload limit is unavailable; zero is never unlimited")
    if logical_bytes < 0:
        raise ScheduleError("logical bytes invalid")
    if logical_bytes == 0:
        return 60
    return int(math.ceil(logical_bytes / (int(limit_kib) * 1024)))


def today_window(site_id: str, now: datetime | None = None) -> dict:
    pol = site_policy(site_id)
    tz = ZoneInfo(pol["timezone"])
    now = now.astimezone(tz) if now else datetime.now(tz)
    day = now.date()

    def at(hhmm: str) -> datetime:
        h, m = parse_hhmm(hhmm)
        return datetime(day.year, day.month, day.day, h, m, tzinfo=tz)

    return {
        "timezone": pol["timezone"],
        "now": now,
        "eligibility": at(pol["eligibility_start"]),
        "latest_start": at(pol["preferred_latest_start"]),
        "hard_stop": at(pol["hard_stop"]),
    }


def hard_stop_reached(schedule: dict | None, now: datetime) -> str | None:
    """HH:MM of today's hard stop when `now` is at or past it, else None.

    The agent clamps every run's deadline to today's hard stop, so a run that
    would start after it is over before it begins. A schedule without a
    timezone or hard stop imposes no clamp and never blocks.
    """
    sched = schedule or {}
    hard, tzname = str(sched.get("hard_stop") or ""), str(sched.get("timezone") or "")
    if not hard or not tzname:
        return None
    h, m = parse_hhmm(hard)
    local = now.astimezone(ZoneInfo(tzname))
    if local >= local.replace(hour=h, minute=m, second=0, microsecond=0):
        return f"{h:02d}:{m:02d}"
    return None


def first_backup_fit(
    *,
    site_id: str,
    logical_bytes: int,
    preferred_hhmm: str,
    measured_upload_mbps: float | None = None,
    now: datetime | None = None,
) -> dict:
    preferred = validate_preferred_start(site_id, preferred_hhmm)
    limit = effective_limit_kib(site_id, measured_upload_mbps)
    seconds = estimate_duration_seconds(logical_bytes, limit)
    win = today_window(site_id, now)
    now_l = win["now"]
    start = max(now_l, win["eligibility"])
    pref_dt = win["eligibility"].replace(hour=parse_hhmm(preferred)[0], minute=parse_hhmm(preferred)[1])
    if now_l < pref_dt:
        start = max(start, pref_dt)
    can_start_today = start <= win["latest_start"]
    finish = start + timedelta(seconds=seconds)
    fits_hard_stop = can_start_today and finish <= win["hard_stop"]
    warn = []
    if not can_start_today:
        warn.append("No new backup may start after the site latest-start time. The PC will use bounded catch-up on the next powered-on eligible day.")
    elif not fits_hard_stop:
        warn.append("Estimated first backup cannot finish before today's hard stop. Keep the PC powered; the job will stop and catch up later.")
    return {
        "preferred_start": preferred,
        "limit_upload_kib": limit,
        "estimated_seconds": seconds,
        "estimated_human": _human_duration(seconds),
        "can_start_today": can_start_today,
        "fits_today_hard_stop": fits_hard_stop,
        "timezone": win["timezone"],
        "hard_stop": win["hard_stop"].strftime("%H:%M"),
        "latest_start": win["latest_start"].strftime("%H:%M"),
        "warnings": warn,
        "stay_powered": "PC must remain powered until backup completes.",
    }


def next_eligible_run(site_id: str, preferred_hhmm: str, now: datetime | None = None) -> str | None:
    try:
        validate_preferred_start(site_id, preferred_hhmm)
    except ScheduleError:
        return None
    win = today_window(site_id, now)
    pref = win["eligibility"].replace(hour=parse_hhmm(preferred_hhmm)[0], minute=parse_hhmm(preferred_hhmm)[1])
    if win["now"] <= win["latest_start"] and pref.date() == win["now"].date():
        when = max(pref, win["now"])
        if when <= win["latest_start"]:
            return when.isoformat()
    nxt = pref + timedelta(days=1)
    return nxt.isoformat()


def apply_preferred_to_policy(base: dict, site_id: str, preferred_hhmm: str) -> dict:
    preferred = validate_preferred_start(site_id, preferred_hhmm)
    pol = site_policy(site_id)
    cfg = dict(base or {})
    sched = dict(cfg.get("schedule") or {})
    sched["timezone"] = pol["timezone"]
    sched["eligibility_start"] = pol["eligibility_start"]
    sched["window_start"] = preferred
    sched["window_end"] = pol["window_end"]
    sched["hard_stop"] = pol["hard_stop"]
    sched["catch_up"] = True
    cfg["schedule"] = sched
    return cfg


def _human_duration(seconds: int) -> str:
    if seconds < 90:
        return f"{seconds}s"
    minutes = math.ceil(seconds / 60)
    if minutes < 90:
        return f"{minutes} min"
    hours = minutes / 60
    return f"{hours:.1f} h"


def normalize_department(name: str, custom: str = "") -> str:
    raw = (custom or name or "").strip()
    if not raw or "\n" in raw or "\r" in raw:
        raise ScheduleError("department required")
    if len(raw) > 64:
        raise ScheduleError("department too long")
    known = {d.lower(): d for d in DEPARTMENTS}
    return known.get(raw.lower(), raw)
