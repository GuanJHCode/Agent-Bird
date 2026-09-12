"""Bounded native/fake activation probe using the auth isolation guard.

The probe owns only fresh profile roots, its grant, a PTY and a private report.
It never calls ``login``, ``launchctl`` or a Keychain API.  A caller may wrap
``run`` in the reviewed activation transaction; this module does not assume
that a public listener is safe without the peer grant written here.
"""

from __future__ import annotations

from contextlib import nullcontext
from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import pty
import re
import select
import signal
import stat
import subprocess
import sys
import termios
import fcntl
import struct
import time
from typing import Any, Iterable, Literal, Sequence
import uuid

from auth_isolation import (
    IsolationContext,
    build_backend_launch,
    build_clean_environment,
    default_control_socket,
    load_isolation_context,
    render_sandbox_profile,
    snapshot_auth_paths,
)

try:
    from proxy_transport import _process_metadata
except ImportError:  # pragma: no cover - the fixed repo path is used in tests.
    _process_metadata = None

try:
    from proxy_native_runtime import PTYDriver
except ImportError:  # pragma: no cover - fixed repo path is supplied by caller.
    PTYDriver = None


MAX_PATH_MANIFEST_BYTES = 64 * 1024
MAX_PTY_BYTES = 256 * 1024
MAX_DIAGNOSTIC_BYTES = 4096
MAX_LOCAL_SECONDS = 10.0


class ProbeError(RuntimeError):
    pass


@dataclass(frozen=True)
class ProbeSpec:
    context: IsolationContext
    supervisor_root: Path
    grants_dir: Path
    profile_id: str
    approved_public_socket: Path
    real_home: Path
    manifest_sha256: str | None = None
    mode: Literal["version", "activation"] = "activation"
    credential_manifest: Path | None = None
    ready_path: Path | None = None
    ready_receipt_path: Path | None = None
    ready_receipt_dir: Path | None = None
    final_receipt_path: Path | None = None
    receipt_dir: Path | None = None
    status_path: Path | None = None
    report_path: Path | None = None
    use_sandbox: bool = True
    allow_test_spawn: bool = False
    sandbox_executable: Path = Path("/usr/bin/sandbox-exec")
    sandbox_executable_sha256: str | None = None
    timeout: float = 10.0


def _path_id(path: Path) -> str:
    return hashlib.sha256(str(path.absolute()).encode("utf-8")).hexdigest()[:16]


def _private_directory(path: Path) -> None:
    info = path.lstat()
    if not stat.S_ISDIR(info.st_mode) or stat.S_ISLNK(info.st_mode):
        raise ProbeError(f"private directory required: {path}")
    if info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o700:
        raise ProbeError(f"private directory owner/mode mismatch: {path}")


def _reject_symlink_components(path: Path) -> None:
    current = Path(path.anchor or "/")
    for component in path.parts[1:]:
        current /= component
        try:
            info = current.lstat()
        except FileNotFoundError:
            return
        if stat.S_ISLNK(info.st_mode):
            raise ProbeError(f"symlink path component is not allowed: {current}")


def _read_owner_manifest(path: Path) -> bytes:
    _reject_symlink_components(path)
    info = path.lstat()
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode):
        raise ProbeError("credential path manifest must be a regular file")
    if info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o600:
        raise ProbeError("credential path manifest must be owner-only")
    if info.st_size > MAX_PATH_MANIFEST_BYTES:
        raise ProbeError("credential path manifest is too large")
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_NONBLOCK", 0)
    fd = os.open(path, flags)
    try:
        first = os.fstat(fd)
        if (first.st_dev, first.st_ino, first.st_uid, first.st_mode, first.st_size) != (
            info.st_dev,
            info.st_ino,
            info.st_uid,
            info.st_mode,
            info.st_size,
        ):
            raise ProbeError("credential path manifest changed before read")
        data = os.read(fd, MAX_PATH_MANIFEST_BYTES + 1)
        second = os.fstat(fd)
    finally:
        os.close(fd)
    stable = ("st_dev", "st_ino", "st_uid", "st_mode", "st_size", "st_nlink", "st_mtime_ns")
    if any(getattr(first, field) != getattr(second, field) for field in stable):
        raise ProbeError("credential path manifest changed while reading")
    if len(data) > MAX_PATH_MANIFEST_BYTES:
        raise ProbeError("credential path manifest is too large")
    return data


def load_credential_path_manifest(path: Path) -> tuple[Path, ...]:
    """Load only absolute path names, ignoring comments; never read targets."""

    lines = _read_owner_manifest(Path(path)).decode("utf-8")
    result: list[Path] = []
    seen: set[Path] = set()
    for line in lines.splitlines():
        item = line.strip()
        if not item or item.startswith("#"):
            continue
        candidate = Path(item)
        if not candidate.is_absolute() or "\x00" in item:
            raise ValueError("credential path inventory requires absolute paths")
        if candidate not in seen:
            result.append(candidate)
            seen.add(candidate)
    if not result:
        raise ValueError("credential path inventory is empty")
    return tuple(result)


