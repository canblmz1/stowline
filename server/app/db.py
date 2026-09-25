from __future__ import annotations

from datetime import datetime, timezone

from sqlalchemy import (
    Boolean,
    DateTime,
    ForeignKey,
    Index,
    BigInteger,
    Integer,
    PrimaryKeyConstraint,
    String,
    Text,
    UniqueConstraint,
    create_engine,
    event,
    JSON,
    Float,
)
from sqlalchemy.orm import DeclarativeBase, Mapped, mapped_column, sessionmaker, Session


def utcnow() -> datetime:
    return datetime.now(timezone.utc)


class Base(DeclarativeBase):
    pass


class User(Base):
    __tablename__ = "users"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    username: Mapped[str] = mapped_column(String(64), unique=True)
    password_hash: Mapped[str] = mapped_column(String(255))
    display_name: Mapped[str] = mapped_column(String(128), default="Operator")
    role: Mapped[str] = mapped_column(String(32), default="ADMIN")
    disabled_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)


class AdminSession(Base):
    __tablename__ = "admin_sessions"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    user_id: Mapped[str] = mapped_column(ForeignKey("users.id"), index=True)
    token_hash: Mapped[str] = mapped_column(String(128), unique=True)
    expires_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), index=True)
    revoked_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    # Set for a session opened with a setup code: it may only enroll
    # computers and name/configure the ones it enrolled (see
    # services.installer_may_touch), never use the rest of the admin API.
    setup_code_id: Mapped[str] = mapped_column(String(36), default="")


class SetupCode(Base):
    """A code an admin hands to whoever installs the computers, so the
    installer never needs the admin password. Stored hashed; shown once."""

    __tablename__ = "setup_codes"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    code_hash: Mapped[str] = mapped_column(String(128), unique=True)
    label: Mapped[str] = mapped_column(String(128), default="")
    # "" = any site; otherwise computers can only be enrolled into this one
    site_id: Mapped[str] = mapped_column(String(64), default="")
    created_by: Mapped[str] = mapped_column(String(36), default="")
    expires_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), index=True)
    max_uses: Mapped[int] = mapped_column(Integer, default=25)
    uses: Mapped[int] = mapped_column(Integer, default=0)
    revoked_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)


class EnrollmentToken(Base):
    __tablename__ = "enrollment_tokens"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    token_hash: Mapped[str] = mapped_column(String(128), unique=True)
    label: Mapped[str] = mapped_column(String(128), default="")
    site_id: Mapped[str] = mapped_column(String(64), default="")
    department: Mapped[str] = mapped_column(String(64), default="")
    created_by: Mapped[str] = mapped_column(String(36), default="")
    expires_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), index=True)
    consumed_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    # the admin session that minted it ("" for older tokens)
    session_id: Mapped[str] = mapped_column(String(36), default="")


class Site(Base):
    __tablename__ = "sites"
    id: Mapped[str] = mapped_column(String(64), primary_key=True)
    display_name: Mapped[str] = mapped_column(String(128), default="")
    measured_upload_mbps: Mapped[float | None] = mapped_column(Float, nullable=True)
    business_backup_budget_percent: Mapped[int] = mapped_column(Integer, default=20)
    max_site_backup_bps: Mapped[int | None] = mapped_column(Integer, nullable=True)
    max_concurrent_wan_backups: Mapped[int] = mapped_column(Integer, default=1)
    restore_download_limit_kibps: Mapped[int] = mapped_column(Integer, default=0)
    pause_new_wan_admissions: Mapped[bool] = mapped_column(Boolean, default=False)
    allow_site_seeds: Mapped[bool] = mapped_column(Boolean, default=False)
    provider_unavailable_until: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    emergency_restore_until: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    emergency_restore_kibps: Mapped[int] = mapped_column(Integer, default=0)
    user_impact_flag: Mapped[str] = mapped_column(String(64), default="")
    ingress_bytes: Mapped[int] = mapped_column(BigInteger, default=0)
    ingress_updated_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    updated_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow, onupdate=utcnow)


