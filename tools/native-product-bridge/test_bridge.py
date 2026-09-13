import hashlib
import json
import os
from pathlib import Path
import sys
from dataclasses import replace

import pytest


ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(Path(__file__).parent))
sys.path.insert(0, str(ROOT / "tasks" / "g0-completion" / "scripts"))
sys.path.insert(0, str(ROOT / "plugins" / "codex-orchestrator" / "lib"))

from native_bridge.bridge import (  # noqa: E402
    BridgeError,
    ControlPlaneClient,
    DeliveryLedger,
    OwnerCapabilityStore,
    OwnerAttachment,
    build_ack_request,
    build_rebind_request,
)
from delivery_adapter import DeliveryAdapter, DeliveryBinding, DeliveryError  # noqa: E402


THREAD = "01a097ff-f802-7b02-9159-4b2bd0552626"
SERVICE = {
    "pid": 900,
    "uid": os.getuid(),
    "birth": "service-birth",
    "executable": "/verified/python",
    "executable_sha256": "b" * 64,
}
OWNER = {
    "pid": 901,
    "uid": os.getuid(),
    "birth": "owner-birth",
    "executable": "/verified/codex",
    "executable_sha256": "c" * 64,
}
HELPER = {
    "pid": 902,
    "uid": os.getuid(),
    "birth": "helper-birth",
    "executable": "/verified/python",
    "executable_sha256": "d" * 64,
}
BACKEND = {
    "pid": 903,
    "uid": os.getuid(),
    "birth": "backend-birth",
    "executable": "/verified/codex",
    "executable_sha256": "c" * 64,
}


def lease():
    return {
        "profile_id": "profile-a",
        "owner_context_sha256": "a" * 64,
        "lease_id": "012345abcdef",
        "owner_connection_id": "conn-1-012345abcdef",
        "owner_epoch": 1,
        "owner_thread_id": THREAD,
        "private_socket": "/private/tmp/verified-owner.sock",
        "backend_pid": BACKEND["pid"],
        "backend_birth": BACKEND["birth"],
        "backend_executable_sha256": "c" * 64,
        "private_socket_identity": [1, 2, os.getuid(), 0o140600],
        "service_identity": SERVICE,
    }


def attachment():
    owner_lease = lease()
    owner_ready = {
        "version": 1,
        "activation_id": "1" * 32,
        "manifest_sha256": "2" * 64,
        "service_identity": SERVICE,
        "frontend": OWNER,
        "owner_lease": owner_lease,
        "ready_published": True,
        "closed": False,
    }
    helper_ready = {
        "version": 1,
        "activation_id": owner_ready["activation_id"],
        "manifest_sha256": owner_ready["manifest_sha256"],
        "service_identity": SERVICE,
        "grant_sha256": "3" * 64,
        "owner_lease": {key: value for key, value in owner_lease.items() if key != "service_identity"},
        "frontend_peer": HELPER,
        "backend_peer": BACKEND,
    }
    grant = {
        "version": 1,
        "role": "owner-helper",
        "profile_id": owner_lease["profile_id"],
        "owner_context_sha256": owner_lease["owner_context_sha256"],
        "lease_id": owner_lease["lease_id"],
        "owner_connection_id": owner_lease["owner_connection_id"],
        "owner_epoch": owner_lease["owner_epoch"],
        "owner_thread_id": owner_lease["owner_thread_id"],
        "private_socket": owner_lease["private_socket"],
        "helper_pid": HELPER["pid"],
        "helper_uid": HELPER["uid"],
        "helper_birth": HELPER["birth"],
        "helper_executable": HELPER["executable"],
        "helper_executable_sha256": HELPER["executable_sha256"],
        "helper_source_sha256": "4" * 64,
    }
    live = {OWNER["pid"]: OWNER, HELPER["pid"]: HELPER,
            SERVICE["pid"]: SERVICE, BACKEND["pid"]: BACKEND}
    return OwnerAttachment.from_service_receipts(
        owner_ready=owner_ready,
        helper_ready=helper_ready,
        helper_grant=grant,
        inspect_process=lambda pid: live[pid],
    )


def events():
    return [
        {
            "version": 1,
            "event_id": "host-launch:41",
            "task_id": "task-a",
            "event_revision": 3,
            "work_revision": 2,
            "kind": "result",
            "payload_hash": "e" * 64,
            "action_slot": "result:task-a:2",
        }
    ]


