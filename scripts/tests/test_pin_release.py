"""Release bundle trust checks; all test agents/evidence are synthetic fixtures."""
from __future__ import annotations

import hashlib
import importlib.util
import json
import subprocess
import sys
from pathlib import Path

import pytest

REPO = Path(__file__).resolve().parents[2]


def module(name):
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), REPO / "scripts" / f"{name}.py")
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


@pytest.fixture
def pin(tmp_path, monkeypatch):
    p = module("pin-release")
    staged = tmp_path / "release" / "bin" / "stowline-agent.exe"
    staged.parent.mkdir(parents=True)
    staged.write_bytes(b"old build")
    manifest = staged.parent.parent / "manifest.json"
    manifest.write_text(json.dumps({"version": "0.1.1", "agent_sha256": p.sha256(staged), "agent_qualified": True, "canary": {"device_ids": ["old-test-device"]}}))
    monkeypatch.setattr(p, "STAGED", staged)
    monkeypatch.setattr(p, "MANIFEST", manifest)
    return p


def invoke(pin, monkeypatch, *args):
    monkeypatch.setattr(sys, "argv", ["pin-release.py", *map(str, args)])
    return pin.main()


def test_staging_binds_the_exact_binary_and_version_and_clears_qualification(pin, monkeypatch, tmp_path):
    source = tmp_path / "new.exe"
    source.write_bytes(b"new release bytes")
    calls = []

    def run(argv, **kwargs):
        calls.append(argv)
        return subprocess.CompletedProcess(argv, 0, stdout="stowline-agent 0.2.3\n")

    monkeypatch.setattr(pin.subprocess, "run", run)
    invoke(pin, monkeypatch, "--agent", source, "--version", "0.2.3")
    data = json.loads(pin.MANIFEST.read_text())
    assert data["version"] == "0.2.3"
    assert data["agent_sha256"] == hashlib.sha256(source.read_bytes()).hexdigest()
    assert pin.STAGED.read_bytes() == source.read_bytes()
    assert data["agent_qualified"] is False
    assert "canary" not in data
    assert calls == [[str(source.resolve()), "version"]]
    assert invoke(pin, monkeypatch, "--verify") == 0


def test_staging_a_wrong_version_leaves_the_existing_bundle_untouched(pin, monkeypatch, tmp_path):
    source = tmp_path / "new.exe"
    source.write_bytes(b"other bytes")
    original = pin.MANIFEST.read_bytes(), pin.STAGED.read_bytes()
    monkeypatch.setattr(pin.subprocess, "run", lambda *a, **k: subprocess.CompletedProcess(a, 0, stdout="stowline-agent 9.9.9\n"))
    with pytest.raises(ValueError, match="version does not match"):
        invoke(pin, monkeypatch, "--agent", source, "--version", "0.2.3")
    assert (pin.MANIFEST.read_bytes(), pin.STAGED.read_bytes()) == original


@pytest.mark.parametrize("pin_value", ["", "b" * 64, "z" * 64])
def test_corrupt_or_empty_pins_cannot_be_verified_or_qualified(pin, monkeypatch, tmp_path, pin_value):
    data = json.loads(pin.MANIFEST.read_text())
    data["agent_sha256"] = pin_value
    pin.MANIFEST.write_text(json.dumps(data))
    with pytest.raises(ValueError, match="SHA-256"):
        invoke(pin, monkeypatch, "--verify")
    with pytest.raises(ValueError, match="SHA-256"):
        invoke(pin, monkeypatch, "--qualify", "--qualification", tmp_path / "missing.json")


def test_a_qualify_flag_alone_cannot_promote_an_agent(pin, monkeypatch):
    with pytest.raises(SystemExit):
        invoke(pin, monkeypatch, "--qualify")


def report(pin, tmp_path):
    data = json.loads(pin.MANIFEST.read_text())
    evidence = tmp_path / "synthetic-test-evidence.txt"
    evidence.write_text("Synthetic test fixture only; no real Windows qualification was performed.\n")
    document = {"schema": "stowline.windows-release-qualification.v1", "agent_sha256": data["agent_sha256"], "version": data["version"], "platform": "windows", "windows_version": "test fixture", "device_id": "synthetic", "operator": "unit-test", "checks": {name: {"result": "PASS", "evidence_file": evidence.name, "evidence_sha256": pin.sha256(evidence)} for name in pin.CHECKS}}
    return document


@pytest.mark.parametrize("mutation", ["old-hash", "linux", "missing-check", "failed-check", "missing-file", "tampered-evidence", "bad-schema"])
def test_qualification_rejects_incomplete_or_mismatched_evidence(pin, tmp_path, mutation):
    data = json.loads(pin.MANIFEST.read_text())
    document = report(pin, tmp_path)
    check = document["checks"][pin.CHECKS[0]]
    if mutation == "old-hash":
        document["agent_sha256"] = "0" * 64
    elif mutation == "linux":
        document["platform"] = "linux"
    elif mutation == "missing-check":
        document["checks"].pop(pin.CHECKS[0])
    elif mutation == "failed-check":
        check["result"] = "FAIL"
    elif mutation == "missing-file":
        check["evidence_file"] = "missing.txt"
    elif mutation == "tampered-evidence":
        check["evidence_sha256"] = "0" * 64
    elif mutation == "bad-schema":
        document["schema"] = "unknown"
    path = tmp_path / "report.json"
    path.write_text(json.dumps(document))
    with pytest.raises(ValueError):
        pin.qualification(path, data)


def test_qualification_records_the_reviewed_report_hash(pin, tmp_path):
    path = tmp_path / "report.json"
    path.write_text(json.dumps(report(pin, tmp_path)))
    assert pin.qualification(path, json.loads(pin.MANIFEST.read_text())) == pin.sha256(path)


def test_installer_refuses_an_agent_different_from_the_release_pin(tmp_path):
    package = module("package-installer")
    (tmp_path / "stowline-agent.exe").write_bytes(b"wrong release")
    with pytest.raises(SystemExit, match="does not match"):
        package.verify_release_agent(tmp_path, {"agent_sha256": "a" * 64})
