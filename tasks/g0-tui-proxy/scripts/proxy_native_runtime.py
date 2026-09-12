"""Guarded runtime/controller for the controlled native TUI proxy experiment.

The runtime owns only the processes and sockets it creates.  It intentionally
keeps the native launch path small: the TUI receives the ordinary ``codex``
argv, while the private backend is the only process started with
``app-server --listen``.  Attachment is inferred from allowlisted observer
events; a driver/status label can only be retained as an independent UI
reference.

This module is preparation code.  Importing it does not start a native process
or load a model.  ``NativeRuntime.start`` is the explicit native boundary used
by a later, reviewed experiment.
"""

from __future__ import annotations

import asyncio
import argparse
import errno
import hashlib
import json
import os
import platform
import pty
import re
import select
import signal
import shutil
import subprocess
import struct
import termios
import time
import uuid
from dataclasses import asdict, dataclass, field
from enum import Enum
from pathlib import Path
from typing import Any, Callable, Iterable, Mapping, Sequence


FIXED_NATIVE_SHA256 = "4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc"
DEFAULT_CLI = Path("/Users/guanjunhui/.local/bin/codex")
LOCAL_WINDOW_SECONDS = 10.0
MODEL_WINDOW_SECONDS = 120.0
NATIVE_OBSERVER_MAX_FRAME_BYTES = 16 * 1024 * 1024
NATIVE_OBSERVER_MAX_MESSAGE_BYTES = 64 * 1024 * 1024


def raw_seconds() -> float:
    clock = getattr(time, "CLOCK_MONOTONIC_RAW", None)
    if clock is None:
        return time.monotonic()
    return time.clock_gettime_ns(clock) / 1_000_000_000


class RuntimeInvariantError(RuntimeError):
    """The controlled experiment cannot safely continue."""


def build_ordinary_tui_argv(cli: str | os.PathLike[str], *extra: str) -> tuple[str, ...]:
    """Return the ordinary TUI argv and reject all alternate launch modes.

    In particular, this prevents accidentally turning the native experiment
    into a ``--remote``, model-selection, config-override, or prompt launch.
    """

    if extra:
        raise ValueError("ordinary TUI entry does not accept remote/model/config/prompt arguments")
    value = os.fspath(cli)
    if not value or value.startswith("-"):
        raise ValueError("CLI executable must be a non-empty ordinary entry")
    return (value,)


def canonical_resume_thread_id(thread_id: str) -> str:
    """Validate and return the one canonical UUID spelling accepted by resume."""

    if not isinstance(thread_id, str):
        raise ValueError("resume thread ID must be a canonical UUID")
    try:
        parsed = uuid.UUID(thread_id)
    except (ValueError, AttributeError):
        raise ValueError("resume thread ID must be a canonical UUID") from None
    canonical = str(parsed)
    if canonical != thread_id:
        raise ValueError("resume thread ID must be a canonical UUID")
    return canonical


def build_resume_tui_argv(
    cli: str | os.PathLike[str], thread_id: str, *extra: str
) -> tuple[str, ...]:
    """Return the controlled native resume entry, with no prompt or overrides."""

    if extra:
        raise ValueError("resume entry does not accept prompt/model/config arguments")
    ordinary = build_ordinary_tui_argv(cli)
    return ordinary + ("resume", canonical_resume_thread_id(thread_id))


def build_native_observer(expected_workspace_cwd: str | os.PathLike[str] | None = None) -> Any:
    """Construct the bounded observer limits used by the native experiment."""

    from proxy_observer import Observer

    options: dict[str, Any] = {
        "max_frame_bytes": NATIVE_OBSERVER_MAX_FRAME_BYTES,
        "max_message_bytes": NATIVE_OBSERVER_MAX_MESSAGE_BYTES,
    }
    if expected_workspace_cwd is not None:
        options["expected_workspace_cwd"] = os.fspath(expected_workspace_cwd)
    return Observer(
        **options,
    )


def sha256_file(path: str | os.PathLike[str]) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def _process_birth(pid: int) -> str | None:
    try:
        result = subprocess.run(
            ("/bin/ps", "-p", str(pid), "-o", "lstart="),
            check=False,
            capture_output=True,
            text=True,
            timeout=1,
        )
    except (OSError, subprocess.SubprocessError):
        return None
    value = result.stdout.strip()
    return value if result.returncode == 0 and value else None


def _process_path(pid: int) -> str | None:
    if platform.system() == "Darwin":
        try:
            import ctypes

            libproc = ctypes.CDLL("/usr/lib/libproc.dylib")
            buffer = ctypes.create_string_buffer(4096)
            length = libproc.proc_pidpath(pid, buffer, len(buffer))
            return os.fsdecode(buffer.value) if length > 0 else None
        except (AttributeError, OSError, ValueError):
            return None
    try:
        target = os.path.realpath(f"/proc/{pid}/exe")
    except OSError:
        return None
    return target if os.path.exists(target) else None


def _capture_process(process: Any, fallback: Path) -> OwnedProcess:
    pid = int(process.pid)
    birth = getattr(process, "birth", None) or _process_birth(pid)
    executable = getattr(process, "executable", None) or _process_path(pid) or fallback
    return OwnedProcess(pid, birth, os.path.realpath(os.fspath(executable)))


def _process_holds_socket(pid: int, path: Path) -> bool:
    lsof = shutil.which("lsof")
    if lsof is None:
        return False
    try:
        result = subprocess.run(
            (lsof, "-a", "-U", "-Fn", "-p", str(pid)),
            check=False,
            capture_output=True,
            text=True,
            timeout=2,
        )
    except (OSError, subprocess.SubprocessError):
        return False
    wanted = os.fspath(path)
    names = {line[1:] for line in result.stdout.splitlines() if line.startswith("n")}
    return wanted in names


@dataclass(frozen=True)
class OwnedProcess:
    pid: int
    birth: str | None
    executable: str | None


@dataclass(frozen=True)
class OwnedEndpoint:
    path: Path
    st_dev: int
    st_ino: int

    @classmethod
    def capture(cls, path: str | os.PathLike[str]) -> "OwnedEndpoint":
        target = Path(path)
        metadata = target.stat()
        return cls(target, metadata.st_dev, metadata.st_ino)

    def still_owned(self) -> bool:
        try:
            metadata = self.path.stat()
        except FileNotFoundError:
            return False
        return (metadata.st_dev, metadata.st_ino) == (self.st_dev, self.st_ino)


@dataclass(frozen=True)
class HashSnapshot:
    binary_sha256: str
    config_sha256: str | None


@dataclass(frozen=True)
class NativeRuntimeConfig:
    frontend_socket: Path
    backend_socket: Path
    cli: Path = DEFAULT_CLI
    cwd: Path = field(default_factory=Path.cwd)
    config_path: Path | None = None
    resume_thread_id: str | None = None
    expected_resume_thread_id: str | None = None
    local_window_seconds: float = LOCAL_WINDOW_SECONDS
    model_window_seconds: float = MODEL_WINDOW_SECONDS

    def __post_init__(self) -> None:
        object.__setattr__(self, "frontend_socket", Path(self.frontend_socket))
        object.__setattr__(self, "backend_socket", Path(self.backend_socket))
        object.__setattr__(self, "cli", Path(self.cli))
        object.__setattr__(self, "cwd", Path(self.cwd))
        if self.config_path is not None:
            object.__setattr__(self, "config_path", Path(self.config_path))
        requested_resume_id = self.resume_thread_id
        expected_resume_id = self.expected_resume_thread_id
        if requested_resume_id is not None and expected_resume_id is not None:
            if canonical_resume_thread_id(requested_resume_id) != canonical_resume_thread_id(expected_resume_id):
                raise ValueError("resume thread ID and expected resume thread ID must match")
        chosen_resume_id = requested_resume_id or expected_resume_id
        if chosen_resume_id is not None:
            chosen_resume_id = canonical_resume_thread_id(chosen_resume_id)
            object.__setattr__(self, "resume_thread_id", chosen_resume_id)
            object.__setattr__(self, "expected_resume_thread_id", chosen_resume_id)
        if self.frontend_socket == self.backend_socket:
            raise ValueError("frontend and backend sockets must be distinct")
        if not self.backend_socket.is_absolute() or not self.cwd.is_absolute():
            raise ValueError("backend_socket and cwd must be absolute paths")
        if self.local_window_seconds <= 0 or self.model_window_seconds <= 0:
            raise ValueError("runtime windows must be positive")

    @property
    def binary_sha256(self) -> str:
        return sha256_file(self.cli)

    @property
    def tui_argv(self) -> tuple[str, ...]:
        if self.resume_thread_id is not None:
            return build_resume_tui_argv(self.cli, self.resume_thread_id)
        return build_ordinary_tui_argv(self.cli)

    @property
    def backend_argv(self) -> tuple[str, ...]:
        return (os.fspath(self.cli), "app-server", "--listen", f"unix://{self.backend_socket}")

    def snapshot_hashes(self) -> HashSnapshot:
        config_hash = sha256_file(self.config_path) if self.config_path is not None else None
        return HashSnapshot(self.binary_sha256, config_hash)

    def require_frozen_binary(self) -> None:
        if self.binary_sha256 != FIXED_NATIVE_SHA256:
            raise RuntimeInvariantError(
                f"native binary SHA256 mismatch: expected {FIXED_NATIVE_SHA256}"
            )

    def verify_hashes(self, expected: HashSnapshot) -> None:
        actual = self.snapshot_hashes()
        if actual != expected:
            raise RuntimeInvariantError(
                "native binary/config changed during the controlled experiment"
            )

    @staticmethod
    def cleanup_owned(
        processes: Iterable[OwnedProcess],
        endpoints: Iterable[OwnedEndpoint],
        *,
        kill: Callable[[int], Any] | None = None,
    ) -> dict[str, list[Any]]:
        """Serially clean exact owned identities, preserving replacements."""

        killer = kill or (lambda pid: os.kill(pid, signal.SIGTERM))
        cleaned_pids: list[int] = []
        cleaned_endpoints: list[str] = []
        for process in processes:
            if process.pid <= 0:
                continue
            # A supplied killer is used by offline tests and by a caller that
            # has already completed its own identity barrier.  The real
            # default path requires both birth and executable to still match.
            if kill is None:
                if process.birth is None or process.executable is None:
                    continue
                if _process_birth(process.pid) != process.birth:
                    continue
                current = _process_path(process.pid)
                if current is None or os.path.realpath(current) != process.executable:
                    continue
            try:
                killer(process.pid)
            except PermissionError:
                continue
            except ProcessLookupError:
                # Already absent is a completed stop observation.
                pass
            cleaned_pids.append(process.pid)
        for endpoint in endpoints:
            if not endpoint.still_owned():
                continue
            try:
                endpoint.path.unlink()
            except FileNotFoundError:
                continue
            cleaned_endpoints.append(os.fspath(endpoint.path))
        return {"pids": cleaned_pids, "endpoints": cleaned_endpoints}


