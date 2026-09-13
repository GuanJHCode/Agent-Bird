import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys


ROOT = Path(__file__).resolve().parents[2]
ENTRY = ROOT / "plugins" / "codex-orchestrator" / "scripts" / "native-product-bridge.py"
CORE = ROOT / "plugins" / "codex-orchestrator" / "lib" / "native_bridge" / "bridge.py"


def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def private_json(path, value):
    path.write_text(json.dumps(value, sort_keys=True) + "\n")
    path.chmod(0o600)


def test_packaged_entrypoint_requires_pinned_interpreter_package_and_g0_runtime(tmp_path):
    package = tmp_path / "package"
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
    runtime.mkdir()
    modules = {}
    for name in ("delivery_adapter", "delivery_audit", "owner_helper", "proxy_transport", "receipt_store"):
        path = runtime / f"{name}.py"
        path.write_text(f"NAME={name!r}\n")
        path.chmod(0o600)
        modules[name] = str(path)
    g0_manifest = tmp_path / "g0-manifest.json"
    private_json(g0_manifest, {
        "version": 1,
        "file_pins": {path: sha(path) for path in modules.values()},
    })
    interpreter = Path(sys.executable).resolve()
    request = tmp_path / "request.json"
    private_json(request, {
        "version": 1,
        "package_root": str(package),
        "package_manifest": str(package_manifest),
        "package_manifest_sha256": sha(package_manifest),
        "interpreter": str(interpreter),
        "interpreter_sha256": sha(interpreter),
        "g0_manifest": str(g0_manifest),
        "g0_manifest_sha256": sha(g0_manifest),
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
