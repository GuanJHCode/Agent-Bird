import json
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
    message = {"jsonrpc": "2.0", "id": "r1", "method": "skills/list", "params": {"cwds": ["/repo"], "forceReload": True, "secret": "CANARY"}}
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


def test_coalesced_server_response_correlates_only_client_request():
    observer = connected_observer()
    request = {"jsonrpc": "2.0", "id": 7, "method": "thread/read", "params": {"threadId": "t1", "includeTurns": False}}
    response = {"jsonrpc": "2.0", "id": 7, "result": {"thread": {"id": "t1", "sessionId": "s1", "parentThreadId": "p1", "forkedFromId": "p1", "ephemeral": True, "cwd": "/repo", "secret": "CANARY"}}}
    observer.feed("c1", "client", rpc_frame(request, direction="client"))
    observer.feed("c1", "server", rpc_frame({"jsonrpc": "2.0", "id": "same", "method": "item/requestUserInput", "params": {"question": "CANARY"}}, direction="server") + rpc_frame(response, direction="server"))
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
    payload = b'{"jsonrpc":"2.0","id":1,"method":"thread/read","method":"skills/list"}'
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
    assert any(row.get("reason") == "frame_too_large" for row in events(observer))
    assert "x" * 33 not in json.dumps(observer.drain_events())

    observer = connected_observer()
    observer.feed("c1", "client", frame(b'{"jsonrpc":"2.0"', fin=False, masked=True))
    observer.close("c1")
    rows = events(observer)
    assert any(row.get("event") == "gap" and row.get("reason") == "incomplete_message" for row in rows)
    assert rows[-1]["event"] == "connection_close"
    assert observer.attachment_state("c1") == "DISCONNECTED"


def test_unknown_method_and_unknown_notification_are_preserved_as_opaque():
    observer = connected_observer()
    observer.feed("c1", "client", rpc_frame({"jsonrpc": "2.0", "id": "u", "method": "vendor/secret", "params": {"text": "CANARY"}}, direction="client"))
    observer.feed("c1", "server", rpc_frame({"jsonrpc": "2.0", "method": "VendorEvent", "params": {"text": "CANARY"}}, direction="server"))
    rows = events(observer)
    unknown = [row for row in rows if row.get("event") == "rpc_unknown"]
    assert [(row["direction"], row.get("request_id"), row["method"]) for row in unknown] == [("client", "u", "vendor/secret"), ("server", None, "VendorEvent")]
    assert all(row["opaque"] for row in unknown)
    assert "CANARY" not in json.dumps(rows)


def test_close_unknown_connection_is_safe_and_new_epoch_does_not_reuse_pending_ids():
    observer = Observer()
    observer.close("missing")
    assert any(row.get("event") == "gap" and row.get("reason") == "unknown_connection" for row in events(observer))
    observer.open("c1")
    observer.feed("c1", "client", upgrade_request())
    observer.feed("c1", "server", upgrade_response())
    observer.feed("c1", "client", rpc_frame({"jsonrpc": "2.0", "id": 1, "method": "thread/read", "params": {"threadId": "old"}}, direction="client"))
    observer.close("c1")
    observer.open("c1")
    observer.feed("c1", "client", upgrade_request())
    observer.feed("c1", "server", upgrade_response())
    observer.feed("c1", "server", rpc_frame({"jsonrpc": "2.0", "id": 1, "result": {}}, direction="server"))
    rows = events(observer)
    assert any(row.get("event") == "rpc_unknown" and row.get("request_id") == 1 for row in rows)
    assert len({row["conn_epoch"] for row in rows if "conn_epoch" in row}) >= 2


def test_upgrade_and_first_frame_can_share_one_chunk_and_control_frame_can_interleave():
    observer = Observer()
    observer.open("c1")
    payload = {"jsonrpc": "2.0", "id": "r1", "method": "thread/start", "params": {"cwd": "/repo"}}
    observer.feed("c1", "client", upgrade_request() + frame(json.dumps(payload).encode(), masked=True))
    observer.feed("c1", "server", upgrade_response())
    observer.feed("c1", "client", frame(b"x", opcode=9, masked=True) + frame(b"", opcode=10, masked=True))
    rows = events(observer)
    assert any(row.get("event") == "rpc" and row.get("method") == "thread/start" for row in rows)
    assert [row["opcode"] for row in rows if row.get("event") == "ws_control"] == ["ping", "pong"]
    assert observer.attachment_state("c1") == "UNBOUND"


def test_unknown_thread_list_response_is_opaque_and_does_not_retain_history():
    observer = connected_observer()
    observer.feed("c1", "client", rpc_frame({"jsonrpc": "2.0", "id": "list", "method": "thread/list", "params": {}}, direction="client"))
    observer.feed("c1", "server", rpc_frame({"jsonrpc": "2.0", "id": "list", "result": {"data": [{"text": "CANARY"}]}}, direction="server"))
    rows = events(observer)
    assert any(row.get("event") == "rpc_unknown" and row.get("method") == "thread/list" and row.get("response") for row in rows)
    assert "CANARY" not in json.dumps(rows)
