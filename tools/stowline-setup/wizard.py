"""Stowline first-run Setup Wizard. Localhost only. Never starts a cloud backup."""

from __future__ import annotations

import argparse
import hashlib
import hmac
import json
import os
import secrets
import subprocess
import sys
import time
import webbrowser
from pathlib import Path

import httpx

from fastapi import Depends, FastAPI, Header, HTTPException
from fastapi.responses import FileResponse
from fastapi.staticfiles import StaticFiles
from pydantic import BaseModel, Field

def _add_app_path() -> None:
    here = Path(__file__).resolve().parent
    for cand in (
        here / "app",
        here.parent / "app",
        Path(os.environ.get("STOWLINE_SETUP_ROOT") or "") / "app",
    ):
        if (cand / "discovery.py").is_file():
            sys.path.insert(0, str(cand.parent))
            return
    repo_server = here.parents[1] / "server"
    if (repo_server / "app" / "discovery.py").is_file():
        sys.path.insert(0, str(repo_server))


_add_app_path()

from app.discovery import KnownFolders, collect_device_identity, discover  # noqa: E402
from app.network_preflight import (  # noqa: E402
    NetworkPreflightError,
    load_deploy,
    preflight,
    reject_insecure_remote,
)
from app.schedule_policy import DEPARTMENTS, SITE_SCHEDULE, first_backup_fit, normalize_department, validate_preferred_start  # noqa: E402
from app.selection import SelectionError, apply_pilot_source_roots, compile_selection, write_local_manifest  # noqa: E402

STATIC = Path(__file__).resolve().parent / "static"
PILOT_JSON = Path(r"C:\Stowline\config\pilot.json")
AGENT = Path(r"C:\Stowline\bin\stowline-agent.exe")
WORKSPACE_REPO_INIT = Path(r"C:\Stowline\bin\workspace-repo-init.exe")
MANIFEST = Path(r"C:\Stowline\config\selection-manifest.v1.json")


def _pinned_agent_sha() -> str:
    """The agent SHA-256 this package pinned (payload\\pins.json, written by
    scripts/package-installer.py); "" when run from a source checkout."""
    for cand in (Path(os.environ.get("STOWLINE_SETUP_ROOT", "")) / "pins.json", Path(__file__).resolve().parents[1] / "pins.json"):
        try:
            return str(json.loads(cand.read_text(encoding="utf-8")).get("stowline-agent.exe") or "")
        except (OSError, ValueError):
            continue
    return ""


QUALIFIED = _pinned_agent_sha()
# Since bootstrap.py started launching this wizard as a DETACHED_PROCESS
# (no console of its own), every console-subsystem child it spawns via
# subprocess.run below -- stowline-agent.exe, workspace-repo-init.exe -- has
# no console to inherit and Windows allocates each one a brand new,
# visible (blank -- their real stdout/stderr are captured to pipes, not
# shown there) console window instead. Confirmed live. CREATE_NO_WINDOW
# suppresses that allocation entirely; capture_output=True already
# captures everything these calls need regardless.
_NO_WINDOW = getattr(subprocess, "CREATE_NO_WINDOW", 0)

app = FastAPI(title="Stowline Setup Wizard", docs_url=None, redoc_url=None)
STATE: dict = {"synthetic": False, "no_enroll": False, "known": None, "discovery": None, "deploy": {}, "token": ""}


def require_wizard_token(x_wizard_token: str = Header(default="")) -> None:
    """This server binds to 127.0.0.1, but that does not stop a same-machine
    CSRF: any web page open in a browser on this PC can make a same-origin-free
    request to a localhost port, and a classic enctype="text/plain" form POST
    reaches a FastAPI route without a CORS preflight even with no CORS
    middleware configured. This process runs elevated and its /api/local/install
    route enrolls a real device and installs (but never starts) a Windows
    service, so every /api/local/* route requires a random per-run token
    that only reaches the wizard's own browser tab (via the URL main() opens,
    immediately stripped from the visible URL/history by the page's own JS)
    and is sent back only as a custom header, which neither an HTML form nor
    a simple cross-origin fetch() can attach. /api/local/start-first-backup,
    the separate action that actually starts the service, sits behind this
    same gate."""
    expected = STATE.get("token") or ""
    if not expected or not hmac.compare_digest(x_wizard_token or "", expected):
        raise HTTPException(401, "missing or invalid wizard session token")


LOCAL_API = Depends(require_wizard_token)


class OrgIn(BaseModel):
    site_id: str
    department: str
    custom_department: str = ""
    control_plane_url: str
    username: str
    password: str


class DiscoverIn(BaseModel):
    extra_roots: list[str] = Field(default_factory=list)


class CompileIn(BaseModel):
    selected: list[dict] = Field(default_factory=list)


