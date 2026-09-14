"""Opt-in native Codex 0.154 refresh-tripwire regression.

This fixture uses only synthetic credentials in new /private/tmp homes.  It is
reviewable by default and must not run unless the caller explicitly supplies
the pinned native binary and opts in.
"""

from __future__ import annotations

import asyncio
import base64
import ctypes
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import socketserver
import stat
import subprocess
import sys
import tempfile
import threading
import time
from dataclasses import dataclass
from http.server import BaseHTTPRequestHandler
from typing import Callable, Mapping, Protocol

import pytest


ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "plugins" / "codex-orchestrator" / "lib"))

from trial.auth_guard import (  # noqa: E402
    MetadataObservation,
    REFRESH_OVERRIDE_ENV,
    REFRESH_TRIPWIRE,
    extract_safe_auth_metadata,
    prepare_guarded_environment,
)


EXPECTED_NATIVE_SHA256 = (
    "4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc"
)
EXPECTED_VERSION_OUTPUT = "codex-cli 0.154.0"
SYNTHETIC_REFRESH_TOKEN = "synthetic-refresh-token-never-from-user-storage"
STDERR_DIAGNOSTIC_PREFIX_LIMIT = 4096
STDERR_SANITIZED_CONTEXT_LIMIT = 512
RUN_OPT_IN = os.environ.get("CODEX_RUN_NATIVE_AUTH_TRIPWIRE") == "1"
DIAGNOSTIC_CONTEXT_OPT_IN = (
    os.environ.get("CODEX_RUN_NATIVE_AUTH_DIAGNOSTIC_CONTEXT") == "1"
)
NATIVE_BINARY_INPUT = os.environ.get("CODEX_TRIAL_NATIVE_BINARY")
MANAGED_CONFIG_PATHS = (
    Path("/etc/codex/config.toml"),
    Path("/etc/codex/requirements.toml"),
)
SYSTEM_REQUIREMENTS_PATHS = (
    "/etc/codex/requirements.toml",
    "/private/etc/codex/requirements.toml",
)

NATIVE_OPT_IN = pytest.mark.skipif(
    not RUN_OPT_IN or not NATIVE_BINARY_INPUT,
    reason="native auth tripwire requires explicit fixed-binary opt-in",
)


class _FixtureMetadataSource:
    def read_verified_nonsecret_metadata(self) -> MetadataObservation:
        return MetadataObservation(
            native_version="0.154.0",
            native_sha256=EXPECTED_NATIVE_SHA256,
            auth_kind="managed_chatgpt",
            bootstrap_auth_kind="managed_chatgpt",
            credential_store="file",
            external_auth_configured=False,
            use_agent_identity=False,
            source_kind="fixed_native_internal_sdk",
            proof_sha256="f" * 64,
        )


@dataclass(frozen=True)
class _FixtureHome:
    root: Path
    home: Path
    codex_home: Path
    sqlite_home: Path
    workspace: Path
    temp: Path
    socket_path: Path
    auth_path: Path
    auth_bytes: bytes


@dataclass(frozen=True)
class _ProcessIdentity:
    pid: int
    uid: int
    pgid: int
    birth: str
    executable: str


class _VersionProcess(Protocol):
    pid: int

    def communicate(self, timeout: float) -> tuple[bytes, object]: ...

    def wait(self, timeout: float) -> int: ...


class NativeFixtureUninterruptible(RuntimeError):
    def __init__(self, owner: _ProcessIdentity, fixture_root: Path) -> None:
        self.safe_report = {
            "code": "native_version_process_uninterruptible",
            "pid": owner.pid,
            "uid": owner.uid,
            "pgid": owner.pgid,
            "birth": owner.birth,
            "fixture_root": str(fixture_root),
        }
        super().__init__("native_version_process_uninterruptible")


class NativeAppServerDiagnostic(RuntimeError):
    def __init__(self, safe_report: Mapping[str, object]) -> None:
        self.safe_report = dict(safe_report)
        super().__init__(
            json.dumps(self.safe_report, sort_keys=True, separators=(",", ":"))
        )


class _RefreshObservation:
    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._count = 0
        self._unexpected = False

    def record(self, *, valid: bool) -> None:
        with self._lock:
            self._count += 1
            self._unexpected = self._unexpected or not valid

    def snapshot(self) -> tuple[int, bool]:
        with self._lock:
            return self._count, self._unexpected


def _b64url(payload: bytes) -> str:
    return base64.urlsafe_b64encode(payload).rstrip(b"=").decode("ascii")


def _synthetic_jwt(payload: Mapping[str, object]) -> str:
    header = _b64url(b'{"alg":"none","typ":"JWT"}')
    body = _b64url(json.dumps(payload, separators=(",", ":")).encode("utf-8"))
    signature = _b64url(b"synthetic-signature")
    return f"{header}.{body}.{signature}"


