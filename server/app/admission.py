"""Durable per-site WAN admission leases."""

from __future__ import annotations

from datetime import timedelta

from fastapi import HTTPException
from sqlalchemy import select
from sqlalchemy.exc import IntegrityError
from sqlalchemy.orm import Session

from app.bandwidth import BandwidthError, compile_site
from app.db import Device, Site, WanAdmissionLease, utcnow
from app.security import new_id, as_utc

LEASE_TTL = timedelta(seconds=90)
PROVIDER_CLASSES = {"PROVIDER_RATE_LIMIT", "PROVIDER_QUOTA", "PROVIDER_5XX", "NETWORK"}
# A site with no explicit restore limit restores at this multiple of its
# per-agent upload share: office download links are several times wider
# than upload, and a restore tied to the upload share made a 208 MB
# self-service restore take 9 minutes on the pilot test PC.
RESTORE_DOWNLOAD_MULTIPLIER = 4
WAN_CLASSES = {"NORMAL_BACKUP", "INITIAL_SEED", "RESTORE", "CANARY", "MAINTENANCE"}


def seed_sites(db: Session) -> None:
    from app.schedule_policy import SITE_SCHEDULE

    for sid, cfg in SITE_SCHEDULE.items():
        if db.get(Site, sid) is None:
            db.add(
                Site(
                    id=sid,
                    display_name=str(cfg.get("display_name") or sid.title()),
                    business_backup_budget_percent=int(cfg.get("budget_percent") or 20),
                    max_concurrent_wan_backups=int(cfg.get("max_concurrent_wan_backups") or 1),
                    measured_upload_mbps=cfg.get("measured_upload_mbps"),
                )
            )


def reap_expired_leases(db: Session) -> int:
    n = 0
    now = utcnow()
    rows = db.scalars(select(WanAdmissionLease).where(WanAdmissionLease.released_at.is_(None))).all()
    for row in rows:
        if as_utc(row.expires_at) is not None and as_utc(row.expires_at) <= now:
            _release_row(row, "expired")
            n += 1
    if n:
        db.commit()
    return n


def _release_row(row: WanAdmissionLease, reason: str) -> None:
    row.released_at = utcnow()
    row.release_reason = reason
    row.hold_key = f"released:{row.id}"
    row.device_hold_key = f"released-device:{row.id}"


def _revive(db: Session, row: WanAdmissionLease) -> bool:
    """Re-takes an expired lease's original site slot and device hold. False
    (nothing changed) if another lease holds either one by now."""
    row.released_at = None
    row.release_reason = ""
    row.hold_key = f"site:{row.site_id}:slot:{row.slot}"
    row.device_hold_key = f"device:{row.device_id}"
    try:
        db.flush()
    except IntegrityError:
        db.rollback()
        return False
    return True


def active_leases(db: Session, site_id: str) -> list[WanAdmissionLease]:
    now = utcnow()
    rows = db.scalars(
        select(WanAdmissionLease).where(
            WanAdmissionLease.site_id == site_id,
            WanAdmissionLease.released_at.is_(None),
        )
    ).all()
    return [r for r in rows if as_utc(r.expires_at) is not None and as_utc(r.expires_at) > now]


def global_active_seeds(db: Session) -> int:
    now = utcnow()
    rows = db.scalars(
        select(WanAdmissionLease).where(
            WanAdmissionLease.wan_class == "INITIAL_SEED",
            WanAdmissionLease.released_at.is_(None),
        )
    ).all()
    return sum(1 for r in rows if as_utc(r.expires_at) is not None and as_utc(r.expires_at) > now)


def overdue_normal_waiting(db: Session, site_id: str) -> bool:
    now = utcnow()
    devices = db.scalars(select(Device).where(Device.site_id == site_id, Device.lifecycle == "ACTIVE")).all()
    for d in devices:
        if d.last_success_at is None:
            continue
        age = (now - as_utc(d.last_success_at)).total_seconds()
        if age > 24 * 3600:
            held = [
                r
                for r in active_leases(db, site_id)
                if r.device_id == d.id
            ]
            if not held:
                return True
    return False


def compile_for_site(site: Site, active_jobs: int = 1) -> dict:
    try:
        compiled = compile_site(site.measured_upload_mbps, site.business_backup_budget_percent or 20, active_jobs)
    except BandwidthError:
        return {
            "ok": False,
            "max_site_backup_bps": None,
            "gateway_bytes_per_sec": None,
            "restic_kibps": None,
            "agent_kibps": None,
        }
    if compiled["ok"] and compiled["max_site_backup_bps"] is not None:
        site.max_site_backup_bps = int(compiled["max_site_backup_bps"])
    return compiled


