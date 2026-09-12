import json
import base64
import hashlib
import struct

import pytest

from proxy_observer import Observer


def upgrade_request(*, extensions=None):
    extension = "" if extensions is None else f"Sec-WebSocket-Extensions: {extensions}\r\n"
    return (
        b"GET / HTTP/1.1\r\n"
        b"Host: localhost\r\n"
        b"Upgrade: websocket\r\n"
        b"Connection: Upgrade\r\n"
        b"Sec-WebSocket-Version: 13\r\n"
        b"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n"
        + extension.encode()
        + b"\r\n"
    )


def upgrade_response(*, extensions=None):
    extension = "" if extensions is None else f"Sec-WebSocket-Extensions: {extensions}\r\n"
    return (
        b"HTTP/1.1 101 Switching Protocols\r\n"
        b"Upgrade: websocket\r\n"
        b"Connection: Upgrade\r\n"
        b"Sec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\n"
        + extension.encode()
        + b"\r\n"
    )


def frame(payload, *, opcode=1, fin=True, masked=False, mask=b"\x01\x02\x03\x04"):
    first = (0x80 if fin else 0) | opcode
    size = len(payload)
    if size < 126:
        header = bytes((first, (0x80 if masked else 0) | size))
    elif size <= 0xFFFF:
        header = bytes((first, (0x80 if masked else 0) | 126)) + struct.pack(">H", size)
    else:
        header = bytes((first, (0x80 if masked else 0) | 127)) + struct.pack(">Q", size)
    if not masked:
        return header + payload
    encoded = bytes(byte ^ mask[i % 4] for i, byte in enumerate(payload))
    return header + mask + encoded


def rpc_frame(value, *, direction="client", **kwargs):
    payload = json.dumps(value, separators=(",", ":")).encode()
    return frame(payload, masked=direction == "client", **kwargs)


def workspace_probe_params(*, cwd="/repo"):
    return {
        "command": ["git", "-c", "safe.bareRepository=explicit", "branch", "--show-current"],
        "cwd": cwd,
        "env": {"GIT_OPTIONAL_LOCKS": "0"},
        "timeoutMs": 5000,
        "outputBytesCap": 65536,
        "tty": False,
        "streamStdin": False,
        "streamStdoutStderr": False,
        "disableOutputCap": False,
        "disableTimeout": False,
        "processId": None,
        "size": None,
        "sandboxPolicy": None,
        "permissionProfile": None,
    }


def connected_observer():
    observer = Observer()
    observer.open("c1")
    observer.feed("c1", "client", upgrade_request())
    observer.feed("c1", "server", upgrade_response())
    return observer


def events(observer):
    return observer.drain_events()


def test_fragmented_masked_client_json_is_reassembled_and_whitelisted():
    observer = connected_observer()
    message = {"id": "r1", "method": "skills/list", "params": {"cwds": ["/repo"], "forceReload": True, "secret": "CANARY"}}
    payload = json.dumps(message, separators=(",", ":")).encode()
    first = frame(payload[:7], fin=False, masked=True)
    second = frame(payload[7:], opcode=0, fin=True, masked=True)

    observer.feed("c1", "client", first[:3])
    observer.feed("c1", "client", first[3:] + second)
    rows = events(observer)
    rpc = [row for row in rows if row.get("event") == "rpc"][-1]
    assert rpc["direction"] == "client"
    assert rpc["request_id"] == "r1"
    assert rpc["method"] == "skills/list"
    assert rpc["params"] == {"cwds": ["/repo"], "forceReload": True}
    assert "CANARY" not in json.dumps(rows)


def test_native_codex_wire_omits_jsonrpc_version_field():
    observer = connected_observer()
    request = {
        "id": 1,
        "method": "initialize",
        "params": {"clientInfo": {"name": "fixture", "version": "0"}},
    }
    response = {"id": 1, "result": {"serverInfo": {"name": "fixture"}}}

    observer.feed("c1", "client", rpc_frame(request, direction="client"))
    observer.feed("c1", "server", rpc_frame(response, direction="server"))
    rows = events(observer)

    request_row = [row for row in rows if row.get("event") == "rpc"][-1]
    assert request_row["direction"] == "client"
    assert request_row["request_id"] == 1
    assert request_row["method"] == "initialize"
    response_row = [row for row in rows if row.get("event") == "rpc_response"][-1]
    assert response_row["direction"] == "server"
    assert response_row["request_id"] == 1
    assert response_row["ok"] is True
    assert not any(row.get("event") == "gap" for row in rows)


