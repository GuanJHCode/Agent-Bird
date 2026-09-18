import asyncio
from contextlib import ExitStack
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import sys
import tempfile

import pytest


ROOT = Path(__file__).resolve().parents[2]
COLD = ROOT / "tasks" / "g0-proxy-continuation" / "cold-start"
for directory in (
    COLD / "scripts",
    COLD / "tests",
    ROOT / "tasks/g0-tui-proxy/scripts",
    ROOT / "tasks/g0-auth-preserving-activation/scripts",
    ROOT / "tasks/g0-completion/tests",
):
    sys.path.insert(0, str(directory))

import activation_service  # noqa: E402
import proxy_transport  # noqa: E402
from test_activation_service import digest, policy, private_json  # noqa: E402
from test_owner_helper_production_pipeline import _line, _wait_process, _write_client  # noqa: E402
from test_local_service_chain import package_copy, runtime_copy  # noqa: E402


PRODUCT_BINARY = os.getenv("ORCHESTRATOR_PRODUCT_BINARY", "")
pytestmark = pytest.mark.skipif(
    not PRODUCT_BINARY, reason="requires integrated final G1 binary"
)
BACKEND = ROOT / "tasks/g0-completion/tests/fixtures/owner_helper_tool_backend.py"


async def cli(binary, *args):
    process = await asyncio.create_subprocess_exec(
        binary,
        *map(str, args),
        stdin=asyncio.subprocess.DEVNULL,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
        env={
            "PATH": "/usr/bin:/bin:/usr/sbin:/sbin",
            "LANG": "C",
            "LC_ALL": "C",
            "ORCHESTRATOR_ENABLE_TEST_FAKE": "1",
            "PYTHONDONTWRITEBYTECODE": "1",
        },
    )
    stdout, stderr = await asyncio.wait_for(process.communicate(), 12)
    detail = stderr.decode()
    request = Path(str(args[-1])) if args else None
    if process.returncode != 0 and request is not None:
        log = request.parent / "driver.log"
        if log.exists():
            detail += "\n" + log.read_text()
    assert process.returncode == 0, detail
    return json.loads(stdout)


def install_binary(package, manifest, binary):
    target = package / "bin/codex-orchestrator"
    target.parent.mkdir(mode=0o700)
    shutil.copyfile(binary, target)
    target.chmod(0o700)
    value = json.loads(manifest.read_text())
    value.update(
        package_name="codex-orchestrator",
        version="0.1.0",
        binary_path="bin/codex-orchestrator",
        binary_sha256=digest(target),
    )
    value["files"].append(
        {"path": "bin/codex-orchestrator", "sha256": digest(target), "mode": 0o700}
    )
    private_json(manifest, value)
    return target


def send_owner(process, value):
    process.stdin.write(value)


