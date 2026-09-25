"""Friendly computer names, and the folder/file listings the panel saves after the first scan.

The control plane cannot read a computer's disk or a backup's contents itself: only the agent can,
so the first look at a folder costs a round trip to the agent (30 seconds to a couple of minutes).
Once a scan has succeeded the server keeps the listing, so the second look is instant, works while
the computer is busy backing up or switched off, and can be searched by name.
"""
from __future__ import annotations

from test_api import _enroll, client, login  # noqa: F401  -- must precede any app import: sets the test env before app.config.settings is built

from app.db import Repository  # noqa: E402
from app.main import SessionLocal  # noqa: E402


def _h(d: dict) -> dict:
    return {"Authorization": f"Bearer {d['control_credential']}"}


def _base(d: dict) -> str:
    return f"/api/v1/admin/devices/{d['device_id']}"


def _agent_answers(client, d: dict, kind: str, result: dict, state: str = "SUCCEEDED") -> dict:
    """What the agent does with the next queued command: claim it, then report a result."""
    claimed = client.post("/api/v1/agent/work/claim", headers=_h(d)).json()["command"]
    assert claimed["kind"] == kind, claimed
    r = client.post(f"/api/v1/agent/commands/{claimed['command_id']}/ack", headers=_h(d), json={"state": state, "extra": result})
    assert r.status_code == 200, r.text
    return claimed


def _scan_local(client, d: dict, result: dict, *, cursor: str = "", state: str = "SUCCEEDED") -> None:
    r = client.post(f"{_base(d)}/browse-local-dir", json={"path": result.get("path") or r"C:\Users", "cursor": cursor})
    assert r.status_code == 200, r.text
    _agent_answers(client, d, "BROWSE_LOCAL_DIR", result, state)


def _bind_snapshot(client, d: dict, snapshot: str) -> None:
    r = client.post("/api/v1/agent/jobs/events", headers=_h(d), json={"outcome": "SUCCEEDED", "snapshot_id": snapshot, "slot_key": f"names-{snapshot[:6]}"})
    assert r.status_code == 200, r.text


def _scan_snapshot(client, d: dict, snapshot: str, result: dict) -> None:
    r = client.post(f"{_base(d)}/browse", json={"snapshot_id": snapshot, "prefix": result["prefix"]})
    assert r.status_code == 200, r.text
    _agent_answers(client, d, "BROWSE_SNAPSHOT", result)


def _cached(client, d: dict, **params) -> dict:
    r = client.get(f"{_base(d)}/browse-cache", params=params)
    assert r.status_code == 200, r.text
    return r.json()


def _search(client, d: dict, **params) -> dict:
    r = client.get(f"{_base(d)}/browse-cache/search", params=params)
    assert r.status_code == 200, r.text
    return r.json()


DESKTOP = {
    "path": r"C:\Users\Lenovo\Desktop",
    "parent": r"C:\Users\Lenovo",
    "entries": [
        {"name": "Faturalar", "path": r"C:\Users\Lenovo\Desktop\Faturalar", "type": "directory"},
        {"name": "notlar.txt", "path": r"C:\Users\Lenovo\Desktop\notlar.txt", "type": "file"},
    ],
}


# --- friendly computer names ----------------------------------------------------------------


def test_a_computer_can_be_given_a_friendly_name_and_keeps_its_real_hostname(client):
    login(client)
    d = _enroll(client, "REN-PC-01", "branch")
    r = client.patch(_base(d), json={"display_name": "  Muhasebe   Mehmet  "})
    assert r.status_code == 200, r.text
    assert r.json()["device"]["display_name"] == "Muhasebe Mehmet"
    assert r.json()["device"]["hostname"] == "REN-PC-01"

    detail = client.get(_base(d)).json()
    assert detail["device"]["display_name"] == "Muhasebe Mehmet"
    assert detail["status"]["display_name"] == "Muhasebe Mehmet" and detail["status"]["hostname"] == "REN-PC-01"
    row = next(x for x in client.get("/api/v1/admin/devices").json()["items"] if x["id"] == d["device_id"])
    assert row["display_name"] == "Muhasebe Mehmet" and row["hostname"] == "REN-PC-01"


def test_a_computer_that_was_never_renamed_has_an_empty_friendly_name(client):
    login(client)
    d = _enroll(client, "REN-PC-02", "branch")
    assert client.get(_base(d)).json()["device"]["display_name"] == ""


