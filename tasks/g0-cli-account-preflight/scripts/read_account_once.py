"""One private-UDS account read. No thread, turn, refresh, login or reset request."""
import asyncio
import ctypes
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import signal
import stat
import subprocess
import threading
import time

from websockets.asyncio.client import unix_connect

ROOT = Path(__file__).resolve().parents[1]
SOCKET = ROOT / "tmp/a.sock"
CLI = Path("/Users/guanjunhui/.local/bin/codex")
CLI_SHA = "4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc"
CWD = Path("/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/test-repo")
ALLOWED = (
    ("initialize", {"clientInfo": {"name": "g0-cli-account-preflight", "version": "0.1.0"}}),
    ("account/read", {"refreshToken": False}),
    ("account/rateLimits/read", {"supportsLunaReserve": False, "excludeResetCreditDetails": True}),
)


def now():
    return time.clock_gettime_ns(time.CLOCK_MONOTONIC_RAW)


def stamp():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def sha(value):
    return hashlib.sha256(value).hexdigest()


def persist(name, value):
    path = ROOT / "docs" / name
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as stream:
        json.dump(value, stream, indent=2)
        stream.write("\n")
        stream.flush()
        os.fsync(stream.fileno())


def identity(pid):
    completed = subprocess.run(["/bin/ps", "-p", str(pid), "-o", "pid=,ppid=,uid=,pgid=,lstart=,comm="],
                               text=True, capture_output=True, timeout=2)
    fields = completed.stdout.strip().split(None, 9)
    if completed.returncode or len(fields) != 10:
        return None
    lib = ctypes.CDLL("/usr/lib/libproc.dylib")
    buffer = ctypes.create_string_buffer(4096)
    count = lib.proc_pidpath(pid, buffer, len(buffer))
    if not 0 < count < len(buffer):
        raise ValueError("process_executable_unavailable")
    return {"pid": int(fields[0]), "ppid": int(fields[1]), "uid": int(fields[2]),
            "pgid": int(fields[3]), "birth": " ".join(fields[4:9]),
            "comm": fields[9], "executable_path": os.fsdecode(buffer.value)}


def same_process(saved):
    current = identity(saved["pid"])
    return current is not None and all(current[k] == saved[k] for k in ("pid", "uid", "pgid", "birth", "executable_path"))


def window(value):
    if value is None:
        return None
    if not isinstance(value, dict) or type(value.get("usedPercent")) is not int:
        raise ValueError("invalid_rate_window")
    return {key: value.get(key) for key in ("usedPercent", "windowDurationMins", "resetsAt")}


def bucket(value):
    return {"limitId": value.get("limitId"), "primary": window(value.get("primary")),
            "secondary": window(value.get("secondary")),
            "spendControlReached": value.get("spendControlReached"),
            "rateLimitReachedType": value.get("rateLimitReachedType")}


class RpcFailure(Exception):
    def __init__(self, detail):
        self.detail = detail


async def account_reads(report, operation_deadline):
    def remaining(deadline):
        left = (min(deadline, operation_deadline) - now()) / 1e9
        if left <= 0:
            raise TimeoutError("stage_deadline_expired")
        return left

    deadline = min(now() + 10_000_000_000, operation_deadline)
    ws = await asyncio.wait_for(unix_connect(str(SOCKET), open_timeout=remaining(deadline),
                                           close_timeout=1, max_size=1024 * 1024, max_queue=16), remaining(deadline))
    remaining(deadline)
    try:
        for request_id, (method, params) in enumerate(ALLOWED, 1):
            began = now()
            deadline = min(began + 10_000_000_000, operation_deadline)
            await asyncio.wait_for(ws.send(json.dumps({"id": request_id, "method": method, "params": params})), remaining(deadline))
            response = None
            for _ in range(64):
                raw = await asyncio.wait_for(ws.recv(), remaining(deadline))
                remaining(deadline)
                packet = json.loads(raw)
                if "method" in packet:
                    report["ignored_notification_count"] += 1
                    continue
                if type(packet.get("id")) is not int or packet["id"] != request_id:
                    raise ValueError("unexpected_rpc_response_id")
                if "error" in packet:
                    error = packet["error"]
                    message = str(error.get("message", "")).encode()
                    raise RpcFailure({"method": method, "code": error.get("code"),
                                      "message_sha256": sha(message), "message_bytes": len(message)})
                response = packet["result"]
                break
            if response is None:
                raise ValueError("response_frame_limit")
            report["stages"].append({"method": method, "params": params, "elapsed_ns": now() - began})
            if method == "initialize":
                report["initialize"] = {"platformOs": response.get("platformOs"),
                                        "codex_home_matches": response.get("codexHome") == "/Users/guanjunhui/.codex"}
                await asyncio.wait_for(ws.send('{"method":"initialized"}'), remaining(deadline))
                remaining(deadline)
            elif method == "account/read":
                account = response.get("account")
                report["account"] = {"kind": account.get("type") if isinstance(account, dict) else None,
                                     "requiresOpenaiAuth": response.get("requiresOpenaiAuth")}
                email = account.get("email") if isinstance(account, dict) else None
                report["account"]["emailSha256"] = sha(email.encode()) if isinstance(email, str) and email else None
            else:
                account_id = response.get("accountId")
                report["usage"] = {"accountIdSha256": sha(account_id.encode()) if isinstance(account_id, str) and account_id else None,
                                   "ordinaryUsageAllowed": response.get("ordinaryUsageAllowed"),
                                   "rateLimits": bucket(response["rateLimits"]),
                                   "rateLimitsByLimitId": {key: bucket(value) for key, value in response.get("rateLimitsByLimitId", {}).items()}}
                report["usage_read_at"] = stamp()
    finally:
        await asyncio.wait_for(ws.close(), 2)


