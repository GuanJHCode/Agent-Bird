import asyncio
import ctypes
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
from types import SimpleNamespace

import pytest


ROOT = Path(__file__).resolve().parents[2]
ENTRY = ROOT / "plugins" / "codex-orchestrator" / "scripts" / "native-product-bridge.py"
CORE = ROOT / "plugins" / "codex-orchestrator" / "lib" / "native_bridge" / "bridge.py"


def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def private_json(path, value):
    path.write_text(json.dumps(value, sort_keys=True) + "\n")
    path.chmod(0o600)


def kernel_executable():
    libproc = ctypes.CDLL("/usr/lib/libproc.dylib")
    libproc.proc_pidpath.argtypes = [ctypes.c_int, ctypes.c_void_p, ctypes.c_uint32]
    libproc.proc_pidpath.restype = ctypes.c_int
    buffer = ctypes.create_string_buffer(4096)
    assert libproc.proc_pidpath(os.getpid(), buffer, len(buffer)) > 0
    return Path(os.fsdecode(buffer.value))


def test_packaged_entrypoint_requires_pinned_interpreter_package_and_g0_runtime(tmp_path):
    package = tmp_path / "package"
    package.mkdir(mode=0o700)
    (package / "scripts").mkdir(parents=True)
    (package / "lib" / "native_bridge").mkdir(parents=True)
    entry = package / "scripts" / ENTRY.name
    core = package / "lib" / "native_bridge" / "bridge.py"
    shutil.copyfile(ENTRY, entry)
    shutil.copyfile(CORE, core)
    entry.chmod(0o700)
    core.chmod(0o600)
    package_manifest = package / "manifest.json"
    private_json(package_manifest, {
        "schema_version": 1,
        "files": [
            {"path": "scripts/native-product-bridge.py", "sha256": sha(entry), "mode": 0o700},
            {"path": "lib/native_bridge/bridge.py", "sha256": sha(core), "mode": 0o600},
        ],
    })
    runtime = tmp_path / "g0-runtime"
    runtime.mkdir(mode=0o700)
    modules = {}
    for name in ("delivery_adapter", "delivery_audit", "owner_helper", "proxy_transport", "receipt_store"):
        path = runtime / f"{name}.py"
        path.write_text(f"NAME={name!r}\n")
        path.chmod(0o600)
        modules[name] = str(path)
    interpreter = kernel_executable()
    g0_manifest = runtime / "runtime-manifest.json"
    private_json(g0_manifest, {
        "version": 1,
        "interpreter": {"path": str(interpreter), "sha256": sha(interpreter)},
        "files": [{"logical_id": name, "path": Path(path).name,
                   "sha256": sha(path), "mode": Path(path).stat().st_mode & 0o777}
                  for name, path in modules.items()],
    })
    request = tmp_path / "request.json"
    private_json(request, {
        "version": 1,
        "package_root": str(package),
        "package_manifest": str(package_manifest),
        "package_manifest_sha256": sha(package_manifest),
        "interpreter": str(interpreter),
        "interpreter_sha256": sha(interpreter),
        "g0_runtime_root": str(runtime),
        "g0_runtime_manifest": str(g0_manifest),
        "g0_runtime_manifest_sha256": sha(g0_manifest),
        "g0_modules": modules,
    })
    completed = subprocess.run([str(interpreter), "-B", str(entry), "self-check", "--request", str(request)], capture_output=True, text=True)
    assert completed.returncode == 0, completed.stderr
    assert json.loads(completed.stdout) == {"status": "ready", "version": 1}

    value = json.loads(request.read_text())
    value["interpreter_sha256"] = "0" * 64
    private_json(request.with_name("bad.json"), value)
    denied = subprocess.run([str(interpreter), "-B", str(entry), "self-check", "--request", str(request.with_name("bad.json"))], capture_output=True, text=True)
    assert denied.returncode == 2
    assert json.loads(denied.stderr)["error"] == "interpreter_changed"


def test_digest_streams_file_through_bounded_reads(tmp_path, monkeypatch):
    spec = importlib.util.spec_from_file_location("native_product_bridge_entry", ENTRY)
    entry = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(entry)
    artifact = tmp_path / "large-artifact"
    payload = b"bridge-digest-block\n" * 160_000
    artifact.write_bytes(payload)
    original_read = entry.os.read
    requested_sizes = []

    def bounded_read(fd, size):
        requested_sizes.append(size)
        assert size <= 1024 * 1024
        return original_read(fd, size)

    monkeypatch.setattr(entry.os, "read", bounded_read)
    assert entry.digest(artifact) == hashlib.sha256(payload).hexdigest()
    assert requested_sizes[-1] == 1
    assert len(requested_sizes) >= 4


