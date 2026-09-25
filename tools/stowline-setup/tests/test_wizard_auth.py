"""Regression: the Setup Wizard's local HTTP API ran elevated with NO
authentication, no CORS, and no CSRF protection. Any web page open on the
same machine while the wizard was running could drive real enrollment /
Windows service install via a classic enctype="text/plain" form POST (no
CORS preflight is triggered, so no CORS middleware would have blocked it).

This also covers two related findings fixed alongside the auth gate:
- do_install() must use the deploy.json-pinned control_plane_url for the
  actual login/enroll, never a request-body value that disagrees with it.
- do_install() must fail loudly if the agent's local pilot.json config is
  missing, not silently skip persisting the operator's folder selection
  while still reporting a successful install.
"""

from __future__ import annotations

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))  # tools/stowline-setup -> import wizard
sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "server"))  # -> import app.*

import pytest
from fastapi.testclient import TestClient

import wizard as w

VALID_INSTALL_BODY = {
    "site_id": "hq",
    "department": "IT",
    "control_plane_url": "https://cp.example.test",
    "username": "admin",
    "password": "x",
    "preferred_start_hhmm": "12:00",
    "selected": [{"path": r"C:\Users\Public\Documents", "kind": "directory"}],
    "confirm_install": True,
}

VALID_START_BACKUP_BODY = {"confirm_start": True}

LOCAL_ROUTES = [
    ("GET", "/api/local/identity", None),
    ("GET", "/api/local/preflight", None),
    ("GET", "/api/local/catalog", None),
    ("POST", "/api/local/discover", {}),
    ("POST", "/api/local/compile", {"selected": []}),
    ("POST", "/api/local/estimate", {"site_id": "hq", "preferred_start_hhmm": "12:00", "logical_bytes": 0}),
    ("POST", "/api/local/install", VALID_INSTALL_BODY),
    ("POST", "/api/local/start-first-backup", VALID_START_BACKUP_BODY),
    ("POST", "/api/local/check-code", {"code": "AAAAA-BBBBB-CCCCC-DDDDD"}),
]


@pytest.fixture()
def client(monkeypatch, tmp_path):
    monkeypatch.setitem(w.STATE, "token", "test-session-token")
    monkeypatch.setitem(w.STATE, "synthetic", True)  # never touches a real agent/network
    monkeypatch.setitem(w.STATE, "no_enroll", True)
    monkeypatch.setitem(w.STATE, "known", w._synthetic_tree(tmp_path / "synthtree"))
    monkeypatch.setitem(w.STATE, "deploy", {})
    with TestClient(w.app) as c:
        yield c


@pytest.mark.parametrize("method,path,body", LOCAL_ROUTES)
def test_every_local_route_requires_the_token(client, method, path, body):
    r = client.request(method, path, json=body)
    assert r.status_code == 401, f"{method} {path} did not require auth: {r.status_code} {r.text}"


@pytest.mark.parametrize("method,path,body", LOCAL_ROUTES)
def test_every_local_route_rejects_wrong_token(client, method, path, body):
    r = client.request(method, path, json=body, headers={"X-Wizard-Token": "not-the-right-token"})
    assert r.status_code == 401


@pytest.mark.parametrize("method,path,body", LOCAL_ROUTES)
def test_every_local_route_accepts_the_correct_token(client, method, path, body):
    r = client.request(method, path, json=body, headers={"X-Wizard-Token": "test-session-token"})
    assert r.status_code != 401, f"{method} {path}: {r.status_code} {r.text}"


def test_classic_form_csrf_shape_is_rejected():
    """The concrete attack: an auto-submitting HTML form with
    enctype="text/plain" reaches the route (no CORS preflight for a
    text/plain body) but supplies no custom header. It must be rejected."""
    with TestClient(w.app) as c:
        w.STATE["token"] = "real-token"
        w.STATE["synthetic"] = True
        r = c.post(
            "/api/local/install",
            content=b'{"confirm_install": true}',
            headers={"Content-Type": "text/plain"},
        )
        assert r.status_code == 401


def test_wizard_token_check_uses_constant_time_compare():
    import inspect

    src = inspect.getsource(w.require_wizard_token)
    assert "hmac.compare_digest" in src


def test_install_rejects_control_plane_url_that_disagrees_with_pinned_deploy(monkeypatch, tmp_path):
    """A pinned deploy.json control_plane_url is health-checked; login/enroll
    must use that same URL, never a different request-supplied one."""
    monkeypatch.setitem(w.STATE, "token", "tok")
    monkeypatch.setitem(w.STATE, "synthetic", False)
    monkeypatch.setitem(w.STATE, "no_enroll", False)
    monkeypatch.setitem(w.STATE, "deploy", {"control_plane_url": "https://real-pinned-cp.example", "gateway_url": ""})
    monkeypatch.setattr(w, "_is_admin", lambda: True)
    # A non-existent path skips the SHA-pin branch entirely, keeping this
    # test independent of whatever agent happens to be installed on the
    # machine running the suite; the pin-mismatch check must fire first
    # regardless, before any AGENT/network access.
    monkeypatch.setattr(w, "AGENT", Path("C:/does-not-exist-stowline-agent.exe"))
    # do_install() 500s if PILOT_JSON is missing, before ever reaching the
    # pin-mismatch check below -- confirmed live: this test silently relied
    # on the real C:\Stowline\config\pilot.json existing on the machine
    # running the suite, and broke the moment that file didn't.
    pilot_json = tmp_path / "pilot.json"
    pilot_json.write_text("{}", encoding="utf-8")
    monkeypatch.setattr(w, "PILOT_JSON", pilot_json)

    body = dict(VALID_INSTALL_BODY)
    body["control_plane_url"] = "https://attacker-controlled.example"
    with TestClient(w.app) as c:
        r = c.post("/api/local/install", json=body, headers={"X-Wizard-Token": "tok"})
    assert r.status_code == 422
    assert "does not match" in r.json()["detail"]


