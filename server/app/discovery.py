"""Local business-data discovery. Never reads document contents."""

from __future__ import annotations

import os
from dataclasses import asdict, dataclass, field
from pathlib import Path

OFFICE_EXTS = {".xlsx", ".xls", ".xlsm", ".csv", ".docx", ".doc", ".pptx", ".ppt", ".pdf", ".accdb", ".mdb"}
PST_EXTS = {".pst"}
OST_EXTS = {".ost"}
SENSITIVE_EXTS = {".pem", ".key", ".pfx", ".p12"}
SENSITIVE_NAMES = {".env", ".env.local", ".env.production", "credentials.json", "secrets.json", "id_rsa", "id_ed25519"}

SKIP_DIR_NAMES = {
    "temp",
    "tmp",
    "cache",
    "caches",
    "inetcache",
    "inetcachewordpress",
    "temporary internet files",
    "node_modules",
    "bower_components",
    ".git",
    ".svn",
    ".hg",
    ".venv",
    "venv",
    "__pycache__",
    ".pytest_cache",
    ".mypy_cache",
    ".ruff_cache",
    ".tox",
    ".nuget",
    ".cargo",
    ".rustup",
    "npm-cache",
    "pip-cache",
    "go-build",
    "appdata",
    "application data",
    "local settings",
    "cookies",
    "nethood",
    "printhood",
    "recent",
    "sendto",
    "start menu",
    "templates",
    "testcorpus",
}

GAME_DIR_MARKERS = {
    "steam",
    "steamapps",
    "epic games",
    "ubisoft",
    "origin games",
    "xbox games",
    "gog galaxy",
    "battle.net",
    "stardew valley",
    "worldbox",
    "mount and blade",
    "bannerlord",
    "better.mart",
    "internet.cafe.simulator",
}

DEFAULT_SKIP_DOWNLOADS = True
FILE_ATTRIBUTE_REPARSE_POINT = 0x0400


@dataclass
class KnownFolders:
    profile: str = ""
    desktop: str = ""
    documents: str = ""
    pictures: str = ""
    downloads: str = ""
    redirected: dict[str, bool] = field(default_factory=dict)
    missing: list[str] = field(default_factory=list)


@dataclass
class Candidate:
    path: str
    category: str
    file_count: int
    bytes: int
    default_selected: bool
    reason: str = ""
    warning: str = ""
    requires_explicit: bool = False
    kind: str = "directory"  # directory | file | category


def _attrs(path: Path) -> int:
    try:
        return int(path.stat().st_file_attributes)  # type: ignore[attr-defined]
    except (OSError, AttributeError):
        return 0


def is_reparse(path: Path) -> bool:
    return bool(_attrs(path) & FILE_ATTRIBUTE_REPARSE_POINT)


def _norm(name: str) -> str:
    return name.strip().lower()


def should_skip_dir(name: str) -> bool:
    n = _norm(name)
    if n in SKIP_DIR_NAMES:
        return True
    if n.startswith(".") and n not in {".env"}:
        # skip hidden tool dirs; still allow later explicit sensitive-file hits
        if n in {".ssh"}:
            return False
        return n not in SENSITIVE_NAMES
    return False


def looks_like_game_dir(name: str) -> bool:
    n = _norm(name)
    return any(marker in n for marker in GAME_DIR_MARKERS)


def resolve_known_folders(override: KnownFolders | None = None) -> KnownFolders:
    if override is not None:
        return override
    kf = KnownFolders()
    home = str(Path.home())
    kf.profile = home
    # Prefer live Windows Known Folders; fall back to common OneDrive redirects.
    mapped = _windows_known_folders()
    kf.desktop = mapped.get("desktop") or _first_existing(
        [str(Path(home) / "OneDrive" / "Desktop"), str(Path(home) / "Desktop")]
    )
    kf.documents = mapped.get("documents") or _first_existing(
        [str(Path(home) / "OneDrive" / "Belgeler"), str(Path(home) / "OneDrive" / "Documents"), str(Path(home) / "Documents")]
    )
    kf.pictures = mapped.get("pictures") or _first_existing(
        [str(Path(home) / "OneDrive" / "Resimler"), str(Path(home) / "OneDrive" / "Pictures"), str(Path(home) / "Pictures")]
    )
    kf.downloads = mapped.get("downloads") or _first_existing([str(Path(home) / "Downloads")])
    naive_desktop = str(Path(home) / "Desktop")
    naive_docs = str(Path(home) / "Documents")
    kf.redirected["desktop"] = bool(kf.desktop and os.path.normcase(kf.desktop) != os.path.normcase(naive_desktop))
    kf.redirected["documents"] = bool(kf.documents and os.path.normcase(kf.documents) != os.path.normcase(naive_docs))
    for key, val in (("desktop", kf.desktop), ("documents", kf.documents), ("pictures", kf.pictures), ("downloads", kf.downloads)):
        if not val or not Path(val).exists():
            kf.missing.append(key)
    return kf


