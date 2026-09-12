import asyncio
import os
import socket
import sys
import uuid
from dataclasses import dataclass, field
from pathlib import Path

import pytest

SCRIPT_DIR = Path(__file__).parents[1] / "scripts"
sys.path.insert(0, str(SCRIPT_DIR))

import proxy_transport as transport_module  # noqa: E402
from proxy_transport import (  # noqa: E402
    CaptureSink,
    DataEvent,
    GapEvent,
    LifecycleEvent,
    PeerIdentity,
    ProxyServer,
)


def async_test(function):
    def wrapper(*args, **kwargs):
        return asyncio.run(function(*args, **kwargs))

    wrapper.__signature__ = __import__("inspect").signature(function)
    return wrapper


@dataclass
class RecordingSink(CaptureSink):
    connects: list = field(default_factory=list)
    data: list[DataEvent] = field(default_factory=list)
    lifecycle: list[LifecycleEvent] = field(default_factory=list)
    gaps: list[GapEvent] = field(default_factory=list)

    def on_connect(self, event):
        self.connects.append(event)

    def on_data(self, event):
        self.data.append(event)

    def on_lifecycle(self, event):
        self.lifecycle.append(event)

    def on_gap(self, event):
        self.gaps.append(event)


@dataclass
class BackendRecorder:
    received: list[tuple[int, bytes]] = field(default_factory=list)
    peers: list[PeerIdentity] = field(default_factory=list)
    _next_id: int = 0
    async def handler(self, reader, writer):
        connection_id = self._next_id
        self._next_id += 1
        from proxy_transport import peer_identity

        self.peers.append(peer_identity(writer.get_extra_info("socket")))
        body = await reader.read()
        self.received.append((connection_id, body))
        writer.write(b"reply:" + body)
        await writer.drain()
        writer.close()
        await writer.wait_closed()


def socket_pair():
    base = Path("/tmp") / f"codex-proxy-{uuid.uuid4().hex[:10]}"
    base.mkdir()
    return base / "frontend.sock", base / "backend.sock"


async def _serve_backend(path: Path, recorder: BackendRecorder):
    server = await asyncio.start_unix_server(recorder.handler, path=str(path))
    return server


@async_test
async def test_each_frontend_connection_is_a_byte_exact_independent_relay(tmp_path):
    frontend_path, backend_path = socket_pair()
    recorder = BackendRecorder()
    backend = await _serve_backend(backend_path, recorder)
    sink = RecordingSink()
    proxy = ProxyServer(
        frontend_path,
        backend_path,
        sink,
        authorize_peer=lambda peer: peer.pid == os.getpid(),
        chunk_size=7,
    )
    await proxy.start()
    try:
        left = await asyncio.open_unix_connection(frontend_path)
        right = await asyncio.open_unix_connection(frontend_path)
        left[1].write(b"left\x00binary")
        right[1].write(b"right\xffbinary")
        await asyncio.gather(left[1].drain(), right[1].drain())
        left[1].write_eof()
        right[1].write_eof()
        replies = await asyncio.gather(left[0].read(), right[0].read())
        assert sorted(replies) == [b"reply:left\x00binary", b"reply:right\xffbinary"]
        left[1].close()
        right[1].close()
        await asyncio.gather(left[1].wait_closed(), right[1].wait_closed())
        await asyncio.sleep(0.1)
    finally:
        await proxy.close()
        backend.close()
        await backend.wait_closed()

    assert sorted(body for _, body in recorder.received) == [b"left\x00binary", b"right\xffbinary"]
    assert len(sink.connects) == 2
    assert len({event.connection_id for event in sink.connects}) == 2
    assert [event.direction_seq for event in sink.data] == sorted(
        event.direction_seq for event in sink.data
    ) or sink.data
    all_events = sink.connects + sink.data + sink.lifecycle
    assert sorted(event.observation_seq for event in all_events) == list(
        range(1, len(all_events) + 1)
    )
    by_connection = {}
    for event in sink.data:
        by_connection.setdefault((event.connection_id, event.direction), bytearray()).extend(event.data)
    assert sorted(by_connection.values()) == sorted(
        [bytearray(b"left\x00binary"), bytearray(b"right\xffbinary"),
         bytearray(b"reply:left\x00binary"), bytearray(b"reply:right\xffbinary")]
    )
    assert all(event.frontend_peer.pid == os.getpid() for event in sink.connects)
    assert all(event.backend_peer is not None for event in sink.connects)
    assert all(event.backend_fd >= 0 for event in sink.connects)


