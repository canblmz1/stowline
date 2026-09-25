"""Elevated bootstrap: layout C:\\Stowline, verify the pinned binaries, launch the wizard.

The SHA-256 of every binary is pinned in payload\\pins.json by
scripts/package-installer.py; nothing that does not match is installed."""

from __future__ import annotations

import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
from pathlib import Path

PILOT = Path(r"C:\Stowline")


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def load_pins(root: Path) -> dict:
    pins_path = root / "pins.json"
    if not pins_path.is_file():
        raise SystemExit(f"ABORT: missing {pins_path}; rebuild the package with scripts/package-installer.py")
    pins = json.loads(pins_path.read_text(encoding="utf-8"))
    for name in ("stowline-agent.exe", "restic.exe", "rclone.exe", "workspace-repo-init.exe"):
        if not re.fullmatch(r"[0-9a-f]{64}", str(pins.get(name, ""))):
            raise SystemExit(f"ABORT: pins.json has no valid SHA-256 for {name}")
    return pins


def require_hash(path: Path, expected: str, label: str) -> None:
    if not path.is_file():
        raise SystemExit(f"ABORT: missing {label}: {path}")
    actual = sha256(path)
    if actual != expected:
        raise SystemExit(f"ABORT: {label} SHA mismatch\n expected {expected}\n actual   {actual}")


def payload_root() -> Path:
    env = os.environ.get("STOWLINE_SETUP_ROOT")
    if env and Path(env).is_dir():
        return Path(env)
    here = Path(__file__).resolve().parent
    for cand in (here.parent, here.parents[1] if len(here.parents) > 1 else here):
        if (cand / "bin" / "stowline-agent.exe").is_file() or (cand / "payload" / "bin" / "stowline-agent.exe").is_file():
            return cand / "payload" if (cand / "payload").is_dir() else cand
    return here.parent


def copy_file(src: Path, dest: Path) -> None:
    dest.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(src, dest)


def _ps_quote(s: str) -> str:
    return "'" + str(s).replace("'", "''") + "'"


def create_start_menu_shortcut(setup_cmd: Path) -> None:
    """Best-effort only: a missing or failed shortcut must never abort
    setup -- it is a convenience for later local/emergency
    administration (re-running Setup Wizard without hunting for the
    installer folder again), not something setup depends on."""
    if not setup_cmd.is_file():
        return
    start_menu = Path(os.environ.get("ProgramData", r"C:\ProgramData")) / "Microsoft" / "Windows" / "Start Menu" / "Programs"
    try:
        start_menu.mkdir(parents=True, exist_ok=True)
    except OSError:
        return
    link_path = start_menu / "Stowline Setup.lnk"
    script = (
        "$s = New-Object -ComObject WScript.Shell; "
        f"$sc = $s.CreateShortcut({_ps_quote(link_path)}); "
        f"$sc.TargetPath = {_ps_quote(setup_cmd)}; "
        f"$sc.WorkingDirectory = {_ps_quote(setup_cmd.parent)}; "
        "$sc.Description = 'Open the Stowline setup wizard'; "
        "$sc.Save()"
    )
    powershell = r"C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe"
    try:
        subprocess.run(
            [powershell, "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", script],
            capture_output=True,
            text=True,
            timeout=30,
            creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
        )
    except (OSError, subprocess.TimeoutExpired):
        pass


