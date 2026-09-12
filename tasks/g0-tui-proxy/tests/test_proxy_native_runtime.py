import hashlib
import json
import os
import struct
import sys
from types import SimpleNamespace
from pathlib import Path

import pytest

SCRIPT_DIR = Path(__file__).parents[1] / "scripts"
sys.path.insert(0, str(SCRIPT_DIR))

from proxy_native_runtime import (  # noqa: E402
    AttachmentInferer,
    ConnectionIdentity,
    NativeRuntimeConfig,
    NativeRuntime,
    OwnedEndpoint,
    OwnedProcess,
    PTYDriver,
    RuntimeInvariantError,
    RuntimeWindow,
    _CaptureBridge,
    build_ordinary_tui_argv,
    classify_screen,
    build_native_observer,
    new_epoch_after_disconnect,
    raw_seconds,
)


def connected_event(epoch=1, sequence=1):
    return {"event": "connection_open", "conn_epoch": epoch, "local_seq": sequence}


def upgrade_events(epoch=1, start=2):
    return [
        {"event": "websocket_upgrade", "conn_epoch": epoch, "direction": "client", "local_seq": start},
        {"event": "websocket_upgrade", "conn_epoch": epoch, "direction": "server", "local_seq": start + 1},
        {"event": "rpc", "conn_epoch": epoch, "direction": "client", "method": "initialize", "request_id": 1, "local_seq": start + 2},
        {"event": "rpc_response", "conn_epoch": epoch, "direction": "server", "method": "initialize", "request_id": 1, "ok": True, "local_seq": start + 3},
        {"event": "rpc", "conn_epoch": epoch, "direction": "client", "method": "thread/start", "request_id": 2, "local_seq": start + 4},
        {"event": "rpc_response", "conn_epoch": epoch, "direction": "server", "method": "thread/start", "request_id": 2, "ok": True, "thread": {"id": "thread-1", "sessionId": "session-1"}, "local_seq": start + 5},
        {"event": "rpc", "conn_epoch": epoch, "direction": "client", "method": "skills/list", "request_id": 3, "local_seq": start + 6},
        {"event": "rpc_response", "conn_epoch": epoch, "direction": "server", "method": "skills/list", "request_id": 3, "ok": True, "local_seq": start + 7},
    ]


def raw_upgrade_request():
    return (
        b"GET / HTTP/1.1\r\n"
        b"Host: localhost\r\n"
        b"Upgrade: websocket\r\n"
        b"Connection: Upgrade\r\n"
        b"Sec-WebSocket-Version: 13\r\n"
        b"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n"
    )


def raw_upgrade_response():
    return (
        b"HTTP/1.1 101 Switching Protocols\r\n"
        b"Upgrade: websocket\r\n"
        b"Connection: Upgrade\r\n"
        b"Sec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\n\r\n"
    )


def raw_ws_frame(value, *, client):
    payload = json.dumps(value, separators=(",", ":")).encode()
    first = 0x81
    if client:
        mask = b"\x01\x02\x03\x04"
        if len(payload) < 126:
            header = bytes((first, 0x80 | len(payload)))
        elif len(payload) <= 0xFFFF:
            header = bytes((first, 0x80 | 126)) + struct.pack(">H", len(payload))
        else:
            header = bytes((first, 0x80 | 127)) + struct.pack(">Q", len(payload))
        encoded = bytes(byte ^ mask[index % 4] for index, byte in enumerate(payload))
        return header + mask + encoded
    if len(payload) < 126:
        return bytes((first, len(payload))) + payload
    if len(payload) <= 0xFFFF:
        return bytes((first, 126)) + struct.pack(">H", len(payload)) + payload
    return bytes((first, 127)) + struct.pack(">Q", len(payload)) + payload


def relay_bytes(bridge, connection_id, epoch, direction, data):
    bridge.on_data(
        SimpleNamespace(
            connection_id=connection_id,
            epoch=epoch,
            direction=direction,
            data=data,
        )
    )


def rpc(bridge, connection_id, epoch, direction, value):
    # Native 0.154 JSONRPCMessage omits a jsonrpc envelope field on the wire.
    # Keep older fixture call sites readable while emitting the real shape.
    value = dict(value)
    value.pop("jsonrpc", None)
    relay_bytes(
        bridge,
        connection_id,
        epoch,
        "frontend_to_backend" if direction == "client" else "backend_to_frontend",
        raw_ws_frame(value, client=direction == "client"),
    )


def test_native_observer_uses_protocol_frame_limits():
    observer = build_native_observer()
    assert observer.max_frame_bytes == 16 * 1024 * 1024
    assert observer.max_message_bytes == 64 * 1024 * 1024


def test_ordinary_entry_has_no_remote_model_config_or_prompt_overrides():
    assert build_ordinary_tui_argv("/Users/guanjunhui/.local/bin/codex") == (
        "/Users/guanjunhui/.local/bin/codex",
    )


def test_screen_diagnostic_is_a_bounded_label_without_terminal_text():
    assert classify_screen(b"Welcome; sign in to continue") == "onboarding"
    assert classify_screen(b"Select a model") == "picker"
    assert classify_screen(b"Session: 01234567-89ab-cdef-0123-456789abcdef") == "session"
    assert classify_screen(b"internal error") == "error"
    assert classify_screen(b"opaque text") == "unknown"


