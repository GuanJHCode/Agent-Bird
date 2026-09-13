import asyncio
import base64
import hashlib
import json
import os
import sys
from pathlib import Path

import pytest

SCRIPT_DIR = Path(__file__).resolve().parents[1] / "scripts"
sys.path.insert(0, str(SCRIPT_DIR))

from owner_helper import (  # noqa: E402
    HelperIdentity,
    OwnerHelperAdmissionError,
    OwnerHelperGrant,
    OwnerLease,
    validate_helper_rpc,
    validate_helper_server_message,
)


OWNER = {
    "profile_id": "profile-synthetic",
    "owner_context_sha256": "a" * 64,
    "lease_id": "lease-synthetic",
    "owner_connection_id": "owner-connection",
    "owner_epoch": 7,
    "owner_thread_id": "01aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "private_socket": "",
}
HELPER = HelperIdentity(
    pid=21001,
    uid=501,
    birth="synthetic-helper-birth",
    executable="/synthetic/bin/helper",
    executable_sha256="b" * 64,
    source_sha256="c" * 64,
)


def socket_path(tmp_path: Path) -> Path:
    return Path("/tmp") / f"oh-{os.getpid()}-{tmp_path.name[:18]}.sock"


def grant_for(path: Path, **changes) -> OwnerHelperGrant:
    values = {
        **OWNER,
        "private_socket": str(path),
        "helper_pid": HELPER.pid,
        "helper_uid": HELPER.uid,
        "helper_birth": HELPER.birth,
        "helper_executable": HELPER.executable,
        "helper_executable_sha256": HELPER.executable_sha256,
        "helper_source_sha256": HELPER.source_sha256,
        "role": "owner-helper",
    }
    values.update(changes)
    return OwnerHelperGrant(**values)


async def _read_http_headers(reader):
    return await reader.readuntil(b"\r\n\r\n")


async def _ws_server(path: Path):
    state = {"connections": [], "handshakes": 0}

    async def handle(reader, writer):
        try:
            request = await _read_http_headers(reader)
            assert b"Upgrade: websocket" in request
            key = next(
                line.split(b":", 1)[1].strip()
                for line in request.split(b"\r\n")
                if line.lower().startswith(b"sec-websocket-key:")
            )
            accept = base64.b64encode(
                hashlib.sha1(
                    key + b"258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
                ).digest()
            )
            writer.write(
                b"HTTP/1.1 101 Switching Protocols\r\n"
                b"Upgrade: websocket\r\nConnection: Upgrade\r\n"
                b"Sec-WebSocket-Accept: "
                + accept
                + b"\r\n\r\n"
            )
            await writer.drain()
            state["connections"].append(writer)
            state["handshakes"] += 1
            await reader.read()
        finally:
            if writer in state["connections"]:
                state["connections"].remove(writer)
            writer.close()
            await writer.wait_closed()

    server = await asyncio.start_unix_server(handle, path=str(path))
    return server, state


async def _open_owner(path: Path):
    reader, writer = await asyncio.open_unix_connection(path)
    request = (
        b"GET / HTTP/1.1\r\nHost: synthetic\r\nUpgrade: websocket\r\n"
        b"Connection: Upgrade\r\nSec-WebSocket-Version: 13\r\n"
        b"Sec-WebSocket-Key: dGVzdC1vd25lcg==\r\n\r\n"
    )
    writer.write(request)
    await writer.drain()
    response = await _read_http_headers(reader)
    assert response.startswith(b"HTTP/1.1 101")
    return reader, writer


@pytest.mark.parametrize("field", [
    "profile_id", "owner_context_sha256", "lease_id", "owner_connection_id",
    "owner_epoch", "owner_thread_id", "private_socket", "helper_source_sha256",
])
def test_grant_must_match_active_owner_lease(tmp_path, field):
    async def run():
        path = socket_path(tmp_path)
        lease = OwnerLease(**{**OWNER, "private_socket": str(path)}, backend_pid=20001, backend_birth="backend-birth", helper_identity=HELPER)
        bad = grant_for(path, **{field: ("wrong" if field != "owner_epoch" else 8)})
        with pytest.raises(OwnerHelperAdmissionError):
            lease.validate_grant(bad, HELPER)

    asyncio.run(run())