def make_control_file(root):
    state = root / "state"
    directory = state / "control"
    directory.mkdir(parents=True, mode=0o700)
    state.chmod(0o700)
    path = directory / "control.json"
    path.write_text("opaque")
    path.chmod(0o600)
    return path


def test_attachment_requires_matching_live_native_owner_not_helper():
    checked = attachment()
    assert checked.controller_thread_id == THREAD
    assert checked.origin_pid == OWNER["pid"]
    assert checked.origin_birth == OWNER["birth"]

    owner_lease = lease()
    owner_ready = {
        "version": 1,
        "activation_id": "1" * 32,
        "manifest_sha256": "2" * 64,
        "service_identity": SERVICE,
        "frontend": HELPER,
        "owner_lease": owner_lease,
        "ready_published": True,
        "closed": False,
    }
    helper_ready = {
        "version": 1,
        "activation_id": "1" * 32,
        "manifest_sha256": "2" * 64,
        "service_identity": SERVICE,
        "grant_sha256": "3" * 64,
        "owner_lease": {key: value for key, value in owner_lease.items() if key != "service_identity"},
        "frontend_peer": HELPER,
        "backend_peer": BACKEND,
    }
    grant = {
        "version": 1,
        "role": "owner-helper",
        "profile_id": "profile-a",
        "owner_context_sha256": "a" * 64,
        "lease_id": "012345abcdef",
        "owner_connection_id": "conn-1-012345abcdef",
        "owner_epoch": 1,
        "owner_thread_id": THREAD,
        "private_socket": "/private/tmp/verified-owner.sock",
        "helper_pid": HELPER["pid"],
        "helper_uid": HELPER["uid"],
        "helper_birth": HELPER["birth"],
        "helper_executable": HELPER["executable"],
        "helper_executable_sha256": HELPER["executable_sha256"],
        "helper_source_sha256": "4" * 64,
    }
    with pytest.raises(BridgeError, match="owner_is_helper"):
        OwnerAttachment.from_service_receipts(
            owner_ready=owner_ready,
            helper_ready=helper_ready,
            helper_grant=grant,
            inspect_process=lambda pid: HELPER if pid == HELPER["pid"] else SERVICE,
        )


def test_uncertain_batch_blocks_second_send_and_exact_history_releases_slot(tmp_path):
    ledger = DeliveryLedger(tmp_path / "bridge")
    first = ledger.prepare_and_claim(attachment(), "task-a", events())
    assert first.envelope["controller_thread_id"] == THREAD
    assert first.envelope["events"] == [
        {
            "event_id": "e_42f5d01c8eaec7d0e4635e6a0e38f733",
            "event_revision": 3,
            "kind": "result",
            "payload_hash": "e" * 64,
            "action_slot": "s_12b95f6a0ae1068aa469426bb75bd90e",
        }
    ]
    assert first.source_events[0]["event_id"] == "host-launch:41"
    ledger.mark_sending(first.delivery_id)
    ledger.mark_uncertain(first.delivery_id, "transport_io")
    with pytest.raises(BridgeError, match="delivery_slot_uncertain"):
        ledger.prepare_and_claim(
            attachment(),
            "task-a",
            [{**events()[0], "event_id": "host-launch:42", "payload_hash": "f" * 64}],
        )
    ledger.confirm_history(first.delivery_id, "5" * 64, "turn_1", "item_1")
    second = ledger.prepare_and_claim(
        attachment(),
        "task-a",
        [{**events()[0], "event_id": "host-launch:42", "payload_hash": "f" * 64}],
    )
    assert second.delivery_id != first.delivery_id