@pytest.mark.parametrize("bad", [("--remote",), ("-m", "luna"), ("-c", "x=y"), ("hello",)])
def test_ordinary_entry_rejects_extra_arguments(bad):
    with pytest.raises(ValueError):
        build_ordinary_tui_argv("codex", *bad)


def test_candidate_is_derived_from_observer_events_and_status_is_only_reference():
    inferer = AttachmentInferer()
    inferer.accept(connected_event())
    for event in upgrade_events():
        inferer.accept(event)
    inferer.record_status_reference({"selected": "driver-forged", "session_id": "wrong"})

    candidate = inferer.candidate
    assert candidate is not None
    assert candidate.thread_id == "thread-1"
    assert candidate.session_id == "session-1"
    assert candidate.source == "observer"
    assert "driver-forged" not in repr(candidate)


def test_allowlisted_account_model_bootstrap_is_opaque_and_never_counts_as_ready():
    inferer = AttachmentInferer()
    inferer.accept(connected_event())
    for sequence, method in ((2, "account/read"),):
        inferer.accept(
            {
                "event": "rpc_unknown",
                "conn_epoch": 1,
                    "direction": "client",
                    "method": method,
                    "request_id": sequence,
                    "opaque": True,
                "local_seq": sequence,
            }
        )
        inferer.accept(
            {
                "event": "rpc_unknown",
                "conn_epoch": 1,
                "direction": "server",
                "method": method,
                "request_id": sequence,
                "response": True,
                "opaque": True,
                "local_seq": sequence + 1,
            }
        )
    for event in (
        {"event": "rpc_unknown", "conn_epoch": 1, "direction": "client", "method": "model/list", "request_id": 4, "opaque": True, "local_seq": 4},
        {"event": "rpc_unknown", "conn_epoch": 1, "direction": "server", "method": "model/list", "request_id": 4, "response": True, "opaque": True, "local_seq": 5},
    ):
        inferer.accept(event)
    assert inferer.candidate is None
    for event in upgrade_events(start=6):
        inferer.accept(event)
    assert inferer.candidate is not None


def test_readonly_bootstrap_requires_server_response_with_matching_request_id():
    methods = (
        "account/read",
        "getAuthStatus",
        "account/rateLimits/read",
        "config/read",
        "configRequirements/read",
        "model/list",
        "collaborationMode/list",
        "hooks/list",
        "plugin/list",
        "thread/list",
        "thread/loaded/list",
        "thread/turns/list",
        "modelProvider/capabilities/read",
    )
    inferer = AttachmentInferer()
    inferer.accept(connected_event())
    sequence = 2
    for method in methods:
        inferer.accept(
            {
                "event": "rpc_unknown",
                "conn_epoch": 1,
                "direction": "client",
                "method": method,
                "request_id": sequence,
                "local_seq": sequence,
            }
        )
        assert inferer.candidate is None
        inferer.accept(
            {
                "event": "rpc_unknown",
                "conn_epoch": 1,
                "direction": "server",
                "method": method,
                "request_id": sequence,
                "response": True,
                "local_seq": sequence + 1,
            }
        )
        sequence += 2
    for event in upgrade_events(start=sequence):
        inferer.accept(event)
    assert inferer.candidate is not None


def test_last_matching_bootstrap_response_restores_existing_candidate():
    inferer = AttachmentInferer()
    inferer.accept(connected_event())
    for event in upgrade_events():
        inferer.accept(event)
    candidate = inferer.candidate
    assert candidate is not None

    inferer.accept(
        {
            "event": "rpc_unknown",
            "conn_epoch": 1,
            "direction": "client",
            "method": "plugin/list",
            "request_id": 10,
            "local_seq": 10,
        }
    )
    assert inferer.candidate is None
    inferer.accept(
        {
            "event": "rpc_unknown",
            "conn_epoch": 1,
            "direction": "server",
            "method": "plugin/list",
            "request_id": 10,
            "response": True,
            "local_seq": 11,
        }
    )
    assert inferer.candidate == candidate


def test_matching_bootstrap_response_without_required_evidence_stays_incomplete():
    inferer = AttachmentInferer()
    inferer.accept(connected_event())
    inferer.accept(
        {
            "event": "rpc_unknown",
            "conn_epoch": 1,
            "direction": "client",
            "method": "plugin/list",
            "request_id": 2,
            "local_seq": 2,
        }
    )
    inferer.accept(
        {
            "event": "rpc_unknown",
            "conn_epoch": 1,
            "direction": "server",
            "method": "plugin/list",
            "request_id": 2,
            "response": True,
            "local_seq": 3,
        }
    )
    assert inferer.candidate is None
    assert inferer.invalid_reason is None


@pytest.mark.parametrize(
    "response",
    [
        {"direction": "client", "request_id": 2},
        {"direction": "server", "request_id": 999},
    ],
)
def test_bootstrap_orphan_or_wrong_direction_response_invalidates(response):
    inferer = AttachmentInferer()
    inferer.accept(connected_event())
    inferer.accept(
        {
            "event": "rpc_unknown",
            "conn_epoch": 1,
            "direction": "client",
            "method": "plugin/list",
            "request_id": 2,
            "local_seq": 2,
        }
    )
    inferer.accept(
        {
            "event": "rpc_unknown",
            "conn_epoch": 1,
            "method": "plugin/list",
            "response": True,
            "local_seq": 3,
            **response,
        }
    )
    assert inferer.candidate is None
    assert inferer.invalid_reason == "unknown_event"


