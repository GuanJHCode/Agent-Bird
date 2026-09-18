from __future__ import annotations

import asyncio
import json
import os
import socket
import struct
import sys
from pathlib import Path
from types import SimpleNamespace

import pytest


ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "plugins" / "codex-orchestrator" / "lib"))
sys.path.insert(0, str(ROOT / "tasks" / "g0-completion" / "scripts"))
sys.path.insert(0, str(ROOT / "tasks" / "g0-tui-proxy" / "scripts"))
sys.path.insert(
    0, str(ROOT / "tasks" / "g0-proxy-continuation" / "cold-start" / "scripts")
)

from owner_helper import NativeWebSocketGate, OwnerHelperAdmissionError  # noqa: E402
from proxy_transport import CaptureSink, ProxyServer  # noqa: E402
from trial.auth_wire import AuthWireRejected, make_auth_wire_gate_factory  # noqa: E402


_CLIENT_HANDSHAKE = (
    b"GET / HTTP/1.1\r\nHost: trial\r\nUpgrade: websocket\r\n"
    b"Connection: Upgrade\r\n\r\n"
)
_SERVER_HANDSHAKE = (
    b"HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\n"
    b"Connection: Upgrade\r\n\r\n"
)


def _frame(
    payload: bytes,
    *,
    masked: bool,
    opcode: int = 1,
    fin: bool = True,
    compressed: bool = False,
) -> bytes:
    first = opcode | (0x80 if fin else 0) | (0x40 if compressed else 0)
    length = len(payload)
    mask_bit = 0x80 if masked else 0
    if length < 126:
        header = bytes((first, mask_bit | length))
    elif length <= 0xFFFF:
        header = bytes((first, mask_bit | 126)) + struct.pack("!H", length)
    else:
        header = bytes((first, mask_bit | 127)) + struct.pack("!Q", length)
    if not masked:
        return header + payload
    key = b"\x11\x22\x33\x44"
    masked_payload = bytes(byte ^ key[index % 4] for index, byte in enumerate(payload))
    return header + key + masked_payload


def _json_frame(message: object, *, masked: bool = True) -> bytes:
    return _frame(
        json.dumps(message, separators=(",", ":")).encode("utf-8"),
        masked=masked,
    )


def _gates(rejections: list[dict[str, object]] | None = None):
    rows = rejections if rejections is not None else []
    factory = make_auth_wire_gate_factory(NativeWebSocketGate)
    return factory(None, None, "conn-test", 1, rows.append)


def test_forbidden_rpc_never_reaches_backend_socket() -> None:
    rejections: list[dict[str, object]] = []
    client_gate, _ = _gates(rejections)
    backend_reader, backend_writer = socket.socketpair()
    try:
        backend_reader.settimeout(0.02)
        backend_writer.sendall(client_gate(_CLIENT_HANDSHAKE))
        assert backend_reader.recv(len(_CLIENT_HANDSHAKE)) == _CLIENT_HANDSHAKE

        forbidden = _json_frame(
            {
                "id": 1,
                "method": "account/login/start",
                "params": {"apiKey": "secret-shaped-fixture"},
            }
        )
        with pytest.raises(AuthWireRejected, match="^auth_rpc_forbidden$") as error:
            forwarded = client_gate(forbidden)
            backend_writer.sendall(forwarded)

        with pytest.raises(TimeoutError):
            backend_reader.recv(1)
        assert "secret-shaped-fixture" not in repr(error.value)
        assert rejections == [
            {
                "stage": "trial_auth_wire_client",
                "reason": "auth_rpc_forbidden",
            }
        ]
    finally:
        backend_reader.close()
        backend_writer.close()


def test_fragmented_message_is_not_released_before_complete_validation() -> None:
    client_gate, _ = _gates()
    assert client_gate(_CLIENT_HANDSHAKE) == _CLIENT_HANDSHAKE
    payload = b'{"id":1,"method":"account/logout","params":{}}'
    split = 21
    first = _frame(payload[:split], masked=True, opcode=1, fin=False)
    last = _frame(payload[split:], masked=True, opcode=0, fin=True)

    assert client_gate(first) == b""
    with pytest.raises(AuthWireRejected, match="^auth_rpc_forbidden$"):
        client_gate(last)


