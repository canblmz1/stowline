from __future__ import annotations

import hashlib
import json
from pathlib import Path
from typing import Literal

from fastapi import Depends, FastAPI, Header, HTTPException, Request, Response
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import FileResponse, JSONResponse
from fastapi.staticfiles import StaticFiles
from pydantic import BaseModel, ConfigDict, Field
from sqlalchemy import select, text
from sqlalchemy.orm import Session

from app.config import settings
from app.db import (
    AuditEvent,
    BackupAttempt,
    BackupJob,
    Command,
    Device,
    Policy,
    PolicyRevision,
    Repository,
    RestoreRequest,
    Snapshot,
    SnapshotCatalog,
    SnapshotCatalogEntry,
    StorageProfile,
    make_engine,
    session_factory,
)
from app import services
from app.errors_ux import explain_error
from app.security import new_id, payload_hash, sha256_hex, validate_policy_config
from app.version import RELEASE_VERSION

engine = make_engine(settings.database_url)
SessionLocal = session_factory(engine)


def get_db():
    db = SessionLocal()
    try:
        yield db
    finally:
        db.close()


class StrictModel(BaseModel):
    model_config = ConfigDict(extra="forbid")


class LoginIn(StrictModel):
    username: str
    password: str


class EnrollmentTokenIn(StrictModel):
    label: str = "pilot"
    minutes: int = Field(default=15, ge=1, le=60)
    site_id: str = "branch"
    department: str = ""


class DevicePatchIn(StrictModel):
    display_name: str | None = None
    department: str | None = None
    pause_new_backups: bool | None = None
    preferred_start_hhmm: str | None = None
    agent_sha: str | None = None
    service_state: str | None = None
    canary_state: str | None = None
    last_restore_state: str | None = None


class SelectionIn(StrictModel):
    selected: list[dict] = Field(default_factory=list)
    office_files: dict[str, list[str]] = Field(default_factory=dict)
    applied_locally: bool = False
    preferred_start_hhmm: str = "12:00"


class EstimateIn(StrictModel):
    site_id: str
    preferred_start_hhmm: str
    logical_bytes: int = Field(ge=0)
    measured_upload_mbps: float | None = None


class SitePatchIn(StrictModel):
    measured_upload_mbps: float | None = None
    business_backup_budget_percent: int | None = Field(default=None, ge=5, le=50)
    max_concurrent_wan_backups: int | None = Field(default=None, ge=1, le=4)
    restore_download_limit_kibps: int | None = Field(default=None, ge=0)
    pause_new_wan_admissions: bool | None = None
    allow_site_seeds: bool | None = None
    user_impact_flag: str | None = None


class EmergencyRestoreIn(StrictModel):
    hours: int = Field(default=4, ge=1, le=24)
    restore_download_kibps: int = Field(ge=1, le=100000)
    pause_new_backups: bool = True


class LeaseAcquireIn(StrictModel):
    job_id: str = ""
    attempt_id: str = ""
    slot_key: str = ""
    class_: str = Field(default="NORMAL_BACKUP", alias="class")
    installation_id: str = ""
    model_config = ConfigDict(extra="forbid", populate_by_name=True)


class LeaseReleaseIn(StrictModel):
    reason: str = "released"


class LeaseRenewIn(StrictModel):
    percent_done: float | None = Field(default=None, ge=0, le=100)
    files_done: int | None = Field(default=None, ge=0)
    total_files: int | None = Field(default=None, ge=0)
    bytes_done: int | None = Field(default=None, ge=0)
    total_bytes: int | None = Field(default=None, ge=0)


class PasswordChangeIn(StrictModel):
    current_password: str
    new_password: str


class SecretIn(StrictModel):
    kind: str = "restic-password"
    secret: str


class SelfServiceSelectionIn(StrictModel):
    source_roots: list[str] = Field(default_factory=list, max_length=500)
    source: Literal["self_service", "device_sync"] = "self_service"


class UserMessageIn(StrictModel):
    message: str = Field(default="", max_length=4000)


class SelfServiceRestoreIn(StrictModel):
    snapshot_id: str
    selections: list[str] = Field(default_factory=list)
    destination: str = ""


class RevealIn(StrictModel):
    password: str
    kind: str = "restic-password"


class EnrollIn(StrictModel):
    token: str
    hostname: str = ""
    agent_version: str = ""
    installation_id: str = ""
    capabilities: dict = Field(default_factory=dict)


class HeartbeatIn(StrictModel):
    sequence: int = 0
    agent_version: str = ""
    current_operation: str = "none"
    policy_revision_id: str = ""
    agent_sha256: str = ""


class AttemptIn(StrictModel):
    job_id: str = ""
    attempt_id: str = ""
    attempt_no: int = 1
    slot_key: str = ""
    outcome: str
    phase: str = ""
    snapshot_id: str = ""
    error_class: str = ""
    consistency: str = ""
    published_not_green: bool = False
    attempt_ended_at: str = ""
    bytes_added: int | None = None
    files_new: int | None = None
    duration_ms: int | None = None
    logical_source_bytes: int | None = None
    restic_total_bytes_processed: int | None = None
    restic_data_added: int | None = None
    gateway_payload_bytes: int | None = None
    backup_elapsed_ms: int | None = None
    queue_elapsed_ms: int | None = None
    retry_count: int | None = None
    retry_after_seconds: int | None = None


class CommandIn(StrictModel):
    kind: str
    payload: dict = Field(default_factory=dict)


class RestoreIn(StrictModel):
    device_id: str
    snapshot_id: str
    selections: list[str] = Field(default_factory=list)


class PolicyIn(StrictModel):
    name: str
    description: str = ""
    config: dict


class PolicyAssignIn(StrictModel):
    revision_id: str


class BrowseIn(StrictModel):
    snapshot_id: str
    prefix: str = ""


class BrowseLocalDirIn(StrictModel):
    path: str
    cursor: str = ""
    limit: int = 200


class CatalogCreateIn(StrictModel):
    snapshot_id: str
    declared_file_count: int = Field(ge=0)
    declared_directory_count: int = Field(ge=0)
    declared_logical_bytes: int = Field(ge=0)
    declared_checksum: str


class CatalogEntryIn(StrictModel):
    path: str
    parent_path: str = ""
    name: str
    type: str
    size: int | None = None
    mtime: str = ""


class CatalogEntriesIn(StrictModel):
    entries: list[CatalogEntryIn] = Field(max_length=500)


class CatalogFinalizeIn(StrictModel):
    file_count: int = Field(ge=0)
    directory_count: int = Field(ge=0)
    logical_bytes: int = Field(ge=0)
    checksum: str


class CatalogFailIn(StrictModel):
    error_class: str


