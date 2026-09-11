#!/usr/bin/env python3
"""Bounded, read-only readiness gate for an owned native goal + Host sample."""

import argparse
import datetime
import json
import pathlib
import re
import sqlite3
import subprocess
import time


TEST_REPO = pathlib.Path("/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/test-repo")
CODEX_DIR = pathlib.Path("/Users/guanjunhui/.codex")


def identity(pid):
    result = subprocess.run(["ps", "-p", str(pid), "-o", "pid=,ppid=,pgid=,lstart=,comm="],
                            text=True, capture_output=True)
    return result.stdout.strip() if result.returncode == 0 else None


def goal_values(value, root, nonce):
    if isinstance(value, dict):
        if (value.get("threadId") == root and nonce in str(value.get("objective", ""))
                and value.get("status") in ("active", "complete", "paused", "blocked")):
            yield {"thread_id": root, "status": value["status"],
                   **{key: value[key] for key in ("createdAt", "updatedAt")
                      if isinstance(value.get(key), (int, float))}}
        for child in value.values():
            yield from goal_values(child, root, nonce)
    elif isinstance(value, list):
        for child in value:
            yield from goal_values(child, root, nonce)


def latest_goal(path, root, nonce):
    found = None
    decoder = json.JSONDecoder()
    for line in path.open():
        if not line.endswith("\n"):
            continue
        event = json.loads(line)
        payload = event.get("payload", {})
        if event.get("type") != "response_item" or payload.get("type") != "custom_tool_call_output":
            continue
        for block in payload.get("output", []):
            text = block.get("text", "")
            start = re.search(r"[\[{]", text)
            if not start:
                continue
            try:
                value, _ = decoder.raw_decode(text[start.start():])
            except json.JSONDecodeError:
                continue
            for goal in goal_values(value, root, nonce):
                found = {"recorded_at": event["timestamp"], **goal}
    return found


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root-thread", required=True)
    parser.add_argument("--native-pid", required=True, type=int)
    parser.add_argument("--nonce", required=True)
    parser.add_argument("--dir", required=True, type=pathlib.Path)
    parser.add_argument("--output", required=True, type=pathlib.Path)
    parser.add_argument("--timeout", type=int, default=90)
    args = parser.parse_args()
    if not re.fullmatch(r"[0-9a-f-]{36}", args.root_thread) or not 1 <= args.timeout <= 100:
        parser.error("explicit UUID and 1..100 second timeout required")
    nonce = re.fullmatch(r"nr-g0-goal-([0-9]{2})", args.nonce)
    expected = TEST_REPO / ("tasks/native-return/data/runner-host-goal-" + (nonce[1] if nonce else "invalid"))
    if nonce is None or args.dir.resolve() != expected or args.dir.stat().st_mode & 0o777 != 0o700:
        parser.error("directory must be the exact owned 0700 synthetic task directory")
    original_native = identity(args.native_pid)
    if original_native is None or not original_native.endswith(" codex"):
        raise SystemExit("owned native CLI PID must be live")
    connection = sqlite3.connect(f"file:{CODEX_DIR / 'state_5.sqlite'}?mode=ro", uri=True)
    deadline = time.monotonic() + args.timeout
    while time.monotonic() < deadline:
        if identity(args.native_pid) != original_native:
            raise SystemExit("native PID disappeared or creation identity changed")
        row = connection.execute("SELECT rollout_path,cwd,source FROM threads WHERE id=?", (args.root_thread,)).fetchone()
        if row:
            path = pathlib.Path(row[0])
            if pathlib.Path(row[1]).resolve() != TEST_REPO or row[2] != "cli" or CODEX_DIR / "sessions" not in path.parents:
                raise SystemExit("explicit native root does not match owned experiment scope")
            goal = latest_goal(path, args.root_thread, args.nonce)
            job_path = args.dir / "job.json"
            if goal and goal["status"] == "active" and job_path.exists():
                if (args.dir / "result.json").exists():
                    raise SystemExit("result already ready; cannot count an in-flight exit sample")
                value = json.loads(job_path.read_text())
                if (value.get("version") != 1 or value.get("status") != "started" or value.get("nonce") != args.nonce
                        or not isinstance(value.get("worker_pid"), int)):
                    raise SystemExit("job identity mismatch")
                worker = identity(value["worker_pid"])
                if worker is None or not worker.endswith("/tools/g0-return-lab/data/g0-return-lab"):
                    raise SystemExit("owned Host worker must be live")
                record = {"ready_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
                          "root_thread": args.root_thread, "nonce": args.nonce, "goal": goal,
                          "native_identity": original_native, "worker_identity": worker,
                          "job": {key: value[key] for key in ("version", "status", "nonce", "worker_pid", "created_at")},
                          "result_present": False}
                args.output.parent.mkdir(parents=True, exist_ok=True)
                with args.output.open("x") as output:
                    output.write(json.dumps(record, indent=2) + "\n")
                print(json.dumps(record), flush=True)
                return
        time.sleep(0.1)
    raise SystemExit("readiness not established within the fixed preparation window")


if __name__ == "__main__":
    main()
