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
