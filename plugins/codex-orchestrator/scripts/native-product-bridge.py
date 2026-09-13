#!/usr/bin/env python3
"""Pinned owner-helper bridge entrypoint.

The installed Go command launches this file with an absolute, hash-pinned
interpreter. The entrypoint validates both the immutable plugin package and
the separately pinned G0 attachment runtime before importing either runtime.
"""
from __future__ import annotations

import asyncio
import ctypes
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
    path = Path(path)
    require(path.is_absolute(), "path_not_absolute")
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except OSError as exc:
        raise EntryError("file_unavailable") from exc
    try:
        before = os.fstat(fd)
        require(stat.S_ISREG(before.st_mode) and before.st_uid in (0, os.getuid())
                and before.st_nlink == 1 and 0 < before.st_size <= 512 * 1024 * 1024,
                "file_identity")
        value = hashlib.sha256()
        remaining = before.st_size
        while remaining:
            block = os.read(fd, min(1024 * 1024, remaining))
            require(block, "file_changed")
            value.update(block)
            remaining -= len(block)
        require(os.read(fd, 1) == b"", "file_changed")
        after = os.fstat(fd)
        identity = lambda item: (item.st_dev, item.st_ino, item.st_uid, item.st_mode,
                                 item.st_nlink, item.st_size, item.st_mtime_ns, item.st_ctime_ns)
        require(identity(before) == identity(after), "file_changed")
        return value.hexdigest()
    finally:
        os.close(fd)


def kernel_executable():
    require(sys.platform == "darwin", "interpreter_changed")
    try:
        libproc = ctypes.CDLL("/usr/lib/libproc.dylib")
        libproc.proc_pidpath.argtypes = [ctypes.c_int, ctypes.c_void_p, ctypes.c_uint32]
        libproc.proc_pidpath.restype = ctypes.c_int
        buffer = ctypes.create_string_buffer(4096)
        length = libproc.proc_pidpath(os.getpid(), buffer, len(buffer))
    except (AttributeError, OSError, ValueError) as exc:
        raise EntryError("interpreter_changed") from exc
    require(0 < length < len(buffer), "interpreter_changed")
    executable = Path(os.fsdecode(buffer.value))
    require(executable.is_absolute() and executable.resolve() == executable,
            "interpreter_changed")
    return executable


def validate_runtime(request):
    required = {"version", "package_root", "package_manifest", "package_manifest_sha256",
                "interpreter", "interpreter_sha256", "g0_runtime_root",
                "g0_runtime_manifest", "g0_runtime_manifest_sha256", "g0_modules"}
    require(isinstance(request, dict) and set(request) == required and request["version"] == 1,
            "request_shape")
    interpreter = Path(request["interpreter"])
    require(interpreter.is_absolute() and interpreter.resolve() == interpreter
            and kernel_executable() == interpreter,
            "interpreter_changed")
    require(digest(interpreter) == request["interpreter_sha256"], "interpreter_changed")

    package_root = Path(request["package_root"])
    manifest_path = Path(request["package_manifest"])
    require(package_root.is_absolute() and package_root.resolve() == package_root
            and manifest_path == package_root / "manifest.json",
            "package_binding")
    package_info = package_root.lstat()
    require(stat.S_ISDIR(package_info.st_mode) and package_info.st_uid == os.getuid()
            and stat.S_IMODE(package_info.st_mode) == 0o700, "package_binding")
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

    runtime_root = Path(request["g0_runtime_root"])
    g0_manifest_path = Path(request["g0_runtime_manifest"])
    require(runtime_root.is_absolute() and runtime_root.resolve() == runtime_root
            and g0_manifest_path == runtime_root / "runtime-manifest.json",
            "g0_runtime_root")
    runtime_info = runtime_root.lstat()
    require(stat.S_ISDIR(runtime_info.st_mode) and runtime_info.st_uid == os.getuid()
            and stat.S_IMODE(runtime_info.st_mode) == 0o700, "g0_runtime_root")
    require(digest(g0_manifest_path) == request["g0_runtime_manifest_sha256"], "g0_manifest_changed")
    g0_manifest = stable_json(g0_manifest_path)
    interpreter_pin = g0_manifest.get("interpreter")
    rows = g0_manifest.get("files")
    modules = request["g0_modules"]
    required_modules = {"delivery_adapter", "delivery_audit", "owner_helper", "proxy_transport", "receipt_store"}
    require(g0_manifest.get("version") == 1 and isinstance(interpreter_pin, dict)
            and set(interpreter_pin) == {"path", "sha256"}
            and interpreter_pin["path"] == request["interpreter"]
            and interpreter_pin["sha256"] == request["interpreter_sha256"]
            and isinstance(rows, list) and isinstance(modules, dict)
            and set(modules) == required_modules, "g0_runtime_shape")
    pins = {}
    for row in rows:
        require(isinstance(row, dict) and set(row) == {"logical_id", "path", "sha256", "mode"}
                and isinstance(row["logical_id"], str) and isinstance(row["path"], str)
                and Path(row["path"]).name == row["path"] and row["logical_id"] not in pins,
                "g0_runtime_manifest")
        pins[row["logical_id"]] = row
    checked = {}
    for name in sorted(required_modules):
        path = Path(modules[name])
        row = pins.get(name)
        require(isinstance(row, dict) and path == runtime_root / row["path"]
                and path.name == name + ".py", "g0_module_path")
        info = path.lstat()
        require(stat.S_ISREG(info.st_mode) and info.st_uid == os.getuid()
                and stat.S_IMODE(info.st_mode) == row["mode"]
                and digest(path) == row["sha256"], "g0_module_changed")
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
        if not matches and attempt < 19:
            await asyncio.sleep(min(0.1, max(0, end - time.monotonic())))
            continue
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
    service_manifest = stable_json(config["manifest_path"], private=True)
    require(digest(config["manifest_path"]) == config["manifest_sha256"], "manifest_binding")
    service_pins = service_manifest.get("file_pins")
    require(isinstance(service_pins, dict)
            and all(service_pins.get(path) == digest(path)
                    for path in config["runtime"]["g0_modules"].values()), "manifest_binding")
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
    store = receipt_store.ReceiptStore(Path(service_manifest["state_dir"]))
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
        if deliver == {"action": "detach"}:
            emit({"version": 1, "event": "detached", "lease_id": attachment.lease_id})
            return
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