def test_remote_control_notifications_are_opaque_server_events():
    inferer = AttachmentInferer()
    inferer.accept(connected_event())
    inferer.accept(
        {
            "event": "rpc_unknown",
            "conn_epoch": 1,
            "direction": "server",
            "method": "remoteControl/status/changed",
            "local_seq": 2,
        }
    )
    assert inferer.invalid_reason is None
    assert inferer.candidate is None


@pytest.mark.parametrize(
    "direction,request_id",
    [("client", None), ("server", 7)],
)
def test_remote_control_notification_wrong_direction_or_id_invalidates(direction, request_id):
    inferer = AttachmentInferer()
    inferer.accept(connected_event())
    inferer.accept(
        {
            "event": "rpc_unknown",
            "conn_epoch": 1,
            "direction": direction,
            "method": "remoteControl/status/changed",
            "request_id": request_id,
            "local_seq": 2,
        }
    )
    assert inferer.candidate is None
    assert inferer.invalid_reason == "unknown_event"


def test_mcp_startup_notification_is_opaque_server_event():
    inferer = AttachmentInferer()
    inferer.accept(connected_event())
    inferer.accept(
        {
            "event": "rpc_unknown",
            "conn_epoch": 1,
            "direction": "server",
            "method": "mcpServer/startupStatus/updated",
            "local_seq": 2,
        }
    )
    assert inferer.invalid_reason is None
    assert inferer.candidate is None


@pytest.mark.parametrize(
    "direction,request_id,response",
    [("client", None, False), ("server", 7, False), ("server", None, True)],
)
def test_mcp_startup_notification_wrong_shape_invalidates(direction, request_id, response):
    inferer = AttachmentInferer()
    inferer.accept(connected_event())
    inferer.accept(
        {
            "event": "rpc_unknown",
            "conn_epoch": 1,
            "direction": direction,
            "method": "mcpServer/startupStatus/updated",
            "request_id": request_id,
            "response": response,
            "local_seq": 2,
        }
    )
    assert inferer.candidate is None
    assert inferer.invalid_reason == "unknown_event"


def test_real_http_ws_bytes_flow_through_observer_bridge_and_reconnect_epochs():
    from proxy_observer import Observer

    observed = []
    observer = Observer(emit=observed.append)
    inferer = AttachmentInferer()
    bridge = _CaptureBridge(observer, inferer)
    connection_id = "native-c1"
    bridge.on_connect(SimpleNamespace(connection_id=connection_id, epoch=1))
    relay_bytes(bridge, connection_id, 1, "frontend_to_backend", raw_upgrade_request())
    relay_bytes(bridge, connection_id, 1, "backend_to_frontend", raw_upgrade_response())

    # These native startup calls are intentionally opaque to the allowlist;
    # they must never themselves make an attachment candidate complete.
    rpc(bridge, connection_id, 1, "client", {"jsonrpc": "2.0", "id": 1, "method": "account/read", "params": {"private": "redacted"}})
    rpc(bridge, connection_id, 1, "server", {"jsonrpc": "2.0", "id": 1, "result": {"account": "redacted"}})
    rpc(bridge, connection_id, 1, "client", {"jsonrpc": "2.0", "id": 2, "method": "model/list", "params": {}})
    rpc(bridge, connection_id, 1, "server", {"jsonrpc": "2.0", "id": 2, "result": {"models": ["redacted"]}})
    assert inferer.candidate is None

    rpc(bridge, connection_id, 1, "client", {"jsonrpc": "2.0", "id": 3, "method": "initialize", "params": {}})
    rpc(bridge, connection_id, 1, "server", {"jsonrpc": "2.0", "id": 3, "result": {}})
    rpc(bridge, connection_id, 1, "server", {"jsonrpc": "2.0", "method": "thread/started", "params": {"thread": {"id": "thread-1", "sessionId": "session-1", "private": "redacted"}}})
    rpc(bridge, connection_id, 1, "client", {"jsonrpc": "2.0", "id": 4, "method": "thread/start", "params": {"cwd": "/repo"}})
    rpc(bridge, connection_id, 1, "server", {"jsonrpc": "2.0", "id": 4, "result": {"thread": {"id": "thread-1", "sessionId": "session-1", "cwd": "/repo", "private": "redacted"}}})
    rpc(bridge, connection_id, 1, "client", {"jsonrpc": "2.0", "id": 5, "method": "skills/list", "params": {"cwds": ["/repo"], "forceReload": True}})
    rpc(bridge, connection_id, 1, "server", {"jsonrpc": "2.0", "id": 5, "result": {"skills": ["redacted"]}})
    assert inferer.candidate is not None
    assert inferer.candidate.thread_id == "thread-1"
    assert inferer.status_reference is None
    assert any(row.get("event") == "rpc_unknown" and row.get("method") == "account/read" for row in observed)
    assert any(row.get("event") == "rpc_unknown" and row.get("method") == "model/list" for row in observed)
    assert any(row.get("event") == "rpc" and row.get("method") == "thread/started" for row in observed)
    assert any(row.get("event") == "rpc_response" and row.get("method") == "thread/start" and row.get("thread", {}).get("id") == "thread-1" for row in observed)

    inferer.clear()
    assert inferer.candidate is None
    rpc(bridge, connection_id, 1, "client", {"jsonrpc": "2.0", "id": 6, "method": "thread/unsubscribe", "params": {"threadId": "thread-1"}})
    rpc(bridge, connection_id, 1, "server", {"jsonrpc": "2.0", "id": 6, "result": {}})
    rpc(bridge, connection_id, 1, "client", {"jsonrpc": "2.0", "id": 7, "method": "thread/start", "params": {"cwd": "/repo"}})
    rpc(bridge, connection_id, 1, "server", {"jsonrpc": "2.0", "id": 7, "result": {"thread": {"id": "thread-2", "sessionId": "session-1", "cwd": "/repo"}}})
    rpc(bridge, connection_id, 1, "client", {"jsonrpc": "2.0", "id": 8, "method": "skills/list", "params": {"cwds": ["/repo"], "forceReload": True}})
    rpc(bridge, connection_id, 1, "server", {"jsonrpc": "2.0", "id": 8, "result": {"skills": []}})
    assert inferer.candidate is not None
    assert inferer.candidate.thread_id == "thread-2"

    rpc(bridge, connection_id, 1, "server", {"jsonrpc": "2.0", "method": "VendorAsyncEvent", "params": {"opaque": "redacted"}})
    assert inferer.candidate is None
    assert inferer.invalid_reason == "unknown_event"

    bridge.on_lifecycle(SimpleNamespace(connection_id=connection_id, epoch=1, kind="eof"))
    assert inferer.candidate is None
    assert inferer.invalid_reason == "unknown_event"

    bridge.on_connect(SimpleNamespace(connection_id=connection_id, epoch=2))
    assert inferer.candidate is None
    assert inferer.invalid_reason is None
    # A new epoch cannot inherit the old thread or qualify from a partial replay.
    rpc(bridge, connection_id, 2, "client", {"jsonrpc": "2.0", "id": 1, "method": "skills/list", "params": {"cwds": ["/repo"]}})
    assert inferer.candidate is None