def _synthetic_auth_bytes() -> bytes:
    id_token = _synthetic_jwt(
        {
            "email": "synthetic@example.invalid",
            "https://api.openai.com/auth": {
                "chatgpt_plan_type": "plus",
                "chatgpt_user_id": "synthetic-user",
                "chatgpt_account_id": "synthetic-account",
            },
        }
    )
    expired_access_token = _synthetic_jwt({"sub": "synthetic-user", "exp": 1})
    payload = {
        "auth_mode": "chatgpt",
        "OPENAI_API_KEY": None,
        "tokens": {
            "id_token": id_token,
            "access_token": expired_access_token,
            "refresh_token": SYNTHETIC_REFRESH_TOKEN,
            "account_id": "synthetic-account",
        },
        "last_refresh": "2000-01-01T00:00:00Z",
        "agent_identity": None,
        "personal_access_token": None,
        "bedrock_api_key": None,
        "bedrock_access_keys": None,
    }
    return (json.dumps(payload, separators=(",", ":")) + "\n").encode("utf-8")


def _owned_private_dir(path: Path) -> None:
    info = os.lstat(path)
    if (
        stat.S_ISLNK(info.st_mode)
        or not stat.S_ISDIR(info.st_mode)
        or info.st_uid != os.getuid()
        or stat.S_IMODE(info.st_mode) != 0o700
    ):
        raise AssertionError("fixture_private_directory_invalid")


def _write_exclusive(path: Path, data: bytes) -> None:
    flags = (
        os.O_WRONLY
        | os.O_CREAT
        | os.O_EXCL
        | getattr(os, "O_NOFOLLOW", 0)
        | getattr(os, "O_CLOEXEC", 0)
    )
    fd = os.open(path, flags, 0o600)
    try:
        remaining = memoryview(data)
        while remaining:
            written = os.write(fd, remaining)
            if written <= 0:
                raise OSError("fixture_write_failed")
            remaining = remaining[written:]
        os.fsync(fd)
    finally:
        os.close(fd)


def _new_fixture_home() -> _FixtureHome:
    root = Path(tempfile.mkdtemp(prefix="g0-auth-", dir="/private/tmp"))
    os.chmod(root, 0o700)
    if root.parent != Path("/private/tmp") or not root.name.startswith("g0-auth-"):
        raise AssertionError("fixture_root_outside_private_tmp")
    _owned_private_dir(root)
    home = root / "home"
    codex_home = root / "codex-home"
    sqlite_home = root / "sqlite"
    workspace = root / "workspace"
    temp = root / "tmp"
    for directory in (home, codex_home, sqlite_home, workspace, temp):
        os.mkdir(directory, 0o700)
        _owned_private_dir(directory)

    config = (
        'cli_auth_credentials_store = "file"\n'
        "\n"
        "[features]\n"
        "use_agent_identity = false\n"
        "respect_system_proxy = false\n"
    ).encode("utf-8")
    _write_exclusive(codex_home / "config.toml", config)
    auth_bytes = _synthetic_auth_bytes()
    auth_path = codex_home / "auth.json"
    _write_exclusive(auth_path, auth_bytes)
    return _FixtureHome(
        root=root,
        home=home,
        codex_home=codex_home,
        sqlite_home=sqlite_home,
        workspace=workspace,
        temp=temp,
        socket_path=root / "app-server.sock",
        auth_path=auth_path,
        auth_bytes=auth_bytes,
    )


def _stable_sha256(path: Path) -> str:
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_CLOEXEC", 0)
    fd = os.open(path, flags)
    digest = hashlib.sha256()
    try:
        before = os.fstat(fd)
        if (
            not stat.S_ISREG(before.st_mode)
            or before.st_uid != os.getuid()
            or not before.st_mode & 0o111
            or before.st_size > 512 * 1024 * 1024
        ):
            raise AssertionError("native_binary_invalid")
        while chunk := os.read(fd, 1024 * 1024):
            digest.update(chunk)
        after = os.fstat(fd)
    finally:
        os.close(fd)
    fields = ("st_dev", "st_ino", "st_uid", "st_mode", "st_size", "st_mtime_ns")
    if any(getattr(before, name) != getattr(after, name) for name in fields):
        raise AssertionError("native_binary_changed_while_hashing")
    return digest.hexdigest()


def _verify_native_binary(raw: str) -> Path:
    binary = Path(raw)
    if not binary.is_absolute():
        raise AssertionError("native_binary_must_be_absolute")
    if binary.resolve(strict=True) != binary:
        raise AssertionError("native_binary_path_not_canonical")
    info = os.lstat(binary)
    if (
        stat.S_ISLNK(info.st_mode)
        or not stat.S_ISREG(info.st_mode)
        or info.st_uid != os.getuid()
        or not info.st_mode & 0o111
    ):
        raise AssertionError("native_binary_identity_invalid")
    if _stable_sha256(binary) != EXPECTED_NATIVE_SHA256:
        raise AssertionError("native_binary_sha256_mismatch")
    current = os.lstat(binary)
    fields = ("st_dev", "st_ino", "st_uid", "st_mode", "st_size", "st_mtime_ns")
    if any(getattr(info, field) != getattr(current, field) for field in fields):
        raise AssertionError("native_binary_changed_during_admission")
    return binary


def _quote_sbpl(path: Path) -> str:
    value = str(path)
    if not path.is_absolute() or any(char in value for char in '\r\n\x00"'):
        raise AssertionError("sandbox_path_invalid")
    return value.replace("\\", "\\\\")