class WanAdmissionLease(Base):
    __tablename__ = "wan_admission_leases"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    site_id: Mapped[str] = mapped_column(String(64), index=True)
    device_id: Mapped[str] = mapped_column(ForeignKey("devices.id"), index=True)
    installation_id: Mapped[str] = mapped_column(String(36), default="")
    job_id: Mapped[str] = mapped_column(String(64), default="")
    attempt_id: Mapped[str] = mapped_column(String(64), unique=True)
    wan_class: Mapped[str] = mapped_column(String(32), default="NORMAL_BACKUP")
    slot: Mapped[int] = mapped_column(Integer, default=1)
    hold_key: Mapped[str] = mapped_column(String(128), unique=True)
    device_hold_key: Mapped[str] = mapped_column(String(128), unique=True)
    acquired_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    expires_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), index=True)
    last_renewed_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    released_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    release_reason: Mapped[str] = mapped_column(String(64), default="")
    bandwidth_kibps: Mapped[int] = mapped_column(Integer, default=0)
    restore_download_kibps: Mapped[int] = mapped_column(Integer, default=0)
    progress_percent: Mapped[float | None] = mapped_column(Float, nullable=True)
    progress_files_done: Mapped[int | None] = mapped_column(BigInteger, nullable=True)
    progress_total_files: Mapped[int | None] = mapped_column(BigInteger, nullable=True)
    progress_bytes_done: Mapped[int | None] = mapped_column(BigInteger, nullable=True)
    progress_total_bytes: Mapped[int | None] = mapped_column(BigInteger, nullable=True)
    progress_updated_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)


class Device(Base):
    __tablename__ = "devices"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    hostname: Mapped[str] = mapped_column(String(256), default="", index=True)
    # Operator-owned label used only by the control-panel UI.  The real
    # hostname remains immutable evidence and continues to own the repository
    # namespace, so renaming a tile can never move or orphan backups.
    display_name: Mapped[str] = mapped_column(String(64), default="")
    department: Mapped[str] = mapped_column(String(64), default="")
    site_id: Mapped[str] = mapped_column(String(64), default="", index=True)
    owner_label: Mapped[str] = mapped_column(String(128), default="")
    lifecycle: Mapped[str] = mapped_column(String(32), default="ACTIVE", index=True)
    active_installation_id: Mapped[str | None] = mapped_column(String(36), nullable=True)
    assigned_policy_revision_id: Mapped[str | None] = mapped_column(String(36), nullable=True)
    agent_version: Mapped[str] = mapped_column(String(64), default="")
    last_seen_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    last_success_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    last_success_snapshot: Mapped[str] = mapped_column(String(64), default="")
    last_success_attempt_ended_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    last_attempt_outcome: Mapped[str] = mapped_column(String(32), default="")
    health: Mapped[str] = mapped_column(String(32), default="NEVER_BACKED_UP", index=True)
    agent_sha: Mapped[str] = mapped_column(String(64), default="")
    service_state: Mapped[str] = mapped_column(String(32), default="")
    pause_new_backups: Mapped[bool] = mapped_column(Boolean, default=False)
    preferred_start_hhmm: Mapped[str] = mapped_column(String(5), default="")
    canary_state: Mapped[str] = mapped_column(String(32), default="")
    last_restore_state: Mapped[str] = mapped_column(String(32), default="")
    # the enrollment token this device was created with ("" for older devices)
    enrollment_token_id: Mapped[str] = mapped_column(String(36), default="")
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    updated_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow, onupdate=utcnow)


class DeviceSelection(Base):
    __tablename__ = "device_selections"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    device_id: Mapped[str] = mapped_column(ForeignKey("devices.id"), index=True)
    version: Mapped[int] = mapped_column(Integer)
    manifest_json: Mapped[dict] = mapped_column(JSON, default=dict)
    selected_bytes: Mapped[int] = mapped_column(BigInteger, default=0)
    selected_files: Mapped[int] = mapped_column(BigInteger, default=0)
    applied_locally: Mapped[bool] = mapped_column(Boolean, default=False)
    created_by: Mapped[str] = mapped_column(String(36), default="")
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    __table_args__ = (UniqueConstraint("device_id", "version"),)