def test_status_reference_alone_never_creates_candidate():
    inferer = AttachmentInferer()
    inferer.record_status_reference({"thread_id": "driver-forged", "selected": "root"})
    assert inferer.candidate is None


def test_replay_readiness_does_not_wait_for_candidate_skills_barrier():
    inferer = AttachmentInferer()
    inferer.accept(connected_event())
    for event in upgrade_events()[:7]:
        inferer.accept(event)
    assert inferer.replay_ready is True
    assert inferer.candidate is None
    for event in upgrade_events()[7:]:
        inferer.accept(event)
    assert inferer.candidate is not None


def test_complete_eof_invalidates_attachment_but_keeps_complete_trace():
    from proxy_observer import Observer

    inferer = AttachmentInferer()
    bridge = _CaptureBridge(Observer(), inferer)
    bridge.on_connect(SimpleNamespace(connection_id="c1", epoch=1, frontend_fd=1, backend_fd=2, frontend_peer=None, backend_peer=None))
    bridge.on_lifecycle(SimpleNamespace(connection_id="c1", epoch=1, kind="eof"))
    assert inferer.candidate is None
    assert inferer.invalid_reason == "eof"
    assert bridge.trace_complete is True


def test_observer_truncated_http_eof_propagates_gap_before_pending():
    from proxy_observer import Observer

    inferer = AttachmentInferer()
    bridge = _CaptureBridge(Observer(), inferer)
    bridge.on_connect(SimpleNamespace(connection_id="c1", epoch=1, frontend_fd=1, backend_fd=2, frontend_peer=None, backend_peer=None))
    relay_bytes(bridge, "c1", 1, "frontend_to_backend", b"GET / HTTP/1.1\r\n")
    bridge.on_lifecycle(SimpleNamespace(connection_id="c1", epoch=1, kind="eof", direction="frontend_to_backend"))
    bridge.on_lifecycle(SimpleNamespace(connection_id="c1", epoch=1, kind="eof", direction="backend_to_frontend"))
    assert any(row.get("event") == "gap" for row in bridge.trace)
    assert bridge.trace_complete is False
    assert inferer.candidate is None


def test_observer_complete_handshake_eof_stays_trace_complete():
    from proxy_observer import Observer

    inferer = AttachmentInferer()
    bridge = _CaptureBridge(Observer(), inferer)
    bridge.on_connect(SimpleNamespace(connection_id="c1", epoch=1, frontend_fd=1, backend_fd=2, frontend_peer=None, backend_peer=None))
    relay_bytes(bridge, "c1", 1, "frontend_to_backend", raw_upgrade_request())
    relay_bytes(bridge, "c1", 1, "backend_to_frontend", raw_upgrade_response())
    bridge.on_lifecycle(SimpleNamespace(connection_id="c1", epoch=1, kind="eof"))
    assert any(row.get("event") == "connection_close" for row in bridge.trace)
    assert bridge.trace_complete is True
    assert inferer.invalid_reason == "eof"


def test_two_transport_eofs_are_both_valid_before_single_observer_close():
    from proxy_observer import Observer

    bridge = _CaptureBridge(Observer(), AttachmentInferer())
    bridge.on_connect(SimpleNamespace(connection_id="c1", epoch=1, frontend_fd=1, backend_fd=2, frontend_peer=None, backend_peer=None))
    bridge.on_lifecycle(SimpleNamespace(connection_id="c1", epoch=1, kind="eof", direction="frontend_to_backend"))
    bridge.on_lifecycle(SimpleNamespace(connection_id="c1", epoch=1, kind="eof", direction="backend_to_frontend"))
    assert bridge.trace_complete is True
    assert [row["event"] for row in bridge.trace].count("eof") == 2