def _sanitize_synthetic_stderr_context(
    stderr_prefix: bytes,
    home: _FixtureHome,
) -> str:
    try:
        context = stderr_prefix.decode("utf-8", errors="strict")
    except UnicodeDecodeError:
        return "context_non_utf8"

    for path in SYSTEM_REQUIREMENTS_PATHS:
        context = context.replace(path, "<SYSTEM_REQUIREMENTS>")

    known_synthetic = (
        SYNTHETIC_REFRESH_TOKEN,
        "synthetic@example.invalid",
        "synthetic-signature",
        "synthetic-account",
        "synthetic-user",
        str(home.auth_path),
        str(home.socket_path),
        str(home.codex_home),
        str(home.sqlite_home),
        str(home.workspace),
        str(home.temp),
        str(home.home),
        str(home.root),
    )
    for value in sorted(known_synthetic, key=len, reverse=True):
        context = context.replace(value, "<redacted>")

    context = re.sub(
        r"(?i)\b(?:https?|wss?|ws|unix)://[^\s'\";,]+",
        "<url>",
        context,
    )
    context = re.sub(r"(?<![A-Za-z0-9_])/(?:[^\s'\";,)]*)", "<path>", context)
    context = re.sub(
        r"(?i)\b(authorization|token|password|secret|credential)"
        r"\s*[:=]\s*[^\s;,]+",
        lambda match: match.group(1) + "=<redacted>",
        context,
    )
    context = re.sub(
        r"\b[A-Za-z0-9_-]{20,}(?:\.[A-Za-z0-9_-]{20,}){1,2}\b",
        "<redacted>",
        context,
    )
    context = re.sub(r"\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\b", "<redacted>", context)
    context = re.sub(r"\b[A-Za-z0-9_-]{32,}\b", "<redacted>", context)
    context = "".join(char if 0x20 <= ord(char) <= 0x7E else " " for char in context)
    context = re.sub(r"\s+", " ", context).strip()[:STDERR_SANITIZED_CONTEXT_LIMIT]

    folded = context.casefold()
    if (
        "/" in context
        or "://" in context
        or "@" in context
        or any(value.casefold() in folded for value in known_synthetic)
        or re.search(r"\b[A-Za-z0-9_-]{32,}\b", context)
    ):
        return "context_redaction_failed"
    return context or "context_empty_after_redaction"


def _safe_native_stderr_diagnostic(
    stderr_prefix: bytes,
    *,
    stderr_bytes: int,
    stderr_sha256: str,
    exit_code: int,
    sanitized_context: str | None = None,
) -> dict[str, object]:
    if len(stderr_prefix) > STDERR_DIAGNOSTIC_PREFIX_LIMIT:
        raise AssertionError("native_stderr_prefix_limit_exceeded")
    lowered = stderr_prefix.lower()
    if b"sandbox-exec:" in lowered or b"sandbox violation" in lowered:
        category = "sandbox_denied"
    elif any(marker in lowered for marker in (b"panicked at", b"fatal error")):
        category = "native_fatal"
    elif any(
        marker in lowered
        for marker in (b"unexpected argument", b"unrecognized option", b"usage:")
    ):
        category = "cli_usage_rejected"
    elif any(
        marker in lowered
        for marker in (b"configuration", b"config.toml", b"toml parse")
    ):
        category = "configuration_rejected"
    elif any(
        marker in lowered
        for marker in (b"failed to bind", b"unix socket", b"address already in use")
    ):
        category = "socket_setup_failed"
    elif any(
        marker in lowered
        for marker in (b"connection refused", b"network is unreachable", b"dns error")
    ):
        category = "network_setup_failed"
    elif stderr_bytes == 0:
        category = "empty"
    else:
        category = "unclassified"

    errno_name = None
    for marker, name in (
        (b"operation not permitted", "operation_not_permitted"),
        (b"permission denied", "permission_denied"),
        (b"address already in use", "address_in_use"),
        (b"connection refused", "connection_refused"),
        (b"no such file or directory", "no_such_file"),
        (b"invalid argument", "invalid_argument"),
    ):
        if marker in lowered:
            errno_name = name
            break

    report: dict[str, object] = {
        "code": "native_exited_before_socket",
        "phase": "app_server_pre_socket",
        "exit_code": exit_code,
        "stderr_category": category,
        "stderr_errno": errno_name,
        "stderr_bytes": stderr_bytes,
        "stderr_sha256": stderr_sha256,
        "stderr_prefix_truncated": stderr_bytes > len(stderr_prefix),
        "stderr_hints": {
            "panicked_at": b"panicked at" in lowered,
            "scproxy": b"scproxy" in lowered,
            "system_configuration": b"systemconfiguration" in lowered,
            "failed_to_create": b"failed to create" in lowered,
            "proxy": b"proxy" in lowered,
        },
    }
    if sanitized_context is not None:
        report["stderr_context_sanitized"] = sanitized_context
    return report


def _raise_native_exited_before_socket(
    stderr_prefix: bytearray,
    *,
    stderr_bytes: int,
    stderr_sha256: str,
    exit_code: int,
    sanitized_context: str | None = None,
) -> None:
    try:
        safe_report = _safe_native_stderr_diagnostic(
            bytes(stderr_prefix),
            stderr_bytes=stderr_bytes,
            stderr_sha256=stderr_sha256,
            exit_code=exit_code,
            sanitized_context=sanitized_context,
        )
    finally:
        stderr_prefix.clear()
    raise NativeAppServerDiagnostic(safe_report)


