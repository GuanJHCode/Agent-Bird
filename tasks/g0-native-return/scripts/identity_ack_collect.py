#!/usr/bin/env python3
"""Read-only, exact-envelope evidence for one owned native positive ACK sample."""

import argparse
import datetime
import hashlib
import json
import pathlib
import re
import sqlite3

from identity_race_collect import objects
from runner_collect import extract_text, safe_input, summarize_text

ROOT = "01a08e8d-97cd-7f01-adb7-5a51990d8d81"
NONCE = "nr-g0-identity-ack-root-01"
COMMAND_ID = "nr-g0-identity-ack-root-01-cmd01"
CODEX = pathlib.Path("/Users/guanjunhui/.codex")
CWD = pathlib.Path("/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/test-repo")
RESULT = {"version": 1, "status": "completed", "nonce": NONCE, "count": 1}
PRODUCER_EVENT = {"version": 1, "nonce": NONCE, "controller_thread": ROOT, "task_revision": 1, "result": RESULT}
EVENT_HASH = hashlib.sha256(json.dumps(PRODUCER_EVENT, separators=(",", ":")).encode()).hexdigest()
ACK = {"version": 1, "status": "acknowledged", "nonce": NONCE, "controller_thread": ROOT,
       "revision": 1, "event_hash": EVENT_HASH, "command_id": COMMAND_ID, "decision": "handled",
       "decision_count": 1, "effect_count": 1}
INSPECT = {"version": 1, "status": "completed", "nonce": NONCE, "controller_thread": ROOT,
           "revision": 1, "task_revision": 1, "cancelled": False, "event_hash": EVENT_HASH,
           "result": RESULT, "ack": None}
WINDOWS = [("produce", "2026-09-11T03:41:20.483Z", "2026-09-11T03:42:56.826Z"),
           ("ack", "2026-09-11T03:45:36.138Z", "2026-09-11T03:46:18.151Z")]


def same(value, expected):
    if type(value) is not type(expected):
        return False
    if isinstance(expected, dict):
        return set(value) == set(expected) and all(same(value[key], item) for key, item in expected.items())
    return value == expected


def envelope(value):
    if same(value, RESULT):
        return "result"
    if same(value, ACK):
        return "ack"
    if same(value, INSPECT) or same(value, {**INSPECT, "ack": ACK}):
        return "inspect"
    keys = {"version", "status", "nonce", "worker_pid", "created_at", "job_file", "result_file", "controller_thread", "task_revision"}
    if not isinstance(value, dict) or set(value) != keys:
        return None
    if (type(value["worker_pid"]) is int and 1 <= value["worker_pid"] <= 2147483647
            and isinstance(value["created_at"], str) and re.fullmatch(r"[0-9T:.Z-]{20,35}", value["created_at"])
            and same({key: value[key] for key in keys - {"worker_pid", "created_at"}},
                     {"version": 1, "status": "started", "nonce": NONCE, "job_file": "job.json",
                      "result_file": "result.json", "controller_thread": ROOT, "task_revision": 1})):
        return "owned_start"
    return None


def instant(value):
    return datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    args = parser.parse_args()
    now = datetime.datetime.now(datetime.timezone.utc)
    for _, lower, upper in WINDOWS:
        if not instant(lower) <= instant(upper) <= now or (instant(upper) - instant(lower)).total_seconds() > 120:
            raise SystemExit("The bounded observation windows must already have elapsed")
    conn = sqlite3.connect(f"file:{CODEX / 'state_5.sqlite'}?mode=ro", uri=True)
    conn.row_factory = sqlite3.Row
    row = conn.execute("SELECT id,source,cwd,rollout_path,created_at_ms,cli_version FROM threads WHERE id=?", (ROOT,)).fetchone()
    if row is None or row["source"] != "cli" or pathlib.Path(row["cwd"]).resolve() != CWD:
        raise SystemExit("Refusing a thread outside the explicitly owned native CLI sample")
    path = pathlib.Path(row["rollout_path"])
    if CODEX / "sessions" not in path.parents:
        raise SystemExit("Refusing a rollout outside native sessions")
    events = []
    native_envelopes = []
    ids_seen = []
    for line in path.open():
        event = json.loads(line)
        phase = next((name for name, lower, upper in WINDOWS if instant(lower) <= instant(event["timestamp"]) <= instant(upper)), None)
        if phase is None:
            continue
        payload = event.get("payload", {})
        kind = payload.get("type")
        out = {"timestamp": event["timestamp"], "window": phase, "record_type": event.get("type"), "type": kind}
        if event.get("type") == "event_msg":
            if kind not in ("task_started", "task_complete", "turn_aborted", "error"):
                continue
            out.update({key: payload[key] for key in ("turn_id", "duration_ms") if key in payload})
        elif event.get("type") == "response_item":
            if kind == "reasoning":
                continue
            out.update({key: payload[key] for key in ("call_id", "name", "namespace", "role", "phase", "status") if key in payload})
            if kind == "custom_tool_call":
                out["input_summary"] = safe_input(payload.get("input", ""), NONCE)
            elif kind in ("custom_tool_call_output", "function_call_output"):
                text = extract_text(payload.get("output"))
                out["output_summary"] = summarize_text(text, NONCE)
                for value in objects(text):
                    candidates = [value]
                    if isinstance(value.get("output"), str):
                        candidates.extend(objects(value["output"]))
                    for item in candidates:
                        item_kind = envelope(item)
                        if item_kind:
                            out.setdefault("fixed_envelopes", []).append({"kind": item_kind, "value": item})
                            native_envelopes.append(item_kind)
                        if same(item, {"CODEX_THREAD_ID": ROOT, "CODEX_SESSION_ID": ROOT}):
                            out.setdefault("native_environment_ids", []).append(item)
                            ids_seen.append(item)
            elif kind == "message":
                text = extract_text(payload.get("content"))
                if text in (NONCE + "-ACK", "nr-g0-identity-ack-positive-ACK"):
                    out["fixed_marker"] = text
        else:
            continue
        events.append(out)
    if native_envelopes.count("owned_start") != 1 or native_envelopes.count("ack") != 1 or not ids_seen:
        raise SystemExit("Missing a unique native owned start, positive ACK, or matching native environment identity")
    output = {"captured_at": now.isoformat(), "root_thread": ROOT, "nonce": NONCE,
              "native_metadata": {"source_kind": "cli", "experiment_cwd_matches": True,
                                  "created_at_ms": row["created_at_ms"], "cli_version": row["cli_version"]},
              "windows": [{"name": name, "since": lower, "through": upper, "maximum_model_seconds": 120} for name, lower, upper in WINDOWS],
              "canonical_event_hash": EVENT_HASH,
              "privacy": "Exact owned root only; strict fixed envelope keys/types/values, two matching UUID environment fields, native event metadata and hashes; no reasoning or arbitrary tool text.",
              "events": events}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(output, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps({"output": str(args.output), "events": len(events), "native_envelopes": native_envelopes, "environment_observations": len(ids_seen)}))


if __name__ == "__main__":
    main()