def test_install_never_auto_starts_the_service():
    """Phase 8 regression: do_install() may install the Windows service but
    must never start it. Starting is start_first_backup()'s job alone, and
    only that function may call `service start`."""
    import inspect

    install_src = inspect.getsource(w.do_install)
    start_src = inspect.getsource(w.start_first_backup)
    assert '"service", "start"' not in install_src, "do_install() must not start the service itself"
    assert '"service", "install"' in install_src
    assert '"service", "start"' in start_src


def test_control_enroll_uses_space_separated_url_and_token_flags():
    """Regression: agent/cmd/stowline-agent/ops.go's cmdControl hand-parses argv
    and only accepts --url/--token as two separate entries (args[i+1]) —
    unlike --mode, neither has '--flag=value' handling. do_install() used to
    pass f"--url={url}" / f"--token={token}" as single argv tokens, which the
    parser silently ignored, leaving url/token empty and always hitting the
    'usage:' branch — confirmed real end-to-end on a clean PC before fixing.
    """
    import inspect

    install_src = inspect.getsource(w.do_install)
    assert 'f"--url={url}"' not in install_src
    assert 'f"--token={token}"' not in install_src
    assert '"--url", url' in install_src
    assert '"--token", token' in install_src


def test_start_first_backup_requires_explicit_confirmation(monkeypatch):
    monkeypatch.setitem(w.STATE, "token", "tok")
    with TestClient(w.app) as c:
        r = c.post("/api/local/start-first-backup", json={"confirm_start": False}, headers={"X-Wizard-Token": "tok"})
    assert r.status_code == 422


def test_cp_login_leaves_the_caller_owned_client_open():
    """Regression: _cp_login used to open its own httpx.Client under a
    `with` block and return it after that block's __exit__ had already
    run, so the very next call do_install() made (minting the enrollment
    token) raised 'RuntimeError: Cannot send a request, as the client has
    been closed.' _cp_login must only ever use a client the caller owns."""
    import httpx

    def handler(request: httpx.Request) -> httpx.Response:
        if request.url.path == "/api/v1/auth/login":
            return httpx.Response(200, json={"ok": True}, headers={"set-cookie": "session=abc; Path=/"})
        return httpx.Response(200, json={"reused": True})

    with httpx.Client(transport=httpx.MockTransport(handler)) as hx:
        url, cookies = w._cp_login(hx, "https://cp.example.test", "admin", "pw")
        assert not hx.is_closed
        # Exactly the next thing do_install() does after login: reuse hx.
        again = hx.post(url + "/api/v1/admin/enrollment-tokens", json={}, cookies=cookies)
        assert again.status_code == 200


def test_install_converts_control_plane_network_failure_to_controlled_error(monkeypatch, tmp_path):
    """A real network/DNS/timeout failure reaching the control plane must
    come back as a controlled wizard error, not an unhandled exception ->
    raw ASGI 500 traceback."""
    import httpx

    monkeypatch.setitem(w.STATE, "token", "tok")
    monkeypatch.setitem(w.STATE, "synthetic", False)
    monkeypatch.setitem(w.STATE, "no_enroll", False)
    monkeypatch.setitem(w.STATE, "deploy", {})
    monkeypatch.setattr(w, "_is_admin", lambda: True)
    monkeypatch.setattr(w, "AGENT", Path("C:/does-not-exist-stowline-agent.exe"))
    pilot_json = tmp_path / "pilot.json"
    pilot_json.write_text("{}", encoding="utf-8")
    monkeypatch.setattr(w, "PILOT_JSON", pilot_json)
    monkeypatch.setattr(w, "MANIFEST", tmp_path / "manifest.json")
    monkeypatch.setattr(w, "preflight", lambda dep: {"ok": True, "can_enroll": True, "errors": []})

    # Starlette's own TestClient is itself implemented with httpx.Client, so
    # patching Client.post unconditionally would also break the test
    # harness's in-process call to the ASGI app. Only fail the wizard's own
    # outbound call to the fake control plane.
    real_post = httpx.Client.post

    def boom(self, url, *args, **kwargs):
        if "cp.example.test" in str(url):
            raise httpx.ConnectError("simulated: connection refused")
        return real_post(self, url, *args, **kwargs)

    monkeypatch.setattr(httpx.Client, "post", boom)

    body = dict(VALID_INSTALL_BODY)
    body["control_plane_url"] = "https://cp.example.test"
    with TestClient(w.app) as c:
        r = c.post("/api/local/install", json=body, headers={"X-Wizard-Token": "tok"})
    assert r.status_code == 502, r.text
    assert "could not reach control plane" in r.json()["detail"]


def test_install_provisions_machine_scope_secrets_before_service_install():
    """Regression: control enroll --mode=service only machine-scopes the
    control-plane/gateway-REST credentials, never PasswordRef (the restic
    repository password) -- that stays whatever bootstrap.py's initial
    `config write` set it to (dpapi-user), so a service that only ever
    ran control-enroll would permanently fail its own repository_auth
    preflight: "service mode requires dpapi-machine, not dpapi-user".
    Found end-to-end on a real external PC. do_install() must run
    `secrets provision --mode=service` -- the agent's own existing
    re-wrap -- before installing the service."""
    import inspect

    src = inspect.getsource(w.do_install)
    provision_idx = src.find('"secrets", "provision", "--mode=service"')
    install_idx = src.find('"service", "install"')
    assert provision_idx != -1, "do_install() must call secrets provision --mode=service"
    assert install_idx != -1
    assert provision_idx < install_idx, "secrets provision must run before service install"


def test_install_initializes_repository_before_secrets_provision_and_service_install():
    """A genuinely fresh device has a control-plane enrollment but no restic
    repository yet. workspace-repo-init must run after control enroll and
    before secrets provision --mode=service (which re-authenticates the
    current password against the repository -- it requires the repository
    to already exist) and before service install."""
    import inspect

    src = inspect.getsource(w.do_install)
    enroll_idx = src.find('"control", "enroll"')
    repo_init_idx = src.find("WORKSPACE_REPO_INIT")
    provision_idx = src.find('"secrets", "provision", "--mode=service"')
    install_idx = src.find('"service", "install"')
    assert -1 not in (enroll_idx, repo_init_idx, provision_idx, install_idx)
    assert enroll_idx < repo_init_idx < provision_idx < install_idx


