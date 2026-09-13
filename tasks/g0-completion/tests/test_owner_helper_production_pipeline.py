import asyncio
from contextlib import ExitStack
import hashlib
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile

import pytest

ROOT = Path(__file__).resolve().parents[2]
COLD = ROOT / "g0-proxy-continuation" / "cold-start"
for directory in (COLD / "scripts", COLD / "tests", ROOT / "g0-tui-proxy" / "scripts", ROOT / "g0-auth-preserving-activation" / "scripts"):
    sys.path.insert(0, str(directory))
import activation_service
import auth_isolation
import proxy_transport
from test_activation_service import digest, policy, private_json

FIXTURES = COLD / "tests" / "fixtures"
OWNER_HELPER = ROOT / "g0-completion" / "scripts" / "owner_helper.py"


def _write_client(path: Path, helper: bool) -> None:
    body = r'''
import json, os, socket, sys, time

def frame(value):
    payload=json.dumps(value,separators=(',',':')).encode(); mask=b'abcd'
    data=bytes(byte^mask[i%4] for i,byte in enumerate(payload))
    header=bytes((0x81,0x80|len(payload))) if len(payload)<126 else bytes((0x81,0xfe))+len(payload).to_bytes(2,'big')
    return header+mask+data

def recv(sock):
    header=sock.recv(2)
    if len(header)<2: raise EOFError
    first,second=header; length=second&127
    if length==126: length=int.from_bytes(sock.recv(2),'big')
    mask=sock.recv(4) if second&128 else None
    data=b''
    while len(data)<length:
        chunk=sock.recv(length-len(data))
        if not chunk: raise EOFError
        data+=chunk
    if mask: data=bytes(byte^mask[i%4] for i,byte in enumerate(data))
    return json.loads(data)

def connect(path):
    sock=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM); sock.connect(path)
    key=b'dGhlIHNhbXBsZSBub25jZQ=='
    sock.sendall(b'GET / HTTP/1.1\r\nHost: helper\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: '+key+b'\r\n\r\n')
    response=b''
    while b'\r\n\r\n' not in response: response+=sock.recv(4096)
    if not response.startswith(b'HTTP/1.1 101'): raise RuntimeError('upgrade')
    return sock

def initialize(sock):
    sock.sendall(frame({'id':1,'method':'initialize','params':{'clientInfo':{'name':'g0-native-standalone','version':'0.1.0'},'capabilities':{'experimentalApi':True}}}))
    while recv(sock).get('id') != 1: pass
    sock.sendall(frame({'method':'initialized'}))

print('ready',flush=True)
if sys.stdin.readline().strip() != 'go': raise SystemExit(2)
sock=connect(sys.argv[1]); initialize(sock)
'''
    if helper:
        body += r'''
sock.sendall(frame({'id':2,'method':'server/diagnostics','params':{}}))
while recv(sock).get('id') != 2: pass
print('helper-ready',flush=True)
command=sys.stdin.readline().strip()
if command == 'wait':
    try:
        while True: recv(sock)
    except EOFError: print('helper-eof',flush=True)
sock.close()
'''
    else:
        body += r'''
sock.sendall(frame({'id':2,'method':'thread/start','params':{'cwd':os.getcwd()}}))
thread=None; started=False
while thread is None or not started:
    packet=recv(sock)
    if packet.get('id') == 2: thread=packet['result']['thread']
    if packet.get('method') == 'thread/started': started=packet.get('params',{}).get('thread',{}).get('id') == (thread or {}).get('id')
print('owner-ready',flush=True)
command=sys.stdin.readline().strip()
if command == 'quit': sock.close()
'''
    path.write_text(body)
    path.chmod(0o700)


async def _line(process):
    return (await asyncio.wait_for(process.stdout.readline(), 5)).decode().strip()


async def _wait_process(process):
    return await asyncio.wait_for(process.wait(), 5)


