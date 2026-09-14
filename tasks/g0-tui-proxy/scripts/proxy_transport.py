"""Small, embeddable Unix-domain socket relay for controlled TUI experiments.

This module deliberately does not inspect or terminate a WebSocket protocol.  It
copies bytes between one frontend connection and one backend connection and
exposes bounded, in-memory observations to a caller supplied sink.
"""

from __future__ import annotations

import asyncio
from contextlib import AsyncExitStack
import ctypes
import errno
import inspect
import os
import platform
import socket
import struct
import subprocess
import uuid
from dataclasses import dataclass
from pathlib import Path
from typing import Any, AsyncContextManager, Awaitable, Callable, Literal


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
BackendPeerValidator = Callable[[PeerIdentity], bool | Awaitable[bool]]


@dataclass(frozen=True)
class BackendConnection:
    reader: asyncio.StreamReader
    writer: asyncio.StreamWriter
    authorize_peer: BackendPeerValidator

    def __post_init__(self) -> None:
        if not callable(self.authorize_peer):
            raise ValueError("a factory backend requires its own peer validator")


BackendFactory = Callable[[PeerIdentity], AsyncContextManager[BackendConnection]]
WireGate = Callable[[bytes], bytes | Awaitable[bytes]]
WireGateFactory = Callable[[PeerIdentity, PeerIdentity, str, int], tuple[WireGate | None, WireGate | None] | None]


