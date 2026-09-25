"""Admin device status: traffic light, freshness, next run, human errors."""

from __future__ import annotations

from datetime import datetime, timezone

from sqlalchemy import select
from sqlalchemy.orm import Session

from app.db import BackupAttempt, BackupJob, Command, Device, DeviceSelection, Heartbeat, PolicyRevision, RestoreRequest, WanAdmissionLease
from app.errors_ux import explain_error, infer_cancel_cause
from app.schedule_policy import next_eligible_run, site_policy
from app.security import as_utc, compute_health
from app import services


def _age_seconds(ts: datetime | None, now: datetime) -> int | None:
    if ts is None:
        return None
    return int((now - as_utc(ts)).total_seconds())


def traffic_light(health: str, *, has_selection: bool, paused: bool, lifecycle: str) -> str:
    if lifecycle and lifecycle.upper() not in {"ACTIVE"}:
        return "RED"
    if not has_selection:
        return "RED"
    if health in {"FAILED", "OFFLINE", "MISSED"}:
        return "RED"
    if paused or health in {"LATE", "PARTIAL", "NEVER_BACKED_UP", "RESTORE_REQUIRED_ATTENTION"}:
        return "YELLOW"
    if health == "HEALTHY":
        return "GREEN"
    return "YELLOW"


def latest_selection(db: Session, device_id: str) -> DeviceSelection | None:
    return db.scalar(
        select(DeviceSelection)
        .where(DeviceSelection.device_id == device_id)
        .order_by(DeviceSelection.version.desc())
    )


def latest_heartbeat(db: Session, device_id: str) -> Heartbeat | None:
    return db.scalar(select(Heartbeat).where(Heartbeat.device_id == device_id).order_by(Heartbeat.created_at.desc()))


# A live lease of one of these classes means a backup is uploading right now.
# RESTORE and MAINTENANCE leases are not backups.
_BACKUP_LEASE_CLASSES = frozenset({"NORMAL_BACKUP", "INITIAL_SEED", "CANARY"})


def running_backup_lease(db: Session, device: Device, now: datetime) -> WanAdmissionLease | None:
    leases = db.scalars(
        select(WanAdmissionLease).where(WanAdmissionLease.device_id == device.id, WanAdmissionLease.released_at.is_(None))
    ).all()
    live = [x for x in leases if x.wan_class in _BACKUP_LEASE_CLASSES and as_utc(x.expires_at) > now]
    return max(live, key=lambda x: as_utc(x.acquired_at)) if live else None