def test_real_activation_owner_ready_helper_second_ws_and_owner_cleanup(tmp_path):
    async def run():
        with tempfile.TemporaryDirectory(prefix="g0-auth-", dir="/private/tmp") as raw, ExitStack() as roots:
            root = Path(raw)
            public = root / "public.sock"
            binary = str(Path(proxy_transport._process_metadata(os.getpid())[1]))
            manifest, profiles, grants = policy(root, public, binary, roots)
            value = json.loads(manifest.read_text())
            backend = FIXTURES / "probe_ws_backend.py"
            value["backend_argv"] = [str(binary), "-B", str(backend), "{socket_path}", "success"]
            value["file_pins"] = {str(path): digest(path) for path in (backend, activation_service.__file__, OWNER_HELPER)}
            value["owner_helper"] = {
                "executable": str(binary),
                "executable_sha256": digest(binary),
                "source_path": str(OWNER_HELPER),
                "source_sha256": digest(OWNER_HELPER),
            }
            value["idle_seconds"] = 2.0
            private_json(manifest, value)
            owner_script = root / "owner.py"
            helper_script = root / "helper.py"
            _write_client(owner_script, False)
            _write_client(helper_script, True)
            listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            listener.bind(str(public)); listener.listen(8); public.chmod(0o600)
            service = activation_service.ActivationService.from_manifest(manifest)
            await service.start(listener)
            env = {"PATH": "/usr/bin:/bin", "HOME": str(root), "PYTHONDONTWRITEBYTECODE": "1"}
            owner = await asyncio.create_subprocess_exec(str(binary), "-B", str(owner_script), str(public), cwd=profiles["a"]["workspace"], stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE, env=env)
            helper = None
            try:
                assert await _line(owner) == "ready"
                birth, executable = proxy_transport._process_metadata(owner.pid)
                private_json(grants / f"{owner.pid}.json", {
                    "version": 1, "pid": owner.pid, "uid": os.getuid(), "birth": birth,
                    "expected_executable": executable, "executable_sha256": digest(binary), "profile_id": "a",
                })
                owner.stdin.write(b"go\n"); await owner.stdin.drain()
                owner_line=await _line(owner)
                assert owner_line == "owner-ready"
                for _ in range(100):
                    if service._owner_leases: break
                    await asyncio.sleep(.01)
                assert len(service._owner_leases) == 1
                lease = next(iter(service._owner_leases.values()))
                record = service._record_for_lease(lease.lease_id)
                assert record and len(service.backend_records) == 1
                helper = await asyncio.create_subprocess_exec(str(binary), "-B", str(helper_script), str(public), cwd=profiles["a"]["workspace"], stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE, env=env)
                assert await _line(helper) == "ready"
                helper_birth, helper_executable = proxy_transport._process_metadata(helper.pid)
                private_json(grants / f"{helper.pid}.json", {
                    "version": 1, "role": "owner-helper", "profile_id": "a",
                    "owner_context_sha256": lease.owner_context_sha256, "lease_id": lease.lease_id,
                    "owner_connection_id": lease.owner_connection_id, "owner_epoch": lease.owner_epoch,
                    "owner_thread_id": lease.owner_thread_id, "private_socket": lease.private_socket,
                    "helper_pid": helper.pid, "helper_uid": os.getuid(), "helper_birth": helper_birth,
                    "helper_executable": helper_executable, "helper_executable_sha256": digest(binary),
                    "helper_source_sha256": digest(OWNER_HELPER),
                })
                helper.stdin.write(b"go\n"); await helper.stdin.drain()
                assert await _line(helper) == "helper-ready"
                assert len(service.backend_records) == 1
                assert record["pid"] == lease.backend_pid and record["birth"] == lease.backend_birth
                assert record.get("helpers") and record["helpers"][0]["owner_thread_id"] == lease.owner_thread_id
                helper.stdin.write(b"wait\n"); await helper.stdin.drain()
                owner.stdin.write(b"quit\n"); await owner.stdin.drain()
                assert await _wait_process(owner) == 0
                assert await _line(helper) == "helper-eof"
                helper.stdin.close()
                await _wait_process(helper)
                for _ in range(100):
                    if record.get("process_stopped"): break
                    await asyncio.sleep(.01)
                assert record.get("process_stopped") is True
                assert record.get("helpers")[0]["state"] == "closed"
            finally:
                if owner.returncode is None: owner.kill(); await owner.wait()
                if helper is not None and helper.returncode is None: helper.kill(); await helper.wait()
                await service.close()

    asyncio.run(run())


