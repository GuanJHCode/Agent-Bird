"""Freeze and verify the inputs for one root-run private native product case."""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys


AUTH_INVENTORY = Path(
    "/private/tmp/g0-next-delivery-auth-inventory/credential-paths.txt"
)
AUTH_INVENTORY_SHA256 = (
    "78a5f826242bc439387a8972bddf587383dc7f45ff4dac19118b7a47cbc996a5"
)
NATIVE = Path(
    "/Users/guanjunhui/.codex/packages/standalone/releases/0.154.0-aarch64-apple-darwin/bin/codex"
)
NATIVE_SHA256 = "4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc"
PYTHON = Path(
    "/opt/homebrew/Cellar/python@3.13/3.13.5/Frameworks/Python.framework/Versions/3.13/Resources/Python.app/Contents/MacOS/Python"
)
PYTHON_SHA256 = "8623f5d3e96ae0279fa09f54ea4b355cdc0daf7da285057311f14c7de8c677a0"
RUNTIME_LOGICAL_IDS = {
    "projectproxy_launchd_entrypoint",
    "launch_activation",
    "activation_service",
    "proxy_transport",
    "proxy_observer",
    "owned_child_guard",
    "owner_helper",
    "auth_isolation",
    "delivery_adapter",
    "delivery_audit",
    "receipt_store",
}


class PlanError(ValueError):
    pass


def require(condition, code):
    if not condition:
        raise PlanError(code)


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":")).encode()


def sha(path):
    path = Path(path)
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        before = os.fstat(fd)
        require(
            stat.S_ISREG(before.st_mode)
            and before.st_nlink == 1
            and before.st_uid in (0, os.getuid())
            and 0 < before.st_size <= 512 * 1024 * 1024,
            "file_identity",
        )
        digest = hashlib.sha256()
        while block := os.read(fd, 1024 * 1024):
            digest.update(block)
        after = os.fstat(fd)
        require(
            (
                before.st_dev,
                before.st_ino,
                before.st_uid,
                before.st_mode,
                before.st_nlink,
                before.st_size,
                before.st_mtime_ns,
                before.st_ctime_ns,
            )
            == (
                after.st_dev,
                after.st_ino,
                after.st_uid,
                after.st_mode,
                after.st_nlink,
                after.st_size,
                after.st_mtime_ns,
                after.st_ctime_ns,
            ),
            "file_changed",
        )
        return digest.hexdigest()
    finally:
        os.close(fd)


def private_json(path):
    path = Path(path)
    info = path.lstat()
    require(
        stat.S_ISREG(info.st_mode)
        and info.st_uid == os.getuid()
        and stat.S_IMODE(info.st_mode) == 0o600
        and info.st_nlink == 1,
        "private_json",
    )
    try:
        value = json.loads(path.read_bytes())
    except (UnicodeDecodeError, ValueError) as exc:
        raise PlanError("invalid_json") from exc
    require(isinstance(value, dict), "invalid_json")
    return value


def write_exclusive(path, value):
    path = Path(path)
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    try:
        data = canonical(value) + b"\n"
        os.write(fd, data)
        os.fsync(fd)
    finally:
        os.close(fd)


def private_dir(path):
    path = Path(path)
    require(path.is_absolute() and path.resolve() == path, "directory_path")
    info = path.lstat()
    require(
        stat.S_ISDIR(info.st_mode)
        and not stat.S_ISLNK(info.st_mode)
        and info.st_uid == os.getuid()
        and stat.S_IMODE(info.st_mode) == 0o700,
        "directory_identity",
    )
    return path


def runner_process_identity(repo):
    scripts = Path(repo) / "tasks/g0-completion/scripts"
    sys.path.insert(0, str(scripts))
    try:
        import native_delivery_case as base
    finally:
        sys.path.remove(str(scripts))
    require(
        Path(base.__file__).resolve() == scripts / "native_delivery_case.py",
        "runner_interpreter",
    )
    try:
        return base.peer_for_process(os.getpid())
    except (OSError, ValueError) as exc:
        raise PlanError("runner_interpreter") from exc


