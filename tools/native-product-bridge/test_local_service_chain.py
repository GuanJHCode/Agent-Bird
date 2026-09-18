import asyncio
from contextlib import ExitStack
import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import sys
import tempfile


import pytest


ROOT = Path(__file__).resolve().parents[2]
COLD = ROOT / "tasks" / "g0-proxy-continuation" / "cold-start"
for directory in (
    COLD / "scripts",
    COLD / "tests",
    ROOT / "tasks" / "g0-tui-proxy" / "scripts",
    ROOT / "tasks" / "g0-auth-preserving-activation" / "scripts",
    ROOT / "tasks" / "g0-completion" / "tests",
):
    sys.path.insert(0, str(directory))

import activation_service  # noqa: E402
import proxy_transport  # noqa: E402
from test_activation_service import digest, policy, private_json  # noqa: E402
from test_owner_helper_production_pipeline import _line, _wait_process, _write_client  # noqa: E402


ENTRY = ROOT / "plugins" / "codex-orchestrator" / "scripts" / "native-product-bridge.py"
CORE = ROOT / "plugins" / "codex-orchestrator" / "lib" / "native_bridge" / "bridge.py"
OWNER_HELPER = ROOT / "tasks" / "g0-completion" / "scripts" / "owner_helper.py"
DELIVERY_ADAPTER = ROOT / "tasks" / "g0-completion" / "scripts" / "delivery_adapter.py"
DELIVERY_AUDIT = ROOT / "tasks" / "g0-pending-resolution" / "scripts" / "delivery_audit.py"
RECEIPT_STORE = ROOT / "tasks" / "g0-global-delivery-validation" / "scripts" / "receipt_store.py"
BACKEND = ROOT / "tasks" / "g0-completion" / "tests" / "fixtures" / "owner_helper_tool_backend.py"
RUNTIME_SOURCES = {
    name: ROOT / "runtime/native" / f"{name}.py"
    for name in ("projectproxy_launchd_entrypoint", "launch_activation", "activation_service",
                 "proxy_transport", "proxy_observer", "owned_child_guard", "owner_helper",
                 "auth_isolation", "delivery_adapter", "delivery_audit", "receipt_store")
}


def package_copy(root):
    package = root / "package"
    package.mkdir(mode=0o700)
    (package / "scripts").mkdir(parents=True)
    (package / "lib" / "native_bridge").mkdir(parents=True)
    entry = package / "scripts" / ENTRY.name
    core = package / "lib" / "native_bridge" / "bridge.py"
    init = package / "lib" / "native_bridge" / "__init__.py"
    shutil.copyfile(ENTRY, entry)
    shutil.copyfile(CORE, core)
    init.write_text("")
    entry.chmod(0o700)
    core.chmod(0o600)
    init.chmod(0o600)
    manifest = package / "manifest.json"
    private_json(manifest, {
        "schema_version": 1,
        "files": [
            {"path": "scripts/native-product-bridge.py", "sha256": digest(entry), "mode": 0o700},
            {"path": "lib/native_bridge/bridge.py", "sha256": digest(core), "mode": 0o600},
        ],
    })
    return package, entry, manifest


def runtime_copy(root, interpreter):
    runtime = root / "runtime" / "g0"
    runtime.mkdir(parents=True, mode=0o700)
    paths = {}
    rows = []
    for logical_id, source in RUNTIME_SOURCES.items():
        target = runtime / source.name
        shutil.copyfile(source, target)
        mode = 0o700 if logical_id == "projectproxy_launchd_entrypoint" else 0o644
        target.chmod(mode)
        paths[logical_id] = str(target)
        rows.append({"logical_id": logical_id, "path": target.name,
                     "sha256": digest(target), "mode": mode})
    manifest = runtime / "runtime-manifest.json"
    private_json(manifest, {"version": 1,
        "interpreter": {"path": interpreter, "sha256": digest(interpreter)},
        "files": rows})
    modules = {name: paths[name] for name in
        ("delivery_adapter", "delivery_audit", "owner_helper", "proxy_transport", "receipt_store")}
    return runtime, manifest, modules, paths


def send(process, value):
    process.stdin.write(json.dumps(value, sort_keys=True, separators=(",", ":")).encode() + b"\n")


async def read_json(process):
    raw = await asyncio.wait_for(process.stdout.readline(), 10)
    if not raw:
        stderr = (await process.stderr.read()).decode()
        raise AssertionError(stderr)
    return json.loads(raw)


