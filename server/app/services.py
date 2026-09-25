from __future__ import annotations

import hashlib
import ntpath
import posixpath
import re
import unicodedata
from datetime import datetime, timedelta
from pathlib import Path

import httpx
from fastapi import HTTPException
from sqlalchemy import func, select, update
from sqlalchemy.orm import Session

from app.config import settings
from app.db import (
    AdminSession,
    SetupCode,
    AuditEvent,
    BackupAttempt,
    BackupJob,
    BrowseCache,
    Command,
    Device,
    DeviceCredential,
    DeviceSecret,
    DeviceSelection,
    EnrollmentToken,
    Heartbeat,
    Installation,
    Policy,
    PolicyAssignment,
    PolicyRevision,
    Repository,
    RestoreRequest,
    Snapshot,
    SnapshotCatalog,
    SnapshotCatalogEntry,
    StorageProfile,
    User,
    Site,
    utcnow,
)
from app.security import (
    ALLOWED_COMMANDS,
    FORBIDDEN_KINDS,
    compute_health,
    default_policy_config,
    hash_secret,
    new_id,
    payload_hash,
    random_token,
    sha256_hex,
    utcnow as now,
    as_utc,
    verify_secret,
)
from app.version import agent_binary_path, agent_pin

_gateway_admin_ok: bool | None = None
_gateway_admin_reason = ""


def parse_iso_datetime(value) -> datetime | None:
    """Best-effort parse of a client-reported ISO-8601 timestamp. Returns
    None for anything absent or malformed rather than raising -- a bad or
    missing value must fall back to the pre-existing behavior, never 500."""
    if not value or not isinstance(value, str):
        return None
    try:
        return as_utc(datetime.fromisoformat(value))
    except ValueError:
        return None


def audit(db: Session, *, actor_type: str, actor_id: str, action: str, resource_type: str = "", resource_id: str = "", result: str = "OK", extra: dict | None = None) -> None:
    db.add(
        AuditEvent(
            id=new_id(),
            actor_type=actor_type,
            actor_id=actor_id,
            action=action,
            resource_type=resource_type,
            resource_id=resource_id,
            result=result,
            safe_diff=extra or {},
        )
    )


from app import admission as wan
from app import vault


def seed(db: Session, admin_user: str, admin_password: str) -> None:
    wan.seed_sites(db)
    if db.scalar(select(User).where(User.username == admin_user)) is None:
        db.add(
            User(
                id=new_id(),
                username=admin_user,
                password_hash=hash_secret(admin_password),
                display_name="Pilot operator",
                role="ADMIN",
            )
        )
    pol = db.scalar(select(Policy).where(Policy.name == "pilot-default"))
    if pol is None:
        pol = Policy(id=new_id(), name="pilot-default", description="Synthetic-corpus pilot policy")
        db.add(pol)
        db.flush()
        cfg = default_policy_config()
        db.add(
            PolicyRevision(
                id=new_id(),
                policy_id=pol.id,
                version=1,
                content_hash=payload_hash(cfg),
                config_json=cfg,
            )
        )
    if db.scalar(select(StorageProfile).where(StorageProfile.label == "lab-gateway")) is None:
        db.add(
            StorageProfile(
                id=new_id(),
                kind="REST_GATEWAY",
                label="lab-gateway",
                status="IMPLEMENTED",
                nonsecret_config={"transport": "lab-insecure-http", "qualified": False},
            )
        )
    drive = db.scalar(select(StorageProfile).where(StorageProfile.kind == "PILOT_RCLONE_DRIVE"))
    if drive is None:
        db.add(
            StorageProfile(
                id=new_id(),
                kind="PILOT_RCLONE_DRIVE",
                label="workspace-shared-drive",
                status="IMPLEMENTED",
                nonsecret_config={
                    "remote": "stowline-drive",
                    "root": "repositories",
                    "shared_drive_name": "Stowline",
                    "own_client_required": True,
                    "endpoint_selects_remote": False,
                },
            )
        )
    else:
        cfg = dict(drive.nonsecret_config or {})
        cfg.setdefault("remote", "stowline-drive")
        cfg.setdefault("root", "repositories")
        cfg.setdefault("shared_drive_name", "Stowline")
        cfg["own_client_required"] = True
        cfg["endpoint_selects_remote"] = False
        cfg.pop("oauth", None)
        drive.nonsecret_config = cfg
    db.commit()


def login(db: Session, username: str, password: str, session_hours: int) -> str:
    user = db.scalar(select(User).where(User.username == username))
    if user is None or user.disabled_at is not None or not verify_secret(password, user.password_hash):
        audit(db, actor_type="operator", actor_id=username, action="login", result="DENIED")
        db.commit()
        raise HTTPException(401, "invalid credentials")
    raw = random_token()
    sess = AdminSession(
        id=new_id(),
        user_id=user.id,
        token_hash=sha256_hex(raw),
        expires_at=now() + timedelta(hours=session_hours),
    )
    db.add(sess)
    audit(db, actor_type="operator", actor_id=user.id, action="login", result="OK")
    db.commit()
    return raw


def session_pair(db: Session, raw: str | None) -> tuple[User, AdminSession] | None:
    if not raw:
        return None
    sess = db.scalar(select(AdminSession).where(AdminSession.token_hash == sha256_hex(raw)))
    if sess is None or sess.revoked_at is not None or as_utc(sess.expires_at) < now():
        return None
    user = db.get(User, sess.user_id)
    if user is None or user.disabled_at is not None:
        return None
    return user, sess


def session_user(db: Session, raw: str | None) -> User | None:
    """The signed-in admin, for the full admin API. A setup-code session
    is not an admin session and gets None here."""
    pair = session_pair(db, raw)
    if pair is None or pair[1].setup_code_id:
        return None
    return pair[0]


# Setup codes -----------------------------------------------------------------
# Readable over the phone: no 0/O or 1/I/L. 20 characters of 31 = ~99 bits.
SETUP_CODE_ALPHABET = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
SETUP_CODE_LEN = 20
SETUP_SESSION_HOURS = 2
MAX_SETUP_CODE_HOURS = 30 * 24


def normalize_setup_code(raw: str) -> str:
    return "".join(ch for ch in (raw or "").upper() if ch.isalnum())


def _new_setup_code() -> str:
    import secrets

    body = "".join(secrets.choice(SETUP_CODE_ALPHABET) for _ in range(SETUP_CODE_LEN))
    return "-".join(body[i : i + 5] for i in range(0, SETUP_CODE_LEN, 5))


def create_setup_code(db: Session, user: User, *, label: str, hours: int, max_uses: int, site_id: str = "") -> tuple[str, SetupCode]:
    site_id = (site_id or "").lower()
    if site_id and db.get(Site, site_id) is None:
        raise HTTPException(422, "unknown site_id")
    if not 1 <= hours <= MAX_SETUP_CODE_HOURS:
        raise HTTPException(422, f"hours must be between 1 and {MAX_SETUP_CODE_HOURS}")
    if not 1 <= max_uses <= 500:
        raise HTTPException(422, "max_uses must be between 1 and 500")
    raw = _new_setup_code()
    rec = SetupCode(
        id=new_id(),
        code_hash=sha256_hex(normalize_setup_code(raw)),
        label=(label or "")[:128],
        site_id=site_id,
        created_by=user.id,
        expires_at=now() + timedelta(hours=hours),
        max_uses=max_uses,
    )
    db.add(rec)
    audit(db, actor_type="operator", actor_id=user.id, action="setup_code_create", resource_id=rec.id, extra={"site_id": site_id, "label": rec.label, "hours": hours, "max_uses": max_uses})
    db.commit()
    return raw, rec


def setup_code_view(rec: SetupCode) -> dict:
    expired = as_utc(rec.expires_at) < now()
    if rec.revoked_at:
        state = "REVOKED"
    elif expired:
        state = "EXPIRED"
    elif rec.uses >= rec.max_uses:
        state = "USED_UP"
    else:
        state = "ACTIVE"
    return {
        "id": rec.id,
        "label": rec.label,
        "site_id": rec.site_id,
        "expires_at": as_utc(rec.expires_at).isoformat(),
        "max_uses": rec.max_uses,
        "uses": rec.uses,
        "state": state,
        "created_at": as_utc(rec.created_at).isoformat() if rec.created_at else None,
    }


def list_setup_codes(db: Session) -> list[dict]:
    rows = db.scalars(select(SetupCode).order_by(SetupCode.created_at.desc()).limit(100)).all()
    return [setup_code_view(r) for r in rows]


def revoke_setup_code(db: Session, user: User, code_id: str) -> dict:
    rec = db.get(SetupCode, code_id)
    if rec is None:
        raise HTTPException(404, "not found")
    if rec.revoked_at is None:
        rec.revoked_at = now()
        # an install already in progress with this code stops too
        db.execute(
            update(AdminSession)
            .where(AdminSession.setup_code_id == rec.id, AdminSession.revoked_at.is_(None))
            .values(revoked_at=now())
        )
        audit(db, actor_type="operator", actor_id=user.id, action="setup_code_revoke", resource_id=rec.id)
        db.commit()
    return setup_code_view(rec)


def _live_setup_code(db: Session, raw: str) -> SetupCode | None:
    code = normalize_setup_code(raw)
    if len(code) != SETUP_CODE_LEN:
        return None
    rec = db.scalar(select(SetupCode).where(SetupCode.code_hash == sha256_hex(code)))
    if rec is None or rec.revoked_at is not None or as_utc(rec.expires_at) < now() or rec.uses >= rec.max_uses:
        return None
    return rec


def check_setup_code(db: Session, raw: str) -> dict:
    rec = _live_setup_code(db, raw)
    if rec is None:
        raise HTTPException(401, "setup code invalid or expired")
    return {"valid": True, "site_id": rec.site_id, "label": rec.label, "expires_at": as_utc(rec.expires_at).isoformat()}


def setup_code_login(db: Session, raw: str) -> str:
    """One install run: counts one use and opens a short installer session."""
    rec = _live_setup_code(db, raw)
    if rec is None:
        raise HTTPException(401, "setup code invalid or expired")
    user = db.get(User, rec.created_by)
    if user is None or user.disabled_at is not None:
        raise HTTPException(401, "setup code invalid or expired")
    used = db.execute(
        update(SetupCode).where(SetupCode.id == rec.id, SetupCode.uses < SetupCode.max_uses).values(uses=SetupCode.uses + 1)
    )
    if (used.rowcount or 0) != 1:
        db.rollback()
        raise HTTPException(401, "setup code invalid or expired")
    raw_session = random_token()
    db.add(
        AdminSession(
            id=new_id(),
            user_id=user.id,
            token_hash=sha256_hex(raw_session),
            expires_at=min(now() + timedelta(hours=SETUP_SESSION_HOURS), as_utc(rec.expires_at)),
            setup_code_id=rec.id,
        )
    )
    audit(db, actor_type="installer", actor_id=rec.id, action="setup_code_login", resource_id=rec.id)
    db.commit()
    return raw_session


