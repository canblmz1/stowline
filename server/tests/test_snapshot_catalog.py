"""Permanent per-snapshot file catalog: models and the DB-level guarantees
this feature depends on (see docs/superpowers/specs/2026-09-22-snapshot-catalog-design.md).
"""
from __future__ import annotations

from test_api import _enroll, client, login  # noqa: F401  -- must precede any app import: sets the test env before app.config.settings is built

from app.db import Repository, Snapshot, SnapshotCatalog, SnapshotCatalogEntry  # noqa: E402
from app.main import SessionLocal  # noqa: E402
from app.security import new_id  # noqa: E402


def _bound_snapshot(client, hostname: str, site: str = "branch") -> tuple[dict, str, str]:
    """Enrolls a device, reports one successful backup, and returns
    (device, snapshot_hex_id, snapshot_row_id) -- the same binding
    create_restore/browse already require before touching a snapshot."""
    d = _enroll(client, hostname, site)
    snap_hex = (new_id().replace("-", "") + new_id().replace("-", ""))[:64]
    h = {"Authorization": f"Bearer {d['control_credential']}"}
    r = client.post("/api/v1/agent/jobs/events", headers=h, json={"outcome": "SUCCEEDED", "snapshot_id": snap_hex, "slot_key": f"cat-{hostname}"})
    assert r.status_code == 200, r.text
    with SessionLocal() as db:
        row = db.query(Snapshot).filter(Snapshot.engine_snapshot_id == snap_hex).one()
        snapshot_row_id = row.id
    return d, snap_hex, snapshot_row_id


def test_a_catalog_and_its_entries_can_be_created_and_read_back(client):
    login(client)
    d, snap_hex, snapshot_row_id = _bound_snapshot(client, "cat-pc-01")
    with SessionLocal() as db:
        repo = db.query(Repository).filter(Repository.device_id == d["device_id"]).one()
        db.add(SnapshotCatalog(
            id=new_id(), snapshot_id=snapshot_row_id, device_id=d["device_id"],
            installation_id=d.get("installation_id", ""), repository_id=repo.id,
            state="PENDING", declared_file_count=2, declared_directory_count=1,
            declared_logical_bytes=100, declared_checksum="deadbeef",
        ))
        db.flush()
        catalog_id = db.query(SnapshotCatalog).filter(SnapshotCatalog.snapshot_id == snapshot_row_id).one().id
        db.add(SnapshotCatalogEntry(
            catalog_id=catalog_id, path_hash="a" * 64, parent_path="/C/Users",
            path="/C/Users/Belge.txt", name="Belge.txt", normalized_name="belge.txt",
            type="file", size=10, mtime="2026-09-22T10:00:00+03:00",
        ))
        db.commit()
    with SessionLocal() as db:
        cat = db.query(SnapshotCatalog).filter(SnapshotCatalog.snapshot_id == snapshot_row_id).one()
        assert cat.state == "PENDING"
        entries = db.query(SnapshotCatalogEntry).filter(SnapshotCatalogEntry.catalog_id == cat.id).all()
        assert len(entries) == 1 and entries[0].name == "Belge.txt"


def test_only_one_catalog_can_ever_exist_per_snapshot(client):
    login(client)
    d, snap_hex, snapshot_row_id = _bound_snapshot(client, "cat-pc-02")
    with SessionLocal() as db:
        repo = db.query(Repository).filter(Repository.device_id == d["device_id"]).one()
        db.add(SnapshotCatalog(
            id=new_id(), snapshot_id=snapshot_row_id, device_id=d["device_id"],
            installation_id="", repository_id=repo.id, state="PENDING",
            declared_file_count=0, declared_directory_count=0, declared_logical_bytes=0, declared_checksum="x",
        ))
        db.commit()
        db.add(SnapshotCatalog(
            id=new_id(), snapshot_id=snapshot_row_id, device_id=d["device_id"],
            installation_id="", repository_id=repo.id, state="PENDING",
            declared_file_count=0, declared_directory_count=0, declared_logical_bytes=0, declared_checksum="y",
        ))
        raised = False
        try:
            db.commit()
        except Exception:
            raised = True
            db.rollback()
        assert raised, "a second catalog for the same snapshot must violate the unique constraint"