def test_install_calls_workspace_repo_init_with_no_arguments():
    """workspace-repo-init (agent/cmd/workspace-repo-init) takes no CLI
    flags at all -- it only reads pilot.json (optionally overridden via
    STOWLINE_PILOT_JSON). do_install() must invoke the packaged executable
    exactly as-is, never inventing flags it doesn't support."""
    import inspect

    src = inspect.getsource(w.do_install)
    assert "[str(WORKSPACE_REPO_INIT)]" in src


def _mock_successful_control_plane(monkeypatch):
    """Mocks httpx.Client.post/get/patch so a real do_install() enroll
    sequence succeeds against a fake control plane at cp.example.test,
    without ever making a real network call. Never touches Starlette's own
    TestClient traffic (also implemented via httpx.Client)."""
    import httpx

    real_post, real_get, real_patch = httpx.Client.post, httpx.Client.get, httpx.Client.patch

    def fake_post(self, url, *args, **kwargs):
        u = str(url)
        if "cp.example.test" not in u:
            return real_post(self, url, *args, **kwargs)
        req = httpx.Request("POST", u)
        if u.endswith("/api/v1/auth/login"):
            return httpx.Response(200, json={"ok": True}, request=req)
        if u.endswith("/api/v1/admin/enrollment-tokens"):
            return httpx.Response(200, json={"token": "tok-abc"}, request=req)
        return httpx.Response(200, json={"ok": True}, request=req)

    def fake_get(self, url, *args, **kwargs):
        u = str(url)
        if "cp.example.test" not in u:
            return real_get(self, url, *args, **kwargs)
        return httpx.Response(200, json={"items": []}, request=httpx.Request("GET", u))

    def fake_patch(self, url, *args, **kwargs):
        u = str(url)
        if "cp.example.test" not in u:
            return real_patch(self, url, *args, **kwargs)
        return httpx.Response(200, json={"ok": True}, request=httpx.Request("PATCH", u))

    monkeypatch.setattr(httpx.Client, "post", fake_post)
    monkeypatch.setattr(httpx.Client, "get", fake_get)
    monkeypatch.setattr(httpx.Client, "patch", fake_patch)


class _FakeCompleted:
    def __init__(self, returncode, stdout="", stderr=""):
        self.returncode = returncode
        self.stdout = stdout
        self.stderr = stderr


def _mock_subprocess(monkeypatch, *, repo_init_returncode=0, repo_init_stderr="", service_install_returncode=0, service_install_stderr=""):
    """Records every subprocess.run call do_install() makes and returns
    canned success results, distinguishing commands by argv so ordering and
    exact invocation shape can be asserted precisely."""
    calls = []

    def fake_run(args, **kwargs):
        calls.append(list(args))
        exe = str(args[0])
        if exe == str(w.WORKSPACE_REPO_INIT):
            return _FakeCompleted(repo_init_returncode, stdout="abc123456789\n", stderr=repo_init_stderr)
        if len(args) >= 3 and args[1] == "service" and args[2] == "install":
            return _FakeCompleted(service_install_returncode, stdout="ok\n", stderr=service_install_stderr)
        return _FakeCompleted(0, stdout="ok\n")

    monkeypatch.setattr(w.subprocess, "run", fake_run)
    return calls


def _install_fixture(monkeypatch, tmp_path, token="tok"):
    import hashlib

    monkeypatch.setitem(w.STATE, "token", token)
    monkeypatch.setitem(w.STATE, "synthetic", False)
    monkeypatch.setitem(w.STATE, "no_enroll", False)
    monkeypatch.setitem(w.STATE, "deploy", {})
    monkeypatch.setattr(w, "_is_admin", lambda: True)
    # do_install() needs AGENT.is_file() == True to reach the enroll/
    # repo-init/provision/service-install sequence at all (it 404s
    # otherwise) -- so this must be a real file, with QUALIFIED patched to
    # match its actual hash rather than the real qualified agent's.
    fake_agent = tmp_path / "fake-stowline-agent.exe"
    fake_agent.write_bytes(b"not a real agent binary, just a test fixture")
    monkeypatch.setattr(w, "AGENT", fake_agent)
    monkeypatch.setattr(w, "QUALIFIED", hashlib.sha256(fake_agent.read_bytes()).hexdigest())
    pilot_json = tmp_path / "pilot.json"
    pilot_json.write_text("{}", encoding="utf-8")
    monkeypatch.setattr(w, "PILOT_JSON", pilot_json)
    monkeypatch.setattr(w, "MANIFEST", tmp_path / "manifest.json")
    monkeypatch.setattr(w, "preflight", lambda dep: {"ok": True, "can_enroll": True, "errors": []})
    body = dict(VALID_INSTALL_BODY)
    body["control_plane_url"] = "https://cp.example.test"
    return body


def test_install_reports_service_install_failure_honestly_not_as_success(monkeypatch, tmp_path):
    """Regression: do_install() used to set result["note"] to the success
    message unconditionally, even when `service install` itself failed
    (inst.returncode != 0) -- exactly the scenario a repair on a machine
    with a leftover StowlineBackup service registration hits. The operator
    must be told the service is NOT installed, not shown a "Servis
    kuruldu... Durduruldu" message for a device that isn't ready."""
    body = _install_fixture(monkeypatch, tmp_path)
    _mock_successful_control_plane(monkeypatch)
    _mock_subprocess(monkeypatch, service_install_returncode=1, service_install_stderr="service already exists")

    with TestClient(w.app) as c:
        r = c.post("/api/local/install", json=body, headers={"X-Wizard-Token": "tok"})
    assert r.status_code == 200, r.text
    out = r.json()
    assert out["service"] == "failed"
    assert out["service_state"] == "not_installed"
    assert "Kurulum tamamlandı.\nServis kuruldu" not in out["note"]
    assert "KURULAMADI" in out["note"]
    assert "service already exists" in out["note"]