@dataclass(frozen=True)
class RuntimeWindow:
    started_at: float
    local_deadline: float
    model_deadline: float

    @classmethod
    def start(
        cls,
        *,
        now: float | None = None,
        local_seconds: float = LOCAL_WINDOW_SECONDS,
        model_seconds: float = MODEL_WINDOW_SECONDS,
    ) -> "RuntimeWindow":
        value = raw_seconds() if now is None else float(now)
        if local_seconds <= 0 or model_seconds <= 0:
            raise ValueError("runtime windows must be positive")
        return cls(value, value + local_seconds, value + model_seconds)

    def local_expired(self, now: float | None = None) -> bool:
        return (raw_seconds() if now is None else float(now)) >= self.local_deadline

    def model_expired(self, now: float | None = None) -> bool:
        return (raw_seconds() if now is None else float(now)) >= self.model_deadline


_SESSION_RE = re.compile(r"(?m)^[ \t│|]*Session:[ \t]*([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})[ \t│|]*\r?$")


class PTYDriver:
    """Bounded, real PTY driver with an in-memory screen tail only."""

    def __init__(self, pid: int, fd: int) -> None:
        self.pid = pid
        self.fd = fd
        self.started_at = raw_seconds()
        self.digest = hashlib.sha256()
        self.byte_count = 0
        self.screen_tail = b""
        self._screen_buffer = b""
        self.cursor_replies = 0
        self.exit_code: int | None = None
        self.eof = False
        self.inputs: list[dict[str, Any]] = []
        self._input_bytes: list[bytes] = []
        self.status_ids: set[str] = set()
        self._status_capture = False
        self._status_buffer = b""

    @classmethod
    def spawn(cls, argv: Sequence[str], *, cwd: str | os.PathLike[str]) -> "PTYDriver":
        if not argv or any(not isinstance(item, str) or not item for item in argv):
            raise ValueError("PTY argv must be non-empty strings")
        pid, fd = pty.fork()
        if pid == 0:
            os.chdir(cwd)
            os.environ["TERM"] = "xterm-256color"
            os.execvp(argv[0], list(argv))
            raise AssertionError("execvp returned")
        driver = cls(pid, fd)
        os.set_blocking(fd, False)
        fcntl = __import__("fcntl")
        fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 60, 200, 0, 0))
        return driver

    def poll(self) -> int | None:
        if self.exit_code is not None:
            return self.exit_code
        pid, status = os.waitpid(self.pid, os.WNOHANG)
        if pid:
            self.exit_code = os.waitstatus_to_exitcode(status)
        return self.exit_code

    def _capture(self, chunk: bytes) -> None:
        self.digest.update(chunk)
        self.byte_count += len(chunk)
        self.screen_tail = (self.screen_tail + chunk)[-262144:]
        self._screen_buffer = (self._screen_buffer + chunk)[-262144:]
        query = b"\x1b[6n"
        count = self._screen_buffer.count(query)
        if count:
            for _ in range(count):
                os.write(self.fd, b"\x1b[1;1R")
            self.cursor_replies += count
            self._screen_buffer = self._screen_buffer.replace(query, b"")
        if self._status_capture:
            self._status_buffer = (self._status_buffer + chunk)[-262144:]
            text = self._status_buffer.decode("utf-8", "replace")
            self.status_ids.update(_SESSION_RE.findall(_strip_terminal_controls(text)))

    def read(self, seconds: float) -> dict[str, Any]:
        if not 0 < seconds <= 5:
            raise ValueError("PTY read must be bounded to five seconds")
        deadline = raw_seconds() + seconds
        while not self.eof and raw_seconds() < deadline:
            remaining = max(0.001, min(0.1, deadline - raw_seconds()))
            ready, _, _ = select.select([self.fd], [], [], remaining)
            if ready:
                try:
                    chunk = os.read(self.fd, 16384)
                except BlockingIOError:
                    continue
                except OSError as exc:
                    if exc.errno not in {errno.EIO, errno.EBADF}:
                        raise
                    self.eof = True
                    break
                if not chunk:
                    self.eof = True
                    break
                self._capture(chunk)
            self.poll()
        self.poll()
        return self.evidence

    def read_available(self) -> dict[str, Any]:
        """Drain readable PTY bytes without blocking the asyncio loop."""

        if self.eof:
            return self.evidence
        while True:
            ready, _, _ = select.select([self.fd], [], [], 0)
            if not ready:
                break
            try:
                chunk = os.read(self.fd, 16384)
            except BlockingIOError:
                break
            except OSError as exc:
                if exc.errno not in {errno.EIO, errno.EBADF}:
                    raise
                self.eof = True
                break
            if not chunk:
                self.eof = True
                break
            self._capture(chunk)
        self.poll()
        return self.evidence

    async def aread_until(self, deadline: float) -> dict[str, Any]:
        while not self.eof and raw_seconds() < deadline:
            self.read_available()
            await asyncio.sleep(0.01)
        self.read_available()
        return self.evidence

    def write(self, data: bytes, *, action: str) -> None:
        if self.poll() is not None:
            raise RuntimeInvariantError("TUI PTY already exited")
        if not isinstance(data, bytes) or not data:
            raise ValueError("PTY input must be non-empty bytes")
        position = 0
        while position < len(data):
            written = os.write(self.fd, data[position:])
            if written <= 0:
                raise OSError("PTY write made no progress")
            position += written
        self._input_bytes.append(bytes(data))
        self.inputs.append({"at_monotonic_raw": raw_seconds(), "action": action, "bytes": len(data), "sha256": hashlib.sha256(data).hexdigest()})

    def command(self, command: str) -> None:
        if not isinstance(command, str) or not command or any(char in command for char in "\r\n\x1b"):
            raise ValueError("TUI command must be one fixed line")
        if command == "/status":
            self._begin_status_capture()
        else:
            self._status_capture = False
        self.write(b"\x1b[200~" + command.encode() + b"\x1b[201~", action=command + "-paste")
        self.write(b"\r", action=command + "-submit")

    def _begin_status_capture(self) -> None:
        self.status_ids.clear()
        self._status_buffer = b""
        self._status_capture = True

    def status_checkpoint(self, seconds: float) -> set[str]:
        if not self._status_capture:
            self._begin_status_capture()
        self.read(seconds)
        self._status_capture = False
        return set(self.status_ids)

    async def async_status_checkpoint(self, deadline: float) -> set[str]:
        if not self._status_capture:
            self._begin_status_capture()
        await self.aread_until(deadline)
        self._status_capture = False
        return set(self.status_ids)

    @property
    def input_bytes(self) -> tuple[bytes, ...]:
        return tuple(self._input_bytes)

    def wait(self, seconds: float) -> int | None:
        if not 0 < seconds <= 10:
            raise ValueError("bounded PTY wait required")
        deadline = raw_seconds() + seconds
        while self.poll() is None and raw_seconds() < deadline:
            self.read(min(0.2, max(0.001, deadline - raw_seconds())))
        return self.poll()

    async def await_exit(self, deadline: float) -> int | None:
        while self.poll() is None and raw_seconds() < deadline:
            self.read_available()
            await asyncio.sleep(0.01)
        self.read_available()
        return self.poll()

    def terminate(self, seconds: float = 10.0) -> bool:
        if self.poll() is not None:
            return True
        os.kill(self.pid, signal.SIGTERM)
        if self.wait(seconds) is not None:
            return True
        os.kill(self.pid, signal.SIGKILL)
        return self.wait(seconds) is not None

    @property
    def evidence(self) -> dict[str, Any]:
        result: dict[str, Any] = {
            "pid": self.pid,
            "bytes": self.byte_count,
            "sha256": self.digest.hexdigest(),
            "exit_code": self.exit_code,
            "cursor_position_replies": self.cursor_replies,
            "ui_state": classify_screen(self.screen_tail),
            "inputs": list(self.inputs),
        }
        if len(self.status_ids) == 1:
            result["status_session_id"] = next(iter(self.status_ids))
        elif len(self.status_ids) > 1:
            result["ambiguous_status_session"] = True
        return result

    def close(self) -> None:
        if self.poll() is None:
            raise RuntimeInvariantError("refuse implicit shutdown of a live PTY")
        try:
            os.close(self.fd)
        except OSError as exc:
            if exc.errno != errno.EBADF:
                raise