class Installation(Base):
    __tablename__ = "agent_installations"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    device_id: Mapped[str] = mapped_column(ForeignKey("devices.id"), index=True)
    lifecycle: Mapped[str] = mapped_column(String(32), default="ACTIVE")
    agent_version: Mapped[str] = mapped_column(String(64), default="")
    capabilities: Mapped[dict] = mapped_column(JSON, default=dict)
    last_seen_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)


class DeviceCredential(Base):
    __tablename__ = "device_credentials"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    installation_id: Mapped[str] = mapped_column(ForeignKey("agent_installations.id"), index=True)
    purpose: Mapped[str] = mapped_column(String(32))  # control | gateway
    secret_hash: Mapped[str] = mapped_column(String(255), index=True)
    revoked_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)


class Policy(Base):
    __tablename__ = "policies"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    name: Mapped[str] = mapped_column(String(128), unique=True)
    description: Mapped[str] = mapped_column(Text, default="")
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)


class PolicyRevision(Base):
    __tablename__ = "policy_revisions"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    policy_id: Mapped[str] = mapped_column(ForeignKey("policies.id"), index=True)
    version: Mapped[int] = mapped_column(Integer)
    schema_version: Mapped[int] = mapped_column(Integer, default=1)
    content_hash: Mapped[str] = mapped_column(String(64))
    config_json: Mapped[dict] = mapped_column(JSON)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    __table_args__ = (UniqueConstraint("policy_id", "version"),)


class PolicyAssignment(Base):
    __tablename__ = "policy_assignments"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    device_id: Mapped[str] = mapped_column(ForeignKey("devices.id"), index=True)
    policy_revision_id: Mapped[str] = mapped_column(ForeignKey("policy_revisions.id"))
    assigned_by: Mapped[str] = mapped_column(String(36), default="")
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)


class StorageProfile(Base):
    __tablename__ = "storage_profiles"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    kind: Mapped[str] = mapped_column(String(32))
    label: Mapped[str] = mapped_column(String(128))
    status: Mapped[str] = mapped_column(String(32), default="IMPLEMENTED")  # IMPLEMENTED|CONFIGURED|QUALIFIED
    nonsecret_config: Mapped[dict] = mapped_column(JSON, default=dict)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)


class Repository(Base):
    __tablename__ = "repositories"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    device_id: Mapped[str] = mapped_column(ForeignKey("devices.id"), index=True)
    storage_profile_id: Mapped[str] = mapped_column(ForeignKey("storage_profiles.id"))
    generation_id: Mapped[str] = mapped_column(String(64), index=True)
    role: Mapped[str] = mapped_column(String(32), default="PRIMARY")
    location: Mapped[str] = mapped_column(String(1024))
    engine_repo_id: Mapped[str] = mapped_column(String(64), default="")
    state: Mapped[str] = mapped_column(String(32), default="ACTIVE")
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    __table_args__ = (UniqueConstraint("device_id", "generation_id", "role"),)


class BackupJob(Base):
    __tablename__ = "backup_jobs"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    device_id: Mapped[str] = mapped_column(ForeignKey("devices.id"), index=True)
    kind: Mapped[str] = mapped_column(String(32), default="BACKUP")
    slot_key: Mapped[str] = mapped_column(String(256), default="", index=True)
    state: Mapped[str] = mapped_column(String(32), default="QUEUED")
    policy_revision_id: Mapped[str] = mapped_column(String(36), default="")
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    updated_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)