class ApplySelectionIn(StrictModel):
    source_roots: list[str] = Field(default_factory=list)
    sensitive_consents: list[str] = Field(default_factory=list)


class CommandAckIn(StrictModel):
    state: Literal["SUCCEEDED", "FAILED", "CANCELLED"]
    error_class: str = ""
    snapshot_id: str = ""
    extra: dict = Field(default_factory=dict)


COOKIE = "stowline_session"

app = FastAPI(title="Stowline control plane", version=RELEASE_VERSION)
if settings.cors_origins:
    app.add_middleware(
        CORSMiddleware,
        allow_origins=[o.strip() for o in settings.cors_origins.split(",") if o.strip()],
        allow_credentials=True,
        allow_methods=["GET", "POST", "PATCH"],
        allow_headers=["Authorization", "Content-Type", "Idempotency-Key", "X-CSRF-Token"],
    )


@app.middleware("http")
async def admin_ui_no_cache(request: Request, call_next):
    """The pilot UI ships as plain static assets under stable names. Prevent
    browsers from pinning an older JavaScript build after a deploy."""
    response = await call_next(request)
    if request.url.path == "/" or request.url.path.startswith("/ui/"):
        response.headers["Cache-Control"] = "no-store, max-age=0"
        response.headers["Pragma"] = "no-cache"
    return response


@app.on_event("startup")
def _startup():
    from app.config import require_admin_credentials_set

    require_admin_credentials_set(settings)
    Path("var").mkdir(exist_ok=True)
    from app.db import Base
    from app.migrate import upgrade

    upgrade(settings.database_url)
    db = SessionLocal()
    try:
        services.seed(db, settings.admin_username, settings.admin_password)
    finally:
        db.close()


def request_id(request: Request) -> str:
    return request.headers.get("x-request-id") or new_id()


def csrf_ok(request: Request) -> None:
    if request.method in ("GET", "HEAD", "OPTIONS"):
        return
    origin = request.headers.get("origin") or ""
    if origin:
        host = request.headers.get("host") or ""
        if host and host not in origin and not settings.lab_mode:
            raise HTTPException(403, "csrf origin")


def operator(request: Request, db: Session = Depends(get_db)):
    csrf_ok(request)
    raw = request.cookies.get(COOKIE)
    user = services.session_user(db, raw)
    if user is None:
        raise HTTPException(401, "unauthenticated")
    return user


def agent_pair(request: Request, db: Session = Depends(get_db), authorization: str = Header(default="")):
    secret = ""
    if authorization.lower().startswith("bearer "):
        secret = authorization[7:].strip()
    pair = services.device_from_control_secret(db, secret)
    if pair is None:
        raise HTTPException(401, "unauthenticated")
    return pair


@app.middleware("http")
async def rid_mw(request: Request, call_next):
    rid = request_id(request)
    if request.method in ("POST", "PATCH", "PUT"):
        cl = request.headers.get("content-length")
        if cl and int(cl) > 256 * 1024:
            return JSONResponse({"code": "too_large", "message": "body too large", "retryable": False, "correlation_id": rid}, 413)
    try:
        resp = await call_next(request)
    except HTTPException as e:
        return JSONResponse(
            {"code": "error", "message": str(e.detail), "retryable": False, "correlation_id": rid},
            e.status_code,
        )
    resp.headers["X-Request-ID"] = rid
    return resp


@app.get("/health")
def health():
    st = services.gateway_registration_state()
    return {"status": "ok", "product": "stowline-control", "version": RELEASE_VERSION, "wan_ready": st["wan_ready"], "gateway_registration": st["gateway_registration"]}


@app.get("/ready")
def ready():
    db = SessionLocal()
    try:
        db.execute(text("SELECT 1"))
        services.probe_gateway_health()
        st = services.gateway_registration_state()
        return {"status": "ready", "database": "ok", **st}
    finally:
        db.close()


@app.get("/api/v1/version")
def version():
    return {"schema_version": 1, "product": "stowline", "version": RELEASE_VERSION, "org_name": settings.org_name, "not": ["production", "1.0"]}


@app.post("/api/v1/auth/login")
def login(body: LoginIn, response: Response, db: Session = Depends(get_db)):
    raw = services.login(db, body.username, body.password, settings.session_hours)
    response.set_cookie(COOKIE, raw, httponly=True, samesite="strict", secure=settings.cookie_secure(), max_age=settings.session_hours * 3600)
    return {"schema_version": 1, "ok": True}


@app.get("/api/v1/admin/me")
def me(user=Depends(operator)):
    return {"schema_version": 1, "id": user.id, "username": user.username, "role": user.role}


@app.post("/api/v1/auth/logout")
def logout(request: Request, response: Response, db: Session = Depends(get_db), user=Depends(operator)):
    raw = request.cookies.get(COOKIE)
    if raw:
        from app.db import AdminSession

        sess = db.scalar(select(AdminSession).where(AdminSession.token_hash == sha256_hex(raw)))
        if sess is not None:
            from app.db import utcnow

            sess.revoked_at = utcnow()
            db.commit()
    response.delete_cookie(COOKIE)
    return {"ok": True}


@app.get("/api/v1/admin/dashboard")
def dash(db: Session = Depends(get_db), user=Depends(operator)):
    return services.dashboard(db)


@app.get("/api/v1/admin/devices")
def devices(db: Session = Depends(get_db), user=Depends(operator), q: str = "", health: str = "", scope: str = "active"):
    """scope: "active" (default: the computers being backed up), "archived"
    (everything that is not active: archived and revoked) or "all"."""
    from app import device_status

    rows = db.scalars(select(Device)).all()
    if scope == "active":
        rows = [d for d in rows if d.lifecycle == "ACTIVE"]
    elif scope == "archived":
        rows = [d for d in rows if d.lifecycle != "ACTIVE"]
    elif scope != "all":
        raise HTTPException(422, "scope must be active, archived or all")
    out = []
    for d in rows:
        if q and q.lower() not in ((d.display_name or "") + d.hostname + d.department + d.id).lower():
            continue
        if health and d.health != health:
            continue
        row = _device(d)
        # Pilot-scale fleet (a handful of devices): one extra per-row status
        # computation here is cheap, and the list view needs the same
        # online/service/progress fields the detail page already shows --
        # duplicating device_admin_view's own per-device queries into a
        # second, list-only code path was more likely to drift out of sync
        # than one admin_devices call being a bit heavier.
        view = device_status.device_admin_view(db, d)
        row["online"] = view["online"]
        row["service_health"] = view["service_health"]
        row["next_eligible_run"] = view["next_eligible_run"]
        row["selected_source_bytes"] = view["selected_source_bytes"]
        row["current_operation"] = view["current_operation"]
        row["current_progress"] = view["current_progress"]
        row["backing_up"] = view["backing_up"]
        row["backup_started_at"] = view["backup_started_at"]
        row["run_limit_kibps"] = view["run_limit_kibps"]
        row["last_error_human"] = view["last_error_human"]
        row["traffic_light"] = view["traffic_light"]
        row["site_policy"] = view["site_policy"]
        row["health"] = view["health"]
        out.append(row)
    return {"schema_version": 1, "items": out}