def installer_may_touch(db: Session, sess: AdminSession, device: Device) -> bool:
    """A setup-code session may see and configure only the computers that
    were enrolled with tokens minted under the same setup code."""
    if not sess.setup_code_id:
        return True
    tok = db.get(EnrollmentToken, device.enrollment_token_id) if device.enrollment_token_id else None
    if tok is None or not tok.session_id:
        return False
    minted_by = db.get(AdminSession, tok.session_id)
    return minted_by is not None and minted_by.setup_code_id == sess.setup_code_id


def mint_enrollment_token(
    db: Session, user: User, label: str, minutes: int, site_id: str = "branch", department: str = "", session_id: str = ""
) -> tuple[str, EnrollmentToken]:
    site_id = (site_id or "branch").lower()
    if db.get(Site, site_id) is None:
        raise HTTPException(422, "unknown site_id")
    from app.schedule_policy import normalize_department

    dept = ""
    if department:
        try:
            dept = normalize_department(department)
        except ValueError as exc:
            raise HTTPException(422, str(exc)) from exc
    raw = random_token()
    rec = EnrollmentToken(
        id=new_id(),
        token_hash=sha256_hex(raw),
        label=label,
        site_id=site_id,
        department=dept,
        created_by=user.id,
        expires_at=now() + timedelta(minutes=minutes),
        session_id=session_id,
    )
    db.add(rec)
    audit(
        db,
        actor_type="operator",
        actor_id=user.id,
        action="enrollment_token_create",
        resource_id=rec.id,
        extra={"site_id": site_id, "label": label, "department": dept},
    )
    db.commit()
    return raw, rec


def enroll_agent(db: Session, *, token: str, hostname: str, agent_version: str, installation_id: str, capabilities: dict, gateway_base: str) -> dict:
    _ = installation_id  # server-issued; client-supplied IDs are not trusted
    rec = db.scalar(select(EnrollmentToken).where(EnrollmentToken.token_hash == sha256_hex(token)))
    if rec is None or rec.consumed_at is not None or as_utc(rec.expires_at) < now():
        raise HTTPException(401, "enrollment token invalid or expired")
    consumed = db.execute(
        update(EnrollmentToken)
        .where(EnrollmentToken.id == rec.id, EnrollmentToken.consumed_at.is_(None))
        .values(consumed_at=now())
    )
    if (consumed.rowcount or 0) != 1:
        db.rollback()
        raise HTTPException(401, "enrollment token invalid or expired")
    hostname = "".join(ch for ch in (hostname or "unknown") if ch not in "\r\n")[:256] or "unknown"
    device_id = new_id()
    inst_id = new_id()
    gen_id = new_id()
    caps = capabilities or {}
    agent_sha = str(caps.get("agent_sha") or caps.get("agent_sha256") or "")[:64]
    device = Device(
        id=device_id,
        hostname=hostname or "unknown",
        department=rec.department or "",
        lifecycle="ACTIVE",
        active_installation_id=inst_id,
        agent_version=agent_version,
        agent_sha=agent_sha,
        health="NEVER_BACKED_UP",
        site_id=rec.site_id or "branch",
        enrollment_token_id=rec.id,
    )
    pol = db.scalar(select(Policy).where(Policy.name == "pilot-default"))
    rev = None
    if pol is not None:
        rev = db.scalar(select(PolicyRevision).where(PolicyRevision.policy_id == pol.id).order_by(PolicyRevision.version.desc()))
        if rev is not None:
            device.assigned_policy_revision_id = rev.id
    db.add(device)
    db.add(
        Installation(
            id=inst_id,
            device_id=device_id,
            agent_version=agent_version,
            capabilities=capabilities or {},
            last_seen_at=now(),
        )
    )
    db.flush()
    control_secret = random_token()
    gateway_secret = random_token()
    gw_cred = DeviceCredential(id=new_id(), installation_id=inst_id, purpose="gateway", secret_hash=sha256_hex(gateway_secret))
    db.add(DeviceCredential(id=new_id(), installation_id=inst_id, purpose="control", secret_hash=sha256_hex(control_secret)))
    db.add(gw_cred)
    profile = db.scalar(select(StorageProfile).where(StorageProfile.kind == "REST_GATEWAY"))
    location = f"{gateway_base.rstrip('/')}/restic/{device_id}/{gen_id}"
    db.add(
        Repository(
            id=new_id(),
            device_id=device_id,
            storage_profile_id=profile.id if profile else new_id(),
            generation_id=gen_id,
            location=location,
            state="ACTIVE",
        )
    )
    audit(db, actor_type="device", actor_id=device_id, action="enroll", resource_type="device", resource_id=device_id)
    db.commit()
    notify_gateway(
        device_id,
        gen_id,
        gw_cred.secret_hash,
        site_id=device.site_id,
        storage_prefix=compute_storage_prefix(device.department, hostname, device_id),
    )
    policy_cfg = rev.config_json if rev else default_policy_config()
    policy_cfg = wan.device_policy_overlay(db, device, policy_cfg)
    return {
        "schema_version": 1,
        "device_id": device_id,
        "installation_id": inst_id,
        "generation_id": gen_id,
        "control_credential": control_secret,
        "gateway_username": device_id,
        "gateway_credential": gateway_secret,
        "gateway_location": location,
        "policy_revision_id": device.assigned_policy_revision_id,
        "policy": policy_cfg,
        "site_id": device.site_id,
        "department": device.department,
        "note": "Store credentials with DPAPI. Values are shown once. Not a shared fleet secret.",
    }


def abort_enrollment(db: Session, device: Device) -> Device:
    """Best-effort unwind after remote enroll if local persistence failed."""
    revoke_device(db, None, device)
    return device


def probe_gateway_health() -> None:
    """Reach the gateway /health over the admin URL. Never logs response bodies."""
    url = (settings.gateway_admin_url or "").strip()
    tok = (settings.gateway_admin_token or "").strip()
    if not url or not tok:
        return
    try:
        res = httpx.get(url.rstrip("/") + "/health", timeout=15.0)
        store = ""
        try:
            store = str((res.json() or {}).get("store") or "")
        except Exception:
            store = ""
        if res.status_code == 200 and store:
            _set_gateway_admin_result(True, "")
        else:
            _set_gateway_admin_result(False, f"gateway health HTTP {res.status_code}")
    except Exception:
        _set_gateway_admin_result(False, "gateway health unreachable")


def gateway_registration_state() -> dict:
    url = (settings.gateway_admin_url or "").strip()
    tok = (settings.gateway_admin_token or "").strip()
    if not url or not tok:
        return {
            "gateway_registration": "unconfigured",
            "wan_ready": False,
            "gateway_registration_reason": "STOWLINE_GATEWAY_ADMIN_URL and STOWLINE_GATEWAY_ADMIN_TOKEN required for WAN device registration",
        }
    if _gateway_admin_ok is False:
        return {
            "gateway_registration": "error",
            "wan_ready": False,
            "gateway_registration_reason": _gateway_admin_reason or "gateway admin call failed",
        }
    return {"gateway_registration": "configured", "wan_ready": True, "gateway_registration_reason": ""}


def _set_gateway_admin_result(ok: bool, reason: str = "") -> None:
    global _gateway_admin_ok, _gateway_admin_reason
    _gateway_admin_ok = bool(ok)
    _gateway_admin_reason = reason


_PREFIX_UNSAFE = re.compile(r"[^A-Za-z0-9 _.-]")


def _sanitize_prefix_segment(raw: str, fallback: str) -> str:
    """Collapse a department/hostname label into a single Drive-folder-
    name-safe, traversal-safe path segment. Never returns empty, '.', or
    '..' -- those are exactly the values that would make the gateway's
    later fallback-to-device_id logic (see gateway/gw/__init__.py) matter,
    so this must not be the thing that produces them."""
    cleaned = _PREFIX_UNSAFE.sub("_", (raw or "").strip())
    cleaned = re.sub(r"_+", "_", cleaned).strip("_. ")
    if not cleaned or cleaned in (".", ".."):
        return fallback
    return cleaned[:80]


def compute_storage_prefix(department: str, hostname: str, device_id: str) -> str:
    """Human-readable Drive namespace for a NEW enrollment:
    <department>/<hostname>--<short device id>. Computed once, server-
    side, from data already trusted at enrollment time (the enrollment
    token's assigned department, the sanitized hostname the agent
    reported, and the server-minted device_id) -- never from a value a
    client supplies on a later, already-authenticated request, which is
    what keeps a department edit or a replayed request from relocating
    an existing repository (see notify_gateway's callers: only the
    enrollment call site passes this at all)."""
    from app.schedule_policy import normalize_department

    dept = _sanitize_prefix_segment(normalize_department(department) if department else "", "Other")
    host = _sanitize_prefix_segment(hostname, "PC")
    short_id = re.sub(r"[^0-9a-fA-F]", "", device_id or "")[:8] or "00000000"
    return f"{dept}/{host}--{short_id}"


def notify_gateway(device_id: str, generation_id: str, password_hash: str, revoked: bool = False, site_id: str = "", storage_prefix: str = "") -> None:
    url = (settings.gateway_admin_url or "").strip()
    tok = (settings.gateway_admin_token or "").strip()
    if not url or not tok:
        return
    try:
        payload = {"device_id": device_id, "generation_id": generation_id, "password_hash": password_hash, "revoked": revoked, "site_id": site_id}
        if storage_prefix:
            payload["storage_prefix"] = storage_prefix
        res = httpx.post(
            url.rstrip("/") + "/admin/register",
            json=payload,
            headers={"Authorization": f"Bearer {tok}"},
            timeout=15.0,
        )
        if res.status_code >= 400:
            _set_gateway_admin_result(False, f"gateway admin HTTP {res.status_code}")
            if not settings.lab_mode:
                raise HTTPException(503, "gateway registration unavailable")
            return
        _set_gateway_admin_result(True, "")
    except HTTPException:
        raise
    except Exception:
        _set_gateway_admin_result(False, "gateway admin unreachable")
        if not settings.lab_mode:
            raise HTTPException(503, "gateway registration unavailable")


