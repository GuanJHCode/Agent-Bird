#!/usr/bin/env python3
"""One-off exact-objective evidence for two owned native Goal edit races.

No Codex writes. No reasoning, arbitrary tool bodies, or arbitrary objectives
are exported. Runtime/control fields and the two authored synthetic objectives
are allowlisted; the original queued cell must exactly match the requested code.
"""

import argparse
import datetime
import hashlib
import json
import pathlib
import re
import sqlite3

from runner_collect import extract_text, safe_input, summarize_text

SAMPLES = json.loads(r'''{"race1":{"root":"01a08e75-cae3-7043-832e-20ebf886aef7","old_objective":"nr-g0-identity-race-old","new_objective":"nr-g0-identity-race-new：请重新启动一次 60 秒等待后才完成；旧目标已经发起的等待不满足新目标。","since":"2026-09-11T03:23:37.756Z","through":"2026-09-11T03:25:37.756Z","expected_cell":"// @exec: {\"yield_time_ms\": 1000}\ntext(await tools.get_goal({}));\ntext(await tools.create_goal({objective:\"nr-g0-identity-race-old\"}));\ntext(await tools.get_goal({}));\ntext(\"nr-g0-identity-race-old-WAITING\");\nlet result=await tools.exec_command({cmd:\"sleep 60; printf '%s\\\\n' nr-g0-identity-race-old-DONE\",yield_time_ms:1000,max_output_tokens:200});\nif(result.session_id) result=await tools.write_stdin({session_id:result.session_id,chars:\"\",yield_time_ms:60000,max_output_tokens:200});\ntext(result);\nif(result.exit_code===0){text(await tools.get_goal({}));text(await tools.update_goal({status:\"complete\"}));text(await tools.get_goal({}));}"},"race2":{"root":"01a08e81-d7c6-7fa3-bfcd-7e8b80ef4d2e","old_objective":"nr-g0-identity-race2-old","new_objective":"nr-g0-identity-race2-new：请重新启动一次 60 秒等待后才完成；旧目标已经发起的等待不满足新目标。","since":"2026-09-11T03:28:15.176Z","through":"2026-09-11T03:30:15.176Z","expected_cell":"// @exec: {\"yield_time_ms\": 120000}\ntext(await tools.get_goal({}));\ntext(await tools.create_goal({objective:\"nr-g0-identity-race2-old\"}));\ntext(await tools.get_goal({}));\ntext(\"nr-g0-identity-race2-old-WAITING\");\nlet result=await tools.exec_command({cmd:\"sleep 60; printf '%s\\\\n' nr-g0-identity-race2-old-DONE\",yield_time_ms:1000,max_output_tokens:200});\nif(result.session_id) result=await tools.write_stdin({session_id:result.session_id,chars:\"\",yield_time_ms:60000,max_output_tokens:200});\ntext(result);\nif(result.exit_code===0){text(await tools.get_goal({}));text(await tools.update_goal({status:\"complete\"}));text(await tools.get_goal({}));}"}}''')
CODEX = pathlib.Path("/Users/guanjunhui/.codex")
CWD = pathlib.Path("/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/test-repo")


def instant(value):
    return datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))


