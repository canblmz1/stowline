"""Isolated restic REST gateway.

Endpoint credentials map to exactly one device/generation directory.
Append-only: create new objects, delete only locks. Overwrite and
destructive delete of data/index/snapshots/keys/config are rejected.

This is NOT immutability. rclone/Drive credentials never go to the endpoint.
"""

from __future__ import annotations

import hashlib
import hmac
import json
import os
import re
import unicodedata
from pathlib import Path

from fastapi import FastAPI, HTTPException, Request, Response
from fastapi.concurrency import run_in_threadpool
from fastapi.responses import FileResponse, PlainTextResponse

from gw.http_range import RangeUnsatisfiable, parse_byte_range
from gw.limiter import read_limited_body
from gw.store import LocalStore, store_from_root

ID_RE = re.compile(r"^[a-fA-F0-9-]{8,64}$")
OBJECT_RE = re.compile(r"^[a-fA-F0-9]+$")
TYPES = {"data", "index", "keys", "locks", "snapshots"}


def _safe_storage_prefix(raw: str | None) -> str:
    """Validates a registry-stored storage_prefix before it is ever used
    as a store path segment -- defense in depth on top of services.py's
    own sanitizing, in case registry.json is ever hand-edited or written
    by something else. Rejects anything with an empty/'.'/'..' segment or
    a backslash; the caller falls back to the immutable device_id, the
    same value this whole feature exists to make less of the visible
    namespace, on any rejection."""
    if not raw or "\\" in raw:
        return ""
    parts = raw.strip("/").split("/")
    if not parts or any(p in ("", ".", "..") for p in parts):
        return ""
    return "/".join(parts)
RESTIC_REST_V2 = "application/vnd.x.restic.rest.v2"


def hash_secret(value: str) -> str:
    salt = b"stowline-gateway-v1"
    # registry entries already store pbkdf2 hashes from the control plane;
    # tests inject plaintext maps hashed here with a dedicated format.
    dk = hashlib.pbkdf2_hmac("sha256", value.encode(), salt, 120_000)
    return "g1$" + dk.hex()


def _admin_token_ok(request: Request) -> bool:
    """Constant-time admin bearer-token check.

    A plain `!=` string compare short-circuits on the first differing byte,
    which turns the shared STOWLINE_GATEWAY_ADMIN_TOKEN into a remote timing
    oracle across enough requests. hmac.compare_digest does not.
    """
    token = os.environ.get("STOWLINE_GATEWAY_ADMIN_TOKEN", "")
    auth = request.headers.get("authorization") or ""
    if not token:
        return False
    return hmac.compare_digest(auth.encode("utf-8"), f"Bearer {token}".encode("utf-8"))


def verify_secret(value: str, stored: str) -> bool:
    if len(stored) == 64 and all(c in "0123456789abcdef" for c in stored):
        return hmac.compare_digest(hashlib.sha256(value.encode("utf-8")).hexdigest(), stored)
    if stored.startswith("g1$"):
        return hmac.compare_digest(hash_secret(value), stored)
    try:
        kind, salt_hex, dk_hex = stored.split("$", 2)
        if kind != "pbkdf2":
            return False
        dk = hashlib.pbkdf2_hmac("sha256", value.encode(), bytes.fromhex(salt_hex), 200_000)
        return hmac.compare_digest(dk.hex(), dk_hex)
    except ValueError:
        return False


class Registry:
    """device_id -> {password_hash, generation_id, revoked}"""

    def __init__(self, path: Path | None):
        self.path = path
        self.memory: dict[str, dict] = {}

    def load(self) -> dict[str, dict]:
        if self.memory:
            return self.memory
        if self.path and self.path.exists():
            return json.loads(self.path.read_text(encoding="utf-8"))
        return {}

    def put(self, device_id: str, generation_id: str, password: str, revoked: bool = False, site_id: str = "") -> None:
        self.put_record(device_id, generation_id, hash_secret(password), revoked, site_id)

    def put_record(
        self,
        device_id: str,
        generation_id: str,
        password_hash: str,
        revoked: bool = False,
        site_id: str = "",
        storage_prefix: str = "",
    ) -> None:
        data = self.load()
        rec = {"generation_id": generation_id, "password_hash": password_hash, "revoked": revoked}
        if site_id:
            rec["site_id"] = site_id
        elif device_id in data and data[device_id].get("site_id"):
            rec["site_id"] = data[device_id]["site_id"]
        # Carry forward like site_id above: a revoke/renew call that
        # doesn't pass storage_prefix must not blank out an existing
        # device's namespace, and a department edit after enrollment
        # never calls this at all (see services.py's notify_gateway
        # callers) so there is nothing to silently relocate here either.
        if storage_prefix:
            rec["storage_prefix"] = storage_prefix
        elif device_id in data and data[device_id].get("storage_prefix"):
            rec["storage_prefix"] = data[device_id]["storage_prefix"]
        data[device_id] = rec
        self.memory = data
        if self.path:
            self.path.parent.mkdir(parents=True, exist_ok=True)
            self.path.write_text(json.dumps(data), encoding="utf-8")

    def lookup(self, device_id: str, password: str) -> dict | None:
        rec = self.load().get(device_id)
        if rec is None or rec.get("revoked"):
            return None
        if not verify_secret(password, rec["password_hash"]):
            return None
        return rec