def test_deleting_the_snapshot_row_removes_its_catalog_and_entries(client):
    login(client)
    d, snap_hex, snapshot_row_id = _bound_snapshot(client, "cat-pc-03")
    with SessionLocal() as db:
        repo = db.query(Repository).filter(Repository.device_id == d["device_id"]).one()
        cat = SnapshotCatalog(
            id=new_id(), snapshot_id=snapshot_row_id, device_id=d["device_id"],
            installation_id="", repository_id=repo.id, state="READY",
            declared_file_count=1, declared_directory_count=0, declared_logical_bytes=5, declared_checksum="z",
            actual_file_count=1, actual_directory_count=0,
        )
        db.add(cat)
        db.flush()
        db.add(SnapshotCatalogEntry(
            catalog_id=cat.id, path_hash="b" * 64, parent_path="/C", path="/C/f.txt",
            name="f.txt", normalized_name="f.txt", type="file", size=5, mtime="2026-09-22T10:00:00+03:00",
        ))
        db.commit()
        catalog_id = cat.id
        snap = db.get(Snapshot, snapshot_row_id)
        db.delete(snap)
        db.commit()
    with SessionLocal() as db:
        assert db.get(SnapshotCatalog, catalog_id) is None
        assert db.query(SnapshotCatalogEntry).filter(SnapshotCatalogEntry.catalog_id == catalog_id).count() == 0


def test_path_hash_is_stable_and_checksum_is_order_independent():
    from app.services import catalog_checksum, catalog_path_hash

    h1 = catalog_path_hash("/C/Users/Belge.txt")
    h2 = catalog_path_hash("/C/Users/Belge.txt")
    h3 = catalog_path_hash("/C/Users/Diğer.txt")
    assert h1 == h2 and len(h1) == 64 and h1 != h3

    forward = catalog_checksum([h1, h3])
    backward = catalog_checksum([h3, h1])
    assert forward == backward
    assert forward != catalog_checksum([h1])


def _headers(d: dict) -> dict:
    return {"Authorization": f"Bearer {d['control_credential']}"}


def _declared(**overrides) -> dict:
    base = {"declared_file_count": 3, "declared_directory_count": 1, "declared_logical_bytes": 300, "declared_checksum": "abc123"}
    base.update(overrides)
    return base


def _entry(path: str, name: str, kind: str = "file", size: int | None = 10) -> dict:
    parent = path.rsplit("/", 1)[0] or "/"
    return {"path": path, "parent_path": parent, "name": name, "type": kind, "size": size, "mtime": "2026-09-22T10:00:00+03:00"}


def test_creating_a_catalog_requires_a_snapshot_bound_to_this_device(client):
    login(client)
    d, snap_hex, _ = _bound_snapshot(client, "cat-create-01")
    unbound = client.post("/api/v1/agent/snapshot-catalogs", headers=_headers(d), json={"snapshot_id": "e" * 64, **_declared()})
    assert unbound.status_code == 422

    ok = client.post("/api/v1/agent/snapshot-catalogs", headers=_headers(d), json={"snapshot_id": snap_hex, **_declared()})
    assert ok.status_code == 200, ok.text
    body = ok.json()
    assert body["state"] == "PENDING"
    assert body["catalog_id"]


def test_creating_a_catalog_twice_for_the_same_snapshot_returns_the_same_one(client):
    login(client)
    d, snap_hex, _ = _bound_snapshot(client, "cat-create-02")
    first = client.post("/api/v1/agent/snapshot-catalogs", headers=_headers(d), json={"snapshot_id": snap_hex, **_declared()}).json()
    second = client.post("/api/v1/agent/snapshot-catalogs", headers=_headers(d), json={"snapshot_id": snap_hex, **_declared(declared_checksum="different")}).json()
    assert second["catalog_id"] == first["catalog_id"]
    assert second["state"] == "PENDING"