class ProxyServer:
    """One frontend listener, with one private backend connection per client."""

    def __init__(
        self,
        frontend_path: str | os.PathLike[str],
        backend_path: str | os.PathLike[str] | None,
        capture_sink: CaptureSink,
        *,
        authorize_peer: AuthorizePeer | None = None,
        authorize_backend_peer: BackendPeerValidator | None = None,
        backend_factory: BackendFactory | None = None,
        stop_on_frontend_eof: bool = False,
        wait_for_frontend_data: bool = False,
        chunk_size: int = 64 * 1024,
        max_connections: int = 64,
        callback_timeout: float = 5.0,
        wire_gate_factory: WireGateFactory | None = None,
    ) -> None:
        if chunk_size <= 0:
            raise ValueError("chunk_size must be positive")
        if max_connections <= 0:
            raise ValueError("max_connections must be positive")
        if callback_timeout <= 0:
            raise ValueError("callback_timeout must be positive")
        self.frontend_path = Path(frontend_path)
        if (backend_path is None) == (backend_factory is None):
            raise ValueError("provide exactly one static backend path or backend factory")
        self.backend_path = Path(backend_path) if backend_path is not None else None
        self.backend_factory = backend_factory
        self.stop_on_frontend_eof = stop_on_frontend_eof
        if type(wait_for_frontend_data) is not bool or wait_for_frontend_data and backend_factory is None:
            raise ValueError("frontend data gate requires an explicit backend factory")
        self.wait_for_frontend_data = wait_for_frontend_data
        self.empty_probe_eofs = 0
        self.capture_sink = capture_sink
        self.authorize_peer = authorize_peer
        self.authorize_backend_peer = authorize_backend_peer
        self.chunk_size = chunk_size
        self.max_connections = max_connections
        self.callback_timeout = callback_timeout
        self.wire_gate_factory = wire_gate_factory
        self._server: asyncio.AbstractServer | None = None
        self._accept_tasks: set[asyncio.Task[None]] = set()
        self._connection_tasks: set[asyncio.Task[None]] = set()
        self._connections: dict[str, tuple[asyncio.StreamWriter, asyncio.StreamWriter]] = {}
        self._socket_stat: os.stat_result | None = None
        self._bind_stat: os.stat_result | None = None
        self._parent_fd: int | None = None
        self._observation_seq = 0
        self._epoch = 0
        self._capture_failed = False
        self._closed = False

    @property
    def active_frontends(self) -> int:
        return len(self._accept_tasks)

    @property
    def capture_failed(self) -> bool:
        return self._capture_failed

    async def start(self, *, inherited_listener: socket.socket | None = None) -> "ProxyServer":
        if self._server is not None:
            return self
        if inherited_listener is not None:
            listener = inherited_listener
            try:
                if (listener.family != socket.AF_UNIX
                        or listener.getsockopt(socket.SOL_SOCKET, socket.SO_TYPE) != socket.SOCK_STREAM
                        or Path(os.fsdecode(listener.getsockname())).absolute() != self.frontend_path.absolute()):
                    raise ValueError("activation FD must be the expected bound Unix stream socket")
                # Darwin rejects getsockopt(SO_ACCEPTCONN). launch_activate_socket's
                # passive service supplies the listener; reject a connected endpoint.
                try:
                    listener.getpeername()
                except OSError as exc:
                    if exc.errno != errno.ENOTCONN:
                        raise
                else:
                    raise ValueError("activation FD must not be an accepted connection")
            except BaseException:
                listener.close()
                raise
        else:
            if self.frontend_path.exists():
                raise FileExistsError(f"frontend socket already exists: {self.frontend_path}")
            self.frontend_path.parent.mkdir(parents=True, exist_ok=True)
            self._parent_fd = os.open(self.frontend_path.parent, os.O_RDONLY)
            listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        try:
            if inherited_listener is None:
                listener.bind(str(self.frontend_path))
                self._bind_stat = os.stat(
                    self.frontend_path.name, dir_fd=self._parent_fd, follow_symlinks=False)
                self._socket_stat = self._bind_stat
                listener.listen(socket.SOMAXCONN)
                os.chmod(self.frontend_path, 0o600)
                self._socket_stat = os.stat(
                    self.frontend_path.name, dir_fd=self._parent_fd, follow_symlinks=False)
            listener.setblocking(False)
            self._server = await asyncio.start_unix_server(
                self._accept,
                sock=listener,
                start_serving=False,
                cleanup_socket=False,
            )
        except BaseException:
            listener.close()
            self._unlink_owned_socket()
            self._close_parent_fd()
            raise
        try:
            await self._server.start_serving()
        except BaseException:
            self._server.close()
            await self._server.wait_closed()
            self._server = None
            self._unlink_owned_socket()
            self._close_parent_fd()
            raise
        self._closed = False
        return self

    async def close(self) -> None:
        self._closed = True
        server = self._server
        self._server = None
        if server is not None:
            server.close()

        writers = [writer for pair in self._connections.values() for writer in pair]
        if writers:
            await self._close_connection_writers(*writers)

        tasks = list(self._accept_tasks | self._connection_tasks)
        for task in tasks:
            task.cancel()
        if tasks:
            await asyncio.gather(*tasks, return_exceptions=True)
        if server is not None:
            await server.wait_closed()
        self._accept_tasks.clear()
        self._connection_tasks.clear()
        self._connections.clear()
        self._unlink_owned_socket()
        self._close_parent_fd()

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
        if self._socket_stat is None or self._parent_fd is None:
            return
        try:
            current = os.stat(
                self.frontend_path.name,
                dir_fd=self._parent_fd,
                follow_symlinks=False,
            )
        except (FileNotFoundError, OSError):
            return
        if (current.st_dev, current.st_ino) != (
            self._socket_stat.st_dev,
            self._socket_stat.st_ino,
        ):
            return
        if current.st_uid != self._socket_stat.st_uid:
            return
        if (current.st_mode & 0o777) != (self._socket_stat.st_mode & 0o777):
            return
        if not stat_is_socket(current):
            return
        try:
            os.unlink(self.frontend_path.name, dir_fd=self._parent_fd)
        except (FileNotFoundError, OSError):
            pass

    def _close_parent_fd(self) -> None:
        if self._parent_fd is not None:
            os.close(self._parent_fd)
            self._parent_fd = None

    def _accept(self, reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
        if len(self._accept_tasks) >= self.max_connections:
            writer.close()
            return
        task = asyncio.create_task(self._handle_client(reader, writer))
        self._accept_tasks.add(task)
        task.add_done_callback(self._accept_tasks.discard)

    async def _handle_client(
        self, reader: asyncio.StreamReader, frontend_writer: asyncio.StreamWriter
    ) -> None:
        backend_writer: asyncio.StreamWriter | None = None
        connection_id: str | None = None
        epoch = 0
        pumps: list[asyncio.Task[None]] = []
        backend_scope = AsyncExitStack()
        first_data = b""
        deadline = asyncio.get_running_loop().time() + self.callback_timeout
        def remaining_start():
            timeout = deadline - asyncio.get_running_loop().time()
            if timeout <= 0:
                raise TimeoutError("activation frontend/backend start window elapsed")
            return timeout
        try:
            frontend_socket = frontend_writer.get_extra_info("socket")
            frontend_peer = peer_identity(frontend_socket)
            if not await self._is_authorized(frontend_peer):
                return

            if self.wait_for_frontend_data:
                # A native default-socket probe sends no application bytes.
                # Hold at most one bounded chunk; EOF must not create a backend.
                timeout = remaining_start()
                first_data = await asyncio.wait_for(reader.read(self.chunk_size), timeout)
                if not first_data:
                    self.empty_probe_eofs += 1
                    return

            backend_validator = self.authorize_backend_peer
            if self.backend_factory is None:
                backend_reader, backend_writer = await asyncio.wait_for(
                    asyncio.open_unix_connection(self.backend_path), self.callback_timeout)
            else:
                timeout = remaining_start() if self.wait_for_frontend_data else self.callback_timeout
                lease = await asyncio.wait_for(
                    backend_scope.enter_async_context(self.backend_factory(frontend_peer)), timeout)
                if not isinstance(lease, BackendConnection):
                    raise TypeError("backend factory did not provide an isolated connection")
                backend_reader, backend_writer = lease.reader, lease.writer
                backend_validator = lease.authorize_peer
            backend_socket = backend_writer.get_extra_info("socket")
            backend_peer = peer_identity(backend_socket)
            if not backend_peer.complete or not await self._is_backend_authorized(backend_peer, backend_validator):
                return

            self._epoch += 1
            epoch = self._epoch
            connection_id = f"conn-{epoch}-{uuid.uuid4().hex[:12]}"
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
            await self._emit("on_connect", event, connection_id, epoch)
            self._connections[connection_id] = (frontend_writer, backend_writer)
            wire_gates = self.wire_gate_factory(frontend_peer, backend_peer, connection_id, epoch) if self.wire_gate_factory is not None else None
            if wire_gates is None:
                frontend_gate = backend_gate = None
            else:
                frontend_gate, backend_gate = wire_gates

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
                        frontend_gate,
                        initial_data=first_data,
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
                        backend_gate,
                    )
                ),
            ]
            self._connection_tasks.update(pumps)
            done, pending = await asyncio.wait(
                pumps, return_when=asyncio.FIRST_COMPLETED if self.stop_on_frontend_eof else asyncio.FIRST_EXCEPTION
            )
            failures = [task.exception() for task in done if not task.cancelled()]
            if self.stop_on_frontend_eof or any(isinstance(result, _CaptureFailure) for result in failures):
                for task in pending:
                    task.cancel()
                await asyncio.gather(*pending, return_exceptions=True)
            else:
                await asyncio.gather(*pending, return_exceptions=True)
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
        except _CaptureFailure:
            pass
        except (OSError, TimeoutError, ValueError, TypeError):
            if self.backend_factory is None:
                raise
            await self._emit("on_gap", GapEvent(self._next_seq(), connection_id or "pending", epoch,
                "isolated_backend_or_relay_failed", False), connection_id or "pending", epoch)
        finally:
            for task in pumps:
                if not task.done():
                    task.cancel()
            if pumps:
                await asyncio.gather(*pumps, return_exceptions=True)
            if connection_id is not None:
                self._connections.pop(connection_id, None)
            for task in pumps:
                self._connection_tasks.discard(task)
            await self._close_connection_writers(frontend_writer, backend_writer)
            await backend_scope.aclose()

    async def _close_connection_writers(self, *writers):
        if not self.stop_on_frontend_eof:
            await _close_writers(*writers)
            return
        try:
            await asyncio.wait_for(_close_writers(*writers), min(1.0, self.callback_timeout))
        except TimeoutError:
            for writer in writers:
                if writer is not None:
                    writer.transport.abort()

    async def _pump(
        self,
        reader: asyncio.StreamReader,
        destination: asyncio.StreamWriter,
        connection_id: str,
        epoch: int,
        direction: Direction,
        direction_seq: dict[Direction, int],
        wire_gate: WireGate | None = None,
        *,
        initial_data: bytes = b"",
    ) -> None:
        try:
            while True:
                data = initial_data or await reader.read(self.chunk_size)
                initial_data = b""
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
                forwarded = data
                if wire_gate is not None:
                    forwarded = wire_gate(data)
                    if inspect.isawaitable(forwarded):
                        forwarded = await asyncio.wait_for(forwarded, self.callback_timeout)
                    if not isinstance(forwarded, bytes):
                        raise ValueError("wire gate must return bytes")
                if wire_gate is None or forwarded:
                    direction_seq[direction] += 1
                    await self._emit(
                        "on_data",
                        DataEvent(
                            observation_seq=self._next_seq(),
                            connection_id=connection_id,
                            epoch=epoch,
                            direction=direction,
                            direction_seq=direction_seq[direction],
                            data=forwarded if wire_gate is not None else data,
                        ),
                        connection_id,
                        epoch,
                    )
                if forwarded:
                    destination.write(forwarded)
                    await destination.drain()
        except _CaptureFailure:
            raise
        except (ConnectionError, asyncio.IncompleteReadError, OSError):
            return

    async def _is_authorized(self, peer: PeerIdentity) -> bool:
        if not peer.complete:
            return False
        if self.authorize_peer is None:
            return False
        result = self.authorize_peer(peer)
        if inspect.isawaitable(result):
            result = await asyncio.wait_for(result, self.callback_timeout)
        return bool(result)

    async def _is_backend_authorized(self, peer: PeerIdentity, validator: BackendPeerValidator | None = None) -> bool:
        validator = validator if validator is not None else self.authorize_backend_peer
        if validator is None:
            return True
        result = validator(peer)
        if inspect.isawaitable(result):
            result = await asyncio.wait_for(result, self.callback_timeout)
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
                await asyncio.wait_for(result, self.callback_timeout)
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


async def _close_writers(*writers: asyncio.StreamWriter | None) -> None:
    present = [writer for writer in writers if writer is not None]
    for writer in present:
        writer.close()
    await asyncio.gather(*(_wait_closed(writer) for writer in present), return_exceptions=True)


__all__ = [
    "BackendConnection",
    "CaptureSink",
    "ConnectionEvent",
    "DataEvent",
    "GapEvent",
    "LifecycleEvent",
    "PeerIdentity",
    "ProxyServer",
    "peer_identity",
]
