r"""Build the Windows installer.

Without --server the package is generic (this is what a GitHub release
ships): the setup wizard asks for the server address and takes the sites and
departments from that server. With --server the address is fixed in the
package and the wizard never asks.

    python scripts/package-installer.py [--server https://backup.example.com] \
        [--gateway https://backup.example.com/gw] [--sites sites.json] \
        [--build releases/<dir>] [--bin-dir <folder with restic.exe and rclone.exe>]

Inputs:
  --build    output of scripts/build-release.ps1 (stowline-agent.exe,
             workspace-repo-init.exe, "Stowline Setup.exe", "Stowline
             Backups.exe"); default: the newest releases/<dir> holding them.
  --bin-dir  restic.exe and rclone.exe (scripts/fetch-pinned-binaries.ps1
             downloads them); each must match release/manifest.json.
  --sites    the same sites/departments JSON the server uses
             (STOWLINE_SITES_FILE); the setup wizard offers exactly these.

Output:
  releases/installer/             the portable package (Setup.cmd, payload\...)
  releases/setup-single/Stowline Setup.exe
                                  the same package as ONE file (unpacks itself)

The package pins the SHA-256 of every binary it ships in payload\pins.json;
bootstrap.py refuses to install anything that does not match.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import shutil
import subprocess
import urllib.request
import zipfile
from pathlib import Path

REPO = Path(__file__).resolve().parents[1]
OUT = REPO / "releases" / "installer"
SINGLE_OUT = REPO / "releases" / "setup-single"
SETUP_CMD = REPO / "agent" / "cmd" / "stowline-setup"
CACHE = REPO / "releases" / "_cache"
MANIFEST = REPO / "release" / "manifest.json"
PY_VER = "3.12.10"
PY_ZIP = f"python-{PY_VER}-embed-amd64.zip"
PY_URL = f"https://www.python.org/ftp/python/{PY_VER}/{PY_ZIP}"
GETPIP = "https://bootstrap.pypa.io/get-pip.py"

# server modules the wizard imports (discovery, selection, schedule rules)
APP_FILES = (
    "__init__.py",
    "discovery.py",
    "selection.py",
    "schedule_policy.py",
    "bandwidth.py",
    "network_preflight.py",
    "version.py",
)


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def download(url: str, dest: Path) -> None:
    dest.parent.mkdir(parents=True, exist_ok=True)
    if dest.is_file() and dest.stat().st_size > 0:
        return
    print(f"download {url}")
    urllib.request.urlretrieve(url, dest)  # noqa: S310


def find_build(arg: str) -> Path:
    if arg:
        d = Path(arg)
    else:
        cands = sorted((REPO / "releases").glob("*/stowline-agent.exe"), key=lambda p: p.stat().st_mtime, reverse=True)
        if not cands:
            raise SystemExit("no build found: run scripts/build-release.ps1 first (or pass --build)")
        d = cands[0].parent
    for name in ("stowline-agent.exe", "workspace-repo-init.exe", "Stowline Setup.exe", "Stowline Backups.exe"):
        if not (d / name).is_file():
            raise SystemExit(f"build {d} lacks {name}")
    return d


def find_pinned(bin_dir: str, name: str, expected: str) -> Path:
    for cand in ([Path(bin_dir) / name] if bin_dir else []) + [REPO / "release" / "bin" / name, CACHE / name]:
        if cand.is_file():
            if sha256(cand) != expected:
                raise SystemExit(f"{cand}: SHA-256 does not match release/manifest.json ({expected})")
            return cand
    raise SystemExit(f"{name} not found: run scripts/fetch-pinned-binaries.ps1 and pass --bin-dir")


def write_text(path: Path, text: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8", newline="\r\n" if path.suffix.lower() in {".txt", ".cmd"} else "\n")


def embedded_python(runtime: Path) -> None:
    zpath = CACHE / PY_ZIP
    download(PY_URL, zpath)
    runtime.mkdir(parents=True)
    with zipfile.ZipFile(zpath) as zf:
        zf.extractall(runtime)
    pth = next(runtime.glob("python*._pth"))
    pth.write_text("python312.zip\n.\nLib\\site-packages\nimport site\n", encoding="utf-8")
    getpip = CACHE / "get-pip.py"
    download(GETPIP, getpip)
    py = runtime / "python.exe"
    subprocess.check_call([str(py), str(getpip), "--no-warn-script-location"], cwd=runtime)
    subprocess.check_call(
        [str(py), "-m", "pip", "install", "--no-warn-script-location", "fastapi", "uvicorn", "pydantic", "httpx", "tzdata"],
        cwd=runtime,
    )
    # Windows has no IANA tz database and the embedded distribution carries
    # none either; without tzdata every ZoneInfo() raises. Fail here, not on
    # the wizard's schedule screen on a clean PC.
    tz = subprocess.run([str(py), "-c", "from zoneinfo import ZoneInfo; ZoneInfo('UTC'); ZoneInfo('Europe/Istanbul')"], capture_output=True, text=True)
    if tz.returncode != 0:
        raise SystemExit("embedded runtime cannot resolve time zones after installing tzdata:\n" + tz.stderr)


def build_single_exe() -> Path:
    """One file for the person installing: "Stowline Setup.exe" with the whole
    package embedded (payload.zip), unpacked at run time under ProgramData.
    The zip is deterministic (sorted names, fixed timestamps) and is removed
    again after the build."""
    zpath = SETUP_CMD / "payload.zip"
    files = sorted(p for p in OUT.rglob("*") if p.is_file() and p.name != "Stowline Setup.exe")
    with zipfile.ZipFile(zpath, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=6) as zf:
        for f in files:
            info = zipfile.ZipInfo(f.relative_to(OUT).as_posix(), date_time=(2026, 1, 1, 0, 0, 0))
            info.compress_type = zipfile.ZIP_DEFLATED
            info.external_attr = 0o644 << 16
            zf.writestr(info, f.read_bytes())
    try:
        if SINGLE_OUT.exists():
            shutil.rmtree(SINGLE_OUT)
        SINGLE_OUT.mkdir(parents=True)
        exe = SINGLE_OUT / "Stowline Setup.exe"
        subprocess.check_call(
            ["go", "build", "-tags", "embedpayload", "-trimpath", "-buildvcs=false", "-ldflags", "-H windowsgui -buildid=", "-o", str(exe), "./agent/cmd/stowline-setup"],
            cwd=REPO,
            env=dict(os.environ, CGO_ENABLED="0"),
        )
    finally:
        zpath.unlink(missing_ok=True)
    (SINGLE_OUT / "SHA256SUMS").write_text(f"{sha256(exe)}  Stowline Setup.exe\n", encoding="ascii")
    return exe


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--server", default="", help="control plane URL, https://... (omit for a generic package)")
    ap.add_argument("--gateway", default="", help="gateway URL (default: <server>/gw)")
    ap.add_argument("--sites", default="", help="sites/departments JSON (see deployments/sites.example.json)")
    ap.add_argument("--build", default="", help="scripts/build-release.ps1 output folder")
    ap.add_argument("--bin-dir", default="", help="folder holding restic.exe and rclone.exe")
    ap.add_argument("--no-single-exe", action="store_true", help="skip the one-file Stowline Setup.exe")
    args = ap.parse_args()

    server = args.server.rstrip("/")
    if server and not server.startswith("https://"):
        raise SystemExit("--server must be an https:// URL")
    gateway = (args.gateway or (server + "/gw" if server else "")).rstrip("/")
    manifest = json.loads(MANIFEST.read_text(encoding="utf-8"))
    build = find_build(args.build)
    restic = find_pinned(args.bin_dir, "restic.exe", manifest["restic_sha256"])
    rclone = find_pinned(args.bin_dir, "rclone.exe", manifest["rclone_sha256"])

    if OUT.exists():
        shutil.rmtree(OUT)
    payload = OUT / "payload"
    CACHE.mkdir(parents=True, exist_ok=True)
    embedded_python(payload / "runtime")

    app_dst = payload / "app"
    app_dst.mkdir(parents=True)
    for name in APP_FILES:
        shutil.copy2(REPO / "server" / "app" / name, app_dst / name)
    if args.sites:
        json.loads(Path(args.sites).read_text(encoding="utf-8"))  # must parse
        shutil.copy2(args.sites, app_dst / "sites.json")

    wiz_src = REPO / "tools" / "stowline-setup"
    wiz_dst = payload / "wizard"
    (wiz_dst / "static").mkdir(parents=True)
    for name in ("wizard.py", "bootstrap.py", "remove_stowline.ps1"):
        shutil.copy2(wiz_src / name, wiz_dst / name)
    for name in ("wizard.html", "i18n.js"):
        shutil.copy2(wiz_src / "static" / name, wiz_dst / "static" / name)

    bin_dst = payload / "bin"
    bin_dst.mkdir(parents=True)
    for src, name in ((build / "stowline-agent.exe", "stowline-agent.exe"), (restic, "restic.exe"), (rclone, "rclone.exe"),
                      (build / "workspace-repo-init.exe", "workspace-repo-init.exe"), (build / "Stowline Backups.exe", "Stowline Backups.exe")):
        shutil.copy2(src, bin_dst / name)
    pins = {name: sha256(bin_dst / name) for name in ("stowline-agent.exe", "restic.exe", "rclone.exe", "workspace-repo-init.exe")}
    (payload / "pins.json").write_text(json.dumps(pins, indent=2) + "\n", encoding="utf-8")

    shutil.copy2(build / "Stowline Setup.exe", OUT / "Stowline Setup.exe")
    for name in ("Setup.cmd", "Remove.cmd"):
        shutil.copy2(wiz_src / name, OUT / name)
    write_text(
        OUT / "Verify.cmd",
        "@echo off\r\nsetlocal\r\ncd /d \"%~dp0\"\r\n"
        "set \"PATH=%~dp0payload\\runtime;C:\\Windows\\System32;C:\\Windows\"\r\n"
        "set \"STOWLINE_SETUP_ROOT=%~dp0payload\"\r\n"
        "\"%~dp0payload\\runtime\\python.exe\" \"%~dp0payload\\wizard\\bootstrap.py\" --verify-only\r\n",
    )
    deploy = {"schema_version": 1, "allow_localhost": False, "lab_insecure_http": False}
    if server:
        deploy.update({"control_plane_url": server, "gateway_url": gateway})
    (OUT / "deploy.json").write_text(json.dumps(deploy, indent=2) + "\n", encoding="utf-8")
    (OUT / "VERSION.json").write_text(json.dumps({"product": "stowline", "version": manifest.get("version", ""), "embedded_python": PY_VER, "pins": pins}, indent=2) + "\n", encoding="utf-8")
    write_text(OUT / "README.txt", README.format(server=server or "asked during setup"))

    lines = [f"{sha256(p)}  {p.relative_to(OUT).as_posix()}" for p in sorted(OUT.rglob("*")) if p.is_file()]
    (OUT / "checksums.txt").write_text("\n".join(lines) + "\n", encoding="utf-8")
    print(f"PACKED {OUT}")
    if not args.no_single_exe:
        print(f"SINGLE_EXE={build_single_exe()}")
    print(f"AGENT_SHA={pins['stowline-agent.exe']}")


README = """Stowline installer -- server {server}
=================================================

The computer being set up needs only Windows, an administrator account (UAC)
and outbound HTTPS. No Python, Git or Go.

1. Double-click "Stowline Setup.exe" and approve the administrator prompt.
   (Fallback: right-click Setup.cmd -> Run as administrator.)
2. The wizard checks the connection to the server ({server}); it will not
   continue while that check fails.
3. Pick the site, department and a display name, sign in with an admin
   panel account, choose the folders and the daily backup time.
4. Install. The service is installed and left stopped; click "Start the
   first backup now" on the last screen to begin.

To remove Stowline from a computer (service + C:\\Stowline), right-click
Remove.cmd -> Run as administrator. The next setup enrolls it as a new
device.

No secrets are in this package: the admin sign-in mints a one-time
enrollment token at install time.
"""


if __name__ == "__main__":
    main()
