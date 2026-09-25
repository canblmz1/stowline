"""Backup runs that start after the hard stop are dead on arrival.

The agent clamps every run's deadline to *today's* hard stop
(agent/cmd/stowline-agent/wan.go deadlineFor). An operator "Backup Now" that
arrives after that time therefore fails in about a second with a generic
INTERNAL error and uploads nothing. These tests pin the control-plane side:
refuse the click up front with a reason the operator can act on, and explain
a run the agent itself stopped at the hard stop instead of blaming an operator.
"""
from __future__ import annotations

from datetime import datetime, timezone

import pytest
from sqlalchemy import func, select

from test_api import client, login  # noqa: F401  -- must precede any app import: sets the test env before app.config.settings is built

from app import errors_ux, schedule_policy, services  # noqa: E402
from app.config import Settings  # noqa: E402
from app.db import BackupAttempt, BackupJob, Command, Device, Policy, PolicyRevision  # noqa: E402
from app.main import SessionLocal  # noqa: E402
from app.security import default_policy_config, new_id, payload_hash  # noqa: E402

IST = "Europe/Istanbul"  # UTC+3, no DST
AT_1500_IST = datetime(2026, 9, 18, 12, 0, tzinfo=timezone.utc)
AT_1744_IST = datetime(2026, 9, 18, 14, 44, 59, tzinfo=timezone.utc)
AT_1745_IST = datetime(2026, 9, 18, 14, 45, 0, tzinfo=timezone.utc)
AT_1800_IST = datetime(2026, 9, 18, 15, 0, tzinfo=timezone.utc)
INCIDENT_ENDED_AT = datetime(2026, 9, 18, 14, 45, 33, tzinfo=timezone.utc)  # 17:45:33 IST, as recorded live


def _enroll(client, label: str) -> dict:
    tok = client.post("/api/v1/admin/enrollment-tokens", json={"label": label}).json()["token"]
    return client.post("/api/v1/agent/enrollments", json={"token": tok, "hostname": f"pc-{label}"}).json()


def _assign_schedule(device_id: str, **overrides) -> None:
    cfg = default_policy_config()
    cfg["schedule"].update(overrides)
    with SessionLocal() as db:
        pol = Policy(id=new_id(), name=f"window-test-{new_id()[:8]}", description="test")
        db.add(pol)
        db.flush()
        rev = PolicyRevision(id=new_id(), policy_id=pol.id, version=1, content_hash=payload_hash(cfg), config_json=cfg)
        db.add(rev)
        db.flush()
        db.get(Device, device_id).assigned_policy_revision_id = rev.id
        db.commit()


def _command_count(device_id: str) -> int:
    with SessionLocal() as db:
        return db.scalar(select(func.count()).select_from(Command).where(Command.device_id == device_id))


@pytest.fixture()
def guard_on(monkeypatch):
    monkeypatch.setattr(services.settings, "enforce_backup_window", True)


def _freeze(monkeypatch, when: datetime) -> None:
    monkeypatch.setattr(services, "now", lambda: when)


# --- schedule_policy.hard_stop_reached --------------------------------------


def test_hard_stop_is_reached_from_the_stop_minute_on():
    sched = {"timezone": IST, "hard_stop": "17:45"}
    assert schedule_policy.hard_stop_reached(sched, AT_1744_IST) is None
    assert schedule_policy.hard_stop_reached(sched, AT_1745_IST) == "17:45"
    assert schedule_policy.hard_stop_reached(sched, AT_1800_IST) == "17:45"


def test_hard_stop_is_judged_in_the_schedules_own_timezone_not_utc():
    # 15:00 UTC is 18:00 in Istanbul, past the stop -- yet 15:00 is earlier
    # than 17:45 on a raw UTC clock, which is the comparison a naive
    # implementation would make.
    sched = {"timezone": IST, "hard_stop": "17:45"}
    assert schedule_policy.hard_stop_reached(sched, AT_1500_IST) is None
    assert schedule_policy.hard_stop_reached(sched, AT_1800_IST) == "17:45"


@pytest.mark.parametrize("sched", [None, {}, {"hard_stop": "17:45"}, {"timezone": IST}, {"timezone": IST, "hard_stop": ""}])
def test_no_schedule_or_no_hard_stop_never_blocks(sched):
    assert schedule_policy.hard_stop_reached(sched, AT_1800_IST) is None


def test_hq_allows_an_evening_seed_run():
    assert schedule_policy.site_policy("hq")["hard_stop"] == "21:00"


@pytest.mark.parametrize("site", sorted(schedule_policy.SITE_SCHEDULE))
def test_every_site_hard_stop_is_at_or_after_its_start_deadline(site):
    # The agent's SchedulePolicy.Validate rejects a policy whose hard stop is
    # before the start deadline, and a rejected policy blocks every backup.
    pol = schedule_policy.site_policy(site)
    assert schedule_policy._minutes(pol["hard_stop"]) >= schedule_policy._minutes(pol["window_end"])