@app.get("/api/v1/admin/devices/{device_id}")
def device_detail(device_id: str, db: Session = Depends(get_db), user=Depends(operator)):
    d = db.get(Device, device_id)
    if d is None:
        raise HTTPException(404, "not found")
    repo = db.scalar(select(Repository).where(Repository.device_id == d.id, Repository.role == "PRIMARY"))
    from app.db import BackupJob

    jobs = db.scalars(select(BackupJob).where(BackupJob.device_id == d.id)).all()
    job_ids = [j.id for j in jobs]
    atts = []
    if job_ids:
        atts = db.scalars(select(BackupAttempt).where(BackupAttempt.job_id.in_(job_ids)).order_by(BackupAttempt.created_at.desc()).limit(50)).all()
    snaps = []
    if repo:
        snaps = db.scalars(select(Snapshot).where(Snapshot.repository_id == repo.id)).all()
    from app.device_status import device_admin_view, latest_selection

    sel = latest_selection(db, d.id)
    return {
        "schema_version": 1,
        "device": _device(d),
        "status": device_admin_view(db, d),
        "selection": {
            "version": sel.version,
            "manifest": sel.manifest_json,
            "selected_bytes": sel.selected_bytes,
            "selected_files": sel.selected_files,
            "applied_locally": sel.applied_locally,
        }
        if sel
        else None,
        "repository": {"generation_id": repo.generation_id, "location": repo.location, "engine_repo_id": repo.engine_repo_id, "state": repo.state} if repo else None,
        "attempts": [_attempt(a) for a in atts],
        "snapshots": [{"id": s.engine_snapshot_id, "consistency": s.consistency, "observation_state": s.observation_state} for s in snaps],
        "vault": services.device_secret_status(db, d),
    }


@app.get("/api/v1/admin/devices/{device_id}/snapshot-reconciliation")
def admin_snapshot_reconciliation(device_id: str, db: Session = Depends(get_db), user=Depends(operator)):
    d = db.get(Device, device_id)
    if d is None:
        raise HTTPException(404, "not found")
    return {"schema_version": 1, **services.reconcile_snapshots(db, d)}


@app.post("/api/v1/admin/enrollment-tokens")
def enrollment_tokens(body: EnrollmentTokenIn, db: Session = Depends(get_db), user=Depends(operator), idempotency_key: str = Header(default="", alias="Idempotency-Key")):
    _ = idempotency_key
    raw, rec = services.mint_enrollment_token(
        db, user, body.label, body.minutes or settings.enrollment_minutes, body.site_id, body.department
    )
    return {
        "schema_version": 1,
        "token_id": rec.id,
        "token": raw,
        "expires_at": rec.expires_at.isoformat(),
        "shown_once": True,
        "site_id": rec.site_id,
        "department": rec.department,
    }


@app.post("/api/v1/admin/devices/{device_id}/commands")
def admin_command(device_id: str, body: CommandIn, db: Session = Depends(get_db), user=Depends(operator)):
    d = db.get(Device, device_id)
    if d is None:
        raise HTTPException(404, "not found")
    cmd = services.enqueue_command(db, user, d, body.kind, body.payload)
    return {
        "schema_version": 1,
        "command_id": cmd.id,
        "kind": cmd.kind,
        "expires_at": cmd.expires_at.isoformat(),
        "deduplicated": bool(getattr(cmd, "_deduplicated", False)),
    }


@app.post("/api/v1/admin/devices/{device_id}/revoke")
def admin_revoke(device_id: str, db: Session = Depends(get_db), user=Depends(operator)):
    d = db.get(Device, device_id)
    if d is None:
        raise HTTPException(404, "not found")
    services.revoke_device(db, user, d)
    return {"schema_version": 1, "lifecycle": d.lifecycle}


@app.post("/api/v1/admin/me/password")
def admin_change_password(body: PasswordChangeIn, db: Session = Depends(get_db), user=Depends(operator)):
    services.change_admin_password(db, user, body.current_password, body.new_password)
    return {"schema_version": 1, "ok": True}


@app.get("/api/v1/admin/usage")
def admin_usage(db: Session = Depends(get_db), user=Depends(operator)):
    return services.drive_usage(db)


@app.get("/api/v1/admin/vault")
def admin_vault(db: Session = Depends(get_db), user=Depends(operator)):
    return services.vault_overview(db)


@app.put("/api/v1/admin/devices/{device_id}/secret")
def admin_put_secret(device_id: str, body: SecretIn, db: Session = Depends(get_db), user=Depends(operator)):
    d = db.get(Device, device_id)
    if d is None:
        raise HTTPException(404, "not found")
    return {"schema_version": 1, **services.store_device_secret(db, d, kind=body.kind, secret=body.secret, actor_type="operator", actor_id=user.id, source="operator")}


@app.post("/api/v1/admin/devices/{device_id}/secret/reveal")
def admin_reveal_secret(device_id: str, body: RevealIn, response: Response, db: Session = Depends(get_db), user=Depends(operator)):
    d = db.get(Device, device_id)
    if d is None:
        raise HTTPException(404, "not found")
    out = services.reveal_device_secret(db, user, d, body.kind, body.password)
    response.headers["Cache-Control"] = "no-store"
    return out


@app.post("/api/v1/admin/devices/{device_id}/archive")
def admin_device_archive(device_id: str, db: Session = Depends(get_db), user=Depends(operator)):
    d = db.get(Device, device_id)
    if d is None:
        raise HTTPException(404, "not found")
    services.archive_device(db, user, d)
    return {"schema_version": 1, "lifecycle": d.lifecycle}


@app.post("/api/v1/admin/devices/{device_id}/unarchive")
def admin_device_unarchive(device_id: str, db: Session = Depends(get_db), user=Depends(operator)):
    d = db.get(Device, device_id)
    if d is None:
        raise HTTPException(404, "not found")
    services.unarchive_device(db, user, d)
    return {"schema_version": 1, "lifecycle": d.lifecycle}


@app.patch("/api/v1/admin/devices/{device_id}")
def admin_device_patch(device_id: str, body: DevicePatchIn, db: Session = Depends(get_db), user=Depends(operator)):
    d = db.get(Device, device_id)
    if d is None:
        raise HTTPException(404, "not found")
    services.patch_device(db, user, d, body.model_dump(exclude_unset=True))
    return {"schema_version": 1, "device": _device(d)}