def test_a_device_cannot_create_a_catalog_for_another_devices_snapshot(client):
    login(client)
    a, snap_a, _ = _bound_snapshot(client, "cat-create-03a")
    b, _, _ = _bound_snapshot(client, "cat-create-03b")
    cross = client.post("/api/v1/agent/snapshot-catalogs", headers=_headers(b), json={"snapshot_id": snap_a, **_declared()})
    assert cross.status_code == 422


def test_entries_upload_moves_pending_to_uploading_and_stores_rows(client):
    login(client)
    d, snap_hex, _ = _bound_snapshot(client, "cat-entries-01")
    cat_id = client.post("/api/v1/agent/snapshot-catalogs", headers=_headers(d), json={"snapshot_id": snap_hex, **_declared(declared_file_count=1, declared_directory_count=0)}).json()["catalog_id"]

    r = client.post(f"/api/v1/agent/snapshot-catalogs/{cat_id}/entries", headers=_headers(d), json={"entries": [_entry("/C/Users/Belge.txt", "Belge.txt")]})
    assert r.status_code == 200, r.text

    admin_row = client.get(f"/api/v1/admin/devices/{d['device_id']}").json()  # sanity: device still resolves normally
    assert admin_row["device"]["id"] == d["device_id"]


def test_resending_the_same_entry_never_duplicates_it(client):
    login(client)
    d, snap_hex, _ = _bound_snapshot(client, "cat-entries-02")
    cat_id = client.post("/api/v1/agent/snapshot-catalogs", headers=_headers(d), json={"snapshot_id": snap_hex, **_declared(declared_file_count=1, declared_directory_count=0)}).json()["catalog_id"]
    entry = _entry("/C/Users/Belge.txt", "Belge.txt")
    for _ in range(3):  # simulates a crash-and-retry re-sending the whole batch
        r = client.post(f"/api/v1/agent/snapshot-catalogs/{cat_id}/entries", headers=_headers(d), json={"entries": [entry]})
        assert r.status_code == 200, r.text
    with SessionLocal() as db:
        assert db.query(SnapshotCatalogEntry).filter(SnapshotCatalogEntry.catalog_id == cat_id).count() == 1


def test_entries_are_rejected_once_the_catalog_is_ready(client):
    login(client)
    d, snap_hex, _ = _bound_snapshot(client, "cat-entries-03")
    cat_id = client.post("/api/v1/agent/snapshot-catalogs", headers=_headers(d), json={"snapshot_id": snap_hex, **_declared(declared_file_count=0, declared_directory_count=0)}).json()["catalog_id"]
    from app.services import catalog_checksum

    fin = client.post(f"/api/v1/agent/snapshot-catalogs/{cat_id}/finalize", headers=_headers(d), json={"file_count": 0, "directory_count": 0, "logical_bytes": 0, "checksum": catalog_checksum([])})
    assert fin.status_code == 200, fin.text
    late = client.post(f"/api/v1/agent/snapshot-catalogs/{cat_id}/entries", headers=_headers(d), json={"entries": [_entry("/C/late.txt", "late.txt")]})
    assert late.status_code == 422


