"""One bounded native standalone-output sample; no generic attachment authority."""
import asyncio
import fcntl
import hashlib
import importlib.metadata
import json
import logging
import os
import pathlib
import re
import socket
import stat
import subprocess
import sys
import threading
import time
import uuid

from delivery_audit import canonical, envelope, parse

CLI_VERSION = "0.153.4"
CLI_SHA = "b973d440acac501fd2594a43e7ca9ce41e0a65b9dfb28d0d7a7837c99e1261e3"
GO_SHA = "feb9b9ad969abca5ff35db28c0f0d8d920dc0778d7aac68a90952900e989a285"
READ_SECONDS, MODEL_SECONDS, MAX_BYTES, MAX_FRAMES = 10, 120, 65536, 256
SAFE = re.compile(r"[A-Za-z0-9_-]{1,64}\Z")
HASH = re.compile(r"[0-9a-f]{64}\Z")
FILES = {"binding.json", "controller-evidence.json", "producer.json", "intent.json", "prepare-meta.json", "send-attempt.json",
         "receipt.json", "send-observation.json", "read-observation.json", "audit-input.json"}


class ProbeError(Exception):
    def __init__(self, code):
        self.code = code
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
    exact(value, ("version", "nonce", "controller_thread_id", "controller_epoch", "expected_cwd", "prior_turn_id",
                  "native_cli_version", "boot_id", "service", "tui", "go_binary", "go_binary_sha256", "job_dir", "controller_evidence_sha256"))
    check(type(value["version"]) is int and value["version"] == 1)
    check(safe(root) and safe(nonce) and len(nonce) <= 40 and value["nonce"] == nonce and value["controller_thread_id"] == root, "root_nonce_mismatch")
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
    exact(service, ("pid", "uid", "birth", "comm", "socket_path", "socket_dev", "socket_ino", "native_binary_sha256"))
    check(number(service["uid"], 0) and service["uid"] == os.getuid(), "owner_mismatch")
    check(all(number(service[key]) for key in ("pid", "socket_dev", "socket_ino")))
    check(type(service["birth"]) is str and 0 < len(service["birth"]) <= 64)
    check(absolute(service["comm"]) and absolute(service["socket_path"]))
    check(service["native_binary_sha256"] == CLI_SHA, "version_mismatch")
    tui = value["tui"]
    exact(tui, ("pid", "uid", "birth", "comm", "native_binary_sha256"))
    check(number(tui["pid"]) and tui["pid"] != service["pid"] and number(tui["uid"], 0) and tui["uid"] == os.getuid(), "owner_mismatch")
    check(type(tui["birth"]) is str and 0 < len(tui["birth"]) <= 64 and absolute(tui["comm"]))
    check(tui["native_binary_sha256"] == CLI_SHA, "version_mismatch")
    return value


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
    try:
        result = subprocess.run(args, capture_output=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        raise ProbeError("local_read_timeout") from None
    check(len(result.stdout) <= MAX_BYTES and len(result.stderr) <= MAX_BYTES, "local_output_limit")
    check(result.returncode == 0, "local_command_failed")
    return result.stdout


def current_clock():
    boot = command_output(["/usr/sbin/sysctl", "-n", "kern.bootsessionuuid"], 2).decode("ascii").strip().lower()
    return {"boot_id": boot, "clock_impl": time.get_clock_info("monotonic").implementation,
            "mono_ns": time.monotonic_ns(), "wall_ns": time.time_ns()}


def remaining_window(attempt, now):
    check(attempt["boot_id"] == now["boot_id"] and attempt["clock_impl"] == now["clock_impl"], "clock_context_changed")
    check(all(number(value[key], 0) for value in (attempt, now) for key in ("mono_ns", "wall_ns")), "invalid_clock")
    elapsed = now["mono_ns"] - attempt["mono_ns"]
    wall_elapsed = now["wall_ns"] - attempt["wall_ns"]
    check(elapsed >= 0 and abs(elapsed - wall_elapsed) <= 2_000_000_000, "clock_inconsistent")
    check(elapsed < MODEL_SECONDS * 1_000_000_000, "model_window_expired")
    return MODEL_SECONDS - elapsed / 1_000_000_000


def verify_machine(binding, hashes=True):
    service = binding["service"]
    for role in ("service", "tui"):
        process = binding[role]
        row = command_output(["/bin/ps", "-p", str(process["pid"]), "-o", "pid=,uid=,lstart=,comm="], 2).decode().strip().split(None, 7)
        check(len(row) == 8 and row[0] == str(process["pid"]) and row[1] == str(process["uid"]) and " ".join(row[2:7]) == process["birth"] and row[7] == process["comm"], role + "_identity_changed")
    info = os.lstat(service["socket_path"])
    check(stat.S_ISSOCK(info.st_mode) and info.st_uid == os.getuid() and (info.st_dev, info.st_ino) == (service["socket_dev"], service["socket_ino"]), "socket_identity_changed")
    check(current_clock()["boot_id"] == binding["boot_id"], "boot_changed")
    if hashes:
        for path, expected in {(service["comm"], CLI_SHA), (binding["tui"]["comm"], CLI_SHA), (binding["go_binary"], GO_SHA)}:
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


def filter_thread(result, binding, intent, require_ready=False):
    check(type(result) is dict and type(result.get("thread")) is dict, "invalid_thread")
    thread = result["thread"]
    check(thread.get("id") == binding["controller_thread_id"] and thread.get("source") == "cli" and thread.get("cliVersion") == CLI_VERSION and thread.get("cwd") == binding["expected_cwd"], "thread_binding_mismatch")
    check(thread.get("historyMode") == "legacy" and thread.get("ephemeral") is False, "unsupported_history")
    check(type(thread.get("status")) is dict and thread["status"].get("type") in ("idle", "active", "notLoaded", "systemError"), "invalid_thread_status")
    check(type(thread.get("turns")) is list and 0 < len(thread["turns"]) <= 32, "history_limit")
    ready_marker = binding["nonce"].upper().replace("-", "_") + "_READY"
    ack_marker = binding["nonce"].upper().replace("-", "_") + "_ACK"
    facts = {"history_match": False, "new_turn_completed": False, "ack_final_marker": False, "prior_tools_terminal": False,
             "prior_started_at": None, "prior_completed_at": None}
    filtered = {"thread": {"id": thread["id"], "historyMode": thread["historyMode"], "turns": []}}
    identities = {}
    for turn in thread["turns"]:
        check(type(turn) is dict and safe(turn.get("id")) and turn.get("status") in ("inProgress", "completed", "failed", "interrupted"), "invalid_turn")
        check(turn.get("itemsView") == "full" and type(turn.get("items")) is list and len(turn["items"]) <= 128, "incomplete_history")
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
        filtered["thread"]["turns"].append(output_turn)
    if require_ready:
        check(thread["status"]["type"] == "idle" and thread["turns"][-1]["id"] == binding["prior_turn_id"] and facts["prior_tools_terminal"], "thread_not_idle")
    return filtered, facts


async def issue_once(rpc, case, binding, intent, binding_hash):
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
    check(type(result) is dict and type(result.get("turn")) is dict, "invalid_receipt")
    turn = result["turn"]
    check(safe(turn.get("id")) and turn["id"] != binding["prior_turn_id"] and turn.get("status") in ("inProgress", "completed", "failed", "interrupted"), "invalid_receipt")
    receipt = {"version": 1, "turn_id": turn["id"], "status": turn["status"], "history_delivery_proven": False}
    case.write_new("receipt.json", receipt)
    return attempt, receipt
