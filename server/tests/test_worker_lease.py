"""Regression: the worker lease takeover must be an atomic compare-and-swap.

Two worker processes can each independently read the same expired
`WorkerLease` row before either writes. A plain read-modify-write (the
original implementation) lets both believe they now hold the lease — a
classic lost update. This matters once the control plane runs more than
one worker replica (e.g. multiple instances behind a load balancer).
"""

from __future__ import annotations

import os
import sys
from datetime import timedelta
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "server"))

DB_PATH = Path("var/test-worker-lease.db")
os.environ.setdefault("STOWLINE_DATABASE_URL", "sqlite+pysqlite:///" + str(DB_PATH.resolve()).replace("\\", "/"))
os.environ.setdefault("STOWLINE_LAB_MODE", "true")
os.environ.setdefault("STOWLINE_ADMIN_PASSWORD", "test-admin-pass")

Path("var").mkdir(exist_ok=True)
if DB_PATH.exists():
    DB_PATH.unlink()

from app.db import Base, WorkerLease, make_engine, session_factory, utcnow  # noqa: E402
from app.security import as_utc  # noqa: E402
from app.worker import _claim  # noqa: E402


def _fresh_session():
    """A brand-new engine/session against the same on-disk DB file — this is
    what two separate worker processes actually look like, unlike sharing one
    in-process Session (which would serialize through SQLAlchemy's identity
    map and hide the race)."""
    engine = make_engine(os.environ["STOWLINE_DATABASE_URL"])
    return session_factory(engine)()


def setup_module(module):
    engine = make_engine(os.environ["STOWLINE_DATABASE_URL"])
    Base.metadata.create_all(engine)


def test_claim_fresh_lease_succeeds():
    db = _fresh_session()
    try:
        assert _claim(db, "lease-fresh") is True
    finally:
        db.close()


def test_claim_held_lease_is_denied_to_second_owner():
    db_a = _fresh_session()
    db_b = _fresh_session()
    try:
        assert _claim(db_a, "lease-held") is True
        assert _claim(db_b, "lease-held") is False
    finally:
        db_a.close()
        db_b.close()


def test_claim_expired_lease_is_takeable():
    db_a = _fresh_session()
    try:
        assert _claim(db_a, "lease-expire") is True
        row = db_a.get(WorkerLease, "lease-expire")
        row.until = as_utc(utcnow()) - timedelta(seconds=1)
        db_a.commit()
    finally:
        db_a.close()

    db_b = _fresh_session()
    try:
        assert _claim(db_b, "lease-expire") is True
    finally:
        db_b.close()


def test_concurrent_takeover_of_an_expired_lease_has_exactly_one_winner():
    """The core regression. Simulates two workers that both independently
    read the same expired lease row (two separate sessions/engines against
    the same DB file, like two OS processes), then both attempt to take it
    over. Exactly one must win."""
    name = "lease-race"
    seed = _fresh_session()
    try:
        seed.add(WorkerLease(id=name, owner="stale", until=utcnow() - timedelta(seconds=5), payload={}))
        seed.commit()
    finally:
        seed.close()

    db_a = _fresh_session()
    db_b = _fresh_session()
    try:
        # Both read the row as expired before either writes — this is exactly
        # what the buggy code did implicitly via db.get() inside _claim().
        row_a = db_a.get(WorkerLease, name)
        row_b = db_b.get(WorkerLease, name)
        assert as_utc(row_a.until) < utcnow()
        assert as_utc(row_b.until) < utcnow()

        results = [_claim(db_a, name), _claim(db_b, name)]
        assert sorted(results) == [False, True], (
            f"expected exactly one winner, got {results} — both workers believe "
            "they hold the lease (lost update)"
        )
    finally:
        db_a.close()
        db_b.close()
