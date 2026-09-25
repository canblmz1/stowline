"""Idempotent schema bootstrap for the control plane.

Alembic-compatible upgrade path: empty database -> current models.
SQLite tests use SQLAlchemy create_all; Compose Postgres uses this on server startup.
Existing DBs get additive columns (site_id) without dropping data.
"""

from sqlalchemy import inspect, text

from app.db import Base, make_engine


def _ensure_column(conn, insp, table: str, column: str, ddl: str) -> None:
    if table not in insp.get_table_names():
        return
    cols = {c["name"] for c in insp.get_columns(table)}
    if column in cols:
        return
    conn.execute(text(f"ALTER TABLE {table} ADD COLUMN {column} {ddl}"))


def _ensure_varchar_width(conn, insp, table: str, column: str, min_length: int) -> None:
    """Widens a VARCHAR column that was created narrower than the current
    model declares. Never narrows or drops data -- Postgres widens
    character varying(n) in place (metadata-only, no table rewrite)."""
    if table not in insp.get_table_names():
        return
    cols = {c["name"]: c["type"] for c in insp.get_columns(table)}
    col_type = cols.get(column)
    if col_type is None:
        return
    current_length = getattr(col_type, "length", None)
    if current_length is not None and current_length >= min_length:
        return
    conn.execute(text(f"ALTER TABLE {table} ALTER COLUMN {column} TYPE VARCHAR({min_length})"))


# Byte and file counts outgrow a 32-bit INTEGER (max ~2.1 GB). Confirmed
# live on a server whose database was created fresh from models that still
# said Integer: a 2 GB+ backup's lease renewal failed with "integer out of
# range", the lease expired, and the panel showed the busy PC as OFFLINE.
BIGINT_COLUMNS = [
    ("sites", "ingress_bytes"),
    ("wan_admission_leases", "progress_files_done"),
    ("wan_admission_leases", "progress_total_files"),
    ("wan_admission_leases", "progress_bytes_done"),
    ("wan_admission_leases", "progress_total_bytes"),
    ("device_selections", "selected_bytes"),
    ("device_selections", "selected_files"),
    ("snapshot_catalogs", "declared_file_count"),
    ("snapshot_catalogs", "declared_logical_bytes"),
    ("snapshot_catalogs", "actual_file_count"),
    ("snapshot_catalog_entries", "size"),
]


def _ensure_bigint(conn, insp, table: str, column: str) -> None:
    """Widens a 32-bit INTEGER column to BIGINT in place (Postgres only;
    SQLite integers are already 64-bit). Never narrows."""
    if conn.dialect.name != "postgresql" or table not in insp.get_table_names():
        return
    cols = {c["name"]: c["type"] for c in insp.get_columns(table)}
    col_type = cols.get(column)
    if col_type is None or type(col_type).__name__.upper() != "INTEGER":
        return
    conn.execute(text(f"ALTER TABLE {table} ALTER COLUMN {column} TYPE BIGINT"))