def test_a_friendly_name_can_be_cleared_and_is_validated(client):
    login(client)
    d = _enroll(client, "REN-PC-03", "branch")
    assert client.patch(_base(d), json={"display_name": "Servis 1"}).json()["device"]["display_name"] == "Servis 1"
    assert client.patch(_base(d), json={"display_name": "   "}).json()["device"]["display_name"] == "", "blank means: go back to the real hostname"

    too_long = client.patch(_base(d), json={"display_name": "x" * 65})
    assert too_long.status_code == 422
    control = client.patch(_base(d), json={"display_name": "bad\x00name"})
    assert control.status_code == 422
    tabs_and_newlines = client.patch(_base(d), json={"display_name": "İK\n\tMasa 2"})
    assert tabs_and_newlines.json()["device"]["display_name"] == "İK Masa 2"
    assert client.patch(_base(d), json={"display_name": "y" * 64}).status_code == 200, "64 characters is allowed"


def test_renaming_never_touches_where_the_backups_live(client):
    login(client)
    d = _enroll(client, "REN-PC-04", "branch")
    with SessionLocal() as s:
        before = [(r.id, r.location) for r in s.query(Repository).filter(Repository.device_id == d["device_id"]).all()]
    assert before
    assert client.patch(_base(d), json={"display_name": "Yeni ad"}).status_code == 200
    with SessionLocal() as s:
        after = [(r.id, r.location) for r in s.query(Repository).filter(Repository.device_id == d["device_id"]).all()]
    assert after == before


def test_the_friendly_name_is_searchable_in_the_computer_list(client):
    login(client)
    d = _enroll(client, "REN-PC-05", "branch")
    client.patch(_base(d), json={"display_name": "Yonetim Kosesi"})
    found = {x["id"] for x in client.get("/api/v1/admin/devices", params={"q": "kosesi"}).json()["items"]}
    assert d["device_id"] in found
    by_hostname = {x["id"] for x in client.get("/api/v1/admin/devices", params={"q": "ren-pc-05"}).json()["items"]}
    assert d["device_id"] in by_hostname, "the real hostname keeps working as a search term"


def test_the_friendly_name_rides_along_wherever_the_panel_lists_a_computer(client):
    login(client)
    d = _enroll(client, "REN-PC-06", "branch")
    snap = "e" * 64
    _bind_snapshot(client, d, snap)
    ok = client.post("/api/v1/admin/restore-requests", json={"device_id": d["device_id"], "snapshot_id": snap, "selections": ["/C/data"]})
    assert ok.status_code == 200, ok.text
    client.patch(_base(d), json={"display_name": "Satis Tezgahi"})

    backups = [x for x in client.get("/api/v1/admin/backups").json()["items"] if x["device_id"] == d["device_id"]]
    assert backups and all(x["display_name"] == "Satis Tezgahi" and x["hostname"] == "REN-PC-06" for x in backups)
    restores = [x for x in client.get("/api/v1/admin/restore-requests").json()["items"] if x["device_id"] == d["device_id"]]
    assert restores and all(x["display_name"] == "Satis Tezgahi" for x in restores)
    vault = [x for x in client.get("/api/v1/admin/vault").json()["items"] if x["device_id"] == d["device_id"]]
    assert vault and vault[0]["display_name"] == "Satis Tezgahi" and vault[0]["hostname"] == "REN-PC-06"
    dash = client.get("/api/v1/admin/dashboard").json()["recent_attempts"]
    assert any(a.get("display_name") == "Satis Tezgahi" for a in dash if a["hostname"] == "REN-PC-06")


def test_a_rename_is_audited_with_the_new_name(client):
    login(client)
    d = _enroll(client, "REN-PC-07", "branch")
    client.patch(_base(d), json={"display_name": "Depo Girisi"})
    client.patch(_base(d), json={"display_name": ""})
    events = [e for e in client.get("/api/v1/admin/audit-events").json()["items"] if e["action"] == "device_rename" and e["device_id"] == d["device_id"]]
    assert [e["detail"] for e in events] == ["", "Depo Girisi"], "newest first; a reset shows an empty name"
    assert all(e["display_name"] == "" or e["display_name"] == "Depo Girisi" for e in events)