def test_finalize_requires_matching_totals_and_checksum(client):
    login(client)
    d, snap_hex, _ = _bound_snapshot(client, "cat-finalize-01")
    cat_id = client.post("/api/v1/agent/snapshot-catalogs", headers=_headers(d), json={"snapshot_id": snap_hex, **_declared(declared_file_count=1, declared_directory_count=0)}).json()["catalog_id"]
    entry = _entry("/C/Users/Belge.txt", "Belge.txt")
    client.post(f"/api/v1/agent/snapshot-catalogs/{cat_id}/entries", headers=_headers(d), json={"entries": [entry]})

    from app.services import catalog_checksum, catalog_path_hash

    wrong = client.post(f"/api/v1/agent/snapshot-catalogs/{cat_id}/finalize", headers=_headers(d), json={"file_count": 2, "directory_count": 0, "logical_bytes": 10, "checksum": catalog_checksum([catalog_path_hash(entry["path"])])})
    assert wrong.status_code == 409, wrong.text
    still_pending_view = client.get(f"/api/v1/admin/devices/{d['device_id']}/snapshot-catalog", params={"snapshot_id": snap_hex, "path": ""})
    assert still_pending_view.json()["state"] != "READY"

    right = client.post(f"/api/v1/agent/snapshot-catalogs/{cat_id}/finalize", headers=_headers(d), json={"file_count": 1, "directory_count": 0, "logical_bytes": 10, "checksum": catalog_checksum([catalog_path_hash(entry["path"])])})
    assert right.status_code == 200, right.text
    assert right.json()["state"] == "READY"


def test_finalize_on_an_already_ready_catalog_with_matching_totals_is_a_noop(client):
    login(client)
    d, snap_hex, _ = _bound_snapshot(client, "cat-finalize-02")
    cat_id = client.post("/api/v1/agent/snapshot-catalogs", headers=_headers(d), json={"snapshot_id": snap_hex, **_declared(declared_file_count=0, declared_directory_count=0)}).json()["catalog_id"]

    from app.services import catalog_checksum

    body = {"file_count": 0, "directory_count": 0, "logical_bytes": 0, "checksum": catalog_checksum([])}
    first = client.post(f"/api/v1/agent/snapshot-catalogs/{cat_id}/finalize", headers=_headers(d), json=body)
    second = client.post(f"/api/v1/agent/snapshot-catalogs/{cat_id}/finalize", headers=_headers(d), json=body)
    assert first.status_code == 200 and second.status_code == 200


def test_fail_marks_the_catalog_failed_and_never_finalizes_over_it(client):
    login(client)
    d, snap_hex, _ = _bound_snapshot(client, "cat-fail-01")
    cat_id = client.post("/api/v1/agent/snapshot-catalogs", headers=_headers(d), json={"snapshot_id": snap_hex, **_declared()}).json()["catalog_id"]
    r = client.post(f"/api/v1/agent/snapshot-catalogs/{cat_id}/fail", headers=_headers(d), json={"error_class": "AUTH"})
    assert r.status_code == 200, r.text
    view = client.get(f"/api/v1/admin/devices/{d['device_id']}/snapshot-catalog", params={"snapshot_id": snap_hex, "path": ""})
    assert view.json()["state"] == "FAILED"


def test_the_read_endpoint_only_ever_serves_a_ready_catalog(client):
    login(client)
    d, snap_hex, _ = _bound_snapshot(client, "cat-read-01")
    cat_id = client.post("/api/v1/agent/snapshot-catalogs", headers=_headers(d), json={"snapshot_id": snap_hex, **_declared(declared_file_count=1, declared_directory_count=0)}).json()["catalog_id"]
    pending = client.get(f"/api/v1/admin/devices/{d['device_id']}/snapshot-catalog", params={"snapshot_id": snap_hex, "path": ""})
    assert pending.json()["state"] == "PENDING" and pending.json()["entries"] == []

    entry = _entry("/C/Users/Belge.txt", "Belge.txt")
    client.post(f"/api/v1/agent/snapshot-catalogs/{cat_id}/entries", headers=_headers(d), json={"entries": [entry]})
    from app.services import catalog_checksum, catalog_path_hash

    client.post(f"/api/v1/agent/snapshot-catalogs/{cat_id}/finalize", headers=_headers(d), json={"file_count": 1, "directory_count": 0, "logical_bytes": 10, "checksum": catalog_checksum([catalog_path_hash(entry["path"])])})

    ready = client.get(f"/api/v1/admin/devices/{d['device_id']}/snapshot-catalog", params={"snapshot_id": snap_hex, "path": "/C/Users"})
    body = ready.json()
    assert body["state"] == "READY"
    assert [e["name"] for e in body["entries"]] == ["Belge.txt"]
    assert body["entries"][0]["size"] == 10


