"""Stage a versioned agent, or promote its exact bytes after Windows qualification.

Staging clears qualification. CI never qualifies agents. See docs/RELEASE-UPDATES.md.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import re
import shutil
import subprocess
from pathlib import Path

REPO = Path(__file__).resolve().parents[1]
MANIFEST = REPO / "release" / "manifest.json"
STAGED = REPO / "release" / "bin" / "stowline-agent.exe"
CHECKS = (
    "backup_restore_bytes", "independent_restore_bytes", "enrollment_preserved",
    "busy_backup_deferred", "busy_restore_deferred", "unhealthy_rollback",
)


def sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def require_version(agent: Path, version: str) -> None:
    if not re.fullmatch(r"\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?", version):
        raise ValueError("a semantic --version without v is required")
    result = subprocess.run([str(agent.resolve()), "version"], capture_output=True, text=True, timeout=30, check=True)
    if result.stdout.strip() != f"stowline-agent {version}":
        raise ValueError("agent binary version does not match the release version")


def verify_pin(data: dict, agent: Path) -> None:
    expected = data.get("agent_sha256", "")
    if not re.fullmatch(r"[0-9a-f]{64}", str(expected)) or sha256(agent) != expected:
        raise ValueError("staged agent does not match manifest SHA-256")


def qualification(report_path: Path, data: dict) -> str:
    report = json.loads(report_path.read_text(encoding="utf-8"))
    if report.get("schema") != "stowline.windows-release-qualification.v1":
        raise ValueError("unknown Windows qualification report schema")
    if report.get("agent_sha256") != data["agent_sha256"] or report.get("version") != data["version"]:
        raise ValueError("qualification report belongs to a different agent/version")
    if report.get("platform") != "windows" or not report.get("windows_version") or not report.get("device_id") or not report.get("operator"):
        raise ValueError("real Windows device and operator details are required")
    for name in CHECKS:
        check = (report.get("checks") or {}).get(name) or {}
        if check.get("result") != "PASS":
            raise ValueError(f"Windows qualification check not passed: {name}")
        relative = Path(str(check.get("evidence_file") or ""))
        evidence = (report_path.parent / relative).resolve()
        if relative.is_absolute() or not evidence.is_relative_to(report_path.parent.resolve()):
            raise ValueError(f"evidence file must be within the report directory: {name}")
        if not evidence.is_file() or not evidence.stat().st_size or sha256(evidence) != check.get("evidence_sha256"):
            raise ValueError(f"missing or mismatched qualification evidence: {name}")
    return sha256(report_path)


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--agent", default="", help="stage a new agent (clears qualification)")
    ap.add_argument("--version", default="", help="version printed by the agent being staged")
    ap.add_argument("--qualify", action="store_true", help="promote the existing pin after real Windows tests")
    ap.add_argument("--qualification", type=Path, help="operator-reviewed Windows qualification report")
    ap.add_argument("--verify", action="store_true", help="check staged bytes against manifest without executing them")
    args = ap.parse_args()
    data = json.loads(MANIFEST.read_text(encoding="utf-8"))
    if args.verify:
        if args.agent or args.qualify or args.version or args.qualification:
            ap.error("--verify cannot be combined with staging or qualification")
        verify_pin(data, STAGED)
        print("release agent SHA-256 verified")
        return 0
    if args.agent:
        if args.qualify or args.qualification:
            ap.error("stage first, test the staged bytes on Windows, then qualify separately")
        source = Path(args.agent)
        require_version(source, args.version or data["version"])
        STAGED.parent.mkdir(parents=True, exist_ok=True)
        if source.resolve() != STAGED.resolve():
            shutil.copy2(source, STAGED)
        data.update(version=args.version or data["version"], agent_sha256=sha256(STAGED), agent_qualified=False)
        data.pop("agent_qualification_sha256", None)
        data.pop("canary", None)
    elif args.qualify:
        if not args.qualification or args.version:
            ap.error("--qualify requires --qualification and promotes only the existing version")
        verify_pin(data, STAGED)
        require_version(STAGED, data["version"])
        data["agent_qualification_sha256"] = qualification(args.qualification, data)
        data["agent_qualified"] = True
    else:
        ap.error("use --agent to stage, --verify to check, or --qualify with Windows evidence")
    MANIFEST.write_text(json.dumps(data, indent=2) + "\n", encoding="utf-8", newline="\n")
    print(f"version={data['version']} agent_sha256={data['agent_sha256']} agent_qualified={data['agent_qualified']}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
