"""Bounded, non-secret owner context for the canonical standalone owner path.

The context identifies one private Codex home and the supported auth-store
location for that home.  It never parses or exports credential contents.
Synthetic homes are used by the tests; callers must not point this helper at
an arbitrary credential database.
"""

from __future__ import annotations

import hashlib
import json
import os
import stat
from pathlib import Path
from typing import Any, Mapping


MAX_STORE_BYTES = 65536
AUTH_STORE_NAME = "auth.json"
CONFIG_STORAGE_MODES = frozenset({"codex_home", "project"})


def _absolute_no_symlink(path: str | os.PathLike[str], *, directory: bool) -> tuple[Path, os.stat_result]:
    raw = os.fspath(path)
    if not isinstance(raw, str) or not os.path.isabs(raw) or any(c in raw for c in "\0\r\n"):
        raise ValueError("owner context path must be absolute")
    value = Path(raw)
    if not os.path.lexists(value):
        raise ValueError("owner context path is missing")
    try:
        resolved = value.resolve(strict=True)
    except OSError as exc:
        raise ValueError("owner context path cannot be canonicalized") from exc
    # A canonical owner path may not contain a symlink, an alias, or a ..
    # component.  Comparing the complete path catches intermediate aliases.
    if resolved != value:
        raise ValueError("owner context path symlink chain is not allowed")
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