def notify_gateway_site_limit(site_id: str, bytes_per_sec: int | None) -> None:
    """Push the gateway backstop. bytes_per_sec is SI bytes, never bits."""
    url = (settings.gateway_admin_url or "").strip()
    tok = (settings.gateway_admin_token or "").strip()
    if not url or not tok or not site_id or not bytes_per_sec or int(bytes_per_sec) <= 0:
        return
    try:
        res = httpx.post(
            url.rstrip("/") + "/admin/site-limits",
            json={"site_id": site_id, "bytes_per_sec": int(bytes_per_sec)},
            headers={"Authorization": f"Bearer {tok}"},
            timeout=15.0,
        )
        if res.status_code >= 400:
            _set_gateway_admin_result(False, f"gateway admin HTTP {res.status_code}")
            if not settings.lab_mode:
                raise HTTPException(503, "gateway site-limit unavailable")
            return
        _set_gateway_admin_result(True, "")
    except HTTPException:
        raise
    except Exception:
        _set_gateway_admin_result(False, "gateway admin unreachable")
        if not settings.lab_mode:
            raise HTTPException(503, "gateway site-limit unavailable")


def fetch_gateway_ingress() -> dict:
    url = (settings.gateway_admin_url or "").strip()
    tok = (settings.gateway_admin_token or "").strip()
    if not url or not tok:
        return {}
    try:
        res = httpx.get(
            url.rstrip("/") + "/admin/ingress",
            headers={"Authorization": f"Bearer {tok}"},
            timeout=2.0,
        )
        if res.status_code != 200:
            return {}
        data = res.json()
        return data if isinstance(data, dict) else {}
    except Exception:
        return {}


def fetch_gateway_usage() -> dict:
    """Object sizes per repository as measured by the gateway (see gw/usage.py)."""
    url = (settings.gateway_admin_url or "").strip()
    tok = (settings.gateway_admin_token or "").strip()
    if not url or not tok:
        return {}
    try:
        res = httpx.get(url.rstrip("/") + "/admin/usage", headers={"Authorization": f"Bearer {tok}"}, timeout=4.0)
        if res.status_code != 200:
            return {}
        data = res.json()
        return data if isinstance(data, dict) else {}
    except Exception:
        return {}


def drive_usage(db: Session) -> dict:
    raw = fetch_gateway_usage()
    if not raw.get("available"):
        return {"schema_version": 1, "available": False, "measuring": bool(raw.get("measuring"))}
    active = {d.id for d in db.scalars(select(Device)).all() if d.lifecycle == "ACTIVE"}
    devices = raw.get("devices") if isinstance(raw.get("devices"), dict) else {}
    return {
        "schema_version": 1,
        "available": True,
        "measured_at": raw.get("measured_at"),
        "total_bytes": sum(int(v.get("bytes") or 0) for k, v in devices.items() if k in active),
        "all_bytes": int(raw.get("total_bytes") or 0),
        "devices": {k: {"bytes": int(v.get("bytes") or 0), "objects": int(v.get("objects") or 0)} for k, v in devices.items()},
    }


def reconcile_snapshots(db: Session, device: Device) -> dict:
    """Read-only Restic-vs-DB drift report for a device's primary repository.

    Never deletes or fixes anything -- this only reports what it sees.
    Works without a restic password: the gateway's /admin/snapshots route
    lists snapshot object ids by existence alone (restic's REST protocol
    already exposes ids without decrypting anything), which is the only
    way this reconciliation is possible at all given the control plane
    never holds a repository password.
    """
    repo = db.scalar(select(Repository).where(Repository.device_id == device.id, Repository.role == "PRIMARY"))
    if repo is None:
        return {"checked": False, "reason": "no repository"}
    url = (settings.gateway_admin_url or "").strip()
    tok = (settings.gateway_admin_token or "").strip()
    if not url or not tok:
        return {"checked": False, "reason": "gateway admin not configured"}
    try:
        res = httpx.get(
            url.rstrip("/") + f"/admin/snapshots/{repo.device_id}/{repo.generation_id}",
            headers={"Authorization": f"Bearer {tok}"},
            timeout=20.0,
        )
        if res.status_code != 200:
            return {"checked": False, "reason": f"gateway admin HTTP {res.status_code}"}
        repo_ids = set(res.json().get("snapshot_ids") or [])
    except Exception:
        return {"checked": False, "reason": "gateway admin unreachable"}
    db_rows = db.scalars(select(Snapshot).where(Snapshot.repository_id == repo.id)).all()
    db_ids = {row.engine_snapshot_id for row in db_rows}
    return {
        "checked": True,
        "repository_id": repo.id,
        "repo_snapshot_count": len(repo_ids),
        "db_snapshot_count": len(db_ids),
        "matched_count": len(repo_ids & db_ids),
        # In the repository but never recorded here -- e.g. a crash after
        # restic finished but before ReportAttempt flushed.
        "repo_only": sorted(repo_ids - db_ids),
        # Recorded here but not found in the repository -- worth a human
        # look, but never auto-deleted or auto-corrected by this endpoint.
        "db_only": sorted(db_ids - repo_ids),
    }


def device_from_control_secret(db: Session, secret: str) -> tuple[Device, Installation] | None:
    if not secret:
        return None
    c = db.scalar(
        select(DeviceCredential).where(
            DeviceCredential.purpose == "control",
            DeviceCredential.revoked_at.is_(None),
            DeviceCredential.secret_hash == sha256_hex(secret),
        )
    )
    if c is None:
        return None
    inst = db.get(Installation, c.installation_id)
    if inst is None or inst.lifecycle != "ACTIVE":
        return None
    dev = db.get(Device, inst.device_id)
    if dev is None or dev.lifecycle != "ACTIVE":
        return None
    return dev, inst


def agent_binary_for_download() -> Path:
    """The path to hand FileResponse for GET /api/v1/agent/binary. Refuses
    to serve anything unless the manifest currently marks the pinned agent
    qualified AND the on-disk file's own hash still matches that exact
    pin -- catches a deploy that updated manifest.json but forgot to also
    update the served .exe, which would otherwise ship an unverified binary
    to every real device's auto-upgrade check."""
    sha, qualified = agent_pin()
    if not qualified or not sha:
        raise HTTPException(503, "agent build not qualified for distribution")
    path = agent_binary_path()
    if not path.exists():
        raise HTTPException(503, "agent binary not staged on this deployment")
    actual = hashlib.sha256(path.read_bytes()).hexdigest()
    if actual != sha:
        raise HTTPException(503, "staged agent binary does not match the qualified pin")
    return path


def heartbeat(db: Session, device: Device, inst: Installation, body: dict) -> dict:
    seq = int(body.get("sequence") or 0)
    existing = db.scalar(
        select(Heartbeat).where(
            Heartbeat.device_id == device.id,
            Heartbeat.installation_id == inst.id,
            Heartbeat.sequence == seq,
        )
    )
    if existing is None:
        db.add(
            Heartbeat(
                id=new_id(),
                device_id=device.id,
                installation_id=inst.id,
                sequence=seq,
                agent_version=str(body.get("agent_version") or device.agent_version),
                current_operation=str(body.get("current_operation") or "none"),
                policy_revision_id=str(body.get("policy_revision_id") or ""),
            )
        )
    device.last_seen_at = now()
    inst.last_seen_at = now()
    device.agent_version = str(body.get("agent_version") or device.agent_version)
    # The running build's own hash, reported on every heartbeat so the panel
    # reflects an automatic upgrade (or its rollback) as soon as it happens.
    # Older agents never send it; their enrollment-time value is kept.
    reported_sha = str(body.get("agent_sha256") or "").lower()
    if len(reported_sha) == 64 and all(c in "0123456789abcdef" for c in reported_sha):
        device.agent_sha = reported_sha
    device.health = compute_health(
        last_success=device.last_success_at,
        last_attempt_outcome=device.last_attempt_outcome,
        last_seen=device.last_seen_at,
    )
    db.commit()
    rev = db.get(PolicyRevision, device.assigned_policy_revision_id) if device.assigned_policy_revision_id else None
    base = rev.config_json if rev else default_policy_config()
    overlay = wan.device_policy_overlay(db, device, base)
    site = db.get(Site, device.site_id) if device.site_id else None
    pause = bool(site.pause_new_wan_admissions) if site else True
    if device.pause_new_backups:
        pause = True
    provider_down = bool(site and site.provider_unavailable_until and as_utc(site.provider_unavailable_until) > now())
    compiled = overlay.get("bandwidth") or {}
    agent_sha256, agent_qualified = agent_pin()
    return {
        "schema_version": 1,
        "server_time": now().isoformat(),
        "poll_seconds": 20 + (int(device.id.replace("-", "")[:8], 16) % 25),
        "desired_policy_revision_id": device.assigned_policy_revision_id,
        "policy": overlay,
        "pause_new_wan_admissions": pause,
        "provider_unavailable": provider_down,
        "compiled_upload_kibps": int(compiled.get("upload_limit_kibps") or 0),
        "site_id": device.site_id or "",
        "agent_sha256": agent_sha256,
        "agent_qualified": agent_qualified,
    }


def ingest_attempt(db: Session, device: Device, body: dict) -> dict:
    attempt_id = str(body.get("attempt_id") or new_id())
    existing = db.get(BackupAttempt, attempt_id)
    if existing is not None:
        job = db.get(BackupJob, existing.job_id)
        if job is None or job.device_id != device.id:
            raise HTTPException(403, "attempt mismatch")
        return {"schema_version": 1, "accepted": True, "idempotent": True, "job_id": existing.job_id, "attempt_id": existing.id, "health": device.health}
    outcome = str(body.get("outcome") or "").upper()
    snapshot = str(body.get("snapshot_id") or "").lower()
    published_not_green = bool(body.get("published_not_green"))
    slot = str(body.get("slot_key") or "")
    job = None
    job_id = str(body.get("job_id") or "")
    if job_id:
        owned = db.get(BackupJob, job_id)
        if owned is not None:
            if owned.device_id != device.id:
                raise HTTPException(403, "job mismatch")
            job = owned
    if slot and job is None:
        job = db.scalar(select(BackupJob).where(BackupJob.device_id == device.id, BackupJob.slot_key == slot))
    if job is None:
        job = BackupJob(id=new_id(), device_id=device.id, slot_key=slot, kind="BACKUP", state="ACTIVE")
        db.add(job)
        db.flush()
    att = BackupAttempt(
        id=attempt_id,
        job_id=job.id,
        attempt_no=int(body.get("attempt_no") or 1),
        phase=str(body.get("phase") or outcome),
        outcome=outcome,
        snapshot_id=snapshot,
        error_class=str(body.get("error_class") or ""),
        consistency=str(body.get("consistency") or ""),
        published_not_green=published_not_green,
        summary_json={
            k: body.get(k)
            for k in (
                "bytes_added",
                "files_new",
                "duration_ms",
                "logical_source_bytes",
                "restic_total_bytes_processed",
                "restic_data_added",
                "gateway_payload_bytes",
                "backup_elapsed_ms",
                "queue_elapsed_ms",
                "retry_count",
            )
            if k in body
        },
        ended_at=now(),
    )
    db.add(att)
    job.state = outcome or job.state
    job.updated_at = now()
    device.last_attempt_outcome = outcome
    device.last_seen_at = now()
    if outcome == "SUCCEEDED" and snapshot and not published_not_green:
        # attempt_ended_at is the agent's own local outbox timestamp (already
        # recorded in its journal when the attempt actually finished), not
        # server receive time. A backlogged/retried report can arrive long
        # after a chronologically newer attempt already advanced the
        # device's success state -- e.g. a week-old outbox entry finally
        # delivered after a schema-width bug that blocked it is fixed. Only
        # let an incoming success move last_success_at/last_success_snapshot
        # forward when it is not older than whatever is already recorded.
        # Older agents that don't send attempt_ended_at fall back to the
        # prior (unordered) behavior -- unchanged, not a regression.
        client_ended_at = parse_iso_datetime(body.get("attempt_ended_at"))
        current = as_utc(device.last_success_attempt_ended_at)
        stale = client_ended_at is not None and current is not None and client_ended_at < current
        if not stale:
            device.last_success_at = now()
            device.last_success_snapshot = snapshot
            if client_ended_at is not None:
                device.last_success_attempt_ended_at = client_ended_at
            repo = db.scalar(select(Repository).where(Repository.device_id == device.id, Repository.role == "PRIMARY"))
            if repo is not None:
                if db.scalar(select(Snapshot).where(Snapshot.repository_id == repo.id, Snapshot.engine_snapshot_id == snapshot)) is None:
                    db.add(
                        Snapshot(
                            id=new_id(),
                            repository_id=repo.id,
                            engine_snapshot_id=snapshot,
                            observation_state="REPORTED",
                            completeness="COMPLETE",
                            consistency=str(body.get("consistency") or "UNKNOWN"),
                            reported_attempt_id=att.id,
                            source_time=now(),
                        )
                    )
    device.health = compute_health(
        last_success=device.last_success_at,
        last_attempt_outcome=device.last_attempt_outcome,
        last_seen=device.last_seen_at,
    )
    err = str(body.get("error_class") or "").upper()
    if err in wan.PROVIDER_CLASSES and device.site_id:
        site = db.get(Site, device.site_id)
        if site is not None:
            retry_after = int(body.get("retry_after_seconds") or 900)
            wan.trip_provider(db, site, retry_after)
    db.commit()
    return {"schema_version": 1, "accepted": True, "job_id": job.id, "attempt_id": att.id, "health": device.health}