class EstimateIn(BaseModel):
    site_id: str
    preferred_start_hhmm: str
    logical_bytes: int = 0


class InstallIn(BaseModel):
    site_id: str
    department: str
    custom_department: str = ""
    control_plane_url: str
    # Either a setup code from the admin panel (preferred: the admin
    # password never has to be typed on the computer) or the admin login.
    setup_code: str = Field(default="", max_length=64)
    username: str = ""
    password: str = ""
    preferred_start_hhmm: str
    selected: list[dict] = Field(default_factory=list)
    confirm_install: bool = False
    # The name the admin panel shows for this PC (the Windows computer name
    # itself is not changed). Empty keeps the hostname.
    display_name: str = Field(default="", max_length=64)


def _is_admin() -> bool:
    if os.name != "nt":
        return True
    try:
        import ctypes

        return bool(ctypes.windll.shell32.IsUserAnAdmin())  # type: ignore[attr-defined]
    except Exception:
        return False


@app.get("/")
def index():
    return FileResponse(STATIC / "wizard.html")


def _deploy() -> dict:
    return STATE.get("deploy") or {}


class ServerIn(BaseModel):
    url: str


def _apply_server_catalog(cat: dict) -> None:
    """Use the server's sites and departments instead of the defaults the
    package was built with (the schedule rules module reads these objects)."""
    sites = cat.get("sites") or []
    if not sites:
        raise HTTPException(502, "the server returned no sites")
    base = next(iter(SITE_SCHEDULE.values()), {}) if SITE_SCHEDULE else {}
    fresh = {}
    for site in sites:
        sid = str(site.get("id") or "").strip().lower()
        if not sid:
            continue
        merged = {"max_concurrent_wan_backups": 1, "limit_upload_kib": None, "catch_up": True, **{k: v for k, v in base.items() if k in ("timezone", "eligibility_start", "preferred_latest_start", "window_end", "hard_stop")}}
        merged.update({k: v for k, v in site.items() if k != "id"})
        fresh[sid] = merged
    SITE_SCHEDULE.clear()
    SITE_SCHEDULE.update(fresh)
    deps = [str(d) for d in (cat.get("departments") or []) if str(d).strip()]
    if deps:
        DEPARTMENTS[:] = deps


@app.post("/api/local/server", dependencies=[LOCAL_API])
def set_server(body: ServerIn):
    """A generic installer has no server baked in: the operator types it on
    the first screen. It must answer the connectivity preflight and serve
    its site list before setup continues."""
    if (STATE.get("deploy") or {}).get("control_plane_url") and not STATE.get("server_typed"):
        raise HTTPException(409, "this package is pinned to its own server")
    url = (body.url or "").strip().rstrip("/")
    if url and "://" not in url:
        url = "https://" + url
    dep = {**(STATE.get("deploy") or {}), "control_plane_url": url, "gateway_url": url + "/gw"}
    try:
        result = preflight(dep)
    except NetworkPreflightError as exc:
        return {"ok": False, "preflight": {"ok": False, "errors": [str(exc)]}}
    if not result.get("ok"):
        return {"ok": False, "preflight": result}
    try:
        with httpx.Client(timeout=15.0, follow_redirects=False) as hx:
            r = hx.get(url + "/api/v1/setup/catalog")
            r.raise_for_status()
            cat = r.json()
    except (httpx.HTTPError, ValueError) as exc:
        return {"ok": False, "preflight": {"ok": False, "errors": [f"could not read the server's site list: {exc}"]}}
    _apply_server_catalog(cat)
    STATE["deploy"] = dep
    STATE["server_typed"] = True
    return {"ok": True, "preflight": result, "catalog": catalog()}


@app.get("/api/local/identity", dependencies=[LOCAL_API])
def identity():
    info = collect_device_identity(str(AGENT))
    info["elevated"] = _is_admin()
    info["qualified_agent_sha"] = QUALIFIED
    info["sha_match"] = bool(info.get("agent_sha256") and info["agent_sha256"] == QUALIFIED)
    info["synthetic"] = bool(STATE["synthetic"])
    info["no_enroll"] = bool(STATE["no_enroll"])
    dep = _deploy()
    info["site_id"] = dep.get("site_id") or "hq"
    info["control_plane_url"] = dep.get("control_plane_url") or ""
    info["gateway_url"] = dep.get("gateway_url") or ""
    return info


@app.get("/api/local/preflight", dependencies=[LOCAL_API])
def local_preflight():
    dep = dict(_deploy())
    if STATE.get("synthetic"):
        return {
            "ok": True,
            "synthetic": True,
            "can_enroll": False,
            "control_plane": {"ok": True},
            "gateway": {"ok": True, "store": "rclone"},
            "google_backend": "rclone",
            "errors": [],
            "note": "Synthetic packaging check; enrollment is disabled.",
        }
    try:
        result = preflight(dep)
    except NetworkPreflightError as exc:
        return {
            "ok": False,
            "can_enroll": False,
            "control_plane": {"ok": False},
            "gateway": {"ok": False},
            "google_backend": "unavailable",
            "errors": [str(exc)],
        }
    return result


