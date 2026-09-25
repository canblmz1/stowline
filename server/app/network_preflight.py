"""Reachability checks for a *remote* endpoint. Loopback is never a valid production target."""

from __future__ import annotations

import json
import ssl
from urllib.error import URLError
from urllib.parse import urlparse
from urllib.request import Request, urlopen

LOOPBACK_HOSTS = frozenset({"localhost", "127.0.0.1", "::1", "[::1]", "0.0.0.0", "[::]"})


class NetworkPreflightError(ValueError):
    pass


def hostname_of(url: str) -> str:
    parsed = urlparse((url or "").strip())
    host = (parsed.hostname or "").lower().strip("[]")
    return host


def scheme_of(url: str) -> str:
    return (urlparse((url or "").strip()).scheme or "").lower()


def is_loopback_url(url: str) -> bool:
    host = hostname_of(url)
    if not host:
        return False
    if host in LOOPBACK_HOSTS:
        return True
    if host.startswith("127."):
        return True
    return False


def reject_loopback(url: str, *, role: str) -> None:
    if is_loopback_url(url):
        raise NetworkPreflightError(
            f"{role} address cannot be localhost/127.0.0.1 on a different physical PC. "
            "Set a stable LAN or DNS name in deploy.json."
        )


def reject_insecure_remote(url: str, *, role: str, lab_insecure_http: bool = False) -> None:
    """Real remote endpoints must be HTTPS. Plain HTTP is lab/synthetic only."""
    reject_loopback(url, role=role)
    scheme = scheme_of(url)
    if scheme == "https":
        return
    if scheme == "http" and lab_insecure_http:
        return
    if scheme == "http":
        raise NetworkPreflightError(
            f"{role} must use HTTPS for a real remote endpoint. "
            "http:// is allowed only in explicit synthetic/lab mode (lab_insecure_http)."
        )
    raise NetworkPreflightError(f"{role} URL must start with https://")


def _ssl_context() -> ssl.SSLContext:
    # System/Windows trust store only. Never disable verification.
    return ssl.create_default_context()


def _get_json(url: str, timeout: float = 5.0) -> dict:
    req = Request(url, method="GET", headers={"Accept": "application/json"})
    try:
        ctx = _ssl_context() if scheme_of(url) == "https" else None
        with urlopen(req, timeout=timeout, context=ctx) as resp:  # noqa: S310 — operator-configured URL
            raw = resp.read().decode("utf-8", errors="replace")
            status = getattr(resp, "status", 200)
    except URLError as exc:
        raise NetworkPreflightError(f"cannot reach {url}: {exc.reason if hasattr(exc, 'reason') else exc}") from exc
    except TimeoutError as exc:
        raise NetworkPreflightError(f"timeout reaching {url}") from exc
    if status >= 400:
        raise NetworkPreflightError(f"{url} returned HTTP {status}")
    try:
        data = json.loads(raw or "{}")
    except json.JSONDecodeError as exc:
        raise NetworkPreflightError(f"{url} did not return JSON") from exc
    return data if isinstance(data, dict) else {}


def check_control_plane(url: str, *, lab_insecure_http: bool = False) -> dict:
    reject_insecure_remote(url, role="Control plane", lab_insecure_http=lab_insecure_http)
    base = url.rstrip("/")
    health = _get_json(base + "/health")
    ready = _get_json(base + "/ready")
    wan_ready = bool(ready.get("wan_ready") if "wan_ready" in ready else health.get("wan_ready"))
    registration = str(ready.get("gateway_registration") or health.get("gateway_registration") or "")
    if not wan_ready:
        raise NetworkPreflightError("control plane wan_ready is not true; enrollment is blocked")
    if registration != "configured":
        raise NetworkPreflightError(
            f"control plane gateway_registration={registration or 'missing'}; expected configured"
        )
    return {
        "ok": True,
        "url": base,
        "wan_ready": True,
        "gateway_registration": registration,
        "health": health,
        "ready": ready,
    }


def check_gateway(url: str, *, lab_insecure_http: bool = False) -> dict:
    reject_insecure_remote(url, role="Backup gateway", lab_insecure_http=lab_insecure_http)
    base = url.rstrip("/")
    health = _get_json(base + "/health")
    if str(health.get("status") or "") != "ok":
        raise NetworkPreflightError(f"gateway status={health.get('status')!r}; expected ok")
    store = str(health.get("store") or "")
    if store != "rclone":
        raise NetworkPreflightError(f"gateway store={store or 'missing'}; expected rclone")
    return {
        "ok": True,
        "url": base,
        "status": "ok",
        "store": store,
        "component": health.get("component"),
        "health": health,
    }


def load_deploy(path: str) -> dict:
    from pathlib import Path

    p = Path(path)
    if not p.is_file():
        return {
            "schema_version": 1,
            "site_id": "hq",
            "control_plane_url": "",
            "gateway_url": "",
            "allow_localhost": False,
            "lab_insecure_http": False,
        }
    data = json.loads(p.read_text(encoding="utf-8"))
    if not isinstance(data, dict):
        raise NetworkPreflightError("deploy.json is not an object")
    return data


def preflight(deploy: dict) -> dict:
    if deploy.get("allow_localhost"):
        raise NetworkPreflightError("allow_localhost is forbidden for a remote endpoint package")
    lab = bool(deploy.get("lab_insecure_http") or deploy.get("synthetic"))
    cp = str(deploy.get("control_plane_url") or "").strip()
    gw = str(deploy.get("gateway_url") or "").strip()
    errors: list[str] = []
    control = {"ok": False}
    gateway = {"ok": False}
    if not cp:
        errors.append("control_plane_url is missing from deploy.json")
    else:
        try:
            control = check_control_plane(cp, lab_insecure_http=lab)
        except NetworkPreflightError as exc:
            errors.append(str(exc))
    if not gw:
        errors.append("gateway_url is missing from deploy.json")
    else:
        try:
            gateway = check_gateway(gw, lab_insecure_http=lab)
        except NetworkPreflightError as exc:
            errors.append(str(exc))
    ok = not errors and bool(control.get("ok")) and bool(gateway.get("ok"))
    google = "rclone" if gateway.get("store") == "rclone" else "unavailable"
    return {
        "ok": ok,
        "control_plane": control,
        "gateway": gateway,
        "google_backend": google,
        "errors": errors,
        "can_enroll": ok,
    }