class BackupAttempt(Base):
    __tablename__ = "backup_attempts"
    id: Mapped[str] = mapped_column(String(64), primary_key=True)
    job_id: Mapped[str] = mapped_column(ForeignKey("backup_jobs.id"), index=True)
    attempt_no: Mapped[int] = mapped_column(Integer, default=1)
    phase: Mapped[str] = mapped_column(String(32), default="QUEUED")
    outcome: Mapped[str] = mapped_column(String(32), default="")
    snapshot_id: Mapped[str] = mapped_column(String(64), default="")
    error_class: Mapped[str] = mapped_column(String(64), default="")
    consistency: Mapped[str] = mapped_column(String(32), default="")
    published_not_green: Mapped[bool] = mapped_column(Boolean, default=False)
    summary_json: Mapped[dict] = mapped_column(JSON, default=dict)
    started_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    ended_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)


class Snapshot(Base):
    __tablename__ = "backup_snapshots"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    repository_id: Mapped[str] = mapped_column(ForeignKey("repositories.id"), index=True)
    engine_snapshot_id: Mapped[str] = mapped_column(String(64), index=True)
    observation_state: Mapped[str] = mapped_column(String(32), default="REPORTED")
    completeness: Mapped[str] = mapped_column(String(32), default="UNKNOWN")
    consistency: Mapped[str] = mapped_column(String(32), default="UNKNOWN")
    reported_attempt_id: Mapped[str] = mapped_column(String(64), default="")
    source_time: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    __table_args__ = (UniqueConstraint("repository_id", "engine_snapshot_id"),)


class SnapshotCatalog(Base):
    """A complete, permanent, agent-built listing of one restic snapshot's
    files. Built once (see docs/superpowers/specs/2026-09-22-snapshot-catalog-design.md);
    exposed to the panel only once state == READY, verified by finalize().
    Deleting the Snapshot row this belongs to cascades here (ondelete="CASCADE")
    -- a catalog can never outlive the snapshot it describes."""

    __tablename__ = "snapshot_catalogs"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    snapshot_id: Mapped[str] = mapped_column(ForeignKey("backup_snapshots.id", ondelete="CASCADE"), index=True)
    device_id: Mapped[str] = mapped_column(ForeignKey("devices.id"), index=True)
    installation_id: Mapped[str] = mapped_column(String(36), default="")
    repository_id: Mapped[str] = mapped_column(ForeignKey("repositories.id"), index=True)
    state: Mapped[str] = mapped_column(String(16), default="PENDING", index=True)  # PENDING | UPLOADING | READY | FAILED
    declared_file_count: Mapped[int] = mapped_column(BigInteger, default=0)
    declared_directory_count: Mapped[int] = mapped_column(Integer, default=0)
    declared_logical_bytes: Mapped[int] = mapped_column(BigInteger, default=0)
    declared_checksum: Mapped[str] = mapped_column(String(64), default="")
    actual_file_count: Mapped[int] = mapped_column(BigInteger, default=0)
    actual_directory_count: Mapped[int] = mapped_column(Integer, default=0)
    generated_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    completed_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    error: Mapped[str] = mapped_column(String(64), default="")
    __table_args__ = (UniqueConstraint("snapshot_id"),)


class SnapshotCatalogEntry(Base):
    """One file/directory node from a snapshot's catalog. Only what restic's
    own `ls` reports -- name/path/type/size/mtime -- never file contents,
    never credentials. Upserted by (catalog_id, path_hash): a retried batch
    after an agent crash or network blip can never create a duplicate or a
    half-applied row."""

    __tablename__ = "snapshot_catalog_entries"
    catalog_id: Mapped[str] = mapped_column(ForeignKey("snapshot_catalogs.id", ondelete="CASCADE"))
    path_hash: Mapped[str] = mapped_column(String(64))
    parent_path: Mapped[str] = mapped_column(String(1024), default="")
    path: Mapped[str] = mapped_column(String(1024), default="")
    name: Mapped[str] = mapped_column(String(512), default="")
    normalized_name: Mapped[str] = mapped_column(String(512), default="")
    type: Mapped[str] = mapped_column(String(16), default="file")
    size: Mapped[int | None] = mapped_column(BigInteger, nullable=True)
    mtime: Mapped[str] = mapped_column(String(64), default="")
    __table_args__ = (
        PrimaryKeyConstraint("catalog_id", "path_hash"),
        Index("ix_snapshot_catalog_entries_search", "catalog_id", "normalized_name"),
    )


