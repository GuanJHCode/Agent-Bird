"""Controlled second frontend for an already leased backend.

The helper opens a second WebSocket to an existing private listener.  It owns
no backend process and implements the narrow native wire contract needed for a
bound owner thread, including the one fixed ``toolOutput`` turn envelope.
"""

from __future__ import annotations

import asyncio
import base64
from dataclasses import dataclass, field
import hashlib
import json
import os
import re
import secrets
import stat


class OwnerHelperAdmissionError(ValueError):
    """The helper grant, identity, lease, frame, or RPC envelope is invalid."""


@dataclass(frozen=True)
class HelperIdentity:
    pid: int
    uid: int
    birth: str
    executable: str
    executable_sha256: str
    source_sha256: str


@dataclass(frozen=True)
class OwnerHelperGrant:
    role: str
    profile_id: str
    owner_context_sha256: str
    lease_id: str
    owner_connection_id: str
    owner_epoch: int
    owner_thread_id: str
    private_socket: str
    helper_pid: int
    helper_uid: int
    helper_birth: str
    helper_executable: str
    helper_executable_sha256: str
    helper_source_sha256: str
    version: int = 1


_ID = re.compile(r"[A-Za-z0-9_-]{1,64}\Z")
_HASH = re.compile(r"[0-9a-f]{64}\Z")
_MAX_OUTPUT_BYTES = 65536
_GLOBAL_READ_PARAMS = {
    "server/diagnostics": {},
    "remoteControl/status/read": {},
}
_SERVER_NOTIFICATIONS = frozenset(
    {
        "thread/status/changed",
        "thread/updated",
        "thread/tokenUsage/updated",
        "thread/goal/cleared",
        "remoteControl/status/changed",
        "mcpServer/startupStatus/updated",
    }
)


def _reject(reason: str) -> None:
    raise OwnerHelperAdmissionError(reason)


def _canonical(value: object) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False)


def _safe_id(value: object) -> bool:
    return type(value) is str and _ID.fullmatch(value) is not None


def _positive(value: object) -> bool:
    return type(value) is int and 1 <= value <= 1_000_000


def _exact_keys(value: object, required: set[str]) -> None:
    if not isinstance(value, dict) or set(value) != required:
        _reject("rpc_envelope")


def _json_unique(payload: bytes):
    def duplicate(key_pairs):
        value = {}
        for key, item in key_pairs:
            if key in value:
                _reject("rpc_duplicate_key")
            value[key] = item
        return value
    try:
        return json.loads(payload, object_pairs_hook=duplicate, parse_constant=lambda _: _reject("rpc_constant"))
    except (UnicodeDecodeError, json.JSONDecodeError, TypeError):
        _reject("rpc_json")


def _parse_delivery_output(output: object, owner_thread_id: str) -> None:
    if type(output) is not str or not output or len(output.encode("utf-8")) > _MAX_OUTPUT_BYTES:
        _reject("tool_output_size")

    def duplicate(key_pairs):
        value = {}
        for key, item in key_pairs:
            if key in value:
                _reject("tool_output_duplicate_key")
            value[key] = item
        return value

    try:
        intent = json.loads(output, object_pairs_hook=duplicate, parse_constant=lambda _: _reject("tool_output_constant"))
    except (UnicodeDecodeError, json.JSONDecodeError, TypeError):
        _reject("tool_output_json")
    if not isinstance(intent, dict) or set(intent) != {
        "version", "delivery_id", "controller_thread_id", "controller_epoch", "events", "payload_hash"
    }:
        _reject("tool_output_shape")
    if type(intent["version"]) is not int or intent["version"] != 1 or not _safe_id(intent["delivery_id"]):
        _reject("tool_output_identity")
    if intent["controller_thread_id"] != owner_thread_id or not _safe_id(intent["controller_thread_id"]):
        _reject("tool_output_thread")
    if not _positive(intent["controller_epoch"]):
        _reject("tool_output_epoch")
    events = intent["events"]
    if not isinstance(events, list) or not 1 <= len(events) <= 8:
        _reject("tool_output_events")
    event_ids, action_slots = set(), set()
    for event in events:
        if not isinstance(event, dict) or set(event) != {
            "event_id", "event_revision", "kind", "payload_hash", "action_slot"
        }:
            _reject("tool_output_event_shape")
        if not _safe_id(event["event_id"]) or not _positive(event["event_revision"]):
            _reject("tool_output_event_identity")
        if event["kind"] not in ("progress", "question", "result"):
            _reject("tool_output_event_kind")
        if type(event["payload_hash"]) is not str or not _HASH.fullmatch(event["payload_hash"]):
            _reject("tool_output_event_hash")
        if not _safe_id(event["action_slot"]) or event["event_id"] in event_ids or event["action_slot"] in action_slots:
            _reject("tool_output_event_duplicate")
        event_ids.add(event["event_id"])
        action_slots.add(event["action_slot"])
    body = {key: value for key, value in intent.items() if key != "payload_hash"}
    if hashlib.sha256(_canonical(body).encode()).hexdigest() != intent["payload_hash"]:
        _reject("tool_output_hash")