_BACKUP_RUN_KINDS = frozenset({"RUN_BACKUP", "RUN_CANARY"})
# Browse is read-only and keyed entirely by its payload.  Repeated clicks or
# an impatient UI poll must reuse the same queued/delivered request instead of
# putting another long-running restic listing in front of a backup.
_BROWSE_KINDS = frozenset({"BROWSE_SNAPSHOT", "BROWSE_LOCAL_DIR"})
_CATALOG_KINDS = frozenset({"BUILD_SNAPSHOT_CATALOG"})


def _device_schedule(db: Session, device: Device) -> dict:
    """The schedule the agent enforces: its assigned policy revision's, with
    the site's as a fallback for a device that has none."""
    rev = db.get(PolicyRevision, device.assigned_policy_revision_id) if device.assigned_policy_revision_id else None
    sched = ((rev.config_json or {}).get("schedule") if rev else None) or {}
    if sched or not device.site_id:
        return sched
    from app.schedule_policy import ScheduleError, site_policy

    try:
        return site_policy(device.site_id)
    except ScheduleError:
        return {}


def _refuse_if_backup_window_closed(db: Session, device: Device) -> None:
    from app.schedule_policy import hard_stop_reached

    sched = _device_schedule(db, device)
    stop = hard_stop_reached(sched, now())
    if stop:
        raise HTTPException(
            409,
            f"Yedekleme penceresi kapalı: bugünkü sınır {stop} ({sched['timezone']}) geçti. "
            "Ajan bu saatten sonra yedek başlatamaz; komut kuyruğa alınmadı. "
            "Sınırdan önce tekrar deneyin ya da sitenin pencere sonunu uzatın.",
        )


def enqueue_command(
    db: Session,
    user: User | None,
    device: Device,
    kind: str,
    payload: dict,
    minutes: int = 15,
    *,
    actor_type: str = "operator",
    actor_id: str | None = None,
) -> Command:
    kind = kind.upper()
    if kind in FORBIDDEN_KINDS or kind not in ALLOWED_COMMANDS:
        raise HTTPException(422, "unsupported or forbidden command")
    if kind in _BACKUP_RUN_KINDS and settings.enforce_backup_window:
        _refuse_if_backup_window_closed(db, device)
    if kind == "RUN_BACKUP" or kind in _BROWSE_KINDS or kind in _CATALOG_KINDS:
        # "Backup now" is an edge-triggered request, not a counter. Lock the
        # device row so concurrent browser clicks cannot both pass the lookup
        # and create a train of identical long-running commands. A fresh
        # request is accepted again as soon as the prior command reaches a
        # terminal state; browse deduplication is additionally keyed by its
        # exact payload hash so different folders/prefixes remain independent.
        db.execute(select(Device.id).where(Device.id == device.id).with_for_update())
        pending_query = select(Command).where(
            Command.device_id == device.id,
            Command.installation_id == (device.active_installation_id or ""),
            Command.kind == kind,
            Command.state.in_(("QUEUED", "DELIVERED")),
            Command.expires_at > now(),
        )
        if kind in _BROWSE_KINDS or kind in _CATALOG_KINDS:
            pending_query = pending_query.where(Command.payload_hash == payload_hash(payload))
        pending = db.scalar(pending_query.order_by(Command.created_at))
        if pending is not None:
            pending._deduplicated = True
            return pending
    if kind == "RESTORE_TO_STAGING":
        dest = str(payload.get("staging_root") or "")
        if dest and ".." in dest:
            raise HTTPException(422, "restore path rejected")
        payload["destination_mode"] = "STAGING"
    cmd = Command(
        id=new_id(),
        device_id=device.id,
        installation_id=device.active_installation_id or "",
        kind=kind,
        payload=payload,
        payload_hash=payload_hash(payload),
        expires_at=now() + timedelta(minutes=minutes),
        job_id=new_id(),
    )
    db.add(cmd)
    cmd._deduplicated = False
    audit(db, actor_type=actor_type, actor_id=actor_id or (user.id if user else ""), action="command_create", resource_type="command", resource_id=cmd.id, extra={"kind": kind, "device_id": device.id})
    db.commit()
    return cmd


#  A command that reached the device (state=DELIVERED) but never got a
# terminal ack -- crashed agent, dropped connection, a bug in the ack call
# itself -- must not stay stuck forever: nothing else ever moves it off
# DELIVERED. Re-offering it to the SAME device after a short bounded lease
# is what lets a transient failure recover instead of wedging the command.
REDELIVERY_LEASE_SECONDS = 20


def claim_command(db: Session, device: Device, inst: Installation) -> dict | None:
    now_ts = now()
    stale_before = now_ts - timedelta(seconds=REDELIVERY_LEASE_SECONDS)
    redeliverable = (
        (Command.state == "DELIVERED")
        & Command.acked_at.is_(None)
        & Command.delivered_at.is_not(None)
        & (Command.delivered_at < stale_before)
    )
    cmd = db.scalar(
        select(Command)
        .where(
            Command.device_id == device.id,
            Command.installation_id == inst.id,
            Command.expires_at > now_ts,
            (Command.state == "QUEUED") | redeliverable,
        )
        .order_by(Command.created_at)
    )
    if cmd is None:
        return None
    claimed = db.execute(
        update(Command)
        .where(
            Command.id == cmd.id,
            Command.device_id == device.id,
            Command.installation_id == inst.id,
            (Command.state == "QUEUED") | redeliverable,
        )
        .values(state="DELIVERED", delivered_at=now_ts, delivery_attempts=func.coalesce(Command.delivery_attempts, 0) + 1)
        # The WHERE above compares a timezone-aware Python datetime against
        # delivered_at; SQLite gives that column back tz-naive, which makes
        # the ORM's default in-memory ("evaluate") sync crash comparing
        # naive vs. aware. "fetch" re-selects matched rows in SQL instead
        # of re-evaluating the WHERE clause in Python.
        .execution_options(synchronize_session="fetch")
    )
    if (claimed.rowcount or 0) != 1:
        db.rollback()
        return None
    db.commit()
    cmd = db.get(Command, cmd.id)
    if cmd is None:
        return None
    return {
        "schema_version": 1,
        "command_id": cmd.id,
        "job_id": cmd.job_id,
        "device_id": device.id,
        "installation_id": inst.id,
        "kind": cmd.kind,
        "payload": cmd.payload,
        "payload_hash": cmd.payload_hash,
        "issued_at": cmd.created_at.isoformat(),
        "expires_at": cmd.expires_at.isoformat(),
    }


def ack_command(db: Session, device: Device, inst: Installation, command_id: str, result: dict) -> dict:
    cmd = db.get(Command, command_id)
    if cmd is None or cmd.device_id != device.id or cmd.installation_id != inst.id:
        raise HTTPException(404, "not found")
    if cmd.state in {"SUCCEEDED", "FAILED", "CANCELLED"}:
        return {"schema_version": 1, "idempotent": True, "state": cmd.state}
    # EXPIRED only means the command's 15-minute delivery window passed; a
    # delivered backup that ran longer than that (confirmed live: 16 min for
    # 1.6 GB) must still record its real outcome instead of a 409.
    if cmd.state != "DELIVERED" and not (cmd.state == "EXPIRED" and cmd.delivered_at is not None):
        raise HTTPException(409, "command was not delivered")
    state = str(result.get("state") or "")
    if state not in {"SUCCEEDED", "FAILED", "CANCELLED"}:
        raise HTTPException(422, "terminal command state required")
    cmd.state = state
    # The agent transports handler output under `extra`; the admin command
    # API deliberately exposes that handler result directly as result_json
    # (UI consumers read result.path / result.entries for BROWSE_LOCAL_DIR
    # and BROWSE_SNAPSHOT alike -- the agent never hands back restic's raw
    # ls --json here, only sanitized {name,path,type,size,mtime} entries).
    # State already has its own column. Keep the exact extra object and only
    # add the two optional top-level terminal metadata fields when present.
    stored_result = dict(result.get("extra") or {})
    for key in ("error_class", "snapshot_id"):
        if result.get(key):
            stored_result[key] = result[key]
    cmd.result_json = stored_result
    cmd.acked_at = now()
    if cmd.state == "SUCCEEDED" and cmd.kind in _BROWSE_KINDS:
        _remember_browse_result(db, device, inst.id, cmd.kind, cmd.payload or {}, stored_result)
    if cmd.kind == "RUN_BACKUP":
        # Older builds allowed rapid clicks to enqueue several identical
        # RUN_BACKUP commands. Once one of them finishes, the remaining
        # queued copies are obsolete; allowing them to run serially makes the
        # UI look stuck and needlessly re-reads the same sources.
        db.execute(
            update(Command)
            .where(
                Command.id != cmd.id,
                Command.device_id == device.id,
                Command.installation_id == inst.id,
                Command.kind == "RUN_BACKUP",
                Command.state == "QUEUED",
            )
            .values(state="CANCELLED", acked_at=now(), result_json={"reason": "coalesced_duplicate"})
            .execution_options(synchronize_session=False)
        )
    if cmd.kind == "RESTORE_TO_STAGING":
        rid = str((cmd.payload or {}).get("restore_request_id") or "")
        if rid:
            req = db.get(RestoreRequest, rid)
            if req is not None and req.device_id == device.id:
                req.state = "READY" if cmd.state == "SUCCEEDED" else "FAILED"
                req.result_json = stored_result
    db.commit()
    return {"schema_version": 1, "accepted": True, "state": cmd.state}


