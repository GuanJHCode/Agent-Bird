"""Offline-safe controller for one user LaunchAgent activation transaction.

The controller owns the plist, a staging/lease record, and a socket only after
freezing its post-bootstrap identity.  A residual socket is removed only through
an identity-checked private quarantine; an ambiguous replacement remains owned
by neither cleanup path and leaves the transaction UNKNOWN.
The executable is injected in tests; callers must not pass the real launchctl
unless a separately approved user-level experiment is in progress.
"""

from __future__ import annotations

from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import plistlib
import re
import stat
import subprocess
import uuid
from contextlib import contextmanager
from typing import Any, Protocol, Sequence


class ActivationError(RuntimeError):
    pass


class ForeignObjectError(ActivationError):
    pass


class RegistrationError(ActivationError):
    pass


class UnknownStateError(ActivationError):
    pass


class AuthChangedError(ActivationError):
    pass


class AuthGuard(Protocol):
    def before(self) -> dict[str, Any]: ...

    def after(self, baseline: dict[str, Any]) -> dict[str, Any]: ...


@dataclass(frozen=True)
class ActivationSpec:
    home: Path
    plist_path: Path
    socket_path: Path
    label: str
    domain: str
    program_arguments: tuple[str, ...]
    startup_sha256: str
    txn_id: str
    launchctl: tuple[str, ...]
    manifest_path: Path | None = None
    manifest_sha256: str | None = None
    launchctl_env: tuple[tuple[str, str], ...] = ()
    artifact_hashes: tuple[tuple[str, str], ...] = ()
    lease_path: Path | None = None
    startup_path: Path | None = None

    @property
    def job_target(self) -> str:
        return f"{self.domain}/{self.label}"

    @property
    def socket_parent(self) -> Path:
        return self.socket_path.parent


@dataclass(frozen=True)
class FileIdentity:
    dev: int
    ino: int
    size: int
    sha256: str
    uid: int
    mode: int


@dataclass(frozen=True)
class DirectoryIdentity:
    dev: int
    ino: int
    uid: int
    mode: int


@dataclass(frozen=True)
class SocketIdentity:
    dev: int
    ino: int
    uid: int
    mode: int


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _identity(path: Path, *, max_size: int = 4 * 1024 * 1024) -> FileIdentity:
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        before = os.fstat(fd)
        if not stat.S_ISREG(before.st_mode) or before.st_size > max_size:
            raise UnknownStateError("identity_not_bounded_regular_file")
        digest = hashlib.sha256()
        remaining = before.st_size
        while remaining:
            chunk = os.read(fd, min(1024 * 1024, remaining))
            if not chunk:
                raise UnknownStateError("identity_short_read")
            digest.update(chunk)
            remaining -= len(chunk)
        after = os.fstat(fd)
        if (before.st_dev, before.st_ino, before.st_size) != (after.st_dev, after.st_ino, after.st_size):
            raise UnknownStateError("identity_changed_during_read")
        return FileIdentity(
            before.st_dev,
            before.st_ino,
            before.st_size,
            digest.hexdigest(),
            before.st_uid,
            stat.S_IMODE(before.st_mode),
        )
    finally:
        os.close(fd)


def _same_identity(path: Path, expected: FileIdentity) -> bool:
    try:
        actual = _identity(path)
    except (FileNotFoundError, OSError, ActivationError):
        return False
    return actual == expected


def _directory_identity(path: Path) -> DirectoryIdentity:
    ActivationTransaction._reject_symlink_components(path)
    st = path.lstat()
    if stat.S_ISLNK(st.st_mode) or not stat.S_ISDIR(st.st_mode):
        raise UnknownStateError("directory_identity_invalid")
    return DirectoryIdentity(st.st_dev, st.st_ino, st.st_uid, stat.S_IMODE(st.st_mode))


def _directory_identity_fd(fd: int) -> DirectoryIdentity:
    st = os.fstat(fd)
    if stat.S_ISLNK(st.st_mode) or not stat.S_ISDIR(st.st_mode):
        raise UnknownStateError("directory_fd_invalid")
    return DirectoryIdentity(st.st_dev, st.st_ino, st.st_uid, stat.S_IMODE(st.st_mode))


def _socket_identity(path: Path) -> SocketIdentity:
    ActivationTransaction._reject_symlink_components(path)
    st = path.lstat()
    if stat.S_ISLNK(st.st_mode) or not stat.S_ISSOCK(st.st_mode):
        raise UnknownStateError("public_socket_not_socket")
    if st.st_uid != os.getuid():
        raise UnknownStateError("public_socket_owner")
    return SocketIdentity(st.st_dev, st.st_ino, st.st_uid, stat.S_IMODE(st.st_mode))