def test_ack_maps_native_aliases_back_to_g1_only_after_history_proof(tmp_path):
    ledger = DeliveryLedger(tmp_path / "bridge")
    batch = ledger.prepare_and_claim(attachment(), "task-a", events())
    alias = batch.envelope["events"][0]["event_id"]
    decisions = {alias: {"decision": "handled", "command_id": "cmd_native_1"}}
    with pytest.raises(BridgeError, match="history_not_confirmed"):
        build_ack_request(batch, decisions, control_file=tmp_path / "control.json")
    ledger.mark_sending(batch.delivery_id)
    ledger.mark_transport_accepted(batch.delivery_id, "turn_1")
    ledger.confirm_history(batch.delivery_id, "6" * 64, "turn_1", "item_1")
    confirmed = ledger.load(batch.delivery_id)
    control_file = make_control_file(tmp_path)
    request = build_ack_request(confirmed, decisions, control_file=control_file)
    assert request == {
        "version": 1,
        "task_id": "task-a",
        "control_file": str(control_file),
        "delivery_id": batch.delivery_id,
        "history_proof_sha256": "6" * 64,
        "decisions": [
            {
                "event_id": "host-launch:41",
                "event_revision": 3,
                "event_hash": "e" * 64,
                "action_slot": "result:task-a:2",
                "decision": "handled",
                "command_id": "cmd_native_1",
            }
        ],
    }


def test_control_plane_ack_uses_immutable_request_and_original_event_id(tmp_path):
    ledger = DeliveryLedger(tmp_path / "bridge")
    batch = ledger.prepare_and_claim(attachment(), "task-a", events())
    ledger.mark_sending(batch.delivery_id)
    ledger.confirm_history(batch.delivery_id, "6" * 64, "turn_1", "item_1")
    batch = ledger.load(batch.delivery_id)
    control = make_control_file(tmp_path)
    captured = tmp_path / "captured.json"
    executable = tmp_path / "fake-orchestrator"
    executable.write_text(
        "#!" + sys.executable + "\n"
        "import json,sys\n"
        "p=sys.argv[sys.argv.index('--request')+1]\n"
        "v=json.load(open(p))\n"
        f"open({str(captured)!r},'w').write(json.dumps(v,sort_keys=True))\n"
        "print(json.dumps({'version':1,'status':'acknowledged','delivery_id':v['delivery_id']}))\n"
    )
    executable.chmod(0o700)
    client = ControlPlaneClient(executable, hashlib.sha256(executable.read_bytes()).hexdigest())
    alias = batch.envelope["events"][0]["event_id"]
    receipt_sha = client.ack(
        batch,
        {alias: {"decision": "handled", "command_id": "cmd_native_1"}},
        control_file=control,
    )
    assert len(receipt_sha) == 64
    sent = json.loads(captured.read_text())
    assert sent["decisions"][0]["event_id"] == "host-launch:41"
    assert sent["control_file"] == str(control)
    request_path = batch.path / "controller-ack-request.json"
    original = request_path.read_bytes()
    assert client.ack(
        batch,
        {alias: {"decision": "handled", "command_id": "cmd_native_1"}},
        control_file=control,
    ) == receipt_sha
    assert request_path.read_bytes() == original
    assert ledger.find_open(THREAD, "task-a").delivery_id == batch.delivery_id
    assert client.replay_ack(batch) == receipt_sha


def test_control_plane_collect_does_not_read_or_copy_control_token(tmp_path):
    control = make_control_file(tmp_path)
    state = control.parent.parent
    control.write_text("this-is-an-opaque-token-file")
    executable = tmp_path / "fake-orchestrator"
    captured = tmp_path / "collect-argv.json"
    payload = {"version": 1, "status": "pending", "events": events()}
    executable.write_text(
        "#!" + sys.executable + "\nimport json,sys\n"
        f"open({str(captured)!r},'w').write(json.dumps(sys.argv))\n"
        f"print({json.dumps(json.dumps(payload))})\n"
    )
    executable.chmod(0o700)
    client = ControlPlaneClient(executable, hashlib.sha256(executable.read_bytes()).hexdigest())
    assert client.collect("task-a", control_file=control) == events()
    assert json.loads(captured.read_text())[1:3] == ["collect", "--state-dir"]
    assert json.loads(captured.read_text())[3] == str(state)
    assert control.read_text() == "this-is-an-opaque-token-file"