_BROWSE_SCOPE_KIND = {"local": "BROWSE_LOCAL_DIR", "snapshot": "BROWSE_SNAPSHOT"}


def _browse_path_key(scope: str, path: str) -> str:
    raw = str(path or "").strip()
    if scope == "local":
        # Windows paths are slash- and case-insensitive. ntpath gives us
        # Windows semantics even though the control plane runs on Linux.
        normalized = ntpath.normpath(raw.replace("/", "\\"))
        canonical = "" if normalized == "." else ntpath.normcase(normalized)
        return hashlib.sha256(canonical.encode("utf-8")).hexdigest()
    normalized = posixpath.normpath(raw or "/")
    canonical = "" if normalized in {".", "/"} else normalized.rstrip("/")
    return hashlib.sha256(canonical.encode("utf-8")).hexdigest()


def _browse_payload_identity(kind: str, payload: dict) -> tuple[str, str, str] | None:
    if kind == "BROWSE_LOCAL_DIR":
        # A cursor is a later page of a bounded directory walk. Never let it
        # replace the first page/catalogue for the folder.
        if str(payload.get("cursor") or ""):
            return None
        path = str(payload.get("path") or "").strip()
        return ("local", "", path) if path else None
    if kind == "BROWSE_SNAPSHOT":
        snapshot_id = str(payload.get("snapshot_id") or "").lower()
        if len(snapshot_id) != 64:
            return None
        return "snapshot", snapshot_id, str(payload.get("prefix") or "")
    return None


def _remember_browse_result(
    db: Session,
    device: Device,
    installation_id: str,
    kind: str,
    payload: dict,
    result: dict,
    *,
    scanned_at: datetime | None = None,
) -> BrowseCache | None:
    identity = _browse_payload_identity(kind, payload)
    entries = result.get("entries") if isinstance(result, dict) else None
    if identity is None or not isinstance(entries, list):
        return None
    # Retain only the documented metadata fields. Even a compromised endpoint
    # cannot smuggle file contents or secret-shaped arbitrary JSON into the
    # long-lived catalogue.
    if len(entries) > 1000 or any(not isinstance(entry, dict) for entry in entries):
        return None
    safe_entries: list[dict] = []
    for entry in entries:
        name = str(entry.get("name") or "")
        entry_path = str(entry.get("path") or "")
        entry_type = str(entry.get("type") or "")
        if not name or len(name) > 512 or not entry_path or len(entry_path) > 2048:
            return None
        if entry_type not in {"file", "dir", "directory", "reparse", "symlink"}:
            return None
        safe = {"name": name, "path": entry_path, "type": entry_type}
        if entry.get("size") is not None:
            try:
                size = int(entry["size"])
            except (TypeError, ValueError):
                return None
            if size < 0:
                return None
            safe["size"] = size
        if entry.get("mtime") is not None:
            mtime = str(entry["mtime"])
            if len(mtime) > 128:
                return None
            safe["mtime"] = mtime
        safe_entries.append(safe)
    scope, snapshot_id, requested_path = identity
    result_path = result.get("path") if scope == "local" else result.get("prefix")
    path = str(result_path if result_path is not None else requested_path)
    if len(path) > 1024:
        return None
    path_key = _browse_path_key(scope, path)
    payload_key = _browse_path_key(scope, requested_path)
    if path_key != payload_key:
        # Do not allow an agent response to populate a different cache key
        # than the path the operator requested.
        return None
    parent = str(result.get("parent") or "")[:1024]
    saved = {
        "entries": safe_entries,
        "count": len(safe_entries),
        "truncated": bool(result.get("truncated") or result.get("cursor")),
        "path" if scope == "local" else "prefix": path,
    }
    if parent:
        saved["parent"] = parent
    row = db.scalar(
        select(BrowseCache).where(
            BrowseCache.device_id == device.id,
            BrowseCache.installation_id == installation_id,
            BrowseCache.scope == scope,
            BrowseCache.snapshot_id == snapshot_id,
            BrowseCache.path_key == path_key,
        )
    )
    if row is None:
        row = BrowseCache(
            id=new_id(),
            device_id=device.id,
            installation_id=installation_id,
            scope=scope,
            snapshot_id=snapshot_id,
            path=path,
            path_key=path_key,
        )
        db.add(row)
    row.path = path
    row.result_json = saved
    row.scanned_at = as_utc(scanned_at) if scanned_at is not None else now()
    return row


def _backfill_browse_cache(db: Session, device: Device, *, scope: str, snapshot_id: str = "", path: str | None = None) -> None:
    """Lazily preserve successful browse results created before the cache table.

    This is what makes the already-scanned live folders immediately useful
    after deployment: no extra endpoint scan is needed just to seed the table.
    """
    kind = _BROWSE_SCOPE_KIND[scope]
    installation_id = device.active_installation_id or ""
    # Serialize the one-time lazy migration so two browser tabs cannot both
    # try to create the same unique cache row.
    db.execute(select(Device.id).where(Device.id == device.id).with_for_update())
    commands = db.scalars(
        select(Command)
        .where(
            Command.device_id == device.id,
            Command.installation_id == installation_id,
            Command.kind == kind,
            Command.state == "SUCCEEDED",
        )
        .order_by(Command.acked_at.desc(), Command.created_at.desc())
        .limit(500)
    ).all()
    wanted_key = _browse_path_key(scope, path or "") if path is not None else None
    existing_keys = {
        (row.snapshot_id, row.path_key)
        for row in db.scalars(
            select(BrowseCache).where(
                BrowseCache.device_id == device.id,
                BrowseCache.installation_id == installation_id,
                BrowseCache.scope == scope,
                BrowseCache.snapshot_id == snapshot_id,
            )
        ).all()
    }
    changed = False
    seen: set[tuple[str, str]] = set()
    for cmd in commands:
        identity = _browse_payload_identity(cmd.kind, cmd.payload or {})
        if identity is None:
            continue
        _, command_snapshot, command_path = identity
        if snapshot_id and command_snapshot != snapshot_id:
            continue
        command_key = _browse_path_key(scope, command_path)
        if wanted_key is not None and command_key != wanted_key:
            continue
        cache_identity = (command_snapshot, command_key)
        if cache_identity in seen or cache_identity in existing_keys:
            continue
        remembered = _remember_browse_result(
            db,
            device,
            installation_id,
            cmd.kind,
            cmd.payload or {},
            cmd.result_json or {},
            scanned_at=cmd.acked_at or cmd.created_at,
        )
        if remembered is not None:
            seen.add(cache_identity)
            existing_keys.add(cache_identity)
            changed = True
        if path is not None and changed:
            break
    if changed:
        db.commit()


def _browse_cache_row(db: Session, device: Device, scope: str, snapshot_id: str, path: str) -> BrowseCache | None:
    return db.scalar(
        select(BrowseCache).where(
            BrowseCache.device_id == device.id,
            BrowseCache.installation_id == (device.active_installation_id or ""),
            BrowseCache.scope == scope,
            BrowseCache.snapshot_id == snapshot_id,
            BrowseCache.path_key == _browse_path_key(scope, path),
        )
    )


def browse_cache_get(db: Session, device: Device, *, scope: str, snapshot_id: str, path: str) -> dict:
    row = _browse_cache_row(db, device, scope, snapshot_id, path)
    if row is None:
        _backfill_browse_cache(db, device, scope=scope, snapshot_id=snapshot_id, path=path)
        row = _browse_cache_row(db, device, scope, snapshot_id, path)
    if row is None:
        return {"schema_version": 1, "cached": False, "scope": scope, "snapshot_id": snapshot_id, "path": path}
    result = dict(row.result_json or {})
    scanned_at = as_utc(row.scanned_at)
    return {
        "schema_version": 1,
        "cached": True,
        "scope": scope,
        "snapshot_id": snapshot_id,
        "path": row.path,
        "parent": str(result.get("parent") or ""),
        "entries": result.get("entries") or [],
        "count": int(result.get("count") or len(result.get("entries") or [])),
        "truncated": bool(result.get("truncated")),
        "scanned_at": scanned_at.isoformat(),
        "age_seconds": max(0, int((now() - scanned_at).total_seconds())),
    }


def _fold_search(value: str) -> str:
    folded = unicodedata.normalize("NFKD", str(value or "").replace("ı", "i").replace("İ", "I"))
    return "".join(ch for ch in folded if not unicodedata.combining(ch)).casefold()


def catalog_path_hash(path: str) -> str:
    return hashlib.sha256(path.encode("utf-8")).hexdigest()


def catalog_checksum(path_hashes: list[str]) -> str:
    return hashlib.sha256("\n".join(sorted(path_hashes)).encode("utf-8")).hexdigest()


def browse_cache_search(db: Session, device: Device, *, scope: str, snapshot_id: str, query: str) -> dict:
    _backfill_browse_cache(db, device, scope=scope, snapshot_id=snapshot_id)
    rows = db.scalars(
        select(BrowseCache).where(
            BrowseCache.device_id == device.id,
            BrowseCache.installation_id == (device.active_installation_id or ""),
            BrowseCache.scope == scope,
            BrowseCache.snapshot_id == snapshot_id,
        )
    ).all()
    needle = _fold_search(query)
    found: dict[str, dict] = {}
    for row in rows:
        # A cached listing also proves that the requested folder itself is a
        # known, selectable folder. Surface it before its children so a path
        # such as Desktop remains one click away even when it was reached by
        # typing the path rather than by opening its parent first.
        if scope == "local":
            trimmed_path = row.path.rstrip("\\/")
            folder_name = ntpath.basename(trimmed_path) or row.path
            folder_parent = ntpath.dirname(trimmed_path)
        else:
            trimmed_path = row.path.rstrip("/")
            folder_name = posixpath.basename(trimmed_path) or "K\u00f6k"
            folder_parent = posixpath.dirname(trimmed_path) or "/"
        if row.path or scope == "snapshot":
            if not needle or needle in _fold_search(folder_name):
                found[row.path_key] = {
                    "name": folder_name,
                    "path": row.path,
                    "type": "directory",
                    "folder": folder_parent,
                    "scanned": True,
                }
        for entry in (row.result_json or {}).get("entries") or []:
            if not isinstance(entry, dict):
                continue
            entry_type = str(entry.get("type") or "")
            is_dir = entry_type in {"dir", "directory"}
            if not needle and not is_dir:
                continue
            if needle and needle not in _fold_search(str(entry.get("name") or "")):
                continue
            path = str(entry.get("path") or "")
            if not path:
                continue
            key = _browse_path_key(scope, path)
            found.setdefault(key, {**entry, "folder": row.path, "scanned": False})
    matches = sorted(
        found.values(),
        key=lambda entry: (
            0 if str(entry.get("type") or "") in {"dir", "directory"} else 1,
            _fold_search(str(entry.get("name") or "")),
            str(entry.get("path") or ""),
        ),
    )
    return {
        "schema_version": 1,
        "scope": scope,
        "snapshot_id": snapshot_id,
        "query": query,
        "matches": matches[:200],
        "truncated": len(matches) > 200,
        "scanned_folders": len(rows),
    }