@app.get("/api/local/catalog", dependencies=[LOCAL_API])
def catalog():
    return {
        "sites": [{"id": k, **v} for k, v in SITE_SCHEDULE.items()],
        "departments": DEPARTMENTS,
    }


@app.post("/api/local/discover", dependencies=[LOCAL_API])
def do_discover(body: DiscoverIn):
    known = STATE.get("known")
    if STATE["synthetic"] and known is None:
        raise HTTPException(500, "synthetic tree missing")
    out = discover(known=known, extra_roots=body.extra_roots, include_downloads=False)
    STATE["discovery"] = out
    return out


@app.post("/api/local/browse-folder", dependencies=[LOCAL_API])
def do_browse_folder():
    """Pops a native Windows folder picker so the operator can add a
    folder beyond the auto-discovered known folders. Via PowerShell +
    WinForms, not tkinter -- the embeddable Python runtime this wizard
    ships with (python-*-embed-amd64.zip) does not include Tk/Tcl at
    all, confirmed: `import tkinter` fails on the packaged runtime."""
    script = (
        "Add-Type -AssemblyName System.Windows.Forms; "
        "$d = New-Object System.Windows.Forms.FolderBrowserDialog; "
        "$d.Description = 'Yedeklemeye eklenecek klasoru sec'; "
        "if ($d.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) { Write-Output $d.SelectedPath }"
    )
    powershell = r"C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe"
    try:
        proc = subprocess.run(
            [powershell, "-NoProfile", "-STA", "-ExecutionPolicy", "Bypass", "-Command", script],
            capture_output=True,
            text=True,
            timeout=300,
            creationflags=_NO_WINDOW,
        )
    except subprocess.TimeoutExpired as exc:
        raise HTTPException(504, "folder picker timed out after 300s") from exc
    if proc.returncode != 0:
        detail = (proc.stderr or "").strip()
        raise HTTPException(500, "folder picker failed: " + (detail or "unknown error"))
    path = (proc.stdout or "").strip()
    return {"path": path or None}


def _discovered_sensitive(disc: dict) -> list[str] | None:
    """Paths discover() flagged as sensitive (.env, keys, ...). Folders are
    backup roots, so these become restic excludes unless the operator
    selected and confirmed them. None (no discovery ran) makes the compiler
    walk the added folders itself."""
    if not disc:
        return None
    return [str(c.get("path")) for c in disc.get("candidates") or [] if c.get("category") == "sensitive" and c.get("path")]


@app.post("/api/local/compile", dependencies=[LOCAL_API])
def do_compile(body: CompileIn):
    disc = STATE.get("discovery") or {}
    try:
        manifest = compile_selection(
            selected=body.selected,
            office_files=disc.get("office_files") or {},
            extra_files=disc.get("extra_files") or {},
            sensitive_files=_discovered_sensitive(disc),
        )
    except SelectionError as exc:
        raise HTTPException(422, str(exc)) from exc
    STATE["manifest"] = manifest
    return manifest


@app.post("/api/local/estimate", dependencies=[LOCAL_API])
def do_estimate(body: EstimateIn):
    try:
        validate_preferred_start(body.site_id, body.preferred_start_hhmm)
        return first_backup_fit(site_id=body.site_id, logical_bytes=body.logical_bytes, preferred_hhmm=body.preferred_start_hhmm)
    except Exception as exc:
        raise HTTPException(422, str(exc)) from exc


def _run_agent(cmd: list[str], *, timeout: float, step: str, **kwargs):
    """subprocess.run wrapper for every stowline-agent.exe/workspace-repo-init.exe
    call in the install flow below. A bare subprocess.run's TimeoutExpired is
    not an HTTPException -- it propagates as an unhandled exception, which
    FastAPI turns into a raw ASGI 500 with no JSON body at all, so the
    browser's api() has nothing to show but the bare status code "500".
    Confirmed live: `secrets provision --mode=service` re-authenticates
    against the real repository (ops.go authenticateRepository -> CatConfig)
    under its own internal 2-minute context timeout, which left this call's
    old 120s budget with no headroom at all -- the two clocks raced and the
    outer one always lost first. Route every step through here instead of
    duplicating the same try/except at each call site."""
    try:
        return subprocess.run(cmd, capture_output=True, text=True, timeout=timeout, creationflags=_NO_WINDOW, **kwargs)
    except subprocess.TimeoutExpired as exc:
        raise HTTPException(504, f"{step} timed out after {timeout:.0f}s") from exc


class CodeIn(BaseModel):
    code: str = Field(max_length=64)