def test_actionable_watch_survives_bootstrap_window_and_stops_on_owner_exit(monkeypatch):
    spec = importlib.util.spec_from_file_location("native_product_bridge_entry", ENTRY)
    entry = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(entry)
    clock = [0.0]
    owner_live = [True]

    class Client:
        calls = 0

        def wait_events(self, task_id, *, control_file, cursor, timeout_ms):
            assert task_id == "task-a"
            assert control_file == Path("/private/tmp/control.json")
            assert cursor == "cursor-a"
            assert timeout_ms == 30_000
            self.calls += 1
            clock[0] += 301.0
            if self.calls == 1:
                return "timeout", [], ""
            return "events", [{"event_id": "event-a"}], "cursor-b"

    monkeypatch.setattr(entry.time, "monotonic", lambda: clock[0])
    monkeypatch.setattr(entry, "attachment_live", lambda *_: owner_live[0])
    client = Client()
    result = entry.wait_actionable_events(
        client,
        "task-a",
        Path("/private/tmp/control.json"),
        "cursor-a",
        object(),
        object(),
    )
    assert clock[0] > 300
    assert result == ("events", [{"event_id": "event-a"}], "cursor-b")

    class DetachingClient(Client):
        def wait_events(self, *args, **kwargs):
            owner_live[0] = False
            return "timeout", [], ""

    owner_live[0] = True
    with pytest.raises(entry.EntryError, match="^owner_detached$"):
        entry.wait_actionable_events(
            DetachingClient(),
            "task-a",
            Path("/private/tmp/control.json"),
            "cursor-a",
            object(),
            object(),
        )


def test_decision_wait_survives_bootstrap_window_and_stops_on_owner_exit(tmp_path, monkeypatch):
    spec = importlib.util.spec_from_file_location("native_product_bridge_entry", ENTRY)
    entry = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(entry)
    decision = tmp_path / "decision.json"
    clock = [0.0]
    owner_live = [True]

    async def advance(_seconds):
        clock[0] += 301.0
        private_json(decision, {"version": 1, "action": "decide"})

    monkeypatch.setattr(entry.time, "monotonic", lambda: clock[0])
    monkeypatch.setattr(entry.asyncio, "sleep", advance)
    monkeypatch.setattr(entry, "attachment_live", lambda *_: owner_live[0])
    assert asyncio.run(entry.wait_decision(decision, object(), object())) == {
        "version": 1,
        "action": "decide",
    }
    assert clock[0] > 300

    decision.unlink()
    owner_live[0] = False
    with pytest.raises(entry.EntryError, match="^owner_detached$"):
        asyncio.run(entry.wait_decision(decision, object(), object()))


def test_exact_history_waits_for_completed_delivery_turn():
    spec = importlib.util.spec_from_file_location("native_product_bridge_entry", ENTRY)
    entry = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(entry)
    envelope = {
        "version": 1,
        "delivery_id": "d_" + "a" * 40,
        "controller_thread_id": "thread-a",
        "controller_epoch": 3,
        "events": [],
        "payload_hash": "b" * 64,
    }

    class Connection:
        reads = 0

        async def send_rpc(self, _request):
            return None

        async def read_rpc(self):
            self.reads += 1
            status = "inProgress" if self.reads == 1 else "completed"
            return {
                "id": 19 + self.reads,
                "result": {
                    "thread": {
                        "id": "thread-a",
                        "turns": [
                            {
                                "id": "turn-a",
                                "status": status,
                                "items": [
                                    {
                                        "id": "item-a",
                                        "type": "functionCallOutput",
                                        "name": "g0_delivery",
                                        "namespace": "orchestration",
                                        "output": json.dumps(envelope),
                                    }
                                ],
                            }
                        ],
                    }
                },
            }

    class Adapter:
        @staticmethod
        def audit_history(_capability, _reads):
            return SimpleNamespace(reads_sha256="c" * 64, summary={})

    connection = Connection()
    proof, marker = asyncio.run(
        entry.exact_history(
            connection,
            Adapter(),
            SimpleNamespace(native_envelope=envelope),
            "thread-a",
        )
    )
    assert connection.reads == 2
    assert proof.reads_sha256 == "c" * 64
    assert marker == ("turn-a", "item-a")
