import hashlib
import importlib.util
import json
from pathlib import Path

import pytest


ROOT = Path(__file__).resolve().parents[2]
MODULE = ROOT / "tasks/g1-g4-delivery/scripts/native_product_plan.py"
spec = importlib.util.spec_from_file_location("native_product_plan", MODULE)
plan = importlib.util.module_from_spec(spec)
spec.loader.exec_module(plan)


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def private_json(path, value):
    Path(path).write_text(json.dumps(value, sort_keys=True) + "\n")
    Path(path).chmod(0o600)


def package(root, interpreter):
    root.mkdir(mode=0o700)
    (root / "bin").mkdir(mode=0o700)
    runtime = root / "runtime" / "g0"
    runtime.mkdir(parents=True, mode=0o700)
    binary = root / "bin" / "codex-orchestrator"
    binary.write_text("product-binary")
    binary.chmod(0o700)
    runtime_rows = []
    package_rows = [
        {"path": "bin/codex-orchestrator", "sha256": digest(binary), "mode": 0o700}
    ]
    for logical_id in sorted(plan.RUNTIME_LOGICAL_IDS):
        path = runtime / f"{logical_id}.py"
        path.write_text(logical_id)
        mode = 0o700 if logical_id == "projectproxy_launchd_entrypoint" else 0o644
        path.chmod(mode)
        row = {
            "logical_id": logical_id,
            "path": path.name,
            "sha256": digest(path),
            "mode": mode,
        }
        runtime_rows.append(row)
        package_rows.append(
            {"path": "runtime/g0/" + path.name, "sha256": row["sha256"], "mode": mode}
        )
    runtime_manifest = runtime / "runtime-manifest.json"
    private_json(
        runtime_manifest,
        {
            "version": 1,
            "interpreter": {"path": str(interpreter), "sha256": digest(interpreter)},
            "files": runtime_rows,
        },
    )
    package_rows.append(
        {
            "path": "runtime/g0/runtime-manifest.json",
            "sha256": digest(runtime_manifest),
            "mode": 0o600,
        }
    )
    manifest = root / "manifest.json"
    private_json(
        manifest,
        {
            "schema_version": 1,
            "package_name": "codex-orchestrator",
            "version": "0.1.0",
            "binary_path": "bin/codex-orchestrator",
            "binary_sha256": digest(binary),
            "files": package_rows,
        },
    )
    return root, runtime


def test_distribution_plan_accepts_only_manifest_bound_runtime(tmp_path):
    interpreter = tmp_path / "python"
    interpreter.write_text("pinned-python")
    interpreter.chmod(0o700)
    package_root, runtime = package(tmp_path / "installed", interpreter)
    value = plan.validate_package(
        package_root, interpreter=interpreter, interpreter_sha256=digest(interpreter)
    )
    assert value["binary"] == str(package_root / "bin/codex-orchestrator")
    assert set(value["runtime_files"]) == plan.RUNTIME_LOGICAL_IDS
    assert value["runtime_root"] == str(runtime)

    changed = runtime / "delivery_adapter.py"
    changed.write_text("changed")
    with pytest.raises(
        plan.PlanError, match="runtime_file_changed|package_file_changed"
    ):
        plan.validate_package(
            package_root,
            interpreter=interpreter,
            interpreter_sha256=digest(interpreter),
        )


def test_native_product_run_module_compiles_without_starting_native():
    for relative in (
        "tasks/g1-g4-delivery/scripts/native_product_case.py",
        "tasks/g1-g4-delivery/scripts/run-native-product-acceptance.py",
    ):
        source = (ROOT / relative).read_text()
        compile(source, str(ROOT / relative), "exec")


def test_private_plan_pins_native_resource_sampler():
    sampler = ROOT / "tasks/g1-g4-delivery/scripts/native_resource_sampler.py"
    assert sampler in plan.source_paths(ROOT)
