"""Offline auth isolation guard for the approved launchd experiment.

The module is deliberately independent from Codex and never invokes a CLI,
login flow, Keychain API, launchctl, or a network client.  Callers provide a
new task-owned home/workspace and an explicit backend socket.  The generated
environment and sandbox profile are suitable inputs for a separately owned
frontend/backend launcher.
"""

from __future__ import annotations

from dataclasses import dataclass, replace
import hashlib
import json
import os
from pathlib import Path
import stat
import subprocess
from typing import Iterable, Mapping, Sequence


AUTH_ENV_NAMES = frozenset(
    {
        "CODEX_API_KEY",
        "CODEX_ACCESS_TOKEN",
        "OPENAI_API_KEY",
        "OPENAI_ACCESS_TOKEN",
        "ANTHROPIC_API_KEY",
        "GEMINI_API_KEY",
        "GOOGLE_API_KEY",
        "AWS_ACCESS_KEY_ID",
        "AWS_SECRET_ACCESS_KEY",
        "AWS_SESSION_TOKEN",
        "AWS_WEB_IDENTITY_TOKEN_FILE",
        "AWS_SHARED_CREDENTIALS_FILE",
        "AZURE_OPENAI_API_KEY",
    }
)

MACOS_UNIX_SOCKET_PATH_LIMIT = 104
MAX_CONTEXT_BYTES = 64 * 1024
MAX_AUTH_SNAPSHOT_BYTES = 8 * 1024 * 1024
PRIVATE_TEST_ROOT = Path("/private/tmp")

SAFE_INHERITED_ENV_NAMES = frozenset(
    {
        "PATH",
        "LANG",
        "LC_ALL",
        "LC_CTYPE",
        "TERM",
        "TERM_PROGRAM",
        "COLORTERM",
    }
)


@dataclass(frozen=True)
class IsolationSpec:
    task_root: Path
    home: Path
    codex_home: Path
    workspace: Path
    profile_id: str
    public_socket: Path
    backend_socket: Path
    protected_paths: tuple[Path, ...]
    protected_read_paths: tuple[Path, ...] = ()
    allowed_executables: tuple[Path, ...] = ()
    owned_loopback_port: int | None = None

    def validate(self) -> None:
        if self.owned_loopback_port is not None and (
            type(self.owned_loopback_port) is not int or not 1 <= self.owned_loopback_port <= 65535
        ):
            raise ValueError("owned loopback port must be an exact integer in 1..65535")
        if not self.profile_id or any(c.isspace() for c in self.profile_id):
            raise ValueError("profile_id must be opaque and whitespace-free")
        paths = (self.task_root, self.home, self.codex_home, self.workspace)
        if any(not path.is_absolute() for path in paths):
            raise ValueError("isolated paths must be absolute")
        if any(not path.is_absolute() for path in self.protected_paths):
            raise ValueError("protected paths must be absolute")
        if any(not path.is_absolute() for path in self.protected_read_paths):
            raise ValueError("protected read paths must be absolute")
        if any(not path.is_absolute() for path in self.allowed_executables):
            raise ValueError("allowed executables must be absolute")
        socket_paths = (
            self.public_socket,
            self.backend_socket,
            default_control_socket(self.codex_home),
        )
        for socket_path in socket_paths:
            if not socket_path.is_absolute():
                raise ValueError("socket paths must be absolute")
            if len(os.fsencode(socket_path)) >= MACOS_UNIX_SOCKET_PATH_LIMIT:
                raise ValueError("Unix socket path exceeds macOS sockaddr_un limit")
        task_root = self.task_root.resolve()
        if task_root.parent != PRIVATE_TEST_ROOT or not task_root.name.startswith("g0-auth-"):
            raise ValueError("task_root must be a new short /private/tmp/g0-auth-* root")
        for path in (self.task_root, self.home, self.codex_home, self.workspace, self.backend_socket):
            _reject_symlink_components(path)
        if self.backend_socket.parent == task_root or self.backend_socket.parent.parent != task_root:
            raise ValueError("backend socket must be below its own direct task child directory")
        for path in (self.home, self.codex_home, self.workspace):
            resolved = path.resolve()
            if resolved != task_root and task_root not in resolved.parents:
                raise ValueError("isolated path escapes task_root")
        isolated_dirs = [
            path.resolve()
            for path in (self.home, self.codex_home, self.workspace, self.backend_socket.parent)
        ]
        for index, left in enumerate(isolated_dirs):
            if any(
                left != right and (left in right.parents or right in left.parents)
                for right in isolated_dirs[index + 1 :]
            ):
                raise ValueError("isolated profile directories overlap")
        if any(path.resolve() == task_root or task_root in path.resolve().parents for path in self.protected_paths):
            raise ValueError("protected path overlaps isolated task_root")
        if any(path.resolve() == task_root or task_root in path.resolve().parents for path in self.protected_read_paths):
            raise ValueError("protected read path overlaps isolated task_root")


