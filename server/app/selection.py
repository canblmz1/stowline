"""Compile an administrator backup selection into explicit source_roots.

The qualified agent backs up `pilot.json` `source_roots` as positional
restic paths. Individual files are valid roots. This compiler never emits
a drive-root plus a giant exclude list.
"""

from __future__ import annotations

import fnmatch
import hashlib
import json
import os
import re
from pathlib import Path

from app.discovery import OFFICE_EXTS, OST_EXTS, PST_EXTS, SENSITIVE_EXTS, SENSITIVE_NAMES, is_sensitive, walk_files

MANIFEST_SCHEMA = 1

# A Windows drive-letter ("C:\...", "C:/...") or UNC ("\\server\share") path
# is already absolute on its own terms. This control plane runs in a Linux
# container, where os.path.abspath() does not recognize either form as
# absolute and silently prepends the container's CWD (confirmed live:
# admin selections were showing "/app/C:\Users\...\file.pst"). It also
# never splits on backslash, so a Windows-style "..\.." traversal segment
# would pass the parts-based check below unnoticed on that platform --
# this is why the check now splits on both separators explicitly, not
# through Path(), which follows host path semantics.
_WINDOWS_ABS_RE = re.compile(r"^(?:[A-Za-z]:[\\/]|\\\\)")


class SelectionError(ValueError):
    pass


def _abspath(path: str) -> str:
    raw = (path or "").strip()
    if not raw or "\x00" in raw:
        raise SelectionError("invalid path")
    if ".." in raw.replace("\\", "/").split("/"):
        raise SelectionError("invalid path")
    if _WINDOWS_ABS_RE.match(raw):
        return raw
    return os.path.abspath(raw)