def test_native_notification_and_error_response_shapes_omit_jsonrpc_version_field():
    observer = connected_observer()
    request = {"id": 2, "method": "thread/read", "params": {"threadId": "t1"}}
    notification = {"method": "initialized"}
    error = {"id": 2, "error": {"code": -32601, "message": "method not found"}}

    observer.feed("c1", "client", rpc_frame(request, direction="client"))
    observer.feed("c1", "server", rpc_frame(notification, direction="server"))
    observer.feed("c1", "server", rpc_frame(error, direction="server"))
    rows = events(observer)

    notification_row = [row for row in rows if row.get("event") == "rpc" and row.get("method") == "initialized"][-1]
    assert notification_row["request_id"] is None
    error_row = [row for row in rows if row.get("event") == "rpc_response"][-1]
    assert error_row["request_id"] == 2
    assert error_row["ok"] is False
    assert not any(row.get("event") == "gap" for row in rows)


def test_native_error_response_missing_required_error_fields_is_fail_closed():
    observer = connected_observer()
    request = {"id": 3, "method": "thread/read", "params": {"threadId": "t1"}}
    malformed_error = {"id": 3, "error": {"message": "method not found"}}

    observer.feed("c1", "client", rpc_frame(request, direction="client"))
    observer.feed("c1", "server", rpc_frame(malformed_error, direction="server"))
    rows = events(observer)

    assert any(row.get("event") == "gap" and row.get("reason") == "invalid_json_rpc" for row in rows)
    assert not any(row.get("event") == "rpc_response" for row in rows)
    assert observer.attachment_state("c1") == "PENDING"


def test_command_exec_workspace_probe_qualification_does_not_retain_body():
    observer = Observer(expected_workspace_cwd="/repo")
    observer.open("c1")
    observer.feed("c1", "client", upgrade_request())
    observer.feed("c1", "server", upgrade_response())
    request = {
        "id": "probe-1",
        "method": "command/exec",
        "params": workspace_probe_params(),
    }
    response = {
        "id": "probe-1",
        "result": {"exitCode": 0, "stdout": "CANARY", "stderr": "SECRET"},
    }

    observer.feed("c1", "client", rpc_frame(request, direction="client"))
    observer.feed("c1", "server", rpc_frame(response, direction="server"))
    rows = events(observer)

    request_row = [
        row for row in rows
        if row.get("event") == "rpc_unknown" and row.get("response") is not True
    ][-1]
    assert request_row["method"] == "command/exec"
    assert request_row["request_id"] == "probe-1"
    assert request_row["workspace_probe_qualified"] is True
    response_row = [row for row in rows if row.get("event") == "rpc_unknown" and row.get("response") is True][-1]
    assert response_row["request_id"] == "probe-1"
    assert "workspace_probe_qualified" not in response_row
    assert "CANARY" not in json.dumps(rows)
    assert "SECRET" not in json.dumps(rows)


@pytest.mark.parametrize(
    "mutation",
    [
        lambda params: params["command"].append("--porcelain"),
        lambda params: params.__setitem__("command", ["gh", "pr", "view"]),
        lambda params: params.__setitem__("cwd", "/other"),
        lambda params: params.__setitem__("env", {"GIT_OPTIONAL_LOCKS": "0", "EXTRA": "1"}),
        lambda params: params.__setitem__("timeoutMs", 5001),
        lambda params: params.__setitem__("timeoutMs", True),
        lambda params: params.__setitem__("outputBytesCap", 65535),
        lambda params: params.__setitem__("tty", True),
        lambda params: params.__setitem__("processId", 7),
        lambda params: params.__setitem__("unknownKey", "SENSITIVE_UNKNOWN"),
    ],
)
def test_command_exec_workspace_probe_mutations_are_false(mutation):
    observer = Observer(expected_workspace_cwd="/repo")
    observer.open("c1")
    observer.feed("c1", "client", upgrade_request())
    observer.feed("c1", "server", upgrade_response())
    params = workspace_probe_params()
    mutation(params)
    observer.feed("c1", "client", rpc_frame({"id": 1, "method": "command/exec", "params": params}, direction="client"))
    rows = events(observer)
    request_row = [row for row in rows if row.get("event") == "rpc_unknown"][-1]
    assert request_row["workspace_probe_qualified"] is False
    assert "SENSITIVE_UNKNOWN" not in json.dumps(rows)