def _first_existing(paths: list[str]) -> str:
    for p in paths:
        if p and Path(p).exists():
            return p
    return paths[0] if paths else ""


def _windows_known_folders() -> dict[str, str]:
    out: dict[str, str] = {}
    try:
        import ctypes
        from ctypes import wintypes

        ole32 = ctypes.windll.ole32  # type: ignore[attr-defined]
        shell32 = ctypes.windll.shell32  # type: ignore[attr-defined]

        class GUID(ctypes.Structure):
            _fields_ = [
                ("Data1", ctypes.c_uint32),
                ("Data2", ctypes.c_uint16),
                ("Data3", ctypes.c_uint16),
                ("Data4", ctypes.c_ubyte * 8),
            ]

        def _guid(text: str) -> GUID:
            g = GUID()
            ole32.CLSIDFromString(ctypes.c_wchar_p("{" + text + "}"), ctypes.byref(g))
            return g

        ids = {
            "desktop": "B4BFCC3A-DB2C-424C-B029-7FE99A87C641",
            "documents": "FDD39AD0-238F-46AF-ADB4-6C85480369C7",
            "pictures": "33E28130-4E1E-4676-835A-98395C3BC3BB",
            "downloads": "374DE290-123F-4565-9164-39C4925E467B",
        }
        for key, raw in ids.items():
            ptr = ctypes.c_wchar_p()
            hr = shell32.SHGetKnownFolderPath(ctypes.byref(_guid(raw)), 0, None, ctypes.byref(ptr))
            if hr == 0 and ptr.value:
                out[key] = ptr.value
            if ptr:
                ctypes.windll.ole32.CoTaskMemFree(ptr)  # type: ignore[attr-defined]
        _ = wintypes
    except Exception:
        return {}
    return out


def is_sensitive(path: Path) -> bool:
    name = path.name
    if name in SENSITIVE_NAMES:
        return True
    return path.suffix.lower() in SENSITIVE_EXTS


def walk_files(root: Path, *, budget: int = 40_000, skip_reparse: bool = True) -> list[Path]:
    found: list[Path] = []
    if not root.exists():
        return found
    stack = [root]
    while stack and len(found) < budget:
        cur = stack.pop()
        try:
            with os.scandir(cur) as it:
                for entry in it:
                    if len(found) >= budget:
                        break
                    try:
                        if entry.is_symlink():
                            continue
                        p = Path(entry.path)
                        if entry.is_dir(follow_symlinks=False):
                            if skip_reparse and is_reparse(p) and p != root:
                                continue
                            if should_skip_dir(entry.name) or looks_like_game_dir(entry.name):
                                continue
                            stack.append(p)
                        elif entry.is_file(follow_symlinks=False):
                            found.append(p)
                    except OSError:
                        continue
        except OSError:
            continue
    return found


def _file_size(path: Path) -> int:
    try:
        return int(path.stat().st_size)
    except OSError:
        return 0