def _strict_sandbox_profile(home: _FixtureHome, binary: Path, port: int) -> str:
    binary_ancestors = []
    current = binary.parent
    while current != Path("/"):
        binary_ancestors.append(current)
        current = current.parent
    lines = [
        "(version 1)",
        "(deny default)",
        '(import "dyld-support.sb")',
        "(allow process-fork)",
        "(allow signal (target self))",
        "(allow sysctl-read)",
        '(allow file-read* (subpath "/System"))',
        '(allow file-read* (subpath "/usr/lib"))',
        '(allow file-read* (subpath "/usr/share"))',
        '(allow file-read* (subpath "/private/etc"))',
        '(allow file-read-metadata file-test-existence\n'
        '       (literal "/private")\n'
        '       (literal "/private/tmp"))',
        '(allow file-read-metadata file-test-existence\n'
        '       (literal "/etc")\n'
        '       (literal "/etc/codex")\n'
        '       (literal "/private/etc")\n'
        '       (literal "/private/etc/codex"))',
        '(allow file-read* file-test-existence\n'
        '       (literal "/etc/codex/requirements.toml")\n'
        '       (literal "/private/etc/codex/requirements.toml"))',
        '(allow file-map-executable\n'
        '       (subpath "/System/Library")\n'
        '       (subpath "/usr/lib"))',
        '(allow file-read* (literal "/dev/null"))',
        '(allow file-read* (literal "/dev/random"))',
        '(allow file-read* (literal "/dev/urandom"))',
        f'(allow file-read* (subpath "{_quote_sbpl(home.root)}"))',
        f'(allow file-read* (literal "{_quote_sbpl(binary)}"))',
        f'(allow process-exec (literal "{_quote_sbpl(binary)}"))',
        f'(allow file-write* (subpath "{_quote_sbpl(home.root)}"))',
        '(allow file-write* (literal "/dev/null"))',
        "(deny network*)",
        f'(allow network-bind (literal "{_quote_sbpl(home.socket_path)}"))',
        f'(allow network-inbound (literal "{_quote_sbpl(home.socket_path)}"))',
        f'(allow network-outbound (literal "{_quote_sbpl(home.socket_path)}"))',
        f'(allow network-outbound (remote tcp4 "localhost:{port}"))',
        '(deny mach-lookup (global-name "com.apple.SecurityServer"))',
        '(deny mach-lookup (global-name "com.apple.securityd"))',
        '(deny mach-lookup (global-name "com.apple.securityd.system"))',
        '(deny mach-lookup (global-name "com.apple.securityd.systemkeychain"))',
    ]
    lines.extend(
        f'(allow file-read-metadata (literal "{_quote_sbpl(path)}"))'
        for path in binary_ancestors
    )
    return "\n".join(lines) + "\n"


def test_strict_profile_adds_only_apple_dyld_bootstrap_permissions() -> None:
    root = Path("/private/tmp/g0-auth-static-fixture")
    home = _FixtureHome(
        root=root,
        home=root / "home",
        codex_home=root / "codex-home",
        sqlite_home=root / "sqlite",
        workspace=root / "workspace",
        temp=root / "tmp",
        socket_path=root / "app-server.sock",
        auth_path=root / "codex-home/auth.json",
        auth_bytes=b"",
    )
    binary = Path("/Users/synthetic/native/codex")

    profile = _strict_sandbox_profile(home, binary, 43123)

    assert profile.count('(import "dyld-support.sb")') == 1
    assert profile.count("(allow file-map-executable") == 1
    assert (
        '(allow file-map-executable\n'
        '       (subpath "/System/Library")\n'
        '       (subpath "/usr/lib"))'
    ) in profile
    assert (
        '(allow file-read-metadata file-test-existence\n'
        '       (literal "/private")\n'
        '       (literal "/private/tmp"))'
    ) in profile
    assert (
        '(allow file-read-metadata file-test-existence\n'
        '       (literal "/etc")\n'
        '       (literal "/etc/codex")\n'
        '       (literal "/private/etc")\n'
        '       (literal "/private/etc/codex"))'
    ) in profile
    assert (
        '(allow file-read* file-test-existence\n'
        '       (literal "/etc/codex/requirements.toml")\n'
        '       (literal "/private/etc/codex/requirements.toml"))'
    ) in profile
    assert '(subpath "/etc")' not in profile
    assert '(subpath "/etc/codex")' not in profile
    assert '(subpath "/private")' not in profile
    assert '(subpath "/private/tmp")' not in profile
    assert (
        '(allow process-exec '
        '(literal "/Users/synthetic/native/codex"))'
    ) in profile
    assert "(allow process-exec*)" not in profile
    assert "(allow mach-bootstrap)" not in profile
    assert "SYS_shared_region" not in profile
    assert "(deny network*)" in profile
    assert '(deny mach-lookup (global-name "com.apple.securityd"))' in profile


