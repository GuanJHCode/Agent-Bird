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

import hashlib
import os
import platform
import signal
import subprocess
import time
from dataclasses import dataclass
from enum import Enum
from pathlib import Path
from typing import Any, Callable, Iterable, Mapping


FIXED_NATIVE_SHA256 = "4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc"
DEFAULT_CLI = Path("/Users/guanjunhui/.local/bin/codex")
LOCAL_WINDOW_SECONDS = 10.0
MODEL_WINDOW_SECONDS = 120.0


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
    config_path: Path | None = None
    local_window_seconds: float = LOCAL_WINDOW_SECONDS
    model_window_seconds: float = MODEL_WINDOW_SECONDS
    expected_binary_sha256: str = FIXED_NATIVE_SHA256

    def __post_init__(self) -> None:
        object.__setattr__(self, "frontend_socket", Path(self.frontend_socket))
        object.__setattr__(self, "backend_socket", Path(self.backend_socket))
        object.__setattr__(self, "cli", Path(self.cli))
        if self.config_path is not None:
            object.__setattr__(self, "config_path", Path(self.config_path))
        if self.frontend_socket == self.backend_socket:
            raise ValueError("frontend and backend sockets must be distinct")
        if self.local_window_seconds <= 0 or self.model_window_seconds <= 0:
            raise ValueError("runtime windows must be positive")

    @property
    def binary_sha256(self) -> str:
        return sha256_file(self.cli)

    @property
    def tui_argv(self) -> tuple[str, ...]:
        return build_ordinary_tui_argv(self.cli)

    @property
    def backend_argv(self) -> tuple[str, ...]:
        return (os.fspath(self.cli), "app-server", "--listen", os.fspath(self.backend_socket))

    def snapshot_hashes(self) -> HashSnapshot:
        config_hash = sha256_file(self.config_path) if self.config_path is not None else None
        return HashSnapshot(self.binary_sha256, config_hash)

    def require_frozen_binary(self) -> None:
        if self.binary_sha256 != self.expected_binary_sha256:
            raise RuntimeInvariantError(
                f"native binary SHA256 mismatch: expected {self.expected_binary_sha256}"
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
            except (ProcessLookupError, PermissionError):
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
        value = time.monotonic() if now is None else float(now)
        if local_seconds <= 0 or model_seconds <= 0:
            raise ValueError("runtime windows must be positive")
        return cls(value, value + local_seconds, value + model_seconds)

    def local_expired(self, now: float | None = None) -> bool:
        return (time.monotonic() if now is None else float(now)) >= self.local_deadline

    def model_expired(self, now: float | None = None) -> bool:
        return (time.monotonic() if now is None else float(now)) >= self.model_deadline


@dataclass(frozen=True)
class AttachmentCandidate:
    connection_epoch: int
    thread_id: str
    session_id: str | None
    source: str = "observer"


class AttachmentInferer:
    """Infer one candidate from a complete, contiguous observer event epoch."""

    _REQUIRED_METHODS = {"initialize", "thread/start", "skills/list"}

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

    @property
    def candidate(self) -> AttachmentCandidate | None:
        return self._candidate if self._invalid_reason is None else None

    @property
    def invalid_reason(self) -> str | None:
        return self._invalid_reason

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
            and event.get("method") == "thread/start"
        ):
            self._invalid_reason = None
            self._clear_pending = False
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
            self._invalidate("unknown_event")
            return
        if kind == "websocket_upgrade":
            direction = event.get("direction")
            if direction in {"client", "server"}:
                self._upgrades.add(direction)
        elif kind == "rpc":
            method = event.get("method")
            if event.get("direction") == "client" and method in self._REQUIRED_METHODS:
                self._requests.add(method)
        elif kind == "rpc_response":
            method = event.get("method")
            if event.get("direction") != "server" or event.get("ok") is not True:
                return
            if method in self._REQUIRED_METHODS:
                self._responses.add(method)
            if method == "thread/start":
                thread = event.get("thread")
                if isinstance(thread, Mapping) and isinstance(thread.get("id"), str):
                    self._thread_id = thread["id"]
                    session_id = thread.get("sessionId")
                    self._session_id = session_id if isinstance(session_id, str) else None
        self._maybe_candidate()

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
        self._candidate = None
        self._upgrades.clear()
        self._requests.clear()
        self._responses.clear()
        self._thread_id = None
        self._session_id = None

    def _maybe_candidate(self) -> None:
        if self._invalid_reason is not None or self._thread_id is None:
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
        if self._invalid_reason is None:
            self._invalid_reason = reason
        self._clear_pending = False
        self._candidate = None


class RuntimePhase(str, Enum):
    NEW = "new"
    BACKEND_RUNNING = "backend_running"
    PROXY_RUNNING = "proxy_running"
    TUI_RUNNING = "tui_running"
    CLOSED = "closed"