def test_changing_only_the_department_is_still_a_plain_device_patch(client):
    login(client)
    d = _enroll(client, "REN-PC-08", "branch")
    client.patch(_base(d), json={"department": "IT"})
    events = [e for e in client.get("/api/v1/admin/audit-events").json()["items"] if e["device_id"] == d["device_id"] and e["action"].startswith("device_")]
    assert [e["action"] for e in events] == ["device_patch"]


# --- saved folder listings ------------------------------------------------------------------


def test_a_finished_folder_scan_is_saved_and_served_without_asking_the_computer(client):
    login(client)
    d = _enroll(client, "CACHE-PC-01", "branch")
    assert _cached(client, d, scope="local", path=DESKTOP["path"])["cached"] is False

    _scan_local(client, d, DESKTOP)
    hit = _cached(client, d, scope="local", path=DESKTOP["path"])
    assert hit["cached"] is True
    assert hit["path"] == DESKTOP["path"] and hit["parent"] == DESKTOP["parent"]
    assert [e["name"] for e in hit["entries"]] == ["Faturalar", "notlar.txt"]
    assert hit["scanned_at"] and hit["age_seconds"] >= 0 and hit["truncated"] is False

    # Windows does not care about case or slash direction, so neither does the saved copy
    assert _cached(client, d, scope="local", path="c:/users/lenovo/desktop/")["cached"] is True
    # ...but a different folder was never scanned
    assert _cached(client, d, scope="local", path=r"C:\Users\Lenovo\Documents")["cached"] is False

    # reading the saved copy is free: it never queues anything for the computer
    assert not client.post("/api/v1/agent/work/claim", headers=_h(d)).json().get("command")


def test_a_failed_or_empty_scan_is_not_saved(client):
    login(client)
    d = _enroll(client, "CACHE-PC-02", "branch")
    _scan_local(client, d, {"path": DESKTOP["path"], "error_class": "BROWSE_PATH_REJECTED"}, state="FAILED")
    assert _cached(client, d, scope="local", path=DESKTOP["path"])["cached"] is False
    _scan_local(client, d, {"path": DESKTOP["path"]})  # a "success" that carries no listing at all
    assert _cached(client, d, scope="local", path=DESKTOP["path"])["cached"] is False
    _scan_local(client, d, {"path": DESKTOP["path"], "entries": "not-a-list"})
    assert _cached(client, d, scope="local", path=DESKTOP["path"])["cached"] is False


def test_an_empty_folder_is_a_real_answer_and_is_saved(client):
    login(client)
    d = _enroll(client, "CACHE-PC-03", "branch")
    _scan_local(client, d, {"path": r"C:\Users\Lenovo\Boş", "parent": r"C:\Users\Lenovo", "entries": []})
    hit = _cached(client, d, scope="local", path=r"C:\Users\Lenovo\Boş")
    assert hit["cached"] is True and hit["entries"] == []


def test_a_newer_scan_replaces_the_saved_listing(client):
    login(client)
    d = _enroll(client, "CACHE-PC-04", "branch")
    _scan_local(client, d, DESKTOP)
    newer = {**DESKTOP, "entries": [{"name": "Yeni klasör", "path": r"C:\Users\Lenovo\Desktop\Yeni klasör", "type": "directory"}]}
    _scan_local(client, d, newer)
    hit = _cached(client, d, scope="local", path=DESKTOP["path"])
    assert [e["name"] for e in hit["entries"]] == ["Yeni klasör"]


def test_a_later_page_never_overwrites_the_first_page(client):
    login(client)
    d = _enroll(client, "CACHE-PC-05", "branch")
    _scan_local(client, d, {**DESKTOP, "cursor": "notlar.txt"})  # first page of a big folder: more is available
    first = _cached(client, d, scope="local", path=DESKTOP["path"])
    assert first["cached"] is True and first["truncated"] is True
    _scan_local(client, d, {**DESKTOP, "entries": [{"name": "son.txt", "path": r"C:\Users\Lenovo\Desktop\son.txt", "type": "file"}]}, cursor="notlar.txt")
    again = _cached(client, d, scope="local", path=DESKTOP["path"])
    assert [e["name"] for e in again["entries"]] == ["Faturalar", "notlar.txt"]


