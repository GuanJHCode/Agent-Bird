import hashlib
import json
import os
from pathlib import Path
import subprocess
import shutil
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
    DeliveryBinding,
    DeliveryError,
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
    if shutil.which("go") is None:
        pytest.skip("Go toolchain is required for the real CLI adapter test")
    binary = tmp / "g0-return-lab"
    subprocess.run(["go", "build", "-o", str(binary), "./tools/g0-return-lab"], cwd=ROOT, check=True)
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
    intent = adapter.prepare_and_claim(binding, task, "delivery_1", _events(), env={"CODEX_THREAD_ID": THREAD})
    assert intent["controller_thread"] == THREAD
    assert intent["revision"] == 1
    native = native_envelope_from_go_intent(intent, binding)
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
    adapter = DeliveryAdapter.__new__(DeliveryAdapter)
    result = adapter.audit_history(native, [read])
    assert result["transport_evidence"] == "exact_history_marker"
    assert result["business_ack_complete"] is False
    assert result["source_service_verified"] is False


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
    import asyncio
    adapter = DeliveryAdapter.__new__(DeliveryAdapter)
    packets = asyncio.run(adapter.send_tool_output_once(connection, native))
    assert len(packets) == 5 and connection.sent[0]["params"]["input"] == []
    with pytest.raises(DeliveryError, match="delivery_already_sent"):
        asyncio.run(adapter.send_tool_output_once(connection, native))


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
        adapter = DeliveryAdapter.__new__(DeliveryAdapter)
        async def run():
            connection = await lease.open_helper(grant, identity)
            try:
                packets = await adapter.send_tool_output_once(connection, native)
                with pytest.raises(DeliveryError, match="delivery_already_sent"):
                    await adapter.send_tool_output_once(connection, native)
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