def test_the_read_endpoint_needs_a_login_and_isolates_devices(client):
    login(client)
    a, snap_a, _ = _bound_snapshot(client, "cat-read-02a")
    b, _, _ = _bound_snapshot(client, "cat-read-02b")
    cross = client.get(f"/api/v1/admin/devices/{b['device_id']}/snapshot-catalog", params={"snapshot_id": snap_a, "path": ""})
    assert cross.json()["state"] == ""  # b has no catalog for a's snapshot -- must not leak a's
    client.post("/api/v1/auth/logout")
    assert client.get(f"/api/v1/admin/devices/{a['device_id']}/snapshot-catalog", params={"snapshot_id": snap_a, "path": ""}).status_code == 401


def test_a_large_directory_paginates(client):
    login(client)
    d, snap_hex, _ = _bound_snapshot(client, "cat-read-03")
    cat_id = client.post("/api/v1/agent/snapshot-catalogs", headers=_headers(d), json={"snapshot_id": snap_hex, **_declared(declared_file_count=250, declared_directory_count=0)}).json()["catalog_id"]
    entries = [_entry(f"/C/Users/file-{i:04d}.txt", f"file-{i:04d}.txt") for i in range(250)]
    for i in range(0, 250, 100):
        client.post(f"/api/v1/agent/snapshot-catalogs/{cat_id}/entries", headers=_headers(d), json={"entries": entries[i : i + 100]})
    from app.services import catalog_checksum, catalog_path_hash

    client.post(f"/api/v1/agent/snapshot-catalogs/{cat_id}/finalize", headers=_headers(d), json={"file_count": 250, "directory_count": 0, "logical_bytes": 2500, "checksum": catalog_checksum([catalog_path_hash(e["path"]) for e in entries])})

    page1 = client.get(f"/api/v1/admin/devices/{d['device_id']}/snapshot-catalog", params={"snapshot_id": snap_hex, "path": "/C/Users", "limit": 100}).json()
    assert len(page1["entries"]) == 100 and page1["truncated"] is True and page1["cursor"]
    page2 = client.get(f"/api/v1/admin/devices/{d['device_id']}/snapshot-catalog", params={"snapshot_id": snap_hex, "path": "/C/Users", "limit": 100, "cursor": page1["cursor"]}).json()
    assert len(page2["entries"]) == 100
    seen = {e["name"] for e in page1["entries"]} | {e["name"] for e in page2["entries"]}
    assert len(seen) == 200


def test_search_within_a_snapshot_is_turkish_aware_and_bounded(client):
    login(client)
    d, snap_hex, _ = _bound_snapshot(client, "cat-search-01")
    cat_id = client.post("/api/v1/agent/snapshot-catalogs", headers=_headers(d), json={"snapshot_id": snap_hex, **_declared(declared_file_count=2, declared_directory_count=0)}).json()["catalog_id"]
    entries = [_entry("/C/Users/Çimko Araç Listesi.xlsx", "Çimko Araç Listesi.xlsx"), _entry("/C/Users/other.txt", "other.txt")]
    client.post(f"/api/v1/agent/snapshot-catalogs/{cat_id}/entries", headers=_headers(d), json={"entries": entries})
    from app.services import catalog_checksum, catalog_path_hash

    client.post(f"/api/v1/agent/snapshot-catalogs/{cat_id}/finalize", headers=_headers(d), json={"file_count": 2, "directory_count": 0, "logical_bytes": 20, "checksum": catalog_checksum([catalog_path_hash(e["path"]) for e in entries])})

    found = client.get(f"/api/v1/admin/devices/{d['device_id']}/snapshot-catalog/search", params={"snapshot_id": snap_hex, "q": "cimko"}).json()
    assert [m["name"] for m in found["matches"]] == ["Çimko Araç Listesi.xlsx"]
    assert found["matches"][0]["folder"] == "/C/Users"
    assert client.get(f"/api/v1/admin/devices/{d['device_id']}/snapshot-catalog/search", params={"snapshot_id": snap_hex, "q": "does-not-exist"}).json()["matches"] == []