def device_admin_view(db: Session, device: Device) -> dict:
    now = datetime.now(timezone.utc)
    wan = services.gateway_registration_state()
    sel = latest_selection(db, device.id)
    hb = latest_heartbeat(db, device.id)
    rev = db.get(PolicyRevision, device.assigned_policy_revision_id) if device.assigned_policy_revision_id else None
    sched = (rev.config_json or {}).get("schedule") if rev else {}
    preferred = device.preferred_start_hhmm or (sched or {}).get("window_start") or "12:00"
    jobs = db.scalars(select(BackupJob).where(BackupJob.device_id == device.id)).all()
    job_ids = [j.id for j in jobs]
    atts: list[BackupAttempt] = []
    if job_ids:
        atts = list(
            db.scalars(select(BackupAttempt).where(BackupAttempt.job_id.in_(job_ids)).order_by(BackupAttempt.created_at.desc()).limit(50))
        )
    last = atts[0] if atts else None
    success = next((a for a in atts if a.outcome == "SUCCEEDED" and not a.published_not_green), None)
    canary = next((a for a in atts if (a.summary_json or {}).get("kind") == "CANARY" or a.outcome in {"CANARY", "READY"} or (jobs and False)), None)
    canary_jobs = [j for j in jobs if (j.kind or "").upper() == "CANARY"]
    canary_state = device.canary_state or ""
    if canary_jobs:
        cj = canary_jobs[-1]
        canary_state = cj.state or canary_state
    restores = list(db.scalars(select(RestoreRequest).where(RestoreRequest.device_id == device.id).order_by(RestoreRequest.created_at.desc()).limit(5)))
    last_restore = restores[0] if restores else None

    paused = bool(getattr(device, "pause_new_backups", False))
    has_sel = bool(sel and (sel.manifest_json or {}).get("source_roots"))
    health = compute_health(
        last_success=device.last_success_at,
        last_attempt_outcome=device.last_attempt_outcome,
        last_seen=device.last_seen_at,
        now=now,
    )
    service_state = device.service_state or ""
    offline = health == "OFFLINE" or device.last_seen_at is None
    service_stopped = service_state.lower() in {"stopped", "not_installed"}
    # Prefer the most recent attempt's specific error_class as the *code* when
    # one exists, but never let it override the currently-computed
    # offline/service_stopped/wan_ready context — explain_error already
    # prioritizes those correctly; a stale error_class from weeks ago must not
    # claim "Disk issue" over a device that is offline or stopped right now.
    # Prefer the most recent attempt's specific error_class as the *code* when
    # one exists, but never let it override the currently-computed
    # offline/service_stopped/wan_ready context — explain_error already
    # prioritizes those correctly; a stale error_class from weeks ago must not
    # claim "Disk issue" over a device that is offline or stopped right now.
    code = device.last_attempt_outcome if device.last_attempt_outcome not in {"SUCCEEDED", ""} else ""
    if last and last.error_class:
        code = last.error_class
    cancel_cause = ""
    if (code or "").upper() == "CANCELLED" and last is not None:
        operator_cancels = db.scalars(
            select(Command.acked_at)
            .where(Command.device_id == device.id, Command.kind == "CANCEL_CURRENT_SAFE_OPERATION", Command.acked_at.is_not(None))
            .order_by(Command.acked_at.desc())
            .limit(5)
        ).all()
        cancel_cause = infer_cancel_cause(last.ended_at, sched, operator_cancels)
    running = running_backup_lease(db, device, now)
    if running is not None:
        # Mid-run there is no problem to report: an old failure or an
        # unconfigured-gateway note would only contradict what is happening.
        human = explain_error(None)
    else:
        human = explain_error(
            code,
            offline=offline and health != "NEVER_BACKED_UP",
            service_stopped=service_stopped and not offline,
            wan_ready=wan.get("wan_ready"),
            cancel_cause=cancel_cause,
            hard_stop=str((sched or {}).get("hard_stop") or ""),
        )

    rate = None
    progress = None
    if last:
        summary = last.summary_json or {}
        elapsed = summary.get("backup_elapsed_ms") or summary.get("duration_ms")
        payload = summary.get("gateway_payload_bytes") or summary.get("restic_data_added") or summary.get("bytes_added")
        if elapsed and payload and int(elapsed) > 0:
            rate = round((int(payload) / (int(elapsed) / 1000)) / 1024, 1)
        progress = {
            "phase": last.phase,
            "outcome": last.outcome,
            "started_at": last.started_at.isoformat() if last.started_at else None,
            "ended_at": last.ended_at.isoformat() if last.ended_at else None,
        }
    if running is not None:
        progress = {
            # PREPARING: lease is live but no real sample has arrived yet --
            # covers both "just granted, first renewal hasn't happened" and
            # "an agent build that never sends progress at all" (the two are
            # indistinguishable by design; a client-side elapsed-time check
            # is what tells them apart for the UI, not this field).
            # FINALIZING: restic's percent counts bytes *read*, not uploaded;
            # under a WAN limit it reaches 100% long before the upload ends.
            "phase": (
                "PREPARING"
                if not running.progress_total_bytes
                else "FINALIZING"
                if (running.progress_bytes_done or 0) >= running.progress_total_bytes
                else "BACKING_UP"
            ),
            "outcome": "RUNNING",
            "started_at": as_utc(running.acquired_at).isoformat(),
            "ended_at": None,
            "percent_done": running.progress_percent,
            "files_done": running.progress_files_done,
            "total_files": running.progress_total_files,
            "bytes_done": running.progress_bytes_done,
            "total_bytes": running.progress_total_bytes,
            "updated_at": as_utc(running.progress_updated_at).isoformat() if running.progress_updated_at else None,
        }

    next_run = next_eligible_run(device.site_id, preferred) if device.site_id else None
    try:
        site_pol = site_policy(device.site_id) if device.site_id else {}
    except Exception:
        site_pol = {}

    return {
        "hostname": device.hostname,
        "display_name": device.display_name or "",
        "site_id": device.site_id,
        "department": device.department,
        "online": running is not None or (bool(device.last_seen_at) and health != "OFFLINE"),
        "backing_up": running is not None,
        "backup_started_at": as_utc(running.acquired_at).isoformat() if running is not None else None,
        # restic takes its --limit-upload once, from the lease granted when the run started
        "run_limit_kibps": int(running.bandwidth_kibps) if running is not None and running.bandwidth_kibps else None,
        "last_heartbeat": device.last_seen_at.isoformat() if device.last_seen_at else None,
        "current_operation": "backup_running" if running is not None else (hb.current_operation if hb else "none"),
        "agent_version": device.agent_version,
        "agent_sha": device.agent_sha,
        "service_health": service_state or ("likely_running" if device.last_seen_at and _age_seconds(device.last_seen_at, now) is not None and _age_seconds(device.last_seen_at, now) < 900 else "unknown"),
        "wan_ready": wan.get("wan_ready"),
        "gateway_registration": wan.get("gateway_registration"),
        "selected_source_bytes": int(sel.selected_bytes) if sel else 0,
        "selected_source_file_count": int(sel.selected_files) if sel else 0,
        "selection_version": sel.version if sel else 0,
        "selection_applied_locally": bool(sel.applied_locally) if sel else False,
        "pending_local_apply": bool(sel and not sel.applied_locally),
        "pending_message": "Yapılandırma değişikliği bekliyor — Korunan Dosyalar sekmesinden Uygula'ya basın; Setup Wizard'ı yeniden çalıştırmak gerekmez." if (sel and not sel.applied_locally) else "",
        "preferred_schedule": preferred,
        "next_eligible_run": next_run,
        "last_backup_start": last.started_at.isoformat() if last and last.started_at else None,
        "last_backup_end": last.ended_at.isoformat() if last and last.ended_at else None,
        "last_successful_snapshot": device.last_success_snapshot,
        "backup_age_seconds": _age_seconds(device.last_success_at, now),
        "current_state": health,
        "current_progress": progress,
        "effective_upload_kibps_observed": rate,
        "last_error_code": (last.error_class if last else "") or device.last_attempt_outcome,
        "last_error_human": human,
        "canary_state": canary_state or (canary.outcome if canary else ""),
        "last_restore_test_state": device.last_restore_state or (last_restore.state if last_restore else ""),
        "pause_new_backups": paused,
        "traffic_light": traffic_light(health, has_selection=has_sel, paused=paused, lifecycle=device.lifecycle),
        "site_policy": site_pol,
        "lifecycle": device.lifecycle,
        "health": health,
    }
