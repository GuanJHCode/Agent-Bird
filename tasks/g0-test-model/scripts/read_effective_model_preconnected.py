#!/usr/bin/env python3
"""Prepare/run one zero-model effective-config read over a private UDS."""
import argparse
import asyncio
import ctypes
import datetime as dt
import hashlib
import json
import logging
import os
from pathlib import Path
import signal
import socket
import stat
import subprocess
import time

from websockets.asyncio.client import unix_connect

TASK_ROOT = Path(__file__).resolve().parents[1]
_local_runtime_parent = TASK_ROOT / "tmp"
RUNTIME_PARENT = (_local_runtime_parent if os.path.lexists(_local_runtime_parent)
                  else TASK_ROOT.parents[2])
RUNTIME_DIR = RUNTIME_PARENT / "e"
SOCKET = RUNTIME_DIR / "a.sock"
CLI = Path("/Users/guanjunhui/.local/bin/codex")
CLI_SHA = "4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc"
CWD = Path("/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/test-repo")
MODEL = "gpt-5.6-luna"
SCHEMA_SHA = "632b1201e888aaa77e7b5d0deb12ca6003f160ae09b3213c31803d19c0f5be2a"
ALLOWED_METHODS = {"initialize", "config/read", "configRequirements/read"}


def now():
    return time.clock_gettime_ns(time.CLOCK_MONOTONIC_RAW)


