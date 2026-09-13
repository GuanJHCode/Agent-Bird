import asyncio
import base64
import hashlib
import json
import os
from pathlib import Path
import sys
from types import SimpleNamespace

import pytest

ROOT = Path(__file__).resolve().parents[2]
SERVICE_DIR = ROOT / "g0-proxy-continuation" / "cold-start" / "scripts"
HELPER_DIR = ROOT / "g0-completion" / "scripts"
sys.path.insert(0, str(SERVICE_DIR))
sys.path.insert(0, str(HELPER_DIR))

import activation_service  # noqa: E402
from owner_helper import HelperIdentity, OwnerHelperAdmissionError, OwnerHelperGrant, OwnerLease  # noqa: E402
from proxy_transport import PeerIdentity, _process_metadata  # noqa: E402


async def _headers(reader):
    return await reader.readuntil(b"\r\n\r\n")


def _server_frame(payload: bytes):
    if len(payload) >= 126:
        raise AssertionError("test payload too large")
    return bytes((0x81, len(payload))) + payload


async def _open_ws(path):
    reader, writer = await asyncio.open_unix_connection(path)
    writer.write(b"GET / HTTP/1.1\r\nHost: synthetic\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGVzdC1vd25lcg==\r\n\r\n")
    await writer.drain()
    response=await _headers(reader)
    assert response.startswith(b"HTTP/1.1 101")
    return reader, writer