def test_bootstrap_opaque_requires_client_request_and_matching_server_response():
    inferer = AttachmentInferer()
    for event in (
        {"event": "connection_open", "conn_epoch": 1, "local_seq": 1},
        {"event": "rpc_unknown", "conn_epoch": 1, "direction": "server", "method": "account/read", "response": True, "request_id": 1, "local_seq": 2},
    ):
        inferer.accept(event)
    assert inferer.candidate is None
    assert inferer.invalid_reason == "unknown_event"


def test_gap_and_eof_invalidate_candidate_immediately():
    inferer = AttachmentInferer()
    inferer.accept(connected_event())
    for event in upgrade_events():
        inferer.accept(event)
    assert inferer.candidate is not None

    inferer.accept({"event": "gap", "conn_epoch": 1, "reason": "transport_gap", "local_seq": 10})
    assert inferer.candidate is None
    assert inferer.invalid_reason == "transport_gap"

    inferer = AttachmentInferer()
    inferer.accept(connected_event())
    for event in upgrade_events():
        inferer.accept(event)
    inferer.accept({"event": "eof", "conn_epoch": 1, "direction": "client", "local_seq": 10})
    assert inferer.candidate is None
    assert inferer.invalid_reason == "eof"


def test_clear_invalidates_old_target_and_new_epoch_cannot_inherit_qualification():
    inferer = AttachmentInferer()
    inferer.accept(connected_event())
    for event in upgrade_events():
        inferer.accept(event)
    assert inferer.candidate is not None

    inferer.clear()
    assert inferer.candidate is None
    assert inferer.invalid_reason == "clear"

    inferer.accept(connected_event(epoch=2, sequence=11))
    inferer.accept({"event": "websocket_upgrade", "conn_epoch": 2, "direction": "client", "local_seq": 12})
    assert inferer.candidate is None


def test_clear_allows_only_a_new_observed_thread_start_on_same_connection():
    inferer = AttachmentInferer()
    inferer.accept(connected_event())
    for event in upgrade_events():
        inferer.accept(event)
    assert inferer.candidate is not None

    inferer.clear()
    assert inferer.candidate is None
    sequence = 10
    for event in [
        {"event": "rpc", "conn_epoch": 1, "direction": "client", "method": "thread/unsubscribe", "request_id": 4, "local_seq": sequence},
        {"event": "rpc", "conn_epoch": 1, "direction": "client", "method": "thread/start", "request_id": 5, "local_seq": sequence + 1},
        {"event": "rpc_response", "conn_epoch": 1, "direction": "server", "method": "thread/start", "request_id": 5, "ok": True, "thread": {"id": "thread-2", "sessionId": "session-1"}, "local_seq": sequence + 2},
        {"event": "rpc", "conn_epoch": 1, "direction": "client", "method": "skills/list", "request_id": 6, "local_seq": sequence + 3},
        {"event": "rpc_response", "conn_epoch": 1, "direction": "server", "method": "skills/list", "request_id": 6, "ok": True, "local_seq": sequence + 4},
    ]:
        inferer.accept(event)
    assert inferer.candidate is not None
    assert inferer.candidate.thread_id == "thread-2"


def test_unknown_observer_event_invalidates_current_epoch():
    inferer = AttachmentInferer()
    inferer.accept(connected_event())
    for event in upgrade_events():
        inferer.accept(event)
    inferer.accept({"event": "rpc_unknown", "conn_epoch": 1, "local_seq": 10, "method": "VendorEvent"})
    assert inferer.candidate is None
    assert inferer.invalid_reason == "unknown_event"


def test_local_sequence_gap_invalidates_even_without_explicit_gap_event():
    inferer = AttachmentInferer()
    inferer.accept(connected_event())
    inferer.accept({"event": "websocket_upgrade", "conn_epoch": 1, "direction": "client", "local_seq": 3})
    assert inferer.candidate is None
    assert inferer.invalid_reason == "event_sequence_gap"


def test_window_has_bounded_local_and_model_deadlines():
    window = RuntimeWindow.start(now=100.0)
    assert window.local_deadline == 110.0
    assert window.model_deadline == 220.0
    assert window.local_expired(109.99) is False
    assert window.local_expired(110.0) is True
    assert window.model_expired(219.99) is False
    assert window.model_expired(220.0) is True


def test_owned_cleanup_only_removes_exact_socket_identity_and_owned_pid(monkeypatch, tmp_path):
    socket_path = tmp_path / "frontend.sock"
    socket_path.write_bytes(b"")
    first = socket_path.stat()
    endpoint = OwnedEndpoint(socket_path, first.st_dev, first.st_ino)
    killed = []
    process = OwnedProcess(pid=1234, birth="birth", executable="/bin/codex")
    cleanup = NativeRuntimeConfig.cleanup_owned(
        [process], [endpoint], kill=lambda pid: killed.append(pid)
    )
    assert cleanup == {"pids": [1234], "endpoints": [str(socket_path)]}
    assert killed == [1234]
    assert not socket_path.exists()


