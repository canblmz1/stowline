"""Real backup percentage/ETA support: the server-side half.

Everything the agent needs to send already ships (see
docs/superpowers/specs/2026-09-22-backup-progress-eta-design.md); these
tests cover the two things this sub-project adds server-side: rejecting a
self-contradictory progress sample, and deriving `phase` from data that was
already flowing before this change.
"""
from __future__ import annotations

from test_api import _enroll, _measure_sites, client, login  # noqa: F401  -- must precede any app import: sets the test env before app.config.settings is built

from app.security import new_id  # noqa: E402


def _headers(d: dict) -> dict:
    return {"Authorization": f"Bearer {d['control_credential']}"}


def _acquire(client, d: dict, attempt: str) -> dict:
    r = client.post(
        "/api/v1/agent/wan/leases/acquire",
        headers=_headers(d),
        json={"attempt_id": attempt, "class": "NORMAL_BACKUP", "job_id": f"job-{attempt[-4:]}"},
    )
    assert r.status_code == 200, r.text
    assert r.json()["status"] == "GRANTED", r.json()
    return r.json()


def _renew(client, d: dict, lease: dict, **progress) -> None:
    r = client.post(f"/api/v1/agent/wan/leases/{lease['lease_id']}/renew", headers=_headers(d), json=progress)
    assert r.status_code == 200, r.text


def _current_progress(client, device_id: str) -> dict:
    return client.get(f"/api/v1/admin/devices/{device_id}").json()["status"]["current_progress"]


def test_a_sample_where_bytes_done_exceeds_total_bytes_is_dropped_not_stored(client):
    _measure_sites(client)
    d = _enroll(client, "progress-bad-sample", "branch")
    lease = _acquire(client, d, new_id())
    _renew(client, d, lease, percent_done=10, bytes_done=100, total_bytes=1000)
    bad = client.post(
        f"/api/v1/agent/wan/leases/{lease['lease_id']}/renew",
        headers=_headers(d),
        json={"percent_done": 90, "bytes_done": 5000, "total_bytes": 1000},
    )
    assert bad.status_code == 200, bad.text  # renewal itself still succeeds -- one bad sample must not drop the lease
    current = _current_progress(client, d["device_id"])
    assert current["bytes_done"] == 100 and current["total_bytes"] == 1000, "the bad sample must not overwrite the last good one"


def test_a_self_consistent_sample_after_a_bad_one_is_still_accepted(client):
    _measure_sites(client)
    d = _enroll(client, "progress-recovers", "branch")
    lease = _acquire(client, d, new_id())
    _renew(client, d, lease, percent_done=10, bytes_done=100, total_bytes=1000)
    client.post(f"/api/v1/agent/wan/leases/{lease['lease_id']}/renew", headers=_headers(d), json={"bytes_done": 5000, "total_bytes": 1000})
    _renew(client, d, lease, percent_done=50, bytes_done=500, total_bytes=1000)
    current = _current_progress(client, d["device_id"])
    assert current["bytes_done"] == 500


def test_bytes_done_alone_without_total_bytes_is_not_second_guessed(client):
    _measure_sites(client)
    d = _enroll(client, "progress-partial-fields", "branch")
    lease = _acquire(client, d, new_id())
    r = client.post(f"/api/v1/agent/wan/leases/{lease['lease_id']}/renew", headers=_headers(d), json={"bytes_done": 500})
    assert r.status_code == 200, r.text
    assert _current_progress(client, d["device_id"])["bytes_done"] == 500


def test_phase_is_preparing_before_any_real_sample_and_backing_up_after(client):
    _measure_sites(client)
    d = _enroll(client, "progress-phase", "branch")
    lease = _acquire(client, d, new_id())
    assert _current_progress(client, d["device_id"])["phase"] == "PREPARING"
    _renew(client, d, lease)  # a real agent's first renewal can arrive with no progress fields at all yet
    assert _current_progress(client, d["device_id"])["phase"] == "PREPARING"
    _renew(client, d, lease, percent_done=0, bytes_done=0, total_bytes=0)  # the documented zero-value placeholder
    assert _current_progress(client, d["device_id"])["phase"] == "PREPARING"
    _renew(client, d, lease, percent_done=12, bytes_done=120, total_bytes=1000)
    assert _current_progress(client, d["device_id"])["phase"] == "BACKING_UP"