@app.post("/api/v1/admin/devices/{device_id}/pause")
def admin_device_pause(device_id: str, db: Session = Depends(get_db), user=Depends(operator)):
    d = db.get(Device, device_id)
    if d is None:
        raise HTTPException(404, "not found")
    services.patch_device(db, user, d, {"pause_new_backups": True})
    return {"schema_version": 1, "device": _device(d)}


@app.post("/api/v1/admin/devices/{device_id}/resume")
def admin_device_resume(device_id: str, db: Session = Depends(get_db), user=Depends(operator)):
    d = db.get(Device, device_id)
    if d is None:
        raise HTTPException(404, "not found")
    services.patch_device(db, user, d, {"pause_new_backups": False})
    return {"schema_version": 1, "device": _device(d)}


@app.post("/api/v1/admin/devices/{device_id}/selection")
def admin_device_selection(device_id: str, body: SelectionIn, db: Session = Depends(get_db), user=Depends(operator)):
    from app.selection import SelectionError, compile_selection

    d = db.get(Device, device_id)
    if d is None:
        raise HTTPException(404, "not found")
    try:
        manifest = compile_selection(selected=body.selected, office_files=body.office_files)
        if body.preferred_start_hhmm:
            services.patch_device(db, user, d, {"preferred_start_hhmm": body.preferred_start_hhmm})
            services.bind_setup_policy(db, user, d, source_roots=manifest["source_roots"], preferred_hhmm=body.preferred_start_hhmm)
        rec = services.save_device_selection(db, user, d, manifest, applied_locally=body.applied_locally)
    except (SelectionError, ValueError) as exc:
        raise HTTPException(422, str(exc)) from exc
    return {
        "schema_version": 1,
        "version": rec.version,
        "manifest": rec.manifest_json,
        "applied_locally": rec.applied_locally,
        "note": "Qualified agent applies source_roots from the endpoint pilot.json. Re-run Setup Wizard on the PC to apply a new selection locally.",
    }


@app.get("/api/v1/setup/catalog")
def setup_catalog():
    """Public and non-secret: what a generic installer's setup wizard needs
    before anyone signs in -- the organisation name, each site's name and
    backup hours, and the department list. Nothing about devices."""
    from app.schedule_policy import DEPARTMENTS, SITE_SCHEDULE

    keys = ("display_name", "timezone", "eligibility_start", "preferred_latest_start", "window_end", "hard_stop")
    return {
        "schema_version": 1,
        "org_name": settings.org_name,
        "sites": [{"id": sid, **{k: cfg[k] for k in keys if k in cfg}} for sid, cfg in SITE_SCHEDULE.items()],
        "departments": list(DEPARTMENTS),
    }


@app.get("/api/v1/admin/catalog")
def admin_catalog(user=Depends(operator)):
    from app.errors_ux import catalog
    from app.schedule_policy import DEPARTMENTS, SITE_SCHEDULE
    from app.version import agent_pin

    return {
        "schema_version": 1,
        "sites": [{"id": k, **v} for k, v in SITE_SCHEDULE.items()],
        "departments": DEPARTMENTS,
        "qualified_agent_sha": agent_pin()[0],
        "errors": catalog()["items"],
        "forbidden_admin_actions": ["forget", "prune", "delete_repository", "repair", "unlock", "shell", "rclone"],
    }


@app.post("/api/v1/admin/estimate")
def admin_estimate(body: EstimateIn, user=Depends(operator)):
    from app.schedule_policy import ScheduleError, first_backup_fit

    try:
        return {"schema_version": 1, **first_backup_fit(site_id=body.site_id, logical_bytes=body.logical_bytes, preferred_hhmm=body.preferred_start_hhmm, measured_upload_mbps=body.measured_upload_mbps)}
    except (ScheduleError, ValueError) as exc:
        raise HTTPException(422, str(exc)) from exc


@app.get("/api/v1/admin/errors")
def admin_errors(user=Depends(operator)):
    from app.errors_ux import catalog

    return catalog()


@app.get("/api/v1/admin/policies")
def policies(db: Session = Depends(get_db), user=Depends(operator)):
    pols = db.scalars(select(Policy)).all()
    items = []
    for p in pols:
        revs = db.scalars(select(PolicyRevision).where(PolicyRevision.policy_id == p.id).order_by(PolicyRevision.version)).all()
        items.append({"id": p.id, "name": p.name, "description": p.description, "revisions": [{"id": r.id, "version": r.version, "content_hash": r.content_hash, "config": r.config_json} for r in revs]})
    return {"schema_version": 1, "items": items}


@app.post("/api/v1/admin/policies")
def policy_create(body: PolicyIn, db: Session = Depends(get_db), user=Depends(operator)):
    from app.security import default_policy_config

    cfg = body.config or default_policy_config()
    validate_policy_config(cfg)
    p = Policy(id=new_id(), name=body.name, description=body.description)
    db.add(p)
    db.flush()
    db.add(PolicyRevision(id=new_id(), policy_id=p.id, version=1, content_hash=payload_hash(cfg), config_json=cfg))
    services.audit(db, actor_type="operator", actor_id=user.id, action="policy_create", resource_type="policy", resource_id=p.id)
    db.commit()
    return {"schema_version": 1, "id": p.id}


@app.post("/api/v1/admin/devices/{device_id}/policy")
def policy_assign(device_id: str, body: PolicyAssignIn, db: Session = Depends(get_db), user=Depends(operator)):
    d = db.get(Device, device_id)
    if d is None:
        raise HTTPException(404, "not found")
    services.assign_policy(db, user, d, body.revision_id)
    return {"schema_version": 1, "device_id": d.id, "policy_revision_id": d.assigned_policy_revision_id}


@app.get("/api/v1/admin/commands/{command_id}")
def command_get(command_id: str, db: Session = Depends(get_db), user=Depends(operator)):
    cmd = db.get(Command, command_id)
    if cmd is None:
        raise HTTPException(404, "not found")
    return {"schema_version": 1, "id": cmd.id, "kind": cmd.kind, "state": cmd.state, "payload": cmd.payload, "result": cmd.result_json, "device_id": cmd.device_id}