def test_command_exec_workspace_probe_requires_expected_cwd_and_client_id():
    cases = (
        ("server", {"id": 1}, "/repo"),
        ("client", {}, "/repo"),
        ("client", {"id": 1}, None),
    )
    for direction, identity, expected_cwd in cases:
        observer = Observer(expected_workspace_cwd=expected_cwd)
        observer.open("c1")
        observer.feed("c1", "client", upgrade_request())
        observer.feed("c1", "server", upgrade_response())
        request = {**identity, "method": "command/exec", "params": workspace_probe_params()}
        observer.feed(
            "c1",
            direction,
            rpc_frame(request, direction=direction),
        )
        rows = events(observer)
        request_row = [
            row for row in rows
            if row.get("event") == "rpc_unknown" and row.get("response") is not True
        ][-1]
        assert request_row["workspace_probe_qualified"] is False


def test_command_exec_workspace_probe_accepts_serde_default_omissions():
    observer = Observer(expected_workspace_cwd="/repo")
    observer.open("c1")
    observer.feed("c1", "client", upgrade_request())
    observer.feed("c1", "server", upgrade_response())
    params = workspace_probe_params()
    for key in (
        "tty",
        "streamStdin",
        "streamStdoutStderr",
        "disableOutputCap",
        "disableTimeout",
        "processId",
        "size",
        "sandboxPolicy",
        "permissionProfile",
    ):
        params.pop(key)
    observer.feed(
        "c1",
        "client",
        rpc_frame({"id": 1, "method": "command/exec", "params": params}, direction="client"),
    )
    request_row = [
        row for row in events(observer)
        if row.get("event") == "rpc_unknown" and row.get("response") is not True
    ][-1]
    assert request_row["workspace_probe_qualified"] is True


def test_explicit_jsonrpc_version_is_accepted_only_for_2_0():
    observer = connected_observer()
    observer.feed("c1", "client", rpc_frame({"jsonrpc": "2.0", "id": 4, "method": "initialize"}, direction="client"))
    assert any(row.get("event") == "rpc" and row.get("request_id") == 4 for row in events(observer))

    for version in ("1.0", None, 2):
        observer = connected_observer()
        observer.feed("c1", "client", rpc_frame({"jsonrpc": version, "id": 4, "method": "initialize"}, direction="client"))
        assert any(row.get("event") == "gap" and row.get("reason") == "invalid_json_rpc" for row in events(observer))


def test_coalesced_server_response_correlates_only_client_request():
    observer = connected_observer()
    request = {"id": 7, "method": "thread/read", "params": {"threadId": "t1", "includeTurns": False}}
    response = {"id": 7, "result": {"thread": {"id": "t1", "sessionId": "s1", "parentThreadId": "p1", "forkedFromId": "p1", "ephemeral": True, "cwd": "/repo", "secret": "CANARY"}}}
    observer.feed("c1", "client", rpc_frame(request, direction="client"))
    observer.feed("c1", "server", rpc_frame({"id": "same", "method": "item/requestUserInput", "params": {"question": "CANARY"}}, direction="server") + rpc_frame(response, direction="server"))
    rows = events(observer)
    response_row = [row for row in rows if row.get("event") == "rpc_response"][-1]
    assert response_row["direction"] == "server"
    assert response_row["request_direction"] == "client"
    assert response_row["ok"] is True
    assert response_row["thread"] == {"id": "t1", "sessionId": "s1", "parentThreadId": "p1", "forkedFromId": "p1", "ephemeral": True, "cwd": "/repo"}
    unknown = [row for row in rows if row.get("event") == "rpc_unknown"][-1]
    assert unknown["direction"] == "server"
    assert unknown["request_id"] == "same"
    assert unknown["opaque"] is True
    assert "CANARY" not in json.dumps(rows)


def test_duplicate_json_keys_make_gap_and_never_emit_partial_rpc():
    observer = connected_observer()
    payload = b'{"id":1,"method":"thread/read","method":"skills/list"}'
    observer.feed("c1", "client", frame(payload, masked=True))
    rows = events(observer)
    assert any(row.get("event") == "gap" and row.get("reason") == "duplicate_json_key" for row in rows)
    assert not any(row.get("event") == "rpc" for row in rows)
    assert observer.attachment_state("c1") == "PENDING"