def test_saved_listings_belong_to_one_computer(client):
    login(client)
    a = _enroll(client, "CACHE-PC-06", "branch")
    b = _enroll(client, "CACHE-PC-07", "branch")
    _scan_local(client, a, DESKTOP)
    assert _cached(client, a, scope="local", path=DESKTOP["path"])["cached"] is True
    assert _cached(client, b, scope="local", path=DESKTOP["path"])["cached"] is False
    assert _search(client, b, scope="local", q="")["matches"] == []


def test_saved_listings_need_a_login_and_a_known_computer(client):
    login(client)
    d = _enroll(client, "CACHE-PC-08", "branch")
    assert client.get(f"/api/v1/admin/devices/{'0' * 8}-0000-0000-0000-{'0' * 12}/browse-cache", params={"scope": "local", "path": "C:\\"}).status_code == 404
    assert client.get(f"{_base(d)}/browse-cache", params={"scope": "nope", "path": "C:\\"}).status_code == 422
    client.post("/api/v1/auth/logout")
    assert client.get(f"{_base(d)}/browse-cache", params={"scope": "local", "path": "C:\\"}).status_code == 401
    assert client.get(f"{_base(d)}/browse-cache/search", params={"scope": "local", "q": "x"}).status_code == 401


def test_snapshot_listings_are_saved_per_snapshot_and_folder(client):
    login(client)
    d = _enroll(client, "CACHE-PC-09", "branch")
    snap, other = "c" * 64, "d" * 64
    _bind_snapshot(client, d, snap)
    _bind_snapshot(client, d, other)
    result = {
        "prefix": "/C/Users/Lenovo/Desktop",
        "count": 2,
        "truncated": False,
        "entries": [
            {"name": "863.pdf", "path": "/C/Users/Lenovo/Desktop/863.pdf", "type": "file", "size": 17937, "mtime": "2023-12-20T13:14:16+03:00"},
            {"name": "Eski", "path": "/C/Users/Lenovo/Desktop/Eski", "type": "dir"},
        ],
    }
    _scan_snapshot(client, d, snap, result)

    hit = _cached(client, d, scope="snapshot", snapshot_id=snap, path="/C/Users/Lenovo/Desktop")
    assert hit["cached"] is True and hit["path"] == "/C/Users/Lenovo/Desktop"
    assert [(e["name"], e["type"], e.get("size")) for e in hit["entries"]] == [("863.pdf", "file", 17937), ("Eski", "dir", None)]
    assert _cached(client, d, scope="snapshot", snapshot_id=snap, path="/C/Users/Lenovo/Desktop/")["cached"] is True
    assert _cached(client, d, scope="snapshot", snapshot_id=other, path="/C/Users/Lenovo/Desktop")["cached"] is False, "another snapshot has its own contents"
    assert _cached(client, d, scope="snapshot", snapshot_id=snap, path="/c/users/lenovo/desktop")["cached"] is False, "restic paths are case-sensitive"
    assert client.get(f"{_base(d)}/browse-cache", params={"scope": "snapshot", "snapshot_id": "short", "path": "/C"}).status_code == 422
    assert client.get(f"{_base(d)}/browse-cache", params={"scope": "snapshot", "path": "/C"}).status_code == 422, "a snapshot listing needs its snapshot"


def test_the_root_of_a_snapshot_can_be_saved_too(client):
    login(client)
    d = _enroll(client, "CACHE-PC-10", "branch")
    snap = "b" * 64
    _bind_snapshot(client, d, snap)
    _scan_snapshot(client, d, snap, {"prefix": "", "count": 1, "truncated": False, "entries": [{"name": "C", "path": "/C", "type": "dir"}]})
    assert _cached(client, d, scope="snapshot", snapshot_id=snap, path="")["cached"] is True
    assert _cached(client, d, scope="snapshot", snapshot_id=snap, path="/")["cached"] is True