def default_control_socket(codex_home: Path) -> Path:
    """Return fixed 0.154's default control socket path for a Codex home."""

    return codex_home / "app-server-control" / "app-server-control.sock"


def _reject_symlink_components(path: Path) -> None:
    current = Path(path.anchor)
    for component in path.parts[1:]:
        current /= component
        if current.is_symlink():
            raise ValueError(f"symlink path component is not allowed: {current}")


def _owned_private_dir(path: Path) -> None:
    info = os.lstat(path)
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISDIR(info.st_mode):
        raise ValueError(f"isolated directory is not a real directory: {path}")
    if info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o700:
        raise ValueError(f"isolated directory owner/mode mismatch: {path}")


def _create_new_private_dir(path: Path) -> None:
    if os.path.lexists(path):
        raise FileExistsError(f"refusing to reuse existing isolated directory: {path}")
    os.mkdir(path, 0o700)


@dataclass(frozen=True)
class IsolationContext:
    spec: IsolationSpec
    expected_executable: Path
    expected_executable_sha256: str


def load_isolation_context(path: Path, profile_id: str) -> IsolationContext:
    """Load one opaque profile from a task-private JSON plan receipt."""

    document = json.loads(_read_stable_manifest(Path(path)))
    profiles = document.get("profiles")
    if document.get("version") != 1 or not isinstance(profiles, dict):
        raise ValueError("unsupported isolation context manifest")
    raw = profiles.get(profile_id)
    if not isinstance(raw, dict):
        raise ValueError("unknown isolation profile")
    required = ("task_root", "home", "codex_home", "workspace", "public_socket", "backend_socket")
    if any(not isinstance(raw.get(name), str) for name in required):
        raise ValueError("incomplete isolation profile")
    expected = raw.get("expected_executable")
    if not isinstance(expected, str) or not Path(expected).is_absolute():
        raise ValueError("profile executable must be absolute")
    expected_sha256 = raw.get("expected_executable_sha256")
    if not isinstance(expected_sha256, str) or len(expected_sha256) != 64 or any(
        character not in "0123456789abcdef" for character in expected_sha256.lower()
    ):
        raise ValueError("profile executable digest is required")
    protected = raw.get("protected_paths", [])
    if not isinstance(protected, list) or any(not isinstance(item, str) for item in protected):
        raise ValueError("invalid protected path list")
    protected_read = raw.get("protected_read_paths", [])
    if not isinstance(protected_read, list) or any(not isinstance(item, str) for item in protected_read):
        raise ValueError("invalid protected read path list")
    if not protected or not protected_read:
        raise ValueError("manifest must include write and read credential boundaries")
    profile_roots = []
    for candidate_id, candidate in profiles.items():
        if not isinstance(candidate_id, str) or not isinstance(candidate, dict):
            raise ValueError("invalid profile entry")
        candidate_paths = [
            candidate.get(name)
            for name in ("task_root", "home", "codex_home", "workspace", "backend_socket")
        ]
        if any(not isinstance(item, str) or not Path(item).is_absolute() for item in candidate_paths):
            raise ValueError("profile paths must be absolute")
        profile_roots.append((candidate_id, [Path(item).resolve() for item in candidate_paths]))
    for index, (left_id, left_paths) in enumerate(profile_roots):
        for right_id, right_paths in profile_roots[index + 1 :]:
            if any(
                left == right or left in right.parents or right in left.parents
                for left in left_paths
                for right in right_paths
            ):
                raise ValueError(f"profile roots overlap: {left_id} and {right_id}")
    spec = IsolationSpec(
        task_root=Path(raw["task_root"]),
        home=Path(raw["home"]),
        codex_home=Path(raw["codex_home"]),
        workspace=Path(raw["workspace"]),
        profile_id=profile_id,
        public_socket=Path(raw["public_socket"]),
        backend_socket=Path(raw["backend_socket"]),
        protected_paths=tuple(Path(item) for item in protected),
        protected_read_paths=tuple(Path(item) for item in protected_read),
        allowed_executables=(Path(expected),),
        owned_loopback_port=raw.get("owned_loopback_port"),
    )
    spec.validate()
    executable_info = os.lstat(expected)
    if stat.S_ISLNK(executable_info.st_mode) or not stat.S_ISREG(executable_info.st_mode):
        raise ValueError("expected executable must be a regular non-symlink file")
    if not executable_info.st_mode & 0o111:
        raise ValueError("expected executable is not executable")
    if _hash_stable_file(Path(expected)) != expected_sha256.lower():
        raise ValueError("expected executable digest mismatch")
    for directory in (spec.task_root, spec.home, spec.codex_home, spec.workspace):
        _owned_private_dir(directory)
    return IsolationContext(
        spec=spec,
        expected_executable=Path(expected),
        expected_executable_sha256=expected_sha256.lower(),
    )