def test_extensions_and_wrong_masking_are_gaps():
    observer = Observer()
    observer.open("c1")
    observer.feed("c1", "client", upgrade_request(extensions="permessage-deflate"))
    assert any(row.get("reason") == "unsupported_extension" for row in events(observer))
    assert observer.attachment_state("c1") == "PENDING"

    observer = connected_observer()
    observer.feed("c1", "server", frame(b'{}', masked=True))
    assert any(row.get("reason") == "server_frame_masked" for row in events(observer))


def test_limits_and_eof_emit_gap_without_retaining_payload():
    observer = Observer(max_frame_bytes=32, max_message_bytes=64)
    observer.open("c1")
    observer.feed("c1", "client", upgrade_request())
    observer.feed("c1", "server", upgrade_response())
    observer.feed("c1", "client", frame(b"x" * 33, masked=True))
    rows = events(observer)
    gap = [row for row in rows if row.get("reason") == "frame_too_large"][-1]
    assert gap["payload_bytes"] == 33
    assert gap["limit_bytes"] == 32
    assert "x" * 33 not in json.dumps(rows)

    observer = Observer(max_frame_bytes=64, max_message_bytes=10)
    observer.open("c1")
    observer.feed("c1", "client", upgrade_request())
    observer.feed("c1", "server", upgrade_response())
    observer.feed("c1", "client", frame(b"x" * 8, fin=False, masked=True))
    observer.feed("c1", "client", frame(b"y" * 3, opcode=0, masked=True))
    rows = events(observer)
    gap = [row for row in rows if row.get("reason") == "message_too_large"][-1]
    assert gap["message_bytes"] == 11
    assert gap["limit_bytes"] == 10
    assert "x" * 8 not in json.dumps(rows)
    assert "y" * 3 not in json.dumps(rows)

    observer = connected_observer()
    observer.feed("c1", "client", frame(b'{"id":1,"method":"thread/read"', fin=False, masked=True))
    observer.close("c1")
    rows = events(observer)
    assert any(row.get("event") == "gap" and row.get("reason") == "incomplete_message" for row in rows)
    assert rows[-1]["event"] == "connection_close"
    assert observer.attachment_state("c1") == "DISCONNECTED"


def test_deep_json_is_bounded_by_parser_failure_and_emits_no_payload():
    observer = connected_observer()
    payload = b"[" * 10000 + b"]" * 10000
    observer.feed("c1", "client", frame(payload, masked=True))
    rows = events(observer)
    assert any(row.get("event") == "gap" and row.get("reason") == "invalid_json" for row in rows)
    assert "[" * 10000 not in json.dumps(rows)


def test_unknown_method_and_unknown_notification_are_preserved_as_opaque():
    observer = connected_observer()
    observer.feed("c1", "client", rpc_frame({"id": "u", "method": "vendor/secret", "params": {"text": "CANARY"}}, direction="client"))
    observer.feed("c1", "server", rpc_frame({"method": "VendorEvent", "params": {"text": "CANARY"}}, direction="server"))
    rows = events(observer)
    unknown = [row for row in rows if row.get("event") == "rpc_unknown"]
    assert [(row["direction"], row.get("request_id"), row["method"]) for row in unknown] == [("client", "u", "vendor/secret"), ("server", None, "VendorEvent")]
    assert all(row["opaque"] for row in unknown)
    assert all("workspace_probe_qualified" not in row for row in unknown)
    assert "CANARY" not in json.dumps(rows)


def test_close_unknown_connection_is_safe_and_new_epoch_does_not_reuse_pending_ids():
    observer = Observer()
    observer.close("missing")
    assert any(row.get("event") == "gap" and row.get("reason") == "unknown_connection" for row in events(observer))
    observer.open("c1")
    observer.feed("c1", "client", upgrade_request())
    observer.feed("c1", "server", upgrade_response())
    observer.feed("c1", "client", rpc_frame({"id": 1, "method": "thread/read", "params": {"threadId": "old"}}, direction="client"))
    observer.close("c1")
    observer.open("c1")
    observer.feed("c1", "client", upgrade_request())
    observer.feed("c1", "server", upgrade_response())
    observer.feed("c1", "server", rpc_frame({"id": 1, "result": {}}, direction="server"))
    rows = events(observer)
    assert any(row.get("event") == "rpc_unknown" and row.get("request_id") == 1 and row.get("method") is None for row in rows)
    assert len({row["conn_epoch"] for row in rows if "conn_epoch" in row}) >= 2


