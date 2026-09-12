"""Small, embeddable Unix-domain socket relay for controlled TUI experiments.

This module deliberately does not inspect or terminate a WebSocket protocol.  It
copies bytes between one frontend connection and one backend connection and
exposes bounded, in-memory observations to a caller supplied sink.
"""

from __future__ import annotations

import asyncio
import ctypes
import ctypes.util
import inspect
import os
import platform
import socket
import struct
import subprocess
import time
import uuid
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Awaitable, Callable, Literal


Direction = Literal["frontend_to_backend", "backend_to_frontend"]


@dataclass(frozen=True)
class PeerIdentity:
    pid: int | None
    uid: int | None
    birth: str | None
    executable: str | None
    pid_available: bool
    uid_available: bool
    birth_available: bool
    executable_available: bool
    source: str

    @property
    def complete(self) -> bool:
        return all(
            (
                self.pid_available,
                self.uid_available,
                self.birth_available,
                self.executable_available,
            )
        )


@dataclass(frozen=True)
class ConnectionEvent:
    observation_seq: int
    connection_id: str
    epoch: int
    frontend_fd: int
    backend_fd: int
    frontend_peer: PeerIdentity
    backend_peer: PeerIdentity
    authenticated: bool


@dataclass(frozen=True)
class DataEvent:
    observation_seq: int
    connection_id: str
    epoch: int
    direction: Direction
    direction_seq: int
    data: bytes


@dataclass(frozen=True)
class LifecycleEvent:
    observation_seq: int
    connection_id: str
    epoch: int
    kind: Literal["eof", "half_close", "disconnect"]
    direction: Direction | None
    direction_seq: int | None


@dataclass(frozen=True)
class GapEvent:
    observation_seq: int
    connection_id: str
    epoch: int
    reason: str
    fatal: bool


class CaptureSink:
    """Optional synchronous or asynchronous observer callback interface."""

    def on_connect(self, event: ConnectionEvent) -> Any:
        return None

    def on_data(self, event: DataEvent) -> Any:
        return None

    def on_lifecycle(self, event: LifecycleEvent) -> Any:
        return None

    def on_gap(self, event: GapEvent) -> Any:
        return None


class _CaptureFailure(RuntimeError):
    pass


def _read_int_option(sock: Any, level: int, option: int) -> int | None:
    try:
        raw = sock.getsockopt(level, option, 4)
    except (AttributeError, OSError, ValueError):
        return None
    if isinstance(raw, int):
        return raw
    if len(raw) < 4:
        return None
    return struct.unpack("=i", raw[:4])[0]


def _process_metadata(pid: int | None) -> tuple[str | None, str | None]:
    if pid is None or pid <= 0:
        return None, None

    birth: str | None = None
    try:
        result = subprocess.run(
            ("/bin/ps", "-p", str(pid), "-o", "lstart="),
            check=False,
            capture_output=True,
            text=True,
            timeout=1,
        )
        value = result.stdout.strip()
        if result.returncode == 0 and value:
            birth = value
    except (OSError, subprocess.SubprocessError):
        pass

    executable: str | None = None
    if platform.system() == "Darwin":
        try:
            libproc = ctypes.CDLL("/usr/lib/libproc.dylib")
            buffer = ctypes.create_string_buffer(4096)
            length = libproc.proc_pidpath(pid, buffer, len(buffer))
            if length > 0:
                executable = os.fsdecode(buffer.value)
        except (AttributeError, OSError, ValueError):
            pass
    else:
        try:
            executable = os.path.realpath(f"/proc/{pid}/exe")
            if not os.path.exists(executable):
                executable = None
        except OSError:
            pass
    return birth, executable


