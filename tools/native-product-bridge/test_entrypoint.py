import ctypes
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess


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