def discover(
    *,
    known: KnownFolders | None = None,
    extra_roots: list[str] | None = None,
    include_downloads: bool = False,
    budget_per_root: int = 20_000,
) -> dict:
    kf = resolve_known_folders(known)
    candidates: list[Candidate] = []
    notes: list[str] = []

    scan_roots: list[tuple[str, str, bool]] = []
    if kf.desktop and Path(kf.desktop).exists():
        scan_roots.append((kf.desktop, "known_folder_desktop", True))
    if kf.documents and Path(kf.documents).exists():
        scan_roots.append((kf.documents, "known_folder_documents", True))
    if kf.pictures and Path(kf.pictures).exists():
        scan_roots.append((kf.pictures, "known_folder_pictures", False))
    if kf.downloads and Path(kf.downloads).exists():
        scan_roots.append((kf.downloads, "downloads", include_downloads))
    for extra in extra_roots or []:
        if extra and Path(extra).exists():
            scan_roots.append((extra, "extra", False))

    outlook_dirs = []
    if kf.profile:
        outlook_dirs.extend(
            [
                str(Path(kf.profile) / "AppData" / "Local" / "Microsoft" / "Outlook"),
                str(Path(kf.profile) / "AppData" / "Roaming" / "Microsoft" / "Outlook"),
            ]
        )
    for odir in outlook_dirs:
        if Path(odir).exists():
            scan_roots.append((odir, "outlook_cache_dir", False))

    seen: set[str] = set()
    pst_hits: list[Candidate] = []
    ost_hits: list[Candidate] = []
    sensitive_hits: list[Candidate] = []
    office_by_root: dict[str, list[Path]] = {}
    extra_by_root: dict[str, list[Path]] = {}
    games: list[Candidate] = []

    for root, category, default_dir in scan_roots:
        key = os.path.normcase(os.path.abspath(root))
        if key in seen:
            continue
        seen.add(key)
        root_p = Path(root)
        if looks_like_game_dir(root_p.name):
            games.append(
                Candidate(
                    path=str(root_p),
                    category="game",
                    file_count=0,
                    bytes=0,
                    default_selected=False,
                    reason="Looks like a game folder",
                    warning="Excluded by default",
                )
            )
            continue
        files = walk_files(root_p, budget=budget_per_root)
        office: list[Path] = []
        extra_other: list[Path] = []
        for f in files:
            suf = f.suffix.lower()
            if suf in PST_EXTS:
                pst_hits.append(
                    Candidate(
                        path=str(f),
                        category="outlook_pst",
                        file_count=1,
                        bytes=_file_size(f),
                        default_selected=True,
                        reason="Business mailbox data",
                        kind="file",
                    )
                )
            elif suf in OST_EXTS:
                ost_hits.append(
                    Candidate(
                        path=str(f),
                        category="outlook_ost",
                        file_count=1,
                        bytes=_file_size(f),
                        default_selected=False,
                        reason="Outlook offline cache; normally rebuildable and should not be backed up",
                        warning="OST is a local cache, not the mailbox source of truth",
                        kind="file",
                    )
                )
            elif is_sensitive(f):
                sensitive_hits.append(
                    Candidate(
                        path=str(f),
                        category="sensitive",
                        file_count=1,
                        bytes=_file_size(f),
                        default_selected=False,
                        requires_explicit=True,
                        warning="Possible secret or credential file. Not included unless you select it.",
                        kind="file",
                    )
                )
            elif category == "extra":
                # An operator-picked custom folder (tools/stowline-setup's
                # folder-browse endpoint) backs up everything under it, not
                # just office files -- unlike a known folder, which offers
                # the narrower office_files subset as an alternative to the
                # whole directory. PST/OST/sensitive hits above still get
                # their own separately-gated candidates either way.
                extra_other.append(f)
            elif suf in OFFICE_EXTS:
                office.append(f)
        if office:
            office_by_root[str(root_p)] = office
            bytes_sum = sum(_file_size(f) for f in office)
            candidates.append(
                Candidate(
                    path=str(root_p),
                    category=category,
                    file_count=len(office),
                    bytes=bytes_sum,
                    default_selected=default_dir and category != "downloads",
                    reason=f"{len(office)} office/business files under this folder",
                    kind="directory",
                )
            )
            candidates.append(
                Candidate(
                    path=str(root_p),
                    category="office_files",
                    file_count=len(office),
                    bytes=bytes_sum,
                    default_selected=False,
                    reason="Select only office/PDF/Access files in this folder, not the whole folder",
                    kind="category",
                )
            )
        elif category == "extra":
            # Without this branch, a custom folder with zero office/PST/OST/
            # sensitive files inside produced no candidate at all -- picking
            # it via Gozat would silently do nothing. Every extra root gets
            # a row regardless of what's inside it.
            extra_by_root[str(root_p)] = extra_other
            bytes_sum = sum(_file_size(f) for f in extra_other)
            candidates.append(
                Candidate(
                    path=str(root_p),
                    category="extra",
                    file_count=len(extra_other),
                    bytes=bytes_sum,
                    default_selected=False,
                    reason=(
                        f"{len(extra_other)} dosya -- Gözat ile eklendi"
                        if extra_other
                        else "Bu klasörde yedeklenecek dosya yok (hepsi hassas/PST/OST olabilir, onlar ayrı listelenir)"
                    ),
                    kind="directory",
                )
            )
        elif default_dir and category.startswith("known_folder"):
            candidates.append(
                Candidate(
                    path=str(root_p),
                    category=category,
                    file_count=0,
                    bytes=0,
                    default_selected=False,
                    reason="Known Folder resolved; no office files found",
                    kind="directory",
                )
            )
        if category == "downloads":
            notes.append("Downloads is discovered but excluded unless the administrator selects it.")

    if kf.documents:
        for child in _child_dirs(Path(kf.documents)):
            if looks_like_game_dir(child.name):
                games.append(
                    Candidate(
                        path=str(child),
                        category="game",
                        file_count=0,
                        bytes=0,
                        default_selected=False,
                        reason="Game folder inside Documents",
                        warning="Excluded by default",
                    )
                )

    candidates.extend(pst_hits)
    candidates.extend(ost_hits)
    candidates.extend(sensitive_hits)
    candidates.extend(games)

    return {
        "schema_version": 1,
        "known_folders": {
            "profile": kf.profile,
            "desktop": kf.desktop,
            "documents": kf.documents,
            "pictures": kf.pictures,
            "downloads": kf.downloads,
            "redirected": kf.redirected,
            "missing": kf.missing,
        },
        "candidates": [asdict(c) for c in candidates],
        "pst_count": len(pst_hits),
        "ost_count": len(ost_hits),
        "sensitive_count": len(sensitive_hits),
        "notes": notes
        + [
            "Document contents were not read.",
            "OST files are excluded by default.",
            "PST files are selected by default as business mail.",
            "Qualification TestCorpus and caches are not proposed.",
        ],
        "office_files": {root: [str(p) for p in files] for root, files in office_by_root.items()},
        "extra_files": {root: [str(p) for p in files] for root, files in extra_by_root.items()},
    }