def test_corrupted_pilot_json_surfaces_as_controlled_error_not_a_crash(monkeypatch, tmp_path):
    """Regression: apply_pilot_source_roots()/write_local_manifest() used
    to run outside any try/except in do_install() -- a leftover/corrupted
    pilot.json from a crashed prior install attempt (a real risk on a
    repair, not a fresh machine) made json.loads() raise uncaught, giving
    a raw ASGI 500 with no detail instead of a clean HTTPException."""
    body = _install_fixture(monkeypatch, tmp_path)
    _mock_successful_control_plane(monkeypatch)
    _mock_subprocess(monkeypatch)
    w.PILOT_JSON.write_text("{not valid json", encoding="utf-8")  # simulates a crashed prior write

    with TestClient(w.app) as c:
        r = c.post("/api/local/install", json=body, headers={"X-Wizard-Token": "tok"})
    assert r.status_code == 500
    assert "could not persist the selection" in r.json()["detail"]


def test_install_end_to_end_success_reaches_stopped_service(monkeypatch, tmp_path):
    """Fresh device, no prior repository: with control enroll, repository
    init, secrets provision, and service install all succeeding, the
    install must report enrolled, service installed but Stopped, and no
    backup started -- first backup stays a separate, explicit action."""
    body = _install_fixture(monkeypatch, tmp_path)
    _mock_successful_control_plane(monkeypatch)
    calls = _mock_subprocess(monkeypatch)

    with TestClient(w.app) as c:
        r = c.post("/api/local/install", json=body, headers={"X-Wizard-Token": "tok"})
    assert r.status_code == 200, r.text
    out = r.json()
    assert out["enrolled"] is True
    assert out["service_state"] == "Stopped"
    assert out["backup_started"] is False
    assert out["first_backup_requires_operator_action"] is True

    exes = [c[0] for c in calls]
    assert str(w.WORKSPACE_REPO_INIT) in exes
    repo_init_pos = exes.index(str(w.WORKSPACE_REPO_INIT))
    provision_pos = next(i for i, c in enumerate(calls) if len(c) >= 2 and c[1] == "secrets")
    install_pos = next(i for i, c in enumerate(calls) if len(c) >= 2 and c[1] == "service")
    assert repo_init_pos < provision_pos < install_pos
    # The repository-init step is a single, argument-free local subprocess
    # call -- it cannot itself talk to the control plane, so it cannot
    # itself create a device. Retrying enrollment itself is guarded
    # separately -- see test_install_does_not_re_enroll_when_pilot_json_
    # already_has_a_real_device_id.
    assert calls[repo_init_pos] == [str(w.WORKSPACE_REPO_INIT)]


def test_install_reports_a_clean_timeout_error_not_a_raw_500(monkeypatch, tmp_path):
    """Regression, confirmed live: `secrets provision --mode=service`
    re-authenticates against the real repository under the agent's own
    internal 2-minute context timeout (ops.go authenticateRepository ->
    CatConfig), which raced a do_install()-side subprocess.run timeout of
    the same 120s with no headroom -- the outer one always lost, and its
    bare subprocess.TimeoutExpired was never caught, so FastAPI returned a
    raw ASGI 500 with no JSON body at all (the browser could show nothing
    but the literal string "500"). Every step must instead come back as a
    controlled, detailed HTTPException."""
    body = _install_fixture(monkeypatch, tmp_path)
    _mock_successful_control_plane(monkeypatch)

    def fake_run(args, **kwargs):
        exe = str(args[0])
        if exe == str(w.AGENT) and len(args) >= 2 and args[1] == "secrets":
            raise w.subprocess.TimeoutExpired(cmd=args, timeout=kwargs.get("timeout"))
        if exe == str(w.WORKSPACE_REPO_INIT):
            return _FakeCompleted(0, stdout="abc123456789\n")
        return _FakeCompleted(0, stdout="ok\n")

    monkeypatch.setattr(w.subprocess, "run", fake_run)

    with TestClient(w.app) as c:
        r = c.post("/api/local/install", json=body, headers={"X-Wizard-Token": "tok"})
    assert r.status_code == 504, r.text
    assert "repository secret provisioning" in r.json()["detail"]
    assert "600" in r.json()["detail"]


def test_install_surfaces_repository_init_failure_before_secrets_provision_runs(monkeypatch, tmp_path):
    """A real failure (e.g. a still-unreachable gateway) must come back as
    a controlled error, and neither secrets provision nor service install
    may run once repository init has failed."""
    body = _install_fixture(monkeypatch, tmp_path)
    _mock_successful_control_plane(monkeypatch)
    calls = _mock_subprocess(monkeypatch, repo_init_returncode=1, repo_init_stderr="init failed: repository does not exist")

    with TestClient(w.app) as c:
        r = c.post("/api/local/install", json=body, headers={"X-Wizard-Token": "tok"})
    assert r.status_code == 500, r.text
    assert "repository initialization failed" in r.json()["detail"]
    assert not any(len(c) >= 2 and c[1] == "secrets" for c in calls)
    assert not any(len(c) >= 2 and c[1] == "service" for c in calls)


def test_repeated_install_calls_repository_init_identically_each_time(monkeypatch, tmp_path):
    """do_install() applies no first-time-vs-retry branching around
    repository init: it calls the same argument-free command every time,
    relying entirely on workspace-repo-init's own init-or-verify behavior
    for idempotency. Simulates an interrupted setup retried from scratch:
    both attempts must succeed identically, with no different invocation
    shape that could imply creating a second repository identity."""
    body = _install_fixture(monkeypatch, tmp_path)
    _mock_successful_control_plane(monkeypatch)
    calls = _mock_subprocess(monkeypatch)

    with TestClient(w.app) as c:
        r1 = c.post("/api/local/install", json=body, headers={"X-Wizard-Token": "tok"})
        r2 = c.post("/api/local/install", json=body, headers={"X-Wizard-Token": "tok"})
    assert r1.status_code == 200, r1.text
    assert r2.status_code == 200, r2.text
    repo_init_calls = [c for c in calls if c == [str(w.WORKSPACE_REPO_INIT)]]
    assert len(repo_init_calls) == 2
    assert repo_init_calls[0] == repo_init_calls[1]