def _fsync_directory(path: Path) -> None:
    fd = os.open(path, os.O_RDONLY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def _write_exclusive(path: Path, data: bytes, mode: int) -> FileIdentity | None:
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, mode)
    created = os.fstat(fd)
    try:
        view = memoryview(data)
        while view:
            written = os.write(fd, view)
            if written <= 0:
                raise OSError("short_write")
            view = view[written:]
        os.fsync(fd)
    except Exception:
        os.close(fd)
        try:
            current = path.lstat()
            if current.st_dev == created.st_dev and current.st_ino == created.st_ino:
                path.unlink()
                _fsync_directory(path.parent)
        finally:
            pass
        raise
    else:
        os.close(fd)
    _fsync_directory(path.parent)
    return _identity(path)


def _safe_job(raw: Any) -> dict[str, Any]:
    if not isinstance(raw, dict):
        raise UnknownStateError("launchctl_print_not_object")
    allowed = {"label", "path", "program_arguments", "pid", "uid", "birth"}
    result: dict[str, Any] = {}
    for key in allowed:
        if key in raw:
            value = raw[key]
            if key == "program_arguments":
                if not isinstance(value, list) or not all(isinstance(item, str) for item in value):
                    raise UnknownStateError("launchctl_program_arguments_shape")
                result[key] = list(value)
            elif isinstance(value, (str, int, float, bool)) or value is None:
                result[key] = value
            else:
                raise UnknownStateError(f"launchctl_{key}_shape")
    if not isinstance(result.get("label"), str) or not isinstance(result.get("path"), str):
        raise UnknownStateError("launchctl_identity_missing")
    if "program_arguments" not in result:
        raise UnknownStateError("launchctl_argv_missing")
    return result


class SubprocessLaunchctl:
    """Small adapter that persists only selected launchctl fields."""

    def __init__(self, executable: Sequence[str], timeout: float = 5.0, env: Sequence[tuple[str, str]] = ()) -> None:
        self.executable = tuple(executable)
        self.timeout = timeout
        self.env = tuple(env)

    def _run(self, *args: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [*self.executable, *args],
            capture_output=True,
            text=True,
            timeout=self.timeout,
            check=False,
            env={"PATH": "/usr/bin:/bin", **dict(self.env)},
        )

    def bootstrap(self, domain: str, plist_path: Path) -> int:
        return self._run("bootstrap", domain, str(plist_path)).returncode

    def bootout(self, domain: str, plist_path: Path) -> int:
        return self._run("bootout", domain, str(plist_path)).returncode

    def query(self, target: str) -> tuple[str, dict[str, Any] | None]:
        result = self._run("print", target)
        if result.returncode != 0:
            # launchctl's not-found status is intentionally the only absence
            # accepted here.  Other failures leave ownership unknown.
            detail = (result.stdout + result.stderr).lower()
            if result.returncode in (36, 44) or any(
                phrase in detail for phrase in ("could not find", "no such process", "service not found")
            ):
                return "absent", None
            return "error", None
        from_text = False
        try:
            raw = json.loads(result.stdout)
        except (TypeError, json.JSONDecodeError):
            raw = self._parse_text(result.stdout)
            from_text = True
        job = _safe_job(raw)
        expected_label = target.rsplit("/", 1)[-1]
        observed_label = job["label"]
        if from_text:
            if observed_label != target:
                raise UnknownStateError("launchctl_domain_mismatch")
            job["label"] = expected_label
        elif observed_label != expected_label:
            raise UnknownStateError("launchctl_label_mismatch")
        return "present", job

    @staticmethod
    def _parse_text(output: str) -> dict[str, Any]:
        """Parse only launchctl's identity/argv fields; discard all else."""
        pid = None
        lines = output.splitlines()
        label: str | None = None
        path: str | None = None
        argv: list[str] = []
        depth = 0
        args_open = False
        args_seen = False
        outer_closed = False
        for line in lines:
            stripped = line.strip()
            if not stripped:
                continue
            if depth == 0:
                if stripped.endswith(" => {"):
                    label = stripped[:-5].strip()
                elif stripped.endswith(" = {"):
                    label = stripped[:-4].strip()
                else:
                    raise UnknownStateError("launchctl_header_unparseable")
                depth = 1
                continue
            if args_open:
                if stripped == "}":
                    args_open = False
                    depth -= 1
                else:
                    argv.append(stripped)
                continue
            if depth == 1 and stripped.startswith("pid = "):
                value = stripped[6:].strip()
                if pid is not None or re.fullmatch(r"[1-9][0-9]*", value) is None:
                    raise UnknownStateError("launchctl_pid_invalid_or_duplicate")
                pid = int(value)
                continue
            if depth == 1 and stripped.startswith("path = "):
                path = stripped[7:].strip()
                continue
            if depth == 1 and stripped.startswith(("program arguments =", "arguments =")):
                args_seen = True
                args_open = True
                depth += 1
                inline = stripped.partition("=")[2].strip()
                if inline and inline != "{":
                    argv.append(inline)
                continue
            if stripped.endswith(" = {"):
                depth += 1
                continue
            if stripped == "}":
                depth -= 1
                if depth < 0:
                    raise UnknownStateError("launchctl_brace_underflow")
                if depth == 0:
                    outer_closed = True
                continue
            # Scalar fields, including program/state, are intentionally ignored.
        if args_open or depth != 0 or not outer_closed:
            raise UnknownStateError("launchctl_print_truncated")
        if label is None or path is None:
            raise UnknownStateError("launchctl_print_unparseable")
        if not args_seen:
            argv = []
        return {"label": label, "path": path, "program_arguments": argv, **({"pid": pid} if pid is not None else {})}