def _write_delivery_client(path: Path) -> None:
    path.write_text(r'''
import json, os, socket, sys

def frame(value):
    payload=json.dumps(value,separators=(",",":")).encode(); mask=b"efgh"
    data=bytes(byte^mask[i%4] for i,byte in enumerate(payload))
    header=bytes((0x81,0x80|len(payload))) if len(payload)<126 else bytes((0x81,0xfe))+len(payload).to_bytes(2,"big")
    return header+mask+data

def recv(sock):
    header=sock.recv(2)
    if len(header)<2: raise EOFError
    first,second=header; length=second&127
    if length==126: length=int.from_bytes(sock.recv(2),"big")
    if length==127: raise RuntimeError("frame")
    mask=sock.recv(4) if second&128 else None
    data=b""
    while len(data)<length:
        chunk=sock.recv(length-len(data))
        if not chunk: raise EOFError
        data+=chunk
    if mask: data=bytes(byte^mask[i%4] for i,byte in enumerate(data))
    return json.loads(data)

def connect(path):
    sock=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM); sock.connect(path)
    key=b"ZGVsaXZlcnktZml4dHVyZQ=="
    sock.sendall(b"GET / HTTP/1.1\r\nHost: helper\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: "+key+b"\r\n\r\n")
    response=b""
    while b"\r\n\r\n" not in response: response+=sock.recv(4096)
    if not response.startswith(b"HTTP/1.1 101"): raise RuntimeError("upgrade")
    return sock

def init(sock):
    sock.sendall(frame({"id":1,"method":"initialize","params":{"clientInfo":{"name":"g0-native-standalone","version":"0.1.0"},"capabilities":{"experimentalApi":True}}}))
    while recv(sock).get("id") != 1: pass
    sock.sendall(frame({"method":"initialized"}))

def wait_eof(sock):
    try:
        while True: recv(sock)
    except EOFError:
        print("attacker-eof",flush=True)

print("ready",flush=True)
if sys.stdin.readline().strip() != "go": raise SystemExit(2)
sock=connect(sys.argv[1]); init(sock)
mode=os.environ.get("DELIVERY_MODE","valid")
if mode == "valid":
    intent=os.environ["DELIVERY_INTENT"]
    sock.sendall(frame({"id":2,"method":"server/diagnostics","params":{}}))
    while recv(sock).get("id") != 2: pass
    sock.sendall(frame({"id":3,"method":"turn/start","params":{"threadId":os.environ["OWNER_THREAD"],"input":[],"toolOutput":{"name":"g0_delivery","namespace":"orchestration","output":intent}}}))
    seen=set(); response=False
    while not response or seen != {"turn/started","item/started","item/completed","turn/completed"}:
        packet=recv(sock)
        if packet.get("id") == 3: response=True
        if packet.get("method") in {"turn/started","item/started","item/completed","turn/completed"}: seen.add(packet["method"])
    print("helper-delivery-ready",flush=True)
    if sys.stdin.readline().strip() == "wait":
        wait_eof(sock)
else:
    bad_thread=os.environ["OWNER_THREAD"] if mode == "ordinary" else "01foreignthread000000000000000000"
    if mode == "ordinary":
        params={"threadId":bad_thread,"input":["ordinary prompt"]}
    else:
        params={"threadId":bad_thread,"input":[],"toolOutput":{"name":"g0_delivery","namespace":"orchestration","output":os.environ["DELIVERY_INTENT"]}}
    sock.sendall(frame({"id":2,"method":"turn/start","params":params}))
    wait_eof(sock)
sock.close()
''')
    path.chmod(0o700)


