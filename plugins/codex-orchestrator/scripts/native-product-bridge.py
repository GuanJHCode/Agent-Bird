#!/usr/bin/env python3
"""Pinned owner-helper bridge entrypoint.

The installed Go command launches this file with an absolute, hash-pinned
interpreter. The entrypoint validates both the immutable plugin package and
the separately pinned G0 attachment runtime before importing either runtime.
"""
from __future__ import annotations

import asyncio
from dataclasses import asdict
import hashlib
import importlib
import json
import os
from pathlib import Path
import re
import stat
import sys
import time


class EntryError(ValueError):
    pass


def require(condition, code):
    if not condition:
        raise EntryError(code)


def stable_bytes(path, maximum=16 * 1024 * 1024):
    path = Path(path)
    require(path.is_absolute(), "path_not_absolute")
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except OSError as exc:
        raise EntryError("file_unavailable") from exc
    try:
        before = os.fstat(fd)
        require(stat.S_ISREG(before.st_mode) and before.st_uid in (0, os.getuid())
                and before.st_nlink == 1 and 0 < before.st_size <= maximum,
                "file_identity")
        data = os.read(fd, before.st_size + 1)
        after = os.fstat(fd)
        identity = lambda item: (item.st_dev, item.st_ino, item.st_uid, item.st_mode,
                                 item.st_nlink, item.st_size, item.st_mtime_ns, item.st_ctime_ns)
        require(len(data) == before.st_size and identity(before) == identity(after), "file_changed")
        return data
    finally:
        os.close(fd)


def stable_json(path, *, private=False):
    if private:
        info = os.lstat(path)
        require(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode) == 0o600
                and info.st_uid == os.getuid() and info.st_nlink == 1,
                "private_file")
    try:
        value = json.loads(stable_bytes(path))
    except (UnicodeDecodeError, ValueError) as exc:
        raise EntryError("invalid_json") from exc
    require(isinstance(value, dict), "invalid_json")
    return value


def digest(path):
    return hashlib.sha256(stable_bytes(path, 512 * 1024 * 1024)).hexdigest()


def validate_runtime(request):
    required = {"version", "package_root", "package_manifest", "package_manifest_sha256",
                "interpreter", "interpreter_sha256", "g0_manifest", "g0_manifest_sha256",
                "g0_modules"}
    require(isinstance(request, dict) and set(request) == required and request["version"] == 1,
            "request_shape")
    interpreter = Path(request["interpreter"])
    require(interpreter.is_absolute() and Path(sys.executable).resolve() == interpreter.resolve(),
            "interpreter_changed")
    require(digest(interpreter) == request["interpreter_sha256"], "interpreter_changed")

    package_root = Path(request["package_root"])
    manifest_path = Path(request["package_manifest"])
    require(package_root.is_absolute() and manifest_path == package_root / "manifest.json",
            "package_binding")
    require(digest(manifest_path) == request["package_manifest_sha256"], "package_manifest_changed")
    manifest = stable_json(manifest_path)
    files = manifest.get("files")
    require(manifest.get("schema_version") == 1 and isinstance(files, list), "package_manifest")
    by_name = {row.get("path"): row for row in files if isinstance(row, dict)}
    expected = {
        "scripts/native-product-bridge.py": Path(__file__).resolve(),
        "lib/native_bridge/bridge.py": package_root / "lib" / "native_bridge" / "bridge.py",
    }
    for name, path in expected.items():
        row = by_name.get(name)
        require(isinstance(row, dict) and path == (package_root / name).resolve(), "package_source")
        info = os.lstat(path)
        require(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode) == row.get("mode")
                and digest(path) == row.get("sha256"), "package_source_changed")

    g0_manifest_path = Path(request["g0_manifest"])
    require(g0_manifest_path.is_absolute()
            and digest(g0_manifest_path) == request["g0_manifest_sha256"], "g0_manifest_changed")
    g0_manifest = stable_json(g0_manifest_path)
    pins = g0_manifest.get("file_pins")
    modules = request["g0_modules"]
    required_modules = {"delivery_adapter", "delivery_audit", "owner_helper", "proxy_transport", "receipt_store"}
    require(isinstance(pins, dict) and isinstance(modules, dict)
            and set(modules) == required_modules, "g0_runtime_shape")
    checked = {}
    for name in sorted(required_modules):
        path = Path(modules[name])
        require(path.is_absolute() and path.name == name + ".py", "g0_module_path")
        expected_hash = pins.get(str(path))
        require(isinstance(expected_hash, str) and digest(path) == expected_hash, "g0_module_changed")
        checked[name] = path
    return checked


