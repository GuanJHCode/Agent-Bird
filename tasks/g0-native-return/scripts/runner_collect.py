#!/usr/bin/env python3
"""Collect bounded, non-reasoning evidence from an explicitly owned CLI thread.

This is a one-off G0 experiment observer, not a product delivery mechanism.
It never writes Codex state or sends a model turn. Direct children must name the
provided root in SQLite's native source metadata before their files are opened.
"""

import argparse
import datetime
import json
import pathlib
import re
import sqlite3
import subprocess


def extract_text(content):
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return "".join(item.get("text", "") for item in content if isinstance(item, dict))
    return ""


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
                           ("type", "role", "channel", "phase", "name", "call_id", "status", "author", "recipient")
                           if key in payload})
            if item_type in ("message", "agent_message"):
                text = extract_text(payload.get("content"))
                if nonce in text:
                    result["text"] = text[:8192]
            elif item_type in ("function_call_output", "custom_tool_call_output"):
                result["output"] = extract_text(payload.get("output"))[:8192]
            elif item_type == "custom_tool_call":
                result["input"] = payload.get("input", "")[:8192]
            elif item_type == "function_call":
                try:
                    args = json.loads(payload.get("arguments", "{}"))
                except json.JSONDecodeError:
                    args = {}
                # The message may be encrypted. Preserve identity/operation,
                # never ciphertext or inherited instructions.
                result["arguments_metadata"] = {
                    key: args[key] for key in
                    ("task_name", "agent_type", "fork_turns", "target", "agent_id", "id", "interrupt")
                    if key in args
                }
        elif kind == "event_msg" and payload.get("type") in (
                "task_started", "task_complete", "turn_aborted", "error"):
            result.update({key: payload[key] for key in
                           ("type", "turn_id", "started_at", "completed_at", "duration_ms")
                           if key in payload})
            text = payload.get("last_agent_message", "")
            if nonce in text:
                result["last_agent_message"] = text[:8192]
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
            hooks.append({"file": path.name, **record})
    process = subprocess.run(["ps", "-p", str(args.pid), "-o", "pid,ppid,lstart,etime,comm"],
                             text=True, capture_output=True)
    output = {
        "captured_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "root_thread": args.root_thread,
        "nonce": args.nonce,
        "window": {"since": args.since, "through": args.through},
        "selection": "Exact root UUID and native source.parent_thread_id direct children only",
        "privacy": "Reasoning omitted; message text only contains this synthetic nonce; no account/config/environment reads",
        "process_observation": {"exit_code": process.returncode, "stdout": process.stdout},
        "hooks": sorted(hooks, key=lambda item: item["recorded_at"]),
        "threads": selected,
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(output, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps({"output": str(args.output), "threads": len(selected),
                      "events": sum(len(row["events"]) for row in selected), "hooks": len(hooks)}))


if __name__ == "__main__":
    main()