class ActivationTransaction:
    """One staged, no-replace, reversible registration."""

    _TXN_RE = re.compile(r"^[A-Za-z0-9._-]{1,96}$")
    _LABEL = "org.codex.orchestration.proxy"

    def __init__(self, spec: ActivationSpec, *, auth_guard: AuthGuard) -> None:
        self.spec = spec
        self.auth_guard = auth_guard
        self.stage_path: Path | None = None
        self.stage_identity: FileIdentity | None = None
        self.plist_identity: FileIdentity | None = None
        self.lease_identity: FileIdentity | None = None
        self.public_socket_identity: SocketIdentity | None = None
        self.public_socket_quarantine_path: Path | None = None
        self.parent_identities: dict[str, DirectoryIdentity] = {}
        self.auth_baseline: dict[str, Any] | None = None
        self.registered = False
        self.receipt: dict[str, Any] = {
            "schema_version": 1,
            "txn_id": spec.txn_id,
            "label": spec.label,
            "domain": spec.domain,
            "state": "NEW",
            "plist_path": str(spec.plist_path),
            "socket_path": str(spec.socket_path),
            "program_arguments": list(spec.program_arguments),
            "startup_sha256": spec.startup_sha256,
            "launchctl": {"calls": []},
        }
        self.launchd = SubprocessLaunchctl(spec.launchctl, env=spec.launchctl_env)

    @property
    def program_arguments(self) -> tuple[str, ...]:
        return self.spec.program_arguments

    def _validate_spec(self) -> None:
        if not self.spec.txn_id or not self._TXN_RE.fullmatch(self.spec.txn_id):
            raise ActivationError("invalid_txn_id")
        if self.spec.label != self._LABEL:
            raise ActivationError("label_not_fixed")
        for path, name in (
            (self.spec.home, "home"),
            (self.spec.plist_path, "plist_path"),
            (self.spec.socket_path, "socket_path"),
        ):
            if not path.is_absolute():
                raise ActivationError(f"{name}_not_absolute")
        if len(os.fsencode(str(self.spec.socket_path))) >= 104:
            raise ActivationError("socket_path_too_long")
        if self.spec.plist_path.parent != self.spec.plist_path.parent.parent / "LaunchAgents":
            # The exact user LaunchAgents boundary is part of the contract.
            raise ActivationError("plist_not_in_launchagents")
        expected_plist = self.spec.home / "Library" / "LaunchAgents" / f"{self.spec.label}.plist"
        expected_socket = self.spec.home / ".codex" / "app-server-control" / "app-server-control.sock"
        if self.spec.plist_path != expected_plist:
            raise ActivationError("plist_path_not_fixed")
        if self.spec.socket_path != expected_socket:
            raise ActivationError("socket_path_not_default")
        if self.spec.domain != f"gui/{os.getuid()}":
            raise ActivationError("domain_not_current_gui_user")
        if not self.spec.program_arguments or any(not isinstance(arg, str) for arg in self.spec.program_arguments):
            raise ActivationError("invalid_program_arguments")
        for arg in self.spec.program_arguments:
            if arg.startswith("--"):
                continue
            if arg.startswith("/") and len(os.fsencode(arg)) >= 1024:
                raise ActivationError("program_argument_too_long")
        if self.spec.manifest_path is not None and not self.spec.manifest_path.is_absolute():
            raise ActivationError("manifest_not_absolute")
        if self.spec.manifest_path is not None and not self.spec.manifest_sha256:
            raise ActivationError("manifest_hash_required")
        if any(key != "FAKE_LAUNCHD_STATE" for key, _ in self.spec.launchctl_env):
            raise ActivationError("launchctl_env_not_allowlisted")
        if self.spec.startup_path is None or not self.spec.startup_path.is_absolute():
            raise ActivationError("startup_path_required")
        if not self.spec.artifact_hashes:
            raise ActivationError("artifact_hashes_required")

    @staticmethod
    def _reject_symlink_components(path: Path) -> None:
        current = Path(path.anchor or "/")
        for component in path.parts[1:]:
            current /= component
            try:
                st = current.lstat()
            except FileNotFoundError:
                return
            if stat.S_ISLNK(st.st_mode):
                raise ActivationError(f"path_component_symlink:{current}")

    @staticmethod
    def _directory_is_safe(path: Path, *, owner_only: bool) -> None:
        ActivationTransaction._reject_symlink_components(path)
        try:
            st = path.lstat()
        except FileNotFoundError as exc:
            raise ActivationError(f"directory_missing:{path}") from exc
        if not stat.S_ISDIR(st.st_mode) or stat.S_ISLNK(st.st_mode):
            raise ActivationError(f"directory_invalid:{path}")
        if st.st_uid != os.getuid():
            raise ActivationError(f"directory_owner:{path}")
        forbidden = 0o077 if owner_only else 0o022
        if st.st_mode & forbidden:
            raise ActivationError(f"directory_mode:{path}")

    def _freeze_parents(self) -> None:
        paths = {
            "home": self.spec.home,
            "library": self.spec.home / "Library",
            "codex": self.spec.home / ".codex",
            "socket_parent": self.spec.socket_parent,
            "plist_parent": self.spec.plist_path.parent,
        }
        if self.spec.lease_path is not None:
            paths["lease_parent"] = self.spec.lease_path.parent
        self.parent_identities = {name: _directory_identity(path) for name, path in paths.items()}

    def _check_frozen_parents(self) -> None:
        paths = {
            "home": self.spec.home,
            "library": self.spec.home / "Library",
            "codex": self.spec.home / ".codex",
            "socket_parent": self.spec.socket_parent,
            "plist_parent": self.spec.plist_path.parent,
        }
        if self.spec.lease_path is not None:
            paths["lease_parent"] = self.spec.lease_path.parent
        for name, expected in self.parent_identities.items():
            try:
                current = _directory_identity(paths[name])
            except (FileNotFoundError, OSError, ActivationError) as exc:
                raise UnknownStateError(f"parent_changed:{name}") from exc
            if current != expected:
                raise UnknownStateError(f"parent_changed:{name}")

    @staticmethod
    def _must_absent(path: Path, name: str) -> None:
        try:
            path.lstat()
        except FileNotFoundError:
            return
        raise ForeignObjectError(f"{name}_exists")

    def _plist_bytes(self) -> bytes:
        payload: dict[str, Any] = {
            "Label": self.spec.label,
            "ProgramArguments": list(self.spec.program_arguments),
            "Sockets": {
                "Listener": {
                    "SockPathName": str(self.spec.socket_path),
                    "SockPathMode": 0o600,
                }
            },
        }
        return plistlib.dumps(payload, fmt=plistlib.FMT_XML, sort_keys=False)

    def _call_log(self, command: str, returncode: int) -> None:
        self.receipt["launchctl"]["calls"].append({"command": command, "returncode": returncode})

    @staticmethod
    def _safe_auth_summary(summary: dict[str, Any]) -> dict[str, Any]:
        if not isinstance(summary, dict):
            raise ValueError("auth_summary_not_object")
        allowed = {"opaque_id", "count", "comparison", "summary", "digest", "unchanged", "real_paths_read"}
        safe: dict[str, Any] = {}
        for key in allowed:
            value = summary.get(key)
            if isinstance(value, (str, int, float, bool)) or value is None:
                safe[key] = value
        return safe

    def _matches_own_job(self, job: dict[str, Any]) -> bool:
        return (
            job.get("label") == self.spec.label
            and job.get("path") == str(self.spec.plist_path)
            and job.get("program_arguments") == list(self.spec.program_arguments)
        )

    def _query_job(self) -> tuple[str, dict[str, Any] | None]:
        status, job = self.launchd.query(self.spec.job_target)
        if status == "error":
            raise UnknownStateError("launchctl_query_failed")
        if job is not None:
            self.receipt["job"] = job
        return status, job

    def _freeze_public_socket(self) -> None:
        self._check_frozen_parents()
        identity = _socket_identity(self.spec.socket_path)
        if identity.mode != 0o600:
            raise UnknownStateError("public_socket_mode")
        self.public_socket_identity = identity
        self.receipt["public_socket"] = {
            "path": str(self.spec.socket_path),
            "dev": identity.dev,
            "ino": identity.ino,
            "uid": identity.uid,
            "mode": format(identity.mode, "04o"),
        }

    def _remove_owned_public_socket(self) -> None:
        if self.public_socket_identity is None:
            raise UnknownStateError("public_socket_identity_missing")
        self._check_frozen_parents()
        parent = self.spec.socket_path.parent
        parent_identity = self.parent_identities["socket_parent"]
        fd = os.open(
            parent,
            os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0),
        )
        quarantine_fd: int | None = None
        quarantine_name: str | None = None
        try:
            if _directory_identity_fd(fd) != parent_identity or _directory_identity(parent) != parent_identity:
                raise UnknownStateError("public_socket_parent_changed")
            try:
                current = os.stat(self.spec.socket_path.name, dir_fd=fd, follow_symlinks=False)
            except FileNotFoundError:
                return
            if stat.S_ISLNK(current.st_mode) or not stat.S_ISSOCK(current.st_mode):
                raise UnknownStateError("public_socket_replaced")
            observed = SocketIdentity(current.st_dev, current.st_ino, current.st_uid, stat.S_IMODE(current.st_mode))
            if observed != self.public_socket_identity:
                raise UnknownStateError("public_socket_replaced")

            # POSIX has no unlink-if-(dev,ino) primitive. Move the current
            # directory entry atomically into a fresh private directory, then
            # inspect the moved inode before deleting anything. If a competitor
            # replaced the public name between stat and rename, the replacement
            # is quarantined and restored with link(2)'s no-replace semantics;
            # EEXIST leaves both objects intact and reports UNKNOWN.
            quarantine_name = f".g0-socket-quarantine-{uuid.uuid4().hex}"
            self.public_socket_quarantine_path = parent / quarantine_name / self.spec.socket_path.name
            os.mkdir(quarantine_name, 0o700, dir_fd=fd)
            os.fsync(fd)
            quarantine_fd = os.open(
                quarantine_name,
                os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0),
                dir_fd=fd,
            )
            quarantine_identity = _directory_identity_fd(quarantine_fd)
            if quarantine_identity.uid != os.getuid() or quarantine_identity.mode & 0o077:
                raise UnknownStateError("public_socket_quarantine_identity")
            try:
                os.rename(
                    self.spec.socket_path.name,
                    self.spec.socket_path.name,
                    src_dir_fd=fd,
                    dst_dir_fd=quarantine_fd,
                )
            except FileNotFoundError:
                os.rmdir(quarantine_name, dir_fd=fd)
                os.fsync(fd)
                self.public_socket_quarantine_path = None
                return
            try:
                moved = os.stat(self.spec.socket_path.name, dir_fd=quarantine_fd, follow_symlinks=False)
            except OSError as exc:
                self.public_socket_quarantine_path = parent / quarantine_name / self.spec.socket_path.name
                raise UnknownStateError("public_socket_quarantine_unreadable") from exc
            moved_identity = SocketIdentity(
                moved.st_dev,
                moved.st_ino,
                moved.st_uid,
                stat.S_IMODE(moved.st_mode),
            )
            if stat.S_ISSOCK(moved.st_mode) and moved_identity == self.public_socket_identity:
                os.unlink(self.spec.socket_path.name, dir_fd=quarantine_fd)
                os.fsync(quarantine_fd)
                os.rmdir(quarantine_name, dir_fd=fd)
                os.fsync(fd)
                try:
                    os.stat(self.spec.socket_path.name, dir_fd=fd, follow_symlinks=False)
                except FileNotFoundError:
                    self.public_socket_quarantine_path = None
                    return
                except OSError as exc:
                    raise UnknownStateError("public_socket_recheck_failed") from exc
                raise UnknownStateError("public_socket_reappeared")

            quarantine_path = parent / quarantine_name / self.spec.socket_path.name
            try:
                os.link(
                    self.spec.socket_path.name,
                    self.spec.socket_path.name,
                    src_dir_fd=quarantine_fd,
                    dst_dir_fd=fd,
                    follow_symlinks=False,
                )
            except OSError as exc:
                self.public_socket_quarantine_path = quarantine_path
                raise UnknownStateError("public_socket_replaced") from exc
            try:
                os.unlink(self.spec.socket_path.name, dir_fd=quarantine_fd)
                os.fsync(quarantine_fd)
                os.rmdir(quarantine_name, dir_fd=fd)
                os.fsync(fd)
                self.public_socket_quarantine_path = None
            except OSError as exc:
                self.public_socket_quarantine_path = quarantine_path
                raise UnknownStateError("public_socket_replacement_restore") from exc
            raise UnknownStateError("public_socket_replaced")
        finally:
            if quarantine_fd is not None:
                os.close(quarantine_fd)
            os.close(fd)

    @staticmethod
    def _validate_artifact(
        path: Path,
        expected_hash: str,
        *,
        max_size: int = 4 * 1024 * 1024,
    ) -> FileIdentity:
        identity = _identity(path, max_size=max_size)
        if identity.uid not in {0, os.getuid()}:
            raise ActivationError("artifact_owner")
        if identity.mode & 0o022:
            raise ActivationError("artifact_writable")
        if identity.sha256 != expected_hash:
            raise ActivationError("artifact_hash_mismatch")
        return identity

    def _revalidate_before_publish(self) -> None:
        self._check_frozen_parents()
        if self.stage_path is None or self.stage_identity is None or not _same_identity(self.stage_path, self.stage_identity):
            raise UnknownStateError("stage_changed_before_publish")
        self._must_absent(self.spec.plist_path, "plist")
        self._must_absent(self.spec.socket_path, "socket")
        status, _ = self._query_job()
        if status == "present":
            raise ForeignObjectError("job_exists")
        if self.spec.manifest_path is not None:
            manifest_identity = self._validate_artifact(self.spec.manifest_path, self.spec.manifest_sha256 or "")
            if self.receipt.get("manifest", {}).get("sha256") != manifest_identity.sha256:
                raise UnknownStateError("manifest_changed_before_publish")
        for raw_path, expected_hash in self.spec.artifact_hashes:
            identity = self._validate_artifact(Path(raw_path), expected_hash, max_size=512 * 1024 * 1024)
            recorded = next(
                (entry["sha256"] for entry in self.receipt.get("artifacts", []) if entry["path"] == raw_path),
                None,
            )
            if recorded != identity.sha256:
                raise UnknownStateError("artifact_changed_before_publish")
        checker = getattr(self.auth_guard, "check", None)
        if callable(checker):
            result = checker(self.auth_baseline)
            if result is False:
                raise AuthChangedError("auth_changed_before_publish")
        elif self.auth_baseline is not None:
            current = self.auth_guard.before()
            if self._safe_auth_summary(current) != self._safe_auth_summary(self.auth_baseline):
                raise AuthChangedError("auth_changed_before_publish")

    def prepare(self) -> dict[str, Any]:
        if self.receipt["state"] != "NEW":
            raise ActivationError("prepare_wrong_state")
        self._validate_spec()
        self._directory_is_safe(self.spec.home, owner_only=False)
        self._directory_is_safe(self.spec.home / "Library", owner_only=False)
        # Existing real homes may expose .codex as 0755; reject writes by
        # group/other without requiring an unrelated chmod of that ancestor.
        self._directory_is_safe(self.spec.home / ".codex", owner_only=False)
        self._directory_is_safe(self.spec.socket_parent, owner_only=True)
        self._directory_is_safe(self.spec.plist_path.parent, owner_only=False)
        self._freeze_parents()
        self._check_frozen_parents()
        self._must_absent(self.spec.socket_path, "socket")
        self._must_absent(self.spec.plist_path, "plist")
        job_status, _ = self._query_job()
        if job_status == "present":
            raise ForeignObjectError("job_exists")
        if self.spec.lease_path is not None:
            if not self.spec.lease_path.is_absolute():
                raise ActivationError("lease_not_absolute")
            self._directory_is_safe(self.spec.lease_path.parent, owner_only=True)
            self._must_absent(self.spec.lease_path, "lease")
        if self.spec.manifest_path is not None:
            try:
                manifest_identity = self._validate_artifact(self.spec.manifest_path, self.spec.manifest_sha256 or "")
            except FileNotFoundError as exc:
                raise ActivationError("manifest_missing") from exc
            if self.spec.manifest_sha256 and manifest_identity.sha256 != self.spec.manifest_sha256:
                raise ActivationError("manifest_hash_mismatch")
            self.receipt["manifest"] = {
                "path": str(self.spec.manifest_path),
                "sha256": manifest_identity.sha256,
            }
        artifact_map: dict[str, str] = {}
        for raw_path, expected_hash in self.spec.artifact_hashes:
            path = Path(raw_path)
            if not path.is_absolute():
                raise ActivationError("artifact_not_absolute")
            identity = self._validate_artifact(path, expected_hash, max_size=512 * 1024 * 1024)
            artifact_map[str(path)] = identity.sha256
            self.receipt.setdefault("artifacts", []).append({"path": str(path), "sha256": identity.sha256})
        if str(self.spec.startup_path) not in artifact_map:
            raise ActivationError("startup_artifact_required")
        if artifact_map[str(self.spec.startup_path)] != self.spec.startup_sha256:
            raise ActivationError("startup_hash_mismatch")
        if not self.spec.program_arguments or not self.spec.program_arguments[0].startswith("/"):
            raise ActivationError("interpreter_argv_required")
        if self.spec.program_arguments[0] not in artifact_map:
            raise ActivationError("interpreter_artifact_required")
        if str(self.spec.startup_path) not in self.spec.program_arguments:
            raise ActivationError("startup_argv_required")
        try:
            self.auth_baseline = self.auth_guard.before()
            auth_before = self._safe_auth_summary(self.auth_baseline)
        except Exception as exc:
            self.receipt["state"] = "UNKNOWN"
            raise AuthChangedError("auth_guard_before_invalid") from exc
        self.receipt["auth_before"] = auth_before
        stage = self.spec.plist_path.parent / f".{self.spec.label}.{self.spec.txn_id}.stage"
        self._must_absent(stage, "stage")
        self._check_frozen_parents()
        data = self._plist_bytes()
        self.stage_path = stage
        try:
            self.stage_identity = _write_exclusive(stage, data, 0o600)
        except Exception:
            self.stage_path = None
            raise
        self.receipt["plist_sha256"] = self.stage_identity.sha256
        self.receipt["stage_path"] = str(stage)
        self.receipt["state"] = "STAGED"
        if self.spec.lease_path is not None:
            lease_data = {
                "schema_version": 1,
                "txn_id": self.spec.txn_id,
                "label": self.spec.label,
                "domain": self.spec.domain,
                "plist_path": str(self.spec.plist_path),
                "plist_sha256": self.stage_identity.sha256,
                "socket_path": str(self.spec.socket_path),
                "public_socket_owner": "launchd",
                "program_arguments": list(self.spec.program_arguments),
                "manifest": self.receipt.get("manifest"),
                "artifacts": self.receipt.get("artifacts", []),
            }
            try:
                self.lease_identity = _write_exclusive(
                    self.spec.lease_path,
                    json.dumps(lease_data, sort_keys=True).encode("utf-8"),
                    0o600,
                )
            except Exception:
                self._remove_stage()
                self.receipt["state"] = "UNKNOWN"
                raise
            self.receipt["lease_path"] = str(self.spec.lease_path)
            self.receipt["lease_sha256"] = self.lease_identity.sha256
        return dict(self.receipt)

    def _unlink_if_own(self, path: Path, expected: FileIdentity | None, name: str) -> None:
        if expected is None:
            return
        if not _same_identity(path, expected):
            if path.exists() or path.is_symlink():
                raise ForeignObjectError(f"{name}_replaced")
            return
        path.unlink()
        _fsync_directory(path.parent)

    def _remove_stage(self) -> None:
        if self.stage_path is not None and self.stage_identity is not None:
            self._check_frozen_parents()
            self._unlink_if_own(self.stage_path, self.stage_identity, "stage")
            self.stage_path = None

    def _remove_plist(self) -> None:
        self._check_frozen_parents()
        self._unlink_if_own(self.spec.plist_path, self.plist_identity, "plist")
        self.plist_identity = None

    def _remove_lease(self) -> None:
        if self.spec.lease_path is not None:
            self._check_frozen_parents()
            self._unlink_if_own(self.spec.lease_path, self.lease_identity, "lease")
            self.lease_identity = None

    def _finish_auth(self) -> None:
        if self.auth_baseline is None or self.receipt.get("auth_after") is not None:
            return
        try:
            after = self.auth_guard.after(self.auth_baseline)
        except Exception as exc:
            self.receipt["state"] = "UNKNOWN"
            self.receipt["auth_after"] = {"comparison": "guard_error"}
            raise AuthChangedError("auth_guard_after_failed") from exc
        try:
            auth_after = self._safe_auth_summary(after)
        except Exception as exc:
            self.receipt["state"] = "UNKNOWN"
            self.receipt["auth_after"] = {"comparison": "guard_error"}
            raise AuthChangedError("auth_guard_after_invalid") from exc
        self.receipt["auth_after"] = auth_after
        if not after.get("unchanged", False):
            self.receipt["state"] = "UNKNOWN"
            raise AuthChangedError("auth_changed")

    def _rollback_job_if_own(self) -> None:
        status, job = self._query_job()
        if status == "absent":
            return
        if job is None or not self._matches_own_job(job):
            raise ForeignObjectError("foreign_job")
        rc = self.launchd.bootout(self.spec.domain, self.spec.plist_path)
        self._call_log("bootout", rc)
        if rc != 0:
            status_after, job_after = self._query_job()
            if status_after != "absent":
                raise UnknownStateError("bootout_unknown")
        status_after, _ = self._query_job()
        if status_after != "absent":
            raise UnknownStateError("job_still_present")

    def register(self) -> dict[str, Any]:
        if self.receipt["state"] != "STAGED" or self.stage_path is None or self.stage_identity is None:
            raise ActivationError("register_wrong_state")
        try:
            self._revalidate_before_publish()
        except ActivationError as exc:
            if isinstance(exc, UnknownStateError):
                self.receipt["state"] = "UNKNOWN"
                raise
            self._remove_stage()
            self._remove_lease()
            self.receipt["state"] = "ROLLED_BACK"
            raise
        try:
            # link(2) is atomic and fails with EEXIST; ordinary rename/install
            # is intentionally forbidden because it may replace a foreign file.
            os.link(self.stage_path, self.spec.plist_path)
            _fsync_directory(self.spec.plist_path.parent)
            self.plist_identity = _identity(self.spec.plist_path)
            if self.plist_identity != self.stage_identity:
                raise UnknownStateError("published_identity_drift")
            self._remove_stage()
            self.receipt["state"] = "PUBLISHED"
        except FileExistsError as exc:
            self._remove_stage()
            self._remove_lease()
            raise ForeignObjectError("plist_exists") from exc
        except Exception:
            if self.stage_path is not None:
                self._remove_stage()
            raise

        self.receipt["state"] = "BOOTSTRAPPING"
        try:
            rc = self.launchd.bootstrap(self.spec.domain, self.spec.plist_path)
        except Exception:
            self.receipt["state"] = "UNKNOWN"
            raise
        self._call_log("bootstrap", rc)
        if rc != 0:
            try:
                status, job = self._query_job()
            except Exception:
                self.receipt["state"] = "UNKNOWN"
                raise
            if status == "present" and job is not None and self._matches_own_job(job):
                self.registered = True
                try:
                    self._rollback_job_if_own()
                except Exception:
                    self.receipt["state"] = "UNKNOWN"
                    raise
            elif status == "present":
                # The job is foreign even though the pathname is ours;
                # remove only our orphaned plist, never bootout its job.
                try:
                    self._remove_plist()
                    self._remove_lease()
                except Exception:
                    self.receipt["state"] = "UNKNOWN"
                    raise
                self.receipt["state"] = "ROLLED_BACK"
                self._finish_auth()
                raise RegistrationError("foreign_job")
            elif status != "absent":
                self.receipt["state"] = "UNKNOWN"
                raise UnknownStateError("bootstrap_unknown")
            try:
                self._remove_plist()
                self._remove_lease()
            except Exception:
                self.receipt["state"] = "UNKNOWN"
                raise
            self.receipt["state"] = "ROLLED_BACK"
            self._finish_auth()
            raise RegistrationError("bootstrap_failed")

        try:
            status, job = self._query_job()
        except Exception:
            self.receipt["state"] = "UNKNOWN"
            raise
        if status != "present" or job is None:
            self._remove_plist()
            self._remove_lease()
            self.receipt["state"] = "ROLLED_BACK"
            self._finish_auth()
            raise RegistrationError("job_missing_after_bootstrap")
        if not self._matches_own_job(job):
            self._remove_plist()
            self._remove_lease()
            self.receipt["state"] = "ROLLED_BACK"
            self._finish_auth()
            raise RegistrationError("foreign_job")
        try:
            self._freeze_public_socket()
        except Exception:
            self.receipt["state"] = "UNKNOWN"
            try:
                self._rollback_job_if_own()
                self._remove_plist()
                self._remove_lease()
            except Exception:
                pass
            raise
        self.registered = True
        self.receipt["state"] = "REGISTERED"
        return dict(self.receipt)

    def verify(self) -> dict[str, Any]:
        if not self.registered or self.receipt["state"] not in {"REGISTERED", "VERIFIED"}:
            raise ActivationError("verify_wrong_state")
        status, job = self._query_job()
        if status != "present" or job is None or not self._matches_own_job(job):
            raise UnknownStateError("registered_job_drift")
        if self.plist_identity is None or not _same_identity(self.spec.plist_path, self.plist_identity):
            raise ForeignObjectError("plist_replaced")
        self.receipt["state"] = "VERIFIED"
        return dict(self.receipt)

    def revoke(self) -> dict[str, Any]:
        if self.receipt["state"] == "NEW":
            return dict(self.receipt)
        if self.receipt["state"] == "STAGED":
            self._remove_stage()
            self._remove_lease()
            if os.path.lexists(self.spec.socket_path):
                self.receipt["public_socket_residual"] = {
                    "path": str(self.spec.socket_path),
                    "cleanup": "manual_launchd_or_owner_audit_required",
                }
                self.receipt["state"] = "UNKNOWN"
                self._finish_auth()
                raise UnknownStateError("public_socket_residual")
            self.receipt["state"] = "REVOKED"
            self._finish_auth()
            return dict(self.receipt)
        if self.plist_identity is None:
            raise ActivationError("missing_owned_plist_identity")
        if not _same_identity(self.spec.plist_path, self.plist_identity):
            raise ForeignObjectError("plist_replaced")
        status, job = self._query_job()
        if status == "present" and (job is None or not self._matches_own_job(job)):
            raise ForeignObjectError("foreign_job")
        if status == "error":
            raise UnknownStateError("revoke_query_unknown")
        if status == "present":
            rc = self.launchd.bootout(self.spec.domain, self.spec.plist_path)
            self._call_log("bootout", rc)
            status_after, job_after = self._query_job()
            if rc != 0 and status_after != "absent":
                if job_after is not None and not self._matches_own_job(job_after):
                    raise ForeignObjectError("foreign_job_after_bootout")
                raise UnknownStateError("bootout_unknown")
            if status_after != "absent":
                raise UnknownStateError("job_still_present")
        public_socket_error: Exception | None = None
        if os.path.lexists(self.spec.socket_path):
            try:
                self._remove_owned_public_socket()
            except Exception as exc:
                public_socket_error = exc
        elif self.public_socket_identity is None:
            public_socket_error = UnknownStateError("public_socket_identity_missing")
        self._remove_plist()
        self._remove_lease()
        self.registered = False
        if public_socket_error is not None:
            self.receipt["public_socket_residual"] = {
                "path": str(self.spec.socket_path),
                "cleanup": "manual_launchd_or_owner_audit_required",
                "reason": type(public_socket_error).__name__,
            }
            if self.public_socket_quarantine_path is not None:
                self.receipt["public_socket_residual"]["quarantine_path"] = str(
                    self.public_socket_quarantine_path
                )
            self.receipt["state"] = "UNKNOWN"
            self._finish_auth()
            if isinstance(public_socket_error, UnknownStateError):
                raise public_socket_error
            raise UnknownStateError("public_socket_residual") from public_socket_error
        self.receipt["state"] = "REVOKED"
        self._finish_auth()
        return dict(self.receipt)

    @contextmanager
    def managed(self):
        """Prepare/register and always attempt symmetric rollback."""
        self.prepare()
        try:
            self.register()
            yield self
        except BaseException:
            if self.receipt["state"] in {"STAGED", "PUBLISHED", "BOOTSTRAPPING", "REGISTERED", "VERIFIED", "UNKNOWN"}:
                try:
                    self.revoke()
                except Exception:
                    self.receipt["state"] = "UNKNOWN"
            raise
        else:
            self.revoke()


__all__ = [
    "ActivationError",
    "ActivationSpec",
    "ActivationTransaction",
    "AuthChangedError",
    "ForeignObjectError",
    "RegistrationError",
    "UnknownStateError",
]
