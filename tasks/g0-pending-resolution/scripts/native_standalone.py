"""One bounded native standalone-output sample; no generic attachment authority."""
import asyncio
import ctypes
import fcntl
import hashlib
import importlib.metadata
import json
import logging
import os
import re
import socket
import stat
import subprocess
import sys
import threading
import time
import uuid

from delivery_audit import canonical, envelope, parse
from owner_context import owner_context_sha256, validate_initialize_codex_home, validate_owner_context

CLI_VERSION = "0.154.0"
ACTIVE_NONCE = "sd-qual-08"
CLI_SHA = "4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc"
GO_SHA = "feb9b9ad969abca5ff35db28c0f0d8d920dc0778d7aac68a90952900e989a285"
READ_SECONDS, MODEL_SECONDS, MAX_BYTES, MAX_FRAMES = 10, 120, 65536, 256
NS = 1_000_000_000
CLOCK_IMPL = "clock_gettime_ns(CLOCK_MONOTONIC_RAW)"
SAFE = re.compile(r"[A-Za-z0-9_-]{1,64}\Z")
HASH = re.compile(r"[0-9a-f]{64}\Z")
FILES = {"binding.json", "controller-evidence.json", "producer.json", "intent.json", "prepare-meta.json", "send-attempt.json",
         "receipt.json", "send-observation.json", "read-observation.json", "audit-input.json"}


class ProbeError(Exception):
    def __init__(self, code, rpc_error=None):
        self.code = code
        self.rpc_error = rpc_error
        super().__init__(code)


def check(ok, code="invalid_data"):
    if not ok:
        raise ProbeError(code)


def exact(value, keys):
    check(type(value) is dict and set(value) == set(keys), "invalid_schema")


def number(value, minimum=1, maximum=2**63 - 1):
    return type(value) is int and minimum <= value <= maximum


def safe(value):
    return type(value) is str and SAFE.fullmatch(value) is not None


def absolute(value):
    return type(value) is str and len(value) <= 4096 and os.path.isabs(value) and not any(c in value for c in "\0\r\n")


def is_uuid(value):
    if type(value) is not str or len(value) != 36:
        return False
    try:
        return str(uuid.UUID(value)) == value
    except ValueError:
        return False


def digest(data):
    return hashlib.sha256(data).hexdigest()


def decode(data):
    try:
        raw = data.encode() if type(data) is str else data
        check(type(raw) is bytes and 0 < len(raw) <= MAX_BYTES, "message_limit")
        return parse(raw)
    except (ValueError, TypeError, RecursionError):
        raise ProbeError("invalid_json") from None


def validate_binding(value, root, nonce):
    base_keys = ("version", "nonce", "controller_thread_id", "controller_epoch", "expected_cwd", "prior_turn_id",
                 "native_cli_version", "boot_id", "service", "tui", "go_binary", "go_binary_sha256", "job_dir", "controller_evidence_sha256")
    v2_keys = base_keys + ("owner_context", "owner_context_sha256")
    check(type(value) is dict and set(value) in (set(base_keys), set(v2_keys)), "invalid_binding_schema")
    check(type(value["version"]) is int and value["version"] in (1, 2))
    if value["version"] == 1:
        check(set(value) == set(base_keys), "invalid_binding_schema")
    else:
        check(set(value) == set(v2_keys), "invalid_binding_schema")
    check(safe(root) and nonce == ACTIVE_NONCE and value["nonce"] == nonce and value["controller_thread_id"] == root, "root_nonce_mismatch")
    check(type(value["controller_epoch"]) is int and value["controller_epoch"] == 1)
    check(value["native_cli_version"] == CLI_VERSION and value["go_binary_sha256"] == GO_SHA, "version_mismatch")
    check(safe(value["prior_turn_id"]) and type(value["boot_id"]) is str)
    try:
        check(str(uuid.UUID(value["boot_id"])) == value["boot_id"], "invalid_boot_id")
    except ValueError:
        raise ProbeError("invalid_boot_id") from None
    check(type(value["controller_evidence_sha256"]) is str and HASH.fullmatch(value["controller_evidence_sha256"]))
    check(all(absolute(value[key]) for key in ("expected_cwd", "go_binary", "job_dir")))
    service = value["service"]
    exact(service, ("pid", "uid", "birth", "comm", "executable_path", "socket_path", "socket_dev", "socket_ino", "native_binary_sha256"))
    check(number(service["uid"], 0) and service["uid"] == os.getuid(), "owner_mismatch")
    check(all(number(service[key]) for key in ("pid", "socket_dev", "socket_ino")))
    check(type(service["birth"]) is str and 0 < len(service["birth"]) <= 64)
    check(absolute(service["executable_path"]) and absolute(service["socket_path"]))
    check(service["native_binary_sha256"] == CLI_SHA, "version_mismatch")
    tui = value["tui"]
    exact(tui, ("pid", "uid", "birth", "comm", "executable_path", "native_binary_sha256"))
    check(number(tui["pid"]) and tui["pid"] != service["pid"] and number(tui["uid"], 0) and tui["uid"] == os.getuid(), "owner_mismatch")
    check(type(tui["birth"]) is str and 0 < len(tui["birth"]) <= 64 and absolute(tui["executable_path"]))
    check(tui["native_binary_sha256"] == CLI_SHA, "version_mismatch")
    for process in (service, tui):
        check(number(process["pid"], 1, 2**31 - 1), "invalid_pid")
        check(type(process["comm"]) is str and 0 < len(process["comm"]) <= 4096 and not any(c in process["comm"] for c in "\0\r\n"), "invalid_comm")
    return value


