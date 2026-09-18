"""Real-process synthetic backend for the owner-helper toolOutput path."""
import asyncio
import base64
import hashlib
import json
import os
import signal
import struct
import sys
import time

THREAD = "01a097ff-f802-7b02-9159-4b2bd0552626"
TURN = "turn_delivery_1"
ITEM = "item_delivery_1"
SOCKET = sys.argv[1]
RECEIPT = sys.argv[2]
MODE = sys.argv[3] if len(sys.argv) > 3 else "complete"


def server_frame(value):
    payload = json.dumps(value, separators=(",", ":"), sort_keys=True).encode()
    if len(payload) < 126:
        return bytes((0x81, len(payload))) + payload
    if len(payload) <= 65535:
        return b"\x81\x7e" + struct.pack(">H", len(payload)) + payload
    raise ValueError("frame too large")


async def receive(reader):
    first, second = await reader.readexactly(2)
    if first & 0x70 or (first & 0x0f) != 1:
        raise ValueError("unsupported frame")
    if not second & 0x80:
        raise ValueError("client frame must be masked")
    length = second & 127
    if length == 126:
        length = struct.unpack(">H", await reader.readexactly(2))[0]
    elif length == 127:
        raise ValueError("frame too large")
    mask = await reader.readexactly(4)
    payload = await reader.readexactly(length)
    return json.loads(bytes(byte ^ mask[i % 4] for i, byte in enumerate(payload)))


def write_receipt(records):
    data = {"version": 1, "thread_id": THREAD, "methods": [r["method"] for r in records], "records": records}
    temp = RECEIPT + ".tmp"
    with open(temp, "w", encoding="utf-8") as stream:
        json.dump(data, stream, sort_keys=True, separators=(",", ":"))
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(temp, RECEIPT)


def turn_body(status):
    return {"id": TURN, "items": [], "itemsView": "full", "status": status,
            "error": None, "startedAt": None, "completedAt": None, "durationMs": None}


def item_body(output):
    return {"type": "functionCallOutput", "id": ITEM, "name": "g0_delivery",
            "namespace": "orchestration", "output": output}