def create_snapshot_catalog(db: Session, device: Device, installation_id: str, snap: Snapshot, declared: dict) -> SnapshotCatalog:
    existing = db.scalar(select(SnapshotCatalog).where(SnapshotCatalog.snapshot_id == snap.id))
    if existing is not None:
        return existing
    cat = SnapshotCatalog(
        id=new_id(),
        snapshot_id=snap.id,
        device_id=device.id,
        installation_id=installation_id,
        repository_id=snap.repository_id,
        state="PENDING",
        declared_file_count=declared["declared_file_count"],
        declared_directory_count=declared["declared_directory_count"],
        declared_logical_bytes=declared["declared_logical_bytes"],
        declared_checksum=declared["declared_checksum"],
    )
    db.add(cat)
    db.commit()
    return cat


def append_catalog_entries(db: Session, cat: SnapshotCatalog, entries: list[dict]) -> None:
    if cat.state == "READY":
        raise HTTPException(422, "catalog already finalized")
    for e in entries:
        h = catalog_path_hash(e["path"])
        row = db.get(SnapshotCatalogEntry, (cat.id, h))
        if row is None:
            row = SnapshotCatalogEntry(catalog_id=cat.id, path_hash=h)
            db.add(row)
        row.parent_path = e.get("parent_path") or ""
        row.path = e["path"]
        row.name = e["name"]
        row.normalized_name = _fold_search(e["name"])
        row.type = e["type"]
        row.size = e.get("size")
        row.mtime = e.get("mtime") or ""
    if cat.state == "PENDING":
        cat.state = "UPLOADING"
    db.commit()


def finalize_snapshot_catalog(db: Session, cat: SnapshotCatalog, declared: dict) -> dict:
    file_count, dir_count = declared["file_count"], declared["directory_count"]
    if cat.state == "READY" and (cat.actual_file_count, cat.actual_directory_count) == (file_count, dir_count):
        return {"catalog_id": cat.id, "state": "READY", "ok": True}
    rows = db.scalars(select(SnapshotCatalogEntry).where(SnapshotCatalogEntry.catalog_id == cat.id)).all()
    actual_files = sum(1 for r in rows if r.type == "file")
    actual_dirs = sum(1 for r in rows if r.type != "file")
    actual_checksum = catalog_checksum([r.path_hash for r in rows])
    ok = (actual_files, actual_dirs, actual_checksum) == (file_count, dir_count, declared["checksum"])
    cat.actual_file_count, cat.actual_directory_count = actual_files, actual_dirs
    if ok:
        cat.state = "READY"
        cat.completed_at = now()
    db.commit()
    return {"catalog_id": cat.id, "state": cat.state, "ok": ok}


def fail_snapshot_catalog(db: Session, cat: SnapshotCatalog, error_class: str) -> None:
    if cat.state == "READY":
        return  # a fail arriving after a successful finalize is a stale/duplicate signal -- READY never regresses
    cat.state = "FAILED"
    cat.error = error_class[:64]
    db.commit()


def read_snapshot_catalog(db: Session, device: Device, snapshot_id_hex: str, path: str, cursor: str, limit: int) -> dict:
    snap = db.scalar(
        select(Snapshot).join(Repository, Repository.id == Snapshot.repository_id).where(
            Repository.device_id == device.id, Snapshot.engine_snapshot_id == snapshot_id_hex.lower()
        )
    )
    cat = db.scalar(select(SnapshotCatalog).where(SnapshotCatalog.snapshot_id == snap.id)) if snap else None
    if cat is None:
        return {"schema_version": 1, "state": "", "cached": False, "path": path, "entries": [], "count": 0, "truncated": False}
    if cat.state != "READY":
        return {"schema_version": 1, "state": cat.state, "cached": False, "path": path, "entries": [], "count": 0, "truncated": False}
    norm_path = path.rstrip("/")
    q = select(SnapshotCatalogEntry).where(SnapshotCatalogEntry.catalog_id == cat.id, SnapshotCatalogEntry.parent_path == norm_path)
    if cursor:
        q = q.where(SnapshotCatalogEntry.path_hash > cursor)
    q = q.order_by(SnapshotCatalogEntry.path_hash).limit(limit + 1)
    rows = db.scalars(q).all()
    truncated = len(rows) > limit
    rows = rows[:limit]
    entries = [{"name": r.name, "path": r.path, "type": r.type, "size": r.size, "mtime": r.mtime} for r in rows]
    return {
        "schema_version": 1,
        "state": "READY",
        "cached": True,
        "path": path,
        "parent": norm_path.rsplit("/", 1)[0] if "/" in norm_path else "",
        "entries": entries,
        "count": len(entries),
        "truncated": truncated,
        "cursor": rows[-1].path_hash if truncated else "",
    }


def search_snapshot_catalog(db: Session, device: Device, snapshot_id_hex: str, query: str, limit: int) -> dict:
    snap = db.scalar(
        select(Snapshot).join(Repository, Repository.id == Snapshot.repository_id).where(
            Repository.device_id == device.id, Snapshot.engine_snapshot_id == snapshot_id_hex.lower()
        )
    )
    cat = db.scalar(select(SnapshotCatalog).where(SnapshotCatalog.snapshot_id == snap.id, SnapshotCatalog.state == "READY")) if snap else None
    if cat is None:
        return {"schema_version": 1, "matches": [], "truncated": False}
    needle = _fold_search(query)
    q = select(SnapshotCatalogEntry).where(SnapshotCatalogEntry.catalog_id == cat.id)
    if needle:
        q = q.where(SnapshotCatalogEntry.normalized_name.contains(needle))
    rows = db.scalars(q.order_by(SnapshotCatalogEntry.type.desc(), SnapshotCatalogEntry.normalized_name).limit(limit + 1)).all()
    truncated = len(rows) > limit
    rows = rows[:limit]
    matches = [{"name": r.name, "path": r.path, "type": r.type, "size": r.size, "mtime": r.mtime, "folder": r.parent_path} for r in rows]
    return {"schema_version": 1, "matches": matches, "truncated": truncated}


def search_device_files(db: Session, device: Device, query: str, limit: int) -> dict:
    needle = _fold_search(query)
    q = (
        select(SnapshotCatalogEntry, Snapshot)
        .join(SnapshotCatalog, SnapshotCatalog.id == SnapshotCatalogEntry.catalog_id)
        .join(Snapshot, Snapshot.id == SnapshotCatalog.snapshot_id)
        .where(SnapshotCatalog.device_id == device.id, SnapshotCatalog.state == "READY")
    )
    if needle:
        q = q.where(SnapshotCatalogEntry.normalized_name.contains(needle))
    rows = db.execute(q.order_by(Snapshot.created_at.desc()).limit(limit + 1)).all()
    truncated = len(rows) > limit
    rows = rows[:limit]
    matches = [
        {"name": e.name, "path": e.path, "size": e.size, "mtime": e.mtime, "folder": e.parent_path, "snapshot_id": s.engine_snapshot_id, "snapshot_time": s.created_at.isoformat()}
        for e, s in rows
    ]
    return {"schema_version": 1, "matches": matches, "truncated": truncated}


def backfill_missing_catalogs(db: Session, user: User, device: Device) -> list[Command]:
    repo = db.scalar(select(Repository).where(Repository.device_id == device.id, Repository.role == "PRIMARY"))
    if repo is None:
        return []
    cataloged = {row[0] for row in db.execute(select(Snapshot.id).join(SnapshotCatalog, SnapshotCatalog.snapshot_id == Snapshot.id).where(Snapshot.repository_id == repo.id))}
    missing = db.scalars(select(Snapshot).where(Snapshot.repository_id == repo.id, Snapshot.id.not_in(cataloged) if cataloged else True)).all()
    return [enqueue_command(db, user, device, "BUILD_SNAPSHOT_CATALOG", {"snapshot_id": s.engine_snapshot_id}) for s in missing]


def create_restore(db: Session, user: User, device: Device, snapshot_id: str, selections: list[str]) -> RestoreRequest:
    if len(snapshot_id) != 64 or any(c not in "0123456789abcdef" for c in snapshot_id.lower()):
        raise HTTPException(422, "snapshot id")
    for sel in selections:
        if ".." in sel or ":" in sel or sel.startswith("\\\\") or sel.startswith("//") or "\x00" in sel:
            raise HTTPException(422, "selection rejected")
    repo = db.scalar(select(Repository).where(Repository.device_id == device.id, Repository.role == "PRIMARY"))
    if repo is None:
        raise HTTPException(422, "no repository")
    snap = db.scalar(select(Snapshot).where(Snapshot.repository_id == repo.id, Snapshot.engine_snapshot_id == snapshot_id.lower()))
    if snap is None:
        raise HTTPException(422, "snapshot not bound to device")
    device.last_restore_state = "QUEUED"
    req = RestoreRequest(
        id=new_id(),
        device_id=device.id,
        snapshot_id=snapshot_id.lower(),
        selections_json=selections,
        destination_mode="STAGING",
        state="QUEUED",
        requested_by=user.username,
    )
    db.add(req)
    db.flush()
    enqueue_command(
        db,
        user,
        device,
        "RESTORE_TO_STAGING",
        {"restore_request_id": req.id, "snapshot_id": req.snapshot_id, "selections": selections, "destination_mode": "STAGING"},
    )
    audit(db, actor_type="operator", actor_id=user.id, action="restore_request", resource_type="restore", resource_id=req.id, extra={"device_id": device.id})
    db.commit()
    return req


# ---- escrow copy of a device's backup key (see app/vault.py) ----


def _secret_row(db: Session, device_id: str, kind: str = vault.KIND_RESTIC) -> DeviceSecret | None:
    return db.scalar(select(DeviceSecret).where(DeviceSecret.device_id == device_id, DeviceSecret.kind == kind))