def _strip_terminal_controls(text: str) -> str:
    text = re.sub(r"\x1b\[[0-9;]*[Hf]", "\n", text)
    return re.sub(r"\x1b\[[0-?]*[ -/]*[@-~]", "", text)


def classify_screen(screen_tail: bytes) -> str:
    """Return a bounded UI state label without retaining terminal text."""

    text = _strip_terminal_controls(screen_tail.decode("utf-8", "replace")).lower()
    if "session:" in text:
        return "session"
    if any(token in text for token in ("error", "failed", "panic")):
        return "error"
    if any(token in text for token in ("picker", "select a", "choose")):
        return "picker"
    if any(token in text for token in ("trust", "onboarding", "sign in", "login", "welcome")):
        return "onboarding"
    if text.strip():
        return "unknown"
    return "empty"


@dataclass(frozen=True)
class AttachmentCandidate:
    connection_epoch: int
    thread_id: str
    session_id: str | None
    source: str = "observer"


@dataclass(frozen=True)
class ConnectionIdentity:
    connection_id: str
    epoch: int
    frontend_fd: int
    backend_fd: int
    frontend_peer: Any
    backend_peer: Any


def _peer_matches(peer: Any, expected: OwnedProcess | None) -> bool:
    if expected is None:
        return False
    executable = getattr(peer, "executable", None)
    return bool(
        getattr(peer, "complete", False)
        and getattr(peer, "pid", None) == expected.pid
        and getattr(peer, "birth", None) == expected.birth
        and isinstance(executable, (str, bytes, os.PathLike))
        and os.path.realpath(executable) == expected.executable
    )


class AttachmentInferer:
    """Infer one candidate from a complete, contiguous observer event epoch."""

    _REQUIRED_METHODS = {"initialize", "thread/start", "skills/list"}
    _OPAQUE_BOOTSTRAP_METHODS = {
        "account/read",
        "getAuthStatus",
        "account/rateLimits/read",
        "config/read",
        "configRequirements/read",
        "model/list",
        "modelProvider/capabilities/read",
        "collaborationMode/list",
        "hooks/list",
        "plugin/list",
        "thread/list",
        "thread/loaded/list",
        "thread/turns/list",
    }
    _OPAQUE_SERVER_NOTIFICATIONS = {
        "remoteControl/status/changed",
        "mcpServer/startupStatus/updated",
    }
    _QUALIFIED_COMMAND_METHOD = "command/exec"

    def __init__(self) -> None:
        self._epoch: int | None = None
        self._next_sequence: int | None = None
        self._invalid_reason: str | None = None
        self._upgrades: set[str] = set()
        self._requests: set[str] = set()
        self._responses: set[str] = set()
        self._thread_id: str | None = None
        self._session_id: str | None = None
        self._candidate: AttachmentCandidate | None = None
        self._status_reference: Mapping[str, Any] | None = None
        self._clear_pending = False
        self._clear_requested = False
        self._saw_unsubscribe_after_clear = False
        self._clear_old_thread_id: str | None = None
        self._clear_new_thread_id: str | None = None
        self._clear_started = False
        self._clear_unsubscribe_pending: dict[Any, str] = {}
        self._clear_unsubscribe_completed: set[Any] = set()
        self._clear_needs_fresh_skills = False
        self._clear_fresh_skills_requested = False
        self._bootstrap_pending: dict[tuple[str, Any], str] = {}

    @property
    def candidate(self) -> AttachmentCandidate | None:
        return self._candidate if self._invalid_reason is None else None

    @property
    def invalid_reason(self) -> str | None:
        return self._invalid_reason

    @property
    def replay_ready(self) -> bool:
        """Protocol replay readiness, independent of the candidate decision."""

        return (
            self._invalid_reason is None
            and not self._clear_pending
            and self._upgrades == {"client", "server"}
            and "initialize" in self._responses
            and "thread/start" in self._responses
            and self._thread_id is not None
            and not self._bootstrap_pending
        )

    @property
    def status_reference(self) -> Mapping[str, Any] | None:
        return self._status_reference

    def record_status_reference(self, reference: Mapping[str, Any]) -> None:
        """Keep an independently read UI status for comparison only."""

        self._status_reference = {
            key: value
            for key, value in reference.items()
            if key in {"session_id", "thread_id", "selected", "screen_match"}
        }

    def clear(self) -> None:
        self._candidate = None
        self._invalid_reason = "clear"
        self._clear_pending = True
        self._clear_requested = True
        self._saw_unsubscribe_after_clear = False
        self._clear_old_thread_id = self._thread_id
        self._clear_new_thread_id = None
        self._clear_started = False
        self._clear_unsubscribe_pending.clear()
        self._clear_unsubscribe_completed.clear()
        self._clear_needs_fresh_skills = False
        self._clear_fresh_skills_requested = False
        self._requests.intersection_update({"initialize"})
        self._responses.intersection_update({"initialize"})
        self._thread_id = None
        self._session_id = None

    def accept(self, event: Mapping[str, Any]) -> None:
        if not isinstance(event, Mapping):
            self._invalidate("invalid_event")
            return
        kind = event.get("event")
        epoch = event.get("conn_epoch")
        sequence = event.get("local_seq")
        if kind == "connection_open":
            self._start_epoch(epoch, sequence)
            return
        if self._epoch is None:
            self._invalidate("event_before_connection")
            return
        if epoch != self._epoch:
            self._invalidate("epoch_mismatch")
            return
        if not isinstance(sequence, int) or sequence != self._next_sequence:
            self._invalidate("event_sequence_gap")
            return
        self._next_sequence += 1
        if (
            self._clear_pending
            and kind == "rpc"
            and event.get("direction") == "client"
            and event.get("method") == "thread/unsubscribe"
        ):
            params = event.get("params")
            target = params.get("threadId") if isinstance(params, Mapping) else None
            request_id = event.get("request_id")
            if target != self._clear_old_thread_id or request_id is None:
                self._invalidate("clear_unsubscribe_target_mismatch")
                return
            if request_id in self._clear_unsubscribe_pending:
                self._invalidate("duplicate_clear_unsubscribe")
                return
            self._saw_unsubscribe_after_clear = True
            self._clear_unsubscribe_pending[request_id] = self._clear_old_thread_id
        if kind == "gap":
            self._invalidate(str(event.get("reason") or "gap"))
            return
        if kind == "eof":
            self._invalidate("eof")
            return
        if kind == "connection_close":
            self._invalidate("connection_close")
            return
        if kind == "rpc_unknown":
            method = event.get("method")
            request_id = event.get("request_id")
            qualified_command_request = (
                method == self._QUALIFIED_COMMAND_METHOD
                and event.get("direction") == "client"
                and request_id is not None
                and "response" not in event
                and event.get("workspace_probe_qualified") is True
            )
            qualified_command_response = (
                method == self._QUALIFIED_COMMAND_METHOD
                and event.get("direction") == "server"
                and event.get("response") is True
                and "workspace_probe_qualified" not in event
            )
            if method in self._OPAQUE_SERVER_NOTIFICATIONS:
                if event.get("direction") != "server" or request_id is not None or event.get("response") is True:
                    self._invalidate("unknown_event")
            elif method == self._QUALIFIED_COMMAND_METHOD and not (
                qualified_command_request or qualified_command_response
            ):
                self._invalidate("unknown_event")
            elif method not in self._OPAQUE_BOOTSTRAP_METHODS and not (
                qualified_command_request or qualified_command_response
            ):
                self._invalidate("unknown_event")
            elif event.get("response") is True:
                if event.get("direction") != "server":
                    self._invalidate("unknown_event")
                    return
                key = ("client", request_id)
                if key not in self._bootstrap_pending or self._bootstrap_pending.pop(key) != method:
                    self._invalidate("unknown_event")
                else:
                    self._maybe_candidate()
            elif event.get("direction") == "client" and request_id is not None:
                key = ("client", request_id)
                if key in self._bootstrap_pending:
                    self._invalidate("duplicate_bootstrap_request")
                else:
                    self._candidate = None
                    self._bootstrap_pending[key] = method
            else:
                self._invalidate("unknown_event")
            return
        if kind == "websocket_upgrade":
            direction = event.get("direction")
            if direction in {"client", "server"}:
                self._upgrades.add(direction)
        elif kind == "rpc":
            method = event.get("method")
            if (
                self._clear_pending
                and event.get("direction") == "server"
                and method == "thread/started"
            ):
                thread = event.get("thread")
                if (
                    self._clear_new_thread_id is None
                    or not isinstance(thread, Mapping)
                    or thread.get("id") != self._clear_new_thread_id
                ):
                    self._invalidate("clear_started_thread_mismatch")
                    return
                self._clear_started = True
                self._maybe_finish_clear()
            if event.get("direction") == "client" and method in self._REQUIRED_METHODS:
                self._requests.add(method)
                if method == "skills/list" and self._clear_needs_fresh_skills:
                    self._clear_fresh_skills_requested = True
        elif kind == "rpc_response":
            method = event.get("method")
            if event.get("direction") != "server" or event.get("ok") is not True:
                return
            if method == "thread/unsubscribe":
                request_id = event.get("request_id")
                if request_id in self._clear_unsubscribe_pending:
                    self._clear_unsubscribe_pending.pop(request_id, None)
                    self._clear_unsubscribe_completed.add(request_id)
                    self._maybe_finish_clear()
            if method in self._REQUIRED_METHODS:
                self._responses.add(method)
            if method == "thread/start":
                thread = event.get("thread")
                if isinstance(thread, Mapping) and isinstance(thread.get("id"), str):
                    self._thread_id = thread["id"]
                    session_id = thread.get("sessionId")
                    self._session_id = session_id if isinstance(session_id, str) else None
                    if self._clear_pending:
                        if self._clear_old_thread_id is None:
                            self._invalidate("clear_without_new_thread")
                            return
                        if self._clear_new_thread_id is not None and self._thread_id != self._clear_new_thread_id:
                            self._invalidate("clear_multiple_new_threads")
                            return
                        self._clear_new_thread_id = self._thread_id
                        self._maybe_finish_clear()
        self._maybe_candidate()

    def _maybe_finish_clear(self) -> None:
        if not self._clear_pending:
            return
        if self._clear_new_thread_id is None or not self._clear_started or len(self._clear_unsubscribe_completed) < 2:
            return
        self._invalid_reason = None
        self._clear_pending = False
        self._requests.discard("skills/list")
        self._responses.discard("skills/list")
        self._clear_needs_fresh_skills = True
        self._clear_fresh_skills_requested = False

    def _start_epoch(self, epoch: Any, sequence: Any) -> None:
        if not isinstance(epoch, int) or isinstance(epoch, bool) or epoch <= 0:
            self._invalidate("invalid_epoch")
            return
        if not isinstance(sequence, int) or sequence <= 0:
            self._invalidate("invalid_sequence")
            return
        self._epoch = epoch
        self._next_sequence = sequence + 1
        self._invalid_reason = None
        self._clear_pending = False
        self._clear_requested = False
        self._saw_unsubscribe_after_clear = False
        self._clear_old_thread_id = None
        self._clear_new_thread_id = None
        self._clear_started = False
        self._clear_unsubscribe_pending.clear()
        self._clear_unsubscribe_completed.clear()
        self._clear_needs_fresh_skills = False
        self._clear_fresh_skills_requested = False
        self._candidate = None
        self._upgrades.clear()
        self._requests.clear()
        self._responses.clear()
        self._thread_id = None
        self._session_id = None
        self._bootstrap_pending.clear()

    def _maybe_candidate(self) -> None:
        if self._invalid_reason not in {None, "clear"} or self._thread_id is None or self._bootstrap_pending:
            return
        if self._clear_pending:
            return
        if self._clear_needs_fresh_skills and (
            not self._clear_fresh_skills_requested or "skills/list" not in self._responses
        ):
            return
        if self._clear_requested and not self._saw_unsubscribe_after_clear:
            return
        if self._upgrades == {"client", "server"} and self._REQUIRED_METHODS <= self._requests and self._REQUIRED_METHODS <= self._responses:
            assert self._epoch is not None
            self._candidate = AttachmentCandidate(
                self._epoch, self._thread_id, self._session_id
            )

    def invalidate(self, reason: str) -> None:
        """Invalidate an epoch for transport lifecycle facts without an observer row."""

        self._invalidate(reason)

    def _invalidate(self, reason: str) -> None:
        if self._invalid_reason in {None, "clear"}:
            self._invalid_reason = reason
        self._clear_pending = False
        self._candidate = None