@pytest.mark.parametrize(
    ("stderr", "category", "errno_name"),
    [
        (
            b"error: unexpected argument '--listen' found\nUsage: codex app-server\n",
            "cli_usage_rejected",
            None,
        ),
        (
            b"failed to load configuration: TOML parse error\n",
            "configuration_rejected",
            None,
        ),
        (
            b"failed to bind unix socket: Address already in use\n",
            "socket_setup_failed",
            "address_in_use",
        ),
        (
            b"sandbox-exec: operation not permitted\n",
            "sandbox_denied",
            "operation_not_permitted",
        ),
        (
            b"thread panicked at SystemConfiguration failed to create scproxy proxy\n",
            "native_fatal",
            None,
        ),
        (b"opaque synthetic failure\n", "unclassified", None),
    ],
)
def test_native_stderr_diagnostic_is_bounded_and_nonrevealing(
    stderr: bytes,
    category: str,
    errno_name: str | None,
) -> None:
    secret_marker = b"synthetic-refresh-token-never-report"
    raw = stderr + secret_marker * 500

    report = _safe_native_stderr_diagnostic(
        raw[:4096],
        stderr_bytes=len(raw),
        stderr_sha256=hashlib.sha256(raw).hexdigest(),
        exit_code=2,
    )

    assert report == {
        "code": "native_exited_before_socket",
        "phase": "app_server_pre_socket",
        "exit_code": 2,
        "stderr_category": category,
        "stderr_errno": errno_name,
        "stderr_bytes": len(raw),
        "stderr_sha256": hashlib.sha256(raw).hexdigest(),
        "stderr_prefix_truncated": True,
        "stderr_hints": {
            "panicked_at": b"panicked at" in stderr.lower(),
            "scproxy": b"scproxy" in stderr.lower(),
            "system_configuration": b"systemconfiguration" in stderr.lower(),
            "failed_to_create": b"failed to create" in stderr.lower(),
            "proxy": b"proxy" in stderr.lower(),
        },
    }
    assert secret_marker.decode() not in json.dumps(report, sort_keys=True)


def test_native_exit_diagnostic_clears_captured_stderr() -> None:
    stderr_prefix = bytearray(
        b"failed to load configuration: synthetic-sensitive-marker"
    )

    with pytest.raises(NativeAppServerDiagnostic) as error:
        _raise_native_exited_before_socket(
            stderr_prefix,
            stderr_bytes=len(stderr_prefix),
            stderr_sha256=hashlib.sha256(stderr_prefix).hexdigest(),
            exit_code=1,
        )

    assert stderr_prefix == b""
    assert error.value.safe_report["stderr_category"] == "configuration_rejected"
    assert json.loads(str(error.value)) == error.value.safe_report
    assert "synthetic-sensitive-marker" not in json.dumps(error.value.safe_report)
    assert "synthetic-sensitive-marker" not in str(error.value)


def test_synthetic_diagnostic_context_redacts_paths_urls_and_secrets() -> None:
    root = Path("/private/tmp/g0-auth-sensitive-fixture")
    home = _FixtureHome(
        root=root,
        home=root / "home",
        codex_home=root / "codex-home",
        sqlite_home=root / "sqlite",
        workspace=root / "workspace",
        temp=root / "tmp",
        socket_path=root / "app-server.sock",
        auth_path=root / "codex-home/auth.json",
        auth_bytes=b"synthetic-only",
    )
    raw = (
        b"Error: requirements /etc/codex/requirements.toml; failed to open "
        b"/private/tmp/g0-auth-sensitive-fixture/"
        b"codex-home/auth.json via /System/Library/example; "
        b"POST https://example.invalid/oauth; "
        b"token=synthetic-refresh-token-never-from-user-storage; "
        b"cause: Operation not permitted (os error 1)"
    )

    context = _sanitize_synthetic_stderr_context(raw, home)
    report = _safe_native_stderr_diagnostic(
        raw,
        stderr_bytes=len(raw),
        stderr_sha256=hashlib.sha256(raw).hexdigest(),
        exit_code=1,
        sanitized_context=context,
    )

    assert "failed to open" in context
    assert "Operation not permitted (os error 1)" in context
    assert "<path>" in context
    assert "<SYSTEM_REQUIREMENTS>" in context
    assert "<url>" in context
    assert "<redacted>" in context
    assert "/" not in context
    assert "@" not in context
    assert SYNTHETIC_REFRESH_TOKEN not in context
    assert report["stderr_context_sanitized"] == context


def _minimal_environment(home: _FixtureHome) -> dict[str, str]:
    return {
        "PATH": "/usr/bin:/bin:/usr/sbin:/sbin",
        "HOME": str(home.home),
        "CODEX_HOME": str(home.codex_home),
        "CODEX_SQLITE_HOME": str(home.sqlite_home),
        "PWD": str(home.workspace),
        "TMPDIR": str(home.temp),
        "LANG": "C",
        "LC_ALL": "C",
    }


