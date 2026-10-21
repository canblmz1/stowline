"""Bundle one built, unqualified Windows candidate; never publish or qualify it."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import xml.etree.ElementTree as ET
import zipfile

REPO = Path(__file__).resolve().parents[1]


def digest(path: Path) -> str:
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def installer_results(path: Path) -> int:
    suites = list(ET.parse(path).getroot().iter("testsuite"))
    count = sum(int(s.get("tests", "0")) for s in suites)
    if count < 4 or any(int(s.get(key, "0")) for s in suites for key in ("failures", "errors", "skipped")):
        raise ValueError("full-installer runtime checks must pass without skips")
    return count


def prepare(repo: Path, out: Path, source_commit: str, results: Path) -> dict:
    if not re.fullmatch(r"[0-9a-f]{40}", source_commit):
        raise ValueError("exact source commit required")
    manifest_path = repo / "release/manifest.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    version = manifest["version"]
    if not re.fullmatch(r"\d+\.\d+\.\d+-[0-9A-Za-z.-]+", version):
        raise ValueError("an explicit prerelease candidate version is required")
    if manifest.get("agent_qualified") is not False or manifest.get("canary") or manifest.get("agent_qualification_sha256"):
        raise ValueError("candidate must remain unqualified without a canary offer")
    installer = repo / "releases/installer"
    payload = installer / "payload"
    pins = json.loads((payload / "pins.json").read_text())
    package = json.loads((installer / "VERSION.json").read_text())
    if package.get("version") != version or package.get("pins") != pins:
        raise ValueError("installer version/pins differ from the candidate")
    for name in ("agent", "restic", "rclone"):
        binary = f"stowline-{name}.exe" if name == "agent" else f"{name}.exe"
        expected = manifest[f"{name}_sha256"]
        if not re.fullmatch(r"[0-9a-f]{64}", expected) or pins.get(binary) != expected or digest(payload / "bin" / binary) != expected:
            raise ValueError(f"installer {name} differs from manifest")
    agent = repo / "release/bin/stowline-agent.exe"
    if digest(agent) != manifest["agent_sha256"] or digest(repo / "releases/build/stowline-agent.exe") != manifest["agent_sha256"]:
        raise ValueError("server/build agent differs from manifest")
    count = installer_results(results)
    single = repo / "releases/setup-single/Stowline Setup.exe"
    recovery = repo / "releases/build/stowline-recovery.exe"
    if not single.is_file() or not recovery.is_file():
        raise ValueError("single-file installer and recovery tool must be built")
    # Never remove/overwrite a previous candidate, even when its version matches.
    out.mkdir(parents=True, exist_ok=False)
    shutil.copy2(single, out / f"Stowline-Setup-{version}.exe")
    shutil.copy2(agent, out / "stowline-agent.exe")
    shutil.copy2(recovery, out / "stowline-recovery.exe")
    shutil.copy2(manifest_path, out / "manifest.json")
    shutil.copy2(results, out / "installer-runtime-results.xml")
    with zipfile.ZipFile(out / "server-release.zip", "w", zipfile.ZIP_DEFLATED) as archive:
        archive.write(manifest_path, "manifest.json")
        archive.write(agent, "bin/stowline-agent.exe")
    with zipfile.ZipFile(out / f"Stowline-Portable-{version}.zip", "w", zipfile.ZIP_DEFLATED) as archive:
        for path in sorted(installer.rglob("*")):
            if path.is_file():
                archive.write(path, path.relative_to(installer).as_posix())
    files = {p.name: {"sha256": digest(p), "size": p.stat().st_size} for p in sorted(out.iterdir())}
    record = {
        "schema": "stowline.windows-qualification-candidate.v1",
        "source_commit": source_commit,
        "version": version,
        "agent_sha256": manifest["agent_sha256"],
        "agent_qualified": False,
        "files": files,
        "automated_checks": {"installer_runtime_passed": count, "manifest_payload_match": "PASS"},
        "windows_qualification": {"status": "NOT_RUN", "reason": "Requires an authorized, isolated Windows service/VSS test environment"},
        "ci_run_url": f"{os.environ.get('GITHUB_SERVER_URL', 'https://github.com')}/{os.environ.get('GITHUB_REPOSITORY', 'canblmz1/stowline')}/actions/runs/{os.environ.get('GITHUB_RUN_ID', '')}",
    }
    (out / "candidate.json").write_text(json.dumps(record, indent=2) + "\n", encoding="utf-8")
    sums = [f"{digest(p)}  {p.name}" for p in sorted(out.iterdir()) if p.is_file()]
    (out / "SHA256SUMS.txt").write_text("\n".join(sums) + "\n", encoding="ascii")
    return record


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", type=Path, default=REPO / "releases/qualification-candidate")
    parser.add_argument("--results", type=Path, required=True)
    args = parser.parse_args()
    source = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=REPO, text=True).strip()
    modified = subprocess.check_output(["git", "diff", "--name-only", "HEAD"], cwd=REPO, text=True).splitlines()
    untracked = subprocess.check_output(["git", "ls-files", "--others", "--exclude-standard"], cwd=REPO, text=True).strip()
    if set(modified) - {"release/manifest.json"} or untracked:
        raise ValueError("source must be clean except the generated release manifest")
    record = prepare(REPO, args.out, source, args.results)
    print(json.dumps({key: record[key] for key in ("source_commit", "version", "agent_sha256", "agent_qualified")}))


if __name__ == "__main__":
    main()