@app.post("/api/v1/admin/devices/{device_id}/browse")
def browse(device_id: str, body: BrowseIn, db: Session = Depends(get_db), user=Depends(operator)):
    d = db.get(Device, device_id)
    if d is None:
        raise HTTPException(404, "not found")
    if ".." in body.prefix or ":" in body.prefix or "\x00" in body.prefix:
        raise HTTPException(422, "prefix rejected")
    repo = db.scalar(select(Repository).where(Repository.device_id == d.id, Repository.role == "PRIMARY"))
    if repo is None:
        raise HTTPException(422, "no repository")
    snap = db.scalar(select(Snapshot).where(Snapshot.repository_id == repo.id, Snapshot.engine_snapshot_id == body.snapshot_id.lower()))
    if snap is None:
        raise HTTPException(422, "snapshot not bound to device")
    cmd = services.enqueue_command(db, user, d, "BROWSE_SNAPSHOT", {"snapshot_id": body.snapshot_id.lower(), "prefix": body.prefix})
    return {
        "schema_version": 1,
        "command_id": cmd.id,
        "kind": cmd.kind,
        "deduplicated": bool(getattr(cmd, "_deduplicated", False)),
    }


@app.post("/api/v1/admin/devices/{device_id}/browse-local-dir")
def browse_local_dir(device_id: str, body: BrowseLocalDirIn, db: Session = Depends(get_db), user=Depends(operator)):
    d = db.get(Device, device_id)
    if d is None:
        raise HTTPException(404, "not found")
    raw = (body.path or "").strip()
    if not raw or "\x00" in raw or ".." in raw.replace("\\", "/").split("/"):
        raise HTTPException(422, "path rejected")
    limit = max(1, min(body.limit or 200, 1000))
    cmd = services.enqueue_command(db, user, d, "BROWSE_LOCAL_DIR", {"path": raw, "cursor": body.cursor or "", "limit": limit})
    return {
        "schema_version": 1,
        "command_id": cmd.id,
        "kind": cmd.kind,
        "deduplicated": bool(getattr(cmd, "_deduplicated", False)),
    }


def _browse_cache_args(scope: str, snapshot_id: str) -> tuple[str, str]:
    normalized_scope = (scope or "").lower()
    if normalized_scope not in {"local", "snapshot"}:
        raise HTTPException(422, "scope must be local or snapshot")
    normalized_snapshot = (snapshot_id or "").lower()
    if normalized_scope == "snapshot":
        if len(normalized_snapshot) != 64 or any(ch not in "0123456789abcdef" for ch in normalized_snapshot):
            raise HTTPException(422, "snapshot id")
    else:
        normalized_snapshot = ""
    return normalized_scope, normalized_snapshot


@app.get("/api/v1/admin/devices/{device_id}/browse-cache/search")
def browse_cache_search(
    device_id: str,
    scope: str,
    q: str = "",
    snapshot_id: str = "",
    db: Session = Depends(get_db),
    user=Depends(operator),
):
    d = db.get(Device, device_id)
    if d is None:
        raise HTTPException(404, "not found")
    normalized_scope, normalized_snapshot = _browse_cache_args(scope, snapshot_id)
    return services.browse_cache_search(
        db,
        d,
        scope=normalized_scope,
        snapshot_id=normalized_snapshot,
        query=(q or "").strip(),
    )


@app.get("/api/v1/admin/devices/{device_id}/browse-cache")
def browse_cache_get(
    device_id: str,
    scope: str,
    path: str = "",
    snapshot_id: str = "",
    db: Session = Depends(get_db),
    user=Depends(operator),
):
    d = db.get(Device, device_id)
    if d is None:
        raise HTTPException(404, "not found")
    normalized_scope, normalized_snapshot = _browse_cache_args(scope, snapshot_id)
    raw_path = (path or "").strip()
    if normalized_scope == "local" and not raw_path:
        raise HTTPException(422, "path required")
    if "\x00" in raw_path or ".." in raw_path.replace("\\", "/").split("/"):
        raise HTTPException(422, "path rejected")
    return services.browse_cache_get(
        db,
        d,
        scope=normalized_scope,
        snapshot_id=normalized_snapshot,
        path=raw_path,
    )


@app.post("/api/v1/admin/devices/{device_id}/apply-selection")
def apply_selection(device_id: str, body: ApplySelectionIn, db: Session = Depends(get_db), user=Depends(operator)):
    from app.discovery import is_sensitive

    d = db.get(Device, device_id)
    if d is None:
        raise HTTPException(404, "not found")
    roots = [r.strip() for r in (body.source_roots or []) if (r or "").strip()]
    if not roots:
        raise HTTPException(422, "at least one source root required")
    consents = {c.strip() for c in (body.sensitive_consents or [])}
    for root in roots:
        if "\x00" in root or ".." in root.replace("\\", "/").split("/"):
            raise HTTPException(422, f"path rejected: {root}")
        if is_sensitive(Path(root)) and root not in consents:
            raise HTTPException(422, f"sensitive path requires explicit consent: {root}")
    revision_id = new_id()
    cmd = services.enqueue_command(
        db,
        user,
        d,
        "APPLY_SELECTION",
        {"revision_id": revision_id, "source_roots": roots, "sensitive_consents": sorted(consents)},
    )
    return {"schema_version": 1, "command_id": cmd.id, "kind": cmd.kind, "revision_id": revision_id}


@app.post("/api/v1/admin/restore-requests")
def restore(body: RestoreIn, db: Session = Depends(get_db), user=Depends(operator)):
    d = db.get(Device, body.device_id)
    if d is None:
        raise HTTPException(404, "not found")
    req = services.create_restore(db, user, d, body.snapshot_id, body.selections)
    return {"schema_version": 1, "id": req.id, "state": req.state, "destination_mode": "STAGING"}


@app.get("/api/v1/admin/restore-requests")
def restore_list(db: Session = Depends(get_db), user=Depends(operator)):
    rows = db.execute(
        select(RestoreRequest, Device)
        .join(Device, Device.id == RestoreRequest.device_id)
        .where(Device.lifecycle == "ACTIVE")
        .order_by(RestoreRequest.created_at.desc())
        .limit(100)
    ).all()
    return {
        "schema_version": 1,
        "items": [
            {
                "id": r.id,
                "device_id": r.device_id,
                "hostname": d.hostname,
                "display_name": d.display_name or "",
                "snapshot_id": r.snapshot_id,
                "state": r.state,
                "destination_mode": r.destination_mode,
            }
            for r, d in rows
        ],
    }


@app.get("/api/v1/admin/backups")
def backups(db: Session = Depends(get_db), user=Depends(operator)):
    rows = db.execute(
        select(BackupAttempt, Device)
        .join(BackupJob, BackupJob.id == BackupAttempt.job_id)
        .join(Device, Device.id == BackupJob.device_id)
        .where(Device.lifecycle == "ACTIVE")
        .order_by(BackupAttempt.created_at.desc())
        .limit(200)
    ).all()
    return {
        "schema_version": 1,
        "items": [
            {
                **_attempt(a),
                "device_id": d.id,
                "hostname": d.hostname,
                "display_name": d.display_name or "",
                "site_id": d.site_id,
                "department": d.department,
            }
            for a, d in rows
        ],
    }


