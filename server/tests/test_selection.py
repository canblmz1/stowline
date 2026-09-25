from __future__ import annotations

from pathlib import Path

import pytest

from app.selection import SelectionError, compile_selection, estimate_paths


def test_windows_drive_path_is_never_given_a_posix_cwd_prefix():
    """Regression: the control plane runs in a Linux container, where
    os.path.abspath() doesn't recognize "C:\\..." as absolute and silently
    prepends the container's CWD -- confirmed live as admin selections
    showing "/app/C:\\Users\\...\\file.pst". A Windows client path is
    already absolute on its own terms and must come back unchanged."""
    win_path = r"C:\Users\op\Desktop\report.docx"
    manifest = compile_selection(selected=[{"path": win_path, "kind": "file"}])
    assert manifest["source_roots"] == [win_path]


def test_windows_style_traversal_is_rejected_even_without_forward_slashes():
    """The old check used Path(raw).parts, which never splits on
    backslash on a POSIX host -- a "..\\.." segment would pass unnoticed
    in the one place (the Linux control plane) this path actually runs."""
    with pytest.raises(SelectionError):
        compile_selection(selected=[{"path": r"C:\Users\op\Desktop\..\..\Windows", "kind": "directory"}])


def test_estimate_paths_counts_a_real_nested_directory(tmp_path: Path):
    root = tmp_path / "docs"
    (root / "sub").mkdir(parents=True)
    (root / "a.txt").write_bytes(b"x" * 10)
    (root / "sub" / "b.txt").write_bytes(b"y" * 20)
    files, nbytes = estimate_paths([str(root)])
    assert files == 2
    assert nbytes == 30


def test_estimate_paths_uses_the_capped_reparse_skipping_walker(tmp_path: Path, monkeypatch):
    """Regression: this used to be a raw, uncapped os.walk() with no
    reparse-point skip -- unlike discovery.py's walk_files, which every
    other filesystem scan in this codebase goes through. A real
    Desktop/Documents tree is default-selected whenever it has office
    files, so this ran on every default install, and could run long
    enough that clicking Next looked frozen."""
    import app.selection as selection_module

    calls = []
    real_walk_files = selection_module.walk_files

    def spy(root, **kwargs):
        calls.append(root)
        return real_walk_files(root, **kwargs)

    monkeypatch.setattr(selection_module, "walk_files", spy)
    root = tmp_path / "docs"
    root.mkdir()
    (root / "a.txt").write_bytes(b"x")
    estimate_paths([str(root)])
    assert calls == [root]


def test_office_files_category_fallback_uses_the_capped_walker(tmp_path: Path, monkeypatch):
    import app.selection as selection_module

    calls = []
    real_walk_files = selection_module.walk_files

    def spy(root, **kwargs):
        calls.append(root)
        return real_walk_files(root, **kwargs)

    monkeypatch.setattr(selection_module, "walk_files", spy)
    root = tmp_path / "office"
    root.mkdir()
    (root / "report.docx").write_bytes(b"x")
    (root / "notes.txt").write_bytes(b"y")
    manifest = compile_selection(selected=[{"path": str(root), "kind": "category", "category": "office_files"}])
    assert calls == [root]
    assert manifest["selected_file_count"] == 1  # only the .docx, not the .txt


def test_office_files_category_fallback_survives_a_permission_error(tmp_path: Path, monkeypatch):
    """Regression: an uncaught OSError from the old raw rglob() (e.g. a
    permission-denied subdirectory) produced a raw ASGI 500 with no
    detail. compile_selection must degrade to "nothing found" instead of
    crashing the request."""
    import app.selection as selection_module

    def boom(root, **kwargs):
        raise PermissionError("simulated: access denied")

    monkeypatch.setattr(selection_module, "walk_files", boom)
    root = tmp_path / "locked"
    root.mkdir()
    (root / "placeholder.txt").write_bytes(b"x")
    try:
        compile_selection(selected=[{"path": str(root), "kind": "category", "category": "office_files"}])
    except selection_module.SelectionError as exc:
        assert "at least one backup path" in str(exc)  # nothing found, not a crash -- acceptable outcome
    else:
        pass  # also acceptable if some other selected path kept the manifest non-empty