def create_app(root: Path, registry: Registry, site_limits: dict | None = None) -> FastAPI:
    store = LocalStore(root)
    return create_app_with_store(store, registry, site_limits)


def create_app_with_store(store, registry: Registry, site_limits: dict | None = None) -> FastAPI:
    from gw.limiter import SiteLimiter
    from gw.usage import UsageMeter

    app = FastAPI(title="Stowline backup gateway", version="0.1.1")
    app.state.store = store
    app.state.registry = registry
    app.state.limiter = SiteLimiter(site_limits or {})
    app.state.usage = UsageMeter(store, registry, _safe_storage_prefix)

    @app.get("/health")
    def health():
        return {"status": "ok", "component": "gateway", "store": getattr(store, "kind", "local")}

    @app.post("/admin/register")
    async def admin_register(request: Request):
        if not _admin_token_ok(request):
            raise HTTPException(401, "admin token")
        body = await request.json()
        app.state.registry.put_record(
            str(body["device_id"]),
            str(body["generation_id"]),
            str(body["password_hash"]),
            bool(body.get("revoked")),
            str(body.get("site_id") or ""),
            str(body.get("storage_prefix") or ""),
        )
        return {"ok": True}

    @app.post("/admin/site-limits")
    async def admin_site_limits(request: Request):
        if not _admin_token_ok(request):
            raise HTTPException(401, "admin token")
        body = await request.json()
        site_id = str(body.get("site_id") or "")
        rate = float(body.get("bytes_per_sec") or 0)
        if not site_id or rate <= 0:
            raise HTTPException(422, "site_id and positive bytes_per_sec required")
        app.state.limiter.set_rate(site_id, rate)
        return {"ok": True, "site_id": site_id, "bytes_per_sec": rate}

    @app.get("/admin/ingress")
    async def admin_ingress(request: Request):
        if not _admin_token_ok(request):
            raise HTTPException(401, "admin token")
        return app.state.limiter.snapshot()

    @app.get("/admin/usage")
    async def admin_usage(request: Request, wait: int = 0):
        # Object sizes only (no restic password involved), measured in the
        # background and cached; wait=1 blocks until a first measurement exists.
        if not _admin_token_ok(request):
            raise HTTPException(401, "admin token")
        from starlette.concurrency import run_in_threadpool

        return await run_in_threadpool(app.state.usage.snapshot, bool(wait))

    @app.get("/admin/snapshots/{device_id}/{generation_id}")
    async def admin_list_snapshots(device_id: str, generation_id: str, request: Request):
        # Read-only, credential-free by design: restic's REST protocol
        # already exposes an object's existence and its id (the id IS the
        # filename restic gives an encrypted snapshot object) without ever
        # decrypting anything, so listing here needs no restic password --
        # only the gateway's own admin token. This is what lets the control
        # plane, which never holds a repository password, reconcile its own
        # snapshot rows against what actually exists in the repository.
        if not _admin_token_ok(request):
            raise HTTPException(401, "admin token")
        if not ID_RE.match(device_id) or not ID_RE.match(generation_id):
            raise HTTPException(400, "invalid identity")
        # The real storage path is keyed by the registered storage_prefix
        # (e.g. "IT-QUALIFICATION/pc01--87de10fa"), not the raw
        # device_id -- restic_proxy's handle() resolves the same way.
        # Missing that here silently pointed every reconciliation lookup
        # at an empty, never-written path and made it look like every
        # single recorded snapshot had vanished from the repository.
        # Confirmed live against a real device: repo_snapshot_count=0,
        # db_snapshot_count=6, before this fix.
        rec = app.state.registry.load().get(device_id) or {}
        prefix = _safe_storage_prefix(rec.get("storage_prefix")) or device_id
        repo = app.state.store.prepare_repo(prefix, generation_id)
        key = app.state.store.resolve(repo, "snapshots")
        names = app.state.store.list_names(key)  # [] for a repo with no snapshots yet, never raises
        ids = [n for n in names if ID_RE.match(n)]
        return {"device_id": device_id, "generation_id": generation_id, "snapshot_ids": ids}

    @app.api_route("/restic/{device_id}/{generation_id}/{full_path:path}", methods=["GET", "HEAD", "POST", "DELETE"])
    async def restic_proxy(device_id: str, generation_id: str, full_path: str, request: Request):
        return await handle(app, request, device_id, generation_id, full_path)

    @app.api_route("/restic/{device_id}/{generation_id}", methods=["GET", "HEAD", "POST", "DELETE"])
    async def restic_root(device_id: str, generation_id: str, request: Request):
        return await handle(app, request, device_id, generation_id, "")

    return app