def test_proxy_capture_sees_only_released_complete_fragmented_message() -> None:
    async def run() -> None:
        suffix = f"{os.getpid()}-{id(run)}"
        frontend_path = Path("/tmp") / f"aw-front-{suffix}.sock"
        backend_path = Path("/tmp") / f"aw-back-{suffix}.sock"
        backend_bytes = bytearray()
        backend_changed = asyncio.Event()
        backend_writers: list[asyncio.StreamWriter] = []

        async def backend(
            reader: asyncio.StreamReader, writer: asyncio.StreamWriter
        ) -> None:
            backend_writers.append(writer)
            try:
                while chunk := await reader.read(65536):
                    backend_bytes.extend(chunk)
                    backend_changed.set()
            finally:
                writer.close()
                await writer.wait_closed()

        class Sink(CaptureSink):
            def __init__(self) -> None:
                self.data = []

            def on_data(self, event: object) -> None:
                self.data.append(event)

        async def wait_for(predicate) -> None:
            deadline = asyncio.get_running_loop().time() + 1
            while not predicate():
                if asyncio.get_running_loop().time() >= deadline:
                    raise AssertionError("fixture_wait_timeout")
                backend_changed.clear()
                try:
                    await asyncio.wait_for(backend_changed.wait(), 0.02)
                except TimeoutError:
                    pass

        sink = Sink()
        factory = make_auth_wire_gate_factory(NativeWebSocketGate)
        gates = lambda front, back, connection, epoch: factory(  # noqa: E731
            front, back, connection, epoch, lambda _row: None
        )
        backend_server = await asyncio.start_unix_server(backend, backend_path)
        proxy = ProxyServer(
            frontend_path,
            backend_path,
            sink,
            authorize_peer=lambda peer: peer.pid == os.getpid(),
            authorize_backend_peer=lambda peer: peer.uid == os.getuid(),
            stop_on_frontend_eof=True,
            wire_gate_factory=gates,
        )
        await proxy.start()
        reader, writer = await asyncio.open_unix_connection(frontend_path)
        del reader
        try:
            writer.write(_CLIENT_HANDSHAKE)
            await writer.drain()
            await wait_for(lambda: len(sink.data) == 1)
            assert sink.data[0].data == _CLIENT_HANDSHAKE
            assert sink.data[0].direction_seq == 1

            payload = (
                b'{"id":7,"method":"account/read",'
                b'"params":{"refreshToken":false}}'
            )
            split = 27
            first = _frame(payload[:split], masked=True, opcode=1, fin=False)
            last = _frame(payload[split:], masked=True, opcode=0, fin=True)
            writer.write(first)
            await writer.drain()
            await asyncio.sleep(0.03)
            assert len(sink.data) == 1
            assert bytes(backend_bytes) == _CLIENT_HANDSHAKE

            writer.write(last)
            await writer.drain()
            await wait_for(lambda: len(sink.data) == 2)
            await wait_for(
                lambda: bytes(backend_bytes) == _CLIENT_HANDSHAKE + first + last
            )
            assert sink.data[1].data == first + last
            assert sink.data[1].direction_seq == 2
        finally:
            writer.close()
            await writer.wait_closed()
            await proxy.close()
            backend_server.close()
            await backend_server.wait_closed()
            for backend_writer in backend_writers:
                if not backend_writer.is_closing():
                    backend_writer.close()
                    await backend_writer.wait_closed()
            frontend_path.unlink(missing_ok=True)
            backend_path.unlink(missing_ok=True)

    asyncio.run(run())


def test_account_read_strict_false_passes_after_complete_message() -> None:
    client_gate, _ = _gates()
    frame = _json_frame(
        {"id": 7, "method": "account/read", "params": {"refreshToken": False}}
    )
    assert client_gate(_CLIENT_HANDSHAKE + frame) == _CLIENT_HANDSHAKE + frame


@pytest.mark.parametrize(
    "invalid_frame",
    [
        _frame(
            b'{"id":1,"method":"account/read","params":{'
            b'"refreshToken":false,"refreshToken":false}}',
            masked=True,
        ),
        _frame(b'{"id":1,"method":"thread/read"}', masked=True, compressed=True),
        _frame(b"x" * 65536, masked=True),
    ],
)
def test_duplicate_json_compression_and_oversize_fail_closed(
    invalid_frame: bytes,
) -> None:
    client_gate, _ = _gates()
    assert client_gate(_CLIENT_HANDSHAKE) == _CLIENT_HANDSHAKE
    with pytest.raises(AuthWireRejected, match="^auth_wire_invalid_message$"):
        client_gate(invalid_frame)