def upgrade(url: str) -> None:
    engine = make_engine(url)
    Base.metadata.create_all(engine)
    insp = inspect(engine)
    with engine.begin() as conn:
        _ensure_column(conn, insp, "devices", "site_id", "VARCHAR(64) DEFAULT ''")
        _ensure_column(conn, insp, "devices", "display_name", "VARCHAR(64) DEFAULT ''")
        _ensure_column(conn, insp, "devices", "agent_sha", "VARCHAR(64) DEFAULT ''")
        _ensure_column(conn, insp, "devices", "service_state", "VARCHAR(32) DEFAULT ''")
        _ensure_column(conn, insp, "devices", "pause_new_backups", "BOOLEAN DEFAULT 0")
        _ensure_column(conn, insp, "devices", "preferred_start_hhmm", "VARCHAR(5) DEFAULT ''")
        _ensure_column(conn, insp, "devices", "canary_state", "VARCHAR(32) DEFAULT ''")
        _ensure_column(conn, insp, "devices", "last_restore_state", "VARCHAR(32) DEFAULT ''")
        _ensure_column(conn, insp, "enrollment_tokens", "site_id", "VARCHAR(64) DEFAULT ''")
        _ensure_column(conn, insp, "enrollment_tokens", "department", "VARCHAR(64) DEFAULT ''")
        # Client-reported, authoritative completion time of the attempt that
        # currently owns device.last_success_at/last_success_snapshot -- lets
        # ingest_attempt refuse to move that state backward when a delayed
        # outbox report (e.g. from a stuck retry queue) arrives after a
        # chronologically newer attempt has already been recorded.
        _ensure_column(conn, insp, "devices", "last_success_attempt_ended_at", "TIMESTAMP")
        # backup_attempts.snapshot_id / backup_snapshots.engine_snapshot_id /
        # restore_requests.snapshot_id were originally sized for a 36-char
        # UUID, before restic's full 64-char hex snapshot IDs were wired
        # in -- app/db.py's models were widened to String(64), but nothing
        # ever widened the already-created production columns. Confirmed
        # live: a real agent reporting a real backup attempt hit
        # "value too long for type character varying(36)" on
        # backup_attempts.snapshot_id.
        _ensure_varchar_width(conn, insp, "backup_attempts", "snapshot_id", 64)
        _ensure_varchar_width(conn, insp, "backup_snapshots", "engine_snapshot_id", 64)
        _ensure_varchar_width(conn, insp, "restore_requests", "snapshot_id", 64)
        # backup_attempts.id is usually a UUID (36 chars) but qualification
        # tooling has been observed writing descriptive ids instead (e.g.
        # "qual-att-qual-success-20260909T185426", 37 chars) -- confirmed
        # live: this exact payload has been failing "value too long for
        # type character varying(36)" on retry since 2026-09-09, spamming
        # /api/v1/agent/jobs/events with 500s. reported_attempt_id carries
        # the same value onto backup_snapshots, so it widens alongside it.
        _ensure_varchar_width(conn, insp, "backup_attempts", "id", 64)
        _ensure_varchar_width(conn, insp, "backup_snapshots", "reported_attempt_id", 64)
        # Command.acked_at used to be stamped at delivery time, so it never
        # actually meant "terminally acked" -- claim_command's redelivery
        # logic needs a real delivery timestamp separate from that, and a
        # count for operator visibility into repeated redeliveries.
        _ensure_column(conn, insp, "commands", "delivered_at", "TIMESTAMP")
        _ensure_column(conn, insp, "commands", "delivery_attempts", "INTEGER DEFAULT 0")
        _ensure_column(conn, insp, "wan_admission_leases", "progress_percent", "DOUBLE PRECISION")
        _ensure_column(conn, insp, "wan_admission_leases", "progress_files_done", "BIGINT")
        _ensure_column(conn, insp, "wan_admission_leases", "progress_total_files", "BIGINT")
        _ensure_column(conn, insp, "wan_admission_leases", "progress_bytes_done", "BIGINT")
        _ensure_column(conn, insp, "wan_admission_leases", "progress_total_bytes", "BIGINT")
        _ensure_column(conn, insp, "wan_admission_leases", "progress_updated_at", "TIMESTAMP")
        _ensure_column(conn, insp, "admin_sessions", "setup_code_id", "VARCHAR(36) DEFAULT ''")
        _ensure_column(conn, insp, "enrollment_tokens", "session_id", "VARCHAR(36) DEFAULT ''")
        _ensure_column(conn, insp, "devices", "enrollment_token_id", "VARCHAR(36) DEFAULT ''")
        for table, column in BIGINT_COLUMNS:
            _ensure_bigint(conn, insp, table, column)
        # Repair rows written under the old meaning of acked_at. Every old
        # DELIVERED row was stamped at claim time even though no terminal ACK
        # had arrived; move that timestamp to delivered_at and clear acked_at
        # so the new lease/redelivery path can recover already-stuck live
        # commands. The delivered_at IS NULL guard makes this data migration
        # safe on every subsequent startup.
        conn.execute(
            text(
                """
                UPDATE commands
                SET delivered_at = acked_at,
                    acked_at = NULL,
                    delivery_attempts = CASE
                        WHEN delivery_attempts IS NULL OR delivery_attempts < 1 THEN 1
                        ELSE delivery_attempts
                    END
                WHERE state = 'DELIVERED'
                  AND delivered_at IS NULL
                  AND acked_at IS NOT NULL
                """
            )
        )