EDGE_PATHS = [
    Path(r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe"),
    Path(r"C:\Program Files\Microsoft\Edge\Application\msedge.exe"),
]
# The agent's local desktop page (agent/cmd/stowline-agent/localui.go, localUIAddr).
LOCAL_UI_URL = "http://127.0.0.1:18080/"
# Stowline Backups.exe, installed by layout() when the package carries it.
USER_APP = PILOT / "bin" / "Stowline Backups.exe"


def create_user_app_shortcuts() -> None:
    """The end user's way into Stowline Backups (progress, own folders, own
    restore): a shortcut on the all-users Desktop and Start menu. It opens
    Stowline Backups.exe when installed (native folder picker, restores into
    Belgeler), otherwise the same page in an Edge app window. Best-effort
    like the setup shortcut -- if PowerShell fails, setup goes on."""
    edge = next((e for e in EDGE_PATHS if e.is_file()), None)
    if USER_APP.is_file():
        target, arguments, icon = USER_APP, "", f"{USER_APP},0"
    elif edge is not None:
        target, arguments, icon = edge, "--app=" + LOCAL_UI_URL, f"{edge},0"
    else:
        return
    public = Path(os.environ.get("PUBLIC", r"C:\Users\Public"))
    start_menu = Path(os.environ.get("ProgramData", r"C:\ProgramData")) / "Microsoft" / "Windows" / "Start Menu" / "Programs"
    powershell = r"C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe"
    for folder in (public / "Desktop", start_menu):
        link_path = folder / "Stowline Backups.lnk"
        script = (
            "$s = New-Object -ComObject WScript.Shell; "
            f"$sc = $s.CreateShortcut({_ps_quote(link_path)}); "
            f"$sc.TargetPath = {_ps_quote(target)}; "
            f"$sc.Arguments = {_ps_quote(arguments)}; "
            f"$sc.IconLocation = {_ps_quote(icon)}; "
            "$sc.Description = 'Stowline Backups: backup status, folders and restore'; "
            "$sc.Save()"
        )
        try:
            folder.mkdir(parents=True, exist_ok=True)
            subprocess.run(
                [powershell, "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", script],
                capture_output=True,
                text=True,
                timeout=30,
                creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
            )
        except (OSError, subprocess.TimeoutExpired):
            pass


def layout(root: Path) -> None:
    pins = load_pins(root)
    names = ("stowline-agent.exe", "restic.exe", "rclone.exe", "workspace-repo-init.exe")
    for name in names:
        require_hash(root / "bin" / name, pins[name], "packaged " + name)

    for sub in ("bin", "config", "secrets", "state", "cache", "cache\\tmp", "Restore", "Repos", "rollback"):
        (PILOT / sub).mkdir(parents=True, exist_ok=True)
    for name in names:
        copy_file(root / "bin" / name, PILOT / "bin" / name)
        require_hash(PILOT / "bin" / name, pins[name], "installed " + name)
    user_app_src = root / "bin" / "Stowline Backups.exe"
    if user_app_src.is_file():
        copy_file(user_app_src, USER_APP)

    agent = PILOT / "bin" / "stowline-agent.exe"
    pilot_json = PILOT / "config" / "pilot.json"
    if not pilot_json.is_file():
        res = subprocess.run([str(agent), "config", "write"], capture_output=True, text=True, check=False)
        if res.returncode != 0 or not pilot_json.is_file():
            # Silently continuing here previously let the wizard report a
            # successful install/enroll while the operator's chosen backup
            # folders were never persisted to the config the service reads.
            raise SystemExit(
                "ABORT: 'stowline-agent.exe config write' failed "
                f"(exit {res.returncode}); {pilot_json} was not created. "
                f"stderr: {(res.stderr or '').strip()[:500]}"
            )

    create_start_menu_shortcut(root.parent / "Setup.cmd")
    create_user_app_shortcuts()


def wizard_passthrough(args: list[str]) -> list[str]:
    """Options Stowline Setup.exe passes through to wizard.py: it shows the
    wizard in its own window, so no browser tab, and picks the port."""
    out: list[str] = []
    for i, a in enumerate(args):
        if a == "--no-browser":
            out.append(a)
        elif a == "--port" and i + 1 < len(args):
            out += [a, args[i + 1]]
    return out


def main() -> None:
    args = sys.argv[1:]
    verify_only = "--verify-only" in args
    no_wizard = "--no-wizard" in args
    root = payload_root()
    os.environ["STOWLINE_SETUP_ROOT"] = str(root)
    deploy = root.parent / "deploy.json"
    if deploy.is_file():
        os.environ["STOWLINE_DEPLOY_JSON"] = str(deploy)
    print(f"payload={root}")
    require_hash(root / "bin" / "stowline-agent.exe", load_pins(root)["stowline-agent.exe"], "packaged agent")
    if verify_only:
        print("VERIFY_OK")
        return
    layout(root)
    print("LAYOUT_OK")
    if no_wizard:
        return
    wizard = Path(__file__).resolve().parent / "wizard.py"
    cmd = [sys.executable, str(wizard)] + wizard_passthrough(args)
    if deploy.is_file():
        cmd += ["--deploy", str(deploy)]
    # DETACHED_PROCESS: the wizard's HTTP server must not share this
    # console. Without it, wizard.py is a child attached to the same
    # console as this elevated window -- closing that window (or losing
    # it to a dropped remote-desktop session) sends a close signal to
    # every process on the console, killing the wizard mid-request. The
    # browser then sees the TCP connection vanish ("failed to fetch")
    # regardless of how far do_install() had gotten. subprocess.call still
    # waits for the child's exit either way; detaching only removes the
    # console-close signal, not the wait.
    #
    # A detached process has no console, so it cannot inherit this
    # console's stdio handles -- confirmed live: without explicit
    # redirection, wizard.py crashed on startup before printing anything
    # at all (exit 1, no output), because Python's own runtime tried to
    # wire up stdout/stderr against handles that don't work without a
    # console. Give it real files instead of relying on inheritance.
    detached = getattr(subprocess, "DETACHED_PROCESS", 0)
    log_dir = PILOT / "cache"
    log_dir.mkdir(parents=True, exist_ok=True)
    log_path = log_dir / "wizard-stdio.log"
    with open(log_path, "ab") as log_fh:
        rc = subprocess.call(cmd, creationflags=detached, stdin=subprocess.DEVNULL, stdout=log_fh, stderr=subprocess.STDOUT)
    raise SystemExit(rc)


if __name__ == "__main__":
    main()