def test_install_does_not_re_enroll_when_pilot_json_already_has_a_real_device_id(monkeypatch, tmp_path):
    """Regression: server/app/services.py enroll_agent() creates a brand-new
    device row for every redeemed enrollment token -- it never dedupes by
    hostname. Retrying a failed/interrupted install used to mint a fresh
    token and call `control enroll` again unconditionally, leaving a
    duplicate device behind on the control plane every time it was retried
    (confirmed against the real Railway catalog: seven duplicate devices
    for one physical test PC after this session's repeated testing). If
    this PC's own pilot.json already carries a real, previously-assigned
    device_id, do_install() must reuse it instead of enrolling again."""
    import json as _json

    body = _install_fixture(monkeypatch, tmp_path)
    # a retry on the same server, device still ACTIVE there (a leftover from
    # another server or a revoked device is enrolled fresh -- see below)
    w.PILOT_JSON.write_text(_json.dumps({"device_id": "already-enrolled-device-999", "control_plane_url": "https://cp.example.test"}), encoding="utf-8")

    import httpx

    posted_urls, patched_urls = [], []
    real_post, real_get, real_patch = httpx.Client.post, httpx.Client.get, httpx.Client.patch

    def fake_post(self, url, *args, **kwargs):
        u = str(url)
        if "cp.example.test" not in u:
            return real_post(self, url, *args, **kwargs)
        posted_urls.append(u)
        return httpx.Response(200, json={"ok": True}, request=httpx.Request("POST", u))

    def fake_get(self, url, *args, **kwargs):
        u = str(url)
        if "cp.example.test" not in u:
            return real_get(self, url, *args, **kwargs)
        if "/api/v1/admin/devices/already-enrolled-device-999" in u:
            return httpx.Response(200, json={"device": {"lifecycle": "ACTIVE"}}, request=httpx.Request("GET", u))
        return httpx.Response(200, json={"items": []}, request=httpx.Request("GET", u))

    def fake_patch(self, url, *args, **kwargs):
        u = str(url)
        if "cp.example.test" not in u:
            return real_patch(self, url, *args, **kwargs)
        patched_urls.append(u)
        return httpx.Response(200, json={"ok": True}, request=httpx.Request("PATCH", u))

    monkeypatch.setattr(httpx.Client, "post", fake_post)
    monkeypatch.setattr(httpx.Client, "get", fake_get)
    monkeypatch.setattr(httpx.Client, "patch", fake_patch)
    calls = _mock_subprocess(monkeypatch)

    with TestClient(w.app) as c:
        r = c.post("/api/local/install", json=body, headers={"X-Wizard-Token": "tok"})
    assert r.status_code == 200, r.text
    out = r.json()
    assert out["enrolled"] is True
    assert out["service_state"] == "Stopped"
    assert out["device_id"] == "already-enrolled-device-999"

    assert not any(u.endswith("/api/v1/admin/enrollment-tokens") for u in posted_urls), (
        "must not mint a new enrollment token when this device is already enrolled"
    )
    assert not any(len(c) >= 3 and c[1] == "control" and c[2] == "enroll" for c in calls), (
        "must not call control enroll again when this device is already enrolled"
    )
    assert any("already-enrolled-device-999" in u for u in patched_urls), "must still patch the existing device"
    # workspace-repo-init and secrets provision/service install must still
    # run on a reused enrollment -- skipping re-enroll must not skip the
    # rest of setup.
    assert str(w.WORKSPACE_REPO_INIT) in [c[0] for c in calls]


def test_install_reads_freshly_persisted_device_id_after_a_successful_enroll(monkeypatch, tmp_path):
    """After a fresh `control enroll` succeeds, the real agent persists the
    server-assigned device_id into pilot.json (agent/internal/enroll
    Bind()). do_install() must read it back from there for the
    PATCH/selection calls, rather than search admin/devices by hostname --
    a search that can silently match the wrong row once duplicate-hostname
    devices already exist from earlier enrollments (already true in
    production for this test rig's own hostname)."""
    import json as _json

    body = _install_fixture(monkeypatch, tmp_path)  # pilot.json starts as "{}" -> not-yet-enrolled path
    _mock_successful_control_plane(monkeypatch)

    def fake_run(args, **kwargs):
        exe = str(args[0])
        if exe == str(w.WORKSPACE_REPO_INIT):
            return _FakeCompleted(0, stdout="abc\n")
        if len(args) >= 3 and args[1] == "control" and args[2] == "enroll":
            # Simulate the real agent's enroll.Bind()/persistConfig writing
            # the server-assigned device_id into pilot.json.
            w.PILOT_JSON.write_text(_json.dumps({"device_id": "freshly-assigned-device-42"}), encoding="utf-8")
            return _FakeCompleted(0, stdout="ok\n")
        return _FakeCompleted(0, stdout="ok\n")

    monkeypatch.setattr(w.subprocess, "run", fake_run)

    import httpx

    patched_urls = []
    real_patch = httpx.Client.patch

    def fake_patch(self, url, *args, **kwargs):
        u = str(url)
        if "cp.example.test" not in u:
            return real_patch(self, url, *args, **kwargs)
        patched_urls.append(u)
        return httpx.Response(200, json={"ok": True}, request=httpx.Request("PATCH", u))

    monkeypatch.setattr(httpx.Client, "patch", fake_patch)

    with TestClient(w.app) as c:
        r = c.post("/api/local/install", json=body, headers={"X-Wizard-Token": "tok"})
    assert r.status_code == 200, r.text
    assert r.json()["device_id"] == "freshly-assigned-device-42"
    assert any("freshly-assigned-device-42" in u for u in patched_urls)