def load_g0_runtime(runtime):
    checked = validate_runtime(runtime)
    for path in checked.values():
        parent = str(path.parent)
        if parent not in sys.path:
            sys.path.insert(0, parent)
    # delivery_adapter imports delivery_audit by its stable module name.
    order = ("delivery_audit", "owner_helper", "proxy_transport", "receipt_store", "delivery_adapter")
    modules = {}
    for name in order:
        module = importlib.import_module(name)
        require(Path(module.__file__).resolve() == checked[name].resolve(), "g0_module_resolution")
        modules[name] = module
    package_lib = Path(runtime["package_root"]) / "lib"
    sys.path.insert(0, str(package_lib))
    bridge = importlib.import_module("native_bridge.bridge")
    require(Path(bridge.__file__).resolve() == (package_lib / "native_bridge" / "bridge.py").resolve(),
            "bridge_module_resolution")
    modules["bridge"] = bridge
    return modules


def emit(value):
    print(json.dumps(value, sort_keys=True, separators=(",", ":")), flush=True)


async def control(reader, seconds=10):
    raw = await asyncio.wait_for(reader.readline(), seconds)
    require(raw and len(raw) <= 65536, "controller_eof")
    try:
        value = json.loads(raw)
    except (UnicodeDecodeError, ValueError) as exc:
        raise EntryError("controller_json") from exc
    require(isinstance(value, dict), "controller_json")
    return value


def process_inspector(proxy_transport, pid):
    birth, executable = proxy_transport._process_metadata(pid)
    require(type(birth) is str and type(executable) is str, "process_identity")
    return {"pid": pid, "uid": os.getuid(), "birth": birth, "executable": executable,
            "executable_sha256": digest(executable)}


def validate_controller_parent(config, proxy_transport):
    expected = config["controller_peer"]
    require(os.getppid() == expected.get("pid") and expected.get("uid") == os.getuid(),
            "controller_parent")
    current = process_inspector(proxy_transport, os.getppid())
    require(all(current.get(key) == expected.get(key)
                for key in ("pid", "uid", "birth", "executable", "executable_sha256")),
            "controller_parent_changed")


def socket_identity(path):
    info = os.lstat(path)
    require(stat.S_ISSOCK(info.st_mode) and info.st_uid == os.getuid()
            and not stat.S_IMODE(info.st_mode) & 0o077, "public_socket")
    return [info.st_dev, info.st_ino, info.st_uid, info.st_mode]


async def wait_for_id(connection, request_id, seconds=10):
    end = time.monotonic() + seconds
    packets = []
    while time.monotonic() < end:
        packet = await asyncio.wait_for(connection.read_rpc(), end - time.monotonic())
        packets.append(packet)
        if packet.get("id") == request_id:
            return packet, packets
    raise EntryError("rpc_timeout")


