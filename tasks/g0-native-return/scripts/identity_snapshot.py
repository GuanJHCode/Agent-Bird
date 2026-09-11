#!/usr/bin/env python3
"""One-off typed supplement for explicitly owned native identity experiments.

Read-only against Codex state. It exports no reasoning, prompt, objective, or
arbitrary tool body. The normal runner collector retains the paired call hashes.
"""

import argparse
import datetime
import json
import pathlib
import re
import sqlite3

from runner_collect import extract_text, json_objects, safe_input


UUID = re.compile(r"[0-9a-f-]{36}\Z")
CODEX = pathlib.Path("/Users/guanjunhui/.codex")
CWD = pathlib.Path("/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/test-repo")


def instant(value):
    return datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root-thread", required=True)
    parser.add_argument("--direct-child")
    parser.add_argument("--nonce", required=True)
    parser.add_argument("--since", required=True)
    parser.add_argument("--through", required=True)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    args = parser.parse_args()
    if not UUID.fullmatch(args.root_thread):
        parser.error("root-thread must be an explicitly owned UUID")
    if args.direct_child is not None and not UUID.fullmatch(args.direct_child):
        parser.error("direct-child must be an explicitly owned UUID")
    if not re.fullmatch(r"nr-g0-identity-[a-z0-9-]{1,50}", args.nonce):
        parser.error("nonce must identify this synthetic experiment")
    now = datetime.datetime.now(datetime.timezone.utc)
    lower, upper = instant(args.since), instant(args.through)
    if not lower <= upper <= now:
        parser.error("observation window must already have elapsed")
    conn = sqlite3.connect(f"file:{CODEX / 'state_5.sqlite'}?mode=ro", uri=True)
    conn.row_factory = sqlite3.Row
    row = conn.execute("SELECT id,source,cwd,rollout_path FROM threads WHERE id=?", (args.root_thread,)).fetchone()
    if row is None or row["source"] != "cli" or pathlib.Path(row["cwd"]).resolve() != CWD:
        raise SystemExit("Refusing a thread outside the explicitly selected native CLI experiment")
    selected_thread = args.root_thread
    if args.direct_child is not None:
        child = conn.execute("SELECT id,source,cwd,rollout_path FROM threads WHERE id=?", (args.direct_child,)).fetchone()
        try:
            source = json.loads(child["source"]) if child is not None else {}
        except (json.JSONDecodeError, TypeError):
            source = {}
        parent = source.get("subagent", {}).get("thread_spawn", {}).get("parent_thread_id")
        if child is None or parent != args.root_thread or pathlib.Path(child["cwd"]).resolve() != CWD:
            raise SystemExit("Refusing a thread not natively identified as this root's direct child")
        row, selected_thread = child, args.direct_child
    path = pathlib.Path(row["rollout_path"])
    if CODEX / "sessions" not in path.parents:
        raise SystemExit("Refusing a rollout outside native sessions")
    calls, observations = {}, []
    for line in path.open():
        event = json.loads(line)
        if not lower <= instant(event["timestamp"]) <= upper or event.get("type") != "response_item":
            continue
        payload = event.get("payload", {})
        call_id = payload.get("call_id")
        if payload.get("type") == "custom_tool_call":
            calls[call_id] = safe_input(payload.get("input", ""), args.nonce).get("operations", [])
            continue
        if payload.get("type") != "custom_tool_call_output" or call_id not in calls:
            continue
        operations = calls[call_id]
        result = {"timestamp": event["timestamp"], "call_id": call_id, "operations": operations}
        for value in json_objects(extract_text(payload.get("output"))):
            if any(operation in operations for operation in ("get_goal", "create_goal", "update_goal")) and "goal" in value:
                goal = value["goal"]
                if goal is None:
                    result.setdefault("goal_snapshots", []).append({"present": False})
                elif isinstance(goal, dict):
                    item = {"present": True, "objective_matches_nonce": args.nonce in str(goal.get("objective", ""))}
                    if item["objective_matches_nonce"]:
                        if isinstance(goal.get("threadId"), str) and UUID.fullmatch(goal["threadId"]):
                            item["thread_id"] = goal["threadId"]
                        if goal.get("status") in ("active", "complete", "paused", "blocked", "budget_exhausted"):
                            item["status"] = goal["status"]
                        for key in ("createdAt", "updatedAt"):
                            if isinstance(goal.get(key), (int, float)) and not isinstance(goal[key], bool):
                                item[key] = goal[key]
                    result.setdefault("goal_snapshots", []).append(item)
            if "exec_command" in operations:
                candidates = [value]
                if isinstance(value.get("output"), str):
                    candidates.extend(json_objects(value["output"]))
                for nested in candidates:
                    if set(nested) == {"CODEX_THREAD_ID", "CODEX_SESSION_ID"} and all(isinstance(v, str) and UUID.fullmatch(v) for v in nested.values()):
                        result.setdefault("native_environment_ids", []).append(nested)
        if "goal_snapshots" in result or "native_environment_ids" in result:
            observations.append(result)
    output = {"captured_at": now.isoformat(), "root_thread": args.root_thread, "selected_thread": selected_thread, "nonce": args.nonce,
              "window": {"since": args.since, "through": args.through},
              "privacy": "Only typed Goal presence/status/identity/timestamps and exactly two UUID environment fields from paired native tool output; no reasoning or arbitrary text.",
              "observations": observations}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(output, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps({"output": str(args.output), "observations": len(observations)}))


if __name__ == "__main__":
    main()
