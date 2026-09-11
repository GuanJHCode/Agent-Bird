#!/usr/bin/env python3
"""Collect bounded, non-reasoning evidence from an explicitly owned CLI thread.

This is a one-off G0 experiment observer, not a product delivery mechanism.
It never writes Codex state or sends a model turn. Direct children must name the
provided root in SQLite's native source metadata before their files are opened.
"""

import argparse
import datetime
import hashlib
import json
import pathlib
import re
import shlex
import sqlite3
import subprocess


def extract_text(content):
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return "".join(item.get("text", "") for item in content if isinstance(item, dict))
    return ""


def markers(text, nonce):
    allowed = ("WAITING", "DONE", "ACK", "RECEIVED", "NO-MESSAGE", "UNAVAILABLE", "LOCAL", "STEERED")
    return sorted({nonce + "-" + suffix for suffix in allowed
                   if re.search(re.escape(nonce + "-" + suffix) + r"(?![A-Za-z0-9_-])", text)})


def summarize_text(text, nonce):
    return {"utf8_bytes": len(text.encode()), "sha256": hashlib.sha256(text.encode()).hexdigest(),
            "markers": markers(text, nonce)}


def safe_input(text, nonce):
    result = summarize_text(text, nonce)
    result["operations"] = sorted(set(re.findall(
        r"tools\.(exec_command|write_stdin|clock__curr_time|get_goal|create_goal|update_goal)\s*\(", text)))
    update = re.search(r'tools\.update_goal\(\s*\{\s*(?:"status"|status)\s*:\s*"(complete|blocked)"\s*\}\s*\)', text)
    if update:
        result["goal_status_argument"] = update[1]
    commands = []
    host_commands = []
    for literal in re.findall(r'\bcmd\s*:\s*("(?:[^"\\]|\\.)*")', text):
        try:
            command = json.loads(literal)
            # These exact experiment directories and this executable suffix
            # are the only Host commands whose operation metadata is exported.
            match = re.fullmatch(r"nr-g0-(goal|offline)-([0-9]{2})", nonce)
            if match:
                expected_dir = ("/Users/guanjunhui/code/ai/codex-cli-orchestration-design/"
                                "tasks/g0-macos/data/test-repo/tasks/native-return/data/runner-host-"
                                + match[1] + "-" + match[2])
                for segment in command.split("&&"):
                    words = shlex.split(segment)
                    if len(words) not in (6, 8) or not words[0].endswith("/tools/g0-return-lab/data/g0-return-lab"):
                        continue
                    if words[1] not in ("start", "read", "wait") or words[2:6] != ["--dir", expected_dir, "--nonce", nonce]:
                        continue
                    record = {"verb": words[1], "nonce": nonce, "experiment_dir_matches": True}
                    if words[1] == "read" and len(words) == 6:
                        host_commands.append(record)
                    elif len(words) == 8 and words[6] == {"start": "--delay", "wait": "--timeout"}.get(words[1]):
                        if re.fullmatch(r"[0-9]{1,3}s", words[7]):
                            record["duration"] = words[7]
                            host_commands.append(record)
            parts = command.split(";")
            if len(parts) != 2 or not re.fullmatch(r"sleep [0-9]{1,2}", parts[0].strip()):
                continue
            words = shlex.split(parts[1])
            if (len(words) == 2 and words[0] == "echo" and words[1] in markers(words[1], nonce)) or (
                    len(words) == 3 and words[0] == "printf" and words[1] in ("%s\\n", "%s\\\\n")
                    and words[2] in markers(words[2], nonce)):
                commands.append(command)
        except (json.JSONDecodeError, ValueError):
            continue
    if commands:
        result["synthetic_commands"] = commands
    if host_commands:
        result["host_commands"] = host_commands
    return result


def json_objects(text):
    decoder = json.JSONDecoder()
    for line in text.splitlines():
        remainder = line.strip()
        while remainder.startswith("{"):
            try:
                value, end = decoder.raw_decode(remainder)
            except json.JSONDecodeError:
                break
            if isinstance(value, dict):
                yield value
            remainder = remainder[end:].strip()