@app.get("/api/v1/admin/audit-events")
def audit_events(db: Session = Depends(get_db), user=Depends(operator)):
    rows = db.scalars(select(AuditEvent).order_by(AuditEvent.occurred_at.desc()).limit(200)).all()
    target = {e.id: _audit_device_id(e) for e in rows}
    wanted = {t for t in target.values() if t}
    names = {d.id: d for d in db.scalars(select(Device).where(Device.id.in_(wanted)))} if wanted else {}
    return {
        "schema_version": 1,
        "items": [
            {
                "id": e.id,
                "actor_type": e.actor_type,
                "actor_id": e.actor_id,
                "action": e.action,
                "resource_type": e.resource_type,
                "resource_id": e.resource_id,
                "device_id": target[e.id],
                "hostname": names[target[e.id]].hostname if target[e.id] in names else "",
                "display_name": (
                    str((e.safe_diff or {}).get("display_name") or "")
                    if e.action == "device_rename"
                    else (names[target[e.id]].display_name or "" if target[e.id] in names else "")
                ),
                "detail": _audit_detail(e),
                "result": e.result,
                "occurred_at": e.occurred_at.isoformat(),
            }
            for e in rows
        ],
    }


def _audit_device_id(e: AuditEvent) -> str:
    """The computer an event is about: the resource itself, or the one a command was aimed at."""
    if e.resource_type == "device":
        return e.resource_id
    return str((e.safe_diff or {}).get("device_id") or "")


def _audit_detail(e: AuditEvent) -> str:
    """Only the command kind is surfaced; the rest of safe_diff stays server-side."""
    if e.action == "command_create":
        return str((e.safe_diff or {}).get("kind") or "")
    if e.action == "device_rename":
        return str((e.safe_diff or {}).get("display_name") or "")
    if e.action == "user_message":
        return str((e.safe_diff or {}).get("message") or "")
    if e.action == "self_service_restore":
        return str((e.safe_diff or {}).get("destination") or "")
    return ""


@app.get("/api/v1/admin/storage-profiles")
def storage_profiles(db: Session = Depends(get_db), user=Depends(operator)):
    rows = db.scalars(select(StorageProfile)).all()
    return {"schema_version": 1, "items": [{"id": s.id, "kind": s.kind, "label": s.label, "status": s.status, "nonsecret_config": s.nonsecret_config} for s in rows]}


def public_gateway_base() -> str:
    """Endpoint data-plane URL. Production must be rest:https:// with public TLS."""
    gw = (settings.gateway_public_base or "").strip()
    if settings.allow_insecure_http or settings.lab_mode:
        return gw
    if not gw.startswith("rest:https://"):
        raise HTTPException(503, "gateway_public_base must be rest:https://")
    return gw


@app.post("/api/v1/agent/enrollments")
def enroll(body: EnrollIn, db: Session = Depends(get_db)):
    gw = public_gateway_base()
    return services.enroll_agent(
        db,
        token=body.token,
        hostname=body.hostname,
        agent_version=body.agent_version,
        installation_id=body.installation_id,
        capabilities=body.capabilities,
        gateway_base=gw,
    )


@app.post("/api/v1/agent/enrollments/abort")
def enroll_abort(db: Session = Depends(get_db), pair=Depends(agent_pair)):
    device, _ = pair
    d = services.abort_enrollment(db, device)
    return {"schema_version": 1, "lifecycle": d.lifecycle, "device_id": d.id}


@app.post("/api/v1/agent/heartbeat")
def hb(body: HeartbeatIn, db: Session = Depends(get_db), pair=Depends(agent_pair)):
    device, inst = pair
    return services.heartbeat(db, device, inst, body.model_dump())


@app.get("/api/v1/agent/binary")
def agent_binary(pair=Depends(agent_pair)):
    path = services.agent_binary_for_download()
    return FileResponse(path, media_type="application/octet-stream", filename="stowline-agent.exe")


@app.get("/api/v1/agent/policy")
def agent_policy(db: Session = Depends(get_db), pair=Depends(agent_pair)):
    device, _ = pair
    rev = db.get(PolicyRevision, device.assigned_policy_revision_id) if device.assigned_policy_revision_id else None
    if rev is None:
        raise HTTPException(404, "no policy")
    from app import admission as wan

    overlay = wan.device_policy_overlay(db, device, rev.config_json)
    return {"schema_version": 1, "revision_id": rev.id, "config": overlay}


@app.post("/api/v1/agent/work/claim")
def claim(db: Session = Depends(get_db), pair=Depends(agent_pair)):
    device, inst = pair
    env = services.claim_command(db, device, inst)
    return {"schema_version": 1, "command": env}


@app.post("/api/v1/agent/jobs/events")
def events(body: AttemptIn, db: Session = Depends(get_db), pair=Depends(agent_pair)):
    device, _ = pair
    return services.ingest_attempt(db, device, body.model_dump())


@app.post("/api/v1/agent/commands/{command_id}/ack")
def ack(command_id: str, body: CommandAckIn, db: Session = Depends(get_db), pair=Depends(agent_pair)):
    device, inst = pair
    return services.ack_command(db, device, inst, command_id, body.model_dump())


@app.post("/api/v1/agent/snapshot-catalogs")
def agent_create_catalog(body: CatalogCreateIn, db: Session = Depends(get_db), pair=Depends(agent_pair)):
    device, inst = pair
    if ".." in body.snapshot_id or len(body.snapshot_id) != 64:
        raise HTTPException(422, "snapshot id")
    repo = db.scalar(select(Repository).where(Repository.device_id == device.id, Repository.role == "PRIMARY"))
    if repo is None:
        raise HTTPException(422, "no repository")
    snap = db.scalar(select(Snapshot).where(Snapshot.repository_id == repo.id, Snapshot.engine_snapshot_id == body.snapshot_id.lower()))
    if snap is None:
        raise HTTPException(422, "snapshot not bound to device")
    cat = services.create_snapshot_catalog(db, device, inst.id, snap, body.model_dump())
    return {"schema_version": 1, "catalog_id": cat.id, "state": cat.state}


@app.post("/api/v1/agent/snapshot-catalogs/{catalog_id}/entries")
def agent_catalog_entries(catalog_id: str, body: CatalogEntriesIn, db: Session = Depends(get_db), pair=Depends(agent_pair)):
    device, _ = pair
    cat = db.get(SnapshotCatalog, catalog_id)
    if cat is None or cat.device_id != device.id:
        raise HTTPException(404, "not found")
    services.append_catalog_entries(db, cat, [e.model_dump() for e in body.entries])
    return {"schema_version": 1, "state": cat.state, "received": len(body.entries)}


