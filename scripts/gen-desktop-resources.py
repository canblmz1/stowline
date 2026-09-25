"""Generate the icon + manifest resources for the three Stowline desktop programs.

Writes agent/cmd/<program>/rsrc_windows_amd64.syso (committed, so a normal
`go build` / build-release.ps1 needs no resource tooling). Re-run only when
an icon or manifest changes:

    python scripts/gen-desktop-resources.py

Needs Pillow and windres (MinGW) on PATH.
"""
from __future__ import annotations

import shutil
import subprocess
import tempfile
from pathlib import Path

from PIL import Image, ImageDraw

REPO = Path(__file__).resolve().parents[1]

# (program dir, tile colour, glyph, execution level)
PROGRAMS = [
    ("stowline-backups", (31, 79, 216), "check", "asInvoker"),
    ("stowline-admin", (11, 27, 63), "grid", "asInvoker"),
    ("stowline-setup", (15, 123, 74), "arrow", "requireAdministrator"),
]

MANIFEST = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0" xmlns:asmv3="urn:schemas-microsoft-com:asm.v3">
  <assemblyIdentity type="win32" name="Stowline.{name}" version="1.0.0.0" processorArchitecture="amd64"/>
  <trustInfo xmlns="urn:schemas-microsoft-com:asm.v3">
    <security><requestedPrivileges><requestedExecutionLevel level="{level}" uiAccess="false"/></requestedPrivileges></security>
  </trustInfo>
  <compatibility xmlns="urn:schemas-microsoft-com:compatibility.v1">
    <application><supportedOS Id="{{8e0f7a12-bfb3-4fe8-b9a5-48fd50a15a9a}}"/></application>
  </compatibility>
  <asmv3:application>
    <asmv3:windowsSettings>
      <dpiAware xmlns="http://schemas.microsoft.com/SMI/2005/WindowsSettings">true/pm</dpiAware>
      <dpiAwareness xmlns="http://schemas.microsoft.com/SMI/2016/WindowsSettings">PerMonitorV2</dpiAwareness>
    </asmv3:windowsSettings>
  </asmv3:application>
  <dependency><dependentAssembly>
    <assemblyIdentity type="win32" name="Microsoft.Windows.Common-Controls" version="6.0.0.0" processorArchitecture="*" publicKeyToken="6595b64144ccf1df" language="*"/>
  </dependentAssembly></dependency>
</assembly>
"""


def draw_icon(size: int, tile: tuple[int, int, int], glyph: str) -> Image.Image:
    s = size * 4  # supersample, then downscale for clean edges
    img = Image.new("RGBA", (s, s), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)
    d.rounded_rectangle((0, 0, s - 1, s - 1), radius=s * 0.22, fill=tile + (255,))
    # shield
    cx, top, w, h = s / 2, s * 0.17, s * 0.56, s * 0.68
    shield = [
        (cx, top), (cx + w / 2, top + h * 0.14), (cx + w / 2, top + h * 0.5),
        (cx + w * 0.34, top + h * 0.78), (cx, top + h), (cx - w * 0.34, top + h * 0.78),
        (cx - w / 2, top + h * 0.5), (cx - w / 2, top + h * 0.14),
    ]
    d.polygon(shield, fill=(255, 255, 255, 255))
    ink = tile + (255,)
    lw = max(2, int(s * 0.065))
    if glyph == "check":
        d.line([(cx - s * 0.13, top + h * 0.5), (cx - s * 0.03, top + h * 0.63), (cx + s * 0.15, top + h * 0.36)], fill=ink, width=lw, joint="curve")
    elif glyph == "grid":
        g = s * 0.075
        for dx in (-1, 1):
            for dy in (-1, 1):
                x0, y0 = cx + dx * g * 1.15 - g, top + h * 0.47 + dy * g * 1.15 - g
                d.rounded_rectangle((x0, y0, x0 + 2 * g * 0.85, y0 + 2 * g * 0.85), radius=g * 0.3, fill=ink)
    else:  # arrow down into a tray
        d.line([(cx, top + h * 0.28), (cx, top + h * 0.62)], fill=ink, width=lw)
        d.line([(cx - s * 0.1, top + h * 0.5), (cx, top + h * 0.64), (cx + s * 0.1, top + h * 0.5)], fill=ink, width=lw, joint="curve")
    return img.resize((size, size), Image.LANCZOS)


def main() -> None:
    windres = shutil.which("windres")
    if not windres:
        raise SystemExit("windres not found on PATH (install MinGW / WinLibs)")
    for name, tile, glyph, level in PROGRAMS:
        out_dir = REPO / "agent" / "cmd" / name
        with tempfile.TemporaryDirectory() as tmp:
            tmp = Path(tmp)
            sizes = [16, 20, 24, 32, 40, 48, 64, 128, 256]
            images = [draw_icon(sz, tile, glyph) for sz in sizes]
            ico = tmp / "app.ico"
            images[-1].save(ico, sizes=[(sz, sz) for sz in sizes], append_images=images[:-1])
            (tmp / "app.manifest").write_text(MANIFEST.format(name=name, level=level), encoding="utf-8")
            (tmp / "app.rc").write_text('1 ICON "app.ico"\n1 24 "app.manifest"\n', encoding="utf-8")
            syso = out_dir / "rsrc_windows_amd64.syso"
            subprocess.run([windres, "-O", "coff", "-F", "pe-x86-64", "-i", "app.rc", "-o", str(syso)], cwd=tmp, check=True)
            images[-1].save(out_dir / "icon-256.png")
            print(f"wrote {syso.relative_to(REPO)}")


if __name__ == "__main__":
    main()
