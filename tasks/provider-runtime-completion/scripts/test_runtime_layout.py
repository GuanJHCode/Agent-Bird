"""Packaged production runtime must run without the repository's task tree."""
import ast
import shutil
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
MODULES = ("projectproxy_launchd_entrypoint", "launch_activation", "activation_service", "proxy_transport", "proxy_observer", "owned_child_guard", "owner_helper", "auth_isolation", "delivery_adapter", "delivery_audit", "receipt_store")


def test_formal_runtime_is_self_contained(tmp_path):
    source = ROOT / "runtime/native"
    assert source.is_dir(), "production runtime still lives in tasks"
    target = tmp_path / "runtime"
    shutil.copytree(source, target)
    for name in MODULES:
        path = target / f"{name}.py"
        assert path.is_file()
        ast.parse(path.read_text())
    code = "import sys; sys.path.insert(0, sys.argv[1]); " + "; ".join(f"import {name}" for name in MODULES)
    result = subprocess.run(["python3", "-I", "-B", "-c", code, str(target)], capture_output=True, text=True)
    assert result.returncode == 0, result.stderr


def test_packager_uses_formal_runtime_sources():
    source = (ROOT / "tasks/g1-g4-delivery/scripts/package-plugin.sh").read_text()
    copies = [line for line in source.splitlines() if line.startswith("copy_runtime ")]
    assert len(copies) == len(MODULES)
    assert all('"$repo_root/runtime/native/' in line for line in copies)