def _delivery_intent(thread_id: str) -> str:
    body = {
        "version": 1, "delivery_id": "delivery_1", "controller_thread_id": thread_id,
        "controller_epoch": 1,
        "events": [{"event_id": "event_1", "event_revision": 1, "kind": "result", "payload_hash": "d" * 64, "action_slot": "ack_r1"}],
    }
    body["payload_hash"] = hashlib.sha256(json.dumps(body, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    return json.dumps(body, sort_keys=True, separators=(",", ":"))


def test_real_activation_owner_helper_tool_output_notifications_and_negative_gate(tmp_path):
    async def run():
        with tempfile.TemporaryDirectory(prefix="g0-owner-helper-", dir="/private/tmp") as raw, ExitStack() as roots:
            root = Path(raw)
            public = root / "public.sock"
            binary = str(Path(proxy_transport._process_metadata(os.getpid())[1]))
            manifest, profiles, grants = policy(root, public, binary, roots)
            receipt = Path(profiles["a"]["workspace"]) / "backend-receipt.json"
            value = json.loads(manifest.read_text())
            backend = Path(__file__).resolve().parents[2] / "g0-completion" / "tests" / "fixtures" / "owner_helper_tool_backend.py"
            value["backend_argv"] = [str(binary), "-B", str(backend), "{socket_path}", str(receipt)]
            value["file_pins"] = {str(path): digest(path) for path in (backend, activation_service.__file__, OWNER_HELPER)}
            value["owner_helper"] = {"executable": str(binary), "executable_sha256": digest(binary), "source_path": str(OWNER_HELPER), "source_sha256": digest(OWNER_HELPER)}
            value["idle_seconds"] = 2.0
            private_json(manifest, value)
            owner_script = root / "owner.py"; helper_script = root / "helper.py"
            attack_script = root / "attack.py"
            _write_client(owner_script, False); _write_delivery_client(helper_script); _write_delivery_client(attack_script)
            listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM); listener.bind(str(public)); listener.listen(8); public.chmod(0o600)
            service = activation_service.ActivationService.from_manifest(manifest); await service.start(listener)
            env = {"PATH": "/usr/bin:/bin", "HOME": str(root), "PYTHONDONTWRITEBYTECODE": "1"}
            owner = await asyncio.create_subprocess_exec(str(binary), "-B", str(owner_script), str(public), cwd=profiles["a"]["workspace"], stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE, env=env)
            helper = attacker1 = attacker2 = None
            try:
                assert await _line(owner) == "ready"
                birth, executable = proxy_transport._process_metadata(owner.pid)
                private_json(grants / f"{owner.pid}.json", {"version": 1, "pid": owner.pid, "uid": os.getuid(), "birth": birth, "expected_executable": executable, "executable_sha256": digest(binary), "profile_id": "a"})
                owner.stdin.write(b"go\n"); await owner.stdin.drain(); assert await _line(owner) == "owner-ready"
                for _ in range(100):
                    if service._owner_leases: break
                    await asyncio.sleep(.01)
                assert service._owner_leases
                lease = next(iter(service._owner_leases.values())); record = service._record_for_lease(lease.lease_id)
                intent = _delivery_intent(lease.owner_thread_id)

                async def start_helper(mode, nonce):
                    process = await asyncio.create_subprocess_exec(str(binary), "-B", str(helper_script if mode == "valid" else attack_script), str(public), cwd=profiles["a"]["workspace"], stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE, env={**env, "DELIVERY_MODE": mode, "OWNER_THREAD": lease.owner_thread_id, "DELIVERY_INTENT": intent})
                    assert await _line(process) == "ready"
                    birth, executable = proxy_transport._process_metadata(process.pid)
                    private_json(grants / f"{process.pid}.json", {"version": 1, "role": "owner-helper", "profile_id": "a", "owner_context_sha256": lease.owner_context_sha256, "lease_id": lease.lease_id, "owner_connection_id": lease.owner_connection_id, "owner_epoch": lease.owner_epoch, "owner_thread_id": lease.owner_thread_id, "private_socket": lease.private_socket, "helper_pid": process.pid, "helper_uid": os.getuid(), "helper_birth": birth, "helper_executable": executable, "helper_executable_sha256": digest(binary), "helper_source_sha256": digest(OWNER_HELPER)})
                    process.stdin.write(b"go\n"); await process.stdin.drain()
                    return process

                helper = await start_helper("valid", "valid")
                assert await _line(helper) == "helper-delivery-ready"
                attacker1 = await start_helper("ordinary", "ordinary")
                assert await _line(attacker1) == "attacker-eof"
                assert await _wait_process(attacker1) == 0
                attacker2 = await start_helper("cross", "cross")
                assert await _line(attacker2) == "attacker-eof"
                assert await _wait_process(attacker2) == 0
                await asyncio.sleep(.05)
                receipt_data = json.loads(receipt.read_text())
                turns = [entry for entry in receipt_data["records"] if entry["method"] == "turn/start"]
                assert len(turns) == 1 and turns[0]["accepted"] is True and turns[0]["thread_target"] == lease.owner_thread_id and turns[0]["input_empty"] is True
                assert receipt_data["methods"].count("turn/start") == 1
                assert record["pid"] == lease.backend_pid and len(service.backend_records) == 1
                helper.stdin.write(b"wait\n"); await helper.stdin.drain()
                owner.stdin.write(b"quit\n"); await owner.stdin.drain(); assert await _wait_process(owner) == 0
                assert await _line(helper) == "attacker-eof"
                helper.stdin.close(); await _wait_process(helper)
                for _ in range(100):
                    if record.get("process_stopped"): break
                    await asyncio.sleep(.01)
                assert record.get("process_stopped") is True and record.get("helpers") and all(item["state"] == "closed" for item in record["helpers"])
            finally:
                for process in (owner, helper, attacker1, attacker2):
                    if process is not None and process.returncode is None: process.kill(); await process.wait()
                await service.close()

    asyncio.run(run())