def source_paths(repo):
    relative = (
        "tasks/g1-g4-delivery/scripts/run-native-product-acceptance.py",
        "tasks/g1-g4-delivery/scripts/native_product_plan.py",
        "tasks/g1-g4-delivery/scripts/native_product_case.py",
        "tasks/g1-g4-delivery/scripts/native_resource_sampler.py",
        "tasks/g1-g4-delivery/scripts/native_tui_case.py",
        "tasks/g1-g4-delivery/scripts/native_tui_session.py",
        "tasks/g0-global-delivery-validation/scripts/global_delivery_case.py",
        "tasks/g0-global-delivery-validation/scripts/inherited_fd_scheduler.py",
        "tasks/g0-global-delivery-validation/scripts/receipt_store.py",
        "tasks/g0-completion/scripts/synthetic_responses.py",
        "tasks/g0-completion/scripts/synthetic_native_fixture.py",
        "tasks/g0-completion/scripts/synthetic_native_transport.py",
        "tasks/g0-completion/scripts/native_delivery_case.py",
        "tasks/g0-completion/scripts/native_delivery_client.py",
        "tasks/g0-completion/scripts/delivery_adapter.py",
        "tasks/g0-completion/scripts/owner_helper.py",
        "tasks/g0-pending-resolution/scripts/delivery_audit.py",
        "tasks/g0-completion/data/source/codex-rs/app-server-protocol/src/protocol/common.rs",
        "tasks/g0-auth-preserving-activation/scripts/auth_isolation.py",
        "tasks/g0-auth-preserving-activation/scripts/activation_transaction.py",
        "tasks/g0-auth-preserving-activation/scripts/native_activation_fixture.py",
        "tasks/g0-auth-preserving-activation/scripts/native_activation_probe.py",
        "tasks/g0-tui-proxy/scripts/proxy_native_runtime.py",
        "tasks/g0-tui-proxy/scripts/proxy_transport.py",
        "tasks/g0-tui-proxy/scripts/proxy_observer.py",
        "tasks/g0-tui-proxy/scripts/owned_child_guard.py",
        "tasks/g0-proxy-continuation/cold-start/scripts/activation_service.py",
        "tasks/g0-proxy-continuation/cold-start/scripts/projectproxy_launchd_entrypoint.py",
        "tasks/g0-proxy-continuation/cold-start/scripts/launch_activation.py",
        "tasks/g0-proxy-continuation/cold-start/scripts/projectproxy_launchd_entrypoint.py",
        "tasks/g0-proxy-continuation/cold-start/scripts/launch_activation.py",
        "tasks/g1-g4-delivery/scripts/native_tui_case.py",
    )
    go_sources = tuple(sorted((repo / "tools/g0-return-lab").glob("*.go")))
    return (
        tuple(repo / item for item in relative)
        + go_sources
        + (repo / "tools/g0-return-lab/go.mod", Path(sys.executable).resolve())
    )


def validate_package(
    package_root, *, interpreter=PYTHON, interpreter_sha256=PYTHON_SHA256
):
    package_root = private_dir(package_root)
    manifest_path = package_root / "manifest.json"
    manifest = private_json(manifest_path)
    require(
        manifest.get("schema_version") == 1
        and manifest.get("package_name") == "codex-orchestrator"
        and isinstance(manifest.get("files"), list),
        "package_manifest",
    )
    rows = {}
    for row in manifest["files"]:
        require(
            isinstance(row, dict)
            and isinstance(row.get("path"), str)
            and re.fullmatch(r"[A-Za-z0-9._/-]{1,240}", row["path"])
            and not row["path"].startswith("/")
            and ".." not in Path(row["path"]).parts
            and row["path"] not in rows
            and re.fullmatch(r"[0-9a-f]{64}", str(row.get("sha256"))),
            "package_manifest",
        )
        target = package_root / row["path"]
        info = target.lstat()
        require(
            stat.S_ISREG(info.st_mode)
            and info.st_uid == os.getuid()
            and stat.S_IMODE(info.st_mode) == row.get("mode")
            and sha(target) == row["sha256"],
            "package_file_changed",
        )
        rows[row["path"]] = row
    binary = package_root / "bin" / "codex-orchestrator"
    require(
        manifest.get("binary_path") == "bin/codex-orchestrator"
        and sha(binary) == manifest.get("binary_sha256")
        and stat.S_IMODE(binary.lstat().st_mode) == 0o700,
        "package_binary",
    )
    runtime_root = private_dir(package_root / "runtime" / "g0")
    runtime_manifest_path = runtime_root / "runtime-manifest.json"
    runtime = private_json(runtime_manifest_path)
    require(
        runtime.get("version") == 1
        and runtime.get("interpreter")
        == {"path": str(interpreter), "sha256": interpreter_sha256}
        and isinstance(runtime.get("files"), list),
        "runtime_manifest",
    )
    runtime_rows = {}
    for row in runtime["files"]:
        require(
            isinstance(row, dict)
            and set(row) == {"logical_id", "path", "sha256", "mode"}
            and row["logical_id"] in RUNTIME_LOGICAL_IDS
            and row["logical_id"] not in runtime_rows
            and Path(row["path"]).name == row["path"],
            "runtime_manifest",
        )
        target = runtime_root / row["path"]
        info = target.lstat()
        require(
            stat.S_ISREG(info.st_mode)
            and info.st_uid == os.getuid()
            and stat.S_IMODE(info.st_mode) == row["mode"]
            and sha(target) == row["sha256"],
            "runtime_file_changed",
        )
        packaged = rows.get("runtime/g0/" + row["path"])
        require(
            packaged is not None
            and packaged["sha256"] == row["sha256"]
            and packaged["mode"] == row["mode"],
            "runtime_package_binding",
        )
        runtime_rows[row["logical_id"]] = {**row, "absolute_path": str(target)}
    require(set(runtime_rows) == RUNTIME_LOGICAL_IDS, "runtime_manifest")
    return {
        "package_root": str(package_root),
        "package_manifest": str(manifest_path),
        "package_manifest_sha256": sha(manifest_path),
        "binary": str(binary),
        "binary_sha256": sha(binary),
        "runtime_root": str(runtime_root),
        "runtime_manifest": str(runtime_manifest_path),
        "runtime_manifest_sha256": sha(runtime_manifest_path),
        "runtime_files": runtime_rows,
        "interpreter": str(interpreter),
        "interpreter_sha256": interpreter_sha256,
    }


