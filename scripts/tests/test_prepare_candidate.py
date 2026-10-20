"""Candidate records must describe actual unqualified installer/server bytes."""
from __future__ import annotations

import importlib.util
import json
from pathlib import Path
import zipfile

import pytest


@pytest.fixture
def bundle(tmp_path):
    source = Path(__file__).resolve().parents[1] / "prepare-candidate.py"
    spec = importlib.util.spec_from_file_location("prepare_candidate", source)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    repo, out = tmp_path / "repo", tmp_path / "candidate"

    def write(name, content):
        path = repo / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)
        return path

    manifest = {"version": "0.1.2-rc.1", "agent_qualified": False}
    pins = {}
    for name in ("agent", "restic", "rclone"):
        filename = "stowline-agent.exe" if name == "agent" else f"{name}.exe"
        path = write(f"releases/installer/payload/bin/{filename}", name.encode())
        pins[filename] = manifest[f"{name}_sha256"] = module.digest(path)
    write("release/bin/stowline-agent.exe", b"agent")
    write("releases/build/stowline-agent.exe", b"agent")
    write("releases/build/stowline-recovery.exe", b"synthetic recovery")
    write("releases/setup-single/Stowline Setup.exe", b"synthetic installer")
    write("release/manifest.json", json.dumps(manifest).encode())
    write("releases/installer/payload/pins.json", json.dumps(pins).encode())
    write("releases/installer/VERSION.json", json.dumps({"version": manifest["version"], "pins": pins}).encode())
    results = write("results.xml", b'<testsuites><testsuite tests="4" failures="0" errors="0" skipped="0"/></testsuites>')
    return module, repo, out, results


def test_exact_candidate_is_bound_to_installer_server_and_source(bundle):
    module, repo, out, results = bundle
    record = module.prepare(repo, out, "a" * 40, results)
    assert record["source_commit"] == "a" * 40
    assert record["agent_qualified"] is False
    assert record["windows_qualification"]["status"] == "NOT_RUN"
    assert record["automated_checks"]["installer_runtime_passed"] == 4
    for name, info in record["files"].items():
        assert module.digest(out / name) == info["sha256"]
    with zipfile.ZipFile(out / "server-release.zip") as archive:
        manifest = json.loads(archive.read("manifest.json"))
        assert manifest["agent_sha256"] == record["agent_sha256"]
        assert archive.read("bin/stowline-agent.exe") == (out / "stowline-agent.exe").read_bytes()
    with zipfile.ZipFile(out / "Stowline-Portable-0.1.2-rc.1.zip") as archive:
        assert archive.read("payload/bin/stowline-agent.exe") == (out / "stowline-agent.exe").read_bytes()


@pytest.mark.parametrize("mutation", ["payload", "server", "build", "qualified", "version", "skipped", "failed"])
def test_incomplete_or_mismatched_candidate_is_not_emitted(bundle, mutation):
    module, repo, out, results = bundle
    if mutation in ("payload", "server", "build"):
        path = {"payload": "releases/installer/payload/bin/stowline-agent.exe", "server": "release/bin/stowline-agent.exe", "build": "releases/build/stowline-agent.exe"}[mutation]
        (repo / path).write_bytes(b"corrupt")
    elif mutation == "qualified":
        path = repo / "release/manifest.json"
        data = json.loads(path.read_text())
        data["agent_qualified"] = True
        path.write_text(json.dumps(data))
    elif mutation == "version":
        path = repo / "releases/installer/VERSION.json"
        data = json.loads(path.read_text())
        data["version"] = "0.1.1"
        path.write_text(json.dumps(data))
    else:
        results.write_text(f'<testsuites><testsuite tests="4" failures="{int(mutation == "failed")}" skipped="{int(mutation == "skipped")}"/></testsuites>')
    with pytest.raises(ValueError):
        module.prepare(repo, out, "a" * 40, results)
    assert not out.exists()


def test_existing_candidate_is_never_overwritten(bundle):
    module, repo, out, results = bundle
    out.mkdir()
    (out / "previous").write_bytes(b"keep this evidence")
    with pytest.raises(FileExistsError):
        module.prepare(repo, out, "a" * 40, results)
    assert list(out.iterdir()) == [out / "previous"]
    assert (out / "previous").read_bytes() == b"keep this evidence"