def _verify_synthetic_diagnostic_preconditions(
    home: _FixtureHome,
    binary: Path,
    environment: Mapping[str, str],
) -> None:
    if home.root.parent != Path("/private/tmp") or not home.root.name.startswith(
        "g0-auth-"
    ):
        raise AssertionError("diagnostic_fixture_root_invalid")
    expected_paths = {
        "home": home.root / "home",
        "codex_home": home.root / "codex-home",
        "sqlite_home": home.root / "sqlite",
        "workspace": home.root / "workspace",
        "temp": home.root / "tmp",
        "socket_path": home.root / "app-server.sock",
        "auth_path": home.root / "codex-home/auth.json",
    }
    if any(getattr(home, name) != path for name, path in expected_paths.items()):
        raise AssertionError("diagnostic_fixture_layout_invalid")
    for directory in (
        home.root,
        home.home,
        home.codex_home,
        home.sqlite_home,
        home.workspace,
        home.temp,
    ):
        _owned_private_dir(directory)

    for path in MANAGED_CONFIG_PATHS:
        try:
            os.lstat(path)
        except FileNotFoundError:
            continue
        raise AssertionError("diagnostic_managed_config_present")

    current_auth, _ = _auth_identity(home.auth_path)
    expected_auth = _synthetic_auth_bytes()
    if current_auth != expected_auth or home.auth_bytes != expected_auth:
        raise AssertionError("diagnostic_auth_not_synthetic")
    if _stable_sha256(binary) != EXPECTED_NATIVE_SHA256:
        raise AssertionError("diagnostic_native_binary_changed")

    child_environment = dict(environment)
    refresh_override = child_environment.pop(REFRESH_OVERRIDE_ENV, None)
    if child_environment != _minimal_environment(home):
        raise AssertionError("diagnostic_environment_not_minimal")
    if refresh_override == REFRESH_TRIPWIRE:
        return
    match = re.fullmatch(
        r"http://127\.0\.0\.1:([0-9]{1,5})/oauth/token",
        refresh_override or "",
    )
    if match is None or not 0 < int(match.group(1)) <= 65535:
        raise AssertionError("diagnostic_refresh_override_invalid")


def _process_identity(pid: int) -> _ProcessIdentity | None:
    completed = subprocess.run(
        ["/bin/ps", "-p", str(pid), "-o", "pid=,uid=,pgid=,lstart="],
        text=True,
        capture_output=True,
        timeout=2,
        check=False,
    )
    fields = completed.stdout.strip().split(None, 7)
    if completed.returncode or len(fields) != 8:
        return None
    libproc = ctypes.CDLL("/usr/lib/libproc.dylib")
    buffer = ctypes.create_string_buffer(4096)
    count = libproc.proc_pidpath(pid, buffer, len(buffer))
    if not 0 < count < len(buffer):
        return None
    return _ProcessIdentity(
        pid=int(fields[0]),
        uid=int(fields[1]),
        pgid=int(fields[2]),
        birth=" ".join(fields[3:]),
        executable=os.fsdecode(buffer.value),
    )


def _same_owned_process(saved: _ProcessIdentity) -> bool:
    current = _process_identity(saved.pid)
    return current is not None and all(
        getattr(current, field) == getattr(saved, field)
        for field in ("pid", "uid", "pgid", "birth")
    )