async def main():
    stop = asyncio.Event()
    clients = set()
    records = []
    history_output = None
    loop = asyncio.get_running_loop()
    loop.add_signal_handler(signal.SIGTERM, stop.set)

    async def connection(reader, writer):
        nonlocal history_output
        clients.add(writer)
        try:
            header = await reader.readuntil(b"\r\n\r\n")
            key = next(line.split(b":", 1)[1].strip() for line in header.split(b"\r\n") if line.lower().startswith(b"sec-websocket-key:"))
            accept = base64.b64encode(hashlib.sha1(key + b"258EAFA5-E914-47DA-95CA-C5AB0DC85B11").digest())
            writer.write(b"HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + b"\r\n\r\n")
            await writer.drain()
            while True:
                message = await receive(reader)
                method = message.get("method")
                record = {"method": method, "id": message.get("id")}
                if method == "turn/start":
                    params = message.get("params", {})
                    tool = params.get("toolOutput", {}) if isinstance(params, dict) else {}
                    output = tool.get("output") if isinstance(tool, dict) else None
                    record.update({"thread_target": params.get("threadId") if isinstance(params, dict) else None,
                                  "input_empty": isinstance(params, dict) and params.get("input") == [],
                                  "tool_output_sha256": hashlib.sha256(output.encode()).hexdigest() if isinstance(output, str) else None})
                    valid = (set(params) == {"threadId", "input", "toolOutput"}
                             and params.get("threadId") == THREAD and params.get("input") == []
                             and isinstance(tool, dict) and set(tool) == {"name", "namespace", "output"}
                             and tool.get("name") == "g0_delivery" and tool.get("namespace") == "orchestration"
                             and isinstance(output, str))
                    if not valid:
                        record["accepted"] = False
                        records.append(record); write_receipt(records)
                        writer.write(server_frame({"id": message.get("id"), "error": {"code": -32602, "message": "invalid delivery"}}))
                        await writer.drain()
                        continue
                    record["accepted"] = True
                    history_output = output
                    records.append(record); write_receipt(records)
                    writer.write(server_frame({"id": message["id"], "result": {"turn": turn_body("inProgress")}}))
                    if MODE != "receipt-only":
                        writer.write(server_frame({"method": "account/rateLimits/updated", "params": {"rateLimits": {
                        "limitId": "codex", "limitName": "Codex", "normalModelSlug": "gpt-5",
                        "primary": {"usedPercent": 1, "windowDurationMins": 300, "resetsAt": 1700000000},
                        "secondary": None, "credits": {"hasCredits": False, "unlimited": False, "balance": None},
                        "individualLimit": None, "spendControlReached": False, "planType": "plus", "rateLimitReachedType": None,
                    }}, "emittedAtMs": int(time.time() * 1000)}))
                        writer.write(server_frame({"method": "turn/started", "params": {"threadId": THREAD, "turn": turn_body("inProgress")}, "emittedAtMs": int(time.time() * 1000)}))
                    item = item_body(output)
                    if MODE != "receipt-only":
                        writer.write(server_frame({"method": "item/started", "params": {"threadId": THREAD, "turnId": TURN, "item": item, "startedAtMs": int(time.time() * 1000)}}))
                        writer.write(server_frame({"method": "item/agentMessage/delta", "params": {"threadId": THREAD, "turnId": TURN, "itemId": ITEM, "delta": "delivery"}, "emittedAtMs": int(time.time() * 1000)}))
                        writer.write(server_frame({"method": "item/completed", "params": {"threadId": THREAD, "turnId": TURN, "item": item, "completedAtMs": int(time.time() * 1000)}}))
                        writer.write(server_frame({"method": "turn/completed", "params": {"threadId": THREAD, "turn": turn_body("completed")}, "emittedAtMs": int(time.time() * 1000)}))
                elif method == "initialize":
                    records.append(record); write_receipt(records)
                    writer.write(server_frame({"id": message["id"], "result": {"codexHome": os.environ.get("CODEX_HOME", ""), "userAgent": "owner-helper-fixture/1"}}))
                elif method == "thread/start":
                    records.append(record); write_receipt(records)
                    thread = {"id": THREAD, "cwd": os.getcwd(), "ephemeral": True, "turns": []}
                    writer.write(server_frame({"id": message["id"], "result": {"thread": thread}}))
                    writer.write(server_frame({"method": "thread/started", "params": {"thread": thread}}))
                elif method == "initialized":
                    records.append(record); write_receipt(records)
                    continue
                elif method == "thread/read":
                    records.append(record); write_receipt(records)
                    item = {"id": ITEM, "type": "functionCallOutput", "name": "g0_delivery", "namespace": "orchestration", "output": history_output or ""}
                    turn = {"id": TURN, "status": "completed", "items": [item], "itemsView": "full"}
                    writer.write(server_frame({"id": message["id"], "result": {"thread": {"id": THREAD, "turns": [turn]}}}))
                elif method in ("server/diagnostics", "remoteControl/status/read"):
                    records.append(record); write_receipt(records)
                    writer.write(server_frame({"id": message["id"], "result": {}}))
                else:
                    records.append(record); write_receipt(records)
                    writer.write(server_frame({"id": message.get("id"), "result": {}}))
                await writer.drain()
        except (asyncio.IncompleteReadError, ConnectionError, OSError, ValueError, StopIteration, json.JSONDecodeError):
            pass
        finally:
            clients.discard(writer)
            writer.close()
            try:
                await writer.wait_closed()
            except OSError:
                pass

    server = await asyncio.start_unix_server(connection, path=SOCKET)
    os.chmod(SOCKET, 0o600)
    await stop.wait()
    server.close()
    for writer in list(clients):
        writer.close()
    await server.wait_closed()
    write_receipt(records)


asyncio.run(main())