def test_install_requires_pilot_json_to_exist_before_reporting_success(monkeypatch, tmp_path):
    """Regression: a prior 'stowline-agent.exe config write' failure must not
    let install silently skip persisting source_roots and still report
    success."""
    missing = tmp_path / "does-not-exist" / "pilot.json"
    monkeypatch.setitem(w.STATE, "token", "tok")
    monkeypatch.setitem(w.STATE, "synthetic", False)
    monkeypatch.setitem(w.STATE, "no_enroll", False)
    monkeypatch.setitem(w.STATE, "deploy", {})
    monkeypatch.setattr(w, "PILOT_JSON", missing)
    monkeypatch.setattr(w, "_is_admin", lambda: True)
    # Independent of whatever real agent binary happens to be installed on
    # the machine running this suite: skip the SHA-pin branch so this test
    # only exercises the config-missing guard.
    monkeypatch.setattr(w, "AGENT", Path("C:/does-not-exist-stowline-agent.exe"))

    body = dict(VALID_INSTALL_BODY)
    with TestClient(w.app) as c:
        r = c.post("/api/local/install", json=body, headers={"X-Wizard-Token": "tok"})
    assert r.status_code == 500
    assert "agent config missing" in r.json()["detail"]


def test_discover_accepts_an_extra_root_and_flows_through_to_compile(client, tmp_path):
    """The folder-browse feature: an operator-picked path sent as
    extra_roots must come back as a real, checkable candidate, and its
    files must be compilable through /api/local/compile afterward --
    proving the whole discover -> compile round trip, not just discover()
    in isolation."""
    custom = tmp_path / "ClientFiles"
    custom.mkdir()
    (custom / "data.bin").write_bytes(b"x" * 40)
    r = client.post("/api/local/discover", json={"extra_roots": [str(custom)]}, headers={"X-Wizard-Token": "test-session-token"})
    assert r.status_code == 200, r.text
    out = r.json()
    extra = [c for c in out["candidates"] if c["category"] == "extra"]
    assert len(extra) == 1 and extra[0]["path"] == str(custom)

    comp = client.post(
        "/api/local/compile",
        json={"selected": [{"path": str(custom), "kind": "directory", "category": "extra"}]},
        headers={"X-Wizard-Token": "test-session-token"},
    )
    assert comp.status_code == 200, comp.text
    manifest = comp.json()
    # the folder itself is the root, so files added to it later are backed up too
    assert manifest["source_roots"] == [str(custom)]


def test_browse_folder_returns_the_picked_path(monkeypatch):
    monkeypatch.setitem(w.STATE, "token", "tok")

    def fake_run(args, **kwargs):
        assert args[0] == r"C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe"
        return _FakeCompleted(0, stdout="C:\\Users\\op\\ClientFiles\n")

    monkeypatch.setattr(w.subprocess, "run", fake_run)
    with TestClient(w.app) as c:
        r = c.post("/api/local/browse-folder", headers={"X-Wizard-Token": "tok"})
    assert r.status_code == 200, r.text
    assert r.json()["path"] == "C:\\Users\\op\\ClientFiles"


def test_browse_folder_returns_null_path_when_the_operator_cancels(monkeypatch):
    monkeypatch.setitem(w.STATE, "token", "tok")
    monkeypatch.setattr(w.subprocess, "run", lambda args, **kwargs: _FakeCompleted(0, stdout=""))
    with TestClient(w.app) as c:
        r = c.post("/api/local/browse-folder", headers={"X-Wizard-Token": "tok"})
    assert r.status_code == 200, r.text
    assert r.json()["path"] is None


def test_browse_folder_surfaces_a_timeout_as_a_controlled_error(monkeypatch):
    monkeypatch.setitem(w.STATE, "token", "tok")

    def fake_run(args, **kwargs):
        raise w.subprocess.TimeoutExpired(cmd=args, timeout=kwargs.get("timeout"))

    monkeypatch.setattr(w.subprocess, "run", fake_run)
    with TestClient(w.app) as c:
        r = c.post("/api/local/browse-folder", headers={"X-Wizard-Token": "tok"})
    assert r.status_code == 504, r.text


def _mock_control_plane_with_device(monkeypatch, *, lifecycle="ACTIVE", exists=True):
    """Like _mock_successful_control_plane, but GET /admin/devices/<id>
    answers for one known device, and every PATCH body is recorded."""
    import httpx

    real_post, real_get, real_patch = httpx.Client.post, httpx.Client.get, httpx.Client.patch
    patches = []

    def fake_post(self, url, *args, **kwargs):
        u = str(url)
        if "cp.example.test" not in u:
            return real_post(self, url, *args, **kwargs)
        req = httpx.Request("POST", u)
        if u.endswith("/api/v1/admin/enrollment-tokens"):
            return httpx.Response(200, json={"token": "tok-abc"}, request=req)
        return httpx.Response(200, json={"ok": True}, request=req)

    def fake_get(self, url, *args, **kwargs):
        u = str(url)
        if "cp.example.test" not in u:
            return real_get(self, url, *args, **kwargs)
        req = httpx.Request("GET", u)
        if "/api/v1/admin/devices/" in u:
            if not exists:
                return httpx.Response(404, json={"detail": "not found"}, request=req)
            return httpx.Response(200, json={"device": {"lifecycle": lifecycle}}, request=req)
        return httpx.Response(200, json={"items": []}, request=req)

    def fake_patch(self, url, *args, **kwargs):
        u = str(url)
        if "cp.example.test" not in u:
            return real_patch(self, url, *args, **kwargs)
        patches.append((u, kwargs.get("json")))
        return httpx.Response(200, json={"ok": True}, request=httpx.Request("PATCH", u))

    monkeypatch.setattr(httpx.Client, "post", fake_post)
    monkeypatch.setattr(httpx.Client, "get", fake_get)
    monkeypatch.setattr(httpx.Client, "patch", fake_patch)
    return patches


def _enroll_calls(calls):
    return [c for c in calls if len(c) >= 3 and c[1] == "control" and c[2] == "enroll"]


def test_a_leftover_pilot_json_from_another_server_is_never_reused(monkeypatch, tmp_path):
    """The 6 PCs being reinstalled still carry their old Railway pilot.json.
    Reusing that device_id would skip enrollment against the new server and
    leave the PC unable to back up."""
    import json

    body = _install_fixture(monkeypatch, tmp_path)
    w.PILOT_JSON.write_text(json.dumps({"device_id": "old-railway-device", "control_plane_url": "https://old-control.example.net"}), encoding="utf-8")
    _mock_control_plane_with_device(monkeypatch)
    calls = _mock_subprocess(monkeypatch)
    with TestClient(w.app) as c:
        r = c.post("/api/local/install", json=body, headers={"X-Wizard-Token": "tok"})
    assert r.status_code == 200, r.text
    assert len(_enroll_calls(calls)) == 1, "must enroll fresh against the new server"