async def handle(app: FastAPI, request: Request, device_id: str, generation_id: str, rest_path: str):
    if not ID_RE.match(device_id) or not ID_RE.match(generation_id):
        raise HTTPException(400, "invalid identity")
    auth = _basic(request)
    if auth is None:
        return Response(status_code=401, headers={"WWW-Authenticate": 'Basic realm="stowline"'})
    user, password = auth
    if user != device_id:
        raise HTTPException(403, "identity mismatch")
    rec = app.state.registry.lookup(user, password)
    if rec is None:
        raise HTTPException(401, "invalid credentials")
    if rec["generation_id"] != generation_id:
        raise HTTPException(403, "generation mismatch")

    rest_path = unicodedata.normalize("NFKC", rest_path.replace("\\", "/")).lstrip("/")
    if ".." in rest_path.split("/") or rest_path.startswith("/") or "//" in rest_path:
        raise HTTPException(400, "path rejected")
    if any(p in (".", "..") or ".." in p for p in rest_path.split("/")):
        raise HTTPException(400, "path rejected")
    # unicode homoglyph dots already covered by .. check after NFC-ish split
    if any(ord(ch) < 32 for ch in rest_path):
        raise HTTPException(400, "path rejected")

    # The URL/auth identity (device_id/generation_id, checked above) never
    # changes -- only where THIS device's data actually lands on the
    # store can move, and only via a prefix this server itself computed
    # and wrote into the registry at enrollment time (services.py's
    # compute_storage_prefix), never from anything in this request. A
    # device pre-dating this feature has no storage_prefix in the
    # registry at all, so it keeps resolving to the same device_id path
    # it always has.
    prefix = _safe_storage_prefix(rec.get("storage_prefix")) or device_id
    store = app.state.store
    repo = await run_in_threadpool(store.prepare_repo, prefix, generation_id)
    target = store.resolve(repo, rest_path)

    method = request.method.upper()
    if method in {"PUT", "PATCH", "TRACE", "CONNECT"}:
        raise HTTPException(405, "method not allowed")

    parts = [p for p in rest_path.split("/") if p]
    if method == "DELETE":
        if not parts:
            raise HTTPException(403, "destructive delete blocked")
        if parts[0] != "locks":
            raise HTTPException(403, "append-only: delete blocked")
        if await run_in_threadpool(store.delete_file, target):
            return Response(status_code=200)
        raise HTTPException(404, "not found")

    if method == "POST":
        if not parts:
            # restic REST Create() POSTs /?create=true before writing config/keys.
            if request.query_params.get("create") == "true":
                for name in TYPES:
                    await run_in_threadpool(store.mkdir, store.resolve(repo, name))
                return Response(status_code=200)
            raise HTTPException(400, "object path required")
        if parts[0] not in TYPES and parts[0] != "config":
            raise HTTPException(400, "unknown object class")
        if len(parts) >= 2 and parts[0] in TYPES and not OBJECT_RE.match(parts[-1]):
            raise HTTPException(400, "invalid object name")
        site_id = str(rec.get("site_id") or "")
        body = await read_limited_body(request, app.state.limiter, site_id, device_id)
        declared = request.headers.get("content-length")
        if declared not in (None, ""):
            try:
                if int(declared) != len(body):
                    raise HTTPException(400, "content-length mismatch")
            except ValueError as exc:
                raise HTTPException(400, "invalid content-length") from exc
        await run_in_threadpool(store.write_exclusive, target, body)
        return Response(status_code=200)

    looks_like_object = _is_object_path(parts, rest_path)
    if looks_like_object:
        if not await run_in_threadpool(store.is_file, target):
            raise HTTPException(404, "not found")
        return await _file_response(store, target, request)

    if not rest_path or rest_path.endswith("/") or await run_in_threadpool(store.is_dir, target):
        return await _listing_response(store, target, rest_path, request)
    if not await run_in_threadpool(store.is_file, target):
        raise HTTPException(404, "not found")
    return await _file_response(store, target, request)