def _validate_turn_start(params: object, owner_thread_id: str) -> None:
    if not isinstance(params, dict) or set(params) != {"threadId", "input", "toolOutput"}:
        _reject("turn_start_envelope")
    if params["threadId"] != owner_thread_id or params["input"] != []:
        _reject("turn_start_target")
    tool_output = params["toolOutput"]
    if not isinstance(tool_output, dict) or set(tool_output) != {"name", "namespace", "output"}:
        _reject("tool_output_envelope")
    if tool_output["name"] != "g0_delivery" or tool_output["namespace"] != "orchestration":
        _reject("tool_output_identity")
    _parse_delivery_output(tool_output["output"], owner_thread_id)


def validate_helper_rpc(message: object, owner_thread_id: str) -> None:
    """Validate a native-wire helper request before writing any frame."""

    if not isinstance(message, dict) or "jsonrpc" in message:
        _reject("native_wire_envelope")
    method = message.get("method")
    if not isinstance(method, str):
        _reject("rpc_method")
    if method == "initialized":
        if set(message) != {"method"}:
            _reject("initialized_envelope")
        return
    if type(message.get("id")) is not int or not _positive(message["id"]):
        _reject("rpc_id")
    if "params" not in message or not isinstance(message["params"], dict):
        _reject("rpc_params")
    params = message["params"]
    if method == "initialize":
        expected = {
            "clientInfo": {"name": "g0-native-standalone", "version": "0.1.0"},
            "capabilities": {"experimentalApi": True},
        }
        if params != expected:
            _reject("initialize_envelope")
        return
    if method in _GLOBAL_READ_PARAMS:
        if params != _GLOBAL_READ_PARAMS[method]:
            _reject("rpc_global_envelope")
        return
    if method == "thread/read":
        if params != {"threadId": owner_thread_id, "includeTurns": True}:
            _reject("rpc_thread_envelope")
        return
    if method == "turn/start":
        _validate_turn_start(params, owner_thread_id)
        return
    _reject("rpc_method_forbidden")


def validate_helper_response(message: object, owner_thread_id: str) -> None:
    """Validate one native response; request-id association is checked by the channel."""

    if not isinstance(message, dict) or "jsonrpc" in message or set(message) not in ({"id", "result"}, {"id", "error"}):
        _reject("response_envelope")
    if type(message["id"]) is not int or not _positive(message["id"]):
        _reject("response_id")
    if "error" in message:
        error = message["error"]
        if not isinstance(error, dict) or set(error) not in ({"code", "message"}, {"code", "message", "data"}):
            _reject("response_error")
        if type(error["code"]) is not int or type(error["message"]) is not str:
            _reject("response_error")
        return
    result = message["result"]
    if not isinstance(result, dict):
        _reject("response_result")
    thread = result.get("thread")
    if isinstance(thread, dict) and "id" in thread and thread["id"] != owner_thread_id:
        _reject("response_thread_target")
    if "threadId" in result and result["threadId"] != owner_thread_id:
        _reject("response_thread_target")