def private_directory(path):
    path = Path(path)
    require(path.is_absolute(), "driver_root")
    info = path.lstat()
    require(
        stat.S_ISDIR(info.st_mode)
        and not stat.S_ISLNK(info.st_mode)
        and info.st_uid == os.getuid()
        and stat.S_IMODE(info.st_mode) == 0o700,
        "driver_root",
    )
    return path


def replace_private(path, value):
    path = Path(path)
    data = json.dumps(value, sort_keys=True, separators=(",", ":")).encode() + b"\n"
    if path.exists():
        info = path.lstat()
        require(
            stat.S_ISREG(info.st_mode)
            and info.st_uid == os.getuid()
            and stat.S_IMODE(info.st_mode) == 0o600
            and info.st_nlink == 1,
            "driver_status",
        )
        stage = path.parent / ("." + path.name + "." + str(os.getpid()) + ".tmp")
        fd = os.open(stage, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        try:
            os.write(fd, data)
            os.fsync(fd)
        finally:
            os.close(fd)
        os.rename(stage, path)
    else:
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        try:
            os.write(fd, data)
            os.fsync(fd)
        finally:
            os.close(fd)
    directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


async def wait_receipt(store, name, seconds=10):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        value = store.read(name)
        if value is not None:
            return value
        await asyncio.sleep(0.02)
    raise EntryError("attachment_receipt_missing")


def bootstrap_remaining(deadline):
    remaining = deadline - time.monotonic()
    require(remaining > 0, "driver_bootstrap_expired")
    return remaining


async def wait_during_bootstrap(awaitable, deadline):
    try:
        return await asyncio.wait_for(awaitable, bootstrap_remaining(deadline))
    except TimeoutError as exc:
        raise EntryError("driver_bootstrap_expired") from exc


def attachment_live(attachment, proxy_transport):
    peers = (
        attachment.service_identity,
        attachment.backend_identity,
        {
            "pid": attachment.origin_pid,
            "uid": os.getuid(),
            "birth": attachment.origin_birth,
            "executable": attachment.origin_executable,
            "executable_sha256": attachment.origin_executable_sha256,
        },
    )
    try:
        for expected in peers:
            current = process_inspector(proxy_transport, expected["pid"])
            if any(
                current.get(key) != expected.get(key)
                for key in ("pid", "uid", "birth", "executable", "executable_sha256")
            ):
                return False
        return socket_identity(attachment.private_socket) == list(
            attachment.private_socket_identity
        )
    except (OSError, EntryError, KeyError):
        return False


def wait_actionable_events(client, task_id, control_file, cursor, attachment,
                           proxy_transport):
    while True:
        if not attachment_live(attachment, proxy_transport):
            raise EntryError("owner_detached")
        status, events, next_cursor = client.wait_events(
            task_id,
            control_file=control_file,
            cursor=cursor,
            timeout_ms=30_000,
        )
        if status == "timeout":
            continue
        if not attachment_live(attachment, proxy_transport):
            raise EntryError("owner_detached")
        return status, events, next_cursor


async def wait_decision(path, attachment, proxy_transport):
    while True:
        if not attachment_live(attachment, proxy_transport):
            raise EntryError("owner_detached")
        if path.exists():
            return stable_json(path, private=True)
        await asyncio.sleep(0.05)


async def run_driver(request_path_value):
    request_path_value = Path(request_path_value)
    request = stable_json(request_path_value, private=True)
    required = {
        "version",
        "runtime",
        "driver_root",
        "manifest_path",
        "manifest_sha256",
        "activation_id",
        "owner_ready_receipt",
        "owner_ready_sha256",
        "profile_id",
        "public_socket",
        "bridge_root",
        "owner_capability_root",
        "g1_state",
        "mode",
        "submit_request",
        "control_file",
        "task_id",
        "bootstrap_timeout_seconds",
        "watch_policy",
        "enable_test_fake",
    }
    require(
        set(request) == required
        and request["version"] == 1
        and request["mode"] in {"submit", "rebind"}
        and request["bootstrap_timeout_seconds"] == 300
        and request["watch_policy"] == "until_terminal_or_owner_detached"
        and type(request["enable_test_fake"]) is bool,
        "driver_request",
    )
    bootstrap_deadline = time.monotonic() + request["bootstrap_timeout_seconds"]
    modules = load_g0_runtime(request["runtime"])
    bootstrap_remaining(bootstrap_deadline)
    proxy_transport = modules["proxy_transport"]
    owner_helper = modules["owner_helper"]
    receipt_store = modules["receipt_store"]
    delivery_adapter = modules["delivery_adapter"]
    bridge_core = modules["bridge"]
    driver_root = private_directory(request["driver_root"])
    bridge_root = private_directory(request["bridge_root"])
    capability_root = private_directory(request["owner_capability_root"])
    private_directory(request["g1_state"])
    launcher = stable_json(driver_root / "launcher.json", private=True)
    require(
        launcher.get("version") == 1
        and launcher.get("pid") == os.getppid()
        and launcher.get("request_sha256") == digest(request_path_value),
        "driver_launcher",
    )
    launcher_peer = process_inspector(proxy_transport, os.getppid())
    require(
        all(
            launcher_peer.get(key) == launcher.get(key)
            for key in ("pid", "birth", "executable", "executable_sha256")
        ),
        "driver_launcher",
    )
    require(
        launcher_peer["executable"]
        == str(Path(request["runtime"]["package_root"]) / "bin" / "codex-orchestrator"),
        "driver_launcher",
    )
    manifest = stable_json(request["manifest_path"], private=True)
    require(
        digest(request["manifest_path"]) == request["manifest_sha256"]
        and manifest.get("public_socket") == request["public_socket"],
        "manifest_binding",
    )
    service_pins = manifest.get("file_pins")
    require(
        isinstance(service_pins, dict)
        and all(
            service_pins.get(path) == digest(path)
            for path in request["runtime"]["g0_modules"].values()
        ),
        "manifest_binding",
    )
    store = receipt_store.ReceiptStore(Path(manifest["state_dir"]))
    connection = None
    ws_writer = None
    base_status = None
    try:
        owner_captured = store.read(request["owner_ready_receipt"])
        require(
            owner_captured is not None
            and owner_captured[1] == request["owner_ready_sha256"],
            "owner_ready_changed",
        )
        require(store.activation_id == request["activation_id"], "owner_ready_changed")
        owner_ready = owner_captured[0]
        lease = owner_ready.get("owner_lease")
        require(
            isinstance(lease, dict)
            and lease.get("profile_id") == request["profile_id"],
            "owner_ready_changed",
        )
        helper = process_inspector(proxy_transport, os.getpid())
        grant = {
            key: lease[key]
            for key in (
                "profile_id",
                "owner_context_sha256",
                "lease_id",
                "owner_connection_id",
                "owner_epoch",
                "owner_thread_id",
                "private_socket",
            )
        }
        grant.update(
            version=1,
            role="owner-helper",
            helper_pid=os.getpid(),
            helper_uid=os.getuid(),
            helper_birth=helper["birth"],
            helper_executable=helper["executable"],
            helper_executable_sha256=helper["executable_sha256"],
            helper_source_sha256=digest(
                request["runtime"]["g0_modules"]["owner_helper"]
            ),
        )
        grants_root = private_directory(manifest["grants_dir"])
        grant_path = grants_root / f"{os.getpid()}.json"
        bridge_core._write_exclusive(grant_path, grant)
        require(
            socket_identity(request["public_socket"])
            == socket_identity(manifest["public_socket"]),
            "public_socket_changed",
        )
        ws_reader, ws_writer = await wait_during_bootstrap(
            owner_helper._open_websocket(request["public_socket"]),
            bootstrap_deadline,
        )
        helper_ready_name = f"helper-ready-{lease['lease_id']}-{os.getpid()}.json"
        helper_ready, helper_sha = await wait_during_bootstrap(
            wait_receipt(store, helper_ready_name), bootstrap_deadline
        )
        attachment = bridge_core.OwnerAttachment.from_service_receipts(
            owner_ready=owner_ready,
            helper_ready=helper_ready,
            helper_grant=grant,
            inspect_process=lambda pid: process_inspector(proxy_transport, pid),
        )
        owner_capability = bridge_core.OwnerCapabilityStore(capability_root).bind(
            attachment
        )
        connection = owner_helper.OwnerHelperConnection(
            ws_reader, ws_writer, attachment.lease_id, attachment.controller_thread_id
        )
        await wait_during_bootstrap(connection.send_rpc(
            {
                "id": 1,
                "method": "initialize",
                "params": {
                    "clientInfo": {
                        "name": "g0-native-standalone",
                        "version": "0.1.0",
                    },
                    "capabilities": {"experimentalApi": True},
                },
            }
        ), bootstrap_deadline)
        initialized, _ = await wait_during_bootstrap(
            wait_for_id(connection, 1), bootstrap_deadline
        )
        require(
            "error" not in initialized and isinstance(initialized.get("result"), dict),
            "initialize_rejected",
        )
        await wait_during_bootstrap(
            connection.send_rpc({"method": "initialized"}), bootstrap_deadline
        )
        control_binary = launcher_peer["executable"]
        client = bridge_core.ControlPlaneClient(
            control_binary,
            launcher_peer["executable_sha256"],
            enable_test_fake=request["enable_test_fake"],
        )
        if request["mode"] == "submit":
            template = stable_json(request["submit_request"], private=True)
            require(
                set(template) == {"version", "run_id", "plan_revision", "tasks"}
                and template["version"] == 1
                and isinstance(template["tasks"], list)
                and any(
                    item.get("id") == request["task_id"]
                    for item in template["tasks"]
                    if isinstance(item, dict)
                ),
                "submit_template",
            )
            derived = {
                "run_id": template["run_id"],
                "controller_thread": attachment.controller_thread_id,
                "plan_revision": template["plan_revision"],
                "origin_context_id": owner_capability.origin_context_id,
                "origin_pid": owner_capability.origin_pid,
                "origin_birth": owner_capability.origin_birth,
                "host_generation": owner_capability.host_generation,
                "owner_capability": str(owner_capability.path),
                "tasks": template["tasks"],
            }
            derived_path = driver_root / "g1-submit-derived.json"
            bridge_core._write_exclusive(derived_path, derived)
            response = client.submit(derived_path, state_dir=Path(request["g1_state"]))
            control_file = Path(response["control_file"])
            run_id = response["run_id"]
        else:
            require(
                request["submit_request"] == "" and request["control_file"],
                "rebind_request",
            )
            control_file = Path(request["control_file"])
            response = client.rebind(owner_capability, control_file=control_file)
            run_id = response["run_id"]
        bootstrap_remaining(bootstrap_deadline)
        base_status = {
            "version": 1,
            "status": "watching",
            "driver_root": str(driver_root),
            "driver_pid": os.getpid(),
            "driver_birth": helper["birth"],
            "launcher_pid": launcher_peer["pid"],
            "launcher_birth": launcher_peer["birth"],
            "task_id": request["task_id"],
            "run_id": run_id,
            "control_action_status": response["status"],
            "control_file": str(control_file),
            "owner_capability": str(owner_capability.path),
            "controller_thread": attachment.controller_thread_id,
            "origin_context_id": owner_capability.origin_context_id,
            "origin_pid": owner_capability.origin_pid,
            "origin_birth": owner_capability.origin_birth,
            "host_generation": owner_capability.host_generation,
            "attachment_proof_sha256": owner_capability.attachment_proof_sha256,
            "helper_ready_receipt": helper_ready_name,
            "helper_ready_sha256": helper_sha,
        }
        replace_private(driver_root / "status.json", base_status)
        ledger = bridge_core.DeliveryLedger(bridge_root)
        cursor = ""
        while True:
            status, events, next_cursor = wait_actionable_events(
                client,
                request["task_id"],
                control_file,
                cursor,
                attachment,
                proxy_transport,
            )
            batch = ledger.prepare_and_claim(attachment, request["task_id"], events)
            binding = delivery_adapter.DeliveryBinding(
                attachment.profile_id,
                attachment.controller_thread_id,
                attachment.controller_epoch,
                batch.delivery_id,
                1,
            )
            adapter = delivery_adapter.DeliveryAdapter.for_external(
                owner_context_sha256=attachment.owner_context_sha256
            )
            batch = ledger.mark_sending(batch.delivery_id)
            capability = adapter.attach_external_claim(binding, batch.path)
            packets = await adapter.send_tool_output_once(
                connection, capability, request_id=2, wait_for_completion=False
            )
            receipts = [packet for packet in packets if packet.get("id") == 2]
            require(
                len(receipts) == 1 and "error" not in receipts[0], "transport_receipt"
            )
            native_turn_id = receipts[0]["result"]["turn"]["id"]
            batch = ledger.mark_transport_accepted(batch.delivery_id, native_turn_id)
            proof, (native_turn_id, native_item_id) = await exact_history(
                connection, adapter, capability, attachment.controller_thread_id
            )
            proof_sha = hashlib.sha256(
                json.dumps(
                    {"reads_sha256": proof.reads_sha256, "summary": proof.summary},
                    sort_keys=True,
                    separators=(",", ":"),
                ).encode()
            ).hexdigest()
            batch = ledger.confirm_history(
                batch.delivery_id, proof_sha, native_turn_id, native_item_id
            )
            history = {
                "version": 1,
                "event": "history_ready",
                "driver_root": str(driver_root),
                "task_id": request["task_id"],
                "delivery_id": batch.delivery_id,
                "history_proof_sha256": proof_sha,
                "native_event_ids": [
                    item["event_id"] for item in batch.envelope["events"]
                ],
                "source_events": list(batch.source_events),
                "next_cursor": next_cursor,
            }
            history_path = driver_root / ("history-" + batch.delivery_id + ".json")
            bridge_core._write_exclusive(history_path, history)
            replace_private(
                driver_root / "status.json",
                dict(
                    base_status,
                    status="awaiting_owner_decision",
                    delivery_id=batch.delivery_id,
                    history_receipt=str(history_path),
                    history_receipt_sha256=digest(history_path),
                ),
            )
            decision_path = driver_root / "decisions" / (batch.delivery_id + ".json")
            decision = await wait_decision(decision_path, attachment, proxy_transport)
            require(
                set(decision)
                == {
                    "version",
                    "action",
                    "delivery_id",
                    "history_proof_sha256",
                    "decisions",
                }
                and decision["version"] == 1
                and decision["action"] == "decide"
                and decision["delivery_id"] == batch.delivery_id
                and decision["history_proof_sha256"] == proof_sha,
                "driver_decision",
            )
            ack_sha = client.ack(
                batch, decision["decisions"], control_file=control_file
            )
            batch = ledger.mark_controller_acked(batch.delivery_id, ack_sha)
            replace_private(
                driver_root / ("ack-" + batch.delivery_id + ".json"),
                {
                    "version": 1,
                    "event": "ack",
                    "delivery_id": batch.delivery_id,
                    "status": batch.status,
                    "controller_ack_sha256": ack_sha,
                },
            )
            current = client.status(request["task_id"], control_file=control_file)
            cursor = next_cursor
            if not cursor and current.get("status") in {
                "completed",
                "failed",
                "cancelled",
                "budget_exhausted",
                "blocked_dependency",
            }:
                replace_private(
                    driver_root / "status.json",
                    dict(
                        base_status, status="completed", delivery_id=batch.delivery_id
                    ),
                )
                return
            replace_private(
                driver_root / "status.json",
                dict(base_status, status="watching", cursor=cursor),
            )
    except Exception as exc:
        if base_status is not None:
            if str(exc) == "owner_detached":
                replace_private(
                    driver_root / "status.json",
                    dict(base_status, status="owner_detached"),
                )
                return
            code = (
                str(exc)
                if re.fullmatch(r"[A-Za-z0-9_-]{1,64}", str(exc))
                else "driver_failed"
            )
            replace_private(
                driver_root / "status.json",
                dict(base_status, status="failed", error=code),
            )
        raise
    finally:
        store.close()
        if connection is not None:
            await connection.close()
        elif ws_writer is not None:
            ws_writer.close()
            await ws_writer.wait_closed()


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
    if len(args) == 3 and args[:2] == ["driver", "--request"]:
        await run_driver(args[2])
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