# --- the command guard --------------------------------------------------------


def test_the_guard_defaults_on_in_production():
    assert Settings.model_fields["enforce_backup_window"].default is True


def test_run_backup_after_the_hard_stop_is_refused_with_an_actionable_turkish_reason(monkeypatch, client, guard_on):
    login(client)
    d = _enroll(client, "guard1")
    _assign_schedule(d["device_id"], hard_stop="17:45")
    _freeze(monkeypatch, AT_1800_IST)
    r = client.post(f"/api/v1/admin/devices/{d['device_id']}/commands", json={"kind": "RUN_BACKUP", "payload": {}})
    assert r.status_code == 409
    detail = r.json()["detail"]
    assert "17:45" in detail and "pencere" in detail.lower()
    assert _command_count(d["device_id"]) == 0


def test_run_canary_is_refused_after_the_hard_stop_too(monkeypatch, client, guard_on):
    login(client)
    d = _enroll(client, "guard2")
    _assign_schedule(d["device_id"], hard_stop="17:45")
    _freeze(monkeypatch, AT_1800_IST)
    r = client.post(f"/api/v1/admin/devices/{d['device_id']}/commands", json={"kind": "RUN_CANARY", "payload": {}})
    assert r.status_code == 409
    assert _command_count(d["device_id"]) == 0


def test_run_backup_before_the_hard_stop_is_queued(monkeypatch, client, guard_on):
    login(client)
    d = _enroll(client, "guard3")
    _assign_schedule(d["device_id"], hard_stop="17:45")
    _freeze(monkeypatch, AT_1500_IST)
    r = client.post(f"/api/v1/admin/devices/{d['device_id']}/commands", json={"kind": "RUN_BACKUP", "payload": {}})
    assert r.status_code == 200, r.text
    assert _command_count(d["device_id"]) == 1


def test_commands_that_are_not_backup_runs_are_never_window_gated(monkeypatch, client, guard_on):
    login(client)
    d = _enroll(client, "guard4")
    _assign_schedule(d["device_id"], hard_stop="17:45")
    _freeze(monkeypatch, AT_1800_IST)
    for kind in ("REFRESH_POLICY", "REPORT_INVENTORY", "CANCEL_CURRENT_SAFE_OPERATION"):
        r = client.post(f"/api/v1/admin/devices/{d['device_id']}/commands", json={"kind": kind, "payload": {}})
        assert r.status_code == 200, (kind, r.text)


def test_the_guard_follows_the_devices_own_assigned_schedule(monkeypatch, client, guard_on):
    login(client)
    d = _enroll(client, "guard5")
    _freeze(monkeypatch, AT_1800_IST)
    _assign_schedule(d["device_id"], hard_stop="23:30")
    ok = client.post(f"/api/v1/admin/devices/{d['device_id']}/commands", json={"kind": "RUN_BACKUP", "payload": {}})
    assert ok.status_code == 200, ok.text
    _assign_schedule(d["device_id"], hard_stop="17:45")
    refused = client.post(f"/api/v1/admin/devices/{d['device_id']}/commands", json={"kind": "RUN_BACKUP", "payload": {}})
    assert refused.status_code == 409


def test_a_device_with_no_hard_stop_in_its_schedule_is_never_blocked(monkeypatch, client, guard_on):
    login(client)
    d = _enroll(client, "guard6")
    _assign_schedule(d["device_id"], hard_stop="")
    _freeze(monkeypatch, AT_1800_IST)
    r = client.post(f"/api/v1/admin/devices/{d['device_id']}/commands", json={"kind": "RUN_BACKUP", "payload": {}})
    assert r.status_code == 200, r.text


# --- error catalog ------------------------------------------------------------


def test_the_error_catalog_is_entirely_turkish():
    english_markers = (" the ", " is ", " not ", " was ", " Backup ", "Start the", "Do not", " cannot ")
    for item in errors_ux.catalog()["items"]:
        for text in (item["title"], item["summary"]):
            assert not any(m in f" {text} " for m in english_markers), (item["code"], text)


def test_cancelled_without_a_known_cause_does_not_blame_an_operator():
    human = errors_ux.explain_error("CANCELLED")
    assert human["code"] == "CANCELLED"
    assert "iptal" in human["title"].lower()
    assert "bir operatör çalışan işi iptal etti" not in human["summary"].lower()
    assert "kaydedilmedi" in human["summary"]