def validate_helper_server_message(message: object, owner_thread_id: str) -> None:
    """Validate a response or fixed server notification; never accept a server request."""

    if not isinstance(message, dict) or "jsonrpc" in message:
        _reject("response_envelope")
    if "id" in message and "method" in message:
        _reject("server_request_forbidden")
    if "id" in message:
        validate_helper_response(message, owner_thread_id)
        return
    method = message.get("method")
    if method not in _SERVER_NOTIFICATIONS or set(message) not in ({"method"}, {"method", "params"}):
        _reject("server_notification_forbidden")
    params = message.get("params", {})
    if not isinstance(params, dict):
        _reject("server_notification_params")
    if "threadId" in params and params["threadId"] != owner_thread_id:
        _reject("response_thread_target")


def _websocket_frame(payload: bytes) -> bytes:
    """Encode a masked native client text frame, bounded to one frame."""

    if len(payload) > 65535:
        _reject("rpc_frame_too_large")
    mask = secrets.token_bytes(4)
    masked = bytes(byte ^ mask[index % 4] for index, byte in enumerate(payload))
    if len(masked) < 126:
        header = bytes((0x81, 0x80 | len(masked)))
    else:
        header = bytes((0x81, 0x80 | 126)) + len(masked).to_bytes(2, "big")
    return header + mask + masked


async def _read_server_frame(reader: asyncio.StreamReader) -> tuple[bool, int, bytes]:
    try:
        first, second = await reader.readexactly(2)
    except asyncio.IncompleteReadError:
        _reject("websocket_eof")
    if first & 0x70:
        _reject("websocket_compression_or_reserved")
    fin, opcode = bool(first & 0x80), first & 0x0F
    if second & 0x80:
        _reject("server_frame_masked")
    length = second & 0x7F
    if length == 126:
        try:
            length = int.from_bytes(await reader.readexactly(2), "big")
        except asyncio.IncompleteReadError:
            _reject("websocket_length")
    elif length == 127:
        try:
            extended = await reader.readexactly(8)
        except asyncio.IncompleteReadError:
            _reject("websocket_length")
        if extended[0] & 0x80:
            _reject("websocket_length")
        length = int.from_bytes(extended, "big")
    if length > 65535:
        _reject("websocket_frame_too_large")
    try:
        payload = await reader.readexactly(length)
    except asyncio.IncompleteReadError:
        _reject("websocket_payload")
    return fin, opcode, payload


async def _read_server_text(reader: asyncio.StreamReader) -> bytes:
    fin, opcode, payload = await _read_server_frame(reader)
    if opcode == 8:
        _reject("server_close")
    if opcode != 1:
        _reject("server_frame_opcode")
    chunks = [payload]
    while not fin:
        fin, opcode, payload = await _read_server_frame(reader)
        if opcode != 0:
            _reject("websocket_fragment_opcode")
        chunks.append(payload)
        if sum(map(len, chunks)) > 65535:
            _reject("websocket_message_too_large")
    return b"".join(chunks)


async def _open_websocket(path: str) -> tuple[asyncio.StreamReader, asyncio.StreamWriter]:
    reader, writer = await asyncio.open_unix_connection(path)
    key = base64.b64encode(secrets.token_bytes(16))
    writer.write(
        b"GET / HTTP/1.1\r\nHost: owner-helper\r\nUpgrade: websocket\r\n"
        b"Connection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: " + key + b"\r\n\r\n"
    )
    await writer.drain()
    try:
        response = await asyncio.wait_for(reader.readuntil(b"\r\n\r\n"), timeout=10)
    except BaseException:
        writer.close()
        await writer.wait_closed()
        raise
    lines = response.split(b"\r\n")
    if not lines or lines[0] != b"HTTP/1.1 101 Switching Protocols":
        writer.close()
        await writer.wait_closed()
        _reject("websocket_upgrade")
    headers = {
        line.split(b":", 1)[0].lower(): line.split(b":", 1)[1].strip()
        for line in lines[1:] if b":" in line
    }
    if headers.get(b"upgrade", b"").lower() != b"websocket" or headers.get(b"connection", b"").lower() != b"upgrade":
        writer.close()
        await writer.wait_closed()
        _reject("websocket_headers")
    accept = headers.get(b"sec-websocket-accept")
    expected = base64.b64encode(hashlib.sha1(key + b"258EAFA5-E914-47DA-95CA-C5AB0DC85B11").digest())
    if accept != expected:
        writer.close()
        await writer.wait_closed()
        _reject("websocket_accept")
    return reader, writer