@app.post("/api/local/check-code", dependencies=[LOCAL_API])
def check_code(body: CodeIn):
    """Checks a setup code against the server on the first screen, so a typo
    shows up before the folders are scanned. Does not use the code up."""
    url = (_deploy().get("control_plane_url") or "").rstrip("/")
    if not url:
        raise HTTPException(412, "connect to the server first")
    try:
        with httpx.Client(timeout=15.0, follow_redirects=False) as hx:
            r = hx.post(url + "/api/v1/setup/code-check", json={"code": body.code})
    except httpx.HTTPError as exc:
        raise HTTPException(502, f"could not reach control plane: {exc}") from exc
    if r.status_code == 429:
        raise HTTPException(429, "too many wrong codes; wait a few minutes")
    if r.status_code != 200:
        raise HTTPException(401, "setup code invalid or expired")
    return r.json()


def _cp_auth(hx, base: str, body: "InstallIn"):
    """Signs in to the control plane with the setup code if one was given,
    otherwise with the admin login. Same caller-owned client rule as
    _cp_login."""
    if body.setup_code.strip():
        url = base.rstrip("/")
        r = hx.post(url + "/api/v1/setup/login", json={"code": body.setup_code.strip()})
        if r.status_code != 200:
            raise HTTPException(401, "setup code invalid or expired")
        return url, r.cookies
    if not body.username or not body.password:
        raise HTTPException(422, "a setup code or the admin login is required")
    return _cp_login(hx, base, body.username, body.password)


def _cp_login(hx, base: str, username: str, password: str):
    """`hx` must be a caller-owned, still-open httpx.Client. This function
    must never open or close it: doing so under a local `with` and then
    returning the client handed back a client whose __exit__ had already
    run, so every call after login raised
    'RuntimeError: Cannot send a request, as the client has been closed.'"""
    url = base.rstrip("/")
    r = hx.post(url + "/api/v1/auth/login", json={"username": username, "password": password})
    if r.status_code != 200:
        raise HTTPException(401, "control plane login failed")
    return url, r.cookies


