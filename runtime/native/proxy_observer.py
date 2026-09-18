"""Bounded, side-channel WebSocket/JSON-RPC observer.

The observer consumes copies of bytes supplied by a transparent relay. It never
modifies, forwards, or stores those bytes. Its output is deliberately a small
allowlist of connection, RPC, and gap records; an incomplete or unsupported
stream invalidates attachment evidence for that connection epoch.
"""

from __future__ import annotations

import base64
import hashlib
import hmac
import json
import os
from collections import deque
from dataclasses import dataclass, field
from typing import Any, Callable, Deque, Dict, Iterable, List, Optional, Tuple

_DIRECTIONS = ("client", "server")
_MAX_SAFE_ID = 256
_MAX_SAFE_STRING = 4096
_MAX_PENDING_REQUESTS = 256
_MAX_CWDS = 64
_MIN_JSONRPC_INTEGER = -(1 << 63)
_MAX_JSONRPC_INTEGER = (1 << 63) - 1
_WS_GUID = b"258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
_KNOWN_METHODS = {
    "initialize", "initialized", "thread/start", "thread/resume", "thread/fork",
    "thread/read", "thread/inject_items", "thread/subscribe", "thread/unsubscribe", "thread/started",
    "skills/list",
}
_THREAD_PARAM_METHODS = {
    "thread/start", "thread/resume", "thread/fork", "thread/read",
    "thread/inject_items", "thread/subscribe", "thread/unsubscribe",
}
_WORKSPACE_PROBE_COMMAND = ["git", "-c", "safe.bareRepository=explicit", "branch", "--show-current"]
_WORKSPACE_PROBE_FALSE_KEYS = (
    "tty",
    "streamStdin",
    "streamStdoutStderr",
    "disableOutputCap",
    "disableTimeout",
)
_WORKSPACE_PROBE_NULL_KEYS = (
    "processId",
    "size",
    "sandboxPolicy",
    "permissionProfile",
)
_THREAD_KEYS = ("id", "sessionId", "parentThreadId", "forkedFromId", "ephemeral", "cwd")


class _DuplicateKey(ValueError):
    pass


class _ParamLimit(ValueError):
    pass


def _reject_duplicate_keys(pairs: Iterable[Tuple[str, Any]]) -> Dict[str, Any]:
    result: Dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise _DuplicateKey
        result[key] = value
    return result


def _reject_nonfinite(value: str) -> None:
    raise ValueError(value)


@dataclass
class _Direction:
    buffer: bytearray = field(default_factory=bytearray)
    handshake_done: bool = False
    fragment_opcode: Optional[int] = None
    fragment_payload: bytearray = field(default_factory=bytearray)


@dataclass
class _Connection:
    epoch: int
    state: str = "UNBOUND"
    valid: bool = True
    closed: bool = False
    overflowed: bool = False
    client_key: Optional[bytes] = None
    directions: Dict[str, _Direction] = field(
        default_factory=lambda: {direction: _Direction() for direction in _DIRECTIONS}
    )
    pending: Dict[Tuple[str, Any], str] = field(default_factory=dict)