def test_real_local_service_admission_bridges_g1_event_and_ack(tmp_path):
    async def scenario():
        with tempfile.TemporaryDirectory(prefix="g1-native-bridge-", dir="/private/tmp") as raw, ExitStack() as roots:
            root = Path(raw)
            public = root / "public.sock"
            binary = str(Path(proxy_transport._process_metadata(os.getpid())[1]))
            runtime_root, runtime_manifest, modules, runtime_paths = runtime_copy(root, binary)
            manifest, profiles, grants = policy(root, public, binary, roots)
            value = json.loads(manifest.read_text())
            backend_receipt = Path(profiles["a"]["workspace"]) / "backend-receipt.json"
            value["backend_argv"] = [binary, "-B", str(BACKEND), "{socket_path}", str(backend_receipt)]
            pinned = [BACKEND, *(Path(path) for path in runtime_paths.values())]
            value["file_pins"] = {str(path): digest(path) for path in pinned}
            value["owner_helper"] = {
                "executable": binary,
                "executable_sha256": digest(binary),
                "source_path": runtime_paths["owner_helper"],
                "source_sha256": digest(runtime_paths["owner_helper"]),
            }
            private_json(manifest, value)
            owner_script = root / "owner.py"
            _write_client(owner_script, False)
            listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            listener.bind(str(public)); listener.listen(8); public.chmod(0o600)
            service = activation_service.ActivationService.from_manifest(manifest)
            await service.start(listener)
            environment = {"PATH": "/usr/bin:/bin", "HOME": str(root), "PYTHONDONTWRITEBYTECODE": "1"}
            owner = await asyncio.create_subprocess_exec(binary, "-B", str(owner_script), str(public),
                cwd=profiles["a"]["workspace"], stdin=asyncio.subprocess.PIPE,
                stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE, env=environment)
            bridge = None
            try:
                assert await _line(owner) == "ready"
                owner_birth, owner_executable = proxy_transport._process_metadata(owner.pid)
                private_json(grants / f"{owner.pid}.json", {
                    "version": 1, "pid": owner.pid, "uid": os.getuid(), "birth": owner_birth,
                    "expected_executable": owner_executable, "executable_sha256": digest(binary),
                    "profile_id": "a",
                })
                owner.stdin.write(b"go\n"); await owner.stdin.drain()
                assert await _line(owner) == "owner-ready"
                for _ in range(100):
                    if service._owner_leases:
                        break
                    await asyncio.sleep(0.01)
                lease = next(iter(service._owner_leases.values()))
                ready_name = f"ready-{lease.lease_id}.json"
                ready_path = service.receipt_dir / ready_name
                assert ready_path.exists()

                package, entry, package_manifest = package_copy(root)
                (root / "g1-state" / "control").mkdir(parents=True, mode=0o700)
                (root / "g1-state").chmod(0o700)
                control = root / "g1-state" / "control" / "control.json"
                control.write_text("opaque-control-capability")
                control.chmod(0o600)
                captured_ack = root / "captured-ack.json"
                fake_control = root / "fake-orchestrator"
                event = {"version": 1, "event_id": "host:7", "task_id": "task-a",
                    "event_revision": 1, "work_revision": 1, "kind": "result",
                    "payload_hash": "e" * 64, "action_slot": "result-task-a-1"}
                fake_control.write_text(
                    "#!" + binary + "\nimport json,sys\n"
                    f"event={event!r}\n"
                    "if sys.argv[1]=='collect': print(json.dumps({'version':1,'status':'pending','events':[event]}))\n"
                    "elif sys.argv[1]=='ack':\n"
                    " p=sys.argv[sys.argv.index('--request')+1]; v=json.load(open(p));\n"
                    f" open({str(captured_ack)!r},'w').write(json.dumps(v,sort_keys=True));\n"
                    " print(json.dumps({'version':1,'status':'acknowledged','delivery_id':v['delivery_id']}))\n"
                )
                fake_control.chmod(0o700)
                controller_birth, controller_executable = proxy_transport._process_metadata(os.getpid())
                runtime = {"version": 1, "package_root": str(package),
                    "package_manifest": str(package_manifest), "package_manifest_sha256": digest(package_manifest),
                    "interpreter": binary, "interpreter_sha256": digest(binary),
                    "g0_runtime_root": str(runtime_root),
                    "g0_runtime_manifest": str(runtime_manifest),
                    "g0_runtime_manifest_sha256": digest(runtime_manifest),
                    "g0_modules": modules}
                async def attach_bridge():
                    child = await asyncio.create_subprocess_exec(binary, "-B", str(entry), "helper",
                        cwd=profiles["a"]["workspace"], stdin=asyncio.subprocess.PIPE,
                        stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE, env=environment)
                    boot = await read_json(child)
                    assert boot == {"event": "boot", "pid": child.pid, "version": 1}
                    helper_birth, helper_executable = proxy_transport._process_metadata(child.pid)
                    grant = {"version": 1, "role": "owner-helper", "profile_id": "a",
                        "owner_context_sha256": lease.owner_context_sha256, "lease_id": lease.lease_id,
                        "owner_connection_id": lease.owner_connection_id, "owner_epoch": lease.owner_epoch,
                        "owner_thread_id": lease.owner_thread_id, "private_socket": lease.private_socket,
                        "helper_pid": child.pid, "helper_uid": os.getuid(), "helper_birth": helper_birth,
                        "helper_executable": helper_executable, "helper_executable_sha256": digest(binary),
                        "helper_source_sha256": digest(runtime_paths["owner_helper"])}
                    grant_path = grants / f"{child.pid}.json"
                    private_json(grant_path, grant)
                    config = {"version": 1, "runtime": runtime, "manifest_path": str(manifest),
                        "manifest_sha256": digest(manifest), "grant_path": str(grant_path),
                        "grant_sha256": digest(grant_path), "profile_id": "a",
                        "public_socket": str(public),
                        "public_socket_identity": list((public.stat().st_dev, public.stat().st_ino, public.stat().st_uid, public.stat().st_mode)),
                        "controller_peer": {"pid": os.getpid(), "uid": os.getuid(), "birth": controller_birth,
                            "executable": controller_executable, "executable_sha256": digest(binary)},
                        "owner_ready_receipt": ready_name, "owner_ready_sha256": digest(ready_path),
                        "bridge_root": str(root / "bridge-ledger"),
                        "owner_capability_root": str(root / "owner-capabilities")}
                    send(child, config); await child.stdin.drain()
                    assert (await read_json(child))["event"] == "public_connected"
                    helper_ready_name = f"helper-ready-{lease.lease_id}-{child.pid}.json"
                    helper_ready_path = service.receipt_dir / helper_ready_name
                    for _ in range(100):
                        if helper_ready_path.exists():
                            break
                        await asyncio.sleep(0.01)
                    admitted = {"action": "admitted", "role": "helper", "lease_id": lease.lease_id,
                        "manifest_sha256": digest(manifest), "grant_sha256": digest(grant_path),
                        "receipt_sha256": digest(helper_ready_path), "activation_id": service.activation_id,
                        "receipt_name": helper_ready_name}
                    send(child, admitted); await child.stdin.drain()
                    attached = await read_json(child)
                    assert attached["event"] == "attached"
                    assert attached["origin_pid"] == owner.pid
                    assert attached["controller_thread"] == lease.owner_thread_id
                    return child, attached

                bootstrap_bridge, bootstrap_attachment = await attach_bridge()
                assert bootstrap_attachment["host_generation"] == "generation-00000001"
                send(bootstrap_bridge, {"action": "detach"}); await bootstrap_bridge.stdin.drain()
                detached = await read_json(bootstrap_bridge)
                assert detached == {"version": 1, "event": "detached", "lease_id": lease.lease_id}
                assert await asyncio.wait_for(bootstrap_bridge.wait(), 5) == 0

                bridge, attached = await attach_bridge()
                assert attached["host_generation"] == "generation-00000002"
                send(bridge, {"action": "deliver", "control_binary": str(fake_control),
                    "control_binary_sha256": digest(fake_control), "control_file": str(control),
                    "task_id": "task-a"})
                await bridge.stdin.drain()
                proof = await read_json(bridge)
                assert proof["event"] == "history_proof"
                assert proof["source_event_ids"] == ["host:7"]
                alias = proof["native_event_ids"][0]
                send(bridge, {"action": "decide", "delivery_id": proof["delivery_id"],
                    "history_proof_sha256": proof["history_proof_sha256"],
                    "decisions": {alias: {"decision": "handled", "command_id": "cmd-bridge-1"}}})
                await bridge.stdin.drain()
                ack = await read_json(bridge)
                assert ack["event"] == "ack" and ack["status"] == "controller_acked"
                sent_ack = json.loads(captured_ack.read_text())
                assert sent_ack["decisions"][0]["event_id"] == "host:7"
                assert sent_ack["history_proof_sha256"] == proof["history_proof_sha256"]
                owner.stdin.write(b"quit\n"); await owner.stdin.drain()
                assert await _wait_process(owner) == 0
                eof = await read_json(bridge)
                assert eof["event"] == "helper_eof"
                assert await asyncio.wait_for(bridge.wait(), 5) == 0
                status = json.loads((root / "bridge-ledger" / proof["delivery_id"] / "batch-status.json").read_text())
                assert status["state"] == "controller_acked"
            finally:
                if owner.returncode is None:
                    owner.kill(); await owner.wait()
                if bridge is not None and bridge.returncode is None:
                    bridge.kill(); await bridge.wait()
                await service.close()
                await asyncio.sleep(0.1)
                listener.close()

    asyncio.run(asyncio.wait_for(scenario(), 30))