async def exact_history(connection, adapter, capability, thread_id):
    end = time.monotonic() + 10
    for attempt in range(20):
        request_id = 20 + attempt
        request = {"id": request_id, "method": "thread/read",
                   "params": {"threadId": thread_id, "includeTurns": True}}
        await connection.send_rpc(request)
        response, _ = await wait_for_id(connection, request_id, max(0.01, end - time.monotonic()))
        require("error" not in response, "history_read_rejected")
        read = {"request": request, "response": response}
        try:
            proof = adapter.audit_history(capability, [read])
        except Exception as exc:
            if str(exc) != "history_uncertain" or attempt == 19:
                raise
            await asyncio.sleep(min(0.1, max(0, end - time.monotonic())))
            continue
        expected = capability.native_envelope
        matches = []
        thread = response.get("result", {}).get("thread", {})
        require(thread.get("id") == thread_id, "history_owner_changed")
        for turn in thread.get("turns", []):
            for item in turn.get("items", []):
                if item.get("type") == "functionCallOutput" and item.get("name") == "g0_delivery" and item.get("namespace") == "orchestration":
                    try:
                        output = json.loads(item.get("output", ""))
                    except (TypeError, ValueError):
                        continue
                    if output == expected and turn.get("status") == "completed":
                        matches.append((turn.get("id"), item.get("id")))
        require(len(matches) == 1, "history_marker_ambiguous")
        return proof, matches[0]
    raise EntryError("history_uncertain")