def safe_output(text, nonce):
    result = summarize_text(text, nonce)
    if re.fullmatch(r"Wall time: [0-9.]+ seconds\nSleep interrupted by new input\.", text):
        result["native_sleep_response"] = text
    elif re.fullmatch(r"Wall time: [0-9.]+ seconds", text):
        result["native_sleep_response"] = text
    runtime = []
    for value in json_objects(text):
        if set(value) == {"task_name"} and re.fullmatch(r"/root/[A-Za-z0-9_/-]{1,100}", value["task_name"]):
            result["native_spawn_response"] = value
        numbers = {key: value[key] for key in ("exit_code", "session_id", "wall_time_seconds")
                   if key in value and isinstance(value[key], (int, float))}
        if numbers:
            runtime.append(numbers)
        goal = value.get("goal")
        if isinstance(goal, dict) and nonce in str(goal.get("objective", "")):
            safe_goal = {}
            if isinstance(goal.get("threadId"), str) and re.fullmatch(r"[0-9a-f-]{36}", goal["threadId"]):
                safe_goal["thread_id"] = goal["threadId"]
            if goal.get("status") in ("active", "complete", "paused", "blocked", "budget_exhausted"):
                safe_goal["status"] = goal["status"]
            for key in ("tokensUsed", "timeUsedSeconds", "createdAt", "updatedAt"):
                if isinstance(goal.get(key), (int, float)):
                    safe_goal[key] = goal[key]
            result["native_goal"] = safe_goal
        # Only the fixed, versioned synthetic Host result may be copied whole.
        if (set(value) == {"version", "status", "nonce", "count"}
                and value["version"] == 1 and value["nonce"] == nonce
                and value["status"] in ("pending", "completed") and value["count"] in (0, 1)):
            result["synthetic_host_result"] = value
        nested = value.get("output")
        if isinstance(nested, str):
            result["markers"] = sorted(set(result["markers"] + markers(nested, nonce)))
            for obj in json_objects(nested):
                if (isinstance(obj, dict) and set(obj) == {"version", "status", "nonce", "count"}
                        and obj["version"] == 1 and obj["nonce"] == nonce
                        and obj["status"] in ("pending", "completed") and obj["count"] in (0, 1)):
                    result["synthetic_host_result"] = obj
    if runtime:
        result["runtime"] = runtime
    return result