def test_a_leftover_quarantined_device_on_this_server_is_never_reused(monkeypatch, tmp_path):
    import json

    body = _install_fixture(monkeypatch, tmp_path)
    w.PILOT_JSON.write_text(json.dumps({"device_id": "revoked-device", "control_plane_url": "https://cp.example.test"}), encoding="utf-8")
    _mock_control_plane_with_device(monkeypatch, lifecycle="QUARANTINED")
    calls = _mock_subprocess(monkeypatch)
    with TestClient(w.app) as c:
        r = c.post("/api/local/install", json=body, headers={"X-Wizard-Token": "tok"})
    assert r.status_code == 200, r.text
    assert len(_enroll_calls(calls)) == 1


def test_a_retry_on_the_same_server_reuses_the_active_enrollment(monkeypatch, tmp_path):
    """A second run of the wizard on the same PC (repair / retry) must not
    create a duplicate device."""
    import json

    body = _install_fixture(monkeypatch, tmp_path)
    w.PILOT_JSON.write_text(json.dumps({"device_id": "live-device", "control_plane_url": "https://cp.example.test/"}), encoding="utf-8")
    _mock_control_plane_with_device(monkeypatch, lifecycle="ACTIVE")
    calls = _mock_subprocess(monkeypatch)
    with TestClient(w.app) as c:
        r = c.post("/api/local/install", json=body, headers={"X-Wizard-Token": "tok"})
    assert r.status_code == 200, r.text
    assert _enroll_calls(calls) == []


def test_the_name_typed_in_the_wizard_becomes_the_panel_name(monkeypatch, tmp_path):
    body = _install_fixture(monkeypatch, tmp_path)
    body["display_name"] = "  Muhasebe - Ayşe  "
    patches = _mock_control_plane_with_device(monkeypatch, exists=False)
    _mock_subprocess(monkeypatch)
    import json

    # control enroll is mocked, so write the device_id it would have persisted
    real_run = w.subprocess.run

    def run_and_bind(args, **kwargs):
        res = real_run(args, **kwargs)
        if len(args) >= 3 and args[1] == "control" and args[2] == "enroll":
            w.PILOT_JSON.write_text(json.dumps({"device_id": "new-device", "control_plane_url": "https://cp.example.test"}), encoding="utf-8")
        return res

    monkeypatch.setattr(w.subprocess, "run", run_and_bind)
    with TestClient(w.app) as c:
        r = c.post("/api/local/install", json=body, headers={"X-Wizard-Token": "tok"})
    assert r.status_code == 200, r.text
    device_patches = [j for u, j in patches if u.endswith("/api/v1/admin/devices/new-device")]
    assert device_patches and device_patches[0].get("display_name") == "Muhasebe - Ayşe"


def _start_fixture(monkeypatch, tmp_path, *, backup_now_status=202, backup_now_body=None):
    """start_first_backup with the service start mocked and the agent's local
    API (127.0.0.1:18080) answered by a fake."""
    import httpx

    monkeypatch.setitem(w.STATE, "token", "tok")
    monkeypatch.setattr(w, "_is_admin", lambda: True)
    fake_agent = tmp_path / "fake-stowline-agent.exe"
    fake_agent.write_bytes(b"x")
    monkeypatch.setattr(w, "AGENT", fake_agent)
    monkeypatch.setattr(w.time, "sleep", lambda s: None)
    _mock_subprocess(monkeypatch)
    sent = []
    real_get, real_post = httpx.Client.get, httpx.Client.post

    def fake_get(self, url, *args, **kwargs):
        if "127.0.0.1:18080" not in str(url):
            return real_get(self, url, *args, **kwargs)
        return httpx.Response(200, json={"backing_up": False}, request=httpx.Request("GET", str(url)))

    def fake_post(self, url, *args, **kwargs):
        if "127.0.0.1:18080" not in str(url):
            return real_post(self, url, *args, **kwargs)
        sent.append((str(url), kwargs.get("headers") or {}))
        return httpx.Response(backup_now_status, json=backup_now_body or {"queued": True}, request=httpx.Request("POST", str(url)))

    monkeypatch.setattr(httpx.Client, "get", fake_get)
    monkeypatch.setattr(httpx.Client, "post", fake_post)
    return sent


def test_start_first_backup_really_queues_a_backup_not_just_the_service(monkeypatch, tmp_path):
    """Regression (found on a real install): the button only started the
    Windows service and reported backup_started=True, while the agent then
    waited for the scheduled time -- the first backup did not start."""
    sent = _start_fixture(monkeypatch, tmp_path)
    with TestClient(w.app) as c:
        r = c.post("/api/local/start-first-backup", json={"confirm_start": True}, headers={"X-Wizard-Token": "tok"})
    assert r.status_code == 200, r.text
    assert r.json()["backup_started"] is True
    assert sent and sent[0][0].endswith("/api/backup-now")
    assert sent[0][1].get("X-Stowline-Local") == "1"


def test_start_first_backup_is_honest_when_the_backup_cannot_be_queued(monkeypatch, tmp_path):
    _start_fixture(monkeypatch, tmp_path, backup_now_status=502, backup_now_body={"error": "backup window closed"})
    with TestClient(w.app) as c:
        r = c.post("/api/local/start-first-backup", json={"confirm_start": True}, headers={"X-Wizard-Token": "tok"})
    assert r.status_code == 200, r.text
    out = r.json()
    assert out["service_state"] == "Running"
    assert out["backup_started"] is False
    assert out.get("message")