class RestoreRequest(Base):
    __tablename__ = "restore_requests"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    device_id: Mapped[str] = mapped_column(ForeignKey("devices.id"), index=True)
    snapshot_id: Mapped[str] = mapped_column(String(64))
    selections_json: Mapped[list] = mapped_column(JSON, default=list)
    destination_mode: Mapped[str] = mapped_column(String(32), default="STAGING")
    state: Mapped[str] = mapped_column(String(32), default="QUEUED")
    result_json: Mapped[dict] = mapped_column(JSON, default=dict)
    requested_by: Mapped[str] = mapped_column(String(64), default="")
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)


class RestoreAttempt(Base):
    __tablename__ = "restore_attempts"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    restore_request_id: Mapped[str] = mapped_column(ForeignKey("restore_requests.id"), index=True)
    command_id: Mapped[str] = mapped_column(String(36), default="")
    state: Mapped[str] = mapped_column(String(32), default="QUEUED")
    result_json: Mapped[dict] = mapped_column(JSON, default=dict)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)


class AgentEvent(Base):
    __tablename__ = "agent_events"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    device_id: Mapped[str] = mapped_column(ForeignKey("devices.id"), index=True)
    kind: Mapped[str] = mapped_column(String(64), default="")
    payload: Mapped[dict] = mapped_column(JSON, default=dict)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)


class Command(Base):
    __tablename__ = "commands"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    device_id: Mapped[str] = mapped_column(ForeignKey("devices.id"), index=True)
    installation_id: Mapped[str] = mapped_column(String(36), default="")
    kind: Mapped[str] = mapped_column(String(64))
    payload: Mapped[dict] = mapped_column(JSON, default=dict)
    payload_hash: Mapped[str] = mapped_column(String(64))
    state: Mapped[str] = mapped_column(String(32), default="QUEUED", index=True)
    job_id: Mapped[str] = mapped_column(String(36), default="")
    expires_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), index=True)
    # Set only on a terminal SUCCEEDED/FAILED/CANCELLED ack -- never at
    # delivery. claim_command's redelivery check depends on this staying
    # NULL for a command that was delivered but never terminally acked.
    acked_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    # Set each time claim_command hands this command to the device (first
    # delivery or a redelivery of a stale unacked one). NULL means never
    # delivered (still QUEUED).
    delivered_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    delivery_attempts: Mapped[int] = mapped_column(Integer, default=0)
    result_json: Mapped[dict] = mapped_column(JSON, default=dict)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)


class BrowseCache(Base):
    """Sanitized directory listings returned by browse commands.

    Only names/paths and file metadata already bounded by the agent are kept;
    file contents and credentials never enter this table.  Snapshot paths are
    immutable.  Local paths are a remembered catalogue that changes only when
    an operator explicitly scans that folder again.
    """

    __tablename__ = "browse_cache"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    device_id: Mapped[str] = mapped_column(ForeignKey("devices.id"), index=True)
    installation_id: Mapped[str] = mapped_column(String(36), default="", index=True)
    scope: Mapped[str] = mapped_column(String(16), index=True)  # local | snapshot
    snapshot_id: Mapped[str] = mapped_column(String(64), default="")
    path: Mapped[str] = mapped_column(String(1024), default="")
    path_key: Mapped[str] = mapped_column(String(64), default="")
    result_json: Mapped[dict] = mapped_column(JSON, default=dict)
    scanned_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    updated_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow, onupdate=utcnow)
    __table_args__ = (
        UniqueConstraint("device_id", "installation_id", "scope", "snapshot_id", "path_key"),
    )


class Heartbeat(Base):
    __tablename__ = "agent_heartbeats"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    device_id: Mapped[str] = mapped_column(ForeignKey("devices.id"), index=True)
    installation_id: Mapped[str] = mapped_column(String(36), default="")
    sequence: Mapped[int] = mapped_column(Integer, default=0)
    agent_version: Mapped[str] = mapped_column(String(64), default="")
    current_operation: Mapped[str] = mapped_column(String(64), default="none")
    policy_revision_id: Mapped[str] = mapped_column(String(36), default="")
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    __table_args__ = (UniqueConstraint("device_id", "installation_id", "sequence"),)