class ResumeAttachmentInferer:
    """Qualify one explicit ``thread/resume`` attachment from a fresh epoch.

    A resume candidate is deliberately separate from ``AttachmentInferer``:
    start/clear evidence must not be reused for a new connection.  The only
    source of the candidate identity is the successful ``thread/resume``
    response; the requested UUID is a constraint and never evidence by itself.
    """

    _OPAQUE_BOOTSTRAP_METHODS = AttachmentInferer._OPAQUE_BOOTSTRAP_METHODS
    _OPAQUE_BOOTSTRAP_METHODS = _OPAQUE_BOOTSTRAP_METHODS | {
        "thread/items/list",
        "thread/goal/get",
    }
    _OPAQUE_SERVER_NOTIFICATIONS = AttachmentInferer._OPAQUE_SERVER_NOTIFICATIONS
    _OPAQUE_SERVER_NOTIFICATIONS = _OPAQUE_SERVER_NOTIFICATIONS | {
        "thread/status/changed",
        "thread/tokenUsage/updated",
        "thread/goal/cleared",
    }
    _QUALIFIED_COMMAND_METHOD = AttachmentInferer._QUALIFIED_COMMAND_METHOD

    def __init__(self, expected_resume_thread_id: str) -> None:
        self.expected_resume_thread_id = canonical_resume_thread_id(expected_resume_thread_id)
        self._epoch: int | None = None
        self._next_sequence: int | None = None
        self._invalid_reason: str | None = None
        self._upgrades: set[str] = set()
        self._pending: dict[tuple[str, Any], str] = {}
        self._bootstrap_pending: dict[tuple[str, Any], str] = {}
        self._initialize_response = False
        self._initialized = False
        self._resume_response = False
        self._resume_request_id: Any | None = None
        self._resume_response_thread: AttachmentCandidate | None = None
        self._skills_after_request: set[Any] = set()
        self._skills_after_response: set[Any] = set()
        self._candidate: AttachmentCandidate | None = None
        self._status_reference: Mapping[str, Any] | None = None

    @property
    def candidate(self) -> AttachmentCandidate | None:
        return self._candidate if self._invalid_reason is None else None

    @property
    def invalid_reason(self) -> str | None:
        return self._invalid_reason

    @property
    def replay_ready(self) -> bool:
        return self.candidate is not None

    @property
    def status_reference(self) -> Mapping[str, Any] | None:
        return self._status_reference

    def record_status_reference(self, reference: Mapping[str, Any]) -> None:
        self._status_reference = {
            key: value
            for key, value in reference.items()
            if key in {"session_id", "thread_id", "selected", "screen_match"}
        }

    def clear(self) -> None:
        self._invalidate("clear")

    def invalidate(self, reason: str) -> None:
        self._invalidate(reason)

    def accept(self, event: Mapping[str, Any]) -> None:
        if not isinstance(event, Mapping):
            self._invalidate("invalid_event")
            return
        kind = event.get("event")
        epoch = event.get("conn_epoch")
        sequence = event.get("local_seq")
        if kind == "connection_open":
            self._start_epoch(epoch, sequence)
            return
        if self._epoch is None:
            self._invalidate("event_before_connection")
            return
        if epoch != self._epoch:
            self._invalidate("epoch_mismatch")
            return
        if not isinstance(sequence, int) or isinstance(sequence, bool) or sequence != self._next_sequence:
            self._invalidate("event_sequence_gap")
            return
        self._next_sequence += 1
        if kind in {"gap", "eof", "connection_close"}:
            self._invalidate(str(event.get("reason") or kind))
            return
        if kind == "websocket_upgrade":
            direction = event.get("direction")
            if direction in {"client", "server"}:
                self._upgrades.add(direction)
            else:
                self._invalidate("invalid_upgrade_direction")
            self._maybe_candidate()
            return
        if kind == "rpc_unknown":
            self._accept_unknown(event)
            self._maybe_candidate()
            return
        if kind == "rpc":
            self._accept_request(event)
            self._maybe_candidate()
            return
        if kind == "rpc_response":
            self._accept_response(event)
            self._maybe_candidate()
            return
        self._invalidate("unknown_event")

    def _start_epoch(self, epoch: Any, sequence: Any) -> None:
        if not isinstance(epoch, int) or isinstance(epoch, bool) or epoch <= 0:
            self._invalidate("invalid_epoch")
            return
        if not isinstance(sequence, int) or isinstance(sequence, bool) or sequence <= 0:
            self._invalidate("invalid_sequence")
            return
        # A second epoch is admissible only after an explicit transport
        # invalidation.  This prevents a new open from inheriting old proof.
        if self._epoch is not None and self._invalid_reason is None:
            self._invalidate("new_epoch_before_invalidation")
            return
        self._epoch = epoch
        self._next_sequence = sequence + 1
        self._invalid_reason = None
        self._upgrades.clear()
        self._pending.clear()
        self._bootstrap_pending.clear()
        self._initialize_response = False
        self._initialized = False
        self._resume_response = False
        self._resume_request_id = None
        self._resume_response_thread = None
        self._skills_after_request.clear()
        self._skills_after_response.clear()
        self._candidate = None

    @staticmethod
    def _request_key(direction: Any, request_id: Any) -> tuple[str, Any] | None:
        if direction not in {"client", "server"} or request_id is None:
            return None
        try:
            hash(request_id)
        except TypeError:
            return None
        return direction, request_id

    def _accept_request(self, event: Mapping[str, Any]) -> None:
        direction = event.get("direction")
        method = event.get("method")
        request_id = event.get("request_id")
        if not isinstance(method, str):
            self._invalidate("invalid_method")
            return
        # thread/started is an optional server notification.  It can check a
        # response identity but can never establish one.
        if method == "thread/started":
            if direction != "server" or request_id is not None:
                self._invalidate("invalid_thread_started")
                return
            thread = event.get("thread")
            if isinstance(thread, Mapping) and "id" in thread and thread.get("id") != self.expected_resume_thread_id:
                self._invalidate("thread_started_identity_mismatch")
            return
        if method == "initialized":
            if direction != "client" or request_id is not None or not self._initialize_response:
                self._invalidate("initialized_before_initialize")
            else:
                self._initialized = True
            return
        if method in {"thread/start", "turn/start"}:
            self._invalidate("unexpected_start_in_resume")
            return
        if direction != "client":
            self._invalidate("unexpected_server_request")
            return
        key = self._request_key(direction, request_id)
        if key is None:
            self._invalidate("missing_request_id")
            return
        if key in self._pending:
            self._invalidate("duplicate_request_id")
            return
        if method not in {"initialize", "thread/read", "thread/resume", "skills/list"}:
            self._invalidate("unsupported_resume_method")
            return
        if method == "thread/resume":
            params = event.get("params")
            thread_id = params.get("threadId") if isinstance(params, Mapping) else None
            if (
                thread_id != self.expected_resume_thread_id
                or self._resume_response
                or self._resume_request_id is not None
            ):
                self._invalidate("resume_request_identity_mismatch")
                return
            self._resume_request_id = request_id
        if method == "skills/list" and self._resume_response:
            self._skills_after_request.add(request_id)
        self._pending[key] = method

    def _accept_response(self, event: Mapping[str, Any]) -> None:
        if event.get("direction") != "server":
            self._invalidate("unexpected_client_response")
            return
        request_id = event.get("request_id")
        key = self._request_key("client", request_id)
        method = self._pending.pop(key, None) if key is not None else None
        if method is None:
            self._invalidate("orphan_response")
            return
        if event.get("method") != method:
            self._invalidate("response_method_mismatch")
            return
        if event.get("ok") is not True:
            self._invalidate(f"{method}_failed")
            return
        if method == "initialize":
            self._initialize_response = True
            return
        if method == "thread/resume":
            thread = event.get("thread")
            thread_id = thread.get("id") if isinstance(thread, Mapping) else None
            if thread_id != self.expected_resume_thread_id:
                self._invalidate("resume_response_identity_mismatch")
                return
            session_id = thread.get("sessionId") if isinstance(thread, Mapping) else None
            session = session_id if isinstance(session_id, str) else None
            self._resume_response = True
            self._resume_response_thread = AttachmentCandidate(
                self._epoch or 0, thread_id, session
            )
            return
        if method == "skills/list" and request_id in self._skills_after_request:
            self._skills_after_request.discard(request_id)
            self._skills_after_response.add(request_id)

    def _accept_unknown(self, event: Mapping[str, Any]) -> None:
        method = event.get("method")
        direction = event.get("direction")
        request_id = event.get("request_id")
        if method == self._QUALIFIED_COMMAND_METHOD:
            qualified_request = (
                direction == "client"
                and request_id is not None
                and event.get("response") is not True
                and event.get("workspace_probe_qualified") is True
            )
            qualified_response = (
                direction == "server"
                and event.get("response") is True
                and "workspace_probe_qualified" not in event
            )
            if qualified_response:
                key = ("client", request_id)
                if self._bootstrap_pending.pop(key, None) != method:
                    self._invalidate("bootstrap_response_mismatch")
                return
            if qualified_request:
                key = ("client", request_id)
                if key in self._bootstrap_pending:
                    self._invalidate("duplicate_bootstrap_request")
                else:
                    self._bootstrap_pending[key] = method
                return
            self._invalidate("unknown_event")
            return
        if method in self._OPAQUE_SERVER_NOTIFICATIONS:
            if direction != "server" or request_id is not None or event.get("response") is True:
                self._invalidate("unknown_event")
            return
        if method not in self._OPAQUE_BOOTSTRAP_METHODS:
            self._invalidate("unknown_event")
            return
        if event.get("response") is True:
            if direction != "server" or request_id is None:
                self._invalidate("unknown_event")
                return
            key = ("client", request_id)
            if self._bootstrap_pending.pop(key, None) != method:
                self._invalidate("bootstrap_response_mismatch")
            return
        if direction != "client" or request_id is None:
            self._invalidate("unknown_event")
            return
        key = ("client", request_id)
        if key in self._bootstrap_pending:
            self._invalidate("duplicate_bootstrap_request")
            return
        self._bootstrap_pending[key] = method

    def _maybe_candidate(self) -> None:
        if self._invalid_reason is not None or self._epoch is None:
            return
        if self._bootstrap_pending or self._pending:
            return
        if self._upgrades != {"client", "server"}:
            return
        if not (self._initialize_response and self._initialized and self._resume_response):
            return
        if not self._skills_after_response:
            return
        resume = self._resume_response_thread
        if resume is None:
            return
        self._candidate = resume

    def _invalidate(self, reason: str) -> None:
        if self._invalid_reason is None:
            self._invalid_reason = reason
        self._candidate = None