def objects(text):
    """Decode complete JSON objects, including native pretty-printed output."""
    decoder = json.JSONDecoder()
    position = 0
    while True:
        start = text.find("{", position)
        if start < 0:
            return
        try:
            value, end = decoder.raw_decode(text[start:])
        except json.JSONDecodeError:
            position = start + 1
            continue
        if isinstance(value, dict):
            yield value
        position = start + end


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--sample", choices=sorted(SAMPLES), required=True)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    args = parser.parse_args()
    sample = SAMPLES[args.sample]
    lower, upper = instant(sample["since"]), instant(sample["through"])
    now = datetime.datetime.now(datetime.timezone.utc)
    if upper > now or (upper - lower).total_seconds() != 120:
        raise SystemExit("The fixed 120-second window must already have elapsed")
    conn = sqlite3.connect(f"file:{CODEX / 'state_5.sqlite'}?mode=ro", uri=True)
    conn.row_factory = sqlite3.Row
    row = conn.execute("SELECT id,source,cwd,rollout_path FROM threads WHERE id=?", (sample["root"],)).fetchone()
    if row is None or row["source"] != "cli" or pathlib.Path(row["cwd"]).resolve() != CWD:
        raise SystemExit("Refusing a thread outside this exact native CLI experiment")
    path = pathlib.Path(row["rollout_path"])
    if CODEX / "sessions" not in path.parents:
        raise SystemExit("Refusing a rollout outside native sessions")
    expected_hash = hashlib.sha256(sample["expected_cell"].strip().encode()).hexdigest()
    events = []
    matched = []
    for line in path.open():
        event = json.loads(line)
        if not lower <= instant(event["timestamp"]) <= upper:
            continue
        payload = event.get("payload", {})
        kind = payload.get("type")
        out = {"timestamp": event["timestamp"], "record_type": event.get("type"), "type": kind}
        if event.get("type") == "event_msg":
            if kind not in ("task_started", "task_complete", "turn_aborted", "error"):
                continue
            out.update({key: payload[key] for key in ("turn_id", "duration_ms") if key in payload})
        elif event.get("type") == "response_item":
            if kind == "reasoning":
                continue
            out.update({key: payload[key] for key in ("call_id", "name", "namespace", "role", "phase", "status") if key in payload})
            if kind == "custom_tool_call":
                text = payload.get("input", "")
                out["input_summary"] = safe_input(text, sample["old_objective"])
                out["matches_requested_cell"] = hashlib.sha256(text.strip().encode()).hexdigest() == expected_hash
                if out["matches_requested_cell"]:
                    matched.append(payload["call_id"])
            elif kind == "function_call" and payload.get("name") == "wait":
                try:
                    arguments = json.loads(payload.get("arguments", "{}"))
                except json.JSONDecodeError:
                    arguments = {}
                safe = {}
                if isinstance(arguments.get("cell_id"), str) and re.fullmatch(r"[0-9]{1,8}", arguments["cell_id"]):
                    safe["cell_id"] = arguments["cell_id"]
                if isinstance(arguments.get("terminate", False), bool):
                    safe["terminate"] = arguments.get("terminate", False)
                if isinstance(arguments.get("yield_time_ms"), int) and 0 <= arguments["yield_time_ms"] <= 120000:
                    safe["yield_time_ms"] = arguments["yield_time_ms"]
                out["wait_arguments"] = safe
            elif kind in ("custom_tool_call_output", "function_call_output"):
                text = extract_text(payload.get("output"))
                out["output_summary"] = summarize_text(text, sample["old_objective"])
                if re.fullmatch(r"Script terminated\nWall time [0-9.]+ seconds\nOutput:\n?", text):
                    out["native_runtime_kind"] = "script_terminated"
                running = re.match(r"Script running with cell ID ([0-9]{1,8})\n", text)
                if running:
                    out["script_cell_id"] = running[1]
                snapshots = []
                for value in objects(text):
                    if value.get("goal", False) is None:
                        snapshots.append({"present": False})
                    goal = value.get("goal")
                    if isinstance(goal, dict):
                        label = next((label for label in ("old", "new") if goal.get("objective") == sample[label + "_objective"]), None)
                        if label:
                            snapshot = {"present": True, "objective_label": label, "objective_exact_match": True}
                            if goal.get("threadId") == sample["root"]:
                                snapshot["thread_id"] = goal["threadId"]
                            if goal.get("status") in ("active", "complete", "paused", "blocked", "budget_exhausted"):
                                snapshot["status"] = goal["status"]
                            for key in ("createdAt", "updatedAt"):
                                if isinstance(goal.get(key), int) and not isinstance(goal[key], bool):
                                    snapshot[key] = goal[key]
                            snapshots.append(snapshot)
                    if isinstance(value.get("exit_code"), int) and not isinstance(value["exit_code"], bool):
                        out.setdefault("tool_exit_codes", []).append(value["exit_code"])
                    nested = value.get("output")
                    if isinstance(nested, str) and nested.strip() == sample["old_objective"] + "-DONE":
                        out["synthetic_wait_result"] = sample["old_objective"] + "-DONE"
                if snapshots:
                    out["goal_snapshots_in_output_order"] = snapshots
        else:
            continue
        events.append(out)
    if len(matched) != 1:
        raise SystemExit("Expected exactly one native cell matching the authored request")
    result = {"captured_at": now.isoformat(), "sample": args.sample, "root_thread": sample["root"],
              "window": {"since": sample["since"], "through": sample["through"], "seconds": 120},
              "objectives": {"old": sample["old_objective"], "new": sample["new_objective"]},
              "requested_cell_sha256": expected_hash, "matched_original_call_id": matched[0],
              "privacy": "Exact synthetic objective map; native types, call identity, bounded runtime fields and hashes only. Reasoning and arbitrary text omitted.",
              "events": events}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps({"output": str(args.output), "events": len(events), "matched_original_call_id": matched[0]}))


if __name__ == "__main__":
    main()