def _write_exclusive(path: Path, data: bytes, mode: int = 0o600) -> tuple[int, int]:
    _reject_symlink_components(path)
    fd = os.open(
        path,
        os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0),
        mode,
    )
    created = os.fstat(fd)
    try:
        view = memoryview(data)
        while view:
            count = os.write(fd, view)
            if count <= 0:
                raise OSError("short write")
            view = view[count:]
        os.fsync(fd)
        info = os.fstat(fd)
    except BaseException:
        os.close(fd)
        try:
            current = path.lstat()
            if (current.st_dev, current.st_ino) == (created.st_dev, created.st_ino):
                path.unlink()
        except FileNotFoundError:
            pass
        raise
    else:
        os.close(fd)
    directory_fd = os.open(path.parent, os.O_RDONLY)
    try:
        os.fsync(directory_fd)
    finally:
        os.close(directory_fd)
    return info.st_dev, info.st_ino


def _unlink_owned(path: Path, identity: tuple[int, int]) -> bool:
    try:
        info = path.lstat()
    except FileNotFoundError:
        return True
    if (info.st_dev, info.st_ino) != identity:
        raise ProbeError(f"owned object changed: {path}")
    path.unlink()
    directory_fd = os.open(path.parent, os.O_RDONLY)
    try:
        os.fsync(directory_fd)
    finally:
        os.close(directory_fd)
    return True


def write_probe_report(report: dict[str, Any], path: Path) -> None:
    """Write an already-safe report once, with owner-only metadata."""

    data = json.dumps(report, sort_keys=True, separators=(",", ":")).encode("utf-8") + b"\n"
    if len(data) > MAX_PATH_MANIFEST_BYTES:
        raise ProbeError("probe report is too large")
    _write_exclusive(Path(path), data)


def install_default_socket_alias(context: IsolationContext, approved_public_socket: Path) -> Path:
    """Create the one new fake-home alias used by default Codex discovery."""

    target = Path(approved_public_socket)
    if not target.is_absolute() or target != context.spec.public_socket:
        raise ProbeError("approved public socket does not match profile")
    alias = default_control_socket(context.spec.codex_home)
    if len(os.fsencode(alias)) >= 104 or len(os.fsencode(target)) >= 104:
        raise ProbeError("Unix socket path exceeds macOS limit")
    _private_directory(context.spec.codex_home)
    parent = alias.parent
    if os.path.lexists(parent):
        _private_directory(parent)
    else:
        os.mkdir(parent, 0o700)
    if os.path.lexists(alias):
        raise ProbeError("default socket alias already exists")
    os.symlink(target, alias)
    return alias


def remove_default_socket_alias(
    context: IsolationContext,
    approved_public_socket: Path,
    expected_identity: tuple[int, int] | None = None,
) -> None:
    alias = default_control_socket(context.spec.codex_home)
    if not os.path.lexists(alias):
        return
    info = alias.lstat()
    if expected_identity is not None and (info.st_dev, info.st_ino) != expected_identity:
        raise ProbeError("default socket alias identity changed")
    if not alias.is_symlink() or Path(os.readlink(alias)) != Path(approved_public_socket):
        raise ProbeError("default socket alias was replaced")
    alias.unlink()
    try:
        alias.parent.rmdir()
    except OSError:
        pass


def build_probe_from_manifest(
    isolation_manifest: Path,
    profile_id: str,
    grants_dir: Path,
    real_home: Path,
    *,
    supervisor_root: Path | None = None,
    manifest_sha256: str | None = None,
    credential_manifest: Path | None = None,
    ready_path: Path | None = None,
    ready_receipt_path: Path | None = None,
    ready_receipt_dir: Path | None = None,
    final_receipt_path: Path | None = None,
    receipt_dir: Path | None = None,
    status_path: Path | None = None,
    use_sandbox: bool = True,
    timeout: float = MAX_LOCAL_SECONDS,
) -> "NativeActivationProbe":
    """Construct a probe from the reviewed immutable isolation receipt."""

    context = load_isolation_context(Path(isolation_manifest), profile_id)
    return NativeActivationProbe(
        ProbeSpec(
            context=context,
            supervisor_root=Path(supervisor_root or Path(grants_dir).parent),
            grants_dir=Path(grants_dir),
            profile_id=profile_id,
            approved_public_socket=context.spec.public_socket,
            real_home=Path(real_home),
            manifest_sha256=manifest_sha256,
            credential_manifest=credential_manifest,
            ready_path=ready_path,
            ready_receipt_path=ready_receipt_path,
            ready_receipt_dir=ready_receipt_dir,
            final_receipt_path=final_receipt_path,
            receipt_dir=receipt_dir,
            status_path=status_path,
            use_sandbox=use_sandbox,
            timeout=timeout,
        )
    )


def _safe_excerpt(data: bytes) -> str:
    text = data[:MAX_DIAGNOSTIC_BYTES].decode("utf-8", "replace")
    text = re.sub(r"(?i)(api[_-]?key|access[_-]?token|token|secret|authorization)\s*[:=]\s*\S+", r"\1=<redacted>", text)
    text = re.sub(r"[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]", "", text)
    return text[:MAX_DIAGNOSTIC_BYTES]