@app.post("/api/local/install", dependencies=[LOCAL_API])
def do_install(body: InstallIn):
    if not body.confirm_install:
        raise HTTPException(422, "explicit Install / Enroll / Enable Backup confirmation required")
    if not _is_admin() and os.name == "nt" and not STATE["no_enroll"]:
        raise HTTPException(403, "Setup Wizard must run elevated")
    sha = ""
    if AGENT.is_file():
        h = hashlib.sha256()
        with open(AGENT, "rb") as fh:
            for chunk in iter(lambda: fh.read(1024 * 1024), b""):
                h.update(chunk)
        sha = h.hexdigest()
        if sha != QUALIFIED:
            raise HTTPException(409, "installed agent hash is not the qualified 0.2.4 binary")
    disc = STATE.get("discovery") or {}
    try:
        dept = normalize_department(body.department, body.custom_department)
        manifest = STATE.get("manifest") or compile_selection(
            selected=body.selected,
            office_files=disc.get("office_files") or {},
            extra_files=disc.get("extra_files") or {},
            sensitive_files=_discovered_sensitive(disc),
        )
        validate_preferred_start(body.site_id, body.preferred_start_hhmm)
    except (SelectionError, ValueError) as exc:
        raise HTTPException(422, str(exc)) from exc

    if not STATE["synthetic"]:
        if not PILOT_JSON.is_file():
            # Do not report a successful install while the operator's chosen
            # folders were never persisted to the config the service reads.
            raise HTTPException(
                500,
                f"agent config missing: {PILOT_JSON}. Run 'stowline-agent.exe config write' "
                "(bootstrap.py should already have done this) before installing.",
            )
        try:
            apply_pilot_source_roots(str(PILOT_JSON), manifest["source_roots"], manifest.get("exclude_paths") or [])
            write_local_manifest(str(MANIFEST), manifest)
        except (ValueError, OSError) as exc:
            # ValueError covers SelectionError and json.JSONDecodeError
            # (json.loads on a corrupted pilot.json) alike.
            # A repair on a machine with a leftover/corrupted pilot.json
            # (crashed prior install attempt) or stale ACLs from an older
            # install must surface as a controlled error, not a raw ASGI
            # 500 with no detail -- these two calls used to run outside
            # any try/except here at all.
            raise HTTPException(500, f"could not persist the selection to {PILOT_JSON}: {exc}") from exc

    result = {
        "source_roots": manifest["source_roots"],
        "selected_file_count": manifest["selected_file_count"],
        "selected_logical_bytes": manifest["selected_logical_bytes"],
        "department": dept,
        "site_id": body.site_id,
        "preferred_start_hhmm": body.preferred_start_hhmm,
        "enrolled": False,
        "service": "",
        "backup_started": False,
        "agent_sha": sha,
    }
    if STATE["no_enroll"] or STATE["synthetic"]:
        result["note"] = (
            "Kurulum tamamlandı.\n"
            "Yedekleme henüz başlatılmadı.\n"
            "İlk yedekleme BT yöneticisi tarafından doğrulandıktan sonra başlatılacak."
        )
        result["backup_started"] = False
        return result

    # A packaged deploy.json pins the verified control-plane URL. If one is
    # pinned, it is the ONLY URL this install may talk to — never a value
    # from the request body, whose preflight check would otherwise pass
    # against the trusted pinned URL while login/enroll silently went
    # somewhere else (an attacker-supplied control_plane_url), sending the
    # operator's real password and the new device's enrollment there.
    dep = dict(_deploy())
    pinned_url = (dep.get("control_plane_url") or "").strip()
    if pinned_url and body.control_plane_url and body.control_plane_url.rstrip("/") != pinned_url.rstrip("/"):
        raise HTTPException(422, "control_plane_url does not match the packaged deploy.json pin")
    effective_url = pinned_url or body.control_plane_url
    try:
        reject_insecure_remote(
            effective_url,
            role="Control plane",
            lab_insecure_http=bool(STATE.get("synthetic")),
        )
        if not pinned_url:
            dep = {
                "control_plane_url": effective_url,
                "gateway_url": (_deploy().get("gateway_url") or ""),
                "allow_localhost": False,
                "lab_insecure_http": bool(STATE.get("synthetic")),
            }
        pf = preflight(dep)
    except NetworkPreflightError as exc:
        raise HTTPException(412, str(exc)) from exc
    if not pf.get("can_enroll"):
        raise HTTPException(412, "; ".join(pf.get("errors") or ["network preflight failed"]))

    import httpx

    device_id = ""
    already_enrolled_device_id = ""
    existing_cp_url = ""
    if PILOT_JSON.is_file():
        try:
            existing = json.loads(PILOT_JSON.read_text(encoding="utf-8-sig"))
            existing_device_id = str(existing.get("device_id") or "")
            existing_cp_url = str(existing.get("control_plane_url") or "")
        except (OSError, ValueError, AttributeError):
            existing_device_id = ""
        if existing_device_id and existing_device_id != "pilot-device":
            already_enrolled_device_id = existing_device_id
    try:
        # One client, opened once, alive for every call below — see
        # _cp_login's docstring for why it must never be closed early.
        with httpx.Client(timeout=15.0, follow_redirects=True) as hx:
            url, cookies = _cp_auth(hx, effective_url, body)
            if already_enrolled_device_id and not _enrollment_still_valid(hx, url, cookies, already_enrolled_device_id, existing_cp_url):
                # A leftover pilot.json from an earlier install -- another
                # server (a previous one), or a device that has since
                # been revoked/quarantined. Reusing it would skip enrolling
                # against this server and leave the PC unable to back up,
                # so enroll fresh; `control enroll` rewrites the identity.
                already_enrolled_device_id = ""
            if already_enrolled_device_id:
                # This PC's own pilot.json already carries a real (non-
                # placeholder) device_id from a prior successful `control
                # enroll`. The control plane (server/app/services.py
                # enroll_agent) creates a brand-new device row for every
                # redeemed enrollment token, with no dedup by hostname -- so
                # minting another token and enrolling again on a retry would
                # leave a duplicate device behind. Reuse the existing
                # enrollment instead of enrolling a second time.
                device_id = already_enrolled_device_id
                result["enrolled"] = True
            else:
                tok = hx.post(
                    url + "/api/v1/admin/enrollment-tokens",
                    json={"label": "setup-wizard", "minutes": 15, "site_id": body.site_id, "department": dept},
                    cookies=cookies,
                )
                if tok.status_code != 200:
                    raise HTTPException(tok.status_code, "could not mint enrollment token")
                token = tok.json()["token"]
                if not AGENT.is_file():
                    raise HTTPException(404, "stowline-agent.exe missing")
                env = os.environ.copy()
                # cmdControl's hand-rolled parser (agent/cmd/stowline-agent/ops.go) only
                # accepts --url/--token as two separate argv entries — unlike --mode,
                # neither has "--flag=value" handling, so that form is silently
                # ignored and url/token come out empty, always hitting the "usage:"
                # branch. Confirmed real end-to-end on this PC before fixing.
                proc = _run_agent(
                    [str(AGENT), "control", "enroll", "--url", url, "--token", token, "--mode=service"],
                    timeout=120,
                    step="service enrollment",
                    env=env,
                )
                if proc.returncode != 0:
                    detail = (proc.stderr or proc.stdout or "").strip()
                    raise HTTPException(500, "service enrollment failed: " + (detail or "token was consumed or aborted by the agent"))
                result["enrolled"] = True
                # `control enroll` just persisted the server-assigned device_id
                # into pilot.json (agent/internal/enroll Bind()) -- read it
                # back directly rather than searching admin/devices by
                # hostname, which can match the wrong row once duplicate-
                # hostname devices exist from earlier enrollments.
                try:
                    device_id = str(json.loads(PILOT_JSON.read_text(encoding="utf-8")).get("device_id") or "")
                except (OSError, ValueError):
                    device_id = ""
            if device_id:
                hx.patch(
                    url + f"/api/v1/admin/devices/{device_id}",
                    json={
                        "department": dept,
                        "preferred_start_hhmm": body.preferred_start_hhmm,
                        "agent_sha": sha,
                        "service_state": "installed",
                        **({"display_name": body.display_name.strip()} if body.display_name.strip() else {}),
                    },
                    cookies=cookies,
                )
                hx.post(
                    url + f"/api/v1/admin/devices/{device_id}/selection",
                    json={"selected": body.selected, "preferred_start_hhmm": body.preferred_start_hhmm, "applied_locally": True},
                    cookies=cookies,
                )
    except httpx.RequestError as exc:
        # A real network/DNS/timeout failure talking to the control plane.
        # Must surface as a controlled wizard error, never an unhandled
        # exception -> raw ASGI 500 traceback.
        raise HTTPException(502, f"could not reach control plane: {exc}") from exc

    # A genuinely fresh device has no restic repository yet -- only a
    # control-plane device record (just enrolled, above) and a gateway
    # route. workspace-repo-init is the agent's own existing, unmodified
    # tool for exactly this: it tries restic init, and on any failure
    # (including "already initialized") falls back to reading the
    # existing repository's config instead -- so it is already safe to
    # run unconditionally, on a brand new repository, an already-
    # initialized one from a prior/interrupted setup, or a retried
    # install. It touches only the repository, never the control-plane
    # enrollment, so retrying this step alone can never create a
    # duplicate device. Must run before `secrets provision --mode=service`
    # below, which re-authenticates the current password against the
    # repository and therefore requires the repository to already exist --
    # confirmed exact failure otherwise: "repository does not exist".
    repo_init = _run_agent([str(WORKSPACE_REPO_INIT)], timeout=1800, step="repository initialization")
    if repo_init.returncode != 0:
        detail = (repo_init.stderr or repo_init.stdout or "").strip()
        raise HTTPException(500, "repository initialization failed: " + (detail or "workspace-repo-init failed"))

    # `control enroll --mode=service` (above) only machine-scopes the
    # control-plane and gateway-REST credentials (agent/internal/enroll
    # Bind()/writeEnvelopes()) — it never touches PasswordRef, the restic
    # REPOSITORY password. That field is bootstrapped by `config write`
    # (run once by bootstrap.py on first layout) using pilot/dpapi-user
    # scope, since a fresh PC has no service enrollment yet. Left
    # unprovisioned, the LocalSystem-run service's own preflight
    # (agent/internal/pilot/preflight RequireServiceProvider) permanently
    # refuses to start: "service mode requires dpapi-machine, not
    # dpapi-user" — found on a real external PC. `secrets provision
    # --mode=service` is the agent's own existing, tested re-wrap (
    # ReWrapToMachine): it re-authenticates the current password against
    # the real repository and only then promotes it to a machine-scope
    # envelope LocalSystem can open, leaving the original intact on any
    # failure. Must run before `service install` so the service is never
    # left installed with secrets it cannot use.
    # 180s, not 120s: this re-authenticates against the real repository
    # (ops.go authenticateRepository -> CatConfig) under its own internal
    # 2-minute context timeout -- see _run_agent's docstring for the exact
    # race a matching 120s outer budget lost to, live, on this PC.
    prov = _run_agent(
        [str(AGENT), "secrets", "provision", "--mode=service"],
        # 600s: provisioning checks the password against the repository
        # three times (source, temporary and final envelope), each a full
        # restic open through the Drive gateway -- measured live at ~60s
        # each, which ran out a 180s budget on a fresh install.
        timeout=600,
        step="repository secret provisioning",
    )
    if prov.returncode != 0:
        detail = (prov.stderr or prov.stdout or "").strip()
        raise HTTPException(500, "repository secret provisioning failed: " + (detail or "secrets provision --mode=service failed"))

    # Phase 8 gate: install the service and stop there. Starting it is a
    # separate, explicit operator action (see start_first_backup below) —
    # do_install() must never be the thing that puts a device on the WAN.
    inst = _run_agent([str(AGENT), "service", "install"], timeout=60, step="service install")
    service_ok = inst.returncode == 0
    result["service"] = "installed" if service_ok else "failed"
    result["service_state"] = "Stopped" if service_ok else "not_installed"
    result["backup_started"] = False
    result["first_backup_requires_operator_action"] = True
    result["device_id"] = device_id
    if service_ok:
        result["note"] = (
            "Kurulum tamamlandı.\n"
            "Servis kuruldu, durumu: Durduruldu (Stopped).\n"
            "Yedekleme henüz başlatılmadı.\n"
            "İlk yedekleme, BT yöneticisi bu ekrandan onaylayıp başlattıktan sonra çalışacak."
        )
    else:
        # Enrollment, repository init, and secrets provisioning above all
        # succeeded -- only the service registration itself failed, most
        # likely a leftover registration from a prior install on a repair
        # machine. Reporting success here (as this used to, unconditionally)
        # would tell the operator a device is ready to back up when its
        # service was never actually installed.
        detail = (inst.stderr or inst.stdout or "").strip()
        result["note"] = (
            "Kayıt/depolama adımları tamamlandı, ama SERVİS KURULAMADI.\n"
            "Muhtemel sebep: eski bir StowlineBackup servis kaydı hâlâ duruyor.\n"
            "Yönetici olarak şunu çalıştırıp tekrar deneyin:\n"
            f"  {AGENT} service remove\n"
            "sonra sihirbazı yeniden başlatın.\n"
            f"Ayrıntı: {detail or 'service install failed'}"
        )
    _ = httpx
    return result


