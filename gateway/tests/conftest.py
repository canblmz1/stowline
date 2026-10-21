"""CI engine validation is mandatory; ordinary developer runs may skip engines."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess

import pytest


@pytest.fixture(scope="session", autouse=True)
def require_pinned_engines():
    if os.environ.get("STOWLINE_REQUIRE_REAL_ENGINES") != "1":
        return
    manifest = json.loads((Path(__file__).parents[2] / "release/manifest.json").read_text())
    for name in ("restic", "rclone"):
        binary = shutil.which(name)
        if not binary:
            pytest.fail(f"required {name} binary missing from PATH")
        if hashlib.sha256(Path(binary).read_bytes()).hexdigest() != manifest[f"{name}_sha256"]:
            pytest.fail(f"required {name} binary digest mismatch")
        version = subprocess.run([binary, "version"], capture_output=True, text=True, check=True, timeout=15).stdout
        if manifest[f"{name}_version"] not in version.splitlines()[0]:
            pytest.fail(f"required {name} version mismatch")