def test_control_plane_wait_events_preserves_actionable_cursor(tmp_path):
    control = make_control_file(tmp_path)
    captured = tmp_path / "wait-argv.json"
    executable = tmp_path / "fake-orchestrator"
    payload = {"version": 1, "status": "events", "events": events(),
               "next_cursor": "delivery-v1-7-0123456789abcdef01234567"}
    executable.write_text(
        "#!" + sys.executable + "\nimport json,sys\n"
        f"open({str(captured)!r},'w').write(json.dumps(sys.argv))\n"
        f"print({json.dumps(json.dumps(payload))})\n"
    )
    executable.chmod(0o700)
    client = ControlPlaneClient(executable, hashlib.sha256(executable.read_bytes()).hexdigest())
    status, pending, cursor = client.wait_events("task-a", control_file=control,
        cursor="delivery-v1-3-fedcba987654321001234567", timeout_ms=1000)
    assert status == "events" and pending == events() and cursor == payload["next_cursor"]
    argv = json.loads(captured.read_text())
    assert argv[1] == "wait-events" and "--cursor" in argv and "--timeout-ms" in argv
    assert "--include-diagnostics" not in argv


def test_control_plane_preserves_source_environment_but_strips_tool_capabilities(tmp_path, monkeypatch):
    control = make_control_file(tmp_path)
    captured = tmp_path / "environment.json"
    executable = tmp_path / "fake-orchestrator"
    payload = {"version": 1, "status": "pending", "events": []}
    executable.write_text(
        "#!" + sys.executable + "\nimport json,os\n"
        f"open({str(captured)!r},'w').write(json.dumps(dict(os.environ)))\n"
        f"print({json.dumps(json.dumps(payload))})\n"
    )
    executable.chmod(0o700)
    monkeypatch.setenv("HTTPS_PROXY", "http://synthetic-proxy.invalid")
    monkeypatch.setenv("PROVIDER_SESSION_MARKER", "synthetic-source-session")
    monkeypatch.setenv("ORCHESTRATOR_CONTROL_TOKEN", "must-not-cross")
    monkeypatch.setenv("ORCHESTRATOR_REPORT_CAPABILITY", "must-not-cross")
    monkeypatch.setenv("ORCHESTRATOR_FUTURE_SLOT", "must-not-cross")
    client = ControlPlaneClient(executable, hashlib.sha256(executable.read_bytes()).hexdigest())
    assert client.collect("task-a", control_file=control) == []
    environment = json.loads(captured.read_text())
    assert environment["HTTPS_PROXY"] == "http://synthetic-proxy.invalid"
    assert environment["PROVIDER_SESSION_MARKER"] == "synthetic-source-session"
    assert "ORCHESTRATOR_CONTROL_TOKEN" not in environment
    assert "ORCHESTRATOR_REPORT_CAPABILITY" not in environment
    assert "ORCHESTRATOR_FUTURE_SLOT" not in environment


def test_owner_capability_generation_advances_for_same_thread_new_tui(tmp_path):
    store = OwnerCapabilityStore(tmp_path / "owner-capabilities")
    first = attachment()
    first_capability = store.bind(first)
    assert first_capability.host_generation == "generation-00000001"
    persisted = json.loads(first_capability.path.read_text())
    assert persisted["owner_attachment"]["service_identity"] == SERVICE
    assert persisted["owner_attachment"]["backend_identity"] == BACKEND
    assert hashlib.sha256(json.dumps(persisted["owner_attachment"], sort_keys=True,
        separators=(",", ":")).encode()).hexdigest() == first_capability.attachment_proof_sha256
    assert store.bind(first).path.read_bytes() == first_capability.path.read_bytes()

    resumed = replace(
        first,
        lease_id="fedcba543210",
        owner_connection_id="conn-1-fedcba543210",
        activation_id="9" * 32,
        origin_pid=991,
        origin_birth="resumed-owner-birth",
    )
    resumed_capability = store.bind(resumed)
    assert resumed_capability.origin_context_id == first_capability.origin_context_id
    assert resumed_capability.controller_thread_id == first_capability.controller_thread_id
    assert resumed_capability.host_generation == "generation-00000002"
    assert resumed_capability.origin_pid == 991


def test_rebind_projection_uses_verified_owner_capability_without_token(tmp_path):
    capability = OwnerCapabilityStore(tmp_path / "owner-capabilities").bind(attachment())
    control = make_control_file(tmp_path)
    request = build_rebind_request(capability, control_file=control)
    assert request == {
        "version": 1,
        "owner_capability": str(capability.path),
        "control_file": str(control),
        "controller_thread": THREAD,
        "origin_context_id": capability.origin_context_id,
        "origin_pid": OWNER["pid"],
        "origin_birth": OWNER["birth"],
        "host_generation": "generation-00000001",
        "attachment_proof_sha256": capability.attachment_proof_sha256,
    }
    assert "token" not in json.dumps(request)