def test_permission_denied_process_cleanup_is_not_reported_successful(tmp_path):
    process = OwnedProcess(pid=1234, birth="birth", executable="/bin/codex")
    result = NativeRuntimeConfig.cleanup_owned(
        [process], [], kill=lambda _pid: (_ for _ in ()).throw(PermissionError("denied"))
    )
    assert result["pids"] == []


def test_real_pty_driver_round_trips_dummy_input_and_waits_for_exit(tmp_path):
    driver = PTYDriver.spawn(
        [sys.executable, "-c", "import sys; data=sys.stdin.readline(); print('reply:'+data, end='', flush=True)"],
        cwd=tmp_path,
    )
    try:
        driver.write(b"ping\n", action="dummy-input")
        driver.read(1.0)
        assert b"reply:ping" in driver.screen_tail
        assert driver.wait(2.0) == 0
        assert driver.evidence["inputs"][0]["action"] == "dummy-input"
    finally:
        driver.close()


def test_async_pty_read_yields_to_heartbeat_task(tmp_path):
    import asyncio

    async def scenario():
        driver = PTYDriver.spawn(
            [sys.executable, "-c", "import time; time.sleep(.25)"],
            cwd=tmp_path,
        )
        ticks = 0

        async def heartbeat():
            nonlocal ticks
            for _ in range(8):
                ticks += 1
                await asyncio.sleep(0.01)

        try:
            await asyncio.gather(driver.aread_until(raw_seconds() + 0.2), heartbeat())
            assert ticks == 8
        finally:
            if driver.poll() is None:
                driver.terminate()
            driver.close()

    asyncio.run(scenario())


def test_status_and_clear_commands_use_exact_bracketed_paste_then_cr(tmp_path):
    driver = PTYDriver.spawn(
        [sys.executable, "-c", "import time; time.sleep(1)"],
        cwd=tmp_path,
    )
    try:
        driver.command("/status")
        driver.command("/clear")
        assert driver.input_bytes == (
            b"\x1b[200~/status\x1b[201~",
            b"\r",
            b"\x1b[200~/clear\x1b[201~",
            b"\r",
        )
    finally:
        if driver.poll() is None:
            driver.terminate()
        driver.close()


def test_real_pty_driver_answers_cursor_position_query(tmp_path):
    driver = PTYDriver.spawn(
        [sys.executable, "-c", "import os,tty; tty.setraw(0); os.write(1,b'\\x1b[6n'); os.read(0,6); os.write(1,b'cursor-ok')"],
        cwd=tmp_path,
    )
    try:
        driver.read(1.0)
        assert driver.evidence["cursor_position_replies"] == 1
        assert b"cursor-ok" in driver.screen_tail
    finally:
        if driver.poll() is None:
            driver.terminate()
        driver.close()


def test_owned_cleanup_does_not_remove_replaced_endpoint(tmp_path):
    socket_path = tmp_path / "frontend.sock"
    socket_path.write_bytes(b"old")
    old = socket_path.stat()
    endpoint = OwnedEndpoint(socket_path, old.st_dev, old.st_ino)
    socket_path.unlink()
    socket_path.write_bytes(b"new")
    killed = []
    result = NativeRuntimeConfig.cleanup_owned([], [endpoint], kill=lambda pid: killed.append(pid))
    assert result["endpoints"] == []
    assert socket_path.read_bytes() == b"new"


def test_runtime_refuses_an_existing_backend_socket_before_spawning(tmp_path):
    cli = tmp_path / "codex"
    cli.write_bytes(b"native")
    frontend = tmp_path / "frontend.sock"
    backend = tmp_path / "backend.sock"
    backend.write_bytes(b"foreign")
    spawned = []
    runtime = NativeRuntime(
        NativeRuntimeConfig(frontend_socket=frontend, backend_socket=backend, cli=cli),
        spawn_process=lambda *args, **kwargs: spawned.append((args, kwargs)),
    )
    with pytest.raises(FileExistsError):
        __import__("asyncio").run(runtime.start())
    assert spawned == []


def test_binary_hash_is_explicitly_recorded(tmp_path):
    binary = tmp_path / "codex"
    binary.write_bytes(b"native")
    config = NativeRuntimeConfig(frontend_socket=tmp_path / "frontend.sock", backend_socket=tmp_path / "backend.sock", cli=binary)
    assert config.binary_sha256 == hashlib.sha256(b"native").hexdigest()


def test_runtime_config_normalizes_path_inputs(tmp_path):
    config = NativeRuntimeConfig(
        frontend_socket=str(tmp_path / "frontend.sock"),
        backend_socket=str(tmp_path / "backend.sock"),
        cli=str(tmp_path / "codex"),
    )
    assert isinstance(config.frontend_socket, Path)
    assert isinstance(config.backend_socket, Path)
    assert isinstance(config.cli, Path)


def test_backend_argv_uses_native_unix_uri_for_private_socket(tmp_path):
    config = NativeRuntimeConfig(
        frontend_socket=tmp_path / "frontend.sock",
        backend_socket=tmp_path / "backend.sock",
        cli=tmp_path / "codex",
    )
    assert config.backend_argv[-1] == f"unix://{tmp_path / 'backend.sock'}"