def device_policy_overlay(db: Session, device: Device, base: dict) -> dict:
    cfg = dict(base or {})
    site = db.get(Site, device.site_id) if device.site_id else None
    cfg["site_id"] = device.site_id or ""
    bw = dict(cfg.get("bandwidth") or {})
    if site is None:
        bw["upload_limit_kibps"] = 0
        cfg["bandwidth"] = bw
        return cfg
    compiled = compile_for_site(site, 1)
    if compiled["ok"]:
        bw["upload_limit_kibps"] = compiled["agent_kibps"]
        restore = site.restore_download_limit_kibps or RESTORE_DOWNLOAD_MULTIPLIER * compiled["agent_kibps"]
        now = utcnow()
        if site.emergency_restore_until and as_utc(site.emergency_restore_until) > now and site.emergency_restore_kibps > 0:
            restore = site.emergency_restore_kibps
        bw["restore_download_limit_kibps"] = restore
    else:
        bw["upload_limit_kibps"] = 0
    bw["lab_local_unlimited"] = False
    cfg["bandwidth"] = bw
    return cfg


def _denied(status: str, message: str = "", queue: int = 0) -> dict:
    return {
        "schema_version": 1,
        "status": status,
        "lease_id": "",
        "slot": 0,
        "class": "",
        "bandwidth_kibps": 0,
        "restore_download_kibps": 0,
        "expires_at": "",
        "queue_position": queue,
        "message": message,
    }


def _granted(row: WanAdmissionLease) -> dict:
    return {
        "schema_version": 1,
        "status": "GRANTED",
        "lease_id": row.id,
        "slot": row.slot,
        "class": row.wan_class,
        "bandwidth_kibps": row.bandwidth_kibps,
        "restore_download_kibps": row.restore_download_kibps,
        "expires_at": row.expires_at.isoformat() if row.expires_at else "",
        "queue_position": 0,
        "message": "",
        "percent_done": row.progress_percent,
    }


def acquire(db: Session, device: Device, inst_id: str, body: dict) -> dict:
    reap_expired_leases(db)
    site_id = device.site_id or ""
    site = db.get(Site, site_id) if site_id else None
    if site is None:
        return _denied("DENIED", "device has no site_id")
    now = utcnow()
    if site.pause_new_wan_admissions:
        return _denied("PAUSED", "pause_new_wan_admissions")
    if site.provider_unavailable_until and as_utc(site.provider_unavailable_until) > now:
        return _denied("PROVIDER_UNAVAILABLE", "provider circuit open")
    compiled = compile_for_site(site, 1)
    if not compiled["ok"]:
        return _denied("SITE_UNMEASURED", "measured_upload_mbps required")

    attempt_id = str(body.get("attempt_id") or new_id())
    existing = db.scalar(select(WanAdmissionLease).where(WanAdmissionLease.attempt_id == attempt_id))
    if existing is not None:
        if existing.released_at is None and as_utc(existing.expires_at) is not None and as_utc(existing.expires_at) > now:
            existing.last_renewed_at = now
            existing.expires_at = now + LEASE_TTL
            db.commit()
            return _granted(existing)
        return _denied("DENIED", "attempt_id already consumed")

    wan_class = str(body.get("class") or "NORMAL_BACKUP").upper()
    if wan_class not in WAN_CLASSES:
        return _denied("DENIED", "invalid class")

    if wan_class == "INITIAL_SEED":
        if not site.allow_site_seeds and global_active_seeds(db) >= 1:
            return _denied("QUEUED", "global seed admission is 1 until Stage 2")
        if overdue_normal_waiting(db, site_id):
            return _denied("QUEUED", "overdue normal backups outrank new seeds")

    active = active_leases(db, site_id)
    max_c = max(1, int(site.max_concurrent_wan_backups or 1))
    used = {r.slot for r in active}
    if len(active) >= max_c:
        return _denied("SITE_CAPACITY_WAIT", "site concurrency exhausted", queue=len(active))

    slot = next(i for i in range(1, max_c + 1) if i not in used)
    fair = compile_for_site(site, len(active) + 1)
    if not fair["ok"]:
        return _denied("SITE_UNMEASURED", "measured_upload_mbps required")
    restore_kib = site.restore_download_limit_kibps or RESTORE_DOWNLOAD_MULTIPLIER * fair["agent_kibps"]
    if site.emergency_restore_until and as_utc(site.emergency_restore_until) > now and site.emergency_restore_kibps > 0:
        restore_kib = site.emergency_restore_kibps

    row = WanAdmissionLease(
        id=new_id(),
        site_id=site_id,
        device_id=device.id,
        installation_id=inst_id,
        job_id=str(body.get("job_id") or ""),
        attempt_id=attempt_id,
        wan_class=wan_class,
        slot=slot,
        hold_key=f"site:{site_id}:slot:{slot}",
        device_hold_key=f"device:{device.id}",
        acquired_at=now,
        expires_at=now + LEASE_TTL,
        last_renewed_at=now,
        bandwidth_kibps=int(fair["agent_kibps"]),
        restore_download_kibps=int(restore_kib),
    )
    db.add(row)
    try:
        db.commit()
    except IntegrityError:
        db.rollback()
        raced = db.scalar(select(WanAdmissionLease).where(WanAdmissionLease.attempt_id == attempt_id))
        if raced is not None and raced.released_at is None and as_utc(raced.expires_at) is not None and as_utc(raced.expires_at) > utcnow():
            return _granted(raced)
        return _denied("SITE_CAPACITY_WAIT", "lost admission race")
    return _granted(row)