class StartBackupIn(BaseModel):
    confirm_start: bool = False


def _enrollment_still_valid(hx, url: str, cookies, device_id: str, recorded_cp_url: str) -> bool:
    """A pilot.json device_id is reusable only for a retry on this very
    server while that device is still ACTIVE there."""
    if not recorded_cp_url or recorded_cp_url.rstrip("/") != url.rstrip("/"):
        return False
    r = hx.get(url + f"/api/v1/admin/devices/{device_id}", cookies=cookies)
    if r.status_code != 200:
        return False
    try:
        lifecycle = str((r.json().get("device") or {}).get("lifecycle") or "")
    except ValueError:
        return False
    return lifecycle.upper() == "ACTIVE"


@app.post("/api/local/start-first-backup", dependencies=[LOCAL_API])
def start_first_backup(body: StartBackupIn):
    """The only code path allowed to start the StowlineBackup service after
    do_install(). Requires the same per-run wizard token plus its own
    explicit confirmation, so the first backup cannot begin merely because
    Setup finished — an operator must come back to this screen and choose
    to start it."""
    if not body.confirm_start:
        raise HTTPException(422, "explicit start confirmation required")
    if not _is_admin() and os.name == "nt":
        raise HTTPException(403, "Setup Wizard must run elevated")
    if not AGENT.is_file():
        raise HTTPException(404, "stowline-agent.exe missing")
    start = _run_agent([str(AGENT), "service", "start"], timeout=60, step="service start")
    if start.returncode != 0 and "already running" in ((start.stderr or "") + (start.stdout or "")).lower():
        # A second click (or a retry after a slow first start) must not show
        # an error, and must not queue a second backup either: the first
        # click already started the service and asked for the backup.
        return {"service": "started", "service_state": "Running", "backup_started": True, "message": "İlk yedekleme zaten başlatıldı."}
    if start.returncode != 0:
        raise HTTPException(500, "service start failed: " + (start.stderr.strip() or start.stdout.strip() or "unknown error"))
    # Starting the service alone does not back anything up: the agent waits
    # for the scheduled time. Ask the running agent for a backup now, the
    # same way Stowline Backups's "Şimdi yedekle" does (it queues a
    # RUN_BACKUP on the control plane with the device's own credential).
    queued, why = _request_backup_now()
    if queued:
        return {"service": "started", "service_state": "Running", "backup_started": True, "message": "İlk yedekleme başladı."}
    return {
        "service": "started",
        "service_state": "Running",
        "backup_started": False,
        "message": "Hizmet çalışıyor, ancak ilk yedek şimdi başlatılamadı (" + why + "). Seçilen saatte kendiliğinden başlayacak.",
    }


