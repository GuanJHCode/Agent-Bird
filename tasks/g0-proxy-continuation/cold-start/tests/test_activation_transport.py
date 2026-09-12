"""Real inherited AF_UNIX sockets; no launchd, Codex or credentials."""
import asyncio
from contextlib import asynccontextmanager
import os
from pathlib import Path
import socket
import sys
import tempfile

import pytest

ROOT = Path(__file__).resolve().parents[4]
sys.path.insert(0, str(ROOT / 'tasks/g0-tui-proxy/scripts'))
import proxy_transport as transport


def run(coro):
    return asyncio.run(asyncio.wait_for(coro, 10))


@pytest.fixture
def local_socket_cwd(tmp_path, monkeypatch):
    # Explicitly authorized short, isolated OS-test root; code/reports stay in the WT.
    with tempfile.TemporaryDirectory(prefix='g0-auth-', dir='/private/tmp') as raw:
        monkeypatch.chdir(raw)
        yield Path(raw)


def listener():
    result = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    result.bind('public.sock')
    result.listen(8)
    return result


async def wait_for(predicate):
    async with asyncio.timeout(2):
        while not predicate():
            await asyncio.sleep(0.01)


def test_inherited_fd_is_used_without_rebinding_or_unlinking(local_socket_cwd):
    async def scenario():
        public = listener()
        original = os.stat('public.sock')
        async def echo(reader, writer):
            writer.write(await reader.read(20))
            await writer.drain()
            writer.close()
            await writer.wait_closed()
        backend = await asyncio.start_unix_server(echo, path='b.sock')
        proxy = transport.ProxyServer('public.sock', 'b.sock', transport.CaptureSink(),
                                      authorize_peer=lambda peer: peer.pid == os.getpid())
        duplicate = socket.socket(fileno=os.dup(public.fileno()))
        try:
            await proxy.start(inherited_listener=duplicate)
            reader, writer = await asyncio.open_unix_connection('public.sock')
            writer.write(b'wire\x00\xff'); await writer.drain()
            assert await reader.read(20) == b'wire\x00\xff'
            writer.close(); await writer.wait_closed()
        finally:
            await proxy.close(); backend.close(); await backend.wait_closed()
            public.close()
        after = os.stat('public.sock')
        assert (after.st_dev, after.st_ino, after.st_mode) == (original.st_dev, original.st_ino, original.st_mode)
        assert duplicate.fileno() == -1
    run(scenario())


def test_each_frontend_gets_own_context_and_eof_stops_only_its_backend(local_socket_cwd):
    async def scenario():
        public = listener(); started = []; closed = []
        @asynccontextmanager
        async def factory(frontend_peer):
            assert frontend_peer.pid == os.getpid()
            number = len(started); started.append(number)
            path = f'b{number}.sock'; writers = []
            async def echo(reader, writer):
                writers.append(writer)
                try:
                    while data := await reader.read(64):
                        writer.write(str(number).encode() + b':' + data); await writer.drain()
                    await asyncio.Future()  # A backend that outlives frontend EOF must be stopped.
                finally:
                    writer.close()
            backend = await asyncio.start_unix_server(echo, path=path)
            try:
                reader, writer = await asyncio.open_unix_connection(path)
                yield transport.BackendConnection(reader, writer, lambda peer: peer.pid == os.getpid())
            finally:
                backend.close()
                for writer in writers: writer.close()
                await backend.wait_closed()
                closed.append(number)
                os.unlink(path)
        proxy = transport.ProxyServer('public.sock', None, transport.CaptureSink(),
            authorize_peer=lambda peer: peer.pid == os.getpid(), backend_factory=factory,
            stop_on_frontend_eof=True)
        try:
            await proxy.start(inherited_listener=socket.socket(fileno=os.dup(public.fileno())))
            left = await asyncio.open_unix_connection('public.sock')
            right = await asyncio.open_unix_connection('public.sock')
            left[1].write(b'left'); right[1].write(b'right')
            await asyncio.gather(left[1].drain(), right[1].drain())
            assert await left[0].read(64) == b'0:left'
            assert await right[0].read(64) == b'1:right'
            left[1].close(); await left[1].wait_closed(); await wait_for(lambda: closed == [0])
            right[1].write(b'alive'); await right[1].drain()
            assert await right[0].read(64) == b'1:alive'
            right[1].close(); await right[1].wait_closed(); await wait_for(lambda: closed == [0, 1])
        finally:
            await proxy.close(); public.close()
        assert os.path.exists('public.sock') and len(started) == 2
    run(scenario())


def test_backend_start_failure_closes_frontend_and_context_not_public_socket(local_socket_cwd):
    async def scenario():
        public = listener(); cleanup = []
        @asynccontextmanager
        async def failure(peer):
            try:
                raise OSError('synthetic startup failure')
                yield
            finally:
                cleanup.append(peer.pid)
        proxy = transport.ProxyServer('public.sock', None, transport.CaptureSink(),
            authorize_peer=lambda peer: peer.pid == os.getpid(), backend_factory=failure,
            stop_on_frontend_eof=True)
        try:
            await proxy.start(inherited_listener=socket.socket(fileno=os.dup(public.fileno())))
            reader, writer = await asyncio.open_unix_connection('public.sock')
            assert await reader.read(64) == b''
            writer.close(); await writer.wait_closed()
            await wait_for(lambda: bool(cleanup))
        finally:
            await proxy.close(); public.close()
        assert cleanup == [os.getpid()] and os.path.exists('public.sock')
    run(scenario())


def test_unregistered_frontend_never_creates_backend(local_socket_cwd):
    async def scenario():
        public = listener(); started = []
        @asynccontextmanager
        async def forbidden(peer):
            started.append(peer.pid)
            pytest.fail('backend must not be created before explicit peer admission')
            yield
        proxy = transport.ProxyServer('public.sock', None, transport.CaptureSink(),
            authorize_peer=lambda peer: False, backend_factory=forbidden, stop_on_frontend_eof=True)
        try:
            await proxy.start(inherited_listener=socket.socket(fileno=os.dup(public.fileno())))
            reader, writer = await asyncio.open_unix_connection('public.sock')
            assert await reader.read(64) == b''
            writer.close(); await writer.wait_closed()
        finally:
            await proxy.close(); public.close()
        assert started == []
    run(scenario())