async def _backend_server(path):
    state = {"handshakes": 0, "writers": []}

    async def handle(reader, writer):
        try:
            req = await _headers(reader)
            key = next(line.split(b":", 1)[1].strip() for line in req.split(b"\r\n") if line.lower().startswith(b"sec-websocket-key:"))
            accept = base64.b64encode(hashlib.sha1(key + b"258EAFA5-E914-47DA-95CA-C5AB0DC85B11").digest())
            writer.write(b"HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + b"\r\n\r\n")
            await writer.drain()
            state["handshakes"] += 1
            state["writers"].append(writer)
            while True:
                first, second = await reader.readexactly(2)
                if first & 0x70 or not first & 0x80 or first & 0x0F != 1 or not second & 0x80:
                    raise AssertionError("unexpected helper frame")
                length = second & 0x7F
                if length == 126:
                    length = int.from_bytes(await reader.readexactly(2), "big")
                mask = await reader.readexactly(4)
                payload = await reader.readexactly(length)
                payload = bytes(byte ^ mask[i % 4] for i, byte in enumerate(payload))
                message = json.loads(payload)
                state.setdefault("requests", []).append(message)
                response = {"id": message["id"], "result": {"process": {"id": 20001}}}
                writer.write(_server_frame(json.dumps(response, separators=(",", ":")).encode()))
                await writer.drain()
        except (asyncio.IncompleteReadError, ConnectionError):
            pass
        finally:
            if writer in state["writers"]:
                state["writers"].remove(writer)
            writer.close()
            await writer.wait_closed()

    server = await asyncio.start_unix_server(handle, path=str(path))
    return server, state


def _peer_for_current_process():
    pid = os.getpid()
    birth, executable = _process_metadata(pid)
    return PeerIdentity(pid=pid, uid=os.getuid(), birth=birth, executable=executable,
        pid_available=True, uid_available=True, birth_available=birth is not None,
        executable_available=executable is not None, source="synthetic")


def _service_for(path: Path, lease: OwnerLease, record: dict):
    service = object.__new__(activation_service.ActivationService)
    service.receipt_dir = path.parent / f".{path.stem}-receipts"
    service.receipt_dir.mkdir(mode=0o700)
    service._directory_pins = {service.receipt_dir: activation_service._private_directory(service.receipt_dir)}
    service.activation_id = "synthetic-activation"
    service.manifest_sha256 = "e" * 64
    peer = _peer_for_current_process()
    service._service_identity = {
        "pid": peer.pid, "uid": peer.uid, "birth": peer.birth,
        "executable": peer.executable, "executable_sha256": activation_service._sha(Path(peer.executable)),
    }
    service._owner_leases = {lease.lease_id: lease}
    service.backend_records = [record]
    service._helper_admissions = {}
    service.contexts = {"profile-synthetic": object()}
    service._helper_pins = {}
    service._config_pins = {}
    service._check_runtime_policy = lambda: None
    service.executable = Path(_peer_for_current_process().executable)
    return service


def _grant(path: Path, identity: HelperIdentity, *, lease_id="lease-synthetic", owner_thread_id="01aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"):
    return {
        "version": 1,
        "role": "owner-helper",
        "profile_id": "profile-synthetic",
        "owner_context_sha256": "a" * 64,
        "lease_id": lease_id,
        "owner_connection_id": "owner-connection",
        "owner_epoch": 7,
        "owner_thread_id": owner_thread_id,
        "private_socket": str(path),
        "helper_pid": identity.pid,
        "helper_uid": identity.uid,
        "helper_birth": identity.birth,
        "helper_executable": identity.executable,
        "helper_executable_sha256": identity.executable_sha256,
        "helper_source_sha256": identity.source_sha256,
    }


def test_activation_service_helper_branch_rejects_without_active_owner(tmp_path):
    path = Path("/tmp") / f"oh-service-negative-{os.getpid()}.sock"
    identity = HelperIdentity(21001, os.getuid(), "helper-birth", "/synthetic/helper", "b" * 64, "c" * 64)
    peer = SimpleNamespace(pid=identity.pid, uid=identity.uid, birth=identity.birth, executable=identity.executable, complete=True)
    service = object.__new__(activation_service.ActivationService)
    service._owner_leases = {}
    service.backend_records = []
    service._helper_admissions = {}
    service.contexts = {}
    service._helper_pins = {"executable": identity.executable, "executable_sha256": identity.executable_sha256, "source_path": identity.executable, "source_sha256": identity.source_sha256}
    with pytest.MonkeyPatch.context() as patch:
        patch.setattr(activation_service, "_process_metadata", lambda pid: (identity.birth, identity.executable))
        patch.setattr(activation_service, "_sha", lambda path: identity.executable_sha256)
        assert service._helper_admission(peer, _grant(path, identity), "d" * 64) is False


def test_activation_service_helper_branch_reuses_existing_listener(tmp_path):
    async def run():
        path = Path("/private/tmp") / f"oh-service-positive-{os.getpid()}.sock"
        server, state = await _backend_server(path)
        backend_peer = _peer_for_current_process()
        owner = OwnerLease(
            profile_id="profile-synthetic", owner_context_sha256="a" * 64,
            lease_id="lease-synthetic", owner_connection_id="owner-connection", owner_epoch=7,
            owner_thread_id="01aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", private_socket=str(path),
            backend_pid=backend_peer.pid, backend_birth=backend_peer.birth, helper_identity=None,
        )
        record = {
            "lease_id": owner.lease_id, "profile_id": owner.profile_id, "state": "connected",
            "private_socket": str(path), "private_socket_identity": list((path.stat().st_dev, path.stat().st_ino, path.stat().st_uid, path.stat().st_mode)),
            "pid": backend_peer.pid, "birth": backend_peer.birth, "owner_connection_id": owner.owner_connection_id,
            "owner_epoch": owner.owner_epoch, "owner_thread_id": owner.owner_thread_id,
        }
        service = _service_for(path, owner, record)
        service._owner_context_sha = lambda context: owner.owner_context_sha256
        identity = HelperIdentity(21001, os.getuid(), "helper-birth", "/synthetic/helper", "b" * 64, "c" * 64)
        grant = _grant(path, identity)
        frontend = SimpleNamespace(pid=identity.pid, uid=identity.uid, birth=identity.birth, executable=identity.executable, complete=True)
        service._admit = lambda peer: True
        key = (frontend.pid, frontend.birth, frontend.executable)
        service._helper_admissions[key] = (grant, "d" * 64)
        try:
            async with service._backend(frontend) as connection:
                # Service returns raw transport; ProxyServer owns the single WS upgrade.
                assert state["handshakes"] == 0
                assert connection.reader is not None and connection.writer is not None
        finally:
            await owner.close()
            server.close()
            await server.wait_closed()
            for child in service.receipt_dir.iterdir():
                child.unlink()
            service.receipt_dir.rmdir()
            path.unlink(missing_ok=True)

    asyncio.run(run())


def test_proxy_service_wire_gate_relays_real_two_hop_websocket(tmp_path):
    async def run():
        frontend_path = Path("/tmp") / f"oh-public-{os.getpid()}.sock"
        backend_path = Path("/tmp") / f"oh-backend-{os.getpid()}.sock"
        backend, backend_state = await _backend_server(backend_path)
        from owner_helper import NativeWebSocketGate, OwnerHelperConnection
        from proxy_transport import CaptureSink, ProxyServer

        owner_thread = "01aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
        def gates(_frontend, _backend, _connection, _epoch):
            pending = set()
            return NativeWebSocketGate(owner_thread, server=False, pending_ids=pending), NativeWebSocketGate(owner_thread, server=True, pending_ids=pending)

        class Sink(CaptureSink):
            pass
        proxy = ProxyServer(frontend_path, backend_path, Sink(), authorize_peer=lambda peer: peer.pid == os.getpid(), authorize_backend_peer=lambda peer: True, wire_gate_factory=gates)
        await proxy.start()
        try:
            reader, writer = await asyncio.wait_for(_open_ws(frontend_path), 2)
            helper = OwnerHelperConnection(reader, writer, "lease-synthetic", owner_thread)
            request = {"id": 1, "method": "server/diagnostics", "params": {}}
            await asyncio.wait_for(helper.send_rpc(request), 2)
            assert await asyncio.wait_for(helper.read_rpc(), 2) == {"id": 1, "result": {"process": {"id": 20001}}}
            assert backend_state["requests"] == [request]
            await helper.close()
        finally:
            await proxy.close()
            backend.close()
            await backend.wait_closed()

    asyncio.run(run())