def _read_stable_manifest(path: Path) -> bytes:
    _reject_symlink_components(path)
    info = os.lstat(path)
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode):
        raise ValueError("isolation manifest must be a regular non-symlink file")
    if info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o600:
        raise ValueError("isolation manifest must be owner-only")
    if info.st_size > MAX_CONTEXT_BYTES:
        raise ValueError("isolation manifest is too large")
    flags = (
        os.O_RDONLY
        | getattr(os, "O_NOFOLLOW", 0)
        | getattr(os, "O_CLOEXEC", 0)
        | getattr(os, "O_NONBLOCK", 0)
    )
    fd = os.open(path, flags)
    try:
        first = os.fstat(fd)
        if not stat.S_ISREG(first.st_mode) or _snapshot_stat_tuple(info) != _snapshot_stat_tuple(first):
            raise ValueError("isolation manifest changed before read")
        data = os.read(fd, MAX_CONTEXT_BYTES + 1)
        second = os.fstat(fd)
    finally:
        os.close(fd)
    stable_fields = ("st_dev", "st_ino", "st_uid", "st_mode", "st_size", "st_nlink", "st_mtime_ns")
    if any(getattr(first, field) != getattr(second, field) for field in stable_fields):
        raise ValueError("isolation manifest changed while reading")
    if len(data) > MAX_CONTEXT_BYTES:
        raise ValueError("isolation manifest is too large")
    return data


def _hash_stable_file(path: Path) -> str:
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_CLOEXEC", 0)
    fd = os.open(path, flags)
    digest = hashlib.sha256()
    try:
        first = os.fstat(fd)
        if not stat.S_ISREG(first.st_mode):
            raise ValueError("expected executable is not regular")
        total = 0
        while True:
            chunk = os.read(fd, min(1024 * 1024, 512 * 1024 * 1024 - total + 1))
            if not chunk:
                break
            total += len(chunk)
            if total > 512 * 1024 * 1024:
                raise ValueError("expected executable is too large")
            digest.update(chunk)
        second = os.fstat(fd)
    finally:
        os.close(fd)
    if _snapshot_stat_tuple(first) != _snapshot_stat_tuple(second):
        raise ValueError("expected executable changed while hashing")
    return digest.hexdigest()


def build_backend_launch(
    context: IsolationContext,
    argv: Sequence[str],
    private_socket: Path | None = None,
) -> dict[str, object]:
    """Build (without spawning) a clean backend launch plan for a supervisor."""

    if not argv or not isinstance(argv[0], str):
        raise ValueError("backend argv must be non-empty")
    if Path(argv[0]).absolute() != context.expected_executable:
        raise ValueError("backend argv executable does not match the profile lease")
    chosen_socket = context.spec.backend_socket if private_socket is None else Path(private_socket)
    chosen = replace(context.spec, backend_socket=chosen_socket)
    chosen.validate()
    if _hash_stable_file(context.expected_executable) != context.expected_executable_sha256:
        raise ValueError("expected executable changed after manifest admission")
    if chosen_socket.parent != context.spec.backend_socket.parent:
        raise ValueError("private backend socket must remain in the profile backend directory")
    if chosen_socket == chosen.public_socket or chosen_socket == default_control_socket(chosen.codex_home):
        raise ValueError("private backend socket aliases a public/default socket")
    if os.path.lexists(chosen_socket) and chosen_socket.is_symlink():
        raise ValueError("private backend socket cannot be a symlink")
    return {
        "profile_id": chosen.profile_id,
        "argv": list(argv),
        "env": build_clean_environment(chosen),
        "cwd": str(chosen.workspace),
        "private_socket": str(chosen_socket),
        "expected_executable": str(context.expected_executable),
        "expected_executable_sha256": context.expected_executable_sha256,
        "sandbox_profile": render_sandbox_profile(chosen),
    }