def require_owner_context(binding):
    """Require the v2 owner context before any new prepare/send operation."""

    check(type(binding) is dict and binding.get("version") == 2, "owner_context_required")
    context = binding.get("owner_context")
    check(type(binding.get("owner_context_sha256")) is str and binding["owner_context_sha256"] == owner_context_sha256(context), "owner_context_invalid")
    try:
        validate_owner_context(context, context)
    except (TypeError, ValueError, KeyError):
        raise ProbeError("owner_context_invalid") from None
    return context


def attach_owner_context(binding, context):
    """Create a v2 binding projection without changing the legacy schema."""

    validate_owner_context(context, context)
    result = dict(binding)
    result["version"] = 2
    result["owner_context"] = dict(context)
    result["owner_context_sha256"] = owner_context_sha256(context)
    return result


class Case:
    def __init__(self, directory):
        check(absolute(directory), "invalid_case")
        info = os.lstat(directory)
        check(stat.S_ISDIR(info.st_mode) and stat.S_IMODE(info.st_mode) == 0o700 and info.st_uid == os.getuid(), "untrusted_case")
        self.fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        current = os.fstat(self.fd)
        check((info.st_dev, info.st_ino) == (current.st_dev, current.st_ino), "untrusted_case")
        self.lock = os.open("case.lock", os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600, dir_fd=self.fd)
        info = os.fstat(self.lock)
        check(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode) == 0o600 and info.st_uid == os.getuid() and info.st_nlink == 1, "untrusted_case")
        try:
            fcntl.flock(self.lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            os.close(self.lock)
            os.close(self.fd)
            raise ProbeError("case_busy") from None

    def __enter__(self):
        return self

    def __exit__(self, *_):
        os.close(self.lock)
        os.close(self.fd)

    def read_raw(self, name):
        check(name in FILES, "invalid_file")
        fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=self.fd)
        with os.fdopen(fd, "rb") as stream:
            info = os.fstat(stream.fileno())
            check(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode) == 0o600 and info.st_uid == os.getuid() and info.st_nlink == 1 and 0 < info.st_size <= MAX_BYTES, "untrusted_file")
            data = stream.read(MAX_BYTES + 1)
            check(len(data) <= MAX_BYTES, "message_limit")
            return data

    def read(self, name):
        return decode(self.read_raw(name))

    def write_new(self, name, value):
        check(name in FILES, "invalid_file")
        data = (canonical(value) + "\n").encode()
        check(len(data) <= MAX_BYTES, "artifact_limit")
        try:
            fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=self.fd)
        except FileExistsError:
            raise ProbeError("artifact_exists") from None
        with os.fdopen(fd, "wb") as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.fsync(self.fd)