class _CaptureBridge:
    """Adapt transport capture callbacks to the side-channel observer."""

    def __init__(self, observer: Any, inferer: AttachmentInferer) -> None:
        self.observer = observer
        self.inferer = inferer

    def _accept_rows(self, rows: Iterable[Mapping[str, Any]]) -> None:
        for row in rows:
            self.inferer.accept(row)

    def on_connect(self, event: Any) -> None:
        epoch = self.observer.open(event.connection_id, conn_epoch=event.epoch)
        self._accept_rows(self.observer.drain_events())
        if epoch != event.epoch:
            self.inferer.accept({"event": "gap", "conn_epoch": event.epoch, "reason": "epoch_mismatch", "local_seq": 2})

    def on_data(self, event: Any) -> None:
        direction = "client" if event.direction == "frontend_to_backend" else "server"
        self._accept_rows(self.observer.feed(event.connection_id, direction, event.data))

    def on_lifecycle(self, event: Any) -> None:
        if event.kind == "eof":
            self.inferer.invalidate("eof")
            self.observer.set_pending(event.connection_id)
        if event.kind == "disconnect":
            self._accept_rows(self.observer.close(event.connection_id))

    def on_gap(self, event: Any) -> None:
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
        self.inferer = inferer or AttachmentInferer()
        self.observer = observer
        self._relay_factory = relay_factory
        self._spawn_process = spawn_process or subprocess.Popen
        self._backend: Any | None = None
        self._tui: Any | None = None
        self._relay: Any | None = None
        self._owned_processes: list[OwnedProcess] = []
        self._owned_endpoints: list[OwnedEndpoint] = []
        self._expected_hashes: HashSnapshot | None = None
        self.phase = RuntimePhase.NEW
        self.window: RuntimeWindow | None = None

    @property
    def tui_identity(self) -> OwnedProcess | None:
        return self._owned_processes[-1] if self._tui is not None and self._owned_processes else None

    @property
    def candidate(self) -> AttachmentCandidate | None:
        return self.inferer.candidate

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

    async def start(self) -> None:
        if self.phase is not RuntimePhase.NEW:
            raise RuntimeInvariantError(f"runtime cannot start from {self.phase.value}")
        if self.config.frontend_socket.exists():
            raise FileExistsError(f"default frontend socket is occupied: {self.config.frontend_socket}")
        if self.config.backend_socket.exists():
            raise FileExistsError(f"private backend socket is occupied: {self.config.backend_socket}")
        self.config.require_frozen_binary()
        self._expected_hashes = self.config.snapshot_hashes()
        self.window = RuntimeWindow.start(
            local_seconds=self.config.local_window_seconds,
            model_seconds=self.config.model_window_seconds,
        )
        self._backend = self._spawn_process(
            list(self.config.backend_argv), stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL
        )
        self._owned_processes.append(_capture_process(self._backend, self.config.cli))
        self.phase = RuntimePhase.BACKEND_RUNNING
        try:
            if self._relay_factory is None:
                from proxy_transport import ProxyServer

                self._relay_factory = ProxyServer
            if self.observer is None:
                from proxy_observer import Observer

                self.observer = Observer()
            bridge = _CaptureBridge(self.observer, self.inferer)
            self._relay = self._relay_factory(
                self.config.frontend_socket,
                self.config.backend_socket,
                bridge,
                authorize_peer=self._authorize_tui,
            )
            await self._relay.start()
            self._owned_endpoints.append(OwnedEndpoint.capture(self.config.frontend_socket))
            self.phase = RuntimePhase.PROXY_RUNNING
            # This is deliberately synchronous after await relay.start: spawn
            # and registration happen in one loop turn before peer callbacks.
            self._tui = self._spawn_process(
                list(self.config.tui_argv), stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL
            )
            self._owned_processes.append(_capture_process(self._tui, self.config.cli))
            self.phase = RuntimePhase.TUI_RUNNING
        except BaseException:
            await self.close()
            raise

    async def close(self) -> dict[str, list[Any]]:
        if self.phase is RuntimePhase.CLOSED:
            return {"pids": [], "endpoints": []}
        if self._relay is not None:
            await self._relay.close()
            self._relay = None
        result = NativeRuntimeConfig.cleanup_owned(self._owned_processes, self._owned_endpoints)
        self._owned_processes.clear()
        self._owned_endpoints.clear()
        self.phase = RuntimePhase.CLOSED
        if self._expected_hashes is not None:
            self.config.verify_hashes(self._expected_hashes)
        return result


__all__ = [
    "AttachmentCandidate",
    "AttachmentInferer",
    "DEFAULT_CLI",
    "FIXED_NATIVE_SHA256",
    "HashSnapshot",
    "NativeRuntime",
    "NativeRuntimeConfig",
    "OwnedEndpoint",
    "OwnedProcess",
    "RuntimeInvariantError",
    "RuntimePhase",
    "RuntimeWindow",
    "build_ordinary_tui_argv",
    "sha256_file",
]