def _collect_version_output(
    process: _VersionProcess,
    fixture_root: Path,
    *,
    identity_reader: Callable[[int], _ProcessIdentity | None] = _process_identity,
    kill_group: Callable[[int, int], None] = os.killpg,
    timeout: float = 5,
    reap_timeout: float = 2,
) -> bytes:
    if (
        fixture_root.parent != Path("/private/tmp")
        or not fixture_root.name.startswith("g0-auth-")
    ):
        raise AssertionError("fixture_root_outside_private_tmp")
    try:
        stdout, _ = process.communicate(timeout=timeout)
        return stdout
    except subprocess.TimeoutExpired:
        owner = identity_reader(process.pid)
        if (
            owner is None
            or owner.uid != os.getuid()
            or owner.pid != process.pid
            or owner.pgid != process.pid
        ):
            raise AssertionError("native_version_process_ownership_invalid")
        try:
            kill_group(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        try:
            process.wait(timeout=reap_timeout)
        except subprocess.TimeoutExpired:
            raise NativeFixtureUninterruptible(owner, fixture_root) from None
        raise AssertionError("native_version_timeout")


def _verify_native_version(
    binary: Path,
    home: _FixtureHome,
    environment: Mapping[str, str],
    profile: str,
) -> None:
    process = subprocess.Popen(
        ["/usr/bin/sandbox-exec", "-p", profile, str(binary), "--version"],
        cwd=home.workspace,
        env=dict(environment),
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
        start_new_session=True,
    )
    stdout = _collect_version_output(process, home.root)
    if (
        process.returncode != 0
        or len(stdout) > 1024
        or stdout.decode("utf-8", errors="strict").strip() != EXPECTED_VERSION_OUTPUT
    ):
        raise AssertionError("native_version_mismatch")


async def _receive_response(websocket: object, request_id: int) -> dict[str, object]:
    for _ in range(32):
        raw = await asyncio.wait_for(websocket.recv(), timeout=5)  # type: ignore[attr-defined]
        packet = json.loads(raw)
        if "method" in packet:
            continue
        if packet.get("id") != request_id:
            raise AssertionError("unexpected_rpc_response_id")
        return packet
    raise AssertionError("rpc_response_frame_limit")


async def _request_refresh_without_model_turn(socket_path: Path) -> None:
    from websockets.asyncio.client import unix_connect

    websocket = await asyncio.wait_for(
        unix_connect(
            str(socket_path),
            open_timeout=5,
            close_timeout=1,
            max_size=1024 * 1024,
            max_queue=16,
        ),
        timeout=6,
    )
    try:
        await websocket.send(
            json.dumps(
                {
                    "id": 1,
                    "method": "initialize",
                    "params": {
                        "clientInfo": {
                            "name": "native-auth-tripwire-fixture",
                            "version": "1",
                        }
                    },
                }
            )
        )
        initialized = await _receive_response(websocket, 1)
        if "result" not in initialized:
            raise AssertionError("initialize_failed")
        await websocket.send('{"method":"initialized"}')
        await websocket.send(
            '{"id":2,"method":"account/read","params":{"refreshToken":true}}'
        )
        account = await _receive_response(websocket, 2)
        if "result" not in account and "error" not in account:
            raise AssertionError("account_read_missing_result")
    finally:
        await asyncio.wait_for(websocket.close(), timeout=2)


def _run_fixed_native(
    binary: Path,
    home: _FixtureHome,
    environment: Mapping[str, str],
    profile: str,
) -> None:
    if _stable_sha256(binary) != EXPECTED_NATIVE_SHA256:
        raise AssertionError("native_binary_changed_before_launch")
    profile_path = home.root / "sandbox.sb"
    _write_exclusive(profile_path, profile.encode("utf-8"))
    stderr_digest = hashlib.sha256()
    stderr_bytes = 0
    stderr_prefix = bytearray()
    process: subprocess.Popen[bytes] | None = None
    owner: _ProcessIdentity | None = None
    reader: threading.Thread | None = None

    try:
        if DIAGNOSTIC_CONTEXT_OPT_IN:
            _verify_synthetic_diagnostic_preconditions(home, binary, environment)
        process = subprocess.Popen(
            [
                "/usr/bin/sandbox-exec",
                "-f",
                str(profile_path),
                str(binary),
                "app-server",
                "--listen",
                "unix://" + str(home.socket_path),
            ],
            cwd=home.workspace,
            env=dict(environment),
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
            start_new_session=True,
        )

        def drain_stderr() -> None:
            nonlocal stderr_bytes
            assert process is not None and process.stderr is not None
            for chunk in iter(lambda: process.stderr.read(65536), b""):
                stderr_digest.update(chunk)
                stderr_bytes += len(chunk)
                remaining = STDERR_DIAGNOSTIC_PREFIX_LIMIT - len(stderr_prefix)
                if remaining > 0:
                    stderr_prefix.extend(chunk[:remaining])

        reader = threading.Thread(target=drain_stderr, daemon=True)
        reader.start()
        owner = _process_identity(process.pid)
        if (
            owner is None
            or owner.uid != os.getuid()
            or owner.pgid != process.pid
        ):
            raise AssertionError("native_process_ownership_invalid")
        deadline = time.monotonic() + 30
        while not home.socket_path.exists():
            exit_code = process.poll()
            if exit_code is not None:
                reader.join(timeout=1)
                if reader.is_alive():
                    stderr_prefix.clear()
                    raise AssertionError("native_stderr_reader_not_finished")
                sanitized_context = None
                if DIAGNOSTIC_CONTEXT_OPT_IN:
                    sanitized_context = _sanitize_synthetic_stderr_context(
                        bytes(stderr_prefix), home
                    )
                _raise_native_exited_before_socket(
                    stderr_prefix,
                    stderr_bytes=stderr_bytes,
                    stderr_sha256=stderr_digest.hexdigest(),
                    exit_code=exit_code,
                    sanitized_context=sanitized_context,
                )
            if time.monotonic() >= deadline:
                raise AssertionError("native_socket_timeout")
            time.sleep(0.025)
        info = os.lstat(home.socket_path)
        if (
            not stat.S_ISSOCK(info.st_mode)
            or info.st_uid != os.getuid()
            or stat.S_IMODE(info.st_mode) != 0o600
        ):
            raise AssertionError("native_socket_identity_invalid")
        running = _process_identity(process.pid)
        if (
            running is None
            or any(
                getattr(running, field) != getattr(owner, field)
                for field in ("pid", "uid", "pgid", "birth")
            )
            or Path(running.executable) != binary
        ):
            raise AssertionError("native_process_identity_invalid")
        asyncio.run(_request_refresh_without_model_turn(home.socket_path))
    finally:
        if process is not None and process.poll() is None:
            if owner is None or not _same_owned_process(owner):
                raise AssertionError("native_identity_changed_before_cleanup")
            os.killpg(process.pid, signal.SIGTERM)
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                if not _same_owned_process(owner):
                    raise AssertionError("native_identity_changed_before_kill")
                os.killpg(process.pid, signal.SIGKILL)
                process.wait(timeout=2)
        if reader is not None:
            reader.join(timeout=1)
        if process is not None and process.poll() is None:
            raise AssertionError("native_process_not_reaped")
        stderr_limit_exceeded = stderr_bytes > 8 * 1024 * 1024
        stderr_prefix.clear()
        if stderr_limit_exceeded:
            raise AssertionError("native_stderr_limit_exceeded")


def _auth_identity(path: Path) -> tuple[bytes, tuple[int, ...]]:
    info = os.lstat(path)
    if (
        stat.S_ISLNK(info.st_mode)
        or not stat.S_ISREG(info.st_mode)
        or info.st_uid != os.getuid()
        or stat.S_IMODE(info.st_mode) != 0o600
    ):
        raise AssertionError("synthetic_auth_identity_invalid")
    data = path.read_bytes()
    return data, (
        info.st_dev,
        info.st_ino,
        info.st_uid,
        info.st_mode,
        info.st_size,
        info.st_nlink,
        info.st_mtime_ns,
    )


class _UnreapableFakeProcess:
    pid = 41001

    def __init__(self) -> None:
        self.communicate_calls = 0
        self.wait_calls = 0

    def communicate(self, timeout: float) -> tuple[bytes, None]:
        self.communicate_calls += 1
        raise subprocess.TimeoutExpired(["synthetic-native", "--version"], timeout)

    def wait(self, timeout: float) -> int:
        self.wait_calls += 1
        raise subprocess.TimeoutExpired(["synthetic-native", "--version"], timeout)


def test_version_probe_reports_uninterruptible_without_unbounded_wait() -> None:
    process = _UnreapableFakeProcess()
    owner = _ProcessIdentity(
        pid=process.pid,
        uid=os.getuid(),
        pgid=process.pid,
        birth="synthetic birth",
        executable="/synthetic/native",
    )
    killed: list[tuple[int, int]] = []
    fixture_root = Path("/private/tmp/g0-auth-synthetic")

    with pytest.raises(
        NativeFixtureUninterruptible,
        match="^native_version_process_uninterruptible$",
    ) as error:
        _collect_version_output(
            process,
            fixture_root,
            identity_reader=lambda _pid: owner,
            kill_group=lambda pgid, sig: killed.append((pgid, sig)),
            timeout=0.01,
            reap_timeout=0.01,
        )

    assert error.value.safe_report == {
        "code": "native_version_process_uninterruptible",
        "pid": process.pid,
        "uid": os.getuid(),
        "pgid": process.pid,
        "birth": "synthetic birth",
        "fixture_root": str(fixture_root),
    }
    assert process.communicate_calls == 1
    assert process.wait_calls == 1
    assert killed == [(process.pid, signal.SIGKILL)]


@NATIVE_OPT_IN
def test_fixed_native_refresh_tripwire_blocks_oauth_egress() -> None:
    binary = _verify_native_binary(str(NATIVE_BINARY_INPUT))
    observation = _RefreshObservation()

    class Handler(BaseHTTPRequestHandler):
        def do_POST(self) -> None:  # noqa: N802
            length_text = self.headers.get("Content-Length", "")
            valid_length = length_text.isdigit() and 0 < int(length_text) <= 16 * 1024
            body = self.rfile.read(int(length_text)) if valid_length else b""
            valid = False
            if self.path == "/oauth/token" and valid_length:
                try:
                    request = json.loads(body)
                except (UnicodeDecodeError, json.JSONDecodeError):
                    request = None
                valid = request == {
                    "client_id": "app_EMoamEEZ73f0CkXaXp7hrann",
                    "grant_type": "refresh_token",
                    "refresh_token": SYNTHETIC_REFRESH_TOKEN,
                }
            observation.record(valid=valid)
            response = b'{"error":"invalid_grant"}'
            self.send_response(400)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(response)))
            self.end_headers()
            self.wfile.write(response)

        def log_message(self, _format: str, *_args: object) -> None:
            return

    server = socketserver.ThreadingTCPServer(("127.0.0.1", 0), Handler)
    server.daemon_threads = True
    port = int(server.server_address[1])
    server_thread = threading.Thread(target=server.serve_forever, daemon=True)
    fixtures: list[_FixtureHome] = []
    succeeded = False
    server_thread.start()
    try:
        baseline = _new_fixture_home()
        fixtures.append(baseline)
        guarded = _new_fixture_home()
        fixtures.append(guarded)
        baseline_env = _minimal_environment(baseline)
        baseline_env["CODEX_REFRESH_TOKEN_URL_OVERRIDE"] = (
            f"http://127.0.0.1:{port}/oauth/token"
        )
        baseline_profile = _strict_sandbox_profile(baseline, binary, port)
        _verify_native_version(binary, baseline, baseline_env, baseline_profile)
        assert observation.snapshot() == (0, False)
        _run_fixed_native(
            binary,
            baseline,
            baseline_env,
            baseline_profile,
        )
        baseline_count, baseline_unexpected = observation.snapshot()
        assert 0 < baseline_count <= 8
        assert not baseline_unexpected

        before_bytes, before_identity = _auth_identity(guarded.auth_path)
        metadata = extract_safe_auth_metadata(_FixtureMetadataSource())
        guarded_env = prepare_guarded_environment(
            _minimal_environment(guarded), metadata
        )
        _run_fixed_native(
            binary,
            guarded,
            guarded_env,
            _strict_sandbox_profile(guarded, binary, port),
        )
        after_count, after_unexpected = observation.snapshot()
        after_bytes, after_identity = _auth_identity(guarded.auth_path)

        assert after_count == baseline_count
        assert not after_unexpected
        assert before_bytes == guarded.auth_bytes == after_bytes
        assert before_identity == after_identity
        succeeded = True
    finally:
        server.shutdown()
        server.server_close()
        server_thread.join(timeout=2)
        if succeeded:
            for fixture in fixtures:
                if fixture.root.parent != Path("/private/tmp"):
                    raise AssertionError("refusing_cleanup_outside_private_tmp")
                shutil.rmtree(fixture.root)
