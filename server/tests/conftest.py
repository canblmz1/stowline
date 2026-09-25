import sys

import pytest


@pytest.fixture(autouse=True)
def _backup_window_guard_is_off_unless_a_test_turns_it_on(monkeypatch):
    # The guard compares against the wall clock, so leaving it on would make
    # every test that queues RUN_BACKUP/RUN_CANARY pass or fail depending on
    # what time of day the suite happens to run. Guard tests enable it
    # explicitly together with a frozen clock.
    services = sys.modules.get("app.services")
    if services is not None:
        monkeypatch.setattr(services.settings, "enforce_backup_window", False, raising=False)
    yield


@pytest.fixture(autouse=True)
def _sign_in_limiter_starts_empty():
    # The limiter is process-wide; a test that fails logins on purpose must
    # not lock the shared admin out of the tests that follow it.
    ratelimit = sys.modules.get("app.ratelimit")
    if ratelimit is not None:
        ratelimit.limiter.reset()
    yield


@pytest.fixture()
def gateway_ready(monkeypatch):
    # Without a configured gateway the panel reports GATEWAY_UNAVAILABLE ahead
    # of any attempt error, which would mask what such tests look at.
    import app.services as services

    monkeypatch.setattr(
        services, "gateway_registration_state", lambda: {"gateway_registration": "configured", "wan_ready": True, "gateway_registration_reason": ""}
    )