@async_test
async def test_probe_that_eof_is_not_promoted_to_authenticated_tui(tmp_path):
    frontend_path, backend_path = socket_pair()

    async def no_response(reader, writer):
        await reader.read()
        writer.close()
        await writer.wait_closed()

    backend = await asyncio.start_unix_server(no_response, path=str(backend_path))
    sink = RecordingSink()
    proxy = ProxyServer(frontend_path, backend_path, sink, authorize_peer=lambda _: True)
    await proxy.start()
    try:
        reader, writer = await asyncio.open_unix_connection(frontend_path)
        await asyncio.sleep(0.1)
        writer.close()
        await writer.wait_closed()
        for _ in range(100):
            if sink.lifecycle:
                break
            await asyncio.sleep(0.01)
        assert await reader.read() == b""
    finally:
        await proxy.close()
        backend.close()
        await backend.wait_closed()

    assert sink.data == []
    assert sink.gaps == []
    assert any(event.kind == "eof" for event in sink.lifecycle)
    # Peer authorization is transport admission only; no TUI authentication is
    # inferred from a connection that produced no handshake/data.
    assert all(event.authenticated is True for event in sink.connects)


@async_test
async def test_unknown_peer_is_rejected_before_backend_and_capture(tmp_path):
    frontend_path, backend_path = socket_pair()
    recorder = BackendRecorder()
    backend = await _serve_backend(backend_path, recorder)
    sink = RecordingSink()
    proxy = ProxyServer(frontend_path, backend_path, sink, authorize_peer=lambda _: False)
    await proxy.start()
    try:
        reader, writer = await asyncio.open_unix_connection(frontend_path)
        writer.write(b"secret")
        await writer.drain()
        assert await reader.read() == b""
        writer.close()
        await writer.wait_closed()
        await asyncio.sleep(0.05)
    finally:
        await proxy.close()
        backend.close()
        await backend.wait_closed()

    assert all(body == b"" for _, body in recorder.received)
    assert sink.connects == []
    assert sink.data == []


@async_test
async def test_capture_exception_emits_gap_and_stops_forwarding(tmp_path):
    frontend_path, backend_path = socket_pair()
    recorder = BackendRecorder()
    backend = await _serve_backend(backend_path, recorder)

    class FailingSink(RecordingSink):
        def on_data(self, event):
            raise RuntimeError("observer unavailable")

    sink = FailingSink()
    proxy = ProxyServer(frontend_path, backend_path, sink, authorize_peer=lambda _: True)
    await proxy.start()
    try:
        reader, writer = await asyncio.open_unix_connection(frontend_path)
        writer.write(b"must-not-be-silent")
        await writer.drain()
        assert await reader.read() == b""
        writer.close()
        await writer.wait_closed()
        await asyncio.sleep(0.05)
    finally:
        await proxy.close()
        backend.close()
        await backend.wait_closed()

    assert all(body == b"" for _, body in recorder.received)
    assert len(sink.gaps) == 1
    assert sink.gaps[0].reason == "capture_callback_failed"
    assert sink.gaps[0].fatal is True


@async_test
async def test_backend_can_finish_after_frontend_half_close(tmp_path):
    frontend_path, backend_path = socket_pair()

    async def backend_handler(reader, writer):
        assert await reader.read() == b"request"
        writer.write(b"response-after-eof")
        await writer.drain()
        writer.close()
        await writer.wait_closed()

    backend = await asyncio.start_unix_server(backend_handler, path=str(backend_path))
    sink = RecordingSink()
    proxy = ProxyServer(frontend_path, backend_path, sink, authorize_peer=lambda _: True)
    await proxy.start()
    try:
        reader, writer = await asyncio.open_unix_connection(frontend_path)
        writer.write(b"request")
        await writer.drain()
        writer.write_eof()
        assert await reader.read() == b"response-after-eof"
        writer.close()
        await writer.wait_closed()
        await asyncio.sleep(0.02)
    finally:
        await proxy.close()
        backend.close()
        await backend.wait_closed()

    assert [event.data for event in sink.data] == [b"request", b"response-after-eof"]
    assert {event.kind for event in sink.lifecycle} >= {"eof", "disconnect"}


@async_test
async def test_capture_is_bounded_to_configured_chunk_size(tmp_path):
    frontend_path, backend_path = socket_pair()

    async def backend_handler(reader, writer):
        await reader.read()
        writer.close()
        await writer.wait_closed()

    backend = await asyncio.start_unix_server(backend_handler, path=str(backend_path))
    sink = RecordingSink()
    proxy = ProxyServer(
        frontend_path,
        backend_path,
        sink,
        authorize_peer=lambda _: True,
        chunk_size=4,
    )
    await proxy.start()
    try:
        reader, writer = await asyncio.open_unix_connection(frontend_path)
        writer.write(b"0123456789")
        await writer.drain()
        writer.write_eof()
        await reader.read()
        writer.close()
        await writer.wait_closed()
        await asyncio.sleep(0.02)
    finally:
        await proxy.close()
        backend.close()
        await backend.wait_closed()

    assert b"".join(event.data for event in sink.data if event.direction == "frontend_to_backend") == b"0123456789"
    assert all(len(event.data) <= 4 for event in sink.data)


