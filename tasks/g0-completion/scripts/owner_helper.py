"""Controlled second frontend for an already leased backend.

This module deliberately owns no backend process.  The service supplies a frozen
lease and a separately inspected helper identity; this module only admits a
second WebSocket to that lease's existing private listener.
"""

from __future__ import annotations

import asyncio
import base64
from dataclasses import dataclass, field
import hashlib
import json
import os
import secrets
import stat


class OwnerHelperAdmissionError(ValueError):
    """The helper grant, identity, lease, or RPC envelope is not admissible."""


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


_READ_METHODS = frozenset(
    {
        "thread/read",
        "thread/turns/list",
        "thread/subscribe",
        "thread/unsubscribe",
    }
)
_GLOBAL_READ_METHODS = frozenset({"initialize", "initialized", "skills/list"})
_FORBIDDEN_METHODS = frozenset(
    {
        "turn/start",
        "turn/steer",
        "thread/start",
        "thread/resume",
        "thread/fork",
        "thread/inject_items",
        "command/exec",
        "prompt",
    }
)
_THREAD_PARAM_KEYS = {
    "thread/read": frozenset({"threadId"}),
    "thread/turns/list": frozenset({"threadId"}),
    "thread/subscribe": frozenset({"threadId"}),
    "thread/unsubscribe": frozenset({"threadId"}),
}


def _reject(reason: str) -> None:
    raise OwnerHelperAdmissionError(reason)


def validate_helper_rpc(message: object, owner_thread_id: str) -> None:
    """Validate one helper-originated JSON-RPC request.

    The allowlist is intentionally small.  A helper can inspect the attached
    thread and bootstrap/read skills, but cannot choose another thread or send
    a turn, prompt, command, fork, or injection.
    """

    if not isinstance(message, dict):
        _reject("rpc_not_object")
    if message.get("jsonrpc") != "2.0":
        _reject("rpc_version")
    if "id" not in message or isinstance(message["id"], (dict, list)):
        _reject("rpc_id")
    method = message.get("method")
    if not isinstance(method, str):
        _reject("rpc_method")
    params = message.get("params", {})
    if not isinstance(params, dict):
        _reject("rpc_params")
    if method in _FORBIDDEN_METHODS or method not in _READ_METHODS | _GLOBAL_READ_METHODS:
        _reject("rpc_method_forbidden")
    if method in _READ_METHODS:
        if set(params) != set(_THREAD_PARAM_KEYS[method]):
            _reject("rpc_thread_envelope")
        if params.get("threadId") != owner_thread_id:
            _reject("rpc_thread_target")
    elif params:
        _reject("rpc_global_envelope")


def validate_helper_response(message: object, owner_thread_id: str) -> None:
    """Reject a backend response that names a different thread."""

    if not isinstance(message, dict):
        _reject("response_not_object")
    result = message.get("result")
    if not isinstance(result, dict):
        return
    thread = result.get("thread")
    if isinstance(thread, dict) and "id" in thread and thread["id"] != owner_thread_id:
        _reject("response_thread_target")
    if "threadId" in result and result["threadId"] != owner_thread_id:
        _reject("response_thread_target")


def _websocket_frame(payload: bytes) -> bytes:
    """Encode a masked client text frame (RFC 6455, bounded to one frame)."""

    if len(payload) > 65535:
        _reject("rpc_frame_too_large")
    mask = secrets.token_bytes(4)
    masked = bytes(byte ^ mask[index % 4] for index, byte in enumerate(payload))
    length = len(masked)
    if length < 126:
        header = bytes((0x81, 0x80 | length))
    else:
        header = bytes((0x81, 0x80 | 126)) + length.to_bytes(2, "big")
    return header + mask + masked


