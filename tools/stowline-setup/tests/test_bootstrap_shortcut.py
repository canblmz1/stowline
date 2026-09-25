from __future__ import annotations

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import bootstrap as b


def test_ps_quote_escapes_embedded_single_quotes():
    assert b._ps_quote("C:\\Stowline\\Setup.cmd") == "'C:\\Stowline\\Setup.cmd'"
    assert b._ps_quote("O'Brien") == "'O''Brien'"


def test_create_start_menu_shortcut_skips_silently_when_setup_cmd_missing(tmp_path, monkeypatch):
    calls = []
    monkeypatch.setattr(b.subprocess, "run", lambda *a, **k: calls.append((a, k)))
    b.create_start_menu_shortcut(tmp_path / "does-not-exist" / "Setup.cmd")
    assert calls == []


def test_create_start_menu_shortcut_never_raises_when_powershell_fails(tmp_path, monkeypatch):
    setup_cmd = tmp_path / "Setup.cmd"
    setup_cmd.write_text("@echo off\n", encoding="utf-8")
    monkeypatch.setenv("ProgramData", str(tmp_path / "ProgramData"))

    def boom(*a, **k):
        raise b.subprocess.TimeoutExpired(cmd=a, timeout=30)

    monkeypatch.setattr(b.subprocess, "run", boom)
    b.create_start_menu_shortcut(setup_cmd)  # must not raise


def test_create_start_menu_shortcut_invokes_powershell_with_the_right_target(tmp_path, monkeypatch):
    setup_cmd = tmp_path / "Setup.cmd"
    setup_cmd.write_text("@echo off\n", encoding="utf-8")
    monkeypatch.setenv("ProgramData", str(tmp_path / "ProgramData"))

    calls = []

    class _FakeCompleted:
        returncode = 0
        stdout = ""
        stderr = ""

    def fake_run(cmd, **kwargs):
        calls.append(cmd)
        return _FakeCompleted()

    monkeypatch.setattr(b.subprocess, "run", fake_run)
    b.create_start_menu_shortcut(setup_cmd)

    assert len(calls) == 1
    script = calls[0][-1]
    assert "CreateShortcut" in script
    assert "Stowline" in script or str(setup_cmd) in script
    assert str(setup_cmd) in script
    start_menu = tmp_path / "ProgramData" / "Microsoft" / "Windows" / "Start Menu" / "Programs"
    assert start_menu.is_dir()


def _fake_runner(calls):
    class _Done:
        returncode = 0
        stdout = ""
        stderr = ""

    def run(cmd, **kwargs):
        calls.append(cmd)
        return _Done()

    return run


def test_user_app_shortcut_opens_the_local_page_in_an_app_window_on_desktop_and_start_menu(tmp_path, monkeypatch):
    edge = tmp_path / "msedge.exe"
    edge.write_text("", encoding="utf-8")
    monkeypatch.setattr(b, "EDGE_PATHS", [edge])
    monkeypatch.setattr(b, "USER_APP", tmp_path / "no-user-app.exe")
    monkeypatch.setenv("ProgramData", str(tmp_path / "ProgramData"))
    monkeypatch.setenv("PUBLIC", str(tmp_path / "Public"))
    calls = []
    monkeypatch.setattr(b.subprocess, "run", _fake_runner(calls))
    b.create_user_app_shortcuts()
    scripts = [c[-1] for c in calls]
    assert len(scripts) == 2
    for s in scripts:
        assert str(edge) in s
        assert "--app=http://127.0.0.1:18080/" in s
        assert "Stowline Backups" in s
    assert any(str(tmp_path / "Public" / "Desktop") in s for s in scripts)
    assert any("Start Menu" in s for s in scripts)


def test_user_app_shortcut_is_skipped_without_edge_and_never_raises(tmp_path, monkeypatch):
    monkeypatch.setattr(b, "EDGE_PATHS", [tmp_path / "missing.exe"])
    monkeypatch.setattr(b, "USER_APP", tmp_path / "no-user-app.exe")
    calls = []
    monkeypatch.setattr(b.subprocess, "run", _fake_runner(calls))
    b.create_user_app_shortcuts()
    assert calls == []


def test_user_app_shortcut_never_raises_when_powershell_fails(tmp_path, monkeypatch):
    edge = tmp_path / "msedge.exe"
    edge.write_text("", encoding="utf-8")
    monkeypatch.setattr(b, "EDGE_PATHS", [edge])
    monkeypatch.setattr(b, "USER_APP", tmp_path / "no-user-app.exe")
    monkeypatch.setenv("ProgramData", str(tmp_path / "ProgramData"))
    monkeypatch.setenv("PUBLIC", str(tmp_path / "Public"))

    def boom(*a, **k):
        raise OSError("no powershell")

    monkeypatch.setattr(b.subprocess, "run", boom)
    b.create_user_app_shortcuts()  # must not raise


def test_user_app_shortcut_prefers_the_installed_yedeklerim_exe(tmp_path, monkeypatch):
    app = tmp_path / "Stowline Backups.exe"
    app.write_text("", encoding="utf-8")
    edge = tmp_path / "msedge.exe"
    edge.write_text("", encoding="utf-8")
    monkeypatch.setattr(b, "EDGE_PATHS", [edge])
    monkeypatch.setattr(b, "USER_APP", tmp_path / "no-user-app.exe")
    monkeypatch.setattr(b, "USER_APP", app)
    monkeypatch.setenv("ProgramData", str(tmp_path / "ProgramData"))
    monkeypatch.setenv("PUBLIC", str(tmp_path / "Public"))
    calls = []
    monkeypatch.setattr(b.subprocess, "run", _fake_runner(calls))
    b.create_user_app_shortcuts()
    assert len(calls) == 2
    for cmd in calls:
        script = cmd[-1]
        assert str(app) in script
        assert "--app=" not in script
        assert str(edge) not in script


def test_wizard_args_forward_the_desktop_installer_options():
    assert b.wizard_passthrough(["--no-browser", "--port", "18999", "--verify-only"]) == ["--no-browser", "--port", "18999"]
    assert b.wizard_passthrough([]) == []
