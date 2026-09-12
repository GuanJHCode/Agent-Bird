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
import time
from typing import Any, Iterable, Sequence
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


MAX_PATH_MANIFEST_BYTES = 64 * 1024
MAX_PTY_BYTES = 256 * 1024
MAX_DIAGNOSTIC_BYTES = 4096
MAX_LOCAL_SECONDS = 10.0


class ProbeError(RuntimeError):
    pass


@dataclass(frozen=True)
class ProbeSpec:
    context: IsolationContext
    grants_dir: Path
    profile_id: str
    approved_public_socket: Path
    real_home: Path
    credential_manifest: Path | None = None
    ready_path: Path | None = None
    status_path: Path | None = None
    use_sandbox: bool = True
    sandbox_executable: Path = Path("/usr/bin/sandbox-exec")
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


def remove_default_socket_alias(context: IsolationContext, approved_public_socket: Path) -> None:
    alias = default_control_socket(context.spec.codex_home)
    if not os.path.lexists(alias):
        return
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
    credential_manifest: Path | None = None,
    ready_path: Path | None = None,
    status_path: Path | None = None,
    use_sandbox: bool = True,
    timeout: float = MAX_LOCAL_SECONDS,
) -> "NativeActivationProbe":
    """Construct a probe from the reviewed immutable isolation receipt."""

    context = load_isolation_context(Path(isolation_manifest), profile_id)
    return NativeActivationProbe(
        ProbeSpec(
            context=context,
            grants_dir=Path(grants_dir),
            profile_id=profile_id,
            approved_public_socket=context.spec.public_socket,
            real_home=Path(real_home),
            credential_manifest=credential_manifest,
            ready_path=ready_path,
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
        if not self.spec.grants_dir.is_absolute():
            raise ProbeError("grant directory must be absolute")
        context.spec.validate()
        _private_directory(context.spec.task_root)
        _private_directory(context.spec.home)
        _private_directory(context.spec.codex_home)
        _private_directory(context.spec.workspace)
        if self.spec.grants_dir.exists():
            _private_directory(self.spec.grants_dir)
            if self.spec.grants_dir.resolve() != context.spec.home.resolve() and context.spec.home.resolve() not in self.spec.grants_dir.resolve().parents:
                raise ProbeError("grant directory must be below isolated home")
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
        auth_paths = self._auth_paths()
        before = snapshot_auth_paths(auth_paths)
        alias = install_default_socket_alias(context, self.spec.approved_public_socket)
        profile_path = context.spec.task_root / f"probe-{uuid.uuid4().hex[:12]}.sbpl"
        profile_identity: tuple[int, int] | None = None
        grant_path: Path | None = None
        grant_identity: tuple[int, int] | None = None
        master: int | None = None
        slave: int | None = None
        read_fd: int | None = None
        write_fd: int | None = None
        process: subprocess.Popen[bytes] | None = None
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
        scope = self.transaction.managed() if self.transaction is not None else nullcontext(None)
        try:
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
            if self.spec.ready_path is not None:
                env["PROBE_READY_PATH"] = str(self.spec.ready_path)
            if self.spec.status_path is not None:
                env["PROBE_STATUS_PATH"] = str(self.spec.status_path)
            report["env_names"] = sorted(env)
            master, slave = pty.openpty()
            read_fd, write_fd = os.pipe()
            gate = [sys.executable, "-B", str(Path(__file__).resolve()), "--gate-child", str(read_fd), str(context.expected_executable), "--", *target_args]
            if self.spec.use_sandbox:
                if not self.spec.sandbox_executable.is_file():
                    raise ProbeError("sandbox-exec is unavailable")
                profile_data = render_sandbox_profile(context.spec).encode("utf-8")
                profile_identity = _write_exclusive(profile_path, profile_data)
                gate = [str(self.spec.sandbox_executable), "-f", str(profile_path), *gate]
            with scope:
                process = subprocess.Popen(
                    gate,
                    stdin=slave,
                    stdout=slave,
                    stderr=slave,
                    pass_fds=(read_fd,),
                    close_fds=True,
                    env=env,
                    cwd=context.spec.workspace,
                    start_new_session=True,
                )
                child_pid = process.pid
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
                if wait_for_ready and self.spec.ready_path is not None:
                    while not self.spec.ready_path.exists() and time.monotonic() < deadline:
                        time.sleep(0.01)
                    if not self.spec.ready_path.exists():
                        raise ProbeError("probe ready timeout")
                pty_bytes, pty_excerpt = _drain_pty(master, digest, pty_bytes, min(deadline, time.monotonic() + 0.2))
                if send_status:
                    os.write(master, b"/status\n")
                    status_sent = True
                    pty_bytes, pty_excerpt = _drain_pty(master, digest, pty_bytes, min(deadline, time.monotonic() + 0.5))
                if send_quit:
                    os.write(master, b"/quit\n")
                    quit_sent = True
                while process.poll() is None and time.monotonic() < deadline:
                    pty_bytes, pty_excerpt = _drain_pty(master, digest, pty_bytes, min(deadline, time.monotonic() + 0.05))
                if process.poll() is None:
                    raise ProbeError("probe child exceeded bounded timeout")
                report["child_exit"] = process.returncode
        except BaseException as exc:
            error = exc
        finally:
            for fd in (write_fd, read_fd):
                if fd is not None:
                    try:
                        os.close(fd)
                    except OSError as exc:
                        cleanup_failures.append(type(exc).__name__)
            if process is not None and process.poll() is None and child_pid is not None and child_birth is not None:
                try:
                    current_birth, _ = _process_identity(child_pid)
                    if current_birth == child_birth:
                        process.send_signal(signal.SIGTERM)
                        try:
                            process.wait(timeout=1)
                        except subprocess.TimeoutExpired:
                            process.kill()
                            process.wait(timeout=1)
                    else:
                        cleanup_failures.append("child_identity_changed")
                except (ProbeError, OSError, subprocess.TimeoutExpired) as exc:
                    cleanup_failures.append(type(exc).__name__)
            if process is not None and report["child_exit"] is None:
                report["child_exit"] = process.poll()
            if master is not None:
                try:
                    pty_bytes, pty_excerpt = _drain_pty(master, digest, pty_bytes, time.monotonic() + 0.1)
                finally:
                    try:
                        os.close(master)
                    except OSError as exc:
                        cleanup_failures.append(type(exc).__name__)
            if slave is not None:
                try:
                    os.close(slave)
                except OSError as exc:
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
                remove_default_socket_alias(context, self.spec.approved_public_socket)
            except (ProbeError, OSError) as exc:
                cleanup_failures.append(type(exc).__name__)
            try:
                after = snapshot_auth_paths(auth_paths)
                report["auth"] = before.compare(after)
            except (OSError, ValueError) as exc:
                report["auth"] = {"unchanged": False, "changed_path_ids": [], "comparison_error": type(exc).__name__}
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