LOCAL_AGENT = "http://127.0.0.1:18080"


def _request_backup_now(wait_seconds: int = 90) -> tuple[bool, str]:
    import httpx

    headers = {"X-Stowline-Local": "1", "Content-Type": "application/json"}
    with httpx.Client(timeout=15.0) as hx:
        deadline = time.time() + wait_seconds
        while True:  # the service needs a few seconds before its local API answers
            try:
                if hx.get(LOCAL_AGENT + "/api/status").status_code == 200:
                    break
            except httpx.RequestError:
                pass
            if time.time() > deadline:
                return False, "hizmet yanıt vermedi"
            time.sleep(2)
        try:
            r = hx.post(LOCAL_AGENT + "/api/backup-now", json={}, headers=headers)
        except httpx.RequestError as exc:
            return False, str(exc)
        if r.status_code in (200, 202):
            return True, ""
        try:
            detail = str(r.json().get("error") or r.status_code)
        except ValueError:
            detail = str(r.status_code)
        return False, detail


if STATIC.exists():
    app.mount("/static", StaticFiles(directory=STATIC), name="static")


def _synthetic_tree(root: Path) -> KnownFolders:
    desk = root / "OneDrive" / "Desktop"
    docs = root / "OneDrive" / "Belgeler"
    pics = root / "OneDrive" / "Resimler"
    downs = root / "Downloads"
    outlook = docs / "Outlook Dosyaları"
    for p in (desk, docs, pics, downs, outlook):
        p.mkdir(parents=True, exist_ok=True)
    (docs / "invoice-form.xlsx").write_bytes(b"x" * 200)
    (docs / "fatura.pdf").write_bytes(b"y" * 100)
    (outlook / "office@example.com.pst").write_bytes(b"pst" * 50)
    (root / "AppData" / "Local" / "Microsoft" / "Outlook").mkdir(parents=True, exist_ok=True)
    (root / "AppData" / "Local" / "Microsoft" / "Outlook" / "cache.ost").write_bytes(b"ost" * 20)
    (desk / ".env").write_text("DEMO=1\n", encoding="utf-8")
    return KnownFolders(profile=str(root), desktop=str(desk), documents=str(docs), pictures=str(pics), downloads=str(downs), redirected={"desktop": True, "documents": True})