def test_upgrade_and_first_frame_can_share_one_chunk_and_control_frame_can_interleave():
    observer = Observer()
    observer.open("c1")
    payload = {"id": "r1", "method": "thread/start", "params": {"cwd": "/repo"}}
    observer.feed("c1", "client", upgrade_request() + frame(json.dumps(payload).encode(), masked=True))
    observer.feed("c1", "server", upgrade_response())
    observer.feed("c1", "client", frame(b"x", opcode=9, masked=True) + frame(b"", opcode=10, masked=True))
    rows = events(observer)
    assert any(row.get("event") == "rpc" and row.get("method") == "thread/start" for row in rows)
    assert [row["opcode"] for row in rows if row.get("event") == "ws_control"] == ["ping", "pong"]
    assert observer.attachment_state("c1") == "UNBOUND"


def test_unknown_thread_list_response_is_opaque_and_does_not_retain_history():
    observer = connected_observer()
    observer.feed("c1", "client", rpc_frame({"id": "list", "method": "thread/list", "params": {}}, direction="client"))
    observer.feed("c1", "server", rpc_frame({"id": "list", "result": {"data": [{"text": "CANARY"}]}}, direction="server"))
    rows = events(observer)
    assert any(row.get("event") == "rpc_unknown" and row.get("method") == "thread/list" and row.get("response") for row in rows)
    assert "CANARY" not in json.dumps(rows)


def test_thread_started_keeps_only_safe_thread_metadata():
    observer = connected_observer()
    observer.feed("c1", "server", rpc_frame({"method": "thread/started", "params": {"thread": {"id": "t1", "sessionId": "s1", "cwd": "/repo", "text": "CANARY"}}}, direction="server"))
    rows = events(observer)
    started = [row for row in rows if row.get("event") == "rpc" and row.get("method") == "thread/started"][-1]
    assert started["thread"] == {"id": "t1", "sessionId": "s1", "cwd": "/repo"}
    assert "CANARY" not in json.dumps(rows)


def test_event_overflow_invalidates_epoch_and_callback_matches_drain():
    callback_rows = []
    observer = Observer(max_events=2, emit=callback_rows.append)
    observer.open("c1")
    observer.feed("c1", "client", upgrade_request())
    observer.feed("c1", "server", upgrade_response())
    drained = observer.drain_events()
    assert callback_rows == drained
    assert drained[-1]["event"] == "gap"
    assert drained[-1]["reason"] == "event_queue_overflow"
    assert drained[-1]["conn_epoch"] == 1
    assert observer.attachment_state("c1") == "PENDING"


def test_eof_mid_upgrade_is_a_gap_but_empty_probe_is_only_unupgraded_close():
    observer = Observer()
    observer.open("partial")
    observer.feed("partial", "client", b"GET / HTTP/1.1\r\nHost: local")
    observer.close("partial")
    rows = events(observer)
    assert any(row.get("event") == "gap" and row.get("reason") == "incomplete_upgrade" for row in rows)
    assert rows[-1]["event"] == "connection_close"

    observer = Observer()
    observer.open("probe")
    observer.close("probe")
    rows = events(observer)
    assert not any(row.get("event") == "gap" for row in rows)
    assert rows[-1]["event"] == "connection_close"
    assert observer.attachment_state("probe") == "DISCONNECTED"

    observer = Observer()
    observer.open("one-sided")
    observer.feed("one-sided", "client", upgrade_request())
    observer.close("one-sided")
    rows = events(observer)
    assert any(row.get("event") == "gap" and row.get("reason") == "incomplete_upgrade" for row in rows)


def test_upgrade_header_limit_applies_when_terminator_is_in_same_chunk():
    observer = Observer(max_header_bytes=32)
    observer.open("c1")
    observer.feed("c1", "client", b"x" * 40 + b"\r\n\r\n")
    rows = events(observer)
    assert any(row.get("event") == "gap" and row.get("reason") == "header_too_large" for row in rows)
    assert observer.attachment_state("c1") == "PENDING"