def test_an_added_folder_is_a_real_root_so_later_files_are_backed_up_too(tmp_path: Path):
    """Regression: a folder added in the installer ("Başka klasör ekle")
    used to be saved as the list of files it held at install time, so
    anything created in it afterwards was never backed up."""
    root = tmp_path / "Project"
    root.mkdir()
    (root / "notes.txt").write_bytes(b"x")
    manifest = compile_selection(selected=[{"path": str(root), "kind": "directory", "category": "extra"}])
    assert manifest["source_roots"] == [str(root)]


def test_sensitive_files_under_a_selected_folder_are_excluded_not_backed_up(tmp_path: Path):
    """The folder is a root, but credentials under it stay out unless their
    own candidate is selected and confirmed; OST caches are always out."""
    root = tmp_path / "Project"
    (root / "sub").mkdir(parents=True)
    (root / "notes.txt").write_bytes(b"x")
    (root / ".env").write_text("SECRET=1\n", encoding="utf-8")
    (root / "sub" / "server.pem").write_text("k", encoding="utf-8")
    manifest = compile_selection(selected=[{"path": str(root), "kind": "directory", "category": "extra"}])
    assert str(root / ".env") in manifest["exclude_paths"]
    assert str(root / "sub" / "server.pem") in manifest["exclude_paths"]
    assert "*.ost" in manifest["exclude_paths"]


def test_a_confirmed_sensitive_file_is_backed_up_not_excluded(tmp_path: Path):
    root = tmp_path / "Project"
    root.mkdir()
    env = root / ".env"
    env.write_text("SECRET=1\n", encoding="utf-8")
    (root / "notes.txt").write_bytes(b"x")
    manifest = compile_selection(
        selected=[
            {"path": str(root), "kind": "directory", "category": "extra"},
            {"path": str(env), "kind": "file", "category": "sensitive", "requires_explicit": True, "confirmed": True},
        ],
        sensitive_files=[str(env)],
    )
    assert str(env) not in manifest["exclude_paths"]


def test_discovered_sensitive_files_under_known_folders_are_excluded_too(tmp_path: Path):
    desk = tmp_path / "Desktop"
    (desk / "teofarm").mkdir(parents=True)
    env = desk / "teofarm" / ".env"
    env.write_text("SECRET=1\n", encoding="utf-8")
    (desk / "rapor.docx").write_bytes(b"x")
    manifest = compile_selection(
        selected=[{"path": str(desk), "kind": "directory", "category": "known_folder_desktop"}],
        sensitive_files=[str(env)],
    )
    assert manifest["source_roots"] == [str(desk)]
    assert str(env) in manifest["exclude_paths"]


def test_name_patterns_keep_out_credentials_created_after_setup_unless_confirmed(tmp_path: Path):
    root = tmp_path / "Project"
    root.mkdir()
    (root / "notes.txt").write_bytes(b"x")
    manifest = compile_selection(selected=[{"path": str(root), "kind": "directory", "category": "extra"}])
    assert ".env" in manifest["exclude_paths"] and "*.pem" in manifest["exclude_paths"]
    env = root / ".env"
    env.write_text("SECRET=1", encoding="utf-8")
    confirmed = compile_selection(
        selected=[
            {"path": str(root), "kind": "directory", "category": "extra"},
            {"path": str(env), "kind": "file", "category": "sensitive", "requires_explicit": True, "confirmed": True},
        ],
        sensitive_files=[str(env)],
    )
    assert ".env" not in confirmed["exclude_paths"] and "*.pem" in confirmed["exclude_paths"]