def command_output(args, timeout=READ_SECONDS):
    deadline_ns = continuous_ns() + int(timeout * NS)
    try:
        result = subprocess.run(args, capture_output=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        raise ProbeError("local_read_timeout") from None
    time_left(deadline_ns)
    check(len(result.stdout) <= MAX_BYTES and len(result.stderr) <= MAX_BYTES, "local_output_limit")
    check(result.returncode == 0, "local_command_failed")
    return result.stdout


def continuous_ns():
    return time.clock_gettime_ns(time.CLOCK_MONOTONIC_RAW)


def time_left(deadline_ns):
    remaining_ns = deadline_ns - continuous_ns()
    check(remaining_ns > 0, "read_window_expired")
    return remaining_ns / NS


def current_clock():
    boot = command_output(["/usr/sbin/sysctl", "-n", "kern.bootsessionuuid"], 2).decode("ascii").strip().lower()
    return {"boot_id": boot, "clock_impl": CLOCK_IMPL, "mono_ns": continuous_ns(), "wall_ns": time.time_ns()}


def remaining_window(attempt, now):
    check(attempt["boot_id"] == now["boot_id"] and attempt["clock_impl"] == now["clock_impl"] == CLOCK_IMPL, "clock_context_changed")
    check(all(number(value["mono_ns"], 0) and type(value["wall_ns"]) is int for value in (attempt, now)), "invalid_clock")
    elapsed = now["mono_ns"] - attempt["mono_ns"]
    check(elapsed >= 0, "clock_inconsistent")
    check(elapsed < MODEL_SECONDS * NS, "model_window_expired")
    return (MODEL_SECONDS * NS - elapsed) / NS


def process_executable_path(pid):
    check(number(pid, 1, 2**31 - 1), "invalid_pid")
    lib = ctypes.CDLL("/usr/lib/libproc.dylib")
    lib.proc_pidpath.argtypes = [ctypes.c_int, ctypes.c_void_p, ctypes.c_uint32]
    lib.proc_pidpath.restype = ctypes.c_int
    buffer = ctypes.create_string_buffer(4096)
    count = lib.proc_pidpath(pid, buffer, len(buffer))
    check(0 < count < len(buffer), "process_executable_unavailable")
    path = os.fsdecode(buffer.value)
    check(absolute(path), "invalid_executable_path")
    return path


def verify_machine(binding, hashes=True):
    service = binding["service"]
    for role in ("service", "tui"):
        process = binding[role]
        row = command_output(["/bin/ps", "-p", str(process["pid"]), "-o", "pid=,uid=,lstart=,comm="], 2).decode().strip().split(None, 7)
        check(len(row) == 8 and row[0] == str(process["pid"]) and row[1] == str(process["uid"]) and " ".join(row[2:7]) == process["birth"] and row[7] == process["comm"], role + "_identity_changed")
        check(process_executable_path(process["pid"]) == process["executable_path"], role + "_executable_changed")
    info = os.lstat(service["socket_path"])
    check(stat.S_ISSOCK(info.st_mode) and info.st_uid == os.getuid() and (info.st_dev, info.st_ino) == (service["socket_dev"], service["socket_ino"]), "socket_identity_changed")
    check(current_clock()["boot_id"] == binding["boot_id"], "boot_changed")
    if hashes:
        for path, expected in {(service["executable_path"], CLI_SHA), (binding["tui"]["executable_path"], CLI_SHA), (binding["go_binary"], GO_SHA)}:
            fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
            with os.fdopen(fd, "rb") as stream:
                info = os.fstat(stream.fileno())
                check(stat.S_ISREG(info.st_mode) and 0 < info.st_size <= 512 * 1024 * 1024, "untrusted_binary")
                h = hashlib.sha256()
                for chunk in iter(lambda: stream.read(1024 * 1024), b""):
                    h.update(chunk)
            check(h.hexdigest() == expected, "binary_mismatch")


def validate_go(value, binding, allow_ack=False):
    exact(value, ("version", "status", "nonce", "controller_thread", "revision", "task_revision", "cancelled", "event_hash", "result", "ack"))
    check(type(value["version"]) is int and value["version"] == 1 and value["status"] == "completed", "producer_not_completed")
    check(value["nonce"] == binding["nonce"] and value["controller_thread"] == binding["controller_thread_id"], "producer_owner_mismatch")
    check(type(value["revision"]) is int and value["revision"] == 1 and type(value["task_revision"]) is int and value["task_revision"] == 1 and value["cancelled"] is False, "producer_revision_mismatch")
    expected_result = {"version": 1, "status": "completed", "nonce": binding["nonce"], "count": 1}
    exact(value["result"], expected_result)
    check(value["result"] == expected_result and type(value["result"]["version"]) is int and type(value["result"]["count"]) is int, "invalid_result")
    event = {"version": 1, "nonce": binding["nonce"], "controller_thread": binding["controller_thread_id"], "task_revision": 1, "result": expected_result}
    check(value["event_hash"] == digest(json.dumps(event, separators=(",", ":")).encode()), "producer_hash_mismatch")
    ack = value["ack"]
    if ack is not None:
        check(allow_ack, "producer_already_acked")
        exact(ack, ("version", "status", "nonce", "controller_thread", "revision", "event_hash", "command_id", "decision", "decision_count", "effect_count"))
        check(type(ack["version"]) is int and ack["version"] == 1 and ack["status"] == "acknowledged")
        check(ack["nonce"] == binding["nonce"] and ack["controller_thread"] == binding["controller_thread_id"] and type(ack["revision"]) is int and ack["revision"] == 1 and ack["event_hash"] == value["event_hash"], "ack_binding_mismatch")
        check(safe(ack["command_id"]) and ack["decision"] in ("handled", "waiting_user", "stale", "rejected"))
        check(type(ack["decision_count"]) is int and ack["decision_count"] == 1 and type(ack["effect_count"]) is int and ack["effect_count"] == (1 if ack["decision"] == "handled" else 0))
    return value


def make_intent(binding, inspection):
    validate_go(inspection, binding)
    body = {"version": 1, "delivery_id": binding["nonce"] + "_delivery", "controller_thread_id": binding["controller_thread_id"],
            "controller_epoch": 1, "events": [{"event_id": binding["nonce"] + "_result", "event_revision": 1, "kind": "result",
            "payload_hash": inspection["event_hash"], "action_slot": "ack_r1"}]}
    return {**body, "payload_hash": digest(canonical(body).encode())}


def audit_ack(intent, inspection):
    """Project one already validated Go ACK into the offline audit schema.

    This is deliberately a projection only: validate_go() has checked the Go
    record's shape and binding, while these checks bind it to the exact event
    declared by the intent before it can enter audit-input.json.
    """
    check(type(inspection) is dict and type(inspection.get("ack")) is dict, "ack_missing")
    ack = inspection["ack"]
    check(intent["delivery_id"] == ack["nonce"] + "_delivery", "ack_nonce_mismatch")
    check(ack["controller_thread"] == intent["controller_thread_id"], "ack_root_mismatch")
    check(ack["revision"] == intent["events"][0]["event_revision"], "ack_revision_mismatch")
    event = next((entry for entry in intent["events"] if entry["payload_hash"] == ack["event_hash"]), None)
    check(event is not None and event["event_id"] == intent["events"][0]["event_id"], "ack_hash_mismatch")
    return {"delivery_id": intent["delivery_id"], "controller_thread_id": intent["controller_thread_id"],
            "controller_epoch": intent["controller_epoch"], "event_id": event["event_id"],
            "event_revision": event["event_revision"], "event_hash": event["payload_hash"],
            "action_slot": event["action_slot"], "decision": ack["decision"],
            "command_id": ack["command_id"], "decision_count": ack["decision_count"],
            "effect_count": ack["effect_count"]}


def make_request(request_id, method, params, binding, intent=None):
    check(number(request_id, 1, 10000), "invalid_request_id")
    allowed = {
        "initialize": {"clientInfo": {"name": "g0-native-standalone", "version": "0.1.0"}, "capabilities": {"experimentalApi": True}},
        "server/diagnostics": {}, "remoteControl/status/read": {},
        "thread/read": {"threadId": binding["controller_thread_id"], "includeTurns": True},
    }
    if method == "turn/start":
        check(intent is not None, "intent_missing")
        try:
            envelope(intent)
        except (ValueError, TypeError, KeyError):
            raise ProbeError("invalid_intent") from None
        check(intent["controller_thread_id"] == binding["controller_thread_id"] and intent["delivery_id"] == binding["nonce"] + "_delivery", "intent_binding_mismatch")
        expected = {"threadId": binding["controller_thread_id"], "input": [], "toolOutput": {"name": "g0_delivery", "namespace": "orchestration", "output": canonical(intent)}}
    else:
        check(method in allowed, "rpc_not_allowed")
        expected = allowed[method]
    check(canonical(params) == canonical(expected), "rpc_params_not_allowed")
    return {"id": request_id, "method": method, "params": expected}


def filter_thread(result, binding, intent, require_ready=False, expected_session_id=None):
    check(type(result) is dict and type(result.get("thread")) is dict, "invalid_thread")
    thread = result["thread"]
    check(thread.get("id") == binding["controller_thread_id"] and thread.get("source") == "vscode" and thread.get("cliVersion") == CLI_VERSION and thread.get("cwd") == binding["expected_cwd"], "thread_binding_mismatch")
    check(thread.get("historyMode") in ("legacy", "paginated") and thread.get("ephemeral") is False, "unsupported_history")
    check(all(key in thread and thread[key] is None for key in ("parentThreadId", "forkedFromId")), "thread_family_mismatch")
    check(is_uuid(thread.get("sessionId")), "invalid_session_id")
    if expected_session_id is not None:
        check(thread["sessionId"] == expected_session_id, "session_changed")
    check(type(thread.get("status")) is dict and thread["status"].get("type") in ("idle", "active", "notLoaded", "systemError"), "invalid_thread_status")
    check(type(thread.get("turns")) is list and 0 < len(thread["turns"]) <= 32, "history_limit")
    ready_marker = binding["nonce"].upper().replace("-", "_") + "_READY"
    ack_marker = binding["nonce"].upper().replace("-", "_") + "_ACK"
    facts = {"history_match": False, "new_turn_completed": False, "ack_final_marker": False, "prior_tools_terminal": False,
             "session_id": thread["sessionId"], "fresh_root_family": True,
             "prior_started_at": None, "prior_completed_at": None, "root_idle": thread["status"]["type"] == "idle",
             "all_tools_and_turns_terminal": True, "matching_turn_is_last": False}
    filtered = {"thread": {"id": thread["id"], "historyMode": thread["historyMode"], "turns": []}}
    identities, turn_ids = {}, set()
    for turn in thread["turns"]:
        check(type(turn) is dict and safe(turn.get("id")) and turn.get("status") in ("inProgress", "completed", "failed", "interrupted"), "invalid_turn")
        check(turn["id"] not in turn_ids, "duplicate_turn_id")
        turn_ids.add(turn["id"])
        check(turn.get("itemsView") == "full" and type(turn.get("items")) is list and len(turn["items"]) <= 64, "incomplete_history")
        output_turn = {"id": turn["id"], "status": turn["status"], "itemsView": "full", "items": []}
        terminal, ready_final, ack_final, match, user_input = True, False, False, False, False
        for item in turn["items"]:
            check(type(item) is dict and safe(item.get("id")), "invalid_item")
            kind = item.get("type")
            check(kind in ("userMessage", "agentMessage", "reasoning", "plan", "functionCallOutput", "commandExecution", "dynamicToolCall", "fileChange", "contextCompaction"), "unsupported_item")
            key, identity = (turn["id"], item["id"]), (kind, item.get("name"), item.get("namespace"))
            check(key not in identities or identities[key] == identity, "item_identity_conflict")
            identities[key] = identity
            output_item = {"id": item["id"], "type": kind}
            if kind == "userMessage": user_input = True
            if kind in ("commandExecution", "dynamicToolCall", "fileChange"):
                check(item.get("status") in ("inProgress", "completed", "failed", "declined"), "unknown_tool_status")
                terminal &= item["status"] != "inProgress"
            if kind == "agentMessage":
                check(type(item.get("text")) is str, "invalid_agent_message")
                ready_final |= item.get("phase") == "final_answer" and item["text"].strip() == ready_marker
                ack_final |= item.get("phase") == "final_answer" and item["text"].strip() == ack_marker
            if kind == "functionCallOutput":
                check(type(item.get("name")) is str and safe(item["name"]), "invalid_tool_name")
                check(item.get("namespace") is None or safe(item["namespace"]), "invalid_tool_namespace")
                output_item.update(name=item["name"], namespace=item.get("namespace"))
                if item["name"] == "g0_delivery":
                    check(intent is not None and item.get("namespace") == "orchestration" and type(item.get("output")) is str, "unexpected_tool_output")
                    check(canonical(decode(item["output"])) == canonical(intent), "unexpected_tool_output")
                    output_item["output"] = item["output"]
                    match = True
            output_turn["items"].append(output_item)
        if turn["id"] == binding["prior_turn_id"]:
            for key in ("startedAt", "completedAt"):
                check(turn.get(key) is None or number(turn[key], 0), "invalid_turn_timestamp")
            facts.update(prior_started_at=turn.get("startedAt"), prior_completed_at=turn.get("completedAt"))
            facts["prior_tools_terminal"] = terminal and turn["status"] == "completed"
            if require_ready:
                check(facts["prior_tools_terminal"] and ready_final, "prior_turn_not_ready")
        if match:
            check(turn["id"] != binding["prior_turn_id"] and not user_input, "not_a_new_standalone_turn")
            facts.update(history_match=True, matching_turn_id=turn["id"], new_turn_completed=terminal and turn["status"] == "completed", ack_final_marker=ack_final)
        facts["all_tools_and_turns_terminal"] &= terminal and turn["status"] != "inProgress"
        filtered["thread"]["turns"].append(output_turn)
    facts["matching_turn_is_last"] = facts.get("matching_turn_id") == thread["turns"][-1]["id"]
    if require_ready:
        check(facts["root_idle"] and facts["all_tools_and_turns_terminal"] and thread["turns"][-1]["id"] == binding["prior_turn_id"] and facts["prior_tools_terminal"], "thread_not_idle")
    return filtered, facts


async def issue_once(rpc, case, binding, intent, binding_hash, *, owner_context_required=True):
    require_owner_context(binding)
    attempt = {"version": 1, "status": "send_intent", "nonce": binding["nonce"], "controller_thread_id": binding["controller_thread_id"],
               "binding_sha256": binding_hash, "payload_sha256": digest(canonical(intent).encode()), **current_clock()}
    check(attempt["boot_id"] == binding["boot_id"], "boot_changed")
    case.write_new("send-attempt.json", attempt)
    # A failed identity check here consumes this case, even before the wire write.
    verify_machine(binding)
    remaining_window(attempt, current_clock())
    params = {"threadId": binding["controller_thread_id"], "input": [],
              "toolOutput": {"name": "g0_delivery", "namespace": "orchestration", "output": canonical(intent)}}
    result = await rpc.call("turn/start", params, intent=intent)
    remaining_window(attempt, current_clock())
    check(type(result) is dict and type(result.get("turn")) is dict, "invalid_receipt")
    turn = result["turn"]
    check(safe(turn.get("id")) and turn["id"] != binding["prior_turn_id"] and turn.get("status") in ("inProgress", "completed", "failed", "interrupted"), "invalid_receipt")
    receipt = {"version": 1, "turn_id": turn["id"], "status": turn["status"], "history_delivery_proven": False}
    case.write_new("receipt.json", receipt)
    return attempt, receipt


class Rpc:
    def __init__(self, binding, deadline_ns, *, owner_context_required=True):
        self.binding, self.deadline_ns = binding, deadline_ns
        self.owner_context_required = True
        self.next_id, self.frames, self.bytes, self.server_requests = 1, 0, 0, 0
        self.last_hash, self.last_request = None, None

    def timeout(self, maximum=READ_SECONDS):
        return min(maximum, time_left(self.deadline_ns))

    async def __aenter__(self):
        from websockets.asyncio.client import unix_connect
        logger = logging.Logger("g0-private-websocket", level=logging.CRITICAL + 1)
        logger.addHandler(logging.NullHandler())
        logger.propagate = False
        raw = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        raw.setblocking(False)
        try:
            connect_deadline_ns = min(self.deadline_ns, continuous_ns() + 3 * NS)
            timeout = time_left(connect_deadline_ns)
            await asyncio.wait_for(asyncio.get_running_loop().sock_connect(raw, self.binding["service"]["socket_path"]), timeout)
            time_left(connect_deadline_ns)
            # A preconnected socket disables websocket redirect reconnection.
            upgrade_deadline_ns = min(self.deadline_ns, continuous_ns() + 3 * NS)
            self.ws = await unix_connect(uri="ws://localhost/", sock=raw, compression=None, proxy=None, ping_interval=None,
                                         open_timeout=time_left(upgrade_deadline_ns), close_timeout=1, max_size=MAX_BYTES, max_queue=4, logger=logger)
            time_left(upgrade_deadline_ns)
        except BaseException:
            raw.close()
            raise
        return self

    async def __aexit__(self, *_):
        try:
            await asyncio.wait_for(self.ws.close(), 2)
        except Exception:
            pass

    async def receive(self, deadline_ns):
        timeout = time_left(deadline_ns)
        raw = await asyncio.wait_for(self.ws.recv(), timeout)
        check(type(raw) is str, "non_text_frame")
        encoded = raw.encode()
        self.frames += 1
        self.bytes += len(encoded)
        self.last_hash = digest(encoded)
        # asyncio's timer may pause during system sleep; late frames never qualify.
        time_left(deadline_ns)
        check(self.frames <= MAX_FRAMES and self.bytes <= 4 * 1024 * 1024, "frame_budget_exceeded")
        packet = decode(encoded)
        check(type(packet) is dict, "invalid_rpc_packet")
        return packet

    def notification(self, packet):
        if "method" not in packet:
            return False
        check(type(packet["method"]) is str and len(packet["method"]) <= 128, "invalid_rpc_packet")
        # Approval/user-input requests are left to the native TUI; never answered.
        if "id" in packet:
            self.server_requests += 1
        return True

    async def call(self, method, params, intent=None):
        require_owner_context(self.binding)
        request = make_request(self.next_id, method, params, self.binding, intent)
        self.next_id += 1
        self.last_request = request
        deadline_ns = min(self.deadline_ns, continuous_ns() + READ_SECONDS * NS)
        timeout = time_left(deadline_ns)
        await asyncio.wait_for(self.ws.send(canonical(request)), timeout)
        time_left(deadline_ns)
        while True:
            packet = await self.receive(deadline_ns)
            if self.notification(packet):
                continue
            check(type(packet.get("id")) is int and packet["id"] == request["id"], "rpc_response_id_mismatch")
            check(set(packet) in ({"id", "result"}, {"id", "error"}), "invalid_rpc_response")
            if "error" in packet:
                error = packet["error"]
                check(type(error) is dict and set(error) in ({"code", "message"}, {"code", "message", "data"}), "invalid_rpc_error")
                check(number(error["code"], -(2**63), 2**63 - 1) and type(error["message"]) is str, "invalid_rpc_error")
                try:
                    message = error["message"].encode("utf-8")
                except UnicodeEncodeError:
                    raise ProbeError("invalid_rpc_error") from None
                check(len(message) <= MAX_BYTES, "invalid_rpc_error")
                summary = {"method": request["method"], "request_id": request["id"], "code": error["code"],
                           "message_bytes": len(message), "message_sha256": digest(message)}
                time_left(deadline_ns)
                raise ProbeError("native_rpc_error", summary)
            time_left(deadline_ns)
            return packet["result"]

    async def wake(self):
        # thread/read doesn't subscribe. A bounded read fallback needs no resume.
        deadline_ns = min(self.deadline_ns, continuous_ns() + 2 * NS)
        while continuous_ns() < deadline_ns:
            try:
                packet = await self.receive(deadline_ns)
            except TimeoutError:
                return
            except ProbeError as error:
                if error.code == "read_window_expired":
                    return
                raise
            check(self.notification(packet), "unexpected_rpc_response")
            params = packet.get("params")
            if type(params) is dict and params.get("threadId") == self.binding["controller_thread_id"]:
                return

    async def initialize(self):
        params = {"clientInfo": {"name": "g0-native-standalone", "version": "0.1.0"}, "capabilities": {"experimentalApi": True}}
        result = await self.call("initialize", params)
        check(type(result) is dict and result.get("platformOs") == "macos" and result.get("platformFamily") == "unix"
              and type(result.get("userAgent")) is str and type(result.get("codexHome")) is str, "initialize_mismatch")
        validate_initialize_codex_home(result, self.binding["owner_context"])
        timeout = self.timeout()
        await asyncio.wait_for(self.ws.send('{"method":"initialized"}'), timeout)
        self.timeout()
        result = await self.call("server/diagnostics", {})
        check(type(result) is dict and type(result.get("process")) is dict and type(result["process"].get("id")) is int
              and result["process"]["id"] == self.binding["service"]["pid"], "diagnostics_pid_mismatch")
        result = await self.call("remoteControl/status/read", {})
        check(type(result) is dict and result.get("status") in ("disabled", "connecting", "connected", "errored"), "invalid_remote_status")
        self.checks = {"diagnostics_pid": self.binding["service"]["pid"], "remote_control_status": result["status"],
                       "native_binary_pinned": True, "machine_binding_checked": True,
                       "source_service_verified": False, "current_display_verified": False}


def go_read(binding, verb, deadline_ns, allow_ack=False, *, owner_context_required=True):
    require_owner_context(binding)
    check(verb in ("read", "inspect"), "local_command_not_allowed")
    remaining = time_left(deadline_ns)
    value = decode(command_output([binding["go_binary"], verb, "--dir", binding["job_dir"], "--nonce", binding["nonce"]], min(READ_SECONDS, remaining)))
    time_left(deadline_ns)
    if verb == "inspect":
        return validate_go(value, binding, allow_ack)
    expected = {"version": 1, "status": "completed", "nonce": binding["nonce"], "count": 1}
    check(canonical(value) == canonical(expected), "producer_not_completed")
    return value


def load_prepared(case, binding, binding_hash):
    producer, intent, meta = case.read("producer.json"), case.read("intent.json"), case.read("prepare-meta.json")
    exact(producer, ("read", "inspection"))
    check(canonical(producer["read"]) == canonical(producer["inspection"]["result"]), "producer_record_mismatch")
    check(canonical(intent) == canonical(make_intent(binding, producer["inspection"])), "intent_binding_mismatch")
    check(type(meta) is dict and meta.get("binding_sha256") == binding_hash and meta.get("payload_sha256") == digest(canonical(intent).encode()), "prepared_binding_mismatch")
    check(meta.get("clock_impl") == CLOCK_IMPL and meta.get("boot_id") == binding["boot_id"], "clock_context_changed")
    check(is_uuid(meta.get("session_id")), "invalid_session_id")
    return intent, meta["session_id"]


def load_attempt(case, binding, intent, binding_hash):
    value = case.read("send-attempt.json")
    exact(value, ("version", "status", "nonce", "controller_thread_id", "binding_sha256", "payload_sha256", "boot_id", "clock_impl", "mono_ns", "wall_ns"))
    check(type(value["version"]) is int and value["version"] == 1 and value["status"] == "send_intent", "invalid_send_intent")
    check(value["nonce"] == binding["nonce"] and value["controller_thread_id"] == binding["controller_thread_id"] and value["binding_sha256"] == binding_hash
          and value["payload_sha256"] == digest(canonical(intent).encode()) and value["boot_id"] == binding["boot_id"], "send_intent_binding_mismatch")
    remaining_window(value, current_clock())
    return value


def exists(case, name):
    check(name in FILES, "invalid_file")
    try:
        os.stat(name, dir_fd=case.fd, follow_symlinks=False)
        return True
    except FileNotFoundError:
        return False


async def observe(rpc, case, binding, intent, attempt, expected_session_id, *, owner_context_required=True):
    require_owner_context(binding)
    last_read, facts, inspection, captured_ack = None, {}, None, None
    rpc_error = None
    reason = "model_window_expired"
    try:
        for _ in range(64):
            remaining_window(attempt, current_clock())
            rpc.deadline_ns = attempt["mono_ns"] + MODEL_SECONDS * NS
            verify_machine(binding, hashes=False)
            raw = await rpc.call("thread/read", {"threadId": binding["controller_thread_id"], "includeTurns": True})
            filtered, facts = filter_thread(raw, binding, intent, expected_session_id=expected_session_id)
            last_read = {"request": rpc.last_request, "response": {"id": rpc.last_request["id"], "result": filtered}}
            inspection = go_read(binding, "inspect", rpc.deadline_ns, allow_ack=True, owner_context_required=owner_context_required)
            check(inspection["event_hash"] == intent["events"][0]["payload_hash"], "producer_changed")
            ack = inspection["ack"]
            if ack is not None:
                # Keep the verified projection for the final audit input;
                # an early poll with no ACK contributes no business record.
                captured_ack = audit_ack(intent, inspection)
            if all(facts[key] for key in ("history_match", "new_turn_completed", "ack_final_marker", "root_idle", "all_tools_and_turns_terminal", "matching_turn_is_last")) and ack is not None and ack["decision"] == "handled" and ack["effect_count"] == 1:
                verify_machine(binding, hashes=False)
                remaining_window(attempt, current_clock())
                reason = None
                break
            await rpc.wake()
        else:
            reason = "read_count_exceeded"
    except asyncio.CancelledError:
        reason = "interrupted"
    except Exception as error:
        reason = error.code if isinstance(error, ProbeError) else "observation_io_failed"
        if isinstance(error, ProbeError) and error.code == "native_rpc_error":
            rpc_error = error.rpc_error
    output = {"version": 1, "scope": "one_bound_native_root", "status": "observed" if reason is None else "uncertain", "reason": reason,
              "source_service_verified": False, "current_display_verified": False, "controller_evidence_sha256": binding["controller_evidence_sha256"],
              "facts": facts, "go_inspection": inspection, "rpc_checks": rpc.checks,
              "frames": rpc.frames, "bytes": rpc.bytes, "server_requests_seen": rpc.server_requests, "last_frame_sha256": rpc.last_hash,
              "absence_proof": False, "resend_authorized": False}
    if rpc_error is not None:
        output["rpc_error"] = rpc_error
    if last_read is not None and not exists(case, "audit-input.json"):
        case.write_new("audit-input.json", {"version": 1, "intent": intent, "reads": [last_read],
                                             "acks": [captured_ack] if captured_ack is not None else []})
    return output


async def run(verb, case, binding, binding_hash, *, owner_context_required=True):
    check(sys.platform == "darwin" and importlib.metadata.version("websockets") == "16.0", "unsupported_runtime")
    if verb == "send-once":
        check(not exists(case, "send-attempt.json"), "send_intent_exists")
    require_owner_context(binding)
    deadline_ns = continuous_ns() + READ_SECONDS * NS
    verify_machine(binding)
    intent, attempt, session_id = None, None, None
    if verb != "prepare":
        intent, session_id = load_prepared(case, binding, binding_hash)
    if verb == "observe":
        attempt = load_attempt(case, binding, intent, binding_hash)
        deadline_ns = min(deadline_ns, attempt["mono_ns"] + MODEL_SECONDS * NS)
    rpc = Rpc(binding, deadline_ns, owner_context_required=True)
    async with rpc as rpc:
        await rpc.initialize()
        if verb == "observe":
            result = await observe(rpc, case, binding, intent, attempt, session_id, owner_context_required=owner_context_required)
            case.write_new("read-observation.json", result)
            return result
        result = await rpc.call("thread/read", {"threadId": binding["controller_thread_id"], "includeTurns": True})
        _, facts = filter_thread(result, binding, None, require_ready=True, expected_session_id=session_id)
        if verb == "prepare":
            read = go_read(binding, "read", deadline_ns, owner_context_required=owner_context_required)
            inspection = go_read(binding, "inspect", deadline_ns, owner_context_required=owner_context_required)
            check(canonical(read) == canonical(inspection["result"]), "producer_record_mismatch")
            intent = make_intent(binding, inspection)
            case.write_new("producer.json", {"read": read, "inspection": inspection})
            case.write_new("intent.json", intent)
            result = {"version": 1, "status": "prepared", "binding_sha256": binding_hash, "payload_sha256": digest(canonical(intent).encode()),
                      "session_id": facts["session_id"], "prior_facts": facts, "rpc_checks": rpc.checks, **current_clock()}
            case.write_new("prepare-meta.json", result)
            time_left(deadline_ns)
            return result
        inspection = go_read(binding, "inspect", deadline_ns, owner_context_required=owner_context_required)
        check(canonical(intent) == canonical(make_intent(binding, inspection)), "producer_changed")
        attempt, _ = await issue_once(rpc, case, binding, intent, binding_hash, owner_context_required=owner_context_required)
        result = await observe(rpc, case, binding, intent, attempt, session_id, owner_context_required=owner_context_required)
        case.write_new("send-observation.json", result)
        return result


def main(args):
    check(len(args) == 7 and args[0] in ("prepare", "send-once", "observe") and args[1::2] == ["--case", "--root", "--nonce"], "usage")
    verb, directory, root, nonce = args[0], args[2], args[4], args[6]
    with Case(directory) as case:
        binding = validate_binding(case.read("binding.json"), root, nonce)
        check(digest(case.read_raw("controller-evidence.json")) == binding["controller_evidence_sha256"], "controller_evidence_changed")
        try:
            return asyncio.run(run(verb, case, binding, digest(canonical(binding).encode()), owner_context_required=True))
        except (Exception, KeyboardInterrupt) as error:
            result = {"version": 1, "status": "uncertain" if exists(case, "send-attempt.json") else "rejected",
                      "reason": error.code if isinstance(error, ProbeError) else "operation_io_failed",
                      "source_service_verified": False, "resend_authorized": False, "absence_proof": False}
            if isinstance(error, ProbeError) and error.code == "native_rpc_error" and error.rpc_error is not None:
                result["rpc_error"] = error.rpc_error
            name = "read-observation.json" if verb == "observe" else "send-observation.json"
            if exists(case, "send-attempt.json") and not exists(case, name):
                case.write_new(name, result)
            return result


if __name__ == "__main__":
    try:
        output = main(sys.argv[1:])
    except (Exception, KeyboardInterrupt) as error:
        output = {"version": 1, "status": "rejected", "reason": error.code if isinstance(error, ProbeError) else "invalid_local_input",
                  "source_service_verified": False, "resend_authorized": False}
    code = 0 if output["status"] in ("prepared", "observed") else 3 if output["status"] == "uncertain" else 2
    payload = (canonical(output) + "\n").encode()
    # Durable artifacts are authoritative even if the caller stops reading stdio.
    def emit():
        try:
            os.write(1, payload)
        except OSError:
            pass
    writer = threading.Thread(target=emit, daemon=True)
    writer.start()
    writer.join(0.25)
    os._exit(code)