def test_native_binary_must_match_frozen_sha_before_start(tmp_path):
    binary = tmp_path / "codex"
    binary.write_bytes(b"native")
    config = NativeRuntimeConfig(
        frontend_socket=tmp_path / "frontend.sock",
        backend_socket=tmp_path / "backend.sock",
        cli=binary,
    )
    with pytest.raises(RuntimeInvariantError):
        config.require_frozen_binary()


def test_hash_failure_during_close_still_finishes_owned_cleanup(tmp_path, monkeypatch):
    binary = tmp_path / "codex"
    binary.write_bytes(b"native")
    config = NativeRuntimeConfig(
        frontend_socket=tmp_path / "frontend.sock",
        backend_socket=tmp_path / "backend.sock",
        cli=binary,
    )
    from proxy_native_runtime import RuntimePhase

    runtime = NativeRuntime(config)
    runtime.phase = RuntimePhase.BACKEND_RUNNING
    monkeypatch.setattr("proxy_native_runtime.FIXED_NATIVE_SHA256", hashlib.sha256(b"native").hexdigest())
    runtime._expected_hashes = config.snapshot_hashes()
    binary.write_bytes(b"changed")
    with pytest.raises(RuntimeInvariantError):
        __import__("asyncio").run(runtime.close())
    assert runtime.phase is RuntimePhase.CLOSED


def test_runtime_can_disconnect_one_owned_proxy_leg_without_stopping_listener(tmp_path):
    import asyncio

    class Relay:
        def __init__(self):
            self.calls = []

        async def disconnect(self, connection_id, *, leg):
            self.calls.append((connection_id, leg))
            return True

    config = NativeRuntimeConfig(
        frontend_socket=tmp_path / "frontend.sock",
        backend_socket=tmp_path / "backend.sock",
        cli=tmp_path / "codex",
    )
    runtime = NativeRuntime(config)
    runtime._relay = Relay()
    assert asyncio.run(runtime.disconnect("conn-1", leg="frontend")) is True
    assert runtime._relay.calls == [("conn-1", "frontend")]


def test_relay_close_failure_still_runs_owned_process_cleanup(tmp_path):
    import asyncio

    class Relay:
        async def close(self):
            raise RuntimeError("relay close failed")

    class Process:
        def __init__(self):
            self.calls = []

        def poll(self):
            return None

        def terminate(self):
            self.calls.append("terminate")

        def wait(self, timeout=None):
            self.calls.append(("wait", timeout))
            return 0

    config = NativeRuntimeConfig(
        frontend_socket=tmp_path / "frontend.sock",
        backend_socket=tmp_path / "backend.sock",
        cli=tmp_path / "codex",
    )
    runtime = NativeRuntime(config)
    runtime._relay = Relay()
    runtime._backend = Process()
    runtime._owned_processes = []
    with pytest.raises(RuntimeError, match="relay close failed"):
        asyncio.run(runtime.close())
    assert runtime.phase.value == "closed"


def test_unknown_peer_is_rejected_without_reading_or_capturing_as_tui(tmp_path):
    from types import SimpleNamespace

    config = NativeRuntimeConfig(
        frontend_socket=tmp_path / "frontend.sock",
        backend_socket=tmp_path / "backend.sock",
        cli=tmp_path / "codex",
    )
    runtime = NativeRuntime(config)
    runtime._tui = object()
    runtime._owned_processes = [OwnedProcess(1234, "birth", "/bin/codex")]
    assert runtime._authorize_tui(
        SimpleNamespace(pid=1234, birth="birth", executable=None, complete=False)
    ) is False


def test_capture_bridge_retains_both_os_peer_identities_and_fds():
    from proxy_observer import Observer

    frontend = SimpleNamespace(pid=11, birth="tui-birth", executable="/bin/codex", complete=True)
    backend = SimpleNamespace(pid=22, birth="server-birth", executable="/bin/codex", complete=True)
    bridge = _CaptureBridge(
        Observer(),
        AttachmentInferer(),
        frontend_identity=OwnedProcess(11, "tui-birth", "/bin/codex"),
        backend_identity=OwnedProcess(22, "server-birth", "/bin/codex"),
        strict_identity=True,
    )
    bridge.on_connect(
        SimpleNamespace(
            connection_id="c1",
            epoch=1,
            frontend_fd=31,
            backend_fd=32,
            frontend_peer=frontend,
            backend_peer=backend,
        )
    )
    record = bridge.connection_records["c1"]
    assert (record.frontend_fd, record.backend_fd, record.epoch) == (31, 32, 1)
    assert record.frontend_peer.pid == 11
    assert record.backend_peer.pid == 22


def test_capture_bridge_trace_overflow_invalidates_and_preserves_only_whitelist():
    from proxy_observer import Observer

    inferer = AttachmentInferer()
    bridge = _CaptureBridge(Observer(), inferer, max_trace_events=1)
    bridge.on_connect(SimpleNamespace(connection_id="c1", epoch=1, frontend_fd=1, backend_fd=2, frontend_peer=None, backend_peer=None))
    bridge.on_gap(SimpleNamespace(connection_id="c1", epoch=1, reason="transport_gap"))
    assert bridge.trace_complete is False
    assert inferer.invalid_reason == "trace_overflow"
    assert all(set(row) <= {"event", "local_seq", "conn_epoch", "direction", "method", "request_id", "request_direction", "response", "ok", "thread", "reason"} for row in bridge.trace)