def test_close_ping_and_pong_control_frames_pass_unchanged() -> None:
    client_gate, _ = _gates()
    assert client_gate(_CLIENT_HANDSHAKE) == _CLIENT_HANDSHAKE
    for frame in (
        _frame(b"ping", masked=True, opcode=9),
        _frame(b"pong", masked=True, opcode=10),
        _frame(struct.pack("!H", 1000), masked=True, opcode=8),
    ):
        assert client_gate(frame) == frame


def test_control_frames_bypass_pending_fragment_and_close_discards_it() -> None:
    client_gate, _ = _gates()
    assert client_gate(_CLIENT_HANDSHAKE) == _CLIENT_HANDSHAKE
    first = _frame(b'{"id":1,', masked=True, opcode=1, fin=False)
    assert client_gate(first) == b""
    ping = _frame(b"ping", masked=True, opcode=9)
    pong = _frame(b"pong", masked=True, opcode=10)
    close = _frame(struct.pack("!H", 1000), masked=True, opcode=8)
    assert client_gate(ping) == ping
    assert client_gate(pong) == pong
    assert client_gate(close) == close
    with pytest.raises(AuthWireRejected, match="^auth_wire_invalid_message$"):
        client_gate(_frame(b'"method":"account/read"}', masked=True, opcode=0))


def test_server_token_refresh_request_is_rejected() -> None:
    _, server_gate = _gates()
    assert server_gate(_SERVER_HANDSHAKE) == _SERVER_HANDSHAKE
    request = _json_frame(
        {
            "id": 3,
            "method": "account/chatgptAuthTokens/refresh",
            "params": {"reason": "unauthorized"},
        },
        masked=False,
    )
    with pytest.raises(AuthWireRejected, match="^account_refresh_forbidden$"):
        server_gate(request)


@pytest.mark.parametrize("method", ["config/value/write", "config/batchWrite"])
def test_user_config_write_methods_are_rejected(method: str) -> None:
    client_gate, _ = _gates()
    assert client_gate(_CLIENT_HANDSHAKE) == _CLIENT_HANDSHAKE
    request = _json_frame(
        {
            "id": 4,
            "method": method,
            "params": {"value": "secret-shaped-fixture"},
        }
    )
    with pytest.raises(
        AuthWireRejected, match="^trial_config_write_forbidden$"
    ) as error:
        client_gate(request)
    assert "secret-shaped-fixture" not in repr(error.value)


@pytest.mark.parametrize(
    "message",
    [
        {
            "id": 5,
            "method": "thread/start",
            "params": {"config": {"features.use_agent_identity": True}},
        },
        {
            "id": 5,
            "method": "thread/start",
            "params": {"modelProvider": "external-provider"},
        },
        {
            "id": 5,
            "method": "thread/resume",
            "params": {"config": {"cli_auth_credentials_store": "keyring"}},
        },
        {
            "id": 5,
            "method": "thread/fork",
            "params": {"modelProvider": "external-provider"},
        },
        {
            "id": 5,
            "method": "turn/start",
            "params": {"config": {"model_provider": "external-provider"}},
        },
    ],
)
def test_request_scoped_auth_or_provider_overrides_are_rejected(
    message: dict[str, object],
) -> None:
    client_gate, _ = _gates()
    assert client_gate(_CLIENT_HANDSHAKE) == _CLIENT_HANDSHAKE
    with pytest.raises(
        AuthWireRejected, match="^trial_session_auth_override_forbidden$"
    ):
        client_gate(_json_frame(message))


@pytest.mark.parametrize(
    "params",
    [
        {"config": None, "modelProvider": None},
        {"config": {}, "modelProvider": None},
        {
            "config": {
                "allow_login_shell": False,
                "features": {"use_agent_identity": False},
                "model_reasoning_effort": "high",
                "web_search": "cached",
            },
            "modelProvider": None,
        },
    ],
)
def test_fixed_remote_tui_non_auth_overrides_pass(params: dict[str, object]) -> None:
    client_gate, _ = _gates()
    assert client_gate(_CLIENT_HANDSHAKE) == _CLIENT_HANDSHAKE
    request = _json_frame({"id": 8, "method": "thread/start", "params": params})
    assert client_gate(request) == request