def peer_identity(sock: Any) -> PeerIdentity:
    """Read OS supplied peer identity without treating a caller label as a PID.

    The Darwin values are from ``sys/un.h`` in the macOS SDK:
    ``SOL_LOCAL=0``, ``LOCAL_PEERCRED=0x001`` and ``LOCAL_PEERPID=0x002``.
    Python 3.13 does not expose these names on this host, so the documented
    SDK values are kept local and are used only on Darwin.
    """

    system = platform.system()
    pid: int | None = None
    uid: int | None = None
    source = "unavailable"
    if system == "Darwin":
        level = getattr(socket, "SOL_LOCAL", 0)
        peer_pid_option = getattr(socket, "LOCAL_PEERPID", 0x002)
        peer_cred_option = getattr(socket, "LOCAL_PEERCRED", 0x001)
        pid = _read_int_option(sock, level, peer_pid_option)
        try:
            raw = sock.getsockopt(level, peer_cred_option, 256)
            # struct xucred starts with uint32 cr_version and uid_t cr_uid.
            if isinstance(raw, bytes) and len(raw) >= 8:
                uid = struct.unpack("=I", raw[4:8])[0]
        except (AttributeError, OSError, ValueError, struct.error):
            pass
        source = "darwin-local"
    elif hasattr(socket, "SO_PEERCRED"):
        try:
            raw = sock.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12)
            if isinstance(raw, bytes) and len(raw) >= 12:
                pid, uid, _gid = struct.unpack("=iii", raw[:12])
        except (AttributeError, OSError, ValueError, struct.error):
            pass
        source = "linux-so_peercred"

    birth, executable = _process_metadata(pid)
    return PeerIdentity(
        pid=pid,
        uid=uid,
        birth=birth,
        executable=executable,
        pid_available=pid is not None,
        uid_available=uid is not None,
        birth_available=birth is not None,
        executable_available=executable is not None,
        source=source,
    )


AuthorizePeer = Callable[[PeerIdentity], bool | Awaitable[bool]]