def test_valid_grant_opens_second_websocket_without_spawning_backend(tmp_path):
    async def run():
        path = socket_path(tmp_path)
        server, state = await _ws_server(path)
        owner_reader, owner_writer = await _open_owner(path)
        lease = OwnerLease(**{**OWNER, "private_socket": str(path)}, backend_pid=20001, backend_birth="backend-birth", helper_identity=HELPER)
        lease.register_owner_connection(owner_writer)
        helper = await lease.open_helper(grant_for(path), HELPER)
        assert state["handshakes"] == 2
        assert helper.role == "owner-helper"
        assert helper.owner_thread_id == OWNER["owner_thread_id"]
        await helper.close()
        assert not owner_writer.is_closing()
        owner_writer.close()
        await owner_writer.wait_closed()
        await lease.close()
        server.close()
        await server.wait_closed()

    asyncio.run(run())


def test_owner_close_closes_helpers_and_rejects_new_helper(tmp_path):
    async def run():
        path = socket_path(tmp_path)
        server, state = await _ws_server(path)
        owner_reader, owner_writer = await _open_owner(path)
        lease = OwnerLease(**{**OWNER, "private_socket": str(path)}, backend_pid=20001, backend_birth="backend-birth", helper_identity=HELPER)
        lease.register_owner_connection(owner_writer)
        helper = await lease.open_helper(grant_for(path), HELPER)
        lease.owner_closed()
        await asyncio.sleep(0)
        assert helper.writer.is_closing()
        assert lease.closed
        with pytest.raises(OwnerHelperAdmissionError):
            await lease.open_helper(grant_for(path), HELPER)
        owner_writer.close()
        await owner_writer.wait_closed()
        server.close()
        await server.wait_closed()

    asyncio.run(run())


@pytest.mark.parametrize("message", [
    {"id": 1, "method": "thread/read", "params": {"threadId": "other", "includeTurns": True}},
    {"id": 1, "method": "turn/start", "params": {"threadId": OWNER["owner_thread_id"], "input": ["prompt"]}},
    {"id": 1, "method": "command/exec", "params": {"threadId": OWNER["owner_thread_id"], "command": "pwd"}},
    {"id": 1, "method": "thread/fork", "params": {"threadId": OWNER["owner_thread_id"]}},
    {"id": 1, "method": "thread/read", "params": {"threadId": OWNER["owner_thread_id"], "includeTurns": True, "prompt": "secret"}},
    {"id": 1, "method": "unknown/method", "params": {}},
    {"id": 1, "method": "turn/start", "params": {"threadId": OWNER["owner_thread_id"], "input": [], "toolOutput": {"name": "wrong", "namespace": "orchestration", "output": "{}"}}},
    {"id": 1, "method": "turn/start", "params": {"threadId": OWNER["owner_thread_id"], "input": [], "toolOutput": {"name": "g0_delivery", "namespace": "orchestration", "output": "{}", "extra": 1}}},
])
def test_helper_rpc_gate_rejects_cross_thread_or_write_envelopes(message):
    with pytest.raises(OwnerHelperAdmissionError):
        validate_helper_rpc(message, OWNER["owner_thread_id"])


