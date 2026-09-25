import os

from pydantic import field_validator, model_validator
from pydantic_settings import BaseSettings, SettingsConfigDict

# Any of these are set by Railway on every deployed container. Their presence
# means this process is a real deployment, not a developer's local shell —
# a context where STOWLINE_LAB_MODE/STOWLINE_ALLOW_INSECURE_HTTP must never be
# silently left at their dev-friendly defaults.
_RAILWAY_ENV_MARKERS = (
    "RAILWAY_ENVIRONMENT",
    "RAILWAY_ENVIRONMENT_NAME",
    "RAILWAY_PROJECT_ID",
    "RAILWAY_SERVICE_ID",
    "RAILWAY_DEPLOYMENT_ID",
    # Any other production host: set STOWLINE_PRODUCTION=1 in the service
    # environment to get the same fail-closed checks.
    "STOWLINE_PRODUCTION",
)

_DEV_DEFAULT_SECRET_KEY = "dev-only-change-me"
_DEV_DEFAULT_ADMIN_PASSWORD = "change-me-now"


def running_on_railway(env: dict | None = None) -> bool:
    env = os.environ if env is None else env
    return any(env.get(name) for name in _RAILWAY_ENV_MARKERS)


def normalize_database_url(url: str) -> str:
    """Accept Railway postgres:// URLs without logging them."""
    raw = (url or "").strip()
    if not raw:
        return raw
    if raw.startswith("postgres://"):
        raw = "postgresql://" + raw[len("postgres://") :]
    scheme, sep, rest = raw.partition("://")
    if sep and scheme in {"postgresql", "postgres"} and "+psycopg" not in scheme:
        raw = "postgresql+psycopg://" + rest
    return raw


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_prefix="STOWLINE_", extra="ignore")

    database_url: str = "sqlite+pysqlite:///./var/stowline-control.db"
    secret_key: str = "dev-only-change-me"
    admin_username: str = "admin"
    admin_password: str = "change-me-now"
    # Shown in the admin panel header, login page and footer.
    org_name: str = "My Organization"
    allow_insecure_http: bool = False
    session_hours: int = 8
    enrollment_minutes: int = 15
    heartbeat_offline_seconds: int = 900
    cors_origins: str = ""
    # Lab mode relaxes cookie security, the CSRF origin check and the https-only
    # gateway rule, and allows the dev default credentials. Off unless asked for.
    lab_mode: bool = False
    gateway_admin_url: str = ""
    gateway_admin_token: str = ""
    gateway_public_base: str = "rest:http://127.0.0.1:8081"
    rate_limit_per_minute: int = 600
    enforce_backup_window: bool = True
    escrow_key: str = ""

    @model_validator(mode="before")
    @classmethod
    def _database_url_fallback(cls, data):
        if not isinstance(data, dict):
            return data
        if data.get("database_url"):
            return data
        fallback = os.environ.get("DATABASE_URL")
        if fallback:
            data["database_url"] = fallback
        return data

    @field_validator("database_url", mode="before")
    @classmethod
    def _normalize_database_url(cls, value: str) -> str:
        return normalize_database_url(value) if isinstance(value, str) else value

    @model_validator(mode="after")
    def _fail_closed_on_railway(self):
        """Production must not rely on defaults. Every Railway service that
        imports this module (control, worker, and anything else added later)
        shares these two flags, and both gate real behavior wherever they're
        read (cookie_secure(), public_gateway_base()) — so it is safe to
        enforce them unconditionally for any process on Railway (detected
        from Railway's own injected env vars, never from a value this class
        could be tricked into reporting). Local dev, CI, and pytest never set
        these markers, so this never fires outside Railway.

        Credential defaults (admin password, secret key) are deliberately
        NOT checked here: they are meaningful only to the service that
        actually serves the admin login (see require_admin_credentials_set
        below), and a service that never reads them — e.g. the worker,
        which has no HTTP login surface and legitimately never had
        STOWLINE_ADMIN_PASSWORD set — must not be crashed by a check for a
        field it doesn't use."""
        if not running_on_railway():
            return self
        problems = []
        if self.lab_mode:
            problems.append("STOWLINE_LAB_MODE must be false on Railway")
        if self.allow_insecure_http:
            problems.append("STOWLINE_ALLOW_INSECURE_HTTP must be false on Railway")
        if problems:
            raise ValueError("refusing to start on Railway with unsafe defaults: " + "; ".join(problems))
        return self

    def cookie_secure(self) -> bool:
        return not self.allow_insecure_http and not self.lab_mode


def require_admin_credentials_set(s: "Settings") -> None:
    """Call this only from the process that actually serves the admin login
    (the control-plane FastAPI app's startup). Refuses to start with the
    publicly-known dev admin password or secret key anywhere but lab mode --
    they are printed in this file, so they protect nothing."""
    if not running_on_railway() and s.lab_mode:
        return
    problems = []
    if s.secret_key == _DEV_DEFAULT_SECRET_KEY:
        problems.append("STOWLINE_SECRET_KEY must be set (dev default in use)")
    if s.admin_password == _DEV_DEFAULT_ADMIN_PASSWORD:
        problems.append("STOWLINE_ADMIN_PASSWORD must be set (dev default in use)")
    if problems:
        raise RuntimeError("refusing to start with unsafe defaults: " + "; ".join(problems) + " -- or set STOWLINE_LAB_MODE=true for a local trial")


settings = Settings()