class RuntimePhase(str, Enum):
    NEW = "new"
    BACKEND_RUNNING = "backend_running"
    PROXY_RUNNING = "proxy_running"
    TUI_RUNNING = "tui_running"
    CLOSED = "closed"


class _CaptureBridge:
    """Adapt transport capture callbacks to the side-channel observer."""

    def __init__(
        self,
        observer: Any,
        inferer: AttachmentInferer,
        *,
        frontend_identity: OwnedProcess | None = None,
        backend_identity: OwnedProcess | None = None,
        strict_identity: bool = False,
        max_trace_events: int = 4096,
    ) -> None:
        if max_trace_events < 1:
            raise ValueError("max_trace_events must be positive")
        self.observer = observer
        self.inferer = inferer
        self.frontend_identity = frontend_identity
        self.backend_identity = backend_identity
        self.strict_identity = strict_identity
        self.connection_records: dict[str, ConnectionIdentity] = {}
        self.identity_errors: dict[str, str] = {}
        self.max_trace_events = max_trace_events
        self.trace: list[dict[str, Any]] = []
        self.trace_complete = True
        self.model_turns = 0
        self.epoch_invalid: dict[int, str] = {}
        self._trace_keys: set[tuple[Any, Any]] = set()
        self._eof_seen: set[tuple[str, Any]] = set()
        self._observer_closed: set[str] = set()

    def _accept_rows(self, rows: Iterable[Mapping[str, Any]]) -> None:
        for row in rows:
            if not self._record_trace(row):
                return
            if row.get("event") == "gap":
                self.trace_complete = False
                self._mark_epoch_invalid(row.get("conn_epoch"), str(row.get("reason") or "gap"))
            elif row.get("event") in {"eof", "connection_close"}:
                self._mark_epoch_invalid(row.get("conn_epoch"), str(row.get("event")))
            if (
                row.get("direction") == "client"
                and row.get("method") == "turn/start"
                and row.get("event") in {"rpc", "rpc_unknown"}
            ):
                self.model_turns += 1
            self.inferer.accept(row)

    def _record_trace(self, row: Mapping[str, Any]) -> bool:
        if "local_seq" in row:
            key = (row.get("conn_epoch"), row.get("local_seq"))
            if key in self._trace_keys:
                self.trace_complete = False
                self.inferer.invalidate("trace_duplicate_sequence")
                return False
            self._trace_keys.add(key)
        if len(self.trace) >= self.max_trace_events:
            self.trace_complete = False
            self.inferer.invalidate("trace_overflow")
            return False
        trace_row = {
            key: row[key]
            for key in (
                "event", "local_seq", "conn_epoch", "direction", "method",
                "request_id", "request_direction", "response", "ok", "thread", "reason",
            )
            if key in row
        }
        for key in ("payload_bytes", "message_bytes", "limit_bytes"):
            value = row.get(key)
            if type(value) is int and value >= 0:
                trace_row[key] = value
        if type(row.get("workspace_probe_qualified")) is bool:
            trace_row["workspace_probe_qualified"] = row["workspace_probe_qualified"]
        if (
            row.get("event") == "rpc"
            and row.get("direction") == "client"
            and row.get("method") in {"thread/unsubscribe", "thread/resume", "thread/fork", "thread/inject_items"}
        ):
            params = row.get("params")
            thread_id = params.get("threadId") if isinstance(params, Mapping) else None
            if isinstance(thread_id, str) and 0 < len(thread_id) <= 256:
                trace_row["thread_id"] = thread_id
        self.trace.append(trace_row)
        return True

    def _mark_epoch_invalid(self, epoch: Any, reason: str) -> None:
        if isinstance(epoch, int) and not isinstance(epoch, bool) and epoch > 0:
            self.epoch_invalid.setdefault(epoch, reason)

    def on_connect(self, event: Any) -> None:
        record = ConnectionIdentity(
            event.connection_id,
            event.epoch,
            int(getattr(event, "frontend_fd", -1)),
            int(getattr(event, "backend_fd", -1)),
            getattr(event, "frontend_peer", None),
            getattr(event, "backend_peer", None),
        )
        self.connection_records[event.connection_id] = record
        if self.strict_identity and (
            not _peer_matches(record.frontend_peer, self.frontend_identity)
            or not _peer_matches(record.backend_peer, self.backend_identity)
        ):
            self.identity_errors[event.connection_id] = "connection_identity_mismatch"
            self.inferer.invalidate("connection_identity_mismatch")
            return
        # Observer returns bounded rows from feed/close but retains them in its
        # queue until drained.  Clear already-consumed rows before a new epoch
        # so a probe cannot replay old local_seq values into the new epoch.
        self.observer.drain_events()
        epoch = self.observer.open(event.connection_id, conn_epoch=event.epoch)
        self._accept_rows(self.observer.drain_events())
        if epoch != event.epoch:
            self.inferer.accept({"event": "gap", "conn_epoch": event.epoch, "reason": "epoch_mismatch", "local_seq": 2})

    def on_data(self, event: Any) -> None:
        direction = "client" if event.direction == "frontend_to_backend" else "server"
        self._accept_rows(self.observer.feed(event.connection_id, direction, event.data))
        self.observer.drain_events()

    def on_lifecycle(self, event: Any) -> None:
        if event.kind == "eof":
            self._mark_epoch_invalid(event.epoch, "eof")
            direction = getattr(event, "direction", None)
            eof_key = (event.connection_id, direction or "unknown")
            if eof_key in self._eof_seen:
                self.trace_complete = False
                self.inferer.invalidate("trace_duplicate_eof")
                return
            self._eof_seen.add(eof_key)
            self._record_trace({"event": "eof", "conn_epoch": event.epoch, "direction": direction, "reason": "eof"})
            self.inferer.invalidate("eof")
            # Wait for both transport legs before asking the observer to
            # validate buffered bytes. A first normal EOF is not a duplicate;
            # close performs the incomplete-frame/upgrade check once.
            legs = {item[1] for item in self._eof_seen if item[0] == event.connection_id}
            if direction is None or {"frontend_to_backend", "backend_to_frontend"} <= legs:
                self._accept_rows(self.observer.close(event.connection_id))
                self.observer.drain_events()
                self._observer_closed.add(event.connection_id)
                self.observer.set_pending(event.connection_id)
        if event.kind == "disconnect":
            self._mark_epoch_invalid(event.epoch, "disconnect")
            if event.connection_id not in self._observer_closed:
                self._accept_rows(self.observer.close(event.connection_id))
                self.observer.drain_events()
                self._observer_closed.add(event.connection_id)

    def on_gap(self, event: Any) -> None:
        self._mark_epoch_invalid(event.epoch, event.reason or "gap")
        self.trace_complete = False
        self._record_trace({"event": "gap", "conn_epoch": event.epoch, "reason": event.reason})
        self.inferer.invalidate(event.reason or "gap")