def _read_bounded_file(path: Path, label: str) -> tuple[os.stat_result, bytes]:
    """Read one private file through one no-follow descriptor with bounded identity checks."""
    try:
        fd = os.open(str(path), os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except OSError as exc:
        raise ValueError(f"{label} is not safely readable") from exc
    try:
        before = os.fstat(fd)
        if (not stat.S_ISREG(before.st_mode) or stat.S_IMODE(before.st_mode) != 0o600
                or before.st_uid != os.getuid() or before.st_nlink != 1
                or not 0 < before.st_size <= MAX_STORE_BYTES):
            raise ValueError(f"{label} is not private and bounded")
        raw = os.read(fd, MAX_STORE_BYTES + 1)
        after = os.fstat(fd)
        stable = (before.st_dev, before.st_ino, before.st_mode, before.st_uid,
                  before.st_nlink, before.st_size, before.st_mtime_ns) == (
                      after.st_dev, after.st_ino, after.st_mode, after.st_uid,
                      after.st_nlink, after.st_size, after.st_mtime_ns)
        if not stable or len(raw) != before.st_size or not 0 < len(raw) <= MAX_STORE_BYTES:
            raise ValueError(f"{label} changed during bounded read")
        return before, raw
    finally:
        os.close(fd)


def _read_auth_store(path: Path) -> tuple[os.stat_result, bytes]:
    return _read_bounded_file(path, "canonical auth store")


def _config_projection(home: Path) -> dict[str, Any]:
    path = home / "config.toml"
    try:
        info = os.lstat(path)
    except FileNotFoundError:
        return {"path": str(path), "present": False}
    except OSError as exc:
        raise ValueError("canonical config cannot be inspected") from exc
    if stat.S_ISLNK(info.st_mode):
        raise ValueError("canonical config symlink is not allowed")
    canonical, _ = _absolute_no_symlink(path, directory=False)
    checked, raw = _read_bounded_file(canonical, "canonical config")
    return {
        "path": str(canonical),
        "present": True,
        "st_dev": checked.st_dev,
        "st_ino": checked.st_ino,
        "mode": stat.S_IMODE(checked.st_mode),
        "uid": checked.st_uid,
        "bytes": len(raw),
        "sha256": hashlib.sha256(raw).hexdigest(),
    }


def _auth_projection(home: Path, *, requires_openai_auth: bool) -> dict[str, Any]:
    path = home / AUTH_STORE_NAME
    try:
        info = os.lstat(path)
    except FileNotFoundError:
        if requires_openai_auth:
            raise ValueError("canonical auth store is required")
        return {"path": str(path), "present": False}
    except OSError as exc:
        raise ValueError("canonical auth store cannot be inspected") from exc
    if not requires_openai_auth:
        # No-auth mode freezes absence.  A provider cannot silently acquire an
        # OpenAI auth file between prepare and send.
        raise ValueError("canonical auth store must be absent in no-auth mode")
    if stat.S_ISLNK(info.st_mode):
        raise ValueError("canonical auth store symlink is not allowed")
    canonical, _ = _absolute_no_symlink(path, directory=False)
    checked, raw = _read_auth_store(canonical)
    # Use the descriptor's fstat identity and the single read for the digest.
    return {
        "path": str(canonical),
        "present": True,
        "st_dev": checked.st_dev,
        "st_ino": checked.st_ino,
        "mode": stat.S_IMODE(checked.st_mode),
        "uid": checked.st_uid,
        "bytes": len(raw),
        "sha256": hashlib.sha256(raw).hexdigest(),
    }


def capture_owner_context(
    codex_home: str | os.PathLike[str],
    credential_store: str | os.PathLike[str] | None = None,
    *,
    requires_openai_auth: bool = True,
    config_storage_mode: str = "codex_home",
) -> dict[str, Any]:
    """Capture the supported owner context from a canonical/synthetic home.

    ``credential_store`` is retained only as a compatibility argument for the
    old helper call shape.  It must be exactly ``<home>/auth.json``; arbitrary
    store paths are rejected.  No-auth providers must explicitly request
    ``requires_openai_auth=False`` and freeze the absent auth file plus storage
    mode.
    """
    if type(requires_openai_auth) is not bool:
        raise ValueError("requires_openai_auth must be boolean")
    if config_storage_mode not in CONFIG_STORAGE_MODES:
        raise ValueError("unsupported config storage mode")
    if not requires_openai_auth and config_storage_mode != "project":
        raise ValueError("no-auth context requires project storage mode")
    home, home_info = _absolute_no_symlink(codex_home, directory=True)
    if stat.S_IMODE(home_info.st_mode) != 0o700:
        raise ValueError("owner home must be private")
    canonical_store = home / AUTH_STORE_NAME
    if credential_store is not None:
        supplied = Path(os.fspath(credential_store))
        if not supplied.is_absolute() or supplied != canonical_store:
            raise ValueError("credential store must be the canonical auth.json")
    auth = _auth_projection(home, requires_openai_auth=requires_openai_auth)
    value: dict[str, Any] = {
        "version": 2,
        "expected_codex_home": str(home),
        "requires_openai_auth": requires_openai_auth,
        "config_storage_mode": config_storage_mode,
        "home_identity": {
            "st_dev": home_info.st_dev,
            "st_ino": home_info.st_ino,
            "mode": stat.S_IMODE(home_info.st_mode),
            "uid": home_info.st_uid,
        },
        "credential_store": auth,
        # Freeze config existence and a bounded metadata/digest projection;
        # storage/provider changes cannot be silently introduced at send time.
        "config_file": _config_projection(home),
    }
    value["fingerprint_sha256"] = owner_context_sha256(value)
    return value


def validate_owner_context(value: Mapping[str, Any], expected: Mapping[str, Any]) -> dict[str, Any]:
    if not isinstance(value, Mapping) or not isinstance(expected, Mapping):
        raise ValueError("owner context is malformed")
    if dict(value) != dict(expected):
        raise ValueError("owner context does not match")
    if value.get("version") != 2 or value.get("fingerprint_sha256") != owner_context_sha256(value):
        raise ValueError("owner context fingerprint is invalid")
    try:
        current = capture_owner_context(
            value["expected_codex_home"],
            credential_store=value["credential_store"]["path"],
            requires_openai_auth=value["requires_openai_auth"],
            config_storage_mode=value["config_storage_mode"],
        )
    except (KeyError, TypeError, ValueError):
        raise ValueError("owner context files changed") from None
    if current != dict(value):
        raise ValueError("owner context files changed")
    return dict(value)


def validate_initialize_codex_home(result: Mapping[str, Any], expected: Mapping[str, Any]) -> bool:
    if not isinstance(result, Mapping) or result.get("codexHome") != expected.get("expected_codex_home"):
        raise ValueError("initialize codexHome does not match owner context")
    return True