def test_owner_bound_driver_waits_then_delivers_without_manual_collect():
    async def scenario():
        with (
            tempfile.TemporaryDirectory(
                prefix="native-driver-", dir="/private/tmp"
            ) as raw,
            ExitStack() as roots,
        ):
            root = Path(raw)
            public = root / "public.sock"
            interpreter = str(Path(proxy_transport._process_metadata(os.getpid())[1]))
            package, _, package_manifest = package_copy(root)
            product = install_binary(package, package_manifest, PRODUCT_BINARY)
            runtime_root, runtime_manifest, modules, runtime_paths = runtime_copy(
                package, interpreter
            )
            manifest, profiles, grants = policy(root, public, interpreter, roots)
            value = json.loads(manifest.read_text())
            backend_receipt = Path(profiles["a"]["workspace"]) / "backend-receipt.json"
            value["backend_argv"] = [
                interpreter,
                "-B",
                str(BACKEND),
                "{socket_path}",
                str(backend_receipt),
            ]
            value["file_pins"] = {
                str(path): digest(path)
                for path in [BACKEND, *(Path(item) for item in runtime_paths.values())]
            }
            value["owner_helper"] = {
                "executable": interpreter,
                "executable_sha256": digest(interpreter),
                "source_path": runtime_paths["owner_helper"],
                "source_sha256": digest(runtime_paths["owner_helper"]),
            }
            private_json(manifest, value)
            owner_script = root / "owner.py"
            _write_client(owner_script, False)
            listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            listener.bind(str(public))
            listener.listen(8)
            public.chmod(0o600)
            service = activation_service.ActivationService.from_manifest(manifest)
            await service.start(listener)
            owner = await asyncio.create_subprocess_exec(
                interpreter,
                "-B",
                str(owner_script),
                str(public),
                cwd=profiles["a"]["workspace"],
                stdin=asyncio.subprocess.PIPE,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
                env={
                    "PATH": "/usr/bin:/bin",
                    "HOME": str(root),
                    "PYTHONDONTWRITEBYTECODE": "1",
                },
            )
            try:
                assert await _line(owner) == "ready"
                owner_birth, owner_executable = proxy_transport._process_metadata(
                    owner.pid
                )
                private_json(
                    grants / f"{owner.pid}.json",
                    {
                        "version": 1,
                        "pid": owner.pid,
                        "uid": os.getuid(),
                        "birth": owner_birth,
                        "expected_executable": owner_executable,
                        "executable_sha256": digest(interpreter),
                        "profile_id": "a",
                    },
                )
                send_owner(owner, b"go\n")
                await owner.stdin.drain()
                assert await _line(owner) == "owner-ready"
                for _ in range(100):
                    if service._owner_leases:
                        break
                    await asyncio.sleep(0.01)
                lease = next(iter(service._owner_leases.values()))
                ready_name = f"ready-{lease.lease_id}.json"
                ready_path = service.receipt_dir / ready_name
                for name in ("driver", "bridge", "capabilities", "g1"):
                    (root / name).mkdir(mode=0o700)
                template = root / "submit-template.json"
                private_json(
                    template,
                    {
                        "version": 1,
                        "run_id": "driver-run",
                        "plan_revision": 1,
                        "tasks": [
                            {
                                "id": "driver-task",
                                "max_attempts": 1,
                                "work_revision": 1,
                                "completion_policy": "exit_success_fixture",
                                "adapter": {
                                    "kind": "fake",
                                    "args": ["/bin/sh", "-c", "exit 0"],
                                    "directory": profiles["a"]["workspace"],
                                },
                            }
                        ],
                    },
                )
                request = root / "driver/request.json"
                private_json(
                    request,
                    {
                        "version": 1,
                        "runtime": {
                            "version": 1,
                            "package_root": str(package),
                            "package_manifest": str(package_manifest),
                            "package_manifest_sha256": digest(package_manifest),
                            "interpreter": interpreter,
                            "interpreter_sha256": digest(interpreter),
                            "g0_runtime_root": str(runtime_root),
                            "g0_runtime_manifest": str(runtime_manifest),
                            "g0_runtime_manifest_sha256": digest(runtime_manifest),
                            "g0_modules": modules,
                        },
                        "driver_root": str(root / "driver"),
                        "manifest_path": str(manifest),
                        "manifest_sha256": digest(manifest),
                        "activation_id": service.activation_id,
                        "owner_ready_receipt": ready_name,
                        "owner_ready_sha256": digest(ready_path),
                        "profile_id": "a",
                        "public_socket": str(public),
                        "bridge_root": str(root / "bridge"),
                        "owner_capability_root": str(root / "capabilities"),
                        "g1_state": str(root / "g1"),
                        "mode": "submit",
                        "submit_request": str(template),
                        "control_file": "",
                        "task_id": "driver-task",
                        "bootstrap_timeout_seconds": 300,
                        "watch_policy": "until_terminal_or_owner_detached",
                        "enable_test_fake": True,
                    },
                )
                started = await cli(
                    str(product), "native-bridge", "start", "--request", request
                )
                assert started["status"] in {"watching", "awaiting_owner_decision"}
                status_request = root / "driver-status.json"
                private_json(
                    status_request, {"version": 1, "driver_root": str(root / "driver")}
                )
                for _ in range(200):
                    status = await cli(
                        str(product),
                        "native-bridge",
                        "status",
                        "--request",
                        status_request,
                    )
                    if status["status"] == "awaiting_owner_decision":
                        break
                    await asyncio.sleep(0.02)
                assert status["status"] == "awaiting_owner_decision"
                history = Path(status["history_receipt"])
                history_value = json.loads(history.read_text())
                decision = root / "decision.json"
                private_json(
                    decision,
                    {
                        "version": 1,
                        "driver_root": str(root / "driver"),
                        "delivery_id": history_value["delivery_id"],
                        "history_receipt": str(history),
                        "history_receipt_sha256": digest(history),
                        "history_proof_sha256": history_value["history_proof_sha256"],
                        "decisions": {
                            history_value["native_event_ids"][0]: {
                                "decision": "handled",
                                "command_id": "local-driver-handled",
                            }
                        },
                    },
                )
                assert (
                    await cli(
                        str(product), "native-bridge", "decide", "--request", decision
                    )
                )["status"] == "decision_recorded"
                for _ in range(200):
                    status = await cli(
                        str(product),
                        "native-bridge",
                        "status",
                        "--request",
                        status_request,
                    )
                    if status["status"] == "completed":
                        break
                    await asyncio.sleep(0.02)
                assert status["status"] == "completed"
                ledger = (
                    root / "bridge" / history_value["delivery_id"] / "batch-status.json"
                )
                assert json.loads(ledger.read_text())["state"] == "controller_acked"
                send_owner(owner, b"quit\n")
                await owner.stdin.drain()
                assert await _wait_process(owner) == 0
            finally:
                if owner.returncode is None:
                    owner.kill()
                    await owner.wait()
                coordinator = root / "g1/coordinator.pid"
                if coordinator.exists():
                    try:
                        os.kill(int(coordinator.read_text()), signal.SIGTERM)
                    except ProcessLookupError:
                        pass
                await service.close()
                await asyncio.sleep(0.1)
                listener.close()

    asyncio.run(asyncio.wait_for(scenario(), 35))