def test_saved_listings_can_be_searched_by_name_without_typing_the_turkish_letters(client):
    login(client)
    d = _enroll(client, "CACHE-PC-11", "branch")
    _scan_local(client, d, {"path": r"C:\Users", "parent": "C:\\", "entries": [
        {"name": "Lenovo", "path": r"C:\Users\Lenovo", "type": "directory"},
        {"name": "Public", "path": r"C:\Users\Public", "type": "directory"},
    ]})
    _scan_local(client, d, {"path": r"C:\Users\Lenovo", "parent": r"C:\Users", "entries": [
        {"name": "Desktop", "path": r"C:\Users\Lenovo\Desktop", "type": "directory"},
        {"name": "Çimko Araç Listesi", "path": r"C:\Users\Lenovo\Çimko Araç Listesi", "type": "directory"},
        {"name": "İstanbul Belgeleri", "path": r"C:\Users\Lenovo\İstanbul Belgeleri", "type": "directory"},
        {"name": "belge.pdf", "path": r"C:\Users\Lenovo\belge.pdf", "type": "file"},
    ]})

    by_folder_name = _search(client, d, scope="local", q="cimko")
    assert [m["path"] for m in by_folder_name["matches"]] == [r"C:\Users\Lenovo\Çimko Araç Listesi"]
    assert by_folder_name["matches"][0]["type"] == "directory" and by_folder_name["matches"][0]["folder"] == r"C:\Users\Lenovo"
    assert [m["name"] for m in _search(client, d, scope="local", q="ISTANBUL")["matches"]] == ["İstanbul Belgeleri"]
    assert [m["name"] for m in _search(client, d, scope="local", q="belge")["matches"]] == ["İstanbul Belgeleri", "belge.pdf"], "folders first, then files"
    # Desktop was never scanned itself, but its name is known from the listing that contains it
    assert [m["path"] for m in _search(client, d, scope="local", q="desk")["matches"]] == [r"C:\Users\Lenovo\Desktop"]

    everything = _search(client, d, scope="local", q="")
    assert any(m["name"] == "Users" and m.get("scanned") for m in everything["matches"]), "the scanned folder itself stays selectable"
    everything["matches"] = [m for m in everything["matches"] if m["name"] != "Users"]
    assert {m["name"] for m in everything["matches"]} == {"Lenovo", "Public", "Desktop", "Çimko Araç Listesi", "İstanbul Belgeleri"}, "no query lists the known folders, not the files"
    assert everything["scanned_folders"] == 2
    assert all(m["type"] == "directory" for m in everything["matches"])


def test_snapshot_search_finds_files_in_the_folders_that_were_opened(client):
    login(client)
    d = _enroll(client, "CACHE-PC-12", "branch")
    snap = "a" * 64
    _bind_snapshot(client, d, snap)
    _scan_snapshot(client, d, snap, {
        "prefix": "/C/Users/Lenovo/Desktop", "count": 2, "truncated": False,
        "entries": [
            {"name": "863.pdf", "path": "/C/Users/Lenovo/Desktop/863.pdf", "type": "file", "size": 17937, "mtime": "2023-12-20T13:14:16+03:00"},
            {"name": "Eski", "path": "/C/Users/Lenovo/Desktop/Eski", "type": "dir"},
        ],
    })
    found = _search(client, d, scope="snapshot", snapshot_id=snap, q="863")
    assert [(m["path"], m["type"], m["size"]) for m in found["matches"]] == [("/C/Users/Lenovo/Desktop/863.pdf", "file", 17937)]
    assert found["matches"][0]["folder"] == "/C/Users/Lenovo/Desktop"
    assert _search(client, d, scope="snapshot", snapshot_id="9" * 64, q="863")["matches"] == [], "search never crosses into another snapshot"
    assert client.get(f"{_base(d)}/browse-cache/search", params={"scope": "snapshot", "q": "863"}).status_code == 422


def test_search_results_are_capped(client):
    login(client)
    d = _enroll(client, "CACHE-PC-13", "branch")
    entries = [{"name": f"klasor-{i:03d}", "path": rf"C:\Users\Lenovo\klasor-{i:03d}", "type": "directory"} for i in range(250)]
    _scan_local(client, d, {"path": r"C:\Users\Lenovo", "parent": r"C:\Users", "entries": entries})
    res = _search(client, d, scope="local", q="klasor")
    assert len(res["matches"]) == 200 and res["truncated"] is True
    assert len(_search(client, d, scope="local", q="klasor-24")["matches"]) == 10


def test_panel_uses_saved_listings_and_exposes_an_explicit_rescan_and_name_field(client):
    source = client.get("/ui/js/device.js").text
    assert "/browse-cache?scope=local" in source
    assert "/browse-cache/search?scope=local" in source
    assert "/browse-cache?scope=snapshot" in source
    assert "Kayıtlı listeyi göster" in source and "Yeniden tara" in source
    assert 'id="s-name"' in source and "display_name" in source