def static_inputs(
    repo,
    package_root,
    *,
    interpreter=PYTHON,
    interpreter_sha256=PYTHON_SHA256,
    native=NATIVE,
    native_sha256=NATIVE_SHA256,
    inventory=AUTH_INVENTORY,
    inventory_sha256=AUTH_INVENTORY_SHA256,
):
    repo = Path(repo).resolve()
    require(repo.is_absolute(), "repo")
    runner = runner_process_identity(repo)
    require(
        runner.get("pid") == os.getpid()
        and runner.get("uid") == os.getuid()
        and runner.get("executable") == str(interpreter)
        and runner.get("executable_sha256") == interpreter_sha256,
        "runner_interpreter",
    )
    require(
        sha(interpreter) == interpreter_sha256 and sha(native) == native_sha256,
        "fixed_executable_changed",
    )
    require(
        sha(inventory) == inventory_sha256
        and len([line for line in inventory.read_text().splitlines() if line]) == 14,
        "auth_inventory_changed",
    )
    paths = source_paths(repo)
    require(all(path.is_file() for path in paths), "source_missing")
    package = validate_package(
        package_root, interpreter=interpreter, interpreter_sha256=interpreter_sha256
    )
    commit = subprocess.check_output(
        ["git", "rev-parse", "HEAD"], cwd=repo, text=True, timeout=5
    ).strip()
    return {
        "version": 1,
        "source_commit": commit,
        "source_pins": {str(path): sha(path) for path in paths},
        "package": package,
        "native": {"path": str(native), "sha256": native_sha256},
        "auth_inventory": {
            "path": str(inventory),
            "sha256": inventory_sha256,
            "path_count": 14,
        },
        "real_launchctl_used": False,
        "external_model_calls": 0,
    }


def prepare_plan(repo, case_id, package_root, *, export_root=None, **overrides):
    repo = Path(repo).resolve()
    require(re.fullmatch(r"native-product-[0-9]{2}", case_id) is not None, "case_id")
    export_root = (
        Path(export_root)
        if export_root is not None
        else repo / "tasks/g1-g4-delivery/data/cases"
    )
    export_root.mkdir(mode=0o700, parents=True, exist_ok=True)
    export = export_root / case_id
    export.mkdir(mode=0o700)
    plan = {
        **static_inputs(repo, package_root, **overrides),
        "case_id": case_id,
        "export_dir": str(export),
        "profile_prefix": "g0-auth-product-",
        "supervisor_prefix": "g0-product-s-",
        "automatic_retry": False,
        "run_deadline_seconds": 150,
    }
    path = export / "private-product-plan.json"
    write_exclusive(path, plan)
    return path, sha(path), plan


def verify_plan(path, expected_sha256):
    path = Path(path)
    require(
        path.is_absolute()
        and re.fullmatch(r"[0-9a-f]{64}", expected_sha256)
        and sha(path) == expected_sha256,
        "plan_changed",
    )
    plan = private_json(path)
    require(
        plan.get("version") == 1
        and plan.get("case_id") == path.parent.name
        and plan.get("export_dir") == str(path.parent)
        and plan.get("real_launchctl_used") is False
        and plan.get("external_model_calls") == 0
        and plan.get("automatic_retry") is False,
        "plan_shape",
    )
    private_dir(path.parent)
    repo = Path(__file__).resolve().parents[3]
    require(
        plan.get("source_pins")
        == {str(item): sha(item) for item in source_paths(repo)},
        "source_changed",
    )
    require(
        sha(plan["native"]["path"]) == plan["native"]["sha256"] == NATIVE_SHA256,
        "native_changed",
    )
    inventory = plan["auth_inventory"]
    require(
        sha(inventory["path"]) == inventory["sha256"] == AUTH_INVENTORY_SHA256
        and inventory["path_count"] == 14,
        "auth_inventory_changed",
    )
    package = validate_package(
        plan["package"]["package_root"],
        interpreter=Path(plan["package"]["interpreter"]),
        interpreter_sha256=plan["package"]["interpreter_sha256"],
    )
    require(package == plan["package"], "package_changed")
    return plan