class NativeRuntime:
    """Own backend, relay, TUI, and the attachment evidence window."""

    def __init__(
        self,
        config: NativeRuntimeConfig,
        *,
        relay_factory: Callable[..., Any] | None = None,
        observer: Any | None = None,
        inferer: AttachmentInferer | None = None,
        spawn_process: Callable[..., Any] | None = None,
    ) -> None:
        self.config = config
        self.inferer = inferer or (
            ResumeAttachmentInferer(config.resume_thread_id)
            if config.resume_thread_id is not None
            else AttachmentInferer()
        )
        self.observer = observer
        self._relay_factory = relay_factory
        self._spawn_process = spawn_process or subprocess.Popen
        self._backend: Any | None = None
        self._tui: Any | None = None
        self._tui_driver: PTYDriver | None = None
        self._relay: Any | None = None
        self._bridge: _CaptureBridge | None = None
        self._backend_identity: OwnedProcess | None = None
        self._tui_identity: OwnedProcess | None = None
        self._owned_processes: list[OwnedProcess] = []
        self._owned_endpoints: list[OwnedEndpoint] = []
        self._expected_hashes: HashSnapshot | None = None
        self.phase = RuntimePhase.NEW
        self.window: RuntimeWindow | None = None

    @property
    def tui_identity(self) -> OwnedProcess | None:
        return self._tui_identity

    @property
    def backend_identity(self) -> OwnedProcess | None:
        return self._backend_identity

    @property
    def candidate(self) -> AttachmentCandidate | None:
        return self.inferer.candidate

    @property
    def bridge(self) -> _CaptureBridge | None:
        return self._bridge

    @property
    def tui_driver(self) -> PTYDriver | None:
        return self._tui_driver

    def record_status_reference(self, status: Mapping[str, Any]) -> None:
        self.inferer.record_status_reference(status)

    async def disconnect(
        self,
        connection_id: str,
        *,
        leg: str = "both",
    ) -> bool:
        """Close a selected owned relay leg while keeping the listener alive."""

        if self._relay is None:
            raise RuntimeInvariantError("proxy is not running")
        if leg not in {"frontend", "backend", "both"}:
            raise ValueError("leg must be frontend, backend, or both")
        return bool(await self._relay.disconnect(connection_id, leg=leg))

    def _authorize_tui(self, peer: Any) -> bool:
        owned = self.tui_identity
        if owned is None or getattr(peer, "pid", None) != owned.pid:
            return False
        executable = getattr(peer, "executable", None)
        if not isinstance(executable, (str, bytes, os.PathLike)):
            return False
        return (
            getattr(peer, "birth", None) == owned.birth
            and os.path.realpath(executable) == owned.executable
            and bool(getattr(peer, "complete", False))
        )

    def _authorize_backend(self, peer: Any) -> bool:
        return _peer_matches(peer, self._backend_identity)

    async def _wait_backend_ready(self) -> None:
        if self._backend is None or self.window is None or self._backend_identity is None:
            raise RuntimeInvariantError("backend readiness checked before spawn")
        while raw_seconds() < self.window.local_deadline:
            poll = self._backend.poll()
            if poll is not None:
                raise RuntimeInvariantError(f"native backend exited before ready: {poll}")
            if self.config.backend_socket.exists() and _process_holds_socket(
                self._backend_identity.pid, self.config.backend_socket
            ):
                return
            await asyncio.sleep(min(0.05, max(0.001, self.window.local_deadline - raw_seconds())))
        raise TimeoutError("native backend did not expose its owned private socket within 10 seconds")

    async def start(self) -> None:
        if self.phase is not RuntimePhase.NEW:
            raise RuntimeInvariantError(f"runtime cannot start from {self.phase.value}")
        if os.path.lexists(self.config.frontend_socket):
            raise FileExistsError(f"default frontend socket is occupied: {self.config.frontend_socket}")
        if os.path.lexists(self.config.backend_socket):
            raise FileExistsError(f"private backend socket is occupied: {self.config.backend_socket}")
        self.config.require_frozen_binary()
        self._expected_hashes = self.config.snapshot_hashes()
        self.window = RuntimeWindow.start(
            local_seconds=self.config.local_window_seconds,
            model_seconds=self.config.model_window_seconds,
        )
        self._backend = self._spawn_process(
            list(self.config.backend_argv),
            cwd=self.config.cwd,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        self._backend_identity = _capture_process(self._backend, self.config.cli)
        self._owned_processes.append(self._backend_identity)
        self.phase = RuntimePhase.BACKEND_RUNNING
        try:
            await self._wait_backend_ready()
            if self._relay_factory is None:
                from proxy_transport import ProxyServer

                self._relay_factory = ProxyServer
            if self.observer is None:
                self.observer = build_native_observer(self.config.cwd)
            bridge = _CaptureBridge(
                self.observer,
                self.inferer,
                backend_identity=self._backend_identity,
                strict_identity=True,
            )
            self._bridge = bridge
            self._relay = self._relay_factory(
                self.config.frontend_socket,
                self.config.backend_socket,
                bridge,
                authorize_peer=self._authorize_tui,
                authorize_backend_peer=self._authorize_backend,
            )
            await self._relay.start()
            if self.config.backend_socket.exists():
                self._owned_endpoints.append(OwnedEndpoint.capture(self.config.backend_socket))
            self._owned_endpoints.append(OwnedEndpoint.capture(self.config.frontend_socket))
            self.phase = RuntimePhase.PROXY_RUNNING
            # This is deliberately synchronous after await relay.start: spawn
            # and registration happen in one loop turn before peer callbacks.
            self._tui_driver = PTYDriver.spawn(list(self.config.tui_argv), cwd=self.config.cwd)
            self._tui = self._tui_driver
            self._tui_identity = _capture_process(self._tui_driver, self.config.cli)
            self._owned_processes.append(self._tui_identity)
            bridge.frontend_identity = self._tui_identity
            self.phase = RuntimePhase.TUI_RUNNING
        except BaseException:
            await self.close()
            raise

    async def close(self) -> dict[str, list[Any]]:
        if self.phase is RuntimePhase.CLOSED:
            return {"pids": [], "endpoints": []}
        relay_error: BaseException | None = None
        if self._relay is not None:
            try:
                await self._relay.close()
            except BaseException as exc:
                relay_error = exc
            self._relay = None
        failed_pids: list[int] = []
        if self._tui_driver is not None:
            try:
                if not self._tui_driver.terminate():
                    failed_pids.append(self._tui_driver.pid)
                self._tui_driver.close()
            except (OSError, RuntimeError, RuntimeInvariantError):
                failed_pids.append(self._tui_driver.pid)
        if self._backend is not None:
            try:
                if self._backend.poll() is None:
                    self._backend.terminate()
                    try:
                        self._backend.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        self._backend.kill()
                        self._backend.wait(timeout=10)
                if self._backend.poll() is None and self._backend_identity is not None:
                    failed_pids.append(self._backend_identity.pid)
            except (OSError, PermissionError, subprocess.SubprocessError):
                if self._backend_identity is not None:
                    failed_pids.append(self._backend_identity.pid)
        result = NativeRuntimeConfig.cleanup_owned([], self._owned_endpoints)
        self._owned_processes.clear()
        self._owned_endpoints.clear()
        self.phase = RuntimePhase.CLOSED
        if self._expected_hashes is not None:
            self.config.verify_hashes(self._expected_hashes)
        if failed_pids:
            result["failed_pids"] = failed_pids
        if relay_error is not None:
            raise relay_error
        if failed_pids:
            raise RuntimeInvariantError("owned process did not exit after bounded cleanup")
        return result


@dataclass(frozen=True)
class CaseDirectory:
    path: Path

    @property
    def name(self) -> str:
        return self.path.name

    @classmethod
    def create(cls, path: str | os.PathLike[str]) -> "CaseDirectory":
        target = Path(path)
        target.parent.mkdir(parents=True, exist_ok=True)
        target.mkdir(mode=0o700)
        return cls(target)

    def write_json(self, name: str, value: Mapping[str, Any]) -> Path:
        if Path(name).name != name or not name.endswith(".json"):
            raise ValueError("case artifact name must be a single JSON filename")
        target = self.path / name
        payload = (json.dumps(value, sort_keys=True, separators=(",", ":")) + "\n").encode()
        fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        try:
            with os.fdopen(fd, "wb") as stream:
                stream.write(payload)
                stream.flush()
                os.fsync(stream.fileno())
        except BaseException:
            try:
                os.unlink(target)
            except FileNotFoundError:
                pass
            raise
        return target


def _peer_summary(peer: Any) -> dict[str, Any] | None:
    if peer is None:
        return None
    return {
        key: getattr(peer, key, None)
        for key in ("pid", "uid", "birth", "executable", "complete", "source")
    }


def _connection_summary(record: ConnectionIdentity) -> dict[str, Any]:
    return {
        "connection_id": record.connection_id,
        "epoch": record.epoch,
        "frontend_fd": record.frontend_fd,
        "backend_fd": record.backend_fd,
        "frontend_peer": _peer_summary(record.frontend_peer),
        "backend_peer": _peer_summary(record.backend_peer),
    }


def _failure_record(exc: BaseException, stage: str) -> dict[str, str]:
    code = {
        "TimeoutError": "deadline_exceeded",
        "FileExistsError": "owned_endpoint_occupied",
        "RuntimeInvariantError": "runtime_invariant",
        "PermissionError": "permission_denied",
    }.get(type(exc).__name__, "runtime_error")
    digest = hashlib.sha256(f"{type(exc).__name__}:{exc}".encode()).hexdigest()
    return {"stage": stage, "type": type(exc).__name__, "code": code, "message_sha256": digest}


def new_epoch_after_disconnect(
    epochs_before_disconnect: set[int], records: Iterable[ConnectionIdentity]
) -> bool:
    """Recognize only epochs observed after the frozen disconnect marker."""

    return any(record.epoch not in epochs_before_disconnect for record in records)


async def run_case(
    config: NativeRuntimeConfig,
    case_dir: str | os.PathLike[str],
    *,
    runtime_factory: Callable[[NativeRuntimeConfig], NativeRuntime] = NativeRuntime,
    termination_mode: str = "forced_disconnect",
) -> dict[str, Any]:
    """Run the bounded zero-model static operation sequence.

    This is the only executable experiment entry.  It writes a new case
    directory with O_EXCL artifacts and never writes PTY screen text.
    """

    if termination_mode not in {"forced_disconnect", "normal_quit"}:
        raise ValueError("termination_mode must be forced_disconnect or normal_quit")
    case = CaseDirectory.create(case_dir)
    runtime = runtime_factory(config)
    result: dict[str, Any] = {
        "case": case.name,
        "status": "unknown",
        "model_turns": None,
        "steps": [],
        "protected_hashes": None,
        "connections": [],
        "termination_mode": termination_mode,
        "termination_trigger": None,
        "termination_requested": False,
        "quit_sent": False,
    }
    initial_candidate_ok = False
    clear_candidate_ok = False
    disconnect_invalid = False
    initial_status_match = False
    clear_status_match = False
    quit_success = False
    initial_thread: str | None = None
    disconnect_epoch: int | None = None
    old_epoch_reason: str | None = None
    connection_id: str | None = None
    termination_epoch: int | None = None
    termination_epoch_reason: str | None = None
    termination_epoch_invalid = False
    new_epoch_observed = False
    epochs_before_disconnect: set[int] = set()
    current_stage = "prepare"

    def step(name: str, **fields: Any) -> None:
        nonlocal current_stage
        current_stage = name
        result["steps"].append({"name": name, **fields})

    try:
        result["protected_hashes"] = asdict(config.snapshot_hashes())
        await runtime.start()
        tui = runtime._tui_driver
        if tui is None:
            raise RuntimeInvariantError("runtime did not register its PTY TUI")
        initial_window = RuntimeWindow.start(local_seconds=config.local_window_seconds, model_seconds=config.model_window_seconds)
        while not runtime.inferer.replay_ready and not initial_window.local_expired():
            await tui.aread_until(min(initial_window.local_deadline, raw_seconds() + 1.0))
        while runtime.inferer.replay_ready and runtime.candidate is None and not initial_window.local_expired():
            await tui.aread_until(min(initial_window.local_deadline, raw_seconds() + 1.0))
        step(
            "initial-readiness",
            replay_ready=runtime.inferer.replay_ready,
            candidate=asdict(runtime.candidate) if runtime.candidate is not None else None,
            tui=tui.evidence,
        )
        initial_candidate_ok = runtime.candidate is not None
        initial_thread = runtime.candidate.thread_id if runtime.candidate else None
        initial_target = runtime.candidate.session_id if runtime.candidate and runtime.candidate.session_id else runtime.candidate.thread_id if runtime.candidate else None

        if not initial_candidate_ok:
            result["diagnostic_status"] = tui.evidence.get("ui_state", "unknown")
            failfast_window = RuntimeWindow.start(
                local_seconds=config.local_window_seconds,
                model_seconds=config.model_window_seconds,
            )
            if tui.poll() is None:
                tui.command("/quit")
                await tui.await_exit(failfast_window.local_deadline)
            step("initial-fail-fast", quit_exit_code=tui.poll(), tui=tui.evidence)
            raise RuntimeInvariantError("initial_replay_or_candidate_missing")

        status_window = RuntimeWindow.start(local_seconds=config.local_window_seconds, model_seconds=config.model_window_seconds)
        tui.command("/status")
        await tui.async_status_checkpoint(status_window.local_deadline)
        status_ids = sorted(tui.status_ids)
        runtime.record_status_reference({"session_id": status_ids[0]} if len(status_ids) == 1 else {"screen_match": False})
        initial_status_match = len(status_ids) == 1 and status_ids[0] == initial_target
        step("status-initial", session_ids=status_ids, screen_match=initial_status_match)

        clear_window = RuntimeWindow.start(local_seconds=config.local_window_seconds, model_seconds=config.model_window_seconds)
        runtime.inferer.clear()
        tui.command("/clear")
        while not runtime.inferer.replay_ready and not clear_window.local_expired():
            await tui.aread_until(min(clear_window.local_deadline, raw_seconds() + 1.0))
        while runtime.inferer.replay_ready and runtime.candidate is None and not clear_window.local_expired():
            await tui.aread_until(min(clear_window.local_deadline, raw_seconds() + 1.0))
        step(
            "clear-replay",
            replay_ready=runtime.inferer.replay_ready,
            candidate=asdict(runtime.candidate) if runtime.candidate is not None else None,
            tui=tui.evidence,
        )
        clear_candidate_ok = runtime.candidate is not None
        clear_target = runtime.candidate.session_id if runtime.candidate and runtime.candidate.session_id else runtime.candidate.thread_id if runtime.candidate else None
        clear_thread = runtime.candidate.thread_id if runtime.candidate else None

        status_window = RuntimeWindow.start(local_seconds=config.local_window_seconds, model_seconds=config.model_window_seconds)
        tui.command("/status")
        await tui.async_status_checkpoint(status_window.local_deadline)
        status_ids = sorted(tui.status_ids)
        runtime.record_status_reference({"session_id": status_ids[0]} if len(status_ids) == 1 else {"screen_match": False})
        clear_status_match = len(status_ids) == 1 and status_ids[0] == clear_target
        step("status-after-clear", session_ids=status_ids, screen_match=clear_status_match)

        connection_id = next(reversed(runtime._bridge.connection_records), None) if runtime._bridge else None
        if connection_id is not None:
            termination_epoch = runtime._bridge.connection_records[connection_id].epoch
            epochs_before_disconnect = {record.epoch for record in runtime._bridge.connection_records.values()}
            if termination_mode == "forced_disconnect":
                disconnect_epoch = termination_epoch
                result["termination_trigger"] = "frontend_disconnect"
                disconnect_window = RuntimeWindow.start(local_seconds=config.local_window_seconds, model_seconds=config.model_window_seconds)
                disconnected = await runtime.disconnect(connection_id, leg="frontend")
                await tui.aread_until(min(disconnect_window.local_deadline, raw_seconds() + 2.0))
                while runtime.candidate is not None and raw_seconds() < disconnect_window.local_deadline:
                    await asyncio.sleep(0.01)
                old_epoch_reason = runtime._bridge.epoch_invalid.get(disconnect_epoch) if runtime._bridge else None
                disconnect_invalid = disconnected and old_epoch_reason is not None
                step("disconnect", connection_id=connection_id, disconnected=disconnected, candidate=asdict(runtime.candidate) if runtime.candidate else None)
            else:
                gate_ok = (
                    initial_candidate_ok
                    and initial_status_match
                    and clear_candidate_ok
                    and clear_status_match
                    and initial_target is not None
                    and clear_target is not None
                    and clear_thread != initial_thread
                )
                step("normal-quit-gate", passed=gate_ok)
                if not gate_ok:
                    result["termination_trigger"] = "normal_quit_gate"
                    raise RuntimeInvariantError("normal_quit_gate_failed")
                if tui.poll() is not None:
                    result["termination_trigger"] = "normal_quit_precondition_failed"
                    raise RuntimeInvariantError("normal_quit_tui_not_alive")
                if runtime._bridge.epoch_invalid.get(termination_epoch) is not None:
                    result["termination_trigger"] = "normal_quit_precondition_failed"
                    raise RuntimeInvariantError("normal_quit_epoch_already_invalid")
                result["termination_trigger"] = "normal_quit"
                result["termination_requested"] = True
        elif termination_mode == "normal_quit":
            result["termination_trigger"] = "normal_quit_gate"
            raise RuntimeInvariantError("normal_quit_connection_missing")

        quit_window = RuntimeWindow.start(local_seconds=config.local_window_seconds, model_seconds=config.model_window_seconds)
        if tui.poll() is None:
            tui.command("/quit")
            result["quit_sent"] = True
            exit_code = await tui.await_exit(quit_window.local_deadline)
        else:
            exit_code = tui.poll()
        quit_success = exit_code == 0
        if exit_code == 0 and runtime._bridge and termination_epoch is not None:
            await tui.aread_until(min(quit_window.local_deadline, raw_seconds() + 2.0))
        if runtime._bridge and termination_epoch is not None:
            termination_epoch_reason = runtime._bridge.epoch_invalid.get(termination_epoch)
            termination_epoch_invalid = (
                termination_epoch_reason in {"eof", "connection_close"}
                if termination_mode == "normal_quit"
                else termination_epoch_reason is not None
            )
        step("quit", exit_code=exit_code, quit_success=quit_success, termination_epoch_invalid=termination_epoch_invalid, quit_sent=result["quit_sent"], tui=tui.evidence)
        result["connections"] = [_connection_summary(record) for record in runtime._bridge.connection_records.values()] if runtime._bridge else []
        result["status"] = "unknown"
    except BaseException as exc:
        result["failure"] = _failure_record(exc, current_stage)
    finally:
        try:
            await runtime.close()
        except BaseException as exc:
            result["cleanup_failure"] = _failure_record(exc, "cleanup")
        try:
            config.verify_hashes(HashSnapshot(**result["protected_hashes"]))
        except BaseException as exc:
            result["protection_failure"] = _failure_record(exc, "protection")
        bridge = runtime._bridge
        if bridge is not None:
            new_epoch_observed = termination_epoch is not None and new_epoch_after_disconnect(
                epochs_before_disconnect, bridge.connection_records.values()
            )
            result["disconnect_barrier"] = {
                "connection_id": connection_id,
                "old_epoch": termination_epoch,
                "old_epoch_invalid": termination_epoch_invalid,
                "old_epoch_invalid_reason": termination_epoch_reason,
                "termination_mode": termination_mode,
                "termination_trigger": result.get("termination_trigger"),
            }
            result["reconnect"] = {
                "observed": new_epoch_observed,
                "status": "unknown" if new_epoch_observed else "not_run",
            }
            result["model_turns"] = bridge.model_turns if bridge.trace_complete else None
            result["trace_complete"] = bridge.trace_complete
            try:
                case.write_json("trace.json", {"complete": bridge.trace_complete, "events": bridge.trace})
                result["trace_artifact"] = "trace.json"
            except BaseException as exc:
                result["trace_failure"] = _failure_record(exc, "trace")
        cleanup_ok = "cleanup_failure" not in result and not (
            runtime._tui_driver is not None and runtime._tui_driver.poll() is None
        ) and not (runtime._backend is not None and runtime._backend.poll() is None)
        result["status"] = "observed" if (
            initial_candidate_ok
            and clear_candidate_ok
            and (termination_mode == "normal_quit" or disconnect_invalid)
            and termination_epoch_invalid
            and not new_epoch_observed
            and initial_status_match
            and clear_status_match
            and initial_target is not None
            and clear_target is not None
            and clear_thread != initial_thread
            and quit_success
            and (termination_mode != "normal_quit" or (result["termination_requested"] and result["quit_sent"]))
            and result.get("model_turns") == 0
            and result.get("trace_complete") is True
            and cleanup_ok
            and not any(key.endswith("failure") or key == "failure" for key in result)
        ) else "unknown"
        case.write_json("result.json", result)
    return result


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="controlled zero-model native proxy case")
    parser.add_argument("--run", action="store_true", help="execute the explicitly requested native case")
    parser.add_argument("--case-dir", type=Path)
    parser.add_argument("--frontend-socket", type=Path)
    parser.add_argument("--backend-socket", type=Path)
    parser.add_argument("--cwd", type=Path, default=Path.cwd())
    parser.add_argument("--config", type=Path)
    parser.add_argument("--cli", type=Path, default=DEFAULT_CLI)
    parser.add_argument(
        "--resume-thread-id",
        help="explicit canonical UUID for the controlled native resume entry",
    )
    parser.add_argument(
        "--termination-mode",
        choices=("forced_disconnect", "normal_quit"),
        default="forced_disconnect",
    )
    args = parser.parse_args(argv)
    if not args.run:
        print("prepared; pass --run with explicit case and private socket paths")
        return 0
    if args.case_dir is None or args.frontend_socket is None or args.backend_socket is None:
        parser.error("--run requires --case-dir, --frontend-socket, and --backend-socket")
    result = asyncio.run(
        run_case(
            NativeRuntimeConfig(
                frontend_socket=args.frontend_socket,
                backend_socket=args.backend_socket,
                cwd=args.cwd,
                config_path=args.config,
                cli=args.cli,
                resume_thread_id=args.resume_thread_id,
            ),
            args.case_dir,
            termination_mode=args.termination_mode,
        )
    )
    print(json.dumps(result, sort_keys=True))
    return 0 if result.get("status") == "observed" else 1


__all__ = [
    "AttachmentCandidate",
    "AttachmentInferer",
    "ResumeAttachmentInferer",
    "CaseDirectory",
    "ConnectionIdentity",
    "DEFAULT_CLI",
    "FIXED_NATIVE_SHA256",
    "HashSnapshot",
    "NativeRuntime",
    "NativeRuntimeConfig",
    "OwnedEndpoint",
    "OwnedProcess",
    "PTYDriver",
    "RuntimeInvariantError",
    "RuntimePhase",
    "RuntimeWindow",
    "build_ordinary_tui_argv",
    "build_resume_tui_argv",
    "canonical_resume_thread_id",
    "raw_seconds",
    "sha256_file",
    "main",
    "new_epoch_after_disconnect",
    "run_case",
]


if __name__ == "__main__":
    raise SystemExit(main())