def _drain_pty(master: int, digest: Any, current: int, deadline: float) -> tuple[int, bytes]:
    excerpt = bytearray()
    while time.monotonic() < deadline and current < MAX_PTY_BYTES:
        readable, _, _ = select.select([master], [], [], min(0.05, max(0.0, deadline - time.monotonic())))
        if not readable:
            continue
        try:
            chunk = os.read(master, min(4096, MAX_PTY_BYTES - current))
        except OSError:
            break
        if not chunk:
            break
        digest.update(chunk)
        current += len(chunk)
        if len(excerpt) < MAX_DIAGNOSTIC_BYTES:
            excerpt.extend(chunk[: MAX_DIAGNOSTIC_BYTES - len(excerpt)])
    return current, bytes(excerpt)


def _process_identity(pid: int) -> tuple[str, str | None]:
    if _process_metadata is None:
        raise ProbeError("proxy_transport._process_metadata unavailable")
    birth, executable = _process_metadata(pid)
    if not birth:
        raise ProbeError("process birth unavailable")
    return birth, executable


def _open_controlling_pty() -> tuple[int, int, Path]:
    master, slave = pty.openpty()
    slave_path = Path(os.ttyname(slave))
    os.set_blocking(master, False)
    fcntl.ioctl(master, termios.TIOCSWINSZ, struct.pack("HHHH", 60, 200, 0, 0))
    return master, slave, slave_path


def _spawn_pty_gate(
    argv: Sequence[str], env: dict[str, str], cwd: Path, master: int, slave: int
) -> tuple[int, int]:
    """Spawn through a real controlling PTY with bounded terminal geometry."""

    if PTYDriver is None:
        raise ProbeError("proxy_native_runtime.PTYDriver unavailable")
    pid = os.fork()
    if pid == 0:
        try:
            os.setsid()
            fcntl.ioctl(slave, termios.TIOCSCTTY, 0)
            for fd in (0, 1, 2):
                os.dup2(slave, fd)
            os.close(master)
            if slave > 2:
                os.close(slave)
            os.chdir(cwd)
            os.environ.clear()
            os.environ.update(env)
            os.environ["TERM"] = "xterm-256color"
            fcntl.ioctl(0, termios.TIOCSWINSZ, struct.pack("HHHH", 60, 200, 0, 0))
            os.execve(argv[0], list(argv), env)
        finally:
            os._exit(127)
    os.close(slave)
    return pid, master


def _read_private_json(path: Path) -> dict[str, Any]:
    _reject_symlink_components(path)
    info = path.lstat()
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode):
        raise ProbeError("receipt must be a regular file")
    if info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o600 or info.st_size > MAX_PATH_MANIFEST_BYTES:
        raise ProbeError("receipt owner/mode/size invalid")
    fd = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_NONBLOCK", 0))
    try:
        first = os.fstat(fd)
        data = os.read(fd, MAX_PATH_MANIFEST_BYTES + 1)
        second = os.fstat(fd)
    finally:
        os.close(fd)
    fields = ("st_dev", "st_ino", "st_uid", "st_mode", "st_size", "st_nlink", "st_mtime_ns")
    if any(getattr(first, field) != getattr(second, field) for field in fields) or len(data) > MAX_PATH_MANIFEST_BYTES:
        raise ProbeError("receipt changed while reading")
    raw = json.loads(data)
    if not isinstance(raw, dict):
        raise ProbeError("receipt must be an object")
    return raw


def _ready_receipt_matches(
    raw: dict[str, Any], context: IsolationContext, pid: int, birth: str, manifest_sha256: str | None
) -> bool:
    frontend = raw.get("frontend")
    initialize = raw.get("initialize")
    thread = raw.get("thread")
    counts = raw.get("turn_counts")
    if not isinstance(frontend, dict) or not isinstance(initialize, dict) or not isinstance(thread, dict) or not isinstance(counts, dict):
        return False
    if raw.get("version") != 1 or raw.get("ready_published") is not True or raw.get("milestone_only") is not True or raw.get("protocol_valid") is not True:
        return False
    if raw.get("profile_id") != context.spec.profile_id or raw.get("zero_turns") is not True or raw.get("closed") is not False or raw.get("initialized") is not True:
        return False
    if not all(isinstance(raw.get(name), (str, int)) for name in ("activation_id", "lease_id", "connection_id", "epoch")):
        return False
    backend = raw.get("backend")
    if (
        not isinstance(backend, dict)
        or not backend.get("pid")
        or not backend.get("birth")
        or backend.get("uid") is None
        or not backend.get("executable", backend.get("exe"))
    ):
        return False
    if manifest_sha256 is not None and raw.get("manifest_sha256") != manifest_sha256:
        return False
    if frontend.get("pid") != pid or frontend.get("birth") != birth or frontend.get("uid") != os.getuid():
        return False
    frontend_executable = frontend.get("executable", frontend.get("exe", ""))
    if os.path.realpath(str(frontend_executable)) != os.path.realpath(str(context.expected_executable)):
        return False
    if initialize.get("home_match") is not True:
        return False
    expected_home_sha = hashlib.sha256(str(context.spec.codex_home).encode("utf-8")).hexdigest()
    if initialize.get("home_sha256") != expected_home_sha:
        return False
    if initialize.get("manifest_sha256") is not None and initialize.get("manifest_sha256") != raw.get("manifest_sha256"):
        return False
    thread_id = thread.get("id")
    if not isinstance(thread_id, str) or not thread_id or thread.get("started_id") != thread_id:
        return False
    if counts.get("start") != 0 or counts.get("steer") != 0:
        return False
    return True