@app.post("/api/v1/agent/snapshot-catalogs/{catalog_id}/finalize")
def agent_catalog_finalize(catalog_id: str, body: CatalogFinalizeIn, db: Session = Depends(get_db), pair=Depends(agent_pair)):
    device, _ = pair
    cat = db.get(SnapshotCatalog, catalog_id)
    if cat is None or cat.device_id != device.id:
        raise HTTPException(404, "not found")
    result = services.finalize_snapshot_catalog(db, cat, body.model_dump())
    if not result["ok"]:
        raise HTTPException(409, "totals or checksum mismatch")
    return {"schema_version": 1, **result}


@app.post("/api/v1/agent/snapshot-catalogs/{catalog_id}/fail")
def agent_catalog_fail(catalog_id: str, body: CatalogFailIn, db: Session = Depends(get_db), pair=Depends(agent_pair)):
    device, _ = pair
    cat = db.get(SnapshotCatalog, catalog_id)
    if cat is None or cat.device_id != device.id:
        raise HTTPException(404, "not found")
    services.fail_snapshot_catalog(db, cat, body.error_class)
    return {"schema_version": 1, "state": cat.state}


@app.get("/api/v1/admin/devices/{device_id}/snapshot-catalog")
def admin_snapshot_catalog(device_id: str, snapshot_id: str, path: str = "", cursor: str = "", limit: int = 200, db: Session = Depends(get_db), user=Depends(operator)):
    d = db.get(Device, device_id)
    if d is None:
        raise HTTPException(404, "not found")
    return services.read_snapshot_catalog(db, d, snapshot_id, path, cursor, max(1, min(limit, 500)))


@app.get("/api/v1/admin/devices/{device_id}/snapshot-catalog/search")
def admin_snapshot_catalog_search(device_id: str, snapshot_id: str, q: str = "", limit: int = 200, db: Session = Depends(get_db), user=Depends(operator)):
    d = db.get(Device, device_id)
    if d is None:
        raise HTTPException(404, "not found")
    return services.search_snapshot_catalog(db, d, snapshot_id, q, max(1, min(limit, 200)))


@app.get("/api/v1/admin/devices/{device_id}/files")
def admin_device_files(device_id: str, q: str = "", limit: int = 200, db: Session = Depends(get_db), user=Depends(operator)):
    d = db.get(Device, device_id)
    if d is None:
        raise HTTPException(404, "not found")
    return services.search_device_files(db, d, q, max(1, min(limit, 200)))


@app.post("/api/v1/admin/devices/{device_id}/backfill-catalogs")
def admin_backfill_catalogs(device_id: str, db: Session = Depends(get_db), user=Depends(operator)):
    d = db.get(Device, device_id)
    if d is None:
        raise HTTPException(404, "not found")
    cmds = services.backfill_missing_catalogs(db, user, d)
    return {"schema_version": 1, "command_ids": [c.id for c in cmds]}


@app.get("/api/v1/admin/sites")
def admin_sites(db: Session = Depends(get_db), user=Depends(operator)):
    from app.db import Site
    from app import admission as wan

    return {"schema_version": 1, "items": [wan.site_status(db, s) for s in db.scalars(select(Site)).all()]}


@app.patch("/api/v1/admin/sites/{site_id}")
def admin_site_patch(site_id: str, body: SitePatchIn, db: Session = Depends(get_db), user=Depends(operator)):
    from app.db import Site
    from app import admission as wan
    from app.bandwidth import BandwidthError, compile_site

    site = db.get(Site, site_id)
    if site is None:
        raise HTTPException(404, "site not found")
    data = body.model_dump(exclude_unset=True)
    if "measured_upload_mbps" in data:
        mbps = data["measured_upload_mbps"]
        if mbps is not None:
            import math

            if mbps <= 0 or math.isnan(mbps) or math.isinf(mbps) or mbps > 10_000:
                raise HTTPException(422, "measured_upload_mbps invalid")
        site.measured_upload_mbps = mbps
        if mbps is None:
            site.max_site_backup_bps = None
    if "business_backup_budget_percent" in data and data["business_backup_budget_percent"] is not None:
        site.business_backup_budget_percent = data["business_backup_budget_percent"]
    if "max_concurrent_wan_backups" in data and data["max_concurrent_wan_backups"] is not None:
        site.max_concurrent_wan_backups = data["max_concurrent_wan_backups"]
    if "restore_download_limit_kibps" in data and data["restore_download_limit_kibps"] is not None:
        site.restore_download_limit_kibps = data["restore_download_limit_kibps"]
    if "pause_new_wan_admissions" in data and data["pause_new_wan_admissions"] is not None:
        site.pause_new_wan_admissions = data["pause_new_wan_admissions"]
    if "allow_site_seeds" in data and data["allow_site_seeds"] is not None:
        site.allow_site_seeds = data["allow_site_seeds"]
    if "user_impact_flag" in data and data["user_impact_flag"] is not None:
        site.user_impact_flag = data["user_impact_flag"]
    try:
        compiled = compile_site(site.measured_upload_mbps, site.business_backup_budget_percent, 1)
        if compiled["ok"]:
            site.max_site_backup_bps = compiled["max_site_backup_bps"]
            services.notify_gateway_site_limit(site.id, compiled["gateway_bytes_per_sec"])
        else:
            site.max_site_backup_bps = None
    except BandwidthError as exc:
        raise HTTPException(422, str(exc)) from exc
    services.audit(db, actor_type="operator", actor_id=user.id, action="site_patch", resource_type="site", resource_id=site.id, extra=data)
    db.commit()
    return {"schema_version": 1, "site": wan.site_status(db, site)}


@app.post("/api/v1/admin/sites/{site_id}/pause")
def admin_site_pause(site_id: str, db: Session = Depends(get_db), user=Depends(operator)):
    from app.db import Site
    from app import admission as wan

    site = db.get(Site, site_id)
    if site is None:
        raise HTTPException(404, "site not found")
    site.pause_new_wan_admissions = True
    services.audit(db, actor_type="operator", actor_id=user.id, action="wan_pause", resource_type="site", resource_id=site.id)
    db.commit()
    return {"schema_version": 1, "site": wan.site_status(db, site)}


@app.post("/api/v1/admin/sites/{site_id}/resume")
def admin_site_resume(site_id: str, db: Session = Depends(get_db), user=Depends(operator)):
    from app.db import Site
    from app import admission as wan

    site = db.get(Site, site_id)
    if site is None:
        raise HTTPException(404, "site not found")
    site.pause_new_wan_admissions = False
    services.audit(db, actor_type="operator", actor_id=user.id, action="wan_resume", resource_type="site", resource_id=site.id)
    db.commit()
    return {"schema_version": 1, "site": wan.site_status(db, site)}