def test_files_search_spans_this_devices_snapshots_and_names_which_one(client):
    login(client)
    d = _enroll(client, "cat-files-01", "branch")
    h = _headers(d)

    def make_snapshot_with(name: str) -> str:
        snap_hex = (new_id().replace("-", "") + new_id().replace("-", ""))[:64]
        client.post("/api/v1/agent/jobs/events", headers=h, json={"outcome": "SUCCEEDED", "snapshot_id": snap_hex, "slot_key": f"cf-{name}"})
        cat_id = client.post("/api/v1/agent/snapshot-catalogs", headers=h, json={"snapshot_id": snap_hex, **_declared(declared_file_count=1, declared_directory_count=0)}).json()["catalog_id"]
        entry = _entry(f"/C/Users/{name}", name)
        client.post(f"/api/v1/agent/snapshot-catalogs/{cat_id}/entries", headers=h, json={"entries": [entry]})
        from app.services import catalog_checksum, catalog_path_hash

        client.post(f"/api/v1/agent/snapshot-catalogs/{cat_id}/finalize", headers=h, json={"file_count": 1, "directory_count": 0, "logical_bytes": 10, "checksum": catalog_checksum([catalog_path_hash(entry["path"])])})
        return snap_hex

    older = make_snapshot_with("rapor-eski.xlsx")
    newer = make_snapshot_with("rapor-yeni.xlsx")

    other_device = _enroll(client, "cat-files-02", "branch")
    make_by_other = (new_id().replace("-", "") + new_id().replace("-", ""))[:64]
    client.post("/api/v1/agent/jobs/events", headers=_headers(other_device), json={"outcome": "SUCCEEDED", "snapshot_id": make_by_other, "slot_key": "cf-other"})
    other_cat = client.post("/api/v1/agent/snapshot-catalogs", headers=_headers(other_device), json={"snapshot_id": make_by_other, **_declared(declared_file_count=1, declared_directory_count=0)}).json()["catalog_id"]
    client.post(f"/api/v1/agent/snapshot-catalogs/{other_cat}/entries", headers=_headers(other_device), json={"entries": [_entry("/C/rapor-eski.xlsx", "rapor-eski.xlsx")]})

    found = client.get(f"/api/v1/admin/devices/{d['device_id']}/files", params={"q": "rapor"}).json()
    names_and_snaps = {(m["name"], m["snapshot_id"]) for m in found["matches"]}
    assert names_and_snaps == {("rapor-eski.xlsx", older), ("rapor-yeni.xlsx", newer)}


def test_backfill_creates_one_command_per_missing_catalog_and_dedupes(client):
    login(client)
    d = _enroll(client, "cat-backfill-01", "branch")
    h = _headers(d)
    snaps = []
    for i in range(3):
        snap_hex = (new_id().replace("-", "") + new_id().replace("-", ""))[:64]
        client.post("/api/v1/agent/jobs/events", headers=h, json={"outcome": "SUCCEEDED", "snapshot_id": snap_hex, "slot_key": f"bf-{i}"})
        snaps.append(snap_hex)
    # one of the three already has a catalog -- must not get a redundant command
    client.post("/api/v1/agent/snapshot-catalogs", headers=h, json={"snapshot_id": snaps[0], **_declared()})

    first = client.post(f"/api/v1/admin/devices/{d['device_id']}/backfill-catalogs")
    assert first.status_code == 200, first.text
    created = first.json()["command_ids"]
    assert len(created) == 2

    second = client.post(f"/api/v1/admin/devices/{d['device_id']}/backfill-catalogs")
    assert second.json()["command_ids"] == created, "a second click before the first finishes must not queue duplicates"


def test_build_snapshot_catalog_is_an_allowed_agent_command_kind():
    from app.security import ALLOWED_COMMANDS

    assert "BUILD_SNAPSHOT_CATALOG" in ALLOWED_COMMANDS