def test_window_cause_names_the_stop_time_and_says_no_operator_did_it():
    human = errors_ux.explain_error("CANCELLED", cancel_cause="window", hard_stop="17:45")
    assert "pencere" in human["title"].lower()
    assert "17:45" in human["summary"]
    assert "operatör yapmadı" in human["summary"]
    assert "korunur" in human["summary"]
    assert human["technical"] == "CANCELLED"


def test_operator_cause_says_an_operator_cancelled():
    human = errors_ux.explain_error("CANCELLED", cancel_cause="operator")
    assert "operatör" in human["summary"].lower()


# --- cancel-cause inference ---------------------------------------------------

SCHED = {"timezone": IST, "hard_stop": "17:45"}


def test_a_run_that_ends_at_the_hard_stop_is_attributed_to_the_window():
    # The live incident: hard stop 17:45:00 IST, the agent released its lease
    # at 17:45:04 and the control plane recorded the attempt at 17:45:33.
    assert errors_ux.infer_cancel_cause(INCIDENT_ENDED_AT, SCHED, []) == "window"


def test_an_operator_cancel_acked_just_before_the_end_wins_over_the_window():
    acked = datetime(2026, 9, 18, 14, 44, 50, tzinfo=timezone.utc)
    assert errors_ux.infer_cancel_cause(INCIDENT_ENDED_AT, SCHED, [acked]) == "operator"


def test_an_operator_cancel_from_long_before_the_run_ended_is_not_the_cause():
    # The agent does not poll while a backup runs, so a cancel acked an hour
    # earlier was delivered before the run started and did nothing.
    acked = datetime(2026, 9, 18, 13, 30, 0, tzinfo=timezone.utc)
    assert errors_ux.infer_cancel_cause(INCIDENT_ENDED_AT, SCHED, [acked]) == "window"


def test_an_end_far_from_the_hard_stop_has_no_inferred_cause():
    assert errors_ux.infer_cancel_cause(datetime(2026, 9, 18, 13, 0, tzinfo=timezone.utc), SCHED, []) == ""


def test_naive_database_timestamps_are_treated_as_utc():
    assert errors_ux.infer_cancel_cause(INCIDENT_ENDED_AT.replace(tzinfo=None), SCHED, []) == "window"


def test_no_end_time_or_no_schedule_gives_no_cause():
    assert errors_ux.infer_cancel_cause(None, SCHED, []) == ""
    assert errors_ux.infer_cancel_cause(INCIDENT_ENDED_AT, {}, []) == ""


# --- what the admin page shows ---------------------------------------------------


def _cancelled_device(client, label: str, ended_at: datetime, operator_acked: datetime | None = None) -> str:
    d = _enroll(client, label)
    _assign_schedule(d["device_id"], hard_stop="17:45")
    r = client.post(
        "/api/v1/agent/jobs/events",
        headers={"Authorization": f"Bearer {d['control_credential']}"},
        json={"outcome": "CANCELLED", "error_class": "CANCELLED", "slot_key": f"slot-{label}"},
    )
    assert r.status_code == 200, r.text
    with SessionLocal() as db:
        att = db.scalar(select(BackupAttempt).join(BackupJob, BackupJob.id == BackupAttempt.job_id).where(BackupJob.device_id == d["device_id"]))
        att.ended_at = ended_at
        if operator_acked is not None:
            db.add(
                Command(
                    id=new_id(),
                    device_id=d["device_id"],
                    kind="CANCEL_CURRENT_SAFE_OPERATION",
                    payload={},
                    payload_hash=payload_hash({}),
                    state="SUCCEEDED",
                    expires_at=operator_acked,
                    acked_at=operator_acked,
                )
            )
        db.commit()
    return d["device_id"]


def _human(client, device_id: str) -> dict:
    return client.get(f"/api/v1/admin/devices/{device_id}").json()["status"]["last_error_human"]


def test_the_page_explains_a_run_the_agent_stopped_at_the_hard_stop(client, gateway_ready):
    login(client)
    device_id = _cancelled_device(client, "page1", INCIDENT_ENDED_AT)
    human = _human(client, device_id)
    assert "pencere" in human["title"].lower()
    assert "17:45" in human["summary"]


def test_the_page_says_operator_only_when_an_operator_cancel_was_acked_at_the_time(client, gateway_ready):
    login(client)
    device_id = _cancelled_device(client, "page2", INCIDENT_ENDED_AT, operator_acked=datetime(2026, 9, 18, 14, 44, 50, tzinfo=timezone.utc))
    assert "operatör" in _human(client, device_id)["summary"].lower()


def test_the_page_stays_generic_when_the_cause_is_unknown(client, gateway_ready):
    login(client)
    device_id = _cancelled_device(client, "page3", datetime(2026, 9, 18, 13, 0, tzinfo=timezone.utc))
    human = _human(client, device_id)
    assert "pencere" not in human["title"].lower()
    assert "iptal" in human["title"].lower()
