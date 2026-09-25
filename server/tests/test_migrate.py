"""Regression: backup_attempts.snapshot_id / backup_snapshots.engine_snapshot_id /
restore_requests.snapshot_id were originally sized for a 36-char UUID. The
SQLAlchemy models (app/db.py) were widened to String(64) for restic's full
64-char hex snapshot IDs, but nothing ever widened the already-created
production columns -- confirmed live: a real agent reporting a real backup
attempt hit "value too long for type character varying(36)" on
backup_attempts.snapshot_id. backup_attempts.id and the matching
backup_snapshots.reported_attempt_id hit the same 36-char ceiling from a
different direction: qualification tooling has been observed writing
descriptive (non-UUID) attempt ids, one of which was 37 characters.

_ensure_varchar_width's decision logic is tested directly against fakes
(rather than a real engine) because the fix issues Postgres-only
`ALTER COLUMN ... TYPE` DDL, which SQLite -- this repo's test database --
does not support at all.
"""

from __future__ import annotations

import os
from pathlib import Path

os.environ.setdefault("STOWLINE_DATABASE_URL", "sqlite+pysqlite:///" + str(Path("var/test-migrate.db").resolve()).replace("\\", "/"))

from app.migrate import _ensure_varchar_width  # noqa: E402


class _FakeType:
    def __init__(self, length):
        self.length = length


class _FakeInspector:
    def __init__(self, tables: dict[str, dict[str, int | None]]):
        self._tables = tables

    def get_table_names(self):
        return list(self._tables)

    def get_columns(self, table):
        return [{"name": name, "type": _FakeType(length)} for name, length in self._tables[table].items()]


class _FakeConn:
    def __init__(self):
        self.executed: list[str] = []

    def execute(self, stmt):
        self.executed.append(str(stmt))


def test_widens_a_column_narrower_than_the_model_declares():
    insp = _FakeInspector({"backup_attempts": {"snapshot_id": 36}})
    conn = _FakeConn()
    _ensure_varchar_width(conn, insp, "backup_attempts", "snapshot_id", 64)
    assert len(conn.executed) == 1
    assert "ALTER TABLE backup_attempts ALTER COLUMN snapshot_id TYPE VARCHAR(64)" in conn.executed[0]


def test_does_not_touch_a_column_already_wide_enough():
    """Idempotent: running the migration again on an already-fixed database
    must not re-issue the ALTER (and must never narrow anything back)."""
    insp = _FakeInspector({"backup_attempts": {"snapshot_id": 64}})
    conn = _FakeConn()
    _ensure_varchar_width(conn, insp, "backup_attempts", "snapshot_id", 64)
    assert conn.executed == []


def test_does_not_touch_a_column_already_wider_than_requested():
    insp = _FakeInspector({"backup_attempts": {"snapshot_id": 128}})
    conn = _FakeConn()
    _ensure_varchar_width(conn, insp, "backup_attempts", "snapshot_id", 64)
    assert conn.executed == []


def test_skips_a_table_that_does_not_exist_yet():
    """A brand-new database gets the right width straight from
    Base.metadata.create_all() -- this helper must not fail before that
    table exists."""
    insp = _FakeInspector({})
    conn = _FakeConn()
    _ensure_varchar_width(conn, insp, "backup_attempts", "snapshot_id", 64)
    assert conn.executed == []


def test_skips_a_column_that_does_not_exist():
    insp = _FakeInspector({"backup_attempts": {"other_column": 36}})
    conn = _FakeConn()
    _ensure_varchar_width(conn, insp, "backup_attempts", "snapshot_id", 64)
    assert conn.executed == []


def test_upgrade_widens_all_five_columns_on_a_real_database():
    """End-to-end against this repo's real test database (SQLite): a fresh
    upgrade() must leave every widened column able to hold a full 64-char
    value. SQLite has no fixed VARCHAR length enforcement, so this proves
    upgrade() runs the width-check step without erroring, not the
    Postgres-specific ALTER itself (covered above against fakes)."""
    from sqlalchemy import inspect

    from app.migrate import upgrade

    db_path = Path("var/test-migrate.db")
    db_path.parent.mkdir(parents=True, exist_ok=True)
    if db_path.exists():
        db_path.unlink()
    url = "sqlite+pysqlite:///" + str(db_path.resolve()).replace("\\", "/")

    upgrade(url)  # fresh create_all
    upgrade(url)  # must be safe to run again (idempotent, mirrors every real startup)

    import sqlalchemy as sa

    engine = sa.create_engine(url)
    insp = inspect(engine)
    for table, column in (
        ("backup_attempts", "snapshot_id"),
        ("backup_attempts", "id"),
        ("backup_snapshots", "engine_snapshot_id"),
        ("backup_snapshots", "reported_attempt_id"),
        ("restore_requests", "snapshot_id"),
    ):
        assert table in insp.get_table_names()
        cols = {c["name"] for c in insp.get_columns(table)}
        assert column in cols


def test_upgrade_repairs_old_delivery_timestamp_semantics(tmp_path):
    """An already-stuck production row must become redeliverable on the
    first startup with the new schema; terminal rows retain their real ACK."""
    import sqlalchemy as sa

    from app.migrate import upgrade

    db_path = tmp_path / "old-command-schema.db"
    url = "sqlite+pysqlite:///" + str(db_path.resolve()).replace("\\", "/")
    engine = sa.create_engine(url)
    old_delivery = "2026-09-17 12:00:00"
    with engine.begin() as conn:
        conn.execute(
            sa.text(
                """
                CREATE TABLE commands (
                    id VARCHAR(36) PRIMARY KEY,
                    device_id VARCHAR(36) NOT NULL,
                    installation_id VARCHAR(36) NOT NULL DEFAULT '',
                    kind VARCHAR(64) NOT NULL,
                    payload JSON NOT NULL DEFAULT '{}',
                    payload_hash VARCHAR(64) NOT NULL,
                    state VARCHAR(32) NOT NULL,
                    job_id VARCHAR(36) NOT NULL DEFAULT '',
                    expires_at TIMESTAMP NOT NULL,
                    acked_at TIMESTAMP,
                    result_json JSON NOT NULL DEFAULT '{}',
                    created_at TIMESTAMP NOT NULL
                )
                """
            )
        )
        for command_id, state in (("old-delivered", "DELIVERED"), ("old-succeeded", "SUCCEEDED")):
            conn.execute(
                sa.text(
                    """
                    INSERT INTO commands
                        (id, device_id, installation_id, kind, payload, payload_hash, state, job_id,
                         expires_at, acked_at, result_json, created_at)
                    VALUES
                        (:id, 'dev', 'inst', 'RUN_CANARY', '{}', 'hash', :state, 'job',
                         '2026-09-18 12:00:00', :acked_at, '{}', '2026-09-17 11:59:00')
                    """
                ),
                {"id": command_id, "state": state, "acked_at": old_delivery},
            )

    upgrade(url)
    upgrade(url)  # data repair and schema changes must both be idempotent

    with engine.connect() as conn:
        delivered = conn.execute(
            sa.text("SELECT delivered_at, acked_at, delivery_attempts FROM commands WHERE id='old-delivered'")
        ).one()
        succeeded = conn.execute(
            sa.text("SELECT delivered_at, acked_at, delivery_attempts FROM commands WHERE id='old-succeeded'")
        ).one()

    assert delivered.delivered_at is not None
    assert delivered.acked_at is None
    assert delivered.delivery_attempts == 1
    assert succeeded.delivered_at is None
    assert succeeded.acked_at is not None
    assert succeeded.delivery_attempts == 0