def compile_selection(
    *,
    selected: list[dict],
    office_files: dict[str, list[str]] | None = None,
    extra_files: dict[str, list[str]] | None = None,
    sensitive_files: list[str] | None = None,
) -> dict:
    """selected items: {path, kind, category, requires_explicit?}

    Folders are backup roots (so files added to them later are backed up
    too). Sensitive files (credentials, keys) that discover() found under a
    selected folder but that were not themselves selected and confirmed go
    to "exclude_paths", which the agent passes to restic as --exclude; OST
    caches are always excluded. extra_files is accepted for compatibility
    and no longer used."""
    roots: list[str] = []
    extra_roots: list[str] = []
    files: list[str] = []
    excluded: list[dict] = []
    warnings: list[str] = []
    explicit_sensitive = 0

    for item in selected or []:
        path = _abspath(str(item.get("path") or ""))
        kind = str(item.get("kind") or "directory")
        category = str(item.get("category") or "")
        if category == "outlook_ost":
            excluded.append({"path": path, "reason": "OST is an Outlook cache and stays excluded"})
            continue
        if category == "game":
            excluded.append({"path": path, "reason": "game folder"})
            continue
        if category == "downloads" and not item.get("confirmed"):
            excluded.append({"path": path, "reason": "Downloads requires explicit confirmation"})
            continue
        if item.get("requires_explicit") or category == "sensitive" or is_sensitive(Path(path)):
            if not item.get("confirmed"):
                raise SelectionError(f"sensitive path requires explicit confirmation: {path}")
            explicit_sensitive += 1
            warnings.append(f"Sensitive path included by administrator: {Path(path).name}")
        if kind == "category" and category == "office_files":
            listed = (office_files or {}).get(path) or (office_files or {}).get(item.get("path") or "")
            if not listed:
                # Resolve now from disk without reading contents. walk_files
                # (not a raw rglob) caps the scan and skips reparse points --
                # an uncapped rglob over a real Desktop/Documents tree can
                # run long enough that the wizard's "Next" click looks frozen,
                # and a permission-denied subdirectory would otherwise raise
                # an uncaught OSError here.
                try:
                    listed = [str(p) for p in walk_files(Path(path)) if p.suffix.lower() in OFFICE_EXTS]
                except OSError:
                    listed = []
            for fp in listed:
                files.append(_abspath(fp))
            continue
        if kind == "directory" and category == "extra":
            # An operator-browsed custom folder is a real root, like the
            # known folders: emitting its files one by one (as before) meant
            # anything added to it after setup was never backed up. Its
            # sensitive descendants are kept out via exclude_paths below.
            roots.append(path)
            extra_roots.append(path)
            continue
        if kind == "file" or Path(path).is_file():
            files.append(path)
        else:
            roots.append(path)

    # de-dupe while preserving order; drop a file if a selected root already covers it
    ordered: list[str] = []
    seen: set[str] = set()
    for p in roots + files:
        key = os.path.normcase(p)
        if key in seen:
            continue
        seen.add(key)
        ordered.append(p)

    covered_files = []
    final = []
    norm_roots = [os.path.normcase(r) for r in roots]
    for p in ordered:
        np = os.path.normcase(p)
        if Path(p).is_file():
            parent = os.path.normcase(str(Path(p).parent))
            if any(parent == r or parent.startswith(r + os.sep) for r in norm_roots):
                covered_files.append(p)
                continue
        final.append(p)

    if not final:
        raise SelectionError("at least one backup path must be selected")

    # Sensitive files under a selected folder stay out unless their own
    # candidate was selected (and confirmed): with discovery data use its
    # list; without it (a caller with no discovery), walk the custom folders.
    if sensitive_files is None:
        sensitive_files = []
        for r in extra_roots:
            try:
                sensitive_files += [str(p) for p in walk_files(Path(r)) if is_sensitive(p)]
            except OSError:
                pass
    chosen = {os.path.normcase(p) for p in roots + files}
    dir_roots = [os.path.normcase(r) for r in final if not Path(r).is_file()]
    exclude_paths = []
    for sp in sensitive_files:
        ap = _abspath(sp)
        n = os.path.normcase(ap)
        if n in chosen:
            continue
        if any(n.startswith(r.rstrip(os.sep) + os.sep) for r in dir_roots):
            exclude_paths.append(ap)
    # Name patterns also keep out credentials created after setup; a pattern
    # is dropped only when an explicitly confirmed file matches it (the
    # listed paths above still exclude that pattern's other known hits).
    confirmed_names = {Path(p).name.lower() for p in roots + files if is_sensitive(Path(p))}
    patterns = [
        pat
        for pat in sorted(SENSITIVE_NAMES) + sorted("*" + ext for ext in SENSITIVE_EXTS)
        if not any(fnmatch.fnmatch(n, pat) for n in confirmed_names)
    ]
    exclude_paths = sorted(set(exclude_paths)) + patterns + ["*.ost"]

    file_count, logical_bytes = estimate_paths(final)
    manifest = {
        "schema_version": MANIFEST_SCHEMA,
        "source_roots": final,
        "exclude_paths": exclude_paths,
        "selected_file_count": file_count,
        "selected_logical_bytes": logical_bytes,
        "excluded": excluded,
        "warnings": warnings,
        "explicit_sensitive_count": explicit_sensitive,
        "covered_by_parent_root": covered_files,
        "mechanism": "explicit-source-roots",
        "notes": [
            "Qualified agent 0.2.4 reads source_roots from the endpoint pilot.json.",
            "Control-plane copies are versioned metadata and policy display.",
            "Restic 0.19.1 accepts directories and individual files as positional backup targets.",
        ],
    }
    manifest["content_hash"] = hashlib.sha256(
        json.dumps({k: manifest[k] for k in ("source_roots", "exclude_paths", "excluded")}, sort_keys=True, ensure_ascii=True).encode("utf-8")
    ).hexdigest()
    return manifest


def estimate_paths(paths: list[str]) -> tuple[int, int]:
    """Counts files and bytes under each selected path. Directories go
    through walk_files -- the same capped, reparse-point-skipping walker
    discovery.py uses -- instead of a raw os.walk(): an uncapped walk over
    a real Desktop/Documents tree (which is default-selected whenever it
    has office files) can run long enough that clicking Next on the
    selection screen looks frozen, with no cap and no way to tell slow
    apart from stuck."""
    files = 0
    nbytes = 0
    for raw in paths:
        p = Path(raw)
        if p.is_file():
            files += 1
            try:
                nbytes += p.stat().st_size
            except OSError:
                continue
            continue
        if not p.is_dir():
            continue
        for fp in walk_files(p):
            files += 1
            try:
                nbytes += fp.stat().st_size
            except OSError:
                continue
    return files, nbytes


def apply_pilot_source_roots(pilot_json_path: str, source_roots: list[str], exclude_paths: list[str] | None = None) -> dict:
    path = Path(pilot_json_path)
    if not path.is_file():
        raise SelectionError(f"pilot.json missing: {path}")
    data = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(data, dict):
        raise SelectionError("pilot.json is not an object")
    data["source_roots"] = list(source_roots)
    if exclude_paths is not None:
        data["exclude_paths"] = list(exclude_paths)
    path.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    return data


def write_local_manifest(dest: str, manifest: dict) -> None:
    path = Path(dest)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(manifest, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
