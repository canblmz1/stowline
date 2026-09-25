"""Regression: a real Railway deployment must not silently inherit the
developer-friendly defaults (STOWLINE_LAB_MODE=true, dev secret key, dev admin
password). Off Railway, those defaults must keep working unchanged so local
dev/CI never needs extra env vars.
"""

from __future__ import annotations

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import pytest

from app.config import Settings, require_admin_credentials_set, running_on_railway

RAILWAY_MARKER = "RAILWAY_ENVIRONMENT"


def _clear_stowline_env(monkeypatch):
    for name in (
        "STOWLINE_LAB_MODE",
        "STOWLINE_ALLOW_INSECURE_HTTP",
        "STOWLINE_SECRET_KEY",
        "STOWLINE_ADMIN_PASSWORD",
        "STOWLINE_DATABASE_URL",
    ):
        monkeypatch.delenv(name, raising=False)
    for marker in (
        "RAILWAY_ENVIRONMENT",
        "RAILWAY_ENVIRONMENT_NAME",
        "RAILWAY_PROJECT_ID",
        "RAILWAY_SERVICE_ID",
        "RAILWAY_DEPLOYMENT_ID",
        "STOWLINE_PRODUCTION",
    ):
        monkeypatch.delenv(marker, raising=False)


def test_running_on_railway_false_by_default(monkeypatch):
    _clear_stowline_env(monkeypatch)
    assert running_on_railway() is False


def test_running_on_railway_true_when_any_marker_set(monkeypatch):
    _clear_stowline_env(monkeypatch)
    monkeypatch.setenv("RAILWAY_SERVICE_ID", "svc_123")
    assert running_on_railway() is True


def test_lab_mode_is_off_unless_asked_for(monkeypatch):
    """A server started with no configuration must not run with relaxed
    cookie/CSRF/https rules; trying it out means STOWLINE_LAB_MODE=true."""
    _clear_stowline_env(monkeypatch)
    s = Settings()
    assert s.lab_mode is False
    assert s.cookie_secure() is True
    monkeypatch.setenv("STOWLINE_LAB_MODE", "true")
    assert Settings().cookie_secure() is False


def test_dev_default_credentials_are_refused_outside_lab_mode(monkeypatch):
    _clear_stowline_env(monkeypatch)
    with pytest.raises(RuntimeError, match="unsafe defaults"):
        require_admin_credentials_set(Settings())


def test_railway_with_safe_overrides_starts_cleanly(monkeypatch):
    _clear_stowline_env(monkeypatch)
    monkeypatch.setenv(RAILWAY_MARKER, "production")
    monkeypatch.setenv("STOWLINE_LAB_MODE", "false")
    monkeypatch.setenv("STOWLINE_ALLOW_INSECURE_HTTP", "false")
    monkeypatch.setenv("STOWLINE_SECRET_KEY", "a-real-generated-secret")
    monkeypatch.setenv("STOWLINE_ADMIN_PASSWORD", "a-real-generated-password")
    s = Settings()
    assert s.cookie_secure() is True
    require_admin_credentials_set(s)  # must not raise


@pytest.mark.parametrize(
    "env",
    [
        {"STOWLINE_LAB_MODE": "true"},
        {"STOWLINE_ALLOW_INSECURE_HTTP": "true"},
        {"STOWLINE_LAB_MODE": "false", "STOWLINE_ALLOW_INSECURE_HTTP": "true"},  # lab_mode fixed, insecure_http not
    ],
)
def test_railway_refuses_to_start_with_lab_or_insecure_http(monkeypatch, env):
    _clear_stowline_env(monkeypatch)
    monkeypatch.setenv(RAILWAY_MARKER, "production")
    for k, v in env.items():
        monkeypatch.setenv(k, v)
    with pytest.raises(ValueError, match="refusing to start on Railway"):
        Settings()


def test_railway_lab_mode_false_and_insecure_http_default_false_starts_cleanly(monkeypatch):
    """allow_insecure_http's own class default is already False, so fixing
    only STOWLINE_LAB_MODE is sufficient — this must NOT raise."""
    _clear_stowline_env(monkeypatch)
    monkeypatch.setenv(RAILWAY_MARKER, "production")
    monkeypatch.setenv("STOWLINE_LAB_MODE", "false")
    Settings()  # must not raise


def test_worker_like_service_without_admin_password_set_does_not_crash(monkeypatch):
    """Regression: a real incident. The worker service has no login endpoint
    and legitimately never had STOWLINE_ADMIN_PASSWORD set. Settings() must not
    require it — only the control-plane startup (require_admin_credentials_set)
    may."""
    _clear_stowline_env(monkeypatch)
    monkeypatch.setenv(RAILWAY_MARKER, "production")
    monkeypatch.setenv("STOWLINE_LAB_MODE", "false")
    monkeypatch.setenv("STOWLINE_ALLOW_INSECURE_HTTP", "false")
    # Deliberately no STOWLINE_ADMIN_PASSWORD / STOWLINE_SECRET_KEY.
    s = Settings()  # must not raise
    assert s.admin_password == "change-me-now"


@pytest.mark.parametrize(
    "overrides",
    [
        {},  # both left at dev defaults
        {"STOWLINE_SECRET_KEY": "real-secret"},  # admin password still default
        {"STOWLINE_ADMIN_PASSWORD": "real-password"},  # secret key still default
    ],
)
def test_require_admin_credentials_set_refuses_dev_defaults_on_railway(monkeypatch, overrides):
    _clear_stowline_env(monkeypatch)
    monkeypatch.setenv(RAILWAY_MARKER, "production")
    monkeypatch.setenv("STOWLINE_LAB_MODE", "false")
    monkeypatch.setenv("STOWLINE_ALLOW_INSECURE_HTTP", "false")
    for k, v in overrides.items():
        monkeypatch.setenv(k, v)
    s = Settings()  # the shared validator does not check credentials; must not raise
    with pytest.raises(RuntimeError, match="refusing to start with unsafe defaults"):
        require_admin_credentials_set(s)


def test_require_admin_credentials_set_is_a_noop_in_lab_mode(monkeypatch):
    _clear_stowline_env(monkeypatch)
    monkeypatch.setenv("STOWLINE_LAB_MODE", "true")
    require_admin_credentials_set(Settings())  # must not raise


def test_a_self_hosted_production_server_gets_the_same_fail_closed_checks(monkeypatch):
    """A plain VM has no Railway markers; STOWLINE_PRODUCTION=1 must
    turn on exactly the same refusals, or a forgotten STOWLINE_LAB_MODE would
    silently run production in lab mode."""
    _clear_stowline_env(monkeypatch)
    monkeypatch.setenv("STOWLINE_PRODUCTION", "1")
    monkeypatch.setenv("STOWLINE_LAB_MODE", "true")
    with pytest.raises(ValueError):
        Settings()
    monkeypatch.setenv("STOWLINE_LAB_MODE", "false")
    monkeypatch.setenv("STOWLINE_ALLOW_INSECURE_HTTP", "false")
    s = Settings()
    assert s.cookie_secure() is True
    with pytest.raises(RuntimeError):
        require_admin_credentials_set(s)