def test_nonfinite_json_and_invalid_close_payloads_make_gaps():
    observer = connected_observer()
    payload = b'{"id":1,"method":"thread/read","params":{"threadId":NaN}}'
    observer.feed("c1", "client", frame(payload, masked=True))
    assert any(row.get("reason") == "invalid_json" for row in events(observer))

    for payload in (b"\x03", b"\x03\xed", b"\x03\xee", b"\x03\xf7", b"\x03\xe8\xff"):
        observer = connected_observer()
        observer.feed("c1", "client", frame(payload, opcode=8, masked=True))
        rows = events(observer)
        assert any(row.get("event") == "gap" and row.get("reason") == "invalid_close_payload" for row in rows)


def test_connection_epoch_cannot_be_reused_across_connections_or_after_close():
    observer = Observer()
    observer.open("c1", conn_epoch=19)
    observer.close("c1")
    with pytest.raises(ValueError):
        observer.open("c2", conn_epoch=19)
    with pytest.raises(ValueError):
        observer.open("c1", conn_epoch=19)


def test_upgrade_requires_websocket_version_key_and_matching_accept():
    observer = Observer()
    observer.open("bad-version")
    observer.feed("bad-version", "client", upgrade_request().replace(b"Sec-WebSocket-Version: 13", b"Sec-WebSocket-Version: 12"))
    assert any(row.get("reason") == "invalid_upgrade_request" for row in events(observer))

    observer = Observer()
    observer.open("bad-key")
    observer.feed("bad-key", "client", upgrade_request().replace(b"dGhlIHNhbXBsZSBub25jZQ==", b"not-base64"))
    assert any(row.get("reason") == "invalid_upgrade_request" for row in events(observer))

    observer = Observer()
    observer.open("bad-accept")
    observer.feed("bad-accept", "client", upgrade_request())
    observer.feed("bad-accept", "server", upgrade_response().replace(b"s3pPLMBiTxaQ9kYGzzhZRbK+xOo=", b"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="))
    rows = events(observer)
    assert any(row.get("reason") == "invalid_upgrade_response" for row in rows)
    assert not any(row.get("event") == "websocket_upgrade" and row.get("direction") == "server" for row in rows)

    key = b"dGhlIHNhbXBsZSBub25jZQ=="
    accept = base64.b64encode(hashlib.sha1(key + b"258EAFA5-E914-47DA-95CA-C5AB0DC85B11").digest())
    observer = Observer()
    observer.open("good")
    observer.feed("good", "client", upgrade_request())
    observer.feed("good", "server", upgrade_response().replace(b"s3pPLMBiTxaQ9kYGzzhZRbK+xOo=", accept))
    assert not any(row.get("event") == "gap" for row in events(observer))


def test_upgrade_tokens_are_exact_and_close_1012_to_1014_are_valid():
    observer = Observer()
    observer.open("bad-token")
    observer.feed("bad-token", "client", upgrade_request().replace(b"Upgrade: websocket", b"Upgrade: websocket-other"))
    assert any(row.get("reason") == "invalid_upgrade_request" for row in events(observer))

    for code in (1012, 1013, 1014):
        observer = connected_observer()
        observer.feed("c1", "client", frame(code.to_bytes(2, "big"), opcode=8, masked=True))
        rows = events(observer)
        assert not any(row.get("event") == "gap" for row in rows)
        assert any(row.get("event") == "ws_control" and row.get("opcode") == "close" for row in rows)


def test_pending_request_cwd_items_and_integer_ids_have_explicit_limits():
    observer = Observer(max_events=1024)
    observer.open("pending")
    observer.feed("pending", "client", upgrade_request())
    observer.feed("pending", "server", upgrade_response())
    requests = b"".join(
        rpc_frame({"id": index, "method": "thread/read", "params": {"threadId": "t"}}, direction="client")
        for index in range(256 + 1)
    )
    observer.feed("pending", "client", requests)
    assert any(row.get("reason") == "pending_requests_too_many" for row in events(observer))

    observer = connected_observer()
    observer.feed("c1", "client", rpc_frame({"id": 2**63, "method": "thread/read", "params": {}}, direction="client"))
    assert any(row.get("reason") == "request_id_too_large" for row in events(observer))

    observer = connected_observer()
    cwds = [f"/repo/{index}" for index in range(65)]
    observer.feed("c1", "client", rpc_frame({"id": 1, "method": "skills/list", "params": {"cwds": cwds}}, direction="client"))
    assert any(row.get("reason") == "cwds_too_many" for row in events(observer))