@async_test
async def test_listener_bind_refuses_existing_socket_and_keeps_owner(tmp_path):
    frontend_path, backend_path = socket_pair()
    owner = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    owner.bind(str(frontend_path))
    owner.listen(1)
    original = frontend_path.stat()
    proxy = ProxyServer(frontend_path, backend_path, RecordingSink(), authorize_peer=lambda _: True)
    with pytest.raises(OSError):
        await proxy.start()
    current = frontend_path.stat()
    assert (current.st_dev, current.st_ino) == (original.st_dev, original.st_ino)
    owner.close()
    frontend_path.unlink()


@async_test
async def test_owned_socket_is_private_and_disconnect_keeps_listener(tmp_path):
    frontend_path, backend_path = socket_pair()

    async def backend_handler(reader, writer):
        await reader.read()
        writer.close()
        await writer.wait_closed()

    backend = await asyncio.start_unix_server(backend_handler, path=str(backend_path))
    sink = RecordingSink()
    proxy = ProxyServer(frontend_path, backend_path, sink, authorize_peer=lambda _: True)
    await proxy.start()
    try:
        assert frontend_path.stat().st_mode & 0o777 == 0o600
        reader, writer = await asyncio.open_unix_connection(frontend_path)
        await asyncio.sleep(0.1)
        assert sink.connects
        connection_id = sink.connects[-1].connection_id
        assert await proxy.disconnect(connection_id, leg="frontend") is True
        assert await reader.read() == b""
        writer.close()
        await writer.wait_closed()
        second_reader, second_writer = await asyncio.open_unix_connection(frontend_path)
        second_writer.close()
        await second_writer.wait_closed()
        await second_reader.read()
    finally:
        await proxy.close()
        backend.close()
        await backend.wait_closed()


@async_test
async def test_close_cancels_hanging_connect_capture_and_closes_client(tmp_path):
    frontend_path, backend_path = socket_pair()
    backend = await asyncio.start_unix_server(
        lambda reader, writer: asyncio.sleep(30), path=str(backend_path)
    )

    class HangingSink(RecordingSink):
        async def on_connect(self, event):
            await asyncio.sleep(30)

    proxy = ProxyServer(
        frontend_path,
        backend_path,
        HangingSink(),
        authorize_peer=lambda _: True,
        callback_timeout=0.05,
    )
    await proxy.start()
    reader, writer = await asyncio.open_unix_connection(frontend_path)
    await asyncio.sleep(0.1)
    await asyncio.wait_for(proxy.close(), timeout=1)
    writer.close()
    await writer.wait_closed()
    assert await reader.read() == b""
    backend.close()
    await backend.wait_closed()


@async_test
async def test_incomplete_frontend_identity_never_connects_backend(tmp_path, monkeypatch):
    frontend_path, backend_path = socket_pair()
    recorder = BackendRecorder()
    backend = await _serve_backend(backend_path, recorder)
    sink = RecordingSink()
    incomplete = PeerIdentity(
        pid=os.getpid(), uid=os.getuid(), birth=None, executable=None,
        pid_available=True, uid_available=True,
        birth_available=False, executable_available=False, source="test",
    )
    monkeypatch.setattr(transport_module, "peer_identity", lambda _: incomplete)
    proxy = ProxyServer(frontend_path, backend_path, sink, authorize_peer=lambda _: True)
    await proxy.start()
    try:
        reader, writer = await asyncio.open_unix_connection(frontend_path)
        writer.write(b"must-not-forward")
        await writer.drain()
        assert await reader.read() == b""
        writer.close()
        await writer.wait_closed()
        await asyncio.sleep(0.05)
    finally:
        await proxy.close()
        backend.close()
        await backend.wait_closed()
    assert recorder.received == []
    assert sink.connects == []


@async_test
async def test_backend_identity_validator_controls_admission(tmp_path):
    frontend_path, backend_path = socket_pair()
    recorder = BackendRecorder()
    backend = await _serve_backend(backend_path, recorder)
    sink = RecordingSink()
    proxy = ProxyServer(
        frontend_path,
        backend_path,
        sink,
        authorize_peer=lambda _: True,
        authorize_backend_peer=lambda _: False,
    )
    await proxy.start()
    try:
        reader, writer = await asyncio.open_unix_connection(frontend_path)
        await asyncio.sleep(0.1)
        assert await reader.read() == b""
        writer.close()
        await writer.wait_closed()
        await asyncio.sleep(0.05)
    finally:
        await proxy.close()
        backend.close()
        await backend.wait_closed()
    assert sink.connects == []
    assert all(body == b"" for _, body in recorder.received)


@async_test
async def test_external_mode_change_prevents_owned_socket_cleanup(tmp_path):
    frontend_path, backend_path = socket_pair()
    proxy = ProxyServer(frontend_path, backend_path, RecordingSink(), authorize_peer=lambda _: True)
    await proxy.start()
    os.chmod(frontend_path, 0o666)
    await proxy.close()
    assert frontend_path.exists()
    frontend_path.unlink()