def stamp():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def value_sha(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def ensure_before_deadline(deadline):
    if now() >= deadline:
        raise TimeoutError("raw10_deadline_expired")


def remaining(deadline):
    left = (deadline - now()) / 1e9
    if left <= 0:
        raise TimeoutError("raw10_deadline_expired")
    return left


def identity(pid):
    result = subprocess.run(["/bin/ps", "-p", str(pid), "-o", "pid=,ppid=,uid=,pgid=,lstart=,comm="], text=True, capture_output=True, timeout=2)
    fields = result.stdout.strip().split(None, 9)
    if result.returncode or len(fields) != 10:
        return None
    libproc = ctypes.CDLL("/usr/lib/libproc.dylib")
    buf = ctypes.create_string_buffer(4096)
    count = libproc.proc_pidpath(pid, buf, len(buf))
    if not 0 < count < len(buf):
        return None
    return {"pid": int(fields[0]), "ppid": int(fields[1]), "uid": int(fields[2]), "pgid": int(fields[3]), "birth": " ".join(fields[4:9]), "comm": fields[9], "executable_path": os.fsdecode(buf.value)}


def same_process(saved):
    current = identity(saved["pid"])
    return current is not None and all(current.get(key) == saved.get(key) for key in ("pid", "uid", "pgid", "birth", "executable_path"))


def source_projection(source):
    if not isinstance(source, dict):
        raise ValueError("config_source_wrong_type")
    allowed = {"type", "file", "dotCodexFolder", "profile", "domain", "key", "id", "name"}
    if not isinstance(source.get("type"), str):
        raise ValueError("config_source_missing_type")
    required = {
        "mdm": {"domain", "key", "type"},
        "system": {"file", "type"},
        "enterpriseManaged": {"id", "name", "type"},
        "user": {"file", "type"},
        "project": {"dotCodexFolder", "type"},
        "sessionFlags": {"type"},
        "legacyManagedConfigTomlFromFile": {"file", "type"},
        "legacyManagedConfigTomlFromMdm": {"type"},
    }
    required_keys = required.get(source["type"])
    if required_keys is None:
        raise ValueError("config_source_unknown_type")
    if not required_keys.issubset(source):
        raise ValueError(f"config_source_missing_fields_{source['type']}")
    return {key: value for key, value in source.items() if key in allowed}


def config_projection(response):
    if not isinstance(response, dict) or not isinstance(response.get("config"), dict) or not isinstance(response.get("origins"), dict):
        raise ValueError("config_read_missing_required_config_or_origins")
    config, origins, layers = response["config"], response["origins"], response.get("layers")
    if layers is not None and not isinstance(layers, list):
        raise ValueError("config_read_layers_wrong_type")
    for key in ("model", "model_reasoning_effort"):
        if key not in config:
            raise ValueError(f"config_missing_{key}")
    result = {"model": config["model"], "model_reasoning_effort": config["model_reasoning_effort"], "origins": {}, "layers": []}
    expected_project = {"type": "project", "dotCodexFolder": str(CWD / ".codex")}
    for key in ("model", "model_reasoning_effort"):
        metadata = origins.get(key)
        if not isinstance(metadata, dict) or "name" not in metadata or "version" not in metadata:
            raise ValueError(f"config_origin_missing_{key}")
        if not isinstance(metadata["version"], str):
            raise ValueError(f"config_origin_version_wrong_type_{key}")
        origin = source_projection(metadata["name"])
        if origin != expected_project:
            raise ValueError(f"config_origin_not_expected_project_{key}")
        result["origins"][key] = {"name": origin, "version": metadata["version"]}
    for layer in layers or []:
        if (not isinstance(layer, dict) or "name" not in layer or "config" not in layer or "version" not in layer
                or not isinstance(layer["name"], dict) or not isinstance(layer["config"], dict)
                or not isinstance(layer["version"], str)):
            raise ValueError("config_layer_missing_required_fields")
        if "disabledReason" in layer and layer["disabledReason"] is not None and not isinstance(layer["disabledReason"], str):
            raise ValueError("config_layer_disabled_reason_wrong_type")
        layer_config = layer["config"]
        result["layers"].append({"name": source_projection(layer["name"]), "version": layer["version"], "disabledReason": layer.get("disabledReason"), "model": layer_config.get("model"), "model_reasoning_effort": layer_config.get("model_reasoning_effort")})
    if not any(layer["name"] == expected_project and layer["disabledReason"] is None for layer in result["layers"]):
        raise ValueError("config_expected_project_layer_missing_or_disabled")
    return result


def requirements_projection(response):
    if not isinstance(response, dict):
        raise ValueError("requirements_read_wrong_type")
    requirements = response.get("requirements")
    if requirements is None:
        return {"requirements": None}
    if not isinstance(requirements, dict):
        raise ValueError("requirements_wrong_type")
    models = requirements.get("models")
    if models is not None and not isinstance(models, dict):
        raise ValueError("requirements_models_wrong_type")
    new_thread = models.get("newThread") if models is not None else None
    if new_thread is not None and not isinstance(new_thread, dict):
        raise ValueError("requirements_models_newThread_wrong_type")
    if new_thread is not None:
        model = new_thread.get("model")
        effort = new_thread.get("modelReasoningEffort")
        if model is not None and model != MODEL:
            raise ValueError("requirements_newThread_model_conflicts_with_luna")
        if effort is not None and effort != "medium":
            raise ValueError("requirements_newThread_effort_conflicts_with_medium")
    return {"requirements": {"models": {"newThread": {"model": new_thread.get("model"), "modelReasoningEffort": new_thread.get("modelReasoningEffort")} if new_thread else None}}}


async def rpc(ws, request_id, method, params, deadline):
    if method not in ALLOWED_METHODS:
        raise ValueError("method_not_allowlisted")
    timeout = remaining(deadline)
    await asyncio.wait_for(ws.send(json.dumps({"id": request_id, "method": method, "params": params})), timeout)
    ensure_before_deadline(deadline)
    while True:
        timeout = remaining(deadline)
        packet = json.loads(await asyncio.wait_for(ws.recv(), timeout))
        ensure_before_deadline(deadline)
        if "method" in packet:
            continue
        if packet.get("id") != request_id:
            raise ValueError("unexpected_rpc_response_id")
        if "error" in packet:
            raise RuntimeError(f"{method}_failed:{packet['error'].get('code')}")
        if not isinstance(packet.get("result"), dict):
            raise ValueError(f"{method}_result_wrong_type")
        ensure_before_deadline(deadline)
        return packet["result"]


async def read_effective(report, deadline):
    read_started = now()
    raw = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    raw.setblocking(False)
    try:
        timeout = remaining(deadline)
        await asyncio.wait_for(asyncio.get_running_loop().sock_connect(raw, str(SOCKET)), timeout)
        ensure_before_deadline(deadline)
    except BaseException:
        raw.close()
        raise
    logger = logging.Logger("g0-effective-model", level=logging.CRITICAL + 1)
    logger.propagate = False
    try:
        ws = await unix_connect(uri="ws://localhost/", sock=raw, compression=None, proxy=None, ping_interval=None, open_timeout=remaining(deadline), close_timeout=1, max_size=262144, max_queue=8, logger=logger)
        ensure_before_deadline(deadline)
    except BaseException:
        raw.close()
        raise
    try:
        init = await rpc(ws, 1, "initialize", {"clientInfo": {"name": "g0-test-model", "version": "0.1.0"}}, deadline)
        report["initialize"] = {"platformOs": init.get("platformOs")}
        timeout = remaining(deadline)
        await asyncio.wait_for(ws.send('{"method":"initialized"}'), timeout)
        ensure_before_deadline(deadline)
        config_raw = await rpc(ws, 2, "config/read", {"cwd": str(CWD), "includeLayers": True}, deadline)
        req_raw = await rpc(ws, 3, "configRequirements/read", {}, deadline)
        config = config_projection(config_raw)
        report["config"] = config
        report["config_response_sha256"] = value_sha(config_raw)
        report["requirements"] = requirements_projection(req_raw)
        report["requirements_response_sha256"] = value_sha(req_raw)
        if config["model"] != MODEL or config["model_reasoning_effort"] != "medium":
            raise ValueError("effective_luna_or_medium_missing")
        report["assertions"] = {"effective_model_luna": True, "effective_reasoning_effort_medium": True}
        ensure_before_deadline(deadline)
        report["read_elapsed_ns"] = now() - read_started
        ensure_before_deadline(deadline)
    finally:
        await asyncio.wait_for(ws.close(), 2)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--run", action="store_true")
    args = parser.parse_args()
    if not args.run:
        print("prepared_only=true")
        print("native_or_model_started=false")
        return
    if (not SOCKET.is_absolute()
            or len(os.fsencode(str(SOCKET))) >= 104):
        raise RuntimeError("private_socket_path_invalid")
    if os.path.lexists(RUNTIME_DIR) or os.path.lexists(SOCKET):
        raise RuntimeError("probe_runtime_already_exists")
    try:
        parent_info = RUNTIME_PARENT.lstat()
    except FileNotFoundError as error:
        raise RuntimeError("probe_parent_must_be_existing_directory") from error
    if not stat.S_ISDIR(parent_info.st_mode):
        raise RuntimeError("probe_parent_must_be_existing_directory")
    executable = CLI.resolve()
    if hashlib.sha256(executable.read_bytes()).hexdigest() != CLI_SHA:
        raise RuntimeError("binary_pin_changed")
    os.mkdir(RUNTIME_DIR, 0o700)
    runtime_info = RUNTIME_DIR.lstat()
    if (not stat.S_ISDIR(runtime_info.st_mode) or stat.S_IMODE(runtime_info.st_mode) != 0o700
            or runtime_info.st_uid != os.getuid()):
        raise RuntimeError("probe_runtime_directory_identity_invalid")
    started = now()
    deadline = started + 10_000_000_000
    report = {"scope": "zero-model effective config read", "started_at": stamp(), "cwd": str(CWD), "binary_sha256": CLI_SHA, "schema_sha256": SCHEMA_SHA, "clock": "CLOCK_MONOTONIC_RAW", "raw_budget_seconds": 10}
    proc, saved = None, None
    try:
        proc = subprocess.Popen([str(executable), "app-server", "--listen", "unix://" + str(SOCKET)], cwd=CWD, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
        saved = identity(proc.pid)
        if saved is None or saved["executable_path"] != str(executable) or saved["uid"] != os.getuid() or saved["pgid"] != proc.pid:
            raise RuntimeError("unexpected_owned_process_identity")
        report["service"] = {key: saved[key] for key in ("pid", "uid", "birth", "executable_path")}
        while not SOCKET.exists():
            if proc.poll() is not None or now() >= deadline:
                raise RuntimeError("socket_startup_deadline_expired")
            time.sleep(0.025)
        info = SOCKET.lstat()
        if not stat.S_ISSOCK(info.st_mode) or info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o600:
            raise RuntimeError("unexpected_socket_identity")
        if not same_process(saved):
            raise RuntimeError("owned_service_identity_changed")
        lsof = subprocess.run(["/usr/sbin/lsof", "-a", "-p", str(proc.pid), "-U", "-Fn"], text=True, capture_output=True, timeout=2)
        if (lsof.returncode or "n" + str(SOCKET) not in lsof.stdout.splitlines()
                or not same_process(saved) or now() >= deadline):
            raise RuntimeError("private_service_socket_unverified")
        ensure_before_deadline(deadline)
        report["socket"] = {"path": str(SOCKET), "dev": info.st_dev, "ino": info.st_ino, "uid": info.st_uid, "mode": "0600", "held_by_owned_service": True}
        asyncio.run(read_effective(report, deadline))
        report["status"] = "read_complete"
    except Exception as error:
        report["status"] = "inconclusive"
        report["error"] = {"type": type(error).__name__, "message_sha256": value_sha(str(error)), "message_bytes": len(str(error).encode())}
    finally:
        cleanup = {"started_at": stamp()}
        try:
            if proc is not None and proc.poll() is None:
                if saved is not None and same_process(saved):
                    os.killpg(proc.pid, signal.SIGTERM)
                    cleanup["term_sent_to_owned_group"] = True
                    try:
                        proc.wait(timeout=4)
                    except subprocess.TimeoutExpired:
                        if same_process(saved):
                            os.killpg(proc.pid, signal.SIGKILL)
                            cleanup["kill_sent_to_owned_group"] = True
                            try:
                                proc.wait(timeout=2)
                            except subprocess.TimeoutExpired:
                                cleanup["service_wait_timeout"] = True
                        else:
                            cleanup["identity_changed_no_kill"] = True
                else:
                    cleanup["identity_changed_no_signal"] = True
        except Exception as error:
            cleanup["process_cleanup_error"] = {"type": type(error).__name__, "message_sha256": value_sha(str(error))}
        cleanup["process_exit_code"] = proc.returncode if proc is not None else None
        cleanup["owned_service_exited"] = proc is not None and saved is not None and proc.poll() is not None
        cleanup["socket_removed"] = False
        if os.path.lexists(SOCKET):
            try:
                info = SOCKET.lstat()
                expected = report.get("socket", {})
                if (cleanup["owned_service_exited"] and stat.S_ISSOCK(info.st_mode)
                        and (info.st_dev, info.st_ino, info.st_uid)
                        == (expected.get("dev"), expected.get("ino"), expected.get("uid"))):
                    SOCKET.unlink()
                    cleanup["socket_removed"] = True
            except OSError as error:
                cleanup["socket_cleanup_error"] = {"type": type(error).__name__, "message_sha256": value_sha(str(error))}
        cleanup["socket_absent"] = not os.path.lexists(SOCKET)
        try:
            RUNTIME_DIR.rmdir()
            cleanup["runtime_dir_removed"] = True
        except OSError:
            cleanup["runtime_dir_removed"] = False
        cleanup["complete"] = (cleanup["owned_service_exited"] and cleanup["socket_absent"]
                                and cleanup["runtime_dir_removed"] and "process_cleanup_error" not in cleanup)
        report["cleanup"] = cleanup
        if report.get("status") == "read_complete" and not cleanup["complete"]:
            report["status"] = "inconclusive"
            report["error"] = {"type": "RuntimeError", "message_sha256": value_sha("cleanup_incomplete"), "message_bytes": len("cleanup_incomplete".encode())}
        print(json.dumps(report, sort_keys=True))
    if report.get("status") != "read_complete":
        raise SystemExit(1)


if __name__ == "__main__":
    main()