async def run_helper():
    emit({"version": 1, "event": "boot", "pid": os.getpid()})
    reader = asyncio.StreamReader(limit=65536)
    await asyncio.get_running_loop().connect_read_pipe(
        lambda: asyncio.StreamReaderProtocol(reader), sys.stdin.buffer)
    config = await control(reader)
    required = {"version", "runtime", "manifest_path", "manifest_sha256", "grant_path",
                "grant_sha256", "profile_id", "public_socket", "public_socket_identity",
                "controller_peer", "owner_ready_receipt", "owner_ready_sha256",
                "bridge_root", "owner_capability_root"}
    require(set(config) == required and config["version"] == 1, "helper_config")
    modules = load_g0_runtime(config["runtime"])
    proxy_transport = modules["proxy_transport"]
    owner_helper = modules["owner_helper"]
    receipt_store = modules["receipt_store"]
    delivery_adapter = modules["delivery_adapter"]
    bridge_core = modules["bridge"]
    validate_controller_parent(config, proxy_transport)
    require(config["manifest_path"] == config["runtime"]["g0_manifest"]
            and config["manifest_sha256"] == config["runtime"]["g0_manifest_sha256"],
            "manifest_binding")
    grant = stable_json(config["grant_path"], private=True)
    require(digest(config["grant_path"]) == config["grant_sha256"]
            and grant.get("version") == 1 and grant.get("role") == "owner-helper"
            and grant.get("helper_pid") == os.getpid() and grant.get("helper_uid") == os.getuid()
            and grant.get("profile_id") == config["profile_id"], "helper_grant")
    current_helper = process_inspector(proxy_transport, os.getpid())
    require(grant.get("helper_birth") == current_helper["birth"]
            and grant.get("helper_executable") == current_helper["executable"]
            and grant.get("helper_executable_sha256") == current_helper["executable_sha256"],
            "helper_identity_changed")
    require(socket_identity(config["public_socket"]) == config["public_socket_identity"],
            "public_socket_changed")
    ws_reader, ws_writer = await owner_helper._open_websocket(config["public_socket"])
    peer = proxy_transport.peer_identity(ws_writer.get_extra_info("socket"))
    emit({"event": "public_connected", "role": "helper", "pid": os.getpid(),
          "profile_id": config["profile_id"], "manifest_sha256": config["manifest_sha256"],
          "grant_sha256": config["grant_sha256"], "public_peer": asdict(peer)})
    admitted = await control(reader)
    admitted_keys = {"action", "role", "lease_id", "manifest_sha256", "grant_sha256",
                     "receipt_sha256", "activation_id", "receipt_name"}
    require(set(admitted) == admitted_keys and admitted["action"] == "admitted"
            and admitted["role"] == "helper" and admitted["lease_id"] == grant["lease_id"]
            and admitted["manifest_sha256"] == config["manifest_sha256"]
            and admitted["grant_sha256"] == config["grant_sha256"], "helper_admission")
    manifest = stable_json(config["manifest_path"], private=True)
    store = receipt_store.ReceiptStore(Path(manifest["state_dir"]))
    connection = None
    ledger = None
    batch = None
    try:
        helper_captured = store.read(admitted["receipt_name"])
        owner_captured = store.read(config["owner_ready_receipt"])
        require(helper_captured is not None and owner_captured is not None, "attachment_receipt_missing")
        helper_ready, helper_sha = helper_captured
        owner_ready, owner_sha = owner_captured
        require(helper_sha == admitted["receipt_sha256"]
                and owner_sha == config["owner_ready_sha256"]
                and store.activation_id == admitted["activation_id"], "attachment_receipt_changed")
        attachment = bridge_core.OwnerAttachment.from_service_receipts(
            owner_ready=owner_ready, helper_ready=helper_ready, helper_grant=grant,
            inspect_process=lambda pid: process_inspector(proxy_transport, pid))
        owner_capability = bridge_core.OwnerCapabilityStore(config["owner_capability_root"]).bind(attachment)
        connection = owner_helper.OwnerHelperConnection(
            ws_reader, ws_writer, attachment.lease_id, attachment.controller_thread_id)
        await connection.send_rpc({"id": 1, "method": "initialize", "params": {
            "clientInfo": {"name": "g0-native-standalone", "version": "0.1.0"},
            "capabilities": {"experimentalApi": True}}})
        initialized, _ = await wait_for_id(connection, 1)
        require("error" not in initialized and isinstance(initialized.get("result"), dict),
                "initialize_rejected")
        await connection.send_rpc({"method": "initialized"})
        emit({"version": 1, "event": "attached", "owner_capability": str(owner_capability.path),
              "controller_thread": owner_capability.controller_thread_id,
              "origin_context_id": owner_capability.origin_context_id,
              "origin_pid": owner_capability.origin_pid, "origin_birth": owner_capability.origin_birth,
              "host_generation": owner_capability.host_generation,
              "attachment_proof_sha256": owner_capability.attachment_proof_sha256})
        deliver = await control(reader, 30)
        require(set(deliver) == {"action", "control_binary", "control_binary_sha256",
                                "control_file", "task_id"}
                and deliver["action"] == "deliver", "deliver_command")
        client = bridge_core.ControlPlaneClient(deliver["control_binary"], deliver["control_binary_sha256"])
        ledger = bridge_core.DeliveryLedger(config["bridge_root"])
        open_batches = ledger.open_batches(attachment.controller_thread_id, deliver["task_id"])
        confirmed = [item for item in open_batches if item.status == "history_confirmed"]
        require(len(confirmed) <= 1, "open_delivery_ambiguous")
        if confirmed:
            batch = confirmed[0]
            if (batch.path / "controller-ack-request.json").exists():
                ack_sha = client.replay_ack(batch)
                batch = ledger.mark_controller_acked(batch.delivery_id, ack_sha)
                emit({"version": 1, "event": "ack", "delivery_id": batch.delivery_id,
                      "status": batch.status, "controller_ack_sha256": ack_sha})
                await wait_helper_eof(connection, attachment)
                return
            proof_sha = batch.history_proof_sha256
            native_turn_id, native_item_id = batch.native_turn_id, batch.native_item_id
        else:
            events = client.collect(deliver["task_id"], control_file=Path(deliver["control_file"]))
            require(events, "no_pending_events")
            batch = ledger.prepare_and_claim(attachment, deliver["task_id"], events[:8])
            binding = delivery_adapter.DeliveryBinding(attachment.profile_id,
                attachment.controller_thread_id, attachment.controller_epoch, batch.delivery_id, 1)
            adapter = delivery_adapter.DeliveryAdapter.for_external(
                owner_context_sha256=attachment.owner_context_sha256)
            if batch.status == "pending":
                batch = ledger.mark_sending(batch.delivery_id)
                capability = adapter.attach_external_claim(binding, batch.path)
                packets = await adapter.send_tool_output_once(connection, capability,
                    request_id=2, wait_for_completion=False)
                receipts = [packet for packet in packets if packet.get("id") == 2]
                require(len(receipts) == 1 and "error" not in receipts[0], "transport_receipt")
                native_turn_id = receipts[0]["result"]["turn"]["id"]
                batch = ledger.mark_transport_accepted(batch.delivery_id, native_turn_id)
            else:
                if batch.status == "sending":
                    batch = ledger.mark_uncertain(batch.delivery_id, "recovered_sending")
                capability = adapter.reconcile_external_claim(binding, batch.path)
            proof, (native_turn_id, native_item_id) = await exact_history(
                connection, adapter, capability, attachment.controller_thread_id)
            proof_sha = hashlib.sha256(json.dumps({"reads_sha256": proof.reads_sha256,
                "summary": proof.summary}, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
            batch = ledger.confirm_history(batch.delivery_id, proof_sha, native_turn_id, native_item_id)
        emit({"version": 1, "event": "history_proof", "delivery_id": batch.delivery_id,
              "history_proof_sha256": proof_sha,
              "native_event_ids": [item["event_id"] for item in batch.envelope["events"]],
              "source_event_ids": [item["event_id"] for item in batch.source_events],
              "native_turn_id": native_turn_id, "native_item_id": native_item_id})
        decision = await control(reader, 30)
        require(set(decision) == {"action", "delivery_id", "history_proof_sha256", "decisions"}
                and decision["action"] == "decide" and decision["delivery_id"] == batch.delivery_id
                and decision["history_proof_sha256"] == proof_sha, "decision_binding")
        ack_sha = client.ack(batch, decision["decisions"], control_file=Path(deliver["control_file"]))
        batch = ledger.mark_controller_acked(batch.delivery_id, ack_sha)
        emit({"version": 1, "event": "ack", "delivery_id": batch.delivery_id,
              "status": batch.status, "controller_ack_sha256": ack_sha})
        await wait_helper_eof(connection, attachment)
    except BaseException:
        if ledger is not None and batch is not None:
            try:
                current = ledger.load(batch.delivery_id)
                if current.status in {"sending", "transport_accepted"}:
                    ledger.mark_uncertain(batch.delivery_id, "helper_failure")
            except Exception:
                pass
        raise
    finally:
        store.close()
        if connection is not None:
            await connection.close()
        else:
            ws_writer.close()
            await ws_writer.wait_closed()


async def wait_helper_eof(connection, attachment):
    while True:
        try:
            await connection.read_rpc()
        except Exception as exc:
            require(str(exc) == "websocket_eof", "helper_transport_failed")
            emit({"version": 1, "event": "helper_eof", "lease_id": attachment.lease_id})
            return


def request_path(args):
    require(len(args) == 3 and args[0] == "self-check" and args[1] == "--request",
            "usage")
    path = Path(args[2])
    require(path.is_absolute(), "request_path")
    return path


def main(args):
    request = stable_json(request_path(args), private=True)
    validate_runtime(request)
    return {"version": 1, "status": "ready"}


async def async_main(args):
    if args == ["helper"]:
        await run_helper()
        return None
    return main(args)


if __name__ == "__main__":
    try:
        output = asyncio.run(async_main(sys.argv[1:]))
    except Exception as exc:
        reason = str(exc)
        code = reason if re.fullmatch(r"[A-Za-z0-9_-]{1,64}", reason) else "entrypoint_failed"
        print(json.dumps({"version": 1, "status": "error", "error": code}, separators=(",", ":")), file=sys.stderr)
        raise SystemExit(2)
    if output is not None:
        print(json.dumps(output, separators=(",", ":")))