class Observer:
    """Parse relay byte copies and emit bounded whitelist records."""

    def __init__(
        self,
        *,
        max_header_bytes: int = 64 * 1024,
        max_frame_bytes: int = 1024 * 1024,
        max_message_bytes: int = 2 * 1024 * 1024,
        max_events: int = 4096,
        emit: Optional[Callable[[Dict[str, Any]], None]] = None,
        expected_workspace_cwd: Optional[str] = None,
    ) -> None:
        if min(max_header_bytes, max_frame_bytes, max_message_bytes, max_events) < 1:
            raise ValueError("observer limits must be positive")
        self.max_header_bytes = max_header_bytes
        self.max_frame_bytes = max_frame_bytes
        self.max_message_bytes = max_message_bytes
        self.max_events = max_events
        self.expected_workspace_cwd = expected_workspace_cwd
        self._emit_callback = emit
        self._connections: Dict[Any, _Connection] = {}
        self._max_epoch_seen = 0
        self._next_epoch = 1
        self._next_sequence = 1
        self._events: Deque[Dict[str, Any]] = deque()
        if max_events < 2:
            raise ValueError("max_events must reserve one slot for an overflow gap")

    def open(self, conn_id: Any, *, conn_epoch: Optional[int] = None, **_: Any) -> int:
        """Start a fresh connection epoch and return its epoch number."""
        if conn_epoch is None:
            epoch = self._next_epoch
        elif isinstance(conn_epoch, int) and not isinstance(conn_epoch, bool) and conn_epoch > 0:
            epoch = conn_epoch
        else:
            raise ValueError("conn_epoch must be a positive integer")
        if epoch <= self._max_epoch_seen:
            raise ValueError("conn_epoch has already been used or is stale")
        self._max_epoch_seen = epoch
        old = self._connections.get(conn_id)
        if old is not None and not old.closed:
            self._gap(old, "connection_reopened", None)
            old.closed = True
            self._emit("connection_close", old, None)
        self._next_epoch = max(self._next_epoch, epoch + 1)
        connection = _Connection(epoch=epoch)
        self._connections[conn_id] = connection
        self._emit("connection_open", connection, None)
        return epoch

    def set_pending(self, conn_id: Any) -> None:
        connection = self._connections.get(conn_id)
        if connection is not None and not connection.closed:
            connection.state = "PENDING"
            connection.valid = False

    def attachment_state(self, conn_id: Any) -> Optional[str]:
        connection = self._connections.get(conn_id)
        return None if connection is None else connection.state

    def feed(self, conn_id: Any, direction: str, data: bytes) -> List[Dict[str, Any]]:
        before = self._next_sequence
        connection = self._connections.get(conn_id)
        if connection is None or connection.closed:
            self._emit("gap", None, direction, reason="unknown_connection")
            return self._events_since(before)
        if direction not in _DIRECTIONS:
            self._gap(connection, "unknown_direction", direction)
            return self._events_since(before)
        if not isinstance(data, (bytes, bytearray, memoryview)):
            self._gap(connection, "non_bytes_input", direction)
            return self._events_since(before)
        if not connection.valid:
            return []
        incoming = bytes(data)
        parser = connection.directions[direction]
        if len(parser.buffer) + len(incoming) > self._buffer_limit(parser):
            self._gap(connection, "input_buffer_too_large", direction)
            return self._events_since(before)
        parser.buffer.extend(incoming)
        if not parser.handshake_done and not self._parse_upgrade(connection, direction):
            return self._events_since(before)
        if connection.valid and parser.handshake_done:
            self._parse_frames(connection, direction)
        return self._events_since(before)

    def close(self, conn_id: Any, **_: Any) -> List[Dict[str, Any]]:
        before = self._next_sequence
        connection = self._connections.get(conn_id)
        if connection is None:
            self._emit("gap", None, None, reason="unknown_connection")
            return self._events_since(before)
        if connection.closed:
            return []
        if connection.valid:
            any_upgrade = any(parser.handshake_done for parser in connection.directions.values())
            for direction, parser in connection.directions.items():
                if parser.fragment_opcode is not None:
                    self._gap(connection, "incomplete_message", direction)
                    break
                if not parser.handshake_done and (parser.buffer or any_upgrade):
                    self._gap(connection, "incomplete_upgrade", direction)
                    break
                if parser.buffer:
                    self._gap(connection, "incomplete_frame", direction)
                    break
        connection.closed = True
        connection.state = "DISCONNECTED"
        connection.valid = False
        self._emit("connection_close", connection, None)
        return self._events_since(before)

    def drain_events(self) -> List[Dict[str, Any]]:
        events = list(self._events)
        self._events.clear()
        return events

    def _events_since(self, sequence: int) -> List[Dict[str, Any]]:
        return [event for event in self._events if event["local_seq"] >= sequence]

    def _buffer_limit(self, parser: _Direction) -> int:
        # A relay may coalesce the HTTP upgrade and the first message, or many
        # complete frames, into one callback. Keep that bounded while allowing
        # the parser to consume the coalesced bytes in one call.
        return self.max_message_bytes + self.max_header_bytes + 32

    def _emit(self, event: str, connection: Optional[_Connection], direction: Optional[str], **fields: Any) -> None:
        if connection is not None and connection.overflowed:
            return
        record: Dict[str, Any] = {"event": event}
        if connection is not None:
            record["conn_epoch"] = connection.epoch
        record["local_seq"] = self._next_sequence
        self._next_sequence += 1
        if direction in _DIRECTIONS:
            record["direction"] = direction
        record.update(fields)
        if len(self._events) >= self.max_events - 1:
            if connection is not None:
                self._invalidate(connection)
                connection.overflowed = True
            overflow = {
                "event": "gap",
                **({"conn_epoch": connection.epoch} if connection is not None else {}),
                "local_seq": self._next_sequence,
                **({"direction": direction} if direction in _DIRECTIONS else {}),
                "reason": "event_queue_overflow",
            }
            self._next_sequence += 1
            self._events.append(overflow)
            if self._emit_callback is not None:
                try:
                    self._emit_callback(dict(overflow))
                except Exception:
                    pass
            return
        self._events.append(record)
        if self._emit_callback is not None:
            try:
                self._emit_callback(dict(record))
            except Exception:
                pass

    def _invalidate(self, connection: _Connection) -> None:
        connection.valid = False
        connection.state = "PENDING"
        for parser in connection.directions.values():
            parser.buffer.clear()
            parser.fragment_payload.clear()
            parser.fragment_opcode = None
        connection.pending.clear()

    def _gap(
        self,
        connection: _Connection,
        reason: str,
        direction: Optional[str],
        **fields: Any,
    ) -> None:
        if connection.valid:
            self._invalidate(connection)
            self._emit("gap", connection, direction, reason=reason, **fields)

    def _parse_upgrade(self, connection: _Connection, direction: str) -> bool:
        parser = connection.directions[direction]
        marker = parser.buffer.find(b"\r\n\r\n")
        if marker < 0:
            if len(parser.buffer) > self.max_header_bytes:
                self._gap(connection, "header_too_large", direction)
            return False
        end = marker + 4
        if end > self.max_header_bytes:
            self._gap(connection, "header_too_large", direction)
            return False
        header = bytes(parser.buffer[:end])
        del parser.buffer[:end]
        try:
            lines = header[:-4].decode("ascii").split("\r\n")
        except UnicodeDecodeError:
            self._gap(connection, "invalid_http_header", direction)
            return False
        if not lines or any("\x00" in line for line in lines):
            self._gap(connection, "invalid_http_header", direction)
            return False
        fields: Dict[str, List[str]] = {}
        for line in lines[1:]:
            if ":" not in line:
                self._gap(connection, "invalid_http_header", direction)
                return False
            key, value = line.split(":", 1)
            key = key.strip().lower()
            if not key or any(char not in "abcdefghijklmnopqrstuvwxyz0123456789-" for char in key):
                self._gap(connection, "invalid_http_header", direction)
                return False
            fields.setdefault(key, []).append(value.strip())
        if any(value.strip() for value in fields.get("sec-websocket-extensions", [])):
            self._gap(connection, "unsupported_extension", direction)
            return False
        first = lines[0]
        upgrade_tokens = self._header_tokens(fields.get("upgrade", []))
        connection_tokens = self._header_tokens(fields.get("connection", []))
        if direction == "client":
            required = ("host", "sec-websocket-key", "sec-websocket-version")
            versions = fields.get("sec-websocket-version", [])
            keys = fields.get("sec-websocket-key", [])
            request_parts = first.split(" ")
            if len(request_parts) != 3 or request_parts[0] != "GET" or request_parts[2] != "HTTP/1.1" or any(not fields.get(name) for name in required) or versions != ["13"] or len(keys) != 1 or "websocket" not in upgrade_tokens or "upgrade" not in connection_tokens:
                self._gap(connection, "invalid_upgrade_request", direction)
                return False
            try:
                key = base64.b64decode(keys[0], validate=True)
            except (ValueError, TypeError):
                self._gap(connection, "invalid_upgrade_request", direction)
                return False
            if len(key) != 16 or base64.b64encode(key).decode("ascii") != keys[0]:
                self._gap(connection, "invalid_upgrade_request", direction)
                return False
            # RFC 6455 hashes the ASCII Sec-WebSocket-Key header value plus
            # the GUID; the decoded 16-byte nonce is only a validity check.
            connection.client_key = keys[0].encode("ascii")
        else:
            accepts = fields.get("sec-websocket-accept", [])
            expected = base64.b64encode(hashlib.sha1((connection.client_key or b"") + _WS_GUID).digest()).decode("ascii")
            if not first.startswith("HTTP/1.1 101 ") or "websocket" not in upgrade_tokens or "upgrade" not in connection_tokens or len(accepts) != 1 or connection.client_key is None or not hmac.compare_digest(accepts[0], expected):
                self._gap(connection, "invalid_upgrade_response", direction)
                return False
        parser.handshake_done = True
        self._emit("websocket_upgrade", connection, direction)
        return True

    @staticmethod
    def _header_tokens(values: Iterable[str]) -> set:
        return {token.strip().lower() for value in values for token in value.split(",") if token.strip()}

    def _parse_frames(self, connection: _Connection, direction: str) -> None:
        parser = connection.directions[direction]
        while connection.valid:
            if len(parser.buffer) < 2:
                return
            first, second = parser.buffer[0], parser.buffer[1]
            fin, rsv, opcode, masked = bool(first & 0x80), first & 0x70, first & 0x0F, bool(second & 0x80)
            expected_mask = direction == "client"
            if rsv:
                self._gap(connection, "reserved_bits", direction)
                return
            if masked != expected_mask:
                self._gap(connection, "client_frame_unmasked" if direction == "client" else "server_frame_masked", direction)
                return
            length_code, offset = second & 0x7F, 2
            if length_code < 126:
                payload_length = length_code
            elif length_code == 126:
                if len(parser.buffer) < 4:
                    return
                payload_length, offset = int.from_bytes(parser.buffer[2:4], "big"), 4
            else:
                if len(parser.buffer) < 10:
                    return
                if parser.buffer[2] & 0x80:
                    self._gap(connection, "invalid_frame_length", direction)
                    return
                payload_length, offset = int.from_bytes(parser.buffer[2:10], "big"), 10
            if opcode >= 11:
                self._gap(connection, "unsupported_opcode", direction)
                return
            if opcode >= 8 and (not fin or payload_length > 125):
                self._gap(connection, "invalid_control_frame", direction)
                return
            if payload_length > self.max_frame_bytes:
                self._gap(
                    connection,
                    "frame_too_large",
                    direction,
                    payload_bytes=payload_length,
                    limit_bytes=self.max_frame_bytes,
                )
                return
            mask_size, total = (4 if masked else 0), offset + (4 if masked else 0) + payload_length
            if len(parser.buffer) < total:
                return
            mask = bytes(parser.buffer[offset:offset + mask_size]) if masked else b""
            payload = bytes(parser.buffer[offset + mask_size:total])
            del parser.buffer[:total]
            if masked:
                payload = bytes(value ^ mask[index % 4] for index, value in enumerate(payload))
            if opcode >= 8:
                if opcode == 8 and not self._valid_close_payload(payload):
                    self._gap(connection, "invalid_close_payload", direction)
                    return
                self._emit("ws_control", connection, direction, opcode={8: "close", 9: "ping", 10: "pong"}.get(opcode, "unknown"))
                continue
            if opcode == 0:
                if parser.fragment_opcode is None:
                    self._gap(connection, "unexpected_continuation", direction)
                    return
                if len(parser.fragment_payload) + len(payload) > self.max_message_bytes:
                    self._gap(
                        connection,
                        "message_too_large",
                        direction,
                        message_bytes=len(parser.fragment_payload) + len(payload),
                        limit_bytes=self.max_message_bytes,
                    )
                    return
                parser.fragment_payload.extend(payload)
                if fin:
                    complete_opcode, complete = parser.fragment_opcode, bytes(parser.fragment_payload)
                    parser.fragment_opcode, parser.fragment_payload = None, bytearray()
                    if complete_opcode == 1:
                        self._process_text(connection, direction, complete)
                    else:
                        self._gap(connection, "binary_message", direction)
                        return
            elif opcode in (1, 2):
                if parser.fragment_opcode is not None:
                    self._gap(connection, "new_data_during_fragment", direction)
                    return
                if len(payload) > self.max_message_bytes:
                    self._gap(
                        connection,
                        "message_too_large",
                        direction,
                        message_bytes=len(payload),
                        limit_bytes=self.max_message_bytes,
                    )
                    return
                if fin:
                    if opcode == 1:
                        self._process_text(connection, direction, payload)
                    else:
                        self._gap(connection, "binary_message", direction)
                        return
                else:
                    parser.fragment_opcode, parser.fragment_payload = opcode, bytearray(payload)
            else:
                self._gap(connection, "unsupported_opcode", direction)
                return

    def _process_text(self, connection: _Connection, direction: str, payload: bytes) -> None:
        try:
            text = payload.decode("utf-8")
        except UnicodeDecodeError:
            self._gap(connection, "invalid_utf8", direction)
            return
        try:
            value = json.loads(text, object_pairs_hook=_reject_duplicate_keys, parse_constant=_reject_nonfinite)
        except _DuplicateKey:
            self._gap(connection, "duplicate_json_key", direction)
            return
        except (ValueError, RecursionError):
            self._gap(connection, "invalid_json", direction)
            return
        self._process_rpc(connection, direction, value)

    @staticmethod
    def _valid_close_payload(payload: bytes) -> bool:
        if len(payload) == 0:
            return True
        if len(payload) == 1:
            return False
        code = int.from_bytes(payload[:2], "big")
        valid_codes = {1000, 1001, 1002, 1003, 1007, 1008, 1009, 1010, 1011, 1012, 1013, 1014}
        if code not in valid_codes and not 3000 <= code <= 4999:
            return False
        try:
            payload[2:].decode("utf-8")
        except UnicodeDecodeError:
            return False
        return True

    @staticmethod
    def _valid_id(value: Any) -> bool:
        return (isinstance(value, str) and 0 < len(value) <= _MAX_SAFE_ID) or (
            isinstance(value, int)
            and not isinstance(value, bool)
            and _MIN_JSONRPC_INTEGER <= value <= _MAX_JSONRPC_INTEGER
        )

    @staticmethod
    def _safe_string(value: Any) -> Optional[str]:
        return value if isinstance(value, str) and len(value) <= _MAX_SAFE_STRING else None

    def _process_rpc(self, connection: _Connection, direction: str, value: Any) -> None:
        if not isinstance(value, dict):
            self._gap(connection, "unsupported_json_rpc", direction)
            return
        if "jsonrpc" in value and value["jsonrpc"] != "2.0":
            self._gap(connection, "invalid_json_rpc", direction)
            return
        has_method, has_result, has_error = "method" in value, "result" in value, "error" in value
        if (has_method and (has_result or has_error)) or (has_result and has_error):
            self._gap(connection, "invalid_json_rpc", direction)
        elif has_method:
            self._process_request(connection, direction, value)
        elif has_result or has_error:
            self._process_response(connection, direction, value)
        else:
            self._gap(connection, "unsupported_json_rpc", direction)

    def _process_request(self, connection: _Connection, direction: str, value: Dict[str, Any]) -> None:
        method = value.get("method")
        if not isinstance(method, str) or not method or len(method) > _MAX_SAFE_STRING:
            self._gap(connection, "invalid_method", direction)
            return
        request_id = value.get("id")
        if "id" in value and not self._valid_id(request_id):
            reason = "request_id_too_large" if isinstance(request_id, int) and not isinstance(request_id, bool) else "invalid_request_id"
            self._gap(connection, reason, direction)
            return
        safe_params: Optional[Dict[str, Any]] = None
        if method in _KNOWN_METHODS:
            try:
                safe_params = self._safe_request_params(method, value.get("params"))
            except _ParamLimit:
                self._gap(connection, "cwds_too_many", direction)
                return
        if "id" in value:
            if len(connection.pending) >= _MAX_PENDING_REQUESTS:
                self._gap(connection, "pending_requests_too_many", direction)
                return
            if (direction, request_id) in connection.pending:
                self._gap(connection, "duplicate_request_id", direction)
                return
            connection.pending[(direction, request_id)] = method
        if method not in _KNOWN_METHODS:
            fields: Dict[str, Any] = {
                "request_id": request_id if "id" in value else None,
                "method": method,
                "opaque": True,
            }
            if method == "command/exec":
                fields["workspace_probe_qualified"] = self._workspace_probe_qualified(
                    direction,
                    value.get("id") if "id" in value else None,
                    value.get("params"),
                    has_id="id" in value,
                )
            self._emit("rpc_unknown", connection, direction, **fields)
        else:
            fields: Dict[str, Any] = {
                "request_id": request_id if "id" in value else None,
                "method": method,
                "params": safe_params if safe_params is not None else {},
            }
            if method == "thread/started":
                thread = self._thread_metadata(value.get("params"))
                if thread is not None:
                    fields["thread"] = thread
            self._emit("rpc", connection, direction, **fields)

    def _safe_request_params(self, method: str, params: Any) -> Dict[str, Any]:
        if not isinstance(params, dict):
            return {}
        safe: Dict[str, Any] = {}
        if method in _THREAD_PARAM_METHODS:
            for key in ("threadId", "parentThreadId", "forkedFromId", "cwd", "ephemeral", "includeTurns"):
                value = params.get(key)
                if key in ("ephemeral", "includeTurns"):
                    if isinstance(value, bool):
                        safe[key] = value
                else:
                    string = self._safe_string(value)
                    if string is not None:
                        safe[key] = string
        elif method == "skills/list":
            cwds = params.get("cwds")
            if isinstance(cwds, list) and len(cwds) > _MAX_CWDS:
                raise _ParamLimit
            if isinstance(cwds, list) and all(self._safe_string(value) is not None for value in cwds):
                safe["cwds"] = list(cwds)
            if isinstance(params.get("forceReload"), bool):
                safe["forceReload"] = params["forceReload"]
        return safe

    def _workspace_probe_qualified(
        self,
        direction: str,
        request_id: Any,
        params: Any,
        *,
        has_id: bool,
    ) -> bool:
        expected_cwd = self.expected_workspace_cwd
        if (
            direction != "client"
            or not has_id
            or not self._valid_id(request_id)
            or not isinstance(expected_cwd, str)
            or not os.path.isabs(expected_cwd)
            or not isinstance(params, dict)
        ):
            return False
        allowed = {
            "command",
            "cwd",
            "env",
            "timeoutMs",
            "outputBytesCap",
            *_WORKSPACE_PROBE_FALSE_KEYS,
            *_WORKSPACE_PROBE_NULL_KEYS,
        }
        if set(params) - allowed:
            return False
        command = params.get("command")
        env = params.get("env")
        if not isinstance(command, list) or any(type(item) is not str for item in command):
            return False
        if not isinstance(env, dict) or any(
            type(key) is not str or type(value) is not str for key, value in env.items()
        ):
            return False
        if (
            command != _WORKSPACE_PROBE_COMMAND
            or not isinstance(params.get("cwd"), str)
            or not os.path.isabs(params["cwd"])
            or params["cwd"] != expected_cwd
            or env != {"GIT_OPTIONAL_LOCKS": "0"}
            or type(params.get("timeoutMs")) is not int
            or params["timeoutMs"] != 5000
            or type(params.get("outputBytesCap")) is not int
            or params["outputBytesCap"] != 65536
        ):
            return False
        for key in _WORKSPACE_PROBE_FALSE_KEYS:
            if key in params and params[key] is not False:
                return False
        for key in _WORKSPACE_PROBE_NULL_KEYS:
            if key in params and params[key] is not None:
                return False
        return True

    def _process_response(self, connection: _Connection, direction: str, value: Dict[str, Any]) -> None:
        if "error" in value and not self._valid_error(value["error"]):
            self._gap(connection, "invalid_json_rpc", direction)
            return
        request_id = value.get("id")
        if not self._valid_id(request_id):
            reason = "response_id_too_large" if isinstance(request_id, int) and not isinstance(request_id, bool) else "invalid_response_id"
            self._gap(connection, reason, direction)
            return
        expected_request_direction = "client" if direction == "server" else "server"
        method = connection.pending.pop((expected_request_direction, request_id), None)
        if method is None:
            self._emit("rpc_unknown", connection, direction, request_id=request_id, method=None, opaque=True, response=True)
            return
        if method not in _KNOWN_METHODS:
            self._emit("rpc_unknown", connection, direction, request_id=request_id, method=method, opaque=True, response=True)
            return
        if "error" in value:
            self._emit("rpc_response", connection, direction, request_id=request_id, request_direction=expected_request_direction, method=method, ok=False)
            return
        fields: Dict[str, Any] = {"request_id": request_id, "request_direction": expected_request_direction, "method": method, "ok": True}
        thread = self._thread_metadata(value.get("result"))
        if thread is not None:
            fields["thread"] = thread
        self._emit("rpc_response", connection, direction, **fields)

    @staticmethod
    def _valid_error(value: Any) -> bool:
        if not isinstance(value, dict):
            return False
        code = value.get("code")
        return (
            isinstance(code, int)
            and not isinstance(code, bool)
            and _MIN_JSONRPC_INTEGER <= code <= _MAX_JSONRPC_INTEGER
            and isinstance(value.get("message"), str)
        )

    def _thread_metadata(self, result: Any) -> Optional[Dict[str, Any]]:
        candidate = result.get("thread") if isinstance(result, dict) and isinstance(result.get("thread"), dict) else result if isinstance(result, dict) and any(key in result for key in _THREAD_KEYS) else None
        if not isinstance(candidate, dict):
            return None
        metadata: Dict[str, Any] = {}
        for key in _THREAD_KEYS:
            value = candidate.get(key)
            if key == "ephemeral":
                if isinstance(value, bool):
                    metadata[key] = value
            else:
                string = self._safe_string(value)
                if string is not None:
                    metadata[key] = string
        return metadata or None


__all__ = ["Observer"]
