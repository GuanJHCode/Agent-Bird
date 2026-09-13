import hashlib
from contextlib import ExitStack
import json
import os
from pathlib import Path
import subprocess
import shutil
import socket
import tempfile
import time

import pytest

ROOT = Path(__file__).resolve().parents[3]
TOOLS = ROOT / "tools" / "g0-return-lab"
SCRIPTS = ROOT / "tasks" / "g0-completion" / "scripts"
import sys
sys.path.insert(0, str(SCRIPTS))

from delivery_adapter import (  # noqa: E402
    DeliveryAdapter,
    DeliveryCapability,
    DeliveryBinding,
    DeliveryError,
    HistoryProof,
    _go_batch_hash,
    native_envelope_from_go_intent,
)
from owner_helper import (  # noqa: E402
    HelperIdentity,
    OwnerHelperGrant,
    OwnerLease,
    validate_helper_rpc,
    validate_helper_server_message,
)

THREAD = "01a097ff-f802-7b02-9159-4b2bd0552626"


def _build(tmp: Path) -> Path:
    go = os.environ.get("G0_GO_EXECUTABLE", "/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go")
    if not Path(go).is_file():
        pytest.fail(f"configured Go toolchain is missing: {go}")
    binary = tmp / "g0-return-lab"
    subprocess.run([go, "build", "-o", str(binary), "."], cwd=ROOT / "tools" / "g0-return-lab", check=True)
    return binary


def _events() -> list[dict]:
    return [{"event_id": "event_1", "event_revision": 1, "kind": "result", "payload_hash": "a" * 64, "action_slot": "slot_1"}]


def _start(binary: Path, task: Path, nonce: str) -> None:
    env = {**os.environ, "CODEX_THREAD_ID": THREAD, "PYTHONDONTWRITEBYTECODE": "1"}
    subprocess.run([str(binary), "start", "--dir", str(task), "--nonce", nonce, "--delay", "100ms", "--controller-thread", THREAD], env=env, check=True, capture_output=True, text=True)
    subprocess.run([str(binary), "wait", "--dir", str(task), "--nonce", nonce, "--timeout", "2s"], env=env, check=True, capture_output=True, text=True)


