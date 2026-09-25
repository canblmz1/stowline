from __future__ import annotations

import os
import time
import uuid
from datetime import timedelta

from sqlalchemy import select, update
from sqlalchemy.exc import IntegrityError

from app.config import settings
from app.db import Site, WorkerLease, make_engine, session_factory, utcnow
from app.security import as_utc
from app.services import expire_commands, refresh_health


def _claim(db, name: str, seconds: int = 12) -> bool:
    now = utcnow()
    owner = f"{os.getpid()}-{uuid.uuid4().hex[:8]}"
    until = now + timedelta(seconds=seconds)
    row = db.get(WorkerLease, name)
    if row is None:
        try:
            db.add(WorkerLease(id=name, owner=owner, until=until, payload={}))
            db.commit()
            return True
        except IntegrityError:
            db.rollback()
            return False
    if as_utc(row.until) is not None and as_utc(row.until) > now:
        return False
    # Two workers can both read this row as expired before either writes.
    # A plain read-modify-write would let both believe they hold the lease
    # (lost update). The WHERE clause is re-checked by the database against
    # the live row at UPDATE time, so only the first writer's statement can
    # match once the other has already renewed `until` into the future.
    res = db.execute(
        update(WorkerLease)
        .where(WorkerLease.id == name, WorkerLease.until <= now)
        .values(owner=owner, until=until)
        .execution_options(synchronize_session=False)
    )
    db.commit()
    return bool(res.rowcount)


SITE_LIMIT_PUSH_SECONDS = 60
_last_site_limit_push = 0.0


def push_site_limits(db) -> int:
    """Re-sends every site's upload cap to the gateway. The gateway keeps
    them only in memory and the control plane used to send one only when a
    site was edited -- confirmed live: after a gateway restart it held no
    site limits at all, so nothing capped a site's total upload."""
    from app import services
    from app.bandwidth import BandwidthError, compile_site

    n = 0
    for site in db.scalars(select(Site)).all():
        try:
            compiled = compile_site(site.measured_upload_mbps, site.business_backup_budget_percent, 1)
        except BandwidthError:
            continue
        if not compiled["ok"] or not compiled.get("gateway_bytes_per_sec"):
            continue
        try:
            services.notify_gateway_site_limit(site.id, compiled["gateway_bytes_per_sec"])
            n += 1
        except Exception:
            pass
    return n


def loop_once() -> dict:
    engine = make_engine(settings.database_url)
    SessionLocal = session_factory(engine)
    db = SessionLocal()
    try:
        if not _claim(db, "control-worker"):
            return {"skipped": True}
        expired = expire_commands(db)
        health = refresh_health(db)
        from app import admission as wan

        reaped = wan.reap_expired_leases(db)
        now = utcnow()
        for site in db.scalars(select(Site)).all():
            if site.emergency_restore_until and as_utc(site.emergency_restore_until) <= now:
                site.emergency_restore_until = None
                site.emergency_restore_kibps = 0
                db.commit()
        global _last_site_limit_push
        pushed = 0
        if time.monotonic() - _last_site_limit_push >= SITE_LIMIT_PUSH_SECONDS:
            pushed = push_site_limits(db)
            _last_site_limit_push = time.monotonic()
        return {"expired_commands": expired, "health_updates": health, "leases_reaped": reaped, "site_limits_pushed": pushed}
    finally:
        db.close()


def run():
    while True:
        loop_once()
        time.sleep(15)


if __name__ == "__main__":
    run()