def _secret_meta(row: DeviceSecret | None) -> dict:
    return {
        "has_key": row is not None,
        "fingerprint": row.fingerprint if row else "",
        "source": row.source if row else "",
        "updated_at": row.updated_at.isoformat() if row else None,
        "last_revealed_at": row.last_revealed_at.isoformat() if row and row.last_revealed_at else None,
        "reveal_count": (row.reveal_count or 0) if row else 0,
    }


def _require_secret_kind(kind: str) -> None:
    if kind != vault.KIND_RESTIC:
        raise HTTPException(422, "Desteklenmeyen anahtar türü.")


def report_self_service_selection(
    db: Session, device: Device, source_roots: list[str], *, source: str = "self_service"
) -> tuple[DeviceSelection | None, bool]:
    """Record the folders the PC actually backs up; returns (latest selection, recorded).

    source="self_service": the user changed them from the local desktop page.
    source="device_sync": the agent reports its pilot.json at start-up, so
    folders discovered at install (which never went through the panel) show
    up in the admin panel. pilot.json on the device is already the truth by
    the time either arrives. An unchanged list records nothing, and a sync
    never replaces an admin selection the PC has not applied yet -- that
    one is on its way to the device and must win."""
    roots = [r.strip() for r in source_roots if (r or "").strip()][:500]
    prev = db.scalar(select(DeviceSelection).where(DeviceSelection.device_id == device.id).order_by(DeviceSelection.version.desc()))
    if prev is not None:
        if list((prev.manifest_json or {}).get("source_roots") or []) == roots:
            return prev, False
        if source == "device_sync" and not prev.applied_locally:
            return prev, False
    rec = save_device_selection(
        db,
        None,
        device,
        {"source_roots": roots, "source": source},
        applied_locally=True,
        actor_type="device",
        actor_id=device.id,
    )
    return rec, True


def request_self_service_backup(db: Session, device: Device) -> Command:
    """The person at the PC pressed "Şimdi yedekle" in Stowline Backups. It
    becomes the same RUN_BACKUP an admin would queue, so admission, backup
    window, deduplication and result reporting all behave identically."""
    return enqueue_command(db, None, device, "RUN_BACKUP", {"source": "self_service"}, actor_type="device", actor_id=device.id)


USER_MESSAGE_MAX = 2000


def report_user_message(db: Session, device: Device, message: str) -> None:
    """ "IT'ye haber ver" from Stowline Backups: lands in Olaylar with its text."""
    text = (message or "").strip()
    if not text or len(text) > USER_MESSAGE_MAX:
        raise HTTPException(422, f"message must be 1..{USER_MESSAGE_MAX} characters")
    audit(db, actor_type="device", actor_id=device.id, action="user_message", resource_type="device", resource_id=device.id, extra={"message": text})
    db.commit()


def report_self_service_restore(db: Session, device: Device, snapshot_id: str, selections: list[str], destination: str) -> None:
    """Audit-only record of a restore the end user triggered themselves
    from the local desktop client (Phase 4, see
    docs/superpowers/specs/2026-09-22-ops-and-desktop-plan.md) -- the
    restore itself already happened locally on the device by the time
    this call arrives; this exists purely so an admin can see, in Olaylar,
    that it happened and where it landed. Never blocks or reverses
    anything -- an audit trail, not a gate."""
    audit(
        db,
        actor_type="device",
        actor_id=device.id,
        action="self_service_restore",
        resource_type="device",
        resource_id=device.id,
        extra={"snapshot_id": snapshot_id[:16], "selections": list(selections)[:50], "selection_count": len(selections), "destination": destination},
    )
    db.commit()


def device_secret_status(db: Session, device: Device, kind: str = vault.KIND_RESTIC) -> dict:
    """What is known about a device's stored key, never the key itself."""
    _require_secret_kind(kind)
    return {"enabled": vault.enabled(), **_secret_meta(_secret_row(db, device.id, kind))}


def store_device_secret(db: Session, device: Device, *, kind: str, secret: str, actor_type: str, actor_id: str, source: str) -> dict:
    _require_secret_kind(kind)
    try:
        clean = vault.validate_secret(secret)
    except vault.VaultError as exc:
        raise HTTPException(422, str(exc)) from exc
    try:
        token = vault.seal(clean)
    except vault.VaultNotConfigured as exc:
        raise HTTPException(503, "Şifre kasası yapılandırılmadı (STOWLINE_ESCROW_KEY).") from exc
    fp = vault.fingerprint(clean)
    ts = now()
    row = _secret_row(db, device.id, kind)
    if row is not None and row.fingerprint == fp and row.source == source:
        # The agent re-pushes its key on every service start; the same key
        # again is not an event worth an Olaylar row or a new saved date.
        return device_secret_status(db, device, kind)
    if row is None:
        db.add(DeviceSecret(id=new_id(), device_id=device.id, kind=kind, ciphertext=token, fingerprint=fp, source=source, created_at=ts, updated_at=ts))
    else:
        row.ciphertext, row.fingerprint, row.source, row.updated_at = token, fp, source, ts
    audit(db, actor_type=actor_type, actor_id=actor_id, action="secret_store", resource_type="device", resource_id=device.id, extra={"kind": kind, "fingerprint": fp, "source": source})
    db.commit()
    return device_secret_status(db, device, kind)


def reveal_device_secret(db: Session, user: User, device: Device, kind: str, password: str) -> dict:
    """The one place a stored key leaves the vault: admin only, password
    re-entered, throttled, and audited whether it works or not."""
    _require_secret_kind(kind)
    if user.role != "ADMIN":
        raise HTTPException(403, "Bu işlem için yönetici yetkisi gerekir.")
    wait = vault.throttle_remaining(user.id)
    if wait:
        raise HTTPException(429, f"Çok fazla hatalı deneme. {wait} saniye sonra tekrar deneyin.")
    if not verify_secret(password or "", user.password_hash):
        vault.record_failure(user.id)
        audit(db, actor_type="operator", actor_id=user.id, action="secret_reveal", resource_type="device", resource_id=device.id, result="DENIED", extra={"kind": kind})
        db.commit()
        raise HTTPException(403, "Yönetici şifresi yanlış.")
    vault.clear_failures(user.id)
    row = _secret_row(db, device.id, kind)
    if row is None:
        raise HTTPException(404, "Bu bilgisayar için kayıtlı anahtar yok.")
    try:
        plain = vault.unseal(row.ciphertext)
    except vault.VaultNotConfigured as exc:
        raise HTTPException(503, "Şifre kasası yapılandırılmadı (STOWLINE_ESCROW_KEY).") from exc
    except vault.VaultError as exc:
        raise HTTPException(409, "Kayıtlı anahtar çözülemedi (kasa anahtarı değişmiş olabilir).") from exc
    row.last_revealed_at = now()
    row.reveal_count = (row.reveal_count or 0) + 1
    audit(db, actor_type="operator", actor_id=user.id, action="secret_reveal", resource_type="device", resource_id=device.id, extra={"kind": kind, "fingerprint": row.fingerprint})
    db.commit()
    return {"secret": plain, "fingerprint": row.fingerprint}


def vault_overview(db: Session) -> dict:
    """Every active computer (key stored or missing) plus any other computer
    that still has a stored key: an archived PC's backups still need theirs."""
    rows = {r.device_id: r for r in db.scalars(select(DeviceSecret).where(DeviceSecret.kind == vault.KIND_RESTIC)).all()}
    items = []
    for d in db.scalars(select(Device)).all():
        row = rows.get(d.id)
        if d.lifecycle != "ACTIVE" and row is None:
            continue
        items.append(
            {
                "device_id": d.id,
                "hostname": d.hostname,
                "display_name": d.display_name or "",
                "department": d.department,
                "site_id": d.site_id,
                "lifecycle": d.lifecycle,
                **_secret_meta(row),
            }
        )
    items.sort(key=lambda x: (x["display_name"] or x["hostname"] or "").lower())
    return {"schema_version": 1, "enabled": vault.enabled(), "items": items}


MIN_ADMIN_PASSWORD_LEN = 12


def change_admin_password(db: Session, user: User, current_password: str, new_password: str) -> None:
    if not verify_secret(current_password or "", user.password_hash):
        audit(db, actor_type="operator", actor_id=user.id, action="password_change", resource_type="user", resource_id=user.id, result="DENIED")
        db.commit()
        raise HTTPException(403, "Mevcut şifre yanlış.")
    if len(new_password or "") < MIN_ADMIN_PASSWORD_LEN:
        raise HTTPException(422, f"Yeni şifre en az {MIN_ADMIN_PASSWORD_LEN} karakter olmalı.")
    if new_password == current_password:
        raise HTTPException(422, "Yeni şifre mevcut şifreyle aynı olamaz.")
    user.password_hash = hash_secret(new_password)
    audit(db, actor_type="operator", actor_id=user.id, action="password_change", resource_type="user", resource_id=user.id)
    db.commit()


def archive_device(db: Session, user: User, device: Device) -> None:
    """Take a computer off the default list. Reversible: nothing is deleted and
    no credential is touched, but an archived device cannot check in (agent
    authentication requires lifecycle ACTIVE) until it is unarchived."""
    if device.lifecycle != "ACTIVE":
        raise HTTPException(409, "Yalnızca etkin bir bilgisayar arşivlenebilir.")
    device.lifecycle = "ARCHIVED"
    audit(db, actor_type="operator", actor_id=user.id, action="device_archive", resource_type="device", resource_id=device.id)
    db.commit()


def unarchive_device(db: Session, user: User, device: Device) -> None:
    """Only an archived device comes back; a revoked one never does."""
    if device.lifecycle != "ARCHIVED":
        raise HTTPException(409, "Yalnızca arşivlenmiş bir bilgisayar geri alınabilir.")
    device.lifecycle = "ACTIVE"
    audit(db, actor_type="operator", actor_id=user.id, action="device_unarchive", resource_type="device", resource_id=device.id)
    db.commit()


def revoke_device(db: Session, user: User | None, device: Device) -> None:
    device.lifecycle = "QUARANTINED"
    insts = db.scalars(select(Installation).where(Installation.device_id == device.id)).all()
    for inst in insts:
        inst.lifecycle = "REVOKED"
        for cred in db.scalars(select(DeviceCredential).where(DeviceCredential.installation_id == inst.id)).all():
            cred.revoked_at = now()
    actor_type = "operator"
    actor_id = device.id
    action = "device_revoke"
    if user is not None:
        actor_id = user.id
    else:
        actor_type = "device"
        action = "enrollment_abort"
    audit(db, actor_type=actor_type, actor_id=actor_id, action=action, resource_type="device", resource_id=device.id)
    db.commit()
    repo = db.scalar(select(Repository).where(Repository.device_id == device.id, Repository.role == "PRIMARY"))
    cred = None
    for inst in insts:
        cred = db.scalar(select(DeviceCredential).where(DeviceCredential.installation_id == inst.id, DeviceCredential.purpose == "gateway"))
        if cred is not None:
            break
    if repo is not None and cred is not None:
        notify_gateway(device.id, repo.generation_id, cred.secret_hash, revoked=True)