async def _read_server_frame(reader: asyncio.StreamReader) -> tuple[bool, int, bytes]:
    try:
        first, second = await reader.readexactly(2)
    except asyncio.IncompleteReadError:
        _reject("websocket_eof")
    if first & 0x70:
        _reject("websocket_compression_or_reserved")
    fin = bool(first & 0x80)
    opcode = first & 0x0F
    masked = bool(second & 0x80)
    if masked:
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
    """Connect to the existing Unix listener and complete one WS handshake."""

    reader, writer = await asyncio.open_unix_connection(path)
    key = base64.b64encode(secrets.token_bytes(16))
    writer.write(
        b"GET / HTTP/1.1\r\n"
        b"Host: owner-helper\r\n"
        b"Upgrade: websocket\r\n"
        b"Connection: Upgrade\r\n"
        b"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: " + key + b"\r\n\r\n"
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
    header_names = {line.split(b":", 1)[0].lower(): line.split(b":", 1)[1].strip().lower()
                    for line in lines[1:] if b":" in line}
    if header_names.get(b"upgrade") != b"websocket" or header_names.get(b"connection") != b"upgrade":
        writer.close()
        await writer.wait_closed()
        _reject("websocket_headers")
    accept = None
    for line in lines[1:]:
        if line.lower().startswith(b"sec-websocket-accept:"):
            accept = line.split(b":", 1)[1].strip()
            break
    expected = base64.b64encode(
        hashlib.sha1(key + b"258EAFA5-E914-47DA-95CA-C5AB0DC85B11").digest()
    )
    if accept != expected:
        writer.close()
        await writer.wait_closed()
        _reject("websocket_accept")
    return reader, writer


@dataclass
class OwnerHelperConnection:
    reader: asyncio.StreamReader
    writer: asyncio.StreamWriter
    lease_id: str
    owner_thread_id: str
    role: str = "owner-helper"
    _closed: bool = False

    async def send_rpc(self, message: object) -> None:
        if self._closed:
            _reject("helper_closed")
        validate_helper_rpc(message, self.owner_thread_id)
        payload = json.dumps(message, separators=(",", ":"), sort_keys=True).encode()
        self.writer.write(_websocket_frame(payload))
        await self.writer.drain()

    async def read_rpc(self) -> dict:
        if self._closed:
            _reject("helper_closed")
        payload = await _read_server_text(self.reader)
        try:
            message = json.loads(payload)
        except (UnicodeDecodeError, json.JSONDecodeError):
            _reject("response_json")
        if not isinstance(message, dict) or message.get("jsonrpc") != "2.0" or "id" not in message:
            _reject("response_envelope")
        validate_helper_response(message, self.owner_thread_id)
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
    helper_identity: HelperIdentity
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
            "role": "owner-helper",
            "profile_id": self.profile_id,
            "owner_context_sha256": self.owner_context_sha256,
            "lease_id": self.lease_id,
            "owner_connection_id": self.owner_connection_id,
            "owner_epoch": self.owner_epoch,
            "owner_thread_id": self.owner_thread_id,
            "private_socket": self.private_socket,
            "helper_pid": identity.pid,
            "helper_uid": identity.uid,
            "helper_birth": identity.birth,
            "helper_executable": identity.executable,
            "helper_executable_sha256": identity.executable_sha256,
            "helper_source_sha256": identity.source_sha256,
        }
        if self.closed:
            _reject("lease_closed")
        if grant.version != 1:
            _reject("grant_version")
        for key, value in expected.items():
            if getattr(grant, key) != value:
                _reject(f"grant_{key}")
        if identity != self.helper_identity:
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
        connection = OwnerHelperConnection(
            reader=reader,
            writer=writer,
            lease_id=self.lease_id,
            owner_thread_id=self.owner_thread_id,
        )
        self._helpers.append(connection)
        return connection

    def owner_closed(self) -> None:
        """Mark the TUI lease dead and synchronously initiate helper EOF."""

        self.closed = True
        for helper in tuple(self._helpers):
            helper.writer.close()

    async def close(self) -> None:
        self.closed = True
        helpers = tuple(self._helpers)
        self._helpers.clear()
        for helper in helpers:
            await helper.close()
