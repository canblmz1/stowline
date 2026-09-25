"""Optional escrow copy of each computer's backup (Restic) password.

The password is generated on the endpoint and normally lives only there, so a
dead computer takes it along. This module seals a copy with a key that is kept
outside the database (STOWLINE_ESCROW_KEY); the database holds only ciphertext.
Nothing here decides who may see a secret: that is services.py, which also
audits every store and every view.
"""

from __future__ import annotations

import hashlib
import re
import time

from cryptography.fernet import Fernet, InvalidToken

from app.config import settings

KIND_RESTIC = "restic-password"
MIN_SECRET_LEN = 8
MAX_SECRET_LEN = 256
FAILURE_LIMIT = 5
FAILURE_WINDOW_SECONDS = 600

_VISIBLE_ASCII = re.compile(r"^[\x21-\x7e]+$")
_failures: dict[str, list[float]] = {}


class VaultError(Exception):
    pass


class VaultNotConfigured(VaultError):
    pass


def _fernet() -> Fernet:
    key = (settings.escrow_key or "").strip()
    if not key:
        raise VaultNotConfigured("STOWLINE_ESCROW_KEY is not set")
    try:
        return Fernet(key.encode("ascii"))
    except ValueError as exc:
        raise VaultNotConfigured("STOWLINE_ESCROW_KEY is not a valid Fernet key") from exc


def enabled() -> bool:
    try:
        _fernet()
    except VaultNotConfigured:
        return False
    return True


def fingerprint(secret: str) -> str:
    """Same recipe as scripts/key-escrow Get-KeyFingerprint, so a copy on paper
    can be matched to what the vault holds: SHA-256 of the UTF-8 text, first 12
    hex characters, upper case, in groups of four."""
    digest = hashlib.sha256(secret.encode("utf-8")).hexdigest().upper()[:12]
    return "-".join(digest[i : i + 4] for i in (0, 4, 8))


def validate_secret(secret: str) -> str:
    text = (secret or "").strip() if isinstance(secret, str) else ""
    if not (MIN_SECRET_LEN <= len(text) <= MAX_SECRET_LEN):
        raise VaultError(f"Anahtar {MIN_SECRET_LEN}-{MAX_SECRET_LEN} karakter olmalı.")
    if not _VISIBLE_ASCII.match(text):
        raise VaultError("Anahtar yalnızca görünür ASCII karakterlerden oluşmalı (boşluk ve satır sonu olamaz).")
    return text


def seal(secret: str) -> str:
    return _fernet().encrypt(secret.encode("utf-8")).decode("ascii")


def unseal(token: str) -> str:
    try:
        return _fernet().decrypt(token.encode("ascii")).decode("utf-8")
    except InvalidToken as exc:
        raise VaultError("stored secret cannot be opened with the current vault key") from exc


def throttle_remaining(user_id: str, now: float | None = None) -> int:
    """Seconds until another attempt is allowed, 0 when none is locked out."""
    at = time.time() if now is None else now
    recent = [t for t in _failures.get(user_id, []) if at - t < FAILURE_WINDOW_SECONDS]
    _failures[user_id] = recent
    if len(recent) < FAILURE_LIMIT:
        return 0
    return max(1, int(FAILURE_WINDOW_SECONDS - (at - recent[0])))


def record_failure(user_id: str, now: float | None = None) -> None:
    _failures.setdefault(user_id, []).append(time.time() if now is None else now)


def clear_failures(user_id: str) -> None:
    _failures.pop(user_id, None)


def reset_throttle() -> None:
    _failures.clear()