def collect_events(path, nonce, since, through):
    rows = []
    lower = datetime.datetime.fromisoformat(since.replace("Z", "+00:00"))
    upper = datetime.datetime.fromisoformat(through.replace("Z", "+00:00"))
    for line in path.open():
        event = json.loads(line)
        timestamp = event.get("timestamp", "")
        instant = datetime.datetime.fromisoformat(timestamp.replace("Z", "+00:00"))
        if instant < lower or instant > upper:
            continue
        payload = event.get("payload", {})
        kind = event.get("type")
        result = {"timestamp": timestamp, "record_type": kind}
        if kind == "response_item":
            item_type = payload.get("type")
            if item_type == "reasoning":
                continue
            result.update({key: payload[key] for key in
                           ("type", "role", "channel", "phase", "name", "namespace", "call_id", "status", "author", "recipient")
                           if key in payload})
            if item_type in ("message", "agent_message"):
                text = extract_text(payload.get("content"))
                if payload.get("role") == "user" and text.startswith('<codex_internal_context source="goal">'):
                    result["control_kind"] = "native_goal_continuation"
                if nonce in text:
                    result["message_summary"] = summarize_text(text, nonce)
                    if text in markers(text, nonce):
                        result["text"] = text
                    elif item_type == "agent_message" and re.fullmatch(
                            r"Message Type: FINAL_ANSWER\nTask name: /root\nSender: /root/[A-Za-z0-9_/-]+\nPayload:\n"
                            + re.escape(nonce) + r"-(?:DONE|ACK|WAITING|RECEIVED)\n?", text):
                        result["text"] = text
            elif item_type in ("function_call_output", "custom_tool_call_output"):
                result["output_summary"] = safe_output(extract_text(payload.get("output")), nonce)
            elif item_type == "custom_tool_call":
                result["input_summary"] = safe_input(payload.get("input", ""), nonce)
            elif item_type == "function_call":
                try:
                    args = json.loads(payload.get("arguments", "{}"))
                except json.JSONDecodeError:
                    args = {}
                # The message may be encrypted. Preserve identity/operation,
                # never ciphertext or inherited instructions.
                allowed = {}
                if payload.get("namespace") == "collaboration" and payload.get("name") == "spawn_agent":
                    if isinstance(args.get("task_name"), str) and re.fullmatch(r"[a-z0-9_]{1,100}", args["task_name"]):
                        allowed["task_name"] = args["task_name"]
                    if args.get("agent_type") == "default":
                        allowed["agent_type"] = "default"
                    if args.get("fork_turns") in ("none", "all"):
                        allowed["fork_turns"] = args["fork_turns"]
                elif payload.get("namespace") == "clock" and payload.get("name") == "sleep":
                    duration = args.get("duration_ms")
                    if isinstance(duration, int) and not isinstance(duration, bool) and 1 <= duration <= 43200000:
                        allowed["duration_ms"] = duration
                result["arguments_metadata"] = allowed
        elif kind == "event_msg" and payload.get("type") in (
                "task_started", "task_complete", "turn_aborted", "error"):
            result.update({key: payload[key] for key in
                           ("type", "turn_id", "started_at", "completed_at", "duration_ms")
                           if key in payload})
            text = payload.get("last_agent_message", "")
            if text in markers(text, nonce):
                result["last_agent_message"] = text
        elif kind == "turn_context":
            result.update({key: payload[key] for key in
                           ("turn_id", "model", "approval_policy", "sandbox_policy")
                           if key in payload})
        else:
            continue
        rows.append(result)
    return rows


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root-thread", required=True)
    parser.add_argument("--nonce", required=True)
    parser.add_argument("--pid", type=int, required=True)
    parser.add_argument("--since", required=True)
    parser.add_argument("--through", required=True)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    parser.add_argument("--codex-dir", type=pathlib.Path,
                        default=pathlib.Path("/Users/guanjunhui/.codex"))
    parser.add_argument("--experiment-cwd", type=pathlib.Path, default=pathlib.Path(
        "/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/test-repo"))
    parser.add_argument("--hook-dir", type=pathlib.Path, default=pathlib.Path(
        "/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/hook-events"))
    args = parser.parse_args()
    if not re.fullmatch(r"[0-9a-f-]{36}", args.root_thread):
        parser.error("root-thread must be an explicit native UUID")
    if not re.fullmatch(r"[a-zA-Z0-9_-]{1,80}", args.nonce):
        parser.error("nonce must be a bounded synthetic marker")
    connection = sqlite3.connect(f"file:{args.codex_dir / 'state_5.sqlite'}?mode=ro", uri=True)
    connection.row_factory = sqlite3.Row
    columns = "id,rollout_path,cwd,created_at_ms,source,cli_version,agent_path"
    root = connection.execute(f"SELECT {columns} FROM threads WHERE id=?", (args.root_thread,)).fetchone()
    if root is None:
        raise SystemExit("Explicit root thread is not persisted yet")
    if root["source"] != "cli":
        raise SystemExit("Explicit root must have native CLI source")
    selected = [dict(root)]
    window_end_ms = int(datetime.datetime.fromisoformat(args.through.replace("Z", "+00:00")).timestamp() * 1000)
    candidates = connection.execute(
        f"SELECT {columns} FROM threads WHERE source LIKE ?", (f"%{args.root_thread}%",))
    for row in candidates:
        try:
            source = json.loads(row["source"])
        except (json.JSONDecodeError, TypeError):
            continue
        if (source.get("subagent", {}).get("thread_spawn", {}).get("parent_thread_id") == args.root_thread
                and row["created_at_ms"] <= window_end_ms):
            selected.append(dict(row))
    ids = {row["id"] for row in selected}
    for row in selected:
        if pathlib.Path(row.pop("cwd")).resolve() != args.experiment_cwd.resolve():
            raise SystemExit("Refusing thread outside the explicitly selected experiment cwd")
        row["experiment_cwd_matches"] = True
        source = row.pop("source")
        if source == "cli":
            row["source_kind"] = "cli"
        else:
            spawn = json.loads(source)["subagent"]["thread_spawn"]
            row["source_kind"] = "subagent"
            row["parent_thread_id"] = spawn["parent_thread_id"]
        path = pathlib.Path(row.pop("rollout_path"))
        if args.codex_dir / "sessions" not in path.parents:
            raise SystemExit("Refusing rollout outside native sessions directory")
        row["events"] = collect_events(path, args.nonce, args.since, args.through)
    hooks = []
    lower = datetime.datetime.fromisoformat(args.since.replace("Z", "+00:00"))
    upper = datetime.datetime.fromisoformat(args.through.replace("Z", "+00:00"))
    for path in args.hook_dir.glob("*.json"):
        record = json.loads(path.read_text())
        recorded = datetime.datetime.fromisoformat(record["recorded_at"].replace("Z", "+00:00"))
        if record.get("session_id") in ids and lower <= recorded <= upper:
            hook = {"file": path.name, **{key: record[key] for key in
                    ("hook_event_name", "nonce", "pid", "ppid", "recorded_at", "session_id", "turn_id")
                    if key in record}}
            if record.get("source") in ("startup", "resume", "clear", "compact"):
                hook["source"] = record["source"]
            hooks.append(hook)
    process = subprocess.run(["ps", "-p", str(args.pid), "-o", "pid,ppid,lstart,etime,comm"],
                             text=True, capture_output=True)
    output = {
        "captured_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "root_thread": args.root_thread,
        "nonce": args.nonce,
        "window": {"since": args.since, "through": args.through},
        "selection": "Exact root UUID and native source.parent_thread_id direct children only",
        "privacy": "Reasoning omitted; text limited to fixed nonce messages and native wrappers; tool data is typed fields, fixed responses or hashes; explicit Hook keys only",
        "process_observation": {"requested_pid": args.pid, "exit_code": process.returncode, "stdout": process.stdout},
        "hooks": sorted(hooks, key=lambda item: item["recorded_at"]),
        "threads": selected,
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(output, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps({"output": str(args.output), "threads": len(selected),
                      "events": sum(len(row["events"]) for row in selected), "hooks": len(hooks)}))


if __name__ == "__main__":
    main()
