"""Execute guide rendering: commands must come from a confirmed location."""
from __future__ import annotations

import json
from pathlib import Path
import shutil
import subprocess

import pytest

NODE = shutil.which("node")
pytestmark = pytest.mark.skipif(NODE is None, reason="node is required for the JavaScript rendering check")
SOURCE = Path(__file__).resolve().parents[1] / "app" / "static" / "js" / "vault.js"


def render(location):
    script = """
const fs = require('fs');
const vm = require('vm');
const ctx = {Pages: {}, UI: {card: 'card'}, icon: () => '',
  esc: x => String(x).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;')};
vm.createContext(ctx);
vm.runInContext(fs.readFileSync(process.argv[1], 'utf8'), ctx);
ctx.location = JSON.parse(process.argv[2]);
process.stdout.write(vm.runInContext('recoveryGuide({id: "a", hostname: "NEW-PC", department: "NEW-DEPT"}, location)', ctx));
"""
    return subprocess.run([NODE, "-e", script, str(SOURCE), json.dumps(location)], check=True, capture_output=True, encoding="utf-8").stdout


def test_remote_command_uses_exact_verified_location_with_literal_powershell_quoting():
    location = "custom-archive:backup/Old Dept/PC'$(`x)--aaaaaaaa/11111111"
    html = render({"available": True, "storage_kind": "rclone", "location": location})
    assert "rclone copy 'custom-archive:backup/Old Dept/PC''$(`x)--aaaaaaaa/11111111'" in html
    assert "stowline-drive:" not in html
    assert "NEW-PC" not in html and "NEW-DEPT" not in html
    assert "stowline-recovery.exe verify" in html
    assert "<pre data-no-i18n" in html
    assert "<span data-no-i18n" in html


def test_unresolved_location_contains_explanation_and_no_recovery_commands():
    html = render({"available": False, "reason": "generation_mismatch"})
    assert "nesilleri uyuşmuyor" in html
    assert "Konum doğrulanmadan" in html
    assert "rclone copy" not in html
    assert "stowline-recovery.exe" not in html
    assert "RESTIC_PASSWORD" not in html


def test_local_gateway_path_requires_export_instead_of_remote_copy():
    html = render({"available": True, "storage_kind": "local", "location": "/data/custom/Old Dept/PC/11111111"})
    assert "/data/custom/Old Dept/PC/11111111" in html
    assert "gateway dosya sistemindedir" in html
    assert "rclone copy" not in html
    assert "stowline-recovery.exe verify" in html
