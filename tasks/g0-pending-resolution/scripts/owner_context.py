"""Non-secret owner/home context checks for the canonical standalone owner path.

This helper intentionally does not open credential databases or inspect token
contents.  The credential store supplied by a caller is treated as a bounded
regular file and only its metadata plus SHA-256 are committed to the owner
context.
"""

from __future__ import annotations

import hashlib
import json
import os
import stat
from pathlib import Path
from typing import Any, Mapping


MAX_STORE_BYTES = 65536


def _absolute_no_symlink(path: str | os.PathLike[str], *, directory: bool) -> tuple[Path, os.stat_result]:
    value = Path(os.path.abspath(os.fspath(path)))
    if not value.is_absolute() or os.path.lexists(value) is False:
        raise ValueError("owner context path is missing")
    info = os.lstat(value)
    if stat.S_ISLNK(info.st_mode):
        raise ValueError("owner context path symlink is not allowed")
    if directory and not stat.S_ISDIR(info.st_mode):
        raise ValueError("expected owner home directory")
    if not directory and not stat.S_ISREG(info.st_mode):
        raise ValueError("expected regular credential store")
    if info.st_uid != os.getuid() or (not directory and info.st_nlink != 1):
        raise ValueError("owner context path identity is not owned")
    return value, info


def _canonical(value: Any) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False)


def owner_context_sha256(value: Mapping[str, Any]) -> str:
    body = dict(value)
    body.pop("fingerprint_sha256", None)
    return hashlib.sha256(_canonical(body).encode()).hexdigest()


def capture_owner_context(codex_home: str | os.PathLike[str], credential_store: str | os.PathLike[str]) -> dict[str, Any]:
    home, home_info = _absolute_no_symlink(codex_home, directory=True)
    store, store_info = _absolute_no_symlink(credential_store, directory=False)
    if stat.S_IMODE(home_info.st_mode) != 0o700:
        raise ValueError("owner home must be private")
    if stat.S_IMODE(store_info.st_mode) != 0o600:
        raise ValueError("credential store must be private")
    raw = store.read_bytes()
    if not 0 < len(raw) <= MAX_STORE_BYTES:
        raise ValueError("credential store exceeds bounded context size")
    value = {
        "version": 1,
        "expected_codex_home": str(home),
        "home_identity": {
            "st_dev": home_info.st_dev,
            "st_ino": home_info.st_ino,
            "mode": stat.S_IMODE(home_info.st_mode),
            "uid": home_info.st_uid,
        },
        "credential_store": {
            "path": str(store),
            "st_dev": store_info.st_dev,
            "st_ino": store_info.st_ino,
            "mode": stat.S_IMODE(store_info.st_mode),
            "uid": store_info.st_uid,
            "bytes": len(raw),
            "sha256": hashlib.sha256(raw).hexdigest(),
        },
    }
    value["fingerprint_sha256"] = owner_context_sha256(value)
    return value


def validate_owner_context(value: Mapping[str, Any], expected: Mapping[str, Any]) -> dict[str, Any]:
    if not isinstance(value, Mapping) or not isinstance(expected, Mapping):
        raise ValueError("owner context is malformed")
    if dict(value) != dict(expected):
        raise ValueError("owner context does not match")
    if value.get("version") != 1 or value.get("fingerprint_sha256") != owner_context_sha256(value):
        raise ValueError("owner context fingerprint is invalid")
    # Recheck the current synthetic/controlled files, so a copied self-report
    # cannot authorize a different home or credential store.
    current = capture_owner_context(value["expected_codex_home"], value["credential_store"]["path"])
    if current != dict(value):
        raise ValueError("owner context files changed")
    return dict(value)


def validate_initialize_codex_home(result: Mapping[str, Any], expected: Mapping[str, Any]) -> bool:
    if not isinstance(result, Mapping) or result.get("codexHome") != expected.get("expected_codex_home"):
        raise ValueError("initialize codexHome does not match owner context")
    return True