def test_prepare_claim_and_native_hash_keep_go_hash_separate(tmp_path):
    binary = _build(tmp_path)
    task = tmp_path / "task"; task.mkdir(mode=0o700)
    nonce = "adapter_1"; _start(binary, task, nonce)
    binding = DeliveryBinding(profile_id="p", owner_thread_id=THREAD, owner_epoch=1, nonce=nonce, revision=1)
    adapter = DeliveryAdapter(binary, expected_sha256=hashlib.sha256(binary.read_bytes()).hexdigest())
    capability = adapter.prepare_and_claim(binding, task, "delivery_1", _events(), env={"CODEX_THREAD_ID": THREAD})
    intent = capability.go_intent
    assert intent["controller_thread"] == THREAD
    assert intent["revision"] == 1
    native = capability.native_envelope
    assert native["controller_thread_id"] == THREAD
    assert native["controller_epoch"] == 1
    assert native["events"] == _events()
    assert native["payload_hash"] == hashlib.sha256(json.dumps({k: v for k, v in native.items() if k != "payload_hash"}, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()).hexdigest()
    assert native["payload_hash"] != intent["payload_hash"]
    with pytest.raises(DeliveryError, match="claim_exists"):
        adapter.claim(binding, task, env={"CODEX_THREAD_ID": THREAD})


def test_mapping_rejects_cross_owner_and_old_revision_before_transport(tmp_path):
    binding = DeliveryBinding(profile_id="p", owner_thread_id=THREAD, owner_epoch=1, nonce="adapter_2", revision=2)
    intent = {"version": 1, "delivery_id": "delivery_1", "nonce": "adapter_2", "controller_thread": THREAD,
              "controller_epoch": 1, "revision": 1, "state": "prepared", "events": _events(), "payload_hash": "0" * 64}
    with pytest.raises(DeliveryError, match="revision"):
        native_envelope_from_go_intent(intent, binding)
    intent["revision"] = 2
    intent["controller_thread"] = "01b097ff-f802-7b02-9159-4b2bd0552626"
    with pytest.raises(DeliveryError, match="owner"):
        native_envelope_from_go_intent(intent, binding)


def test_history_audit_is_separate_from_business_decision():
    binding = DeliveryBinding(profile_id="p", owner_thread_id=THREAD, owner_epoch=1, nonce="adapter_3", revision=1)
    intent = {"version": 1, "delivery_id": "delivery_1", "nonce": binding.nonce, "controller_thread": THREAD,
              "controller_epoch": 1, "revision": 1, "state": "prepared", "events": _events(), "payload_hash": ""}
    intent["payload_hash"] = _go_batch_hash(intent)
    native = native_envelope_from_go_intent(intent, binding)
    read = {"request": {"id": 1, "method": "thread/read", "params": {"threadId": THREAD, "includeTurns": True}},
            "response": {"id": 1, "result": {"thread": {"id": THREAD, "turns": [{"id": "turn_1", "status": "completed", "items": [{"id": "item_1", "type": "functionCallOutput", "name": "g0_delivery", "namespace": "orchestration", "output": json.dumps(native, sort_keys=True, separators=(",", ":"))}]}]}}}}
    adapter = DeliveryAdapter.__new__(DeliveryAdapter); adapter._capability_token = object()
    result = adapter.audit_history(native, [read])
    assert result.summary["transport_evidence"] == "exact_history_marker"
    assert result.summary["business_ack_complete"] is False
    assert result.summary["source_service_verified"] is False


def test_missing_or_nonhandled_decision_stays_pending_without_go_ack(tmp_path):
    binding = DeliveryBinding(profile_id="p", owner_thread_id=THREAD, owner_epoch=1, nonce="adapter_pending", revision=1)
    intent = {"version": 1, "delivery_id": "delivery_1", "nonce": binding.nonce, "controller_thread": THREAD,
              "controller_epoch": 1, "revision": 1, "state": "prepared", "events": _events(), "payload_hash": ""}
    intent["payload_hash"] = _go_batch_hash(intent)
    adapter = DeliveryAdapter.__new__(DeliveryAdapter); adapter._capability_token = object()
    native = native_envelope_from_go_intent(intent, binding)
    capability = DeliveryCapability(binding, tmp_path, intent, native, "c" * 64, adapter._capability_token)
    proof = HistoryProof(native, {"business_ack_complete": False}, frozenset({"event_1"}), adapter._capability_token)
    empty = adapter.ack_confirmed(capability, proof, {}, command_ids={}, env={})
    assert empty.records == () and empty.pending_event_ids == ("event_1",)
    waiting = adapter.ack_confirmed(capability, proof, {"event_1": "waiting_user"}, command_ids={}, env={})
    assert waiting.records == () and waiting.nonhandled_decisions == {"event_1": "waiting_user"}
    with pytest.raises(DeliveryError, match="proof_owner"):
        adapter.ack_confirmed(capability, HistoryProof(native, {}, frozenset({"event_1"}), object()), {"event_1": "handled"}, command_ids={"event_1": "cmd_1"}, env={})


def test_send_tool_output_uses_owner_helper_wire_contract():
    class Connection:
        def __init__(self):
            self.sent = []
            self.pending = []

        async def send_rpc(self, packet):
            validate_helper_rpc(packet, THREAD)
            self.sent.append(packet)
            self.pending.extend([
                {"id": packet["id"], "result": {"turn": {"id": "turn_1", "status": "inProgress"}}},
                {"method": "turn/started", "params": {"threadId": THREAD, "turn": {"id": "turn_1"}}},
                {"method": "item/started", "params": {"threadId": THREAD, "turnId": "turn_1", "item": {"id": "item_1"}, "startedAtMs": 1}},
                {"method": "item/completed", "params": {"threadId": THREAD, "turnId": "turn_1", "item": {"id": "item_1"}, "completedAtMs": 2}},
                {"method": "turn/completed", "params": {"threadId": THREAD, "turn": {"id": "turn_1"}}},
            ])

        async def read_rpc(self):
            packet = self.pending.pop(0)
            validate_helper_server_message(packet, THREAD)
            return packet

    binding = DeliveryBinding(profile_id="p", owner_thread_id=THREAD, owner_epoch=1, nonce="adapter_4", revision=1)
    intent = {"version": 1, "delivery_id": "delivery_1", "nonce": binding.nonce, "controller_thread": THREAD,
              "controller_epoch": 1, "revision": 1, "state": "prepared", "events": _events(), "payload_hash": ""}
    intent["payload_hash"] = _go_batch_hash(intent)
    native = native_envelope_from_go_intent(intent, binding)
    connection = Connection()
    adapter = DeliveryAdapter.__new__(DeliveryAdapter); adapter._capability_token = object()
    import asyncio
    # The fake connection only exercises the packet contract; production UDS
    # coverage below uses a capability returned by prepare_and_claim.
    adapter._run = lambda *args, **kwargs: {"claim": "claimed", "delivery_id": "delivery_1", "revision": 1, "transport_status": "ready"}
    # Avoid global filesystem mutation in this contract-only test by using a temp claim.
    claim_path = Path(tempfile.mkdtemp(prefix="g0-claim-")) / "batch-send-claim.json"; claim_path.write_text("claim")
    capability = DeliveryCapability(binding, claim_path.parent, intent, native, hashlib.sha256(b"claim").hexdigest(), adapter._capability_token)
    packets = asyncio.run(adapter.send_tool_output_once(connection, capability))
    assert len(packets) == 5 and connection.sent[0]["params"]["input"] == []
    with pytest.raises(DeliveryError, match="delivery_already_sent"):
        asyncio.run(adapter.send_tool_output_once(connection, capability))


def test_send_tool_output_over_real_owner_helper_uds_process(tmp_path):
    fixture = ROOT / "tasks" / "g0-completion" / "tests" / "fixtures" / "owner_helper_tool_backend.py"
    socket_path = Path("/private/tmp") / f"g0-da-{os.getpid()}-{time.time_ns()}.sock"
    receipt = tmp_path / "receipt.json"
    process = subprocess.Popen([sys.executable, "-B", str(fixture), str(socket_path), str(receipt)], env={**os.environ, "PYTHONDONTWRITEBYTECODE": "1"})
    try:
        for _ in range(100):
            if socket_path.exists():
                break
            time.sleep(0.01)
        assert socket_path.exists()
        executable = Path(sys.executable).resolve()
        executable_sha = hashlib.sha256(executable.read_bytes()).hexdigest()
        identity = HelperIdentity(os.getpid(), os.getuid(), "adapter-process", str(executable), executable_sha, "c" * 64)
        lease = OwnerLease("p", "b" * 64, "lease_1", "owner_1", 1, THREAD, str(socket_path), process.pid, "backend", identity)
        grant = OwnerHelperGrant("owner-helper", "p", "b" * 64, "lease_1", "owner_1", 1, THREAD, str(socket_path), os.getpid(), os.getuid(), "adapter-process", str(executable), executable_sha, "c" * 64)
        native = {"version": 1, "delivery_id": "delivery_1", "controller_thread_id": THREAD, "controller_epoch": 1, "events": _events()}
        native["payload_hash"] = hashlib.sha256(json.dumps(native, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()).hexdigest()
        adapter = DeliveryAdapter.__new__(DeliveryAdapter); adapter._capability_token = object()
        claim_dir = Path(tempfile.mkdtemp(prefix="g0-uds-claim-")); claim_file = claim_dir / "batch-send-claim.json"; claim_file.write_text("claim")
        adapter._run = lambda *args, **kwargs: {"claim": "claimed", "delivery_id": "delivery_1", "revision": 1, "transport_status": "ready"}
        capability = DeliveryCapability(DeliveryBinding("p", THREAD, 1, "adapter_uds", 1), claim_dir, {"version":1,"delivery_id":"delivery_1","nonce":"adapter_uds","controller_thread":THREAD,"controller_epoch":1,"revision":1,"state":"prepared","events":_events(),"payload_hash":"0"*64}, native, hashlib.sha256(b"claim").hexdigest(), adapter._capability_token)
        async def run():
            connection = await lease.open_helper(grant, identity)
            try:
                packets = await adapter.send_tool_output_once(connection, capability)
                with pytest.raises(DeliveryError, match="delivery_already_sent"):
                    await adapter.send_tool_output_once(connection, capability)
                return packets
            finally:
                await connection.close()
        import asyncio
        packets = asyncio.run(run())
        assert {packet.get("method") for packet in packets} >= {"turn/started", "item/started", "item/completed", "turn/completed"}
        assert json.loads(receipt.read_text())["methods"].count("turn/start") == 1
    finally:
        process.terminate(); process.wait(timeout=5)
        socket_path.unlink(missing_ok=True)


def _write_go_history_client(path: Path) -> None:
    path.write_text(r'''
import json, os, socket

def frame(value):
    payload=json.dumps(value,separators=(",",":")).encode(); mask=b"ijkl"
    data=bytes(byte^mask[i%4] for i,byte in enumerate(payload))
    if len(payload)<126: header=bytes((0x81,0x80|len(payload)))
    else: header=bytes((0x81,0xfe))+len(payload).to_bytes(2,"big")
    return header+mask+data

def recv(sock):
    head=sock.recv(2)
    if len(head)<2: raise EOFError
    first,second=head; length=second&127
    if length==126: length=int.from_bytes(sock.recv(2),"big")
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
    key=b"ZGVsaXZlcnktaXNudW1lcmlj"
    sock.sendall(b"GET / HTTP/1.1\r\nHost: helper\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: "+key+b"\r\n\r\n")
    response=b""
    while b"\r\n\r\n" not in response: response+=sock.recv(4096)
    if not response.startswith(b"HTTP/1.1 101"): raise RuntimeError("upgrade")
    return sock

def request(sock, packet, request_id):
    sock.sendall(frame(packet))
    while True:
        value=recv(sock)
        if value.get("id") == request_id: return value

print("ready",flush=True)
if input().strip() != "go": raise SystemExit(2)
sock=connect(os.sys.argv[1])
request(sock,{"id":1,"method":"initialize","params":{"clientInfo":{"name":"g0-native-standalone","version":"0.1.0"},"capabilities":{"experimentalApi":True}}},1)
sock.sendall(frame({"method":"initialized"}))
thread=os.environ["OWNER_THREAD"]; intent=os.environ["DELIVERY_INTENT"]
sock.sendall(frame({"id":2,"method":"turn/start","params":{"threadId":thread,"input":[],"toolOutput":{"name":"g0_delivery","namespace":"orchestration","output":intent}}}))
seen=set(); response=False
while not response or seen != {"turn/started","item/started","item/completed","turn/completed"}:
    value=recv(sock)
    response=response or value.get("id")==2
    if value.get("method") in {"turn/started","item/started","item/completed","turn/completed"}: seen.add(value["method"])
history=request(sock,{"id":3,"method":"thread/read","params":{"threadId":thread,"includeTurns":True}},3)
with open(os.environ["HISTORY_PATH"],"w",encoding="utf-8") as stream:
    json.dump({"request":{"id":3,"method":"thread/read","params":{"threadId":thread,"includeTurns":True}},"response":history},stream,separators=(",",":"))
print("helper-history-ready",flush=True)
if input().strip() == "wait":
    try:
        while True: recv(sock)
    except EOFError: print("helper-eof",flush=True)
sock.close()
''')
    path.chmod(0o700)


def test_production_service_go_batch_history_and_ack_once(tmp_path):
    import asyncio
    import sys as _sys
    cold = ROOT / "tasks" / "g0-proxy-continuation" / "cold-start"
    for directory in (cold / "scripts", cold / "tests", ROOT / "tasks" / "g0-tui-proxy" / "scripts", ROOT / "tasks" / "g0-auth-preserving-activation" / "scripts"):
        if str(directory) not in _sys.path: _sys.path.insert(0, str(directory))
    import activation_service
    import proxy_transport
    import test_owner_helper_production_pipeline as owner_pipeline
    from test_activation_service import digest, policy, private_json
    from owner_helper import HelperIdentity, OwnerHelperConnection, OwnerHelperGrant, _open_websocket

    async def run():
        with tempfile.TemporaryDirectory(prefix="g0-delivery-prod-", dir="/private/tmp") as raw, ExitStack() as roots:
            root = Path(raw); public = root / "public.sock"
            binary = str(Path(proxy_transport._process_metadata(os.getpid())[1]))
            manifest, profiles, grants = policy(root, public, binary, roots)
            value = json.loads(manifest.read_text())
            backend = ROOT / "tasks" / "g0-completion" / "tests" / "fixtures" / "owner_helper_tool_backend.py"
            receipt = Path(profiles["a"]["workspace"]) / "backend-receipt.json"
            value["backend_argv"] = [binary, "-B", str(backend), "{socket_path}", str(receipt)]
            value["file_pins"] = {str(path): digest(path) for path in (backend, activation_service.__file__, ROOT / "tasks" / "g0-completion" / "scripts" / "owner_helper.py")}
            owner_helper = ROOT / "tasks" / "g0-completion" / "scripts" / "owner_helper.py"
            value["owner_helper"] = {"executable": binary, "executable_sha256": digest(binary), "source_path": str(owner_helper), "source_sha256": digest(owner_helper)}
            private_json(manifest, value)
            task = Path(profiles["a"]["workspace"]) / "go-task"; task.mkdir(mode=0o700)
            go = os.environ.get("G0_GO_EXECUTABLE", "/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go")
            go_binary = tmp_path / "g0-return-lab"
            subprocess.run([go, "build", "-o", str(go_binary), "."], cwd=ROOT / "tools" / "g0-return-lab", check=True)
            env = {**os.environ, "CODEX_THREAD_ID": THREAD, "PYTHONDONTWRITEBYTECODE": "1"}
            subprocess.run([str(go_binary), "start", "--dir", str(task), "--nonce", "adapter_prod", "--delay", "100ms", "--controller-thread", THREAD], env=env, check=True, capture_output=True)
            subprocess.run([str(go_binary), "wait", "--dir", str(task), "--nonce", "adapter_prod", "--timeout", "2s"], env=env, check=True, capture_output=True)
            owner_script = root / "owner.py"
            owner_pipeline._write_client(owner_script, False)
            listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM); listener.bind(str(public)); listener.listen(8); public.chmod(0o600)
            service = activation_service.ActivationService.from_manifest(manifest); await service.start(listener)
            owner = await asyncio.create_subprocess_exec(binary, "-B", str(owner_script), str(public), cwd=profiles["a"]["workspace"], stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE, env={"PATH":"/usr/bin:/bin", "HOME":str(root), "PYTHONDONTWRITEBYTECODE":"1"})
            helper_connection = None
            try:
                assert await owner_pipeline._line(owner) == "ready"
                birth, executable = proxy_transport._process_metadata(owner.pid)
                private_json(grants / f"{owner.pid}.json", {"version":1,"pid":owner.pid,"uid":os.getuid(),"birth":birth,"expected_executable":executable,"executable_sha256":digest(binary),"profile_id":"a"})
                owner.stdin.write(b"go\n"); await owner.stdin.drain(); assert await owner_pipeline._line(owner) == "owner-ready"
                for _ in range(100):
                    if service._owner_leases: break
                    await asyncio.sleep(.01)
                lease = next(iter(service._owner_leases.values())); binding = DeliveryBinding("a", lease.owner_thread_id, lease.owner_epoch, "adapter_prod", 1)
                adapter = DeliveryAdapter(go_binary, expected_sha256=hashlib.sha256(go_binary.read_bytes()).hexdigest())
                events = _events(); capability = adapter.prepare_and_claim(binding, task, "delivery_1", events, env={"CODEX_THREAD_ID": THREAD})
                intent = capability.go_intent; native = capability.native_envelope
                helper_birth, helper_executable = proxy_transport._process_metadata(os.getpid())
                private_json(grants / f"{os.getpid()}.json", {"version":1,"role":"owner-helper","profile_id":"a","owner_context_sha256":lease.owner_context_sha256,"lease_id":lease.lease_id,"owner_connection_id":lease.owner_connection_id,"owner_epoch":lease.owner_epoch,"owner_thread_id":lease.owner_thread_id,"private_socket":lease.private_socket,"helper_pid":os.getpid(),"helper_uid":os.getuid(),"helper_birth":helper_birth,"helper_executable":helper_executable,"helper_executable_sha256":digest(binary),"helper_source_sha256":digest(owner_helper)})
                helper_identity = HelperIdentity(os.getpid(), os.getuid(), helper_birth, helper_executable, digest(binary), digest(owner_helper))
                helper_grant = OwnerHelperGrant("owner-helper", "a", lease.owner_context_sha256, lease.lease_id, lease.owner_connection_id, lease.owner_epoch, lease.owner_thread_id, lease.private_socket, os.getpid(), os.getuid(), helper_birth, helper_executable, digest(binary), digest(owner_helper))
                reader, writer = await _open_websocket(str(public))
                helper_connection = OwnerHelperConnection(reader, writer, lease.lease_id, lease.owner_thread_id)
                await adapter.send_tool_output_once(helper_connection, capability)
                await helper_connection.send_rpc({"id":4,"method":"thread/read","params":{"threadId":lease.owner_thread_id,"includeTurns":True}})
                history_response = None
                while history_response is None:
                    packet = await helper_connection.read_rpc()
                    if packet.get("id") == 4: history_response = packet
                history = {"request":{"id":4,"method":"thread/read","params":{"threadId":lease.owner_thread_id,"includeTurns":True}},"response":history_response}
                proof = adapter.audit_history(native, [history]); assert proof.summary["transport_evidence"] == "exact_history_marker"
                ack = adapter.ack_confirmed(capability, proof, {"event_1":"handled"}, command_ids={"event_1":"cmd_1"}, env={"CODEX_THREAD_ID":THREAD})
                assert ack.records and ack.records[0]["status"] == "acknowledged"
                restarted = DeliveryAdapter(go_binary, expected_sha256=hashlib.sha256(go_binary.read_bytes()).hexdigest())
                with pytest.raises(DeliveryError, match="claim_exists"):
                    restarted.claim(binding, task, env={"CODEX_THREAD_ID":THREAD})
                with pytest.raises(DeliveryError, match="batch_intent_exists"):
                    restarted.prepare_and_claim(binding, task, "delivery_1", events, env={"CODEX_THREAD_ID":THREAD})
                owner.stdin.write(b"quit\n"); await owner.stdin.drain(); assert await owner_pipeline._wait_process(owner) == 0
                with pytest.raises(Exception):
                    await asyncio.wait_for(helper_connection.read_rpc(), 5)
            finally:
                if owner.returncode is None: owner.kill(); await owner.wait()
                if helper_connection is not None: await helper_connection.close()
                await service.close()
    asyncio.run(run())