class ProxyServer:
    """One frontend listener, with one private backend connection per client."""

    def __init__(
        self,
        frontend_path: str | os.PathLike[str],
        backend_path: str | os.PathLike[str],
        capture_sink: CaptureSink,
        *,
        authorize_peer: AuthorizePeer | None = None,
        chunk_size: int = 64 * 1024,
    ) -> None:
        if chunk_size <= 0:
            raise ValueError("chunk_size must be positive")
        self.frontend_path = Path(frontend_path)
        self.backend_path = Path(backend_path)
        self.capture_sink = capture_sink
        self.authorize_peer = authorize_peer
        self.chunk_size = chunk_size
        self._server: asyncio.AbstractServer | None = None
        self._accept_tasks: set[asyncio.Task[None]] = set()
        self._connection_tasks: set[asyncio.Task[None]] = set()
        self._connections: dict[str, tuple[asyncio.StreamWriter, asyncio.StreamWriter]] = {}
        self._socket_stat: os.stat_result | None = None
        self._observation_seq = 0
        self._epoch = 0
        self._capture_failed = False
        self._closed = False

    @property
    def capture_failed(self) -> bool:
        return self._capture_failed

    async def start(self) -> "ProxyServer":
        if self._server is not None:
            return self
        if self.frontend_path.exists():
            raise FileExistsError(f"frontend socket already exists: {self.frontend_path}")
        self.frontend_path.parent.mkdir(parents=True, exist_ok=True)
        listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        try:
            listener.bind(str(self.frontend_path))
            listener.listen(socket.SOMAXCONN)
            os.chmod(self.frontend_path, 0o600)
            listener.setblocking(False)
            self._server = await asyncio.start_unix_server(
                self._accept,
                sock=listener,
                start_serving=False,
            )
        except BaseException:
            listener.close()
            raise
        self._socket_stat = self.frontend_path.lstat()
        await self._server.start_serving()
        self._closed = False
        return self

    async def close(self) -> None:
        self._closed = True
        if self._server is not None:
            self._server.close()
            await self._server.wait_closed()
            self._server = None
        tasks = list(self._accept_tasks | self._connection_tasks)
        for task in tasks:
            task.cancel()
        if tasks:
            await asyncio.gather(*tasks, return_exceptions=True)
        self._accept_tasks.clear()
        self._connection_tasks.clear()
        self._connections.clear()
        self._unlink_owned_socket()

    async def disconnect(
        self,
        connection_id: str,
        *,
        leg: Literal["frontend", "backend", "both"] = "both",
    ) -> bool:
        """Close one live connection's selected leg without stopping the listener."""
        if leg not in {"frontend", "backend", "both"}:
            raise ValueError("leg must be frontend, backend, or both")
        writers = self._connections.get(connection_id)
        if writers is None:
            return False
        selected = {
            "frontend": (writers[0],),
            "backend": (writers[1],),
            "both": writers,
        }[leg]
        await _close_writers(*selected)
        return True

    def _unlink_owned_socket(self) -> None:
        if self._socket_stat is None:
            return
        try:
            current = self.frontend_path.stat()
        except FileNotFoundError:
            return
        if (current.st_dev, current.st_ino) != (
            self._socket_stat.st_dev,
            self._socket_stat.st_ino,
        ):
            return
        if current.st_uid != self._socket_stat.st_uid:
            return
        if not stat_is_socket(current):
            return
        try:
            self.frontend_path.unlink()
        except FileNotFoundError:
            pass

    def _accept(self, reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
        task = asyncio.create_task(self._handle_client(reader, writer))
        self._accept_tasks.add(task)
        task.add_done_callback(self._accept_tasks.discard)

    async def _handle_client(
        self, reader: asyncio.StreamReader, frontend_writer: asyncio.StreamWriter
    ) -> None:
        frontend_socket = frontend_writer.get_extra_info("socket")
        frontend_peer = peer_identity(frontend_socket)
        authorized = await self._is_authorized(frontend_peer)
        if not authorized:
            frontend_writer.close()
            await _wait_closed(frontend_writer)
            return

        try:
            backend_reader, backend_writer = await asyncio.open_unix_connection(
                self.backend_path
            )
        except (OSError, asyncio.CancelledError):
            frontend_writer.close()
            await _wait_closed(frontend_writer)
            raise

        self._epoch += 1
        epoch = self._epoch
        connection_id = f"conn-{epoch}-{uuid.uuid4().hex[:12]}"
        backend_socket = backend_writer.get_extra_info("socket")
        backend_peer = peer_identity(backend_socket)
        event = ConnectionEvent(
            observation_seq=self._next_seq(),
            connection_id=connection_id,
            epoch=epoch,
            frontend_fd=_fd(frontend_socket),
            backend_fd=_fd(backend_socket),
            frontend_peer=frontend_peer,
            backend_peer=backend_peer,
            authenticated=True,
        )
        try:
            await self._emit("on_connect", event, connection_id, epoch)
        except _CaptureFailure:
            await _close_writers(frontend_writer, backend_writer)
            return

        self._connections[connection_id] = (frontend_writer, backend_writer)

        direction_seq: dict[Direction, int] = {
            "frontend_to_backend": 0,
            "backend_to_frontend": 0,
        }
        pumps = [
            asyncio.create_task(
                self._pump(
                    reader,
                    backend_writer,
                    connection_id,
                    epoch,
                    "frontend_to_backend",
                    direction_seq,
                )
            ),
            asyncio.create_task(
                self._pump(
                    backend_reader,
                    frontend_writer,
                    connection_id,
                    epoch,
                    "backend_to_frontend",
                    direction_seq,
                )
            ),
        ]
        self._connection_tasks.update(pumps)
        try:
            done, pending = await asyncio.wait(
                pumps, return_when=asyncio.FIRST_EXCEPTION
            )
            failures = [task.exception() for task in done if not task.cancelled()]
            if any(isinstance(result, _CaptureFailure) for result in failures):
                for task in pending:
                    task.cancel()
                await asyncio.gather(*pending, return_exceptions=True)
            else:
                await asyncio.gather(*pending, return_exceptions=True)
            if any(
                isinstance(result, Exception)
                and not isinstance(result, (_CaptureFailure, asyncio.CancelledError))
                for result in failures
            ):
                # Socket errors are represented by the final disconnect event;
                # they do not turn into an observed data event.
                pass
            if not self._capture_failed:
                await self._emit(
                    "on_lifecycle",
                    LifecycleEvent(
                        observation_seq=self._next_seq(),
                        connection_id=connection_id,
                        epoch=epoch,
                        kind="disconnect",
                        direction=None,
                        direction_seq=None,
                    ),
                    connection_id,
                    epoch,
                )
        finally:
            await _close_writers(frontend_writer, backend_writer)
            self._connections.pop(connection_id, None)
            for task in pumps:
                self._connection_tasks.discard(task)

    async def _pump(
        self,
        reader: asyncio.StreamReader,
        destination: asyncio.StreamWriter,
        connection_id: str,
        epoch: int,
        direction: Direction,
        direction_seq: dict[Direction, int],
    ) -> None:
        try:
            while True:
                data = await reader.read(self.chunk_size)
                if not data:
                    direction_seq[direction] += 1
                    await self._emit(
                        "on_lifecycle",
                        LifecycleEvent(
                            observation_seq=self._next_seq(),
                            connection_id=connection_id,
                            epoch=epoch,
                            kind="eof",
                            direction=direction,
                            direction_seq=direction_seq[direction],
                        ),
                        connection_id,
                        epoch,
                    )
                    destination.write_eof()
                    await self._emit(
                        "on_lifecycle",
                        LifecycleEvent(
                            observation_seq=self._next_seq(),
                            connection_id=connection_id,
                            epoch=epoch,
                            kind="half_close",
                            direction=direction,
                            direction_seq=direction_seq[direction],
                        ),
                        connection_id,
                        epoch,
                    )
                    return
                direction_seq[direction] += 1
                await self._emit(
                    "on_data",
                    DataEvent(
                        observation_seq=self._next_seq(),
                        connection_id=connection_id,
                        epoch=epoch,
                        direction=direction,
                        direction_seq=direction_seq[direction],
                        data=data,
                    ),
                    connection_id,
                    epoch,
                )
                destination.write(data)
                await destination.drain()
        except _CaptureFailure:
            raise
        except (ConnectionError, asyncio.IncompleteReadError, OSError):
            return

    async def _is_authorized(self, peer: PeerIdentity) -> bool:
        if peer.pid is None:
            return False
        if self.authorize_peer is None:
            return False
        result = self.authorize_peer(peer)
        if inspect.isawaitable(result):
            result = await result
        return bool(result)

    async def _emit(
        self, method: str, event: Any, connection_id: str, epoch: int
    ) -> None:
        if self._capture_failed:
            raise _CaptureFailure("capture sink is invalidated")
        callback = getattr(self.capture_sink, method)
        try:
            result = callback(event)
            if inspect.isawaitable(result):
                await result
        except Exception as exc:
            self._capture_failed = True
            gap = GapEvent(
                observation_seq=self._next_seq(),
                connection_id=connection_id,
                epoch=epoch,
                reason="capture_callback_failed",
                fatal=True,
            )
            try:
                result = self.capture_sink.on_gap(gap)
                if inspect.isawaitable(result):
                    await result
            except Exception:
                pass
            raise _CaptureFailure("capture callback failed") from exc

    def _next_seq(self) -> int:
        self._observation_seq += 1
        return self._observation_seq


def stat_is_socket(stat_result: os.stat_result) -> bool:
    return stat_result.st_mode & 0o170000 == 0o140000


def _fd(sock: Any) -> int:
    try:
        return int(sock.fileno())
    except (AttributeError, OSError, TypeError, ValueError):
        return -1


async def _wait_closed(writer: asyncio.StreamWriter) -> None:
    try:
        await writer.wait_closed()
    except (ConnectionError, OSError):
        pass


async def _close_writers(*writers: asyncio.StreamWriter) -> None:
    for writer in writers:
        writer.close()
    await asyncio.gather(*(_wait_closed(writer) for writer in writers))


__all__ = [
    "CaptureSink",
    "ConnectionEvent",
    "DataEvent",
    "GapEvent",
    "LifecycleEvent",
    "PeerIdentity",
    "ProxyServer",
    "peer_identity",
]