class NativeWebSocketGate:
    """Synchronous byte gate used by the service before relay forwarding.

    It buffers handshake/frame fragments and releases a complete frame only
    after the complete native message has passed the owner-thread contract.
    """

    def __init__(self, owner_thread_id: str, *, server: bool, pending_ids=None):
        self.owner_thread_id=owner_thread_id
        self.server=server
        self.pending_ids=pending_ids if pending_ids is not None else set()
        self._buffer=bytearray()
        self._handshake_done=False
        self._fragment_payload=bytearray()
        self._fragment_frames=[]

    def _frame(self):
        if len(self._buffer)<2: return None
        first,second=self._buffer[0],self._buffer[1]
        if first&0x70: _reject('websocket_compression_or_reserved')
        fin=bool(first&0x80); opcode=first&0x0f; masked=bool(second&0x80)
        if masked==self.server: _reject('websocket_mask_direction')
        length=second&0x7f; header_len=2
        if length==126:
            if len(self._buffer)<4: return None
            length=int.from_bytes(self._buffer[2:4],'big'); header_len=4
        elif length==127:
            if len(self._buffer)<10: return None
            if self._buffer[2]&0x80: _reject('websocket_length')
            length=int.from_bytes(self._buffer[2:10],'big'); header_len=10
        if length>65535: _reject('websocket_frame_too_large')
        total=header_len+(4 if masked else 0)+length
        if len(self._buffer)<total: return None
        raw=bytes(self._buffer[:total]); del self._buffer[:total]
        offset=header_len
        mask=raw[offset:offset+4] if masked else b''; offset+=4 if masked else 0
        payload=raw[offset:]
        if masked: payload=bytes(byte^mask[index%4] for index,byte in enumerate(payload))
        return fin,opcode,payload,raw

    def _complete_message(self, payload):
        message=_json_unique(payload)
        if self.server:
            validate_helper_server_message(message,self.owner_thread_id)
            if 'id' in message:
                if message['id'] not in self.pending_ids: _reject('response_id_mismatch')
                self.pending_ids.remove(message['id'])
        else:
            validate_helper_rpc(message,self.owner_thread_id)
            if 'id' in message:
                if message['id'] in self.pending_ids: _reject('rpc_id_reused')
                self.pending_ids.add(message['id'])
        frames=self._fragment_frames; self._fragment_frames=[]; self._fragment_payload.clear()
        return b''.join(frames)

    def __call__(self, data: bytes) -> bytes:
        return self.feed(data)

    def feed(self, data: bytes) -> bytes:
        if not isinstance(data,(bytes,bytearray)) or len(data)>4*1024*1024: _reject('wire_chunk')
        self._buffer.extend(data); output=[]
        if not self._handshake_done:
            marker=self._buffer.find(b'\r\n\r\n')
            if marker<0:
                if len(self._buffer)>65536: _reject('websocket_handshake_size')
                return b''
            end=marker+4; output.append(bytes(self._buffer[:end])); del self._buffer[:end]; self._handshake_done=True
        while True:
            parsed=self._frame()
            if parsed is None: break
            fin,opcode,payload,raw=parsed
            if opcode==1:
                if self._fragment_frames: _reject('websocket_fragment_start')
                self._fragment_payload.extend(payload); self._fragment_frames=[raw]
                if fin: output.append(self._complete_message(bytes(self._fragment_payload)))
            elif opcode==0:
                if not self._fragment_frames: _reject('websocket_fragment_continuation')
                self._fragment_payload.extend(payload); self._fragment_frames.append(raw)
                if len(self._fragment_payload)>65535: _reject('websocket_message_too_large')
                if fin: output.append(self._complete_message(bytes(self._fragment_payload)))
            else:
                _reject('websocket_frame_opcode')
        return b''.join(output)