def test_observer_gap_sizes_are_preserved_through_capture_bridge():
    from proxy_observer import Observer

    observer = Observer(max_frame_bytes=4)
    bridge = _CaptureBridge(observer, AttachmentInferer())
    connection_id = "gap-size-c1"
    bridge.on_connect(SimpleNamespace(connection_id=connection_id, epoch=1))
    relay_bytes(bridge, connection_id, 1, "frontend_to_backend", raw_upgrade_request())
    relay_bytes(bridge, connection_id, 1, "backend_to_frontend", raw_upgrade_response())
    relay_bytes(
        bridge,
        connection_id,
        1,
        "frontend_to_backend",
        raw_ws_frame({"oversized": "payload"}, client=True),
    )
    gap = next(row for row in bridge.trace if row.get("event") == "gap")
    assert gap["payload_bytes"] > gap["limit_bytes"] == 4
    assert bridge.trace_complete is False


def test_capture_bridge_drops_invalid_gap_size_metadata():
    from proxy_observer import Observer

    bridge = _CaptureBridge(Observer(), AttachmentInferer())
    bridge.on_connect(SimpleNamespace(connection_id="invalid-gap-c1", epoch=1))
    bridge._accept_rows(
        [
            {
                "event": "gap",
                "conn_epoch": 1,
                "local_seq": 2,
                "payload_bytes": True,
                "message_bytes": -1,
                "limit_bytes": "4096",
                "reason": "frame_too_large",
            }
        ]
    )
    gap = bridge.trace[-1]
    assert all(key not in gap for key in ("payload_bytes", "message_bytes", "limit_bytes"))
    assert bridge.trace_complete is False


def test_duplicate_eof_close_rows_make_trace_incomplete():
    from proxy_observer import Observer

    bridge = _CaptureBridge(Observer(), AttachmentInferer())
    bridge.on_connect(SimpleNamespace(connection_id="c1", epoch=1, frontend_fd=1, backend_fd=2, frontend_peer=None, backend_peer=None))
    event = SimpleNamespace(connection_id="c1", epoch=1, kind="eof")
    bridge.on_lifecycle(event)
    bridge.on_lifecycle(event)
    assert bridge.trace_complete is False


def test_duplicate_observer_local_sequence_makes_trace_incomplete():
    from proxy_observer import Observer

    bridge = _CaptureBridge(Observer(), AttachmentInferer())
    bridge.on_connect(SimpleNamespace(connection_id="c1", epoch=1, frontend_fd=1, backend_fd=2, frontend_peer=None, backend_peer=None))
    row = {"event": "websocket_upgrade", "conn_epoch": 1, "local_seq": 2, "direction": "client"}
    bridge._accept_rows([row, row])
    assert bridge.trace_complete is False
    assert bridge.inferer.invalid_reason == "trace_duplicate_sequence"


def test_model_turn_count_comes_from_observed_client_turn_start():
    from proxy_observer import Observer

    bridge = _CaptureBridge(Observer(), AttachmentInferer())
    bridge.on_connect(SimpleNamespace(connection_id="c1", epoch=1, frontend_fd=1, backend_fd=2, frontend_peer=None, backend_peer=None))
    relay_bytes(bridge, "c1", 1, "frontend_to_backend", raw_upgrade_request())
    relay_bytes(bridge, "c1", 1, "backend_to_frontend", raw_upgrade_response())
    rpc(bridge, "c1", 1, "client", {"jsonrpc": "2.0", "id": 9, "method": "turn/start", "params": {}})
    assert bridge.model_turns == 1
    assert bridge.trace_complete is True


def test_runtime_backend_authorizer_requires_the_registered_native_identity(tmp_path):
    config = NativeRuntimeConfig(
        frontend_socket=tmp_path / "frontend.sock",
        backend_socket=tmp_path / "backend.sock",
        cli=tmp_path / "codex",
    )
    runtime = NativeRuntime(config)
    runtime._backend_identity = OwnedProcess(22, "server-birth", "/bin/codex")
    good = SimpleNamespace(pid=22, birth="server-birth", executable="/bin/codex", complete=True)
    foreign = SimpleNamespace(pid=23, birth="server-birth", executable="/bin/codex", complete=True)
    assert runtime._authorize_backend(good) is True
    assert runtime._authorize_backend(foreign) is False


def test_reconnect_epoch_ignores_history_before_disconnect_marker():
    before = {1, 2}
    records = [SimpleNamespace(epoch=1), SimpleNamespace(epoch=2)]
    assert new_epoch_after_disconnect(before, records) is False
    records.append(SimpleNamespace(epoch=3))
    assert new_epoch_after_disconnect(before, records) is True


def test_case_directory_and_artifacts_are_exclusive(tmp_path):
    from proxy_native_runtime import CaseDirectory

    case = CaseDirectory.create(tmp_path / "proxy-attach-01")
    case.write_json("result.json", {"case": "proxy-attach-01"})
    with pytest.raises(FileExistsError):
        case.write_json("result.json", {"case": "replacement"})
    with pytest.raises(FileExistsError):
        CaseDirectory.create(tmp_path / "proxy-attach-01")


def test_case_label_comes_from_actual_case_directory(tmp_path):
    from proxy_native_runtime import CaseDirectory

    case = CaseDirectory.create(tmp_path / "proxy-attach-02")
    assert case.name == "proxy-attach-02"