def test_phase_is_finalizing_once_everything_is_read_but_the_run_is_still_going(client):
    # restic's percent counts bytes read, not bytes uploaded: under a WAN
    # limit it reaches 100% in seconds and then keeps uploading for minutes.
    # A live lease at 100% must not look like a finished backup.
    _measure_sites(client)
    d = _enroll(client, "progress-finalizing", "branch")
    lease = _acquire(client, d, new_id())
    _renew(client, d, lease, percent_done=100, bytes_done=1000, total_bytes=1000)
    assert _current_progress(client, d["device_id"])["phase"] == "FINALIZING"


def test_an_agent_that_never_sends_progress_stays_in_preparing_indefinitely(client):
    """Simulates today's real, currently-deployed agent (pre-progress-support):
    it renews the lease (proving it is alive) but never once includes a
    progress field. The panel's "this agent version doesn't report %"
    message (built client-side, see the design doc) keys off exactly this:
    phase stuck at PREPARING with total_bytes never present, for a run
    that has otherwise clearly been going for a while."""
    _measure_sites(client)
    d = _enroll(client, "progress-old-agent", "branch")
    lease = _acquire(client, d, new_id())
    for _ in range(3):
        _renew(client, d, lease)
    current = _current_progress(client, d["device_id"])
    assert current["phase"] == "PREPARING"
    assert current["total_bytes"] is None


def test_progress_disappears_once_the_lease_expires_no_new_code_needed(client):
    """Proves the spec's claim that staleness detection is already free:
    progress lives on the lease row, and a lease that stops renewing simply
    stops counting as a live lease. No new code in this task touches this --
    this test exists so the claim is verified, not assumed."""
    import datetime as dt

    from app.db import WanAdmissionLease
    from app.main import SessionLocal

    _measure_sites(client)
    d = _enroll(client, "progress-stale", "branch")
    lease = _acquire(client, d, new_id())
    _renew(client, d, lease, percent_done=50, bytes_done=500, total_bytes=1000)
    assert _current_progress(client, d["device_id"])["percent_done"] == 50

    with SessionLocal() as db:
        row = db.get(WanAdmissionLease, lease["lease_id"])
        row.expires_at = dt.datetime.now(dt.timezone.utc) - dt.timedelta(seconds=1)
        db.commit()

    status = client.get(f"/api/v1/admin/devices/{d['device_id']}").json()["status"]
    assert status["backing_up"] is False
    assert status["current_progress"] is None or status["current_progress"].get("phase") != "BACKING_UP"


def test_restore_download_defaults_to_four_times_the_upload_share_when_the_site_sets_none(client):
    # Download links are far wider than upload; tying restores to the upload
    # share made a 208 MB self-service restore take 9 minutes on the test PC.
    _measure_sites(client)
    d = _enroll(client, "restore-speed-default", "branch")
    backup = _acquire(client, d, new_id())
    r = client.post(
        "/api/v1/agent/wan/leases/acquire",
        headers=_headers(d),
        json={"attempt_id": new_id(), "class": "RESTORE", "job_id": "job-rest"},
    )
    body = r.json()
    if body["status"] != "GRANTED":  # one device holds one lease at a time; release the backup first
        client.post(f"/api/v1/agent/wan/leases/{backup['lease_id']}/release", headers=_headers(d), json={})
        body = client.post(
            "/api/v1/agent/wan/leases/acquire",
            headers=_headers(d),
            json={"attempt_id": new_id(), "class": "RESTORE", "job_id": "job-rest2"},
        ).json()
    assert body["status"] == "GRANTED", body
    assert body["restore_download_kibps"] == 4 * body["bandwidth_kibps"]