def _final_receipt_complete(raw: dict[str, Any], established: dict[str, Any] | None = None) -> bool:
    if established is not None:
        for name in ("activation_id", "manifest_sha256"):
            if raw.get(name) != established.get(name):
                return False
        records = raw.get("backend_records")
        if not isinstance(records, list):
            return False
        transport = raw.get("transport") if isinstance(raw.get("transport"), dict) else {}
        protocol_rows = transport.get("protocol")
        if not isinstance(protocol_rows, list):
            return False
        matching_protocol = [
            row for row in protocol_rows
            if isinstance(row, dict)
            and row.get("connection_id") == established.get("connection_id")
            and row.get("epoch") == established.get("epoch")
        ]
        if not matching_protocol:
            return False
        matching_records = [
            row for row in records
            if isinstance(row, dict) and row.get("lease_id") == established.get("lease_id")
        ]
        if not matching_records or not any(
            _backend_identity_matches(row, matching_protocol, established.get("backend", {}))
            for row in matching_records
        ):
            return False
        if not any(
            isinstance(row, dict)
            and row.get("connection_id") == established.get("connection_id")
            and row.get("epoch") == established.get("epoch")
            for row in protocol_rows
        ):
            return False
    transport = raw.get("transport") if isinstance(raw.get("transport"), dict) else {}
    protocols = transport.get("protocol")
    if isinstance(protocols, list):
        if not protocols or any(
            not isinstance(row, dict)
            or row.get("protocol_valid") is not True
            or row.get("zero_turns") is not True
            or any(row.get("turn_counts", {}).get(name) != 0 for name in ("start", "steer"))
            for row in protocols
        ):
            return False
    else:
        protocol = protocols if isinstance(protocols, dict) else raw
        if protocol.get("protocol_valid") is not True or protocol.get("zero_turns") is not True:
            return False
        counts = protocol.get("turn_counts")
        if isinstance(counts, dict) and any(counts.get(name) != 0 for name in ("start", "steer")):
            return False
    if raw.get("closed") is not True and "closed" in raw:
        return False
    records = raw.get("backend_records")
    if isinstance(records, list):
        return bool(records) and all(
            isinstance(row, dict) and row.get("process_stopped") is True and row.get("socket_removed") is True
            for row in records
        )
    return raw.get("process_stopped") is True and raw.get("socket_removed") is True


def _backend_identity_matches(
    record: dict[str, Any], protocol_rows: list[dict[str, Any]], expected: dict[str, Any]
) -> bool:
    """Compare final owned backend identity with the ready lease identity."""

    expected_pid = expected.get("pid")
    expected_birth = expected.get("birth")
    expected_executable = expected.get("executable", expected.get("exe"))
    record_pid = record.get("pid")
    record_birth = record.get("birth", record.get("creation_birth"))
    record_executable = record.get("executable", record.get("creation_executable"))
    if record_pid is None or record_birth is None or record_executable is None:
        peers = [row.get("backend") for row in protocol_rows if isinstance(row.get("backend"), dict)]
        if not peers:
            return False
        peer = peers[0]
        record_pid = peer.get("pid")
        record_birth = peer.get("birth")
        record_executable = peer.get("executable", peer.get("exe"))
        record_uid = peer.get("uid")
    else:
        record_uid = record.get("uid")
    if record_uid is None:
        peers = [row.get("backend") for row in protocol_rows if isinstance(row.get("backend"), dict)]
        if peers:
            record_uid = peers[0].get("uid")
    if (record_pid, record_birth, os.path.realpath(str(record_executable))) != (
        expected_pid,
        expected_birth,
        os.path.realpath(str(expected_executable)),
    ):
        return False
    expected_uid = expected.get("uid")
    return expected_uid is not None and record_uid == expected_uid


def _receipt_candidates(path: Path | None, directory: Path | None, pattern: str) -> list[Path]:
    if path is not None:
        return [path]
    if directory is None:
        return []
    try:
        direct = list(directory.glob(pattern))
        nested = [] if "/" in pattern else list(directory.glob(f"*/{pattern}"))
        return sorted(direct + nested)
    except OSError:
        return []