@app.post("/api/v1/admin/sites/{site_id}/emergency-restore")
def admin_emergency_restore(site_id: str, body: EmergencyRestoreIn, db: Session = Depends(get_db), user=Depends(operator)):
    from datetime import timedelta
    from app.db import Site
    from app import admission as wan
    from app.security import utcnow

    site = db.get(Site, site_id)
    if site is None:
        raise HTTPException(404, "site not found")
    site.emergency_restore_until = utcnow() + timedelta(hours=body.hours)
    site.emergency_restore_kibps = body.restore_download_kibps
    if body.pause_new_backups:
        site.pause_new_wan_admissions = True
    services.audit(
        db,
        actor_type="operator",
        actor_id=user.id,
        action="emergency_restore_profile",
        resource_type="site",
        resource_id=site.id,
        extra={"hours": body.hours, "kibps": body.restore_download_kibps},
    )
    db.commit()
    return {"schema_version": 1, "site": wan.site_status(db, site)}


@app.post("/api/v1/agent/wan/leases/acquire")
def agent_lease_acquire(body: LeaseAcquireIn, db: Session = Depends(get_db), pair=Depends(agent_pair)):
    from app import admission as wan

    device, inst = pair
    payload = body.model_dump(by_alias=True)
    if "class_" in payload:
        payload["class"] = payload.pop("class_")
    return wan.acquire(db, device, inst.id, payload)


@app.post("/api/v1/agent/wan/leases/{lease_id}/renew")
def agent_lease_renew(lease_id: str, body: LeaseRenewIn | None = None, db: Session = Depends(get_db), pair=Depends(agent_pair)):
    from app import admission as wan

    device, _ = pair
    return wan.renew(db, device, lease_id, body.model_dump(exclude_none=True) if body else {})


@app.put("/api/v1/agent/escrow")
def agent_put_escrow(body: SecretIn, db: Session = Depends(get_db), pair=Depends(agent_pair)):
    device, _ = pair
    status = services.store_device_secret(db, device, kind=body.kind, secret=body.secret, actor_type="device", actor_id=device.id, source="device")
    return {"schema_version": 1, "ok": True, **status}


@app.get("/api/v1/agent/escrow")
def agent_get_escrow(kind: str = "restic-password", db: Session = Depends(get_db), pair=Depends(agent_pair)):
    device, _ = pair
    return {"schema_version": 1, **services.device_secret_status(db, device, kind)}


@app.post("/api/v1/agent/self-service-selection")
def agent_self_service_selection(body: SelfServiceSelectionIn, db: Session = Depends(get_db), pair=Depends(agent_pair)):
    device, _ = pair
    if not body.source_roots:
        raise HTTPException(422, "at least one source root required")
    rec, recorded = services.report_self_service_selection(db, device, body.source_roots, source=body.source)
    return {"schema_version": 1, "ok": True, "recorded": recorded, "version": rec.version if rec else 0}


@app.post("/api/v1/agent/self-service-backup")
def agent_self_service_backup(db: Session = Depends(get_db), pair=Depends(agent_pair)):
    device, _ = pair
    cmd = services.request_self_service_backup(db, device)
    return {"schema_version": 1, "queued": True, "command_id": cmd.id, "deduplicated": bool(getattr(cmd, "_deduplicated", False))}


@app.post("/api/v1/agent/user-messages")
def agent_user_message(body: UserMessageIn, db: Session = Depends(get_db), pair=Depends(agent_pair)):
    device, _ = pair
    services.report_user_message(db, device, body.message)
    return {"schema_version": 1, "ok": True}


@app.post("/api/v1/agent/self-service-restores")
def agent_self_service_restore(body: SelfServiceRestoreIn, db: Session = Depends(get_db), pair=Depends(agent_pair)):
    device, _ = pair
    services.report_self_service_restore(db, device, body.snapshot_id, body.selections, body.destination)
    return {"schema_version": 1, "ok": True}


@app.post("/api/v1/agent/wan/leases/{lease_id}/release")
def agent_lease_release(lease_id: str, body: LeaseReleaseIn, db: Session = Depends(get_db), pair=Depends(agent_pair)):
    from app import admission as wan

    device, _ = pair
    return wan.release(db, device, lease_id, body.reason)


def _device(d: Device) -> dict:
    return {
        "id": d.id,
        "hostname": d.hostname,
        "display_name": d.display_name or "",
        "department": d.department,
        "site_id": d.site_id,
        "lifecycle": d.lifecycle,
        "health": d.health,
        "agent_version": d.agent_version,
        "agent_sha": d.agent_sha,
        "service_state": d.service_state,
        "pause_new_backups": d.pause_new_backups,
        "preferred_start_hhmm": d.preferred_start_hhmm,
        "canary_state": d.canary_state,
        "last_restore_state": d.last_restore_state,
        "last_seen_at": d.last_seen_at.isoformat() if d.last_seen_at else None,
        "last_success_at": d.last_success_at.isoformat() if d.last_success_at else None,
        "last_success_snapshot": d.last_success_snapshot,
        "last_success_attempt_ended_at": d.last_success_attempt_ended_at.isoformat() if d.last_success_attempt_ended_at else None,
        "last_attempt_outcome": d.last_attempt_outcome,
        "policy_revision_id": d.assigned_policy_revision_id,
    }


def _attempt(a: BackupAttempt) -> dict:
    return {
        "id": a.id,
        "job_id": a.job_id,
        "outcome": a.outcome,
        "phase": a.phase,
        "snapshot_id": a.snapshot_id,
        "error_class": a.error_class,
        "error_title": explain_error(a.error_class)["title"],
        "consistency": a.consistency,
        "published_not_green": a.published_not_green,
        "started_at": a.started_at.isoformat() if a.started_at else None,
        "ended_at": a.ended_at.isoformat() if a.ended_at else None,
        "summary": a.summary_json or {},
    }


static_dir = Path(__file__).resolve().parent / "static"
if static_dir.exists():
    app.mount("/ui", StaticFiles(directory=static_dir, html=True), name="ui")


@app.get("/")
def root():
    index = static_dir / "index.html"
    if index.exists():
        return FileResponse(index)
    return {"product": "stowline", "ui": "/ui/", "api": "/api/v1"}


def run():
    import os
    import uvicorn

    port = int(os.environ.get("PORT") or os.environ.get("STOWLINE_CONTROL_PORT") or "8080")
    # Behind a local reverse proxy set STOWLINE_CONTROL_HOST=127.0.0.1: forwarded
    # headers are trusted, so the port must never be reachable except via the proxy.
    host = os.environ.get("STOWLINE_CONTROL_HOST", "0.0.0.0").strip() or "0.0.0.0"
    uvicorn.run(
        "app.main:app",
        host=host,
        port=port,
        reload=False,
        proxy_headers=True,
        forwarded_allow_ips="*",
    )
