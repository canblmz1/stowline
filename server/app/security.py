from __future__ import annotations

import hashlib
import hmac
import os
import secrets
from datetime import datetime, timedelta, timezone


def as_utc(dt: datetime | None) -> datetime | None:
    if dt is None:
        return None
    if dt.tzinfo is None:
        return dt.replace(tzinfo=timezone.utc)
    return dt.astimezone(timezone.utc)


def utcnow() -> datetime:
    return datetime.now(timezone.utc)


def new_id() -> str:
    return str(__import__("uuid").uuid4())


def hash_secret(value: str) -> str:
    salt = os.urandom(16)
    dk = hashlib.pbkdf2_hmac("sha256", value.encode("utf-8"), salt, 200_000)
    return f"pbkdf2${salt.hex()}${dk.hex()}"


def verify_secret(value: str, stored: str) -> bool:
    try:
        kind, salt_hex, dk_hex = stored.split("$", 2)
    except ValueError:
        return False
    if kind != "pbkdf2":
        return False
    dk = hashlib.pbkdf2_hmac("sha256", value.encode("utf-8"), bytes.fromhex(salt_hex), 200_000)
    return hmac.compare_digest(dk.hex(), dk_hex)


def random_token(nbytes: int = 32) -> str:
    return secrets.token_urlsafe(nbytes)


def sha256_hex(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def payload_hash(obj: dict) -> str:
    import json

    raw = json.dumps(obj, sort_keys=True, separators=(",", ":"))
    return hashlib.sha256(raw.encode("utf-8")).hexdigest()


ALLOWED_COMMANDS = frozenset(
    {
        "RUN_BACKUP",
        "RESTORE_TO_STAGING",
        "RUN_CANARY",
        "REFRESH_POLICY",
        "REPORT_INVENTORY",
        "CANCEL_CURRENT_SAFE_OPERATION",
        "BROWSE_SNAPSHOT",
        "BROWSE_LOCAL_DIR",
        "BUILD_SNAPSHOT_CATALOG",
        "APPLY_SELECTION",
    }
)

FORBIDDEN_KINDS = frozenset({"EXEC", "SHELL", "FORGET", "PRUNE", "UNLOCK", "REPAIR", "KEY"})


def validate_policy_config(cfg: dict) -> None:
    from fastapi import HTTPException

    if not isinstance(cfg, dict):
        raise HTTPException(422, "policy config must be an object")
    roots = cfg.get("source_roots") or []
    if not roots or not isinstance(roots, list):
        raise HTTPException(422, "source_roots required")
    for r in roots:
        if not isinstance(r, str) or ".." in r or r.strip() == "":
            raise HTTPException(422, "invalid source root")
    vss = cfg.get("vss_mode")
    if vss not in ("required", "disabled"):
        raise HTTPException(422, "vss_mode must be required or disabled")
    sched = cfg.get("schedule") or {}
    tz = str(sched.get("timezone") or "")
    try:
        from zoneinfo import ZoneInfo

        ZoneInfo(tz)
    except Exception as e:
        raise HTTPException(422, f"bad timezone: {e}") from e
    for key in ("window_start", "window_end", "eligibility_start", "hard_stop"):
        if key not in sched and key in ("eligibility_start", "hard_stop"):
            continue
        val = str(sched.get(key) or "")
        if len(val) != 5 or val[2] != ":":
            raise HTTPException(422, f"bad {key}")
    excludes = cfg.get("excludes") or []
    if excludes and not isinstance(excludes, list):
        raise HTTPException(422, "excludes must be a list")
    for ex in excludes:
        if not isinstance(ex, str) or ".." in ex or "\n" in ex:
            raise HTTPException(422, "invalid exclude")
    ret = cfg.get("retention") or {}
    if ret.get("endpoint_may_execute"):
        raise HTTPException(422, "endpoint must not execute retention")
    for k in ("keep_daily", "keep_weekly", "keep_monthly"):
        n = int(ret.get(k) or 0)
        if n < 0 or n > 3650:
            raise HTTPException(422, f"invalid {k}")


def default_policy_config() -> dict:
    return {
        "schema_version": 1,
        "source_roots": [r"C:\Stowline\TestCorpus"],
        "optional_roots": [],
        "vss_mode": "required",
        "schedule": {
            "timezone": "Europe/Istanbul",
            "eligibility_start": "08:30",
            "window_start": "12:00",
            "window_end": "16:30",
            "hard_stop": "17:45",
            "catch_up": True,
            "jitter_max_minutes": 15,
            "epoch": "pilot",
            "max_attempts": 3,
            "retry_delay_1_minutes": 15,
            "retry_delay_2_minutes": 60,
            "boot_delay_min_minutes": 5,
            "boot_delay_max_minutes": 20,
            "normal_max_runtime_minutes": 240,
            "seed_max_runtime_minutes": 480,
        },
        "bandwidth": {
            "upload_limit_kibps": 0,
            "restore_download_limit_kibps": 0,
            "lab_local_unlimited": False,
        },
        "restore_staging_root": r"C:\Stowline\Restore",
        "retention": {"keep_daily": 7, "keep_weekly": 5, "keep_monthly": 12, "endpoint_may_execute": False},
    }


def compute_health(
    *,
    last_success: datetime | None,
    last_attempt_outcome: str,
    last_seen: datetime | None,
    now: datetime | None = None,
    offline_after: int = 900,
) -> str:
    now = as_utc(now) or utcnow()
    last_success = as_utc(last_success)
    last_seen = as_utc(last_seen)
    outcome = (last_attempt_outcome or "").upper()
    if last_seen is None:
        if outcome == "FAILED":
            return "FAILED"
        if outcome == "PARTIAL":
            return "PARTIAL"
        if last_success is None:
            return "NEVER_BACKED_UP"
        return "OFFLINE"
    if (now - last_seen).total_seconds() > offline_after:
        if last_success is None and outcome == "FAILED":
            return "FAILED"
        if last_success is None:
            return "OFFLINE"
        return "OFFLINE"
    if outcome == "FAILED":
        return "FAILED"
    if outcome == "PARTIAL":
        return "PARTIAL"
    if last_success is None:
        return "NEVER_BACKED_UP"
    age = (now - last_success).total_seconds()
    if age > 48 * 3600:
        return "MISSED"
    if age > 26 * 3600:
        return "LATE"
    return "HEALTHY"