@dataclass
class OwnerHelperConnection:
    reader: asyncio.StreamReader
    writer: asyncio.StreamWriter
    lease_id: str
    owner_thread_id: str
    role: str = "owner-helper"
    _closed: bool = False
    _pending_ids: set[int] = field(default_factory=set, init=False, repr=False)

    async def send_rpc(self, message: object) -> None:
        if self._closed:
            _reject("helper_closed")
        validate_helper_rpc(message, self.owner_thread_id)
        request_id = message.get("id") if isinstance(message, dict) else None
        if request_id is not None and request_id in self._pending_ids:
            _reject("rpc_id_reused")
        payload = json.dumps(message, separators=(",", ":"), sort_keys=True).encode()
        frame = _websocket_frame(payload)
        if request_id is not None:
            self._pending_ids.add(request_id)
        try:
            self.writer.write(frame)
            await self.writer.drain()
        except BaseException:
            if request_id is not None:
                self._pending_ids.discard(request_id)
            raise

    async def read_rpc(self) -> dict:
        if self._closed:
            _reject("helper_closed")
        payload = await _read_server_text(self.reader)
        message = _json_unique(payload)
        validate_helper_server_message(message, self.owner_thread_id)
        if "id" in message:
            if message["id"] not in self._pending_ids:
                _reject("response_id_mismatch")
            self._pending_ids.remove(message["id"])
        return message

    async def close(self) -> None:
        if self._closed:
            return
        self._closed = True
        self.writer.close()
        await self.writer.wait_closed()


@dataclass
class OwnerLease:
    profile_id: str
    owner_context_sha256: str
    lease_id: str
    owner_connection_id: str
    owner_epoch: int
    owner_thread_id: str
    private_socket: str
    backend_pid: int
    backend_birth: str
    helper_identity: HelperIdentity | None
    closed: bool = False
    _owner_writer: asyncio.StreamWriter | None = field(default=None, init=False, repr=False)
    _helpers: list[OwnerHelperConnection] = field(default_factory=list, init=False, repr=False)

    def __post_init__(self) -> None:
        if not os.path.isabs(self.private_socket):
            _reject("private_socket_not_absolute")
        if len(self.owner_context_sha256) != 64:
            _reject("owner_context_sha256")

    def register_owner_connection(self, writer: asyncio.StreamWriter) -> None:
        if self.closed:
            _reject("lease_closed")
        self._owner_writer = writer

    def validate_grant(self, grant: OwnerHelperGrant, identity: HelperIdentity) -> None:
        expected = {
            "role": "owner-helper", "profile_id": self.profile_id,
            "owner_context_sha256": self.owner_context_sha256, "lease_id": self.lease_id,
            "owner_connection_id": self.owner_connection_id, "owner_epoch": self.owner_epoch,
            "owner_thread_id": self.owner_thread_id, "private_socket": self.private_socket,
            "helper_pid": identity.pid, "helper_uid": identity.uid, "helper_birth": identity.birth,
            "helper_executable": identity.executable,
            "helper_executable_sha256": identity.executable_sha256,
            "helper_source_sha256": identity.source_sha256,
        }
        if self.closed:
            _reject("lease_closed")
        if type(grant.version) is not int or grant.version != 1:
            _reject("grant_version")
        for key, value in expected.items():
            if getattr(grant, key) != value:
                _reject(f"grant_{key}")
        if self.helper_identity is not None and identity != self.helper_identity:
            _reject("helper_identity")
        try:
            mode = os.lstat(self.private_socket)
        except FileNotFoundError:
            _reject("private_socket_missing")
        if stat.S_ISLNK(mode.st_mode):
            _reject("private_socket_symlink")
        if not stat.S_ISSOCK(mode.st_mode):
            _reject("private_socket_not_socket")

    async def open_helper(self, grant: OwnerHelperGrant, identity: HelperIdentity) -> OwnerHelperConnection:
        self.validate_grant(grant, identity)
        reader, writer = await _open_websocket(self.private_socket)
        connection = OwnerHelperConnection(reader=reader, writer=writer, lease_id=self.lease_id, owner_thread_id=self.owner_thread_id)
        self._helpers.append(connection)
        return connection

    def owner_closed(self) -> None:
        self.closed = True
        for helper in tuple(self._helpers):
            helper.writer.close()

    async def close(self) -> None:
        self.closed = True
        helpers = tuple(self._helpers)
        self._helpers.clear()
        for helper in helpers:
            await helper.close()