def _child_dirs(root: Path) -> list[Path]:
    out: list[Path] = []
    try:
        with os.scandir(root) as it:
            for entry in it:
                if entry.is_dir(follow_symlinks=False):
                    out.append(Path(entry.path))
    except OSError:
        return out
    return out


def collect_device_identity(agent_path: str = r"C:\Stowline\bin\stowline-agent.exe") -> dict:
    import hashlib
    import platform
    import shutil

    host = platform.node()
    release = platform.platform()
    user = os.environ.get("USERNAME") or os.environ.get("USER") or ""
    profile = str(Path.home())
    disks = []
    for p in _windows_drives():
        usage = shutil.disk_usage(p)
        disks.append(
            {
                "path": p,
                "total_bytes": usage.total,
                "free_bytes": usage.free,
            }
        )
    sha = ""
    version = ""
    if Path(agent_path).is_file():
        h = hashlib.sha256()
        with open(agent_path, "rb") as fh:
            for chunk in iter(lambda: fh.read(1024 * 1024), b""):
                h.update(chunk)
        sha = h.hexdigest()
        version = "stowline-agent (hash verified locally)"
    return {
        "hostname": host,
        "windows_version": release,
        "current_user": user,
        "profile": profile,
        "disks": disks,
        "agent_path": agent_path if Path(agent_path).is_file() else "",
        "agent_sha256": sha,
        "agent_version": version,
    }


def _windows_drives() -> list[str]:
    drives = []
    if os.name == "nt":
        for letter in "CDEFGHIJKLMNOPQRSTUVWXYZ":
            p = f"{letter}:\\"
            if os.path.exists(p):
                drives.append(p)
    else:
        drives.append(str(Path.home().anchor or "/"))
    return drives or [str(Path.home())]
