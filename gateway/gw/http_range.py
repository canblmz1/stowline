"""HTTP Range parsing for restic REST GET. Single byte range only."""

from __future__ import annotations

from fastapi import HTTPException, Response


class RangeUnsatisfiable(Exception):
    def __init__(self, size: int):
        self.size = size


def parse_byte_range(header: str | None, size: int) -> tuple[int, int] | None:
    if not header:
        return None
    raw = header.strip()
    if not raw.lower().startswith("bytes="):
        raise HTTPException(400, "unsupported range unit")
    spec = raw[6:].strip()
    if "," in spec:
        raise HTTPException(400, "multiple ranges are not supported")
    if size <= 0:
        raise RangeUnsatisfiable(size)
    if spec.startswith("-"):
        if spec[1:] == "":
            raise HTTPException(400, "invalid range")
        try:
            suffix = int(spec[1:])
        except ValueError as exc:
            raise HTTPException(400, "invalid range") from exc
        if suffix <= 0:
            raise RangeUnsatisfiable(size)
        start = max(size - suffix, 0)
        end = size - 1
        return start, end
    if "-" not in spec:
        raise HTTPException(400, "invalid range")
    left, right = spec.split("-", 1)
    try:
        start = int(left)
        end = int(right) if right != "" else size - 1
    except ValueError as exc:
        raise HTTPException(400, "invalid range") from exc
    if start < 0 or start >= size or end < start:
        raise RangeUnsatisfiable(size)
    if end >= size:
        end = size - 1
    return start, end


def ranged_response(body: bytes, start: int, end: int, media_type: str = "application/octet-stream") -> Response:
    slice_ = body[start : end + 1]
    return Response(
        content=slice_,
        status_code=206,
        media_type=media_type,
        headers={
            "Content-Length": str(len(slice_)),
            "Content-Range": f"bytes {start}-{end}/{len(body)}",
            "Accept-Ranges": "bytes",
        },
    )


def unsatisfiable_response(size: int) -> Response:
    return Response(status_code=416, headers={"Content-Range": f"bytes */{size}"})
