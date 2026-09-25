from __future__ import annotations

from pathlib import Path

from app.discovery import KnownFolders, discover
from app.selection import SelectionError, compile_selection
from app.schedule_policy import ScheduleError, estimate_duration_seconds, first_backup_fit, validate_preferred_start


def _tree(tmp: Path) -> KnownFolders:
    desk = tmp / "OneDrive" / "Desktop"
    docs = tmp / "OneDrive" / "Belgeler"
    pics = tmp / "OneDrive" / "Resimler"
    downs = tmp / "Downloads"
    outlook = docs / "Outlook Dosyaları"
    game = docs / "Mount and Blade II Bannerlord"
    envdir = desk / "teofarm"
    for p in (desk, docs, pics, downs, outlook, game, envdir):
        p.mkdir(parents=True, exist_ok=True)
    (docs / "fatura.xlsx").write_bytes(b"x" * 100)
    (docs / "sozlesme.docx").write_bytes(b"y" * 50)
    (docs / "teklif.pdf").write_bytes(b"z" * 20)
    (docs / "müşteri fiyatları.csv").write_bytes(b"a,b\n")
    (outlook / "office@example.com.pst").write_bytes(b"pst1" * 100)
    (outlook / "arsiv.pst").write_bytes(b"pst2" * 80)
    (tmp / "AppData" / "Local" / "Microsoft" / "Outlook").mkdir(parents=True)
    (tmp / "AppData" / "Local" / "Microsoft" / "Outlook" / "offline.ost").write_bytes(b"ost" * 30)
    (game / "save.dat").write_bytes(b"game")
    (envdir / ".env").write_text("SECRET=1\n", encoding="utf-8")
    (envdir / "tls.pem").write_text("-----BEGIN CERT-----\n", encoding="utf-8")
    (downs / "Setup.exe").write_bytes(b"dl" * 100)
    (tmp / "Stowline" / "TestCorpus").mkdir(parents=True)
    (tmp / "Stowline" / "TestCorpus" / "synthetic.txt").write_text("nope", encoding="utf-8")
    empty = tmp / "empty-profile"
    empty.mkdir()
    return KnownFolders(
        profile=str(tmp),
        desktop=str(desk),
        documents=str(docs),
        pictures=str(pics),
        downloads=str(downs),
        redirected={"desktop": True, "documents": True},
        missing=[],
    )


def test_known_folder_redirection_and_unicode(tmp_path: Path):
    kf = _tree(tmp_path)
    out = discover(known=kf, extra_roots=[], include_downloads=False)
    assert out["known_folders"]["redirected"]["desktop"] is True
    assert out["known_folders"]["documents"].endswith("Belgeler")
    paths = [c["path"] for c in out["candidates"]]
    assert any("müşteri fiyatları.csv" in p or "Belgeler" in p for p in paths)
    assert out["pst_count"] == 2
    assert out["ost_count"] == 1
    assert out["sensitive_count"] >= 2
    pst = [c for c in out["candidates"] if c["category"] == "outlook_pst"]
    assert all(c["default_selected"] for c in pst)
    ost = [c for c in out["candidates"] if c["category"] == "outlook_ost"]
    assert ost and ost[0]["default_selected"] is False
    downs = [c for c in out["candidates"] if c["category"] == "downloads"]
    assert not downs or downs[0]["default_selected"] is False
    games = [c for c in out["candidates"] if c["category"] == "game"]
    assert games
    assert not any("TestCorpus" in c["path"] for c in out["candidates"])


def test_extra_root_with_no_office_files_still_produces_a_candidate(tmp_path: Path):
    """Regression: discover()'s per-root branching only ever produced a
    candidate for an extra root if it had office-extension files, or for
    known folders regardless of contents -- an operator-picked custom
    folder full of e.g. photos produced NO candidate at all, so picking it
    via the wizard's folder browser would silently do nothing."""
    custom = tmp_path / "ClientPhotos"
    custom.mkdir()
    (custom / "photo1.jpg").write_bytes(b"x" * 100)
    (custom / "photo2.jpg").write_bytes(b"y" * 200)
    kf = KnownFolders(profile=str(tmp_path))
    out = discover(known=kf, extra_roots=[str(custom)], include_downloads=False)
    extra = [c for c in out["candidates"] if c["category"] == "extra"]
    assert len(extra) == 1
    assert extra[0]["path"] == str(custom)
    assert extra[0]["file_count"] == 2
    assert extra[0]["bytes"] == 300
    assert extra[0]["default_selected"] is False
    assert len(out["extra_files"][str(custom)]) == 2


