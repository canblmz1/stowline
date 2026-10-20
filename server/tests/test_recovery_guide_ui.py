"""Execute guide rendering: commands must come from a confirmed location."""
from __future__ import annotations

import json
import os
from html import unescape
from pathlib import Path
import re
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
    return unescape(subprocess.run([NODE, "-e", script, str(SOURCE), json.dumps(location)], check=True, capture_output=True, encoding="utf-8").stdout)


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


def test_restore_selects_listed_subtree_and_requires_staged_content_verification():
    html = render({"available": True, "storage_kind": "rclone", "location": "archive:recorded/path/generation"})
    assert "restic.exe --repo D:\\independent\\repo-copy ls $Snapshot" in html
    assert '$Selector = "${Snapshot}:$SnapshotPath"' in html
    assert "--snapshot $Selector" in html
    assert "--snapshot '<snapshot-id>:/<folder>'" in html
    assert "--snapshot <snapshot-id> --staging" not in html
    assert "Get-FileHash -Algorithm SHA256" in html
    assert "beklenen byte/içerik" in html
    assert "Repository check işlemi" in html


def test_backup_key_is_prompted_securely_and_environment_is_cleared_in_finally():
    html = render({"available": True, "storage_kind": "rclone", "location": "archive:recorded/path/generation"})
    assert "Read-Host 'Repository backup key' -AsSecureString" in html
    assert "[System.Net.NetworkCredential]::new('', $RecoveryKey).Password" in html
    assert "} finally {" in html
    assert "Remove-Item Env:RESTIC_PASSWORD -ErrorAction SilentlyContinue" in html
    assert "$RecoveryKey.Dispose()" in html
    assert '<kasadan gösterdiğiniz anahtar>' not in html
    assert "$LASTEXITCODE -ne 0" in html


@pytest.mark.skipif(shutil.which("powershell") is None, reason="Windows PowerShell 5.1 is required for source argument roundtrip")
@pytest.mark.parametrize("name", ["O'Brien", "PC\u2018name", "PC\u2019name", "PC\u201aname", "PC\u201bname", "PC'\u2018\u2019\u201a\u201bname"])
def test_generated_powershell_passes_repository_source_as_one_literal_argument(tmp_path, name):
    source = "archive:backups/" + name + "/Türkçe boşluk $HOME `$value $($global:SourceExpressionRan = $true) $(Invoke-SyntheticSourceCommand)/generation"
    html = render({"available": True, "storage_kind": "rclone", "location": source})
    commands = re.search(r"<pre data-no-i18n[^>]*>(.*?)</pre>", html, re.S).group(1)
    # Execute the actual guide block. Harmless functions capture rclone's
    # arguments and stop before reading a key or running a recovery engine.
    script = """
$CaptureFile = $args[0]
if ($PSVersionTable.PSVersion.Major -ne 5 -or $PSVersionTable.PSVersion.Minor -ne 1) {
    throw 'This regression requires Windows PowerShell 5.1'
}
$global:SourceExpressionRan = $false
$global:SourceCommandRan = $false
$script:RcloneArgs = @()
function global:Invoke-SyntheticSourceCommand { $global:SourceCommandRan = $true }
function global:rclone {
    $script:RcloneArgs = @($args)
    $global:LASTEXITCODE = 0
}
function global:stowline-recovery.exe { throw 'synthetic-source-captured-stop' }
$stopped = $false
try {
    & {
""" + commands + """
    }
} catch {
    if ($_.Exception.Message -ne 'synthetic-source-captured-stop') { throw }
    $stopped = $true
}
if (-not $stopped) { throw 'Expected synthetic recovery stop' }
$capture = @{
    arguments = @($script:RcloneArgs)
    expression_ran = $global:SourceExpressionRan
    command_ran = $global:SourceCommandRan
    powershell_version = $PSVersionTable.PSVersion.ToString()
} | ConvertTo-Json
[System.IO.File]::WriteAllText($CaptureFile, $capture, [System.Text.Encoding]::UTF8)
"""
    path = tmp_path / "generated-source-roundtrip.ps1"
    captured = tmp_path / "captured.json"
    path.write_text(script, encoding="utf-8-sig")
    env = os.environ.copy()
    env.pop("PSModulePath", None)
    result = subprocess.run([shutil.which("powershell"), "-NoProfile", "-NonInteractive", "-File", str(path), str(captured)], capture_output=True, encoding="utf-8", errors="replace", env=env)
    assert result.returncode == 0, result.stdout + result.stderr
    data = json.loads(captured.read_text(encoding="utf-8-sig"))
    assert data["arguments"] == ["copy", source, "C:\\Recovery\\repo-copy"]
    assert data["expression_ran"] is False
    assert data["command_ran"] is False
    assert data["powershell_version"].startswith("5.1.")


@pytest.mark.skipif(shutil.which("powershell") is None, reason="Windows PowerShell is required for generated command execution")
def test_generated_powershell_clears_password_when_repository_check_fails(tmp_path, monkeypatch):
    html = render({"available": True, "storage_kind": "rclone", "location": "archive:recorded/path/generation"})
    commands = re.search(r"<pre data-no-i18n[^>]*>(.*?)</pre>", html, re.S).group(1)
    # Synthetic command doubles fail at verify, before any real restic runs.
    # This executes the displayed script's native-exit guard and finally path.
    script = """
function global:rclone { $global:LASTEXITCODE = 0 }
function global:stowline-recovery.exe {
    if ($args[0] -eq 'verify') { $global:LASTEXITCODE = 7 }
    else { $global:LASTEXITCODE = 0 }
}
function global:Read-Host([string]$Prompt, [switch]$AsSecureString) {
    ConvertTo-SecureString 'synthetic-backup-key' -AsPlainText -Force
}
$caught = $false
try {
    & {
""" + commands + """
    }
} catch {
    if ($_.Exception.Message -notlike '*Recovery command failed: 7*') { throw }
    $caught = $true
}
if (-not $caught) { throw 'Expected repository check failure' }
if (Test-Path Env:RESTIC_PASSWORD) { throw 'Backup password remained in environment' }
"""
    path = tmp_path / "generated-recovery-check.ps1"
    path.write_text(script, encoding="utf-8-sig")
    # A pwsh CI parent exports its .NET Core module directories. Windows
    # PowerShell 5.1 must discover its own modules instead of importing those.
    monkeypatch.setenv("PSModulePath", str(tmp_path / "foreign-shell-modules"))
    env = os.environ.copy()
    env.pop("PSModulePath", None)
    result = subprocess.run([shutil.which("powershell"), "-NoProfile", "-NonInteractive", "-File", str(path)], capture_output=True, encoding="utf-8", errors="replace", env=env)
    assert result.returncode == 0, result.stdout + result.stderr