def _mkdir_private(path: Path) -> None:
    _create_new_private_dir(path)


def prepare_isolated_home(spec: IsolationSpec, *, no_auth_provider: bool = True) -> None:
    """Create only new task-owned directories and a minimal safe config."""

    spec.validate()
    if os.path.lexists(spec.task_root):
        _owned_private_dir(spec.task_root)
        if os.listdir(spec.task_root):
            raise ValueError("task_root must be empty before this transaction claims it")
    else:
        _mkdir_private(spec.task_root)
    marker = spec.task_root / (".auth-isolation-owner-" + hashlib.sha256(spec.profile_id.encode()).hexdigest()[:16])
    fd = os.open(marker, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    os.close(fd)
    _mkdir_private(spec.task_root / "tmp")
    _mkdir_private(spec.home)
    _mkdir_private(spec.codex_home)
    _mkdir_private(spec.workspace)
    backend_parent = spec.backend_socket.parent
    if backend_parent != spec.task_root:
        if backend_parent.parent != spec.task_root:
            raise ValueError("backend socket directory must be a direct task child")
        _mkdir_private(backend_parent)
    config = spec.codex_home / "config.toml"
    if config.exists():
        raise FileExistsError(f"refusing to overwrite existing isolated config: {config}")
    lines = [
        'cli_auth_credentials_store = "file"',
        'model = "gpt-5.6-luna"',
        'model_reasoning_effort = "medium"',
        "",
    ]
    if no_auth_provider:
        lines.extend(
            [
                'model_provider = "synthetic"',
                "",
                "[model_providers.synthetic]",
                'name = "synthetic-offline"',
                f'base_url = "http://127.0.0.1:{spec.owned_loopback_port or 1}"',
                'wire_api = "responses"',
                "requires_openai_auth = false",
                "",
            ]
        )
        if spec.owned_loopback_port is not None:
            lines.extend([
                'supports_websockets = false',
                'request_max_retries = 0',
                'stream_max_retries = 0',
                'stream_idle_timeout_ms = 10000',
            ])
    fd = os.open(config, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as stream:
        stream.write("\n".join(lines))


def write_synthetic_api_key_fixture(spec: IsolationSpec, marker: str) -> Path:
    """Write a new, obviously synthetic API-key fixture; never copy auth data."""

    spec.validate()
    if not marker.startswith("synthetic-") or any(c in marker for c in "\r\n\x00"):
        raise ValueError("API-key fixture must be a newline-free synthetic marker")
    auth_file = spec.codex_home / "auth.json"
    payload = {
        "auth_mode": "apikey",
        "OPENAI_API_KEY": marker,
        "tokens": None,
        "last_refresh": None,
        "agent_identity": None,
        "personal_access_token": None,
        "bedrock_api_key": None,
        "bedrock_access_keys": None,
    }
    fd = os.open(auth_file, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as stream:
        json.dump(payload, stream, separators=(",", ":"))
        stream.write("\n")
    return auth_file


def build_clean_environment(
    spec: IsolationSpec, source: Mapping[str, str] | None = None
) -> dict[str, str]:
    """Build an allowlist environment with explicit isolated paths.

    Values from auth-like names and all unlisted names are discarded.  The
    resulting environment carries an opaque profile lease marker for the
    frontend; it is not a credential and does not replace peer admission.
    """

    spec.validate()
    source = dict(os.environ if source is None else source)
    clean: dict[str, str] = {
        name: value for name, value in source.items() if name in SAFE_INHERITED_ENV_NAMES
    }
    clean["PATH"] = "/usr/bin:/bin:/usr/sbin:/sbin"
    clean["HOME"] = str(spec.home)
    clean["CODEX_HOME"] = str(spec.codex_home)
    clean["CODEX_SQLITE_HOME"] = str(spec.codex_home)
    clean["PWD"] = str(spec.workspace)
    clean["TMPDIR"] = str(spec.task_root / "tmp")
    clean["CODEX_ORCHESTRATION_PROFILE_ID"] = spec.profile_id
    for name in tuple(clean):
        if name in AUTH_ENV_NAMES or "TOKEN" in name or "SECRET" in name or "KEY" in name:
            clean.pop(name, None)
    return clean


def _path_id(path: Path) -> str:
    return hashlib.sha256(str(path.absolute()).encode("utf-8")).hexdigest()[:16]


class SnapshotReport(dict):
    def compare(self, other: Mapping[str, object]) -> dict[str, object]:
        before = {item["path_id"]: item for item in self["paths"]}
        after = {item["path_id"]: item for item in other["paths"]}
        changed = sorted(
            path_id
            for path_id in sorted(set(before) | set(after))
            if before.get(path_id) != after.get(path_id)
        )
        return {"unchanged": not changed, "changed_path_ids": changed}


def snapshot_auth_paths(paths: Iterable[Path]) -> SnapshotReport:
    """Hash bytes only; never parse or emit credential content."""

    records: list[dict[str, object]] = []
    for raw_path in paths:
        path = Path(raw_path)
        record: dict[str, object] = {"path_id": _path_id(path)}
        try:
            info = path.lstat()
        except FileNotFoundError:
            record.update(
                {
                    "exists": False,
                    "mode": None,
                    "kind": "missing",
                    "sha256": None,
                    "st_dev": None,
                    "st_ino": None,
                    "st_uid": None,
                }
            )
            records.append(record)
            continue
        record.update(
            {
                "exists": True,
                "mode": oct(stat.S_IMODE(info.st_mode)),
                "kind": "symlink" if stat.S_ISLNK(info.st_mode) else "file" if stat.S_ISREG(info.st_mode) else "other",
                "size": info.st_size,
                "st_dev": info.st_dev,
                "st_ino": info.st_ino,
                "st_uid": info.st_uid,
            }
        )
        if stat.S_ISREG(info.st_mode):
            flags = (
                os.O_RDONLY
                | getattr(os, "O_NOFOLLOW", 0)
                | getattr(os, "O_CLOEXEC", 0)
                | getattr(os, "O_NONBLOCK", 0)
            )
            fd = os.open(path, flags)
            digest = hashlib.sha256()
            try:
                opened = os.fstat(fd)
                if _snapshot_stat_tuple(info) != _snapshot_stat_tuple(opened):
                    raise ValueError("auth snapshot changed before read")
                total = 0
                while True:
                    chunk = os.read(fd, min(1024 * 1024, MAX_AUTH_SNAPSHOT_BYTES - total + 1))
                    if not chunk:
                        break
                    total += len(chunk)
                    if total > MAX_AUTH_SNAPSHOT_BYTES:
                        raise ValueError("auth snapshot exceeds bounded read size")
                    digest.update(chunk)
                closed = os.fstat(fd)
            finally:
                os.close(fd)
            if _snapshot_stat_tuple(opened) != _snapshot_stat_tuple(closed):
                raise ValueError("auth snapshot changed while reading")
            record["sha256"] = digest.hexdigest()
        elif stat.S_ISLNK(info.st_mode):
            target = os.readlink(path)
            after = path.lstat()
            if _snapshot_stat_tuple(info) != _snapshot_stat_tuple(after):
                raise ValueError("auth symlink changed while reading")
            record["target_sha256"] = hashlib.sha256(target.encode()).hexdigest()
        records.append(record)
    return SnapshotReport({"paths": records})


def _snapshot_stat_tuple(info: os.stat_result) -> tuple[int, int, int, int, int, int, int]:
    return (
        info.st_dev,
        info.st_ino,
        info.st_uid,
        info.st_mode,
        info.st_size,
        info.st_nlink,
        info.st_mtime_ns,
    )


def write_private_manifest(snapshot: Mapping[str, object], path: Path) -> None:
    """Persist only metadata in a new owner-only manifest; never overwrite."""

    path.parent.mkdir(parents=True, exist_ok=True)
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as stream:
        json.dump(snapshot, stream, sort_keys=True, separators=(",", ":"))
        stream.write("\n")


def _sbpl_path(path: Path) -> str:
    value = str(path)
    if not path.is_absolute() or any(c in value for c in "\r\n\x00"):
        raise ValueError("sandbox path must be absolute and line-safe")
    return value.replace("\\", "\\\\").replace('"', '\\"')


def render_sandbox_profile(spec: IsolationSpec, pty_slave: Path | None = None) -> str:
    """Render a deny-by-default SBPL profile for a task-owned process.

    The profile allows reads needed by a native executable, writes only below
    the profile-owned directories, denies known credential roots, denies
    securityd lookup, and denies IP networking.  Direct SBPL literal rules
    allow only the public/private Unix socket paths for egress and the private
    backend path for bind. A launcher must still enforce peer/profile
    admission; SBPL is a resource guard, not an identity protocol.
    """

    spec.validate()
    if pty_slave is not None and not Path(pty_slave).is_absolute():
        raise ValueError("PTY slave path must be absolute")
    write_paths = (spec.home, spec.codex_home, spec.workspace, spec.task_root / "tmp", spec.backend_socket.parent)
    lines = [
        "(version 1)",
        "(deny default)",
        "(allow process-fork)",
        "(allow signal (target self))",
        "(allow file-read*)",
    ]
    for isolated in write_paths:
        lines.append(f'(allow file-write* (subpath "{_sbpl_path(isolated)}"))')
    # SBPL's literal process-exec filter rejects interpreter/runtime re-exec
    # chains (for example /bin/sh -> /bin/bash and Python's framework binary).
    # The launcher therefore enforces the exact frontend/backend executable
    # digest and peer lease; the resource sandbox permits the runtime's exec
    # chain while keeping file/network/Mach restrictions in force.
    lines.append("(allow process-exec*)")
    for protected in spec.protected_read_paths:
        rendered = _sbpl_path(protected)
        lines.append(f'(deny file-read* (subpath "{rendered}"))')
    for protected in spec.protected_paths:
        rendered = _sbpl_path(protected)
        lines.append(f'(deny file-write* (subpath "{rendered}"))')
    lines.extend(
        [
            "(deny network-outbound)",
            '(allow file-write* (literal "/dev/null"))',
            '(allow sysctl-read (sysctl-name "hw.pagesize_compat"))',
            f'(allow network-outbound (literal "{_sbpl_path(spec.backend_socket)}"))',
            f'(allow network-outbound (literal "{_sbpl_path(spec.public_socket)}"))',
            f'(allow network-bind (literal "{_sbpl_path(spec.backend_socket)}"))',
            f'(allow network-inbound (literal "{_sbpl_path(spec.backend_socket)}"))',
            '(deny mach-lookup (global-name "com.apple.SecurityServer"))',
            '(deny mach-lookup (global-name "com.apple.securityd.systemkeychain"))',
            '(deny mach-lookup (global-name "com.apple.securityd"))',
            '(deny mach-lookup (global-name "com.apple.securityd.system"))',
        ]
    )
    if pty_slave is not None:
        rendered_pty = _sbpl_path(Path(pty_slave))
        lines.extend(
            [
                f'(allow file-read* file-write* file-ioctl (literal "{rendered_pty}"))',
            ]
        )
    if spec.owned_loopback_port is not None:
        # The fixture owns and continuously holds this exact listener. No
        # hostname, wildcard, alternate address, bind or inbound IP rule.
        lines.append(f'(allow network-outbound (remote tcp4 "localhost:{spec.owned_loopback_port}"))')
    return "\n".join(lines) + "\n"


def sandboxed_argv(spec: IsolationSpec, command: Sequence[str], profile_path: Path) -> list[str]:
    """Return argv for a caller-owned sandbox-exec launch; does not execute it."""

    spec.validate()
    if not command or any(not isinstance(item, str) or not item for item in command):
        raise ValueError("command must be a non-empty argv")
    return ["/usr/bin/sandbox-exec", "-f", str(profile_path), *command]


__all__ = [
    "IsolationSpec",
    "MACOS_UNIX_SOCKET_PATH_LIMIT",
    "SnapshotReport",
    "build_clean_environment",
    "build_backend_launch",
    "default_control_socket",
    "IsolationContext",
    "load_isolation_context",
    "prepare_isolated_home",
    "render_sandbox_profile",
    "sandboxed_argv",
    "snapshot_auth_paths",
    "write_private_manifest",
    "write_synthetic_api_key_fixture",
]