def renew(db: Session, device: Device, lease_id: str, progress: dict | None = None) -> dict:
    row = db.get(WanAdmissionLease, lease_id)
    if row is None or row.device_id != device.id:
        raise HTTPException(404, "lease not found")
    now = utcnow()
    # A renewal proves the owner is alive and still running. If its lease
    # lapsed only because renewals were missed (confirmed live: 80 s of
    # server errors on a 2 GB+ backup), take it back instead of denying --
    # the backup keeps uploading either way, and a denied lease hid it:
    # the panel showed the busy PC as OFFLINE with no progress, and the
    # freed site slot let a second PC start alongside it.
    if row.released_at is not None and not (row.release_reason == "expired" and _revive(db, row)):
        device.last_seen_at = now
        db.commit()
        return _denied("DENIED", "lease already released")
    row.last_renewed_at = now
    row.expires_at = now + LEASE_TTL
    progress = progress or {}
    fields = {
        "percent_done": "progress_percent",
        "files_done": "progress_files_done",
        "total_files": "progress_total_files",
        "bytes_done": "progress_bytes_done",
        "total_bytes": "progress_total_bytes",
    }
    # A sample that contradicts itself (more bytes done than the total it
    # also reports) is dropped whole -- not clamped, not partially applied --
    # so a transient bad reading never corrupts an otherwise-good row. The
    # renewal itself still succeeds: one bad sample must not cost the lease.
    candidate_bytes_done = progress.get("bytes_done", row.progress_bytes_done)
    candidate_total_bytes = progress.get("total_bytes", row.progress_total_bytes)
    if (
        candidate_bytes_done is not None
        and candidate_total_bytes is not None
        and candidate_bytes_done > candidate_total_bytes
    ):
        progress = {}
    changed = False
    for source, target in fields.items():
        if source in progress:
            setattr(row, target, progress[source])
            changed = True
    if changed:
        row.progress_updated_at = now
    # The agent neither heartbeats nor polls while a backup runs, so a lease
    # renewal is the only sign of life the control plane gets for the whole
    # run. Without this the panel calls a busy, healthy computer OFFLINE.
    device.last_seen_at = now
    db.commit()
    return _granted(row)


def release(db: Session, device: Device, lease_id: str, reason: str) -> dict:
    row = db.get(WanAdmissionLease, lease_id)
    if row is None or row.device_id != device.id:
        raise HTTPException(404, "lease not found")
    if row.released_at is None:
        _release_row(row, reason or "released")
        db.commit()
    return {"schema_version": 1, "status": "RELEASED", "lease_id": row.id}


def trip_provider(db: Session, site: Site, seconds: int = 900) -> None:
    site.provider_unavailable_until = utcnow() + timedelta(seconds=seconds)


def site_status(db: Session, site: Site) -> dict:
    compiled = compile_for_site(site, 1)
    active = active_leases(db, site.id)
    devices = db.scalars(select(Device).where(Device.site_id == site.id)).all()
    oldest = None
    for d in devices:
        if d.last_success_at is None:
            continue
        age = (utcnow() - as_utc(d.last_success_at)).total_seconds()
        if oldest is None or age > oldest:
            oldest = age
    now = utcnow()
    status = "HEALTHY"
    if site.pause_new_wan_admissions:
        status = "PAUSED"
    elif site.provider_unavailable_until and as_utc(site.provider_unavailable_until) > now:
        status = "PROVIDER_UNAVAILABLE"
    elif not compiled["ok"]:
        status = "WAIT"
    elif len(active) >= max(1, site.max_concurrent_wan_backups or 1):
        status = "WAIT"
    queued = max(0, len([d for d in devices if d.lifecycle == "ACTIVE"]) - len(active))
    ceiling_mbps = None
    if compiled["ok"] and compiled["max_site_backup_bps"]:
        ceiling_mbps = compiled["max_site_backup_bps"] / 1_000_000
    ingress_mbps = None
    if site.ingress_updated_at and site.ingress_bytes:
        # last reported payload bytes over an unknown window — UI shows raw bytes + age
        ingress_mbps = None
    return {
        "id": site.id,
        "display_name": site.display_name,
        "measured_upload_mbps": site.measured_upload_mbps,
        "business_backup_budget_percent": site.business_backup_budget_percent,
        "hard_site_ceiling_mbps": ceiling_mbps,
        "compiled_agent_kibps": compiled.get("agent_kibps"),
        "restic_kibps": compiled.get("restic_kibps"),
        "max_concurrent_wan_backups": site.max_concurrent_wan_backups,
        "active": len(active),
        "queued": queued,
        "ingress_bytes": site.ingress_bytes,
        "oldest_backup_seconds": oldest,
        "status": status,
        "pause_new_wan_admissions": site.pause_new_wan_admissions,
        "user_impact_flag": site.user_impact_flag,
        "allow_site_seeds": site.allow_site_seeds,
        "emergency_restore_until": site.emergency_restore_until.isoformat() if site.emergency_restore_until else None,
    }