@pytest.mark.parametrize(
    "params",
    [
        {"config": []},
        {"config": {"features": None}},
        {"config": {"features": []}},
        {"config": {"features": {"use_agent_identity": None}}},
        {"config": {"features": {"use_agent_identity": "false"}}},
        {"config": {"features": {"secret_auth_storage": True}}},
        {"config": {"features": {"mcp_oauth_refresh_coordination": True}}},
        {"config": {"features": {"auth_elicitation": True}}},
        {"config": {"features": {"respect_system_proxy": True}}},
        {"config": {"features": {"unified_exec": False}}},
        {"config": {"allow_login_shell": True}},
        {"config": {"shell_environment_policy": {"inherit": "none"}}},
        {"config": {"future_auth_setting": False}},
        {"modelProvider": ""},
    ],
)
def test_malformed_or_unknown_session_overrides_fail_closed(
    params: dict[str, object],
) -> None:
    client_gate, _ = _gates()
    assert client_gate(_CLIENT_HANDSHAKE) == _CLIENT_HANDSHAKE
    with pytest.raises(
        AuthWireRejected, match="^trial_session_auth_override_forbidden$"
    ):
        client_gate(_json_frame({"id": 9, "method": "thread/start", "params": params}))


def test_handshake_terminator_beyond_limit_is_rejected() -> None:
    client_gate, _ = _gates()
    oversized_handshake = b"GET / HTTP/1.1\r\nX: " + b"a" * 65520 + b"\r\n\r\n"
    assert len(oversized_handshake) > 65536
    with pytest.raises(AuthWireRejected, match="^auth_wire_invalid_message$"):
        client_gate(oversized_handshake)


def test_activation_injection_keeps_helper_native_gate_stricter() -> None:
    import activation_service

    peer = SimpleNamespace(pid=10, birth="birth", executable="/helper")
    service = object.__new__(activation_service.ActivationService)
    service._helper_admissions = {
        (peer.pid, peer.birth, peer.executable): (
            {"owner_thread_id": "01aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
            "grant-sha",
        )
    }
    service._wire_rejections = []
    service._auth_wire_gate_factory = make_auth_wire_gate_factory(
        NativeWebSocketGate
    )

    client_gate, _ = service._wire_gate_factory(peer, peer, "conn-helper", 9)
    assert client_gate(_CLIENT_HANDSHAKE) == _CLIENT_HANDSHAKE
    auth_allowed_but_helper_forbidden = _json_frame(
        {"id": 1, "method": "account/read", "params": {"refreshToken": False}}
    )
    with pytest.raises(OwnerHelperAdmissionError, match="^rpc_method_forbidden$"):
        client_gate(auth_allowed_but_helper_forbidden)


def test_activation_default_owner_path_remains_without_wire_gate() -> None:
    import activation_service

    peer = SimpleNamespace(pid=10, birth="birth", executable="/owner")
    service = object.__new__(activation_service.ActivationService)
    service._helper_admissions = {}
    service._wire_rejections = []
    service._auth_wire_gate_factory = None

    assert service._wire_gate_factory(peer, peer, "conn-owner", 1) is None


def test_activation_owner_path_uses_explicit_in_process_auth_factory() -> None:
    import activation_service

    peer = SimpleNamespace(pid=10, birth="birth", executable="/owner")
    service = object.__new__(activation_service.ActivationService)
    service._helper_admissions = {}
    service._wire_rejections = []
    service._auth_wire_gate_factory = make_auth_wire_gate_factory(
        NativeWebSocketGate
    )

    client_gate, _ = service._wire_gate_factory(peer, peer, "conn-owner", 2)
    assert client_gate(_CLIENT_HANDSHAKE) == _CLIENT_HANDSHAKE
    with pytest.raises(AuthWireRejected, match="^auth_rpc_forbidden$"):
        client_gate(
            _json_frame(
                {"id": 1, "method": "account/logout", "params": {}}
            )
        )