def test_start_first_backup_twice_is_not_an_error(monkeypatch, tmp_path):
    """Live: the first click started the service slowly; a second click got
    "service start failed: An instance of the service is already running"."""
    import subprocess

    monkeypatch.setitem(w.STATE, "token", "tok")
    monkeypatch.setattr(w, "_is_admin", lambda: True)
    agent = tmp_path / "stowline-agent.exe"
    agent.write_bytes(b"x")
    monkeypatch.setattr(w, "AGENT", agent)
    monkeypatch.setattr(w, "_run_agent", lambda *a, **k: subprocess.CompletedProcess([], 1, "", "error: An instance of the service is already running."))
    called = []
    monkeypatch.setattr(w, "_request_backup_now", lambda: called.append(1) or (True, ""))
    with TestClient(w.app) as c:
        r = c.post("/api/local/start-first-backup", json={"confirm_start": True}, headers={"X-Wizard-Token": "tok"})
    assert r.status_code == 200 and r.json()["backup_started"] is True
    assert not called, "must not queue a second backup"


def test_a_generic_package_connects_to_a_typed_server_and_uses_its_sites(monkeypatch):
    """A generic installer has no server baked in: /api/local/server checks
    the typed address and switches the wizard to that server's sites."""
    import httpx

    monkeypatch.setitem(w.STATE, "token", "tok")
    monkeypatch.setitem(w.STATE, "synthetic", False)
    monkeypatch.setitem(w.STATE, "deploy", {})
    monkeypatch.setitem(w.STATE, "server_typed", False)
    saved_sites, saved_deps = dict(w.SITE_SCHEDULE), list(w.DEPARTMENTS)
    monkeypatch.setattr(w, "preflight", lambda dep: {"ok": True, "errors": []})

    real_get = httpx.Client.get

    def fake_get(self, url, *a, **k):
        if not str(url).startswith("https://"):
            return real_get(self, url, *a, **k)  # the TestClient's own requests
        assert url == "https://backup.example.org/api/v1/setup/catalog"
        req = httpx.Request("GET", url)
        return httpx.Response(200, request=req, json={"sites": [{"id": "Plant", "display_name": "Plant", "eligibility_start": "07:00"}], "departments": ["Ops"]})

    monkeypatch.setattr(httpx.Client, "get", fake_get)
    try:
        with TestClient(w.app) as c:
            r = c.post("/api/local/server", json={"url": "backup.example.org"}, headers={"X-Wizard-Token": "tok"})
            assert r.status_code == 200, r.text
            body = r.json()
            assert body["ok"] is True
            assert [s["id"] for s in body["catalog"]["sites"]] == ["plant"]
            assert body["catalog"]["departments"] == ["Ops"]
            ident = c.get("/api/local/identity", headers={"X-Wizard-Token": "tok"}).json()
            assert ident["control_plane_url"] == "https://backup.example.org"
            assert ident["gateway_url"] == "https://backup.example.org/gw"
    finally:
        w.SITE_SCHEDULE.clear()
        w.SITE_SCHEDULE.update(saved_sites)
        w.DEPARTMENTS[:] = saved_deps


def test_a_pinned_package_refuses_a_typed_server(monkeypatch):
    monkeypatch.setitem(w.STATE, "token", "tok")
    monkeypatch.setitem(w.STATE, "deploy", {"control_plane_url": "https://pinned.example.org"})
    monkeypatch.setitem(w.STATE, "server_typed", False)
    with TestClient(w.app) as c:
        r = c.post("/api/local/server", json={"url": "https://elsewhere.example.org"}, headers={"X-Wizard-Token": "tok"})
    assert r.status_code == 409


def _record_control_plane_posts(monkeypatch):
    import httpx

    real_post = httpx.Client.post
    seen = []

    def spy(self, url, *args, **kwargs):
        if "cp.example.test" in str(url):
            seen.append((str(url), kwargs.get("json")))
        return real_post(self, url, *args, **kwargs)

    monkeypatch.setattr(httpx.Client, "post", spy)
    return seen


def test_install_with_a_setup_code_never_sends_the_admin_password(monkeypatch, tmp_path):
    body = _install_fixture(monkeypatch, tmp_path)
    _mock_successful_control_plane(monkeypatch)
    seen = _record_control_plane_posts(monkeypatch)
    _mock_subprocess(monkeypatch)
    body.update({"setup_code": "ABCDE-FGHJK-MNPQR-STUVW", "username": "", "password": ""})
    with TestClient(w.app) as c:
        r = c.post("/api/local/install", json=body, headers={"X-Wizard-Token": "tok"})
    assert r.status_code == 200, r.text
    urls = [u for u, _ in seen]
    assert any(u.endswith("/api/v1/setup/login") for u in urls)
    assert not any(u.endswith("/api/v1/auth/login") for u in urls)
    login = next(j for u, j in seen if u.endswith("/api/v1/setup/login"))
    assert login == {"code": "ABCDE-FGHJK-MNPQR-STUVW"}


def test_install_without_code_or_login_is_refused(monkeypatch, tmp_path):
    body = _install_fixture(monkeypatch, tmp_path)
    _mock_successful_control_plane(monkeypatch)
    _mock_subprocess(monkeypatch)
    body.update({"setup_code": "", "username": "", "password": ""})
    with TestClient(w.app) as c:
        r = c.post("/api/local/install", json=body, headers={"X-Wizard-Token": "tok"})
    assert r.status_code == 422


def test_check_code_asks_the_connected_server(monkeypatch):
    import httpx

    monkeypatch.setitem(w.STATE, "token", "tok")
    monkeypatch.setitem(w.STATE, "deploy", {"control_plane_url": "https://cp.example.test"})
    real_post = httpx.Client.post

    def fake_post(self, url, *args, **kwargs):
        u = str(url)
        if "cp.example.test" not in u:
            return real_post(self, url, *args, **kwargs)
        ok = kwargs.get("json", {}).get("code") == "GOOD"
        return httpx.Response(200 if ok else 401, json={"valid": ok, "site_id": "hq"}, request=httpx.Request("POST", u))

    monkeypatch.setattr(httpx.Client, "post", fake_post)
    with TestClient(w.app) as c:
        good = c.post("/api/local/check-code", json={"code": "GOOD"}, headers={"X-Wizard-Token": "tok"})
        bad = c.post("/api/local/check-code", json={"code": "BAD"}, headers={"X-Wizard-Token": "tok"})
    assert good.status_code == 200 and good.json()["site_id"] == "hq"
    assert bad.status_code == 401