class NativeActivationProbe:
    def __init__(self, spec: ProbeSpec, *, transaction: Any | None = None) -> None:
        self.spec = spec
        self.transaction = transaction
        self._validate_spec()

    def _validate_spec(self) -> None:
        context = self.spec.context
        if context.spec.profile_id != self.spec.profile_id:
            raise ProbeError("profile id does not match isolation context")
        if context.spec.public_socket != self.spec.approved_public_socket:
            raise ProbeError("approved public socket does not match isolation context")
        if context.spec.home.resolve() == self.spec.real_home.resolve():
            raise ProbeError("isolated home may not be the real home")
        if (
            context.spec.home.resolve() in self.spec.real_home.resolve().parents
            or self.spec.real_home.resolve() in context.spec.home.resolve().parents
        ):
            raise ProbeError("isolated home overlaps real home")
        if self.spec.timeout <= 0 or self.spec.timeout > MAX_LOCAL_SECONDS:
            raise ProbeError("probe timeout exceeds local bound")
        if self.spec.mode not in {"version", "activation"}:
            raise ProbeError("probe mode must be version or activation")
        if self.spec.credential_manifest is None:
            raise ProbeError("probe requires the fixed credential path inventory")
        load_credential_path_manifest(self.spec.credential_manifest)
        if not self.spec.use_sandbox:
            raise ProbeError("native probe requires sandbox-exec")
        if not self.spec.sandbox_executable.is_file():
            raise ProbeError("sandbox-exec is unavailable")
        if not self.spec.allow_test_spawn and self.spec.sandbox_executable != Path("/usr/bin/sandbox-exec"):
            raise ProbeError("production probe requires canonical sandbox-exec")
        if not self.spec.allow_test_spawn and (
            not isinstance(self.spec.sandbox_executable_sha256, str)
            or len(self.spec.sandbox_executable_sha256) != 64
        ):
            raise ProbeError("production probe requires sandbox-exec digest")
        for supervisor_path in (self.spec.supervisor_root, self.spec.grants_dir):
            if not supervisor_path.is_absolute():
                raise ProbeError("supervisor paths must be absolute")
        _private_directory(self.spec.supervisor_root)
        _private_directory(self.spec.grants_dir)
        if self.spec.grants_dir.parent != self.spec.supervisor_root:
            raise ProbeError("grant directory must be a direct supervisor child")
        context.spec.validate()
        _private_directory(context.spec.task_root)
        _private_directory(context.spec.home)
        _private_directory(context.spec.codex_home)
        _private_directory(context.spec.workspace)
        profile_roots = (
            context.spec.task_root.resolve(),
            context.spec.home.resolve(),
            context.spec.codex_home.resolve(),
            context.spec.workspace.resolve(),
            context.spec.backend_socket.parent.resolve(),
        )
        supervisor_root = self.spec.supervisor_root.resolve()
        if any(supervisor_root == root or root in supervisor_root.parents or supervisor_root in root.parents for root in profile_roots):
            raise ProbeError("supervisor root overlaps profile writable roots")
        if self.spec.mode == "activation" and self.spec.ready_receipt_path is None and self.spec.ready_receipt_dir is None:
            raise ProbeError("activation mode requires a ready receipt path or directory")
        if self.spec.mode == "activation" and self.spec.final_receipt_path is None and self.spec.receipt_dir is None:
            raise ProbeError("activation mode requires a final cleanup receipt path or directory")
        if self.spec.mode == "activation" and (
            not isinstance(self.spec.manifest_sha256, str) or len(self.spec.manifest_sha256) != 64
        ):
            raise ProbeError("activation mode requires the frozen manifest digest")
        if self.spec.mode == "version" and any(
            path is not None for path in (
                self.spec.ready_receipt_path,
                self.spec.ready_receipt_dir,
                self.spec.final_receipt_path,
                self.spec.receipt_dir,
            )
        ):
            raise ProbeError("version mode cannot use activation receipts")
        for child_path in (self.spec.ready_path, self.spec.status_path):
            if child_path is None:
                continue
            if not child_path.is_absolute():
                raise ProbeError("probe evidence paths must be absolute")
            _reject_symlink_components(child_path)
            resolved = child_path.resolve()
            roots = (context.spec.home.resolve(), context.spec.workspace.resolve(), (context.spec.task_root / "tmp").resolve())
            if not any(resolved == root or root in resolved.parents for root in roots):
                raise ProbeError("probe evidence path must be below isolated roots")
        for receipt_path in (self.spec.ready_receipt_path, self.spec.ready_receipt_dir, self.spec.final_receipt_path, self.spec.receipt_dir):
            if receipt_path is None:
                continue
            if not receipt_path.is_absolute():
                raise ProbeError("activation receipt path must be absolute")
            _reject_symlink_components(receipt_path)
            resolved = receipt_path.resolve()
            if resolved != supervisor_root and supervisor_root not in resolved.parents:
                raise ProbeError("activation receipt must be below supervisor root")
        if self.spec.report_path is not None:
            if not self.spec.report_path.is_absolute():
                raise ProbeError("probe report path must be absolute")
            _reject_symlink_components(self.spec.report_path)
            report_parent = self.spec.report_path.parent.resolve()
            if report_parent != context.spec.task_root.resolve() and context.spec.task_root.resolve() not in report_parent.parents:
                raise ProbeError("probe report path must be below task root")

    def _auth_paths(self) -> tuple[Path, ...]:
        if self.spec.credential_manifest is None:
            return ()
        return load_credential_path_manifest(self.spec.credential_manifest)

    def _backend_plan(self) -> dict[str, object]:
        context = self.spec.context
        argv = (
            str(context.expected_executable),
            "app-server",
            "--listen",
            f"unix://{context.spec.backend_socket}",
        )
        return build_backend_launch(context, argv, context.spec.backend_socket)

    def run(
        self,
        *,
        target_args: Sequence[str] = (),
        wait_for_ready: bool = True,
        send_status: bool = False,
        send_quit: bool = True,
    ) -> dict[str, Any]:
        """Run one bounded gate-to-exec probe and return safe metadata only."""

        context = self.spec.context
        if self.spec.mode == "version":
            if tuple(target_args) != ("--version",) or send_status or send_quit:
                raise ProbeError("version mode requires exactly --version and no PTY commands")
        else:
            if tuple(target_args) or not send_status or not send_quit:
                raise ProbeError("activation mode requires empty argv and bounded status/quit")
        auth_paths = self._auth_paths()
        before = snapshot_auth_paths(auth_paths)
        alias = install_default_socket_alias(context, self.spec.approved_public_socket)
        alias_info = alias.lstat()
        alias_identity = (alias_info.st_dev, alias_info.st_ino)
        profile_path = context.spec.task_root / f"probe-{uuid.uuid4().hex[:12]}.sbpl"
        profile_identity: tuple[int, int] | None = None
        grant_path: Path | None = None
        grant_identity: tuple[int, int] | None = None
        master: int | None = None
        slave: int | None = None
        pty_slave_path: Path | None = None
        read_fd: int | None = None
        write_fd: int | None = None
        process: Any | None = None
        driver: Any | None = None
        child_pid: int | None = None
        child_birth: str | None = None
        digest = hashlib.sha256()
        pty_bytes = 0
        pty_excerpt = b""
        status_sent = False
        quit_sent = False
        error: BaseException | None = None
        cleanup_failures: list[str] = []
        report: dict[str, Any] = {
            "schema_version": 1,
            "profile_id": self.spec.profile_id,
            "expected_executable": str(context.expected_executable),
            "expected_executable_sha256": context.expected_executable_sha256,
            "public_socket": str(self.spec.approved_public_socket),
            "backend_socket": str(context.spec.backend_socket),
            "default_socket_alias": str(alias),
            "env_names": [],
            "grant_registered_before_release": False,
            "status_sent": False,
            "quit_sent": False,
            "child_exit": None,
            "auth": {"unchanged": False, "changed_path_ids": []},
        }
        report_path = self.spec.report_path or context.spec.task_root / f"probe-report-{uuid.uuid4().hex[:12]}.json"
        scope = self.transaction.managed() if self.transaction is not None else nullcontext(None)
        scope_entered = False
        try:
            sandbox_digest = hashlib.sha256(self.spec.sandbox_executable.read_bytes()).hexdigest()
            if self.spec.sandbox_executable_sha256 is not None and sandbox_digest != self.spec.sandbox_executable_sha256:
                raise ProbeError("sandbox-exec digest mismatch")
            report["sandbox_executable"] = str(self.spec.sandbox_executable)
            report["sandbox_executable_sha256"] = sandbox_digest
            scope.__enter__()
            scope_entered = True
            backend_plan = self._backend_plan()
            report["backend_plan"] = {
                "private_socket": backend_plan["private_socket"],
                "expected_executable_sha256": backend_plan["expected_executable_sha256"],
            }
            if self.spec.ready_path is not None and os.path.lexists(self.spec.ready_path):
                raise ProbeError("ready path already exists")
            if self.spec.status_path is not None and os.path.lexists(self.spec.status_path):
                raise ProbeError("status path already exists")
            env = build_clean_environment(context.spec)
            env["TERM"] = "xterm-256color"
            if self.spec.ready_path is not None:
                env["PROBE_READY_PATH"] = str(self.spec.ready_path)
            if self.spec.status_path is not None:
                env["PROBE_STATUS_PATH"] = str(self.spec.status_path)
            if self.spec.ready_receipt_path is not None:
                env["PROBE_READY_RECEIPT_PATH"] = str(self.spec.ready_receipt_path)
            if self.spec.final_receipt_path is not None:
                env["PROBE_FINAL_RECEIPT_PATH"] = str(self.spec.final_receipt_path)
            if self.spec.manifest_sha256 is not None:
                env["PROBE_MANIFEST_SHA256"] = self.spec.manifest_sha256
            env["PROBE_EXPECTED_HOME_SHA256"] = hashlib.sha256(str(context.spec.codex_home).encode("utf-8")).hexdigest()
            env["PROBE_EXPECTED_EXECUTABLE"] = str(context.expected_executable)
            env["PROBE_PROFILE_ID"] = self.spec.profile_id
            report["env_names"] = sorted(env)
            read_fd, write_fd = os.pipe()
            os.set_inheritable(read_fd, True)
            master, slave, pty_slave_path = _open_controlling_pty()
            gate = [sys.executable, "-B", str(Path(__file__).resolve()), "--gate-child", str(read_fd), str(context.expected_executable), "--", *target_args]
            if self.spec.use_sandbox:
                if not self.spec.sandbox_executable.is_file():
                    raise ProbeError("sandbox-exec is unavailable")
                profile_data = render_sandbox_profile(context.spec, pty_slave_path).encode("utf-8")
                profile_identity = _write_exclusive(profile_path, profile_data)
                gate = [str(self.spec.sandbox_executable), "-f", str(profile_path), *gate]
            child_pid, master = _spawn_pty_gate(gate, env, context.spec.workspace, master, slave)
            slave = None
            driver = PTYDriver(child_pid, master)
            child_birth, gate_executable = _process_identity(child_pid)
            grant_path = self.spec.grants_dir / f"{child_pid}.json"
            grant = {
                "version": 1,
                "profile_id": self.spec.profile_id,
                "pid": child_pid,
                "uid": os.getuid(),
                "birth": child_birth,
                "expected_executable": str(context.expected_executable),
                "executable_sha256": context.expected_executable_sha256,
                "home": str(context.spec.home),
                "codex_home": str(context.spec.codex_home),
                "workspace": str(context.spec.workspace),
                "public_socket": str(context.spec.public_socket),
                "backend_socket": str(context.spec.backend_socket),
                "phase": "pre_exec_gate",
            }
            grant_identity = _write_exclusive(grant_path, json.dumps(grant, sort_keys=True).encode("utf-8"))
            report["grant_registered_before_release"] = True
            report["gate_executable"] = gate_executable
            os.write(write_fd, b"1")
            os.close(write_fd)
            write_fd = None
            deadline = time.monotonic() + self.spec.timeout
            if self.spec.mode == "activation":
                deadline = time.monotonic() + self.spec.timeout
                while time.monotonic() < deadline:
                    driver.read(min(0.05, max(0.01, deadline - time.monotonic())))
                    try:
                        for ready_path in _receipt_candidates(self.spec.ready_receipt_path, self.spec.ready_receipt_dir, "ready-*.json"):
                            ready = _read_private_json(ready_path)
                            if _ready_receipt_matches(ready, context, child_pid, child_birth, self.spec.manifest_sha256):
                                report["ready"] = {
                                    "path": str(ready_path),
                                    "connection_id": ready.get("connection_id"),
                                    "epoch": ready.get("epoch"),
                                    "thread_id": ready.get("thread", {}).get("id"),
                                    "home_match": True,
                                }
                                report["established"] = {
                                    "activation_id": ready.get("activation_id"),
                                    "lease_id": ready.get("lease_id"),
                                    "connection_id": ready.get("connection_id"),
                                    "epoch": ready.get("epoch"),
                                    "manifest_sha256": ready.get("manifest_sha256"),
                                    "frontend": ready.get("frontend"),
                                    "backend": ready.get("backend"),
                                }
                                break
                        if "ready" in report:
                            break
                    except (FileNotFoundError, ProbeError, json.JSONDecodeError, OSError):
                        pass
                    time.sleep(0.01)
                else:
                    raise ProbeError("activation readiness receipt timeout")
            elif wait_for_ready and self.spec.ready_path is not None:
                while not self.spec.ready_path.exists() and time.monotonic() < deadline:
                    time.sleep(0.01)
                if not self.spec.ready_path.exists():
                    raise ProbeError("probe ready timeout")
            if send_status:
                driver.command("/status")
                status_sent = True
                status_ids = sorted(driver.status_checkpoint(min(0.5, max(0.01, deadline - time.monotonic()))))
                report["status_session_ids"] = status_ids
                if self.spec.mode == "activation":
                    expected_thread = report.get("ready", {}).get("thread_id")
                    if status_ids != [expected_thread]:
                        raise ProbeError("PTY status session does not match ready thread")
            if send_quit:
                driver.command("/quit")
                quit_sent = True
            if driver.wait(max(0.01, deadline - time.monotonic())) is None:
                raise ProbeError("probe child exceeded bounded timeout")
            report["child_exit"] = driver.exit_code
            if self.spec.mode == "activation" and driver.exit_code != 0:
                raise ProbeError("activation child exit was not zero")
        except BaseException as exc:
            error = exc
        finally:
            for fd in (write_fd, read_fd):
                if fd is not None:
                    try:
                        os.close(fd)
                    except OSError as exc:
                        cleanup_failures.append(type(exc).__name__)
            if slave is not None:
                try:
                    os.close(slave)
                except OSError as exc:
                    cleanup_failures.append(type(exc).__name__)
            if driver is not None and driver.poll() is None and child_pid is not None and child_birth is not None:
                try:
                    current_birth, _ = _process_identity(child_pid)
                    if current_birth == child_birth:
                        os.kill(child_pid, signal.SIGTERM)
                        if driver.wait(1) is None:
                            os.kill(child_pid, signal.SIGKILL)
                            driver.wait(1)
                    else:
                        cleanup_failures.append("child_identity_changed")
                except (ProbeError, OSError, subprocess.TimeoutExpired) as exc:
                    cleanup_failures.append(type(exc).__name__)
            if driver is not None:
                if report["child_exit"] is None:
                    report["child_exit"] = driver.poll()
                evidence = driver.evidence
                pty_bytes = int(evidence.get("bytes", 0))
                digest = driver.digest
                pty_excerpt = getattr(driver, "screen_tail", b"")[:MAX_DIAGNOSTIC_BYTES]
                try:
                    driver.close()
                except (OSError, RuntimeError) as exc:
                    cleanup_failures.append(type(exc).__name__)
            if grant_path is not None and grant_identity is not None:
                try:
                    _unlink_owned(grant_path, grant_identity)
                except (ProbeError, OSError) as exc:
                    cleanup_failures.append(type(exc).__name__)
            if profile_identity is not None:
                try:
                    _unlink_owned(profile_path, profile_identity)
                except (ProbeError, OSError) as exc:
                    cleanup_failures.append(type(exc).__name__)
            try:
                remove_default_socket_alias(context, self.spec.approved_public_socket, alias_identity)
            except (ProbeError, OSError) as exc:
                cleanup_failures.append(type(exc).__name__)
            if self.spec.mode == "activation" and (
                self.spec.final_receipt_path is not None or self.spec.receipt_dir is not None
            ):
                final_deadline = time.monotonic() + self.spec.timeout
                final_ok = False
                while time.monotonic() < final_deadline:
                    try:
                        for final_path in _receipt_candidates(
                            self.spec.final_receipt_path,
                            self.spec.receipt_dir,
                            "*/activation.json",
                        ):
                            if _final_receipt_complete(
                                _read_private_json(final_path), report.get("established")
                            ):
                                report["final_receipt_path"] = str(final_path)
                                final_ok = True
                                break
                        if final_ok:
                            break
                    except (FileNotFoundError, ProbeError, json.JSONDecodeError, OSError):
                        pass
                    time.sleep(0.01)
                if not final_ok:
                    cleanup_failures.append("final_receipt_timeout")
                    if error is None:
                        error = ProbeError("activation cleanup receipt timeout")
            if scope_entered:
                try:
                    scope.__exit__(
                        type(error) if error is not None else None,
                        error,
                        error.__traceback__ if error is not None else None,
                    )
                except BaseException as exc:
                    cleanup_failures.append(type(exc).__name__)
                    if error is None:
                        error = exc
                scope_entered = False
            try:
                after = snapshot_auth_paths(auth_paths)
                report["auth"] = before.compare(after)
            except (OSError, ValueError) as exc:
                report["auth"] = {"unchanged": False, "changed_path_ids": [], "comparison_error": type(exc).__name__}
            if report["auth"].get("unchanged") is not True and error is None:
                error = ProbeError("auth snapshot changed")
            if cleanup_failures and error is None:
                error = ProbeError("cleanup failed")
            report["status_sent"] = status_sent
            report["quit_sent"] = quit_sent
            report["default_socket_exists_after"] = os.path.lexists(alias)
            report["cleanup_failures"] = cleanup_failures
            if error is not None:
                report["error_type"] = type(error).__name__
            report["pty"] = {
                "bytes_captured": pty_bytes,
                "sha256": digest.hexdigest(),
                "excerpt": _safe_excerpt(pty_excerpt),
            }
            report["status"] = "passed" if error is None else "failed"
            report["report_path"] = str(report_path)
            try:
                write_probe_report(report, report_path)
            except (OSError, ProbeError) as exc:
                report["cleanup_failures"].append(type(exc).__name__)
                report["status"] = "failed"
                if error is None:
                    error = ProbeError("probe report write failed")
        if error is not None:
            raise ProbeError(str(error)) from error
        return report


def _gate_child(read_fd: int, executable: str, target_args: Sequence[str]) -> int:
    try:
        if os.read(read_fd, 1) != b"1":
            return 91
    finally:
        os.close(read_fd)
    os.execv(executable, [executable, *target_args])
    return 92


def main(argv: Sequence[str] | None = None) -> int:
    import argparse

    parser = argparse.ArgumentParser()
    parser.add_argument("--gate-child", nargs=2, metavar=("READ_FD", "EXECUTABLE"))
    parser.add_argument("target_args", nargs=argparse.REMAINDER)
    args = parser.parse_args(argv)
    if args.gate_child:
        target_args = list(args.target_args)
        if target_args and target_args[0] == "--":
            target_args.pop(0)
        return _gate_child(int(args.gate_child[0]), args.gate_child[1], target_args)
    parser.error("probe requires an embedding caller")
    return 2


__all__ = [
    "NativeActivationProbe",
    "ProbeError",
    "ProbeSpec",
    "build_probe_from_manifest",
    "install_default_socket_alias",
    "load_credential_path_manifest",
    "remove_default_socket_alias",
    "write_probe_report",
]


if __name__ == "__main__":
    raise SystemExit(main())