def save_device_selection(
    db: Session,
    user: User | None,
    device: Device,
    manifest: dict,
    *,
    applied_locally: bool = False,
    actor_type: str | None = None,
    actor_id: str | None = None,
) -> DeviceSelection:
    prev = db.scalar(select(DeviceSelection).where(DeviceSelection.device_id == device.id).order_by(DeviceSelection.version.desc()))
    version = (prev.version + 1) if prev else 1
    rec = DeviceSelection(
        id=new_id(),
        device_id=device.id,
        version=version,
        manifest_json=manifest,
        selected_bytes=int(manifest.get("selected_logical_bytes") or 0),
        selected_files=int(manifest.get("selected_file_count") or 0),
        applied_locally=applied_locally,
        created_by=user.id if user else "",
    )
    db.add(rec)
    audit(
        db,
        actor_type=actor_type or ("operator" if user else "setup"),
        actor_id=actor_id or (user.id if user else "wizard"),
        action="selection_save",
        resource_type="device",
        resource_id=device.id,
        extra={"version": version, "applied_locally": applied_locally, "bytes": rec.selected_bytes},
    )
    db.commit()
    return rec


def patch_device(db: Session, user: User, device: Device, fields: dict) -> Device:
    from app.schedule_policy import ScheduleError

    extra = {}
    try:
        _patch_device_fields(db, device, fields, extra)
    except ScheduleError as exc:
        raise HTTPException(422, str(exc)) from exc
    action = "device_rename" if "display_name" in fields else "device_patch"
    audit(db, actor_type="operator", actor_id=user.id, action=action, resource_type="device", resource_id=device.id, extra=extra)
    db.commit()
    return device


def _patch_device_fields(db: Session, device: Device, fields: dict, extra: dict) -> None:
    if "display_name" in fields and fields["display_name"] is not None:
        value = " ".join(str(fields["display_name"]).split())
        if any(ord(ch) < 32 or ord(ch) == 127 for ch in value) or len(value) > 64:
            raise HTTPException(422, "display_name invalid")
        device.display_name = value
        extra["display_name"] = value
    if "department" in fields and fields["department"] is not None:
        from app.schedule_policy import normalize_department

        device.department = normalize_department(str(fields["department"]))
        extra["department"] = device.department
    if "pause_new_backups" in fields and fields["pause_new_backups"] is not None:
        device.pause_new_backups = bool(fields["pause_new_backups"])
        extra["pause_new_backups"] = device.pause_new_backups
    if "preferred_start_hhmm" in fields and fields["preferred_start_hhmm"]:
        from app.schedule_policy import apply_preferred_to_policy, validate_preferred_start

        preferred = validate_preferred_start(device.site_id, str(fields["preferred_start_hhmm"]))
        device.preferred_start_hhmm = preferred
        extra["preferred_start_hhmm"] = preferred
        if device.assigned_policy_revision_id:
            rev = db.get(PolicyRevision, device.assigned_policy_revision_id)
            if rev is not None:
                cfg = apply_preferred_to_policy(rev.config_json, device.site_id, preferred)
                new_rev = PolicyRevision(
                    id=new_id(),
                    policy_id=rev.policy_id,
                    version=rev.version + 1,
                    content_hash=payload_hash(cfg),
                    config_json=cfg,
                )
                # unique (policy_id, version) — if collision, bump
                existing = db.scalar(
                    select(PolicyRevision).where(PolicyRevision.policy_id == rev.policy_id, PolicyRevision.version == new_rev.version)
                )
                if existing is not None:
                    latest = db.scalar(select(PolicyRevision).where(PolicyRevision.policy_id == rev.policy_id).order_by(PolicyRevision.version.desc()))
                    new_rev.version = (latest.version + 1) if latest else new_rev.version
                db.add(new_rev)
                db.flush()
                device.assigned_policy_revision_id = new_rev.id
    if "agent_sha" in fields and fields["agent_sha"] is not None:
        device.agent_sha = str(fields["agent_sha"])[:64]
        extra["agent_sha"] = device.agent_sha
    if "service_state" in fields and fields["service_state"] is not None:
        device.service_state = str(fields["service_state"])[:32]
        extra["service_state"] = device.service_state
    if "canary_state" in fields and fields["canary_state"] is not None:
        device.canary_state = str(fields["canary_state"])[:32]
    if "last_restore_state" in fields and fields["last_restore_state"] is not None:
        device.last_restore_state = str(fields["last_restore_state"])[:32]


def bind_setup_policy(db: Session, user: User | None, device: Device, *, source_roots: list[str], preferred_hhmm: str) -> PolicyRevision:
    from app.schedule_policy import apply_preferred_to_policy
    from app.security import default_policy_config, validate_policy_config

    cfg = apply_preferred_to_policy(default_policy_config(), device.site_id, preferred_hhmm)
    cfg["source_roots"] = list(source_roots)
    validate_policy_config(cfg)
    name = f"endpoint-{device.hostname}-{device.id[:8]}"
    pol = db.scalar(select(Policy).where(Policy.name == name))
    if pol is None:
        pol = Policy(id=new_id(), name=name, description=f"Endpoint policy for {device.hostname}")
        db.add(pol)
        db.flush()
        version = 1
    else:
        latest = db.scalar(select(PolicyRevision).where(PolicyRevision.policy_id == pol.id).order_by(PolicyRevision.version.desc()))
        version = (latest.version + 1) if latest else 1
    rev = PolicyRevision(id=new_id(), policy_id=pol.id, version=version, content_hash=payload_hash(cfg), config_json=cfg)
    db.add(rev)
    db.flush()
    device.assigned_policy_revision_id = rev.id
    device.preferred_start_hhmm = preferred_hhmm
    db.add(
        PolicyAssignment(
            id=new_id(),
            device_id=device.id,
            policy_revision_id=rev.id,
            assigned_by=user.id if user else "",
        )
    )
    audit(
        db,
        actor_type="operator" if user else "setup",
        actor_id=user.id if user else "wizard",
        action="setup_policy_bind",
        resource_type="device",
        resource_id=device.id,
        extra={"revision_id": rev.id, "preferred_start": preferred_hhmm},
    )
    db.commit()
    return rev


def assign_policy(db: Session, user: User, device: Device, revision_id: str) -> None:
    rev = db.get(PolicyRevision, revision_id)
    if rev is None:
        raise HTTPException(404, "policy revision not found")
    device.assigned_policy_revision_id = rev.id
    db.add(
        PolicyAssignment(
            id=new_id(),
            device_id=device.id,
            policy_revision_id=rev.id,
            assigned_by=user.id,
        )
    )
    audit(db, actor_type="operator", actor_id=user.id, action="policy_assign", resource_type="device", resource_id=device.id, extra={"revision_id": rev.id})
    db.commit()


def dashboard(db: Session) -> dict:
    every = db.scalars(select(Device)).all()
    devices = [d for d in every if d.lifecycle == "ACTIVE"]
    counts: dict[str, int] = {}
    for d in devices:
        counts[d.health] = counts.get(d.health, 0) + 1
    active_ids = {d.id for d in devices}
    recent = []
    for a, job_device_id in db.execute(
        select(BackupAttempt, BackupJob.device_id).join(BackupJob, BackupJob.id == BackupAttempt.job_id).order_by(BackupAttempt.created_at.desc()).limit(60)
    ):
        if job_device_id in active_ids:
            recent.append((a, next(d for d in devices if d.id == job_device_id)))
        if len(recent) >= 20:
            break
    sites = []
    ingress = fetch_gateway_ingress()
    site_snap = ingress.get("sites") if isinstance(ingress.get("sites"), dict) else {}
    for s in db.scalars(select(Site)).all():
        st = wan.site_status(db, s)
        observed = (site_snap.get(s.id) or {}).get("observed_bps")
        try:
            observed_f = float(observed) if observed is not None else None
        except (TypeError, ValueError):
            observed_f = None
        st["ingress_bps"] = observed_f
        st["ingress_mbps"] = (observed_f * 8 / 1_000_000) if observed_f else None
        try:
            from app.schedule_policy import site_policy as _site_policy

            st["schedule"] = _site_policy(s.id)
        except Exception:
            st["schedule"] = {}
        sites.append(st)
    # Messages users send from Stowline Backups ("IT'ye haber ver"), newest
    # first, with the computer they came from -- they were only visible as
    # an anonymous "Bilgisayar" row deep in the Olaylar log.
    by_id = {d.id: d for d in every}
    user_messages = []
    for e in db.scalars(select(AuditEvent).where(AuditEvent.action == "user_message").order_by(AuditEvent.occurred_at.desc()).limit(10)):
        dev = by_id.get(e.resource_id)
        user_messages.append(
            {
                "device_id": e.resource_id,
                "hostname": dev.hostname if dev else "",
                "display_name": (dev.display_name or "") if dev else "",
                "site_id": dev.site_id if dev else "",
                "department": dev.department if dev else "",
                "message": str((e.safe_diff or {}).get("message") or ""),
                "occurred_at": e.occurred_at.isoformat(),
            }
        )
    return {
        "schema_version": 1,
        "user_messages": user_messages,
        "total_devices": len(devices),
        "archived_count": len(every) - len(devices),
        "counts": counts,
        "sites": sites,
        **gateway_registration_state(),
        "recent_attempts": [
            {
                "id": a.id,
                "job_id": a.job_id,
                "outcome": a.outcome,
                "snapshot_id": a.snapshot_id,
                "error_class": a.error_class,
                "ended_at": a.ended_at.isoformat() if a.ended_at else None,
                "device_id": dev.id,
                "hostname": dev.hostname,
                "display_name": dev.display_name or "",
                "site_id": dev.site_id,
                "department": dev.department,
                "duration_ms": (a.summary_json or {}).get("duration_ms") or (a.summary_json or {}).get("backup_elapsed_ms"),
            }
            for a, dev in recent
        ],
    }


def expire_commands(db: Session) -> int:
    res = db.execute(
        update(Command)
        .where(Command.state.in_(("QUEUED", "DELIVERED")), Command.expires_at < now())
        .values(state="EXPIRED")
    )
    db.commit()
    return int(res.rowcount or 0)


def refresh_health(db: Session) -> int:
    n = 0
    for d in db.scalars(select(Device)).all():
        h = compute_health(last_success=d.last_success_at, last_attempt_outcome=d.last_attempt_outcome, last_seen=d.last_seen_at)
        if h != d.health:
            d.health = h
            n += 1
    db.commit()
    return n