def test_helper_rpc_gate_allows_native_owner_requests_and_tool_output():
    initialize = {
        "id": 1,
        "method": "initialize",
        "params": {
            "clientInfo": {"name": "g0-native-standalone", "version": "0.1.0"},
            "capabilities": {"experimentalApi": True},
        },
    }
    assert validate_helper_rpc(initialize, OWNER["owner_thread_id"]) is None
    assert validate_helper_rpc({"method": "initialized"}, OWNER["owner_thread_id"]) is None
    for method in ("server/diagnostics", "remoteControl/status/read"):
        assert validate_helper_rpc({"id": 2, "method": method, "params": {}}, OWNER["owner_thread_id"]) is None
    assert validate_helper_rpc(
        {"id": 3, "method": "thread/read", "params": {"threadId": OWNER["owner_thread_id"], "includeTurns": True}},
        OWNER["owner_thread_id"],
    ) is None
    intent = {
        "version": 1,
        "delivery_id": "delivery_1",
        "controller_thread_id": OWNER["owner_thread_id"],
        "controller_epoch": 1,
        "events": [{"event_id": "event_1", "event_revision": 1, "kind": "result", "payload_hash": "d" * 64, "action_slot": "ack_r1"}],
    }
    import hashlib
    import json as _json
    intent["payload_hash"] = hashlib.sha256(_json.dumps({k: v for k, v in intent.items() if k != "payload_hash"}, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    params = {"threadId": OWNER["owner_thread_id"], "input": [], "toolOutput": {"name": "g0_delivery", "namespace": "orchestration", "output": _json.dumps(intent, sort_keys=True, separators=(",", ":"))}}
    assert validate_helper_rpc({"id": 4, "method": "turn/start", "params": params}, OWNER["owner_thread_id"]) is None


def _server_frame(payload: bytes, *, opcode: int = 1, fin: bool = True, masked: bool = False, rsv: int = 0) -> bytes:
    first = (0x80 if fin else 0) | ((rsv & 0x7) << 4) | opcode
    second = (0x80 if masked else 0) | len(payload)
    if len(payload) >= 126:
        raise AssertionError("test payload must use the short frame form")
    if not masked:
        return bytes((first, second)) + payload
    mask = b"abcd"
    return bytes((first, second)) + mask + bytes(byte ^ mask[i % 4] for i, byte in enumerate(payload))


def test_helper_reassembles_fragmented_and_pipelined_server_json(tmp_path):
    async def run():
        path = socket_path(tmp_path)
        server, state = await _ws_server(path)
        await _open_owner(path)
        lease = OwnerLease(**{**OWNER, "private_socket": str(path)}, backend_pid=20001, backend_birth="backend-birth", helper_identity=HELPER)
        helper = await lease.open_helper(grant_for(path), HELPER)
        message = {"id": 1, "result": {"thread": {"id": OWNER["owner_thread_id"]}}}
        encoded = json.dumps(message, separators=(",", ":")).encode()
        await helper.send_rpc({"id": 1, "method": "server/diagnostics", "params": {}})
        await helper.send_rpc({"id": 2, "method": "remoteControl/status/read", "params": {}})
        writer = state["connections"][-1]
        split = len(encoded) // 2
        writer.write(_server_frame(encoded[:split], fin=False) + _server_frame(encoded[split:], opcode=0))
        writer.write(_server_frame(b'{"id":2,"result":{}}'))
        await writer.drain()
        assert await helper.read_rpc() == message
        assert await helper.read_rpc() == {"id": 2, "result": {}}
        await helper.close()
        server.close()
        await server.wait_closed()

    asyncio.run(run())


@pytest.mark.parametrize("frame", [
    _server_frame(b"{}", masked=True),
    _server_frame(b"{}", rsv=1),
    _server_frame(b"{}", opcode=2),
])
def test_helper_rejects_unsupported_server_frame_before_rpc_delivery(tmp_path, frame):
    async def run():
        path = socket_path(tmp_path)
        server, state = await _ws_server(path)
        await _open_owner(path)
        lease = OwnerLease(**{**OWNER, "private_socket": str(path)}, backend_pid=20001, backend_birth="backend-birth", helper_identity=HELPER)
        helper = await lease.open_helper(grant_for(path), HELPER)
        writer = state["connections"][-1]
        writer.write(frame)
        await writer.drain()
        with pytest.raises(OwnerHelperAdmissionError):
            await helper.read_rpc()
        await helper.close()
        server.close()
        await server.wait_closed()

    asyncio.run(run())


def test_symlinked_backend_socket_is_not_admitted(tmp_path):
    async def run():
        real = socket_path(tmp_path)
        alias = Path(f"{real}.alias")
        server, state = await _ws_server(real)
        alias.symlink_to(real)
        lease = OwnerLease(**{**OWNER, "private_socket": str(alias)}, backend_pid=20001, backend_birth="backend-birth", helper_identity=HELPER)
        with pytest.raises(OwnerHelperAdmissionError):
            lease.validate_grant(grant_for(alias), HELPER)
        server.close()
        await server.wait_closed()

    asyncio.run(run())


def test_grant_cannot_self_assign_another_helper_identity(tmp_path):
    async def run():
        path = socket_path(tmp_path)
        server, state = await _ws_server(path)
        lease = OwnerLease(**{**OWNER, "private_socket": str(path)}, backend_pid=20001, backend_birth="backend-birth", helper_identity=HELPER)
        other = HelperIdentity(**{**HELPER.__dict__, "pid": HELPER.pid + 1})
        with pytest.raises(OwnerHelperAdmissionError):
            lease.validate_grant(grant_for(path, helper_pid=other.pid), other)
        server.close()
        await server.wait_closed()

    asyncio.run(run())


def test_helper_rejects_response_for_another_thread(tmp_path):
    async def run():
        path = socket_path(tmp_path)
        server, state = await _ws_server(path)
        await _open_owner(path)
        lease = OwnerLease(**{**OWNER, "private_socket": str(path)}, backend_pid=20001, backend_birth="backend-birth", helper_identity=HELPER)
        helper = await lease.open_helper(grant_for(path), HELPER)
        writer = state["connections"][-1]
        await helper.send_rpc({"id": 1, "method": "thread/read", "params": {"threadId": OWNER["owner_thread_id"], "includeTurns": True}})
        wrong = {"id": 1, "result": {"thread": {"id": "other-thread"}}}
        writer.write(_server_frame(json.dumps(wrong).encode()))
        await writer.drain()
        with pytest.raises(OwnerHelperAdmissionError):
            await helper.read_rpc()
        await helper.close()
        server.close()
        await server.wait_closed()

    asyncio.run(run())


def test_connection_enforces_rpc_gate_before_writing_frame():
    class CaptureWriter:
        def __init__(self):
            self.writes = []
            self.closed = False

        def write(self, data):
            self.writes.append(data)

        async def drain(self):
            return None

        def close(self):
            self.closed = True

        async def wait_closed(self):
            return None

    async def run():
        from owner_helper import OwnerHelperConnection

        writer = CaptureWriter()
        connection = OwnerHelperConnection(
            reader=asyncio.StreamReader(),
            writer=writer,
            lease_id=OWNER["lease_id"],
            owner_thread_id=OWNER["owner_thread_id"],
        )
        bad = {"id": 1, "method": "turn/start", "params": {"threadId": OWNER["owner_thread_id"]}}
        with pytest.raises(OwnerHelperAdmissionError):
            await connection.send_rpc(bad)
        assert writer.writes == []
        await connection.send_rpc({"id": 2, "method": "server/diagnostics", "params": {}})
        assert writer.writes and writer.writes[0][1] & 0x80
        await connection.close()

    asyncio.run(run())


def test_helper_accepts_scoped_server_notification_and_rejects_unknown(tmp_path):
    async def run():
        path = socket_path(tmp_path)
        server, state = await _ws_server(path)
        await _open_owner(path)
        lease = OwnerLease(**{**OWNER, "private_socket": str(path)}, backend_pid=20001, backend_birth="backend-birth", helper_identity=HELPER)
        helper = await lease.open_helper(grant_for(path), HELPER)
        writer = state["connections"][-1]
        notification = {
            "method": "thread/status/changed",
            "params": {"threadId": OWNER["owner_thread_id"], "status": "idle"},
        }
        encoded_notification = json.dumps(notification).encode()
        split = len(encoded_notification) // 2
        writer.write(_server_frame(encoded_notification[:split], fin=False) + _server_frame(encoded_notification[split:], opcode=0))
        await writer.drain()
        assert await helper.read_rpc() == notification
        unknown = {"method": "thread/other/changed", "params": {}}
        writer.write(_server_frame(json.dumps(unknown).encode()))
        await writer.drain()
        with pytest.raises(OwnerHelperAdmissionError):
            await helper.read_rpc()
        await helper.close()
        server.close()
        await server.wait_closed()

    asyncio.run(run())


async def _echo_ws_server(path: Path):
    state = {"connections": [], "handshakes": 0, "requests": []}

    async def handle(reader, writer):
        try:
            request = await _read_http_headers(reader)
            key = next(line.split(b":", 1)[1].strip() for line in request.split(b"\r\n") if line.lower().startswith(b"sec-websocket-key:"))
            accept = base64.b64encode(hashlib.sha1(key + b"258EAFA5-E914-47DA-95CA-C5AB0DC85B11").digest())
            writer.write(b"HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + b"\r\n\r\n")
            await writer.drain()
            state["connections"].append(writer)
            state["handshakes"] += 1
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
                state["requests"].append(message)
                response = {"id": message["id"], "result": {"process": {"id": 20001}}}
                writer.write(_server_frame(json.dumps(response, separators=(",", ":")).encode()))
                await writer.drain()
        except (asyncio.IncompleteReadError, ConnectionError):
            pass
        finally:
            if writer in state["connections"]:
                state["connections"].remove(writer)
            writer.close()
            await writer.wait_closed()

    server = await asyncio.start_unix_server(handle, path=str(path))
    return server, state


def test_real_uds_websocket_forwards_only_authorized_helper_request(tmp_path):
    async def run():
        path = socket_path(tmp_path)
        server, state = await _echo_ws_server(path)
        await _open_owner(path)
        lease = OwnerLease(**{**OWNER, "private_socket": str(path)}, backend_pid=20001, backend_birth="backend-birth", helper_identity=HELPER)
        helper = await lease.open_helper(grant_for(path), HELPER)
        request = {"id": 1, "method": "server/diagnostics", "params": {}}
        await helper.send_rpc(request)
        assert await helper.read_rpc() == {"id": 1, "result": {"process": {"id": 20001}}}
        assert state["handshakes"] == 2
        assert state["requests"] == [request]
        with pytest.raises(OwnerHelperAdmissionError):
            await helper.send_rpc({"id": 2, "method": "turn/start", "params": {"threadId": OWNER["owner_thread_id"], "input": ["ordinary prompt"]}})
        assert state["requests"] == [request]
        await helper.close()
        server.close()
        await server.wait_closed()

    asyncio.run(run())


def _client_frame(payload: bytes, *, opcode=1, fin=True):
    mask = b"wxyz"
    first = (0x80 if fin else 0) | opcode
    masked = bytes(byte ^ mask[i % 4] for i, byte in enumerate(payload))
    if len(payload) >= 126:
        raise AssertionError("test payload must use short frame")
    return bytes((first, 0x80 | len(payload))) + mask + masked


def _upgrade_bytes():
    return b"GET / HTTP/1.1\r\nHost: synthetic\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"


def test_service_wire_gate_validates_before_forwarding_and_tracks_response_ids():
    from owner_helper import NativeWebSocketGate

    owner = OWNER["owner_thread_id"]
    client = NativeWebSocketGate(owner, server=False)
    request = {"id": 1, "method": "thread/read", "params": {"threadId": owner, "includeTurns": True}}
    frame = _client_frame(json.dumps(request, separators=(",", ":")).encode())
    assert client.feed(_upgrade_bytes() + frame[:4]) == _upgrade_bytes()
    assert client.feed(frame[4:]) == frame
    server = NativeWebSocketGate(owner, server=True, pending_ids=client.pending_ids)
    response = {"id": 1, "result": {"thread": {"id": owner}}}
    server_frame = _server_frame(json.dumps(response, separators=(",", ":")).encode())
    assert server.feed(_upgrade_bytes() + server_frame) == _upgrade_bytes() + server_frame
    assert not client.pending_ids
    bad = {"id": 2, "method": "turn/start", "params": {"threadId": owner, "input": ["ordinary prompt"]}}
    with pytest.raises(OwnerHelperAdmissionError):
        client.feed(_client_frame(json.dumps(bad).encode()))


def test_service_wire_gate_rejects_server_request_and_cross_thread_response():
    from owner_helper import NativeWebSocketGate

    owner = OWNER["owner_thread_id"]
    client = NativeWebSocketGate(owner, server=False)
    request = {"id": 1, "method": "server/diagnostics", "params": {}}
    client.feed(_upgrade_bytes() + _client_frame(json.dumps(request).encode()))
    server = NativeWebSocketGate(owner, server=True, pending_ids=client.pending_ids)
    server_request = {"id": 99, "method": "thread/read", "params": {"threadId": owner, "includeTurns": True}}
    with pytest.raises(OwnerHelperAdmissionError):
        server.feed(_upgrade_bytes() + _server_frame(json.dumps(server_request).encode()))
    wrong = {"id": 1, "result": {"thread": {"id": "other"}}}
    with pytest.raises(OwnerHelperAdmissionError):
        server.feed(_server_frame(json.dumps(wrong).encode()))


def test_tool_output_rejects_boolean_intent_version():
    intent = {
        "version": True,
        "delivery_id": "delivery_1",
        "controller_thread_id": OWNER["owner_thread_id"],
        "controller_epoch": 1,
        "events": [{"event_id": "event_1", "event_revision": 1, "kind": "result", "payload_hash": "d" * 64, "action_slot": "ack_r1"}],
        "payload_hash": "e" * 64,
    }
    with pytest.raises(OwnerHelperAdmissionError):
        validate_helper_rpc({
            "id": 1,
            "method": "turn/start",
            "params": {"threadId": OWNER["owner_thread_id"], "input": [], "toolOutput": {
                "name": "g0_delivery", "namespace": "orchestration", "output": json.dumps(intent),
            }},
        }, OWNER["owner_thread_id"])


def test_owner_delivery_lifecycle_notifications_are_native_and_owner_scoped():
    owner = OWNER["owner_thread_id"]
    turn = {"id": "turn_delivery_1", "status": "inProgress", "items": []}
    item = {"type": "functionCallOutput", "id": "item_delivery_1", "name": "g0_delivery", "namespace": "orchestration", "output": "{}"}
    messages = [
        {"method": "turn/started", "params": {"threadId": owner, "turn": turn}},
        {"method": "item/started", "params": {"threadId": owner, "turnId": turn["id"], "item": item, "startedAtMs": 1}},
        {"method": "item/completed", "params": {"threadId": owner, "turnId": turn["id"], "item": item, "completedAtMs": 2}},
        {"method": "turn/completed", "params": {"threadId": owner, "turn": {**turn, "status": "completed"}}},
    ]
    for message in messages:
        assert validate_helper_server_message(message, owner) is None
    wrong = {**messages[1], "params": {**messages[1]["params"], "threadId": "other"}}
    with pytest.raises(OwnerHelperAdmissionError):
        validate_helper_server_message(wrong, owner)


@pytest.mark.parametrize("emitted_at_ms", [-(1 << 63), (1 << 63) - 1])
def test_server_notification_accepts_bounded_emitted_at_ms(emitted_at_ms):
    message = {
        "method": "turn/started",
        "params": {"threadId": OWNER["owner_thread_id"], "turn": {"id": "turn_1"}},
        "emittedAtMs": emitted_at_ms,
    }
    assert validate_helper_server_message(message, OWNER["owner_thread_id"]) is None


@pytest.mark.parametrize("emitted_at_ms", [True, False, 1.0, 1 << 63, -(1 << 63) - 1])
def test_server_notification_rejects_invalid_emitted_at_ms(emitted_at_ms):
    message = {
        "method": "turn/started",
        "params": {"threadId": OWNER["owner_thread_id"], "turn": {"id": "turn_1"}},
        "emittedAtMs": emitted_at_ms,
    }
    with pytest.raises(OwnerHelperAdmissionError):
        validate_helper_server_message(message, OWNER["owner_thread_id"])


def test_server_notification_rejects_other_top_level_metadata():
    message = {
        "method": "turn/started",
        "params": {"threadId": OWNER["owner_thread_id"], "turn": {"id": "turn_1"}},
        "emittedAtMs": 1,
        "extra": "reject",
    }
    with pytest.raises(OwnerHelperAdmissionError):
        validate_helper_server_message(message, OWNER["owner_thread_id"])


def test_native_agent_message_delta_is_owner_scoped_and_type_strict():
    owner = OWNER["owner_thread_id"]
    message = {
        "method": "item/agentMessage/delta",
        "params": {
            "threadId": owner,
            "turnId": "turn_1",
            "itemId": "item_1",
            "delta": "partial text",
        },
        "emittedAtMs": 1,
    }
    assert validate_helper_server_message(message, owner) is None
    for key, value in (("threadId", "other"), ("turnId", 1), ("itemId", False), ("delta", 1)):
        bad = {**message, "params": {**message["params"], key: value}}
        with pytest.raises(OwnerHelperAdmissionError):
            validate_helper_server_message(bad, owner)
    with pytest.raises(OwnerHelperAdmissionError):
        validate_helper_server_message({**message, "params": {**message["params"], "extra": 1}}, owner)


def test_account_notifications_use_fixed_read_only_shapes():
    owner = OWNER["owner_thread_id"]
    rate_limits = {
        "limitId": "codex",
        "limitName": "Codex",
        "normalModelSlug": "gpt-5",
        "primary": {"usedPercent": 1, "windowDurationMins": 300, "resetsAt": 1700000000},
        "secondary": None,
        "credits": {"hasCredits": False, "unlimited": False, "balance": None},
        "individualLimit": None,
        "spendControlReached": False,
        "planType": "plus",
        "rateLimitReachedType": None,
    }
    limits = {"method": "account/rateLimits/updated", "params": {"rateLimits": rate_limits}, "emittedAtMs": 2}
    assert validate_helper_server_message(limits, owner) is None
    assert validate_helper_server_message({"method": "account/rateLimits/updated", "params": {"rateLimits": {}}}, owner) is None
    assert validate_helper_server_message({"method": "account/rateLimits/updated", "params": {"rateLimits": {
        "primary": {"usedPercent": 1}, "credits": {"hasCredits": False, "unlimited": False},
    }}}, owner) is None
    for bad in (
        {"method": "account/updated", "params": {"authMode": "chatgpt", "planType": "plus"}},
        {**limits, "params": {"rateLimits": {**rate_limits, "primary": {"usedPercent": True, "windowDurationMins": 1, "resetsAt": 2}}}},
        {**limits, "params": {"rateLimits": {**rate_limits, "extra": 1}}},
        {**limits, "params": {"rateLimits": {"primary": {"usedPercent": 1, "unknown": 2}}}},
    ):
        with pytest.raises(OwnerHelperAdmissionError):
            validate_helper_server_message(bad, owner)


def test_thread_started_remains_rejected_for_helper():
    with pytest.raises(OwnerHelperAdmissionError):
        validate_helper_server_message(
            {"method": "thread/started", "params": {"thread": {"id": OWNER["owner_thread_id"]}}},
            OWNER["owner_thread_id"],
        )