def main() -> None:
    parser = argparse.ArgumentParser(description="Stowline first-run Setup Wizard")
    parser.add_argument("--synthetic", action="store_true", help="Use a temp tree only. Never scan or enroll a real PC.")
    parser.add_argument("--no-enroll", action="store_true", help="Do not call enroll or start the service.")
    parser.add_argument("--port", type=int, default=18765)
    parser.add_argument("--no-browser", action="store_true")
    parser.add_argument("--deploy", default="", help="Path to deploy.json")
    args = parser.parse_args()
    STATE["synthetic"] = args.synthetic
    STATE["no_enroll"] = args.no_enroll or args.synthetic
    # Stowline Setup.exe generates the token itself so it can open the
    # wizard in its own window without reading this process's stdout.
    env_token = os.environ.get("STOWLINE_WIZARD_TOKEN", "")
    STATE["token"] = env_token if len(env_token) >= 32 else secrets.token_urlsafe(32)
    deploy_path = args.deploy or os.environ.get("STOWLINE_DEPLOY_JSON") or ""
    if not deploy_path:
        for cand in (
            Path(__file__).resolve().parents[1] / "deploy.json",
            Path(__file__).resolve().parents[2] / "deploy.json",
            Path.cwd() / "deploy.json",
        ):
            if cand.is_file():
                deploy_path = str(cand)
                break
    STATE["deploy"] = load_deploy(deploy_path) if deploy_path else load_deploy("")
    if args.synthetic:
        root = Path(os.environ.get("TEMP") or "/tmp") / "stowline-setup-synthetic"
        if root.exists():
            pass
        STATE["known"] = _synthetic_tree(root)
    if os.name == "nt" and not _is_admin() and not args.synthetic:
        print("Setup Wizard must run elevated (UAC). Re-run from an Administrator PowerShell.")
    import threading

    import uvicorn

    # webbrowser.open() used to fire immediately before the blocking
    # uvicorn.run() call below even started binding the port -- a real,
    # unsynchronized race: whichever was faster on a given run decided
    # whether the browser's first request landed on a live listener or
    # got ERR_CONNECTION_REFUSED / "127.0.0.1 refused to connect", with
    # no way to tell in advance which one would happen. Confirmed live.
    # Start the server in a background thread, wait for it to actually
    # report started, then open the browser, then block on the thread --
    # this keeps main()'s overall blocking behavior (bootstrap.py's
    # subprocess.call waits for this process to exit) unchanged.
    server = uvicorn.Server(uvicorn.Config(app, host="127.0.0.1", port=args.port, log_level="info"))
    thread = threading.Thread(target=server.run, daemon=True)
    thread.start()
    for _ in range(100):
        if server.started:
            break
        time.sleep(0.1)
    wizard_url = f"http://127.0.0.1:{args.port}/?t={STATE['token']}"
    # Only reachable from this machine, and only for this process's
    # lifetime -- safe to print unconditionally. bootstrap.py already
    # redirects this process's stdout to cache\wizard-stdio.log, so this
    # is the only way to recover the token if the auto-opened browser
    # tab is lost (closed, wrong browser focused, remote session drop)
    # or, same as before, when --no-browser was passed deliberately.
    print(f"Stowline Setup Wizard listening: {wizard_url}")
    if not args.no_browser:
        webbrowser.open(wizard_url)
    thread.join()


if __name__ == "__main__":
    main()