class AuditEvent(Base):
    __tablename__ = "audit_events"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    actor_type: Mapped[str] = mapped_column(String(32))
    actor_id: Mapped[str] = mapped_column(String(64), default="")
    action: Mapped[str] = mapped_column(String(64), index=True)
    resource_type: Mapped[str] = mapped_column(String(64), default="")
    resource_id: Mapped[str] = mapped_column(String(64), default="", index=True)
    result: Mapped[str] = mapped_column(String(32), default="OK")
    correlation_id: Mapped[str] = mapped_column(String(64), default="")
    safe_diff: Mapped[dict] = mapped_column(JSON, default=dict)
    occurred_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow, index=True)


class DeviceSecret(Base):
    """Sealed escrow copy of a device secret (see app/vault.py). Only ciphertext
    and a fingerprint are stored; the sealing key lives outside the database."""

    __tablename__ = "device_secrets"
    __table_args__ = (UniqueConstraint("device_id", "kind", name="uq_device_secret_kind"),)
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    device_id: Mapped[str] = mapped_column(ForeignKey("devices.id"), index=True)
    kind: Mapped[str] = mapped_column(String(32))
    ciphertext: Mapped[str] = mapped_column(Text)
    fingerprint: Mapped[str] = mapped_column(String(16), default="")
    source: Mapped[str] = mapped_column(String(16), default="")
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    updated_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    last_revealed_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    reveal_count: Mapped[int] = mapped_column(Integer, default=0)


class IdempotencyRecord(Base):
    __tablename__ = "idempotency_records"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    actor_scope: Mapped[str] = mapped_column(String(128))
    key: Mapped[str] = mapped_column(String(128))
    request_hash: Mapped[str] = mapped_column(String(64))
    status_code: Mapped[int] = mapped_column(Integer, default=200)
    response_json: Mapped[dict] = mapped_column(JSON, default=dict)
    expires_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)
    __table_args__ = (UniqueConstraint("actor_scope", "key"),)


class WorkerLease(Base):
    __tablename__ = "worker_leases"
    id: Mapped[str] = mapped_column(String(64), primary_key=True)
    owner: Mapped[str] = mapped_column(String(64), default="")
    until: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    payload: Mapped[dict] = mapped_column(JSON, default=dict)


class RetentionPlan(Base):
    __tablename__ = "retention_plans"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    repository_id: Mapped[str] = mapped_column(String(36), index=True)
    keep_daily: Mapped[int] = mapped_column(Integer, default=7)
    keep_weekly: Mapped[int] = mapped_column(Integer, default=5)
    keep_monthly: Mapped[int] = mapped_column(Integer, default=12)
    dry_run_json: Mapped[dict] = mapped_column(JSON, default=dict)
    executed_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)


class RecoveryCheck(Base):
    __tablename__ = "recovery_checks"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    repository_id: Mapped[str] = mapped_column(String(36), default="")
    kind: Mapped[str] = mapped_column(String(32), default="COPY")
    outcome: Mapped[str] = mapped_column(String(32), default="")
    evidence: Mapped[dict] = mapped_column(JSON, default=dict)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), default=utcnow)


def make_engine(url: str):
    connect_args = {}
    if url.startswith("sqlite"):
        connect_args["check_same_thread"] = False
        connect_args["timeout"] = 30
    engine = create_engine(url, future=True, connect_args=connect_args)
    if url.startswith("sqlite"):

        @event.listens_for(engine, "connect")
        def _fk(dbapi_conn, _):
            dbapi_conn.execute("PRAGMA foreign_keys=ON")

    return engine


def session_factory(engine) -> sessionmaker[Session]:
    return sessionmaker(engine, expire_on_commit=False, class_=Session)