def test_extra_root_sensitive_file_is_excluded_from_the_bulk_candidate(tmp_path: Path):
    """The whole point of extra_files: a sensitive descendant must be
    reachable only through its own separately-gated candidate, never
    folded into the "back up this whole folder" bulk file list."""
    custom = tmp_path / "Project"
    custom.mkdir()
    (custom / "notes.txt").write_bytes(b"hello")
    (custom / ".env").write_text("SECRET=1\n", encoding="utf-8")
    kf = KnownFolders(profile=str(tmp_path))
    out = discover(known=kf, extra_roots=[str(custom)], include_downloads=False)
    extra_files = out["extra_files"][str(custom)]
    assert len(extra_files) == 1
    assert extra_files[0].endswith("notes.txt")
    sensitive = [c for c in out["candidates"] if c["category"] == "sensitive"]
    assert any(c["path"].endswith(".env") for c in sensitive)


def test_extra_root_with_nothing_backupable_reports_zero_not_missing(tmp_path: Path):
    """A custom folder containing ONLY a sensitive file must still get a
    candidate row (so the operator sees it was picked), just with
    file_count 0 -- not silently vanish, matching the bug above."""
    custom = tmp_path / "SecretsOnly"
    custom.mkdir()
    (custom / "id_rsa").write_text("private", encoding="utf-8")
    kf = KnownFolders(profile=str(tmp_path))
    out = discover(known=kf, extra_roots=[str(custom)], include_downloads=False)
    extra = [c for c in out["candidates"] if c["category"] == "extra"]
    assert len(extra) == 1
    assert extra[0]["file_count"] == 0


def test_empty_and_missing_known_folders(tmp_path: Path):
    empty = tmp_path / "empty"
    empty.mkdir()
    kf = KnownFolders(profile=str(empty), desktop="", documents="", pictures="", downloads="", missing=["desktop", "documents"])
    out = discover(known=kf)
    assert out["known_folders"]["missing"]
    assert out["pst_count"] == 0


def test_office_category_does_not_force_whole_profile(tmp_path: Path):
    kf = _tree(tmp_path)
    out = discover(known=kf)
    office = [c for c in out["candidates"] if c["kind"] == "category" and c["category"] == "office_files"]
    assert office
    manifest = compile_selection(
        selected=[{"path": office[0]["path"], "kind": "category", "category": "office_files"}],
        office_files=out["office_files"],
    )
    assert all(Path(p).is_file() for p in manifest["source_roots"])
    assert not any(p.lower().endswith(".pst") for p in manifest["source_roots"])


def test_sensitive_requires_explicit(tmp_path: Path):
    kf = _tree(tmp_path)
    env = tmp_path / "OneDrive" / "Desktop" / "teofarm" / ".env"
    try:
        compile_selection(selected=[{"path": str(env), "kind": "file", "category": "sensitive", "requires_explicit": True}])
        raise AssertionError("expected SelectionError")
    except SelectionError:
        pass
    manifest = compile_selection(
        selected=[{"path": str(env), "kind": "file", "category": "sensitive", "requires_explicit": True, "confirmed": True}]
    )
    assert str(env) in manifest["source_roots"] or any(p.endswith(".env") for p in manifest["source_roots"])


def test_ost_never_compiled_as_root(tmp_path: Path):
    ost = tmp_path / "offline.ost"
    ost.write_bytes(b"ost")
    pst = tmp_path / "mail.pst"
    pst.write_bytes(b"pst")
    manifest = compile_selection(
        selected=[
            {"path": str(ost), "kind": "file", "category": "outlook_ost"},
            {"path": str(pst), "kind": "file", "category": "outlook_pst"},
        ]
    )
    assert any(p.endswith("mail.pst") for p in manifest["source_roots"])
    assert not any(p.endswith(".ost") for p in manifest["source_roots"])


def test_hq_schedule_and_size_estimate(monkeypatch):
    from app import schedule_policy

    monkeypatch.setitem(schedule_policy.SITE_SCHEDULE["hq"], "limit_upload_kib", 878)
    validate_preferred_start("hq", "14:00")
    try:
        validate_preferred_start("hq", "17:00")
        raise AssertionError("17:00 should fail")
    except ScheduleError:
        pass
    try:
        estimate_duration_seconds(1000, 0)
        raise AssertionError("zero limit must fail closed")
    except ScheduleError:
        pass
    # 878 KiB/s × 60s = 878*1024 bytes
    sec = estimate_duration_seconds(878 * 1024 * 60, 878)
    assert 59 <= sec <= 61
    fit = first_backup_fit(site_id="hq", logical_bytes=10_000_000, preferred_hhmm="12:00")
    assert fit["limit_upload_kib"] == 878
    assert fit["stay_powered"]
    huge = first_backup_fit(site_id="hq", logical_bytes=80_000_000_000, preferred_hhmm="16:00")
    assert huge["warnings"]