async def _file_response(store, target, request: Request) -> Response:
    # Local files: Starlette FileResponse implements RFC 7233 the same way rest-server
    # uses ServeContent (206 + Content-Range for Range: bytes=0-), and streams the
    # file itself via Starlette's own async file I/O -- not blocking here.
    if isinstance(target, Path):
        return FileResponse(
            target,
            media_type="application/octet-stream",
            stat_result=await run_in_threadpool(target.stat),
        )
    size = await run_in_threadpool(store.file_size, target)
    method = request.method.upper()
    if method == "HEAD" and not request.headers.get("range"):
        return Response(
            status_code=200,
            media_type="application/octet-stream",
            headers={"Accept-Ranges": "bytes", "Content-Length": str(size)},
        )
    try:
        rng = parse_byte_range(request.headers.get("range"), size)
    except RangeUnsatisfiable as exc:
        return Response(status_code=416, headers={"Content-Range": f"bytes */{exc.size}", "Accept-Ranges": "bytes"})
    if rng is None:
        body = await run_in_threadpool(store.read_bytes, target)
        return Response(
            content=body,
            media_type="application/octet-stream",
            headers={"Content-Length": str(len(body)), "Accept-Ranges": "bytes"},
        )
    start, end = rng
    if hasattr(store, "read_range"):
        slice_ = await run_in_threadpool(store.read_range, target, start, end)
    else:
        full = await run_in_threadpool(store.read_bytes, target)
        slice_ = full[start : end + 1]
    return Response(
        content=slice_,
        status_code=206,
        media_type="application/octet-stream",
        headers={
            "Content-Length": str(len(slice_)),
            "Content-Range": f"bytes {start}-{end}/{size}",
            "Accept-Ranges": "bytes",
        },
    )


def _is_object_path(parts: list[str], rest_path: str) -> bool:
    if not parts or rest_path.endswith("/"):
        return False
    if parts == ["config"]:
        return True
    return parts[0] in TYPES and bool(OBJECT_RE.match(parts[-1]))


async def _listing_response(store, target, rest_path: str, request: Request) -> Response:
    parts = [p for p in rest_path.split("/") if p]
    recursive = bool(parts) and parts[0] == "data"
    infos = await run_in_threadpool(store.list_infos, target, recursive=recursive)
    accept = (request.headers.get("accept") or "").split(",")[0].strip()
    if accept == RESTIC_REST_V2:
        # Exact v2 type — restic compares Content-Type with == and rejects charset=utf-8.
        payload = json.dumps(infos, separators=(",", ":")).encode("ascii")
        return Response(content=payload, headers={"Content-Type": RESTIC_REST_V2, "Content-Length": str(len(payload))})
    names = [item["name"] for item in infos]
    text = "\n".join(names) + ("\n" if names else "")
    return PlainTextResponse(text)


def _basic(request: Request) -> tuple[str, str] | None:
    header = request.headers.get("authorization") or ""
    if not header.lower().startswith("basic "):
        return None
    import base64

    try:
        raw = base64.b64decode(header.split(" ", 1)[1]).decode("utf-8")
        user, pw = raw.split(":", 1)
        return user, pw
    except Exception:
        return None


def _site_limits_from_env() -> dict:
    raw = os.environ.get("STOWLINE_GATEWAY_SITE_LIMITS", "").strip()
    if not raw:
        return {}
    data = json.loads(raw)
    return {str(k): float(v) for k, v in data.items() if float(v) > 0}


def run():
    import uvicorn

    raw = os.environ.get("STOWLINE_GATEWAY_ROOT", "./var/gateway-store")
    store = store_from_root(raw)
    registry = Registry(Path(os.environ.get("STOWLINE_GATEWAY_REGISTRY", "./var/gateway-registry.json")))
    application = create_app_with_store(store, registry, _site_limits_from_env())
    host = os.environ.get("STOWLINE_GATEWAY_HOST", "0.0.0.0").strip() or "0.0.0.0"
    port = int(os.environ.get("PORT") or os.environ.get("STOWLINE_GATEWAY_PORT", "8081"))
    uvicorn.run(application, host=host, port=port, proxy_headers=True, forwarded_allow_ips="*")


def _module_app():
    raw = os.environ.get("STOWLINE_GATEWAY_ROOT", "./var/gateway-store")
    if raw.startswith("rclone:"):
        # Import-time app is local-only; rclone store is constructed in run().
        raw = "./var/gateway-store"
    return create_app(Path(raw), Registry(Path(os.environ.get("STOWLINE_GATEWAY_REGISTRY", "./var/gateway-registry.json"))))


app = _module_app()