def main():
    if SOCKET.exists() or os.path.lexists(SOCKET) or len(os.fsencode(SOCKET)) >= 104:
        raise ValueError("private_socket_unavailable")
    if stat.S_IMODE(SOCKET.parent.stat().st_mode) != 0o700:
        raise ValueError("private_directory_mode")
    executable = CLI.resolve()
    if sha(executable.read_bytes()) != CLI_SHA:
        raise ValueError("binary_pin_changed")
    started = now()
    operation_deadline = started + 40_000_000_000
    report = {"scope": "Private service account reads only; not G0 qualification", "started_at": stamp(),
              "clock_impl": "clock_gettime_ns(CLOCK_MONOTONIC_RAW)", "overall_budget_seconds": 50,
              "per_stage_budget_seconds": 10, "cwd": str(CWD), "binary_sha256": CLI_SHA,
              "stages": [], "ignored_notification_count": 0,
              "implementation_boundary": "refreshToken=false; no explicit refresh/login/reset request. Native AuthManager may internally maintain existing login state."}
    proc = None
    saved = None
    stderr_hash = hashlib.sha256()
    stderr_bytes = [0]
    reader = None
    try:
        proc = subprocess.Popen([str(CLI), "app-server", "--listen", "unix://" + str(SOCKET)],
                                cwd=CWD, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                                stderr=subprocess.PIPE, start_new_session=True)
        def drain():
            for data in iter(lambda: proc.stderr.read(65536), b""):
                stderr_hash.update(data)
                stderr_bytes[0] += len(data)
        reader = threading.Thread(target=drain, daemon=True)
        reader.start()
        saved = identity(proc.pid)
        if saved is None or saved["executable_path"] != str(executable) or saved["uid"] != os.getuid() or saved["pgid"] != proc.pid:
            raise ValueError("unexpected_owned_process_identity")
        report["service"] = saved
        deadline = min(started + 10_000_000_000, operation_deadline)
        while not SOCKET.exists():
            if proc.poll() is not None:
                raise ValueError("service_exited_before_socket")
            if now() >= deadline:
                raise TimeoutError("socket_startup_deadline_expired")
            time.sleep(0.025)
        info = SOCKET.lstat()
        if not stat.S_ISSOCK(info.st_mode) or info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o600:
            raise ValueError("unexpected_socket_identity")
        lsof = subprocess.run(["/usr/sbin/lsof", "-a", "-p", str(proc.pid), "-U", "-Fn"],
                              text=True, capture_output=True, timeout=2)
        if lsof.returncode or "n" + str(SOCKET) not in lsof.stdout.splitlines() or not same_process(saved) or now() >= deadline:
            raise ValueError("private_service_socket_unverified")
        report["socket"] = {"path": str(SOCKET), "dev": info.st_dev, "ino": info.st_ino, "uid": info.st_uid,
                            "mode": "0600", "held_by_owned_service": True, "default_socket_used": False}
        report["stages"].append({"method": "owned_service_start", "elapsed_ns": now() - started})
        persist("service-binding.json", {"service": saved, "socket": report["socket"], "binary_sha256": CLI_SHA})
        asyncio.run(account_reads(report, operation_deadline))
        report["status"] = "read_complete"
    except Exception as error:
        report["status"] = "inconclusive"
        report["error"] = error.detail if isinstance(error, RpcFailure) else {
            "type": type(error).__name__, "message_sha256": sha(str(error).encode())}
    finally:
        began = now()
        cleanup = {"started_at": stamp()}
        if proc is not None:
            if proc.poll() is None:
                if saved is not None and same_process(saved):
                    os.killpg(proc.pid, signal.SIGTERM)
                    cleanup["term_sent_to_owned_group"] = True
                    try:
                        proc.wait(timeout=4)
                    except subprocess.TimeoutExpired:
                        if same_process(saved):
                            os.killpg(proc.pid, signal.SIGKILL)
                            cleanup["kill_sent_to_owned_group"] = True
                        proc.wait(timeout=2)
                else:
                    cleanup["identity_changed_no_signal"] = True
            cleanup["owned_service_exit_code"] = proc.poll()
            cleanup["owned_service_exited"] = proc.poll() is not None
            if reader is not None:
                reader.join(timeout=0.5)
        if SOCKET.exists() and report.get("socket") and cleanup.get("owned_service_exited"):
            info = SOCKET.lstat()
            if (info.st_dev, info.st_ino, info.st_uid) == tuple(report["socket"][k] for k in ("dev", "ino", "uid")):
                SOCKET.unlink()
                cleanup["removed_owned_socket"] = True
        cleanup["private_socket_exists_after"] = os.path.lexists(SOCKET)
        cleanup["elapsed_ns"] = now() - began
        report["cleanup"] = cleanup
        report["stderr"] = {"bytes": stderr_bytes[0], "sha256": stderr_hash.hexdigest(), "body_retained": False}
        report["elapsed_ns"] = now() - started
        report["ended_at"] = stamp()
        persist("cli-account-read.json", report)
        print(json.dumps({"status": report["status"], "cleanup": cleanup, "elapsed_ns": report["elapsed_ns"]}))


if __name__ == "__main__":
    main()