def test_control_plane_rebind_uses_verified_projection_and_owner_response(tmp_path):
    capability = OwnerCapabilityStore(tmp_path / "owner-capabilities").bind(attachment())
    control = make_control_file(tmp_path)
    captured = tmp_path / "rebind-captured.json"
    executable = tmp_path / "fake-orchestrator"
    executable.write_text(
        "#!" + sys.executable + "\nimport json,sys\n"
        "assert sys.argv[1]=='rebind-owner'\n"
        "p=sys.argv[sys.argv.index('--request')+1];v=json.load(open(p))\n"
        f"open({str(captured)!r},'w').write(json.dumps(v,sort_keys=True))\n"
        "print(json.dumps({'version':1,'status':'owner_rebound','run_id':'run-a','origin_context_id':v['origin_context_id'],'host_generation':v['host_generation']}))\n"
    )
    executable.chmod(0o700)
    client = ControlPlaneClient(executable, hashlib.sha256(executable.read_bytes()).hexdigest())
    response = client.rebind(capability, control_file=control)
    assert response == {"version": 1, "status": "owner_rebound", "run_id": "run-a",
        "origin_context_id": capability.origin_context_id,
        "host_generation": capability.host_generation}
    sent = json.loads(captured.read_text())
    assert sent["owner_capability"] == str(capability.path)
    assert sent["origin_pid"] == OWNER["pid"]


def test_external_g0_capability_revalidates_bridge_claim_before_wire_write(tmp_path):
    ledger = DeliveryLedger(tmp_path / "bridge")
    batch = ledger.prepare_and_claim(attachment(), "task-a", events())
    ledger.mark_sending(batch.delivery_id)
    batch = ledger.load(batch.delivery_id)
    adapter = DeliveryAdapter.for_external(owner_context_sha256="a" * 64)
    binding = DeliveryBinding("profile-a", THREAD, 1, batch.delivery_id, 1)
    capability = adapter.attach_external_claim(binding, batch.path)

    class Connection:
        def __init__(self):
            self.writes = []
            self.responses = [{"id": 7, "result": {"turn": {"id": "turn_1", "status": "inProgress"}}}]

        async def send_rpc(self, packet):
            self.writes.append(packet)

        async def read_rpc(self):
            return self.responses.pop(0)

    connection = Connection()
    import asyncio

    packets = asyncio.run(adapter.send_tool_output_once(connection, capability, request_id=7, wait_for_completion=False))
    assert packets[0]["result"]["turn"]["id"] == "turn_1"
    assert json.loads(connection.writes[0]["params"]["toolOutput"]["output"]) == batch.envelope
    with pytest.raises(DeliveryError, match="delivery_already_sent"):
        asyncio.run(adapter.send_tool_output_once(connection, capability, request_id=8, wait_for_completion=False))


def test_external_reconcile_is_history_only_after_crash(tmp_path):
    ledger = DeliveryLedger(tmp_path / "bridge")
    batch = ledger.prepare_and_claim(attachment(), "task-a", events())
    ledger.mark_sending(batch.delivery_id)
    ledger.mark_uncertain(batch.delivery_id, "process_exit")
    adapter = DeliveryAdapter.for_external(owner_context_sha256="a" * 64)
    binding = DeliveryBinding("profile-a", THREAD, 1, batch.delivery_id, 1)
    capability = adapter.reconcile_external_claim(binding, batch.path)
    assert capability.send_disabled is True
    with pytest.raises(DeliveryError, match="send_disabled"):
        import asyncio
        asyncio.run(adapter.send_tool_output_once(object(), capability, wait_for_completion=False))


def test_external_claim_preserves_positive_owner_epoch(tmp_path):
    current = replace(attachment(), controller_epoch=7)
    ledger = DeliveryLedger(tmp_path / "bridge")
    batch = ledger.prepare_and_claim(current, "task-a", events())
    ledger.mark_sending(batch.delivery_id)
    adapter = DeliveryAdapter.for_external(owner_context_sha256="a" * 64)
    binding = DeliveryBinding("profile-a", THREAD, 7, batch.delivery_id, 1)
    capability = adapter.attach_external_claim(binding, batch.path)
    assert capability.native_envelope["controller_epoch"] == 7
