#!/usr/bin/env python3
"""Exercise observer disclosure/selection with isolated fake native storage."""

import datetime
import json
import os
import pathlib
import sqlite3
import subprocess
import sys
import tempfile


BASE = pathlib.Path(__file__).resolve().parents[3]
TASK = BASE / "tasks/g0-native-return"
COLLECTOR = TASK / "scripts/runner_collect.py"
EXPERIMENT_CWD = "/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/test-repo"
ROOT = "00000000-0000-4000-8000-000000000001"
CHILD = "00000000-0000-4000-8000-000000000002"
WRONG_PARENT = "00000000-0000-4000-8000-000000000003"
OTHER = "00000000-0000-4000-8000-000000000004"
CANARY = "DO_NOT_EXPORT_PRIVATE_CANARY_64ecf9"
NONCE = "nr-check"


def run(script, args):
    return subprocess.run([sys.executable, str(script), *args], text=True, capture_output=True)


def main():
    (TASK / "tmp").mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="runner-privacy-regression-", dir=TASK / "tmp") as temporary:
        fixture = pathlib.Path(temporary)
        native = fixture / "native"
        (native / "sessions").mkdir(parents=True)
        hooks = fixture / "hooks"
        hooks.mkdir()
        events = [
            {"type": "custom_tool_call", "name": "unknown_tool", "input": CANARY},
            {"type": "custom_tool_call_output", "output": [
                {"type": "input_text", "text": json.dumps({"output": NONCE + "-DONE " + CANARY,
                                                            "exit_code": 0, "private": CANARY})}]},
            {"type": "function_call", "name": "unknown_tool", "namespace": "unknown",
             "arguments": json.dumps({"target": CANARY, "id": CANARY})},
            {"type": "message", "role": "user", "content": [
                {"type": "input_text", "text": NONCE + "-DONE " + CANARY}]},
        ]
        root_rollout = native / "sessions/root.jsonl"
        root_rollout.write_text("".join(json.dumps({"timestamp": "2026-09-11T00:00:01Z",
                                                   "type": "response_item", "payload": e}) + "\n" for e in events))
        child_rollout = native / "sessions/child.jsonl"
        child_rollout.write_text("")
        # The wrong-parent file is intentionally absent: successful selection
        # must not attempt to open it, even when another source key names root.
        wrong_rollout = native / "sessions/must-not-open.jsonl"
        db = sqlite3.connect(native / "state_5.sqlite")
        db.execute("CREATE TABLE threads(id TEXT,rollout_path TEXT,cwd TEXT,created_at_ms INTEGER,source TEXT,cli_version TEXT,agent_path TEXT)")
        child_source = {"subagent": {"thread_spawn": {"parent_thread_id": ROOT,
                         "agent_path": "/root/nr_check", "agent_nickname": CANARY}}}
        wrong_source = {"subagent": {"thread_spawn": {"parent_thread_id": OTHER}}, "unrelated": ROOT}
        db.executemany("INSERT INTO threads VALUES(?,?,?,?,?,?,?)", [
            (ROOT, str(root_rollout), EXPERIMENT_CWD, 0, "cli", "fixture", None),
            (CHILD, str(child_rollout), EXPERIMENT_CWD, 1, json.dumps(child_source), "fixture", "/root/nr_check"),
            (WRONG_PARENT, str(wrong_rollout), "/must-not-read", 1, json.dumps(wrong_source), "fixture", "/unrelated"),
        ])
        db.commit()
        (hooks / "hook.json").write_text(json.dumps({"hook_event_name": "SessionStart", "nonce": NONCE,
            "pid": 100, "ppid": 99, "recorded_at": "2026-09-11T00:00:01Z", "session_id": ROOT,
            "source": CANARY, "private_extension": CANARY}))
        old_script = fixture / "old_collector.py"
        old_source = subprocess.check_output(["git", "show", "153a4ff:tasks/g0-native-return/scripts/runner_collect.py"], cwd=BASE, text=True)
        old_script.write_text(old_source)
        common = ["--root-thread", ROOT, "--nonce", NONCE, "--pid", str(os.getpid()),
                  "--since", "2026-09-11T00:00:00Z", "--through", "2026-09-11T00:00:02Z",
                  "--codex-dir", str(native), "--hook-dir", str(hooks)]
        old_output = fixture / "old.json"
        red = run(old_script, [*common, "--output", str(old_output)])
        assert red.returncode == 0, red.stderr
        assert CANARY in old_output.read_text(), "old observer must reproduce actual plaintext disclosure"
        new_output = fixture / "new.json"
        green = run(COLLECTOR, [*common, "--output", str(new_output)])
        assert green.returncode == 0, green.stderr
        text = new_output.read_text()
        assert CANARY not in text, "current observer exported the private canary"
        selected = json.loads(text)
        assert {t["id"] for t in selected["threads"]} == {ROOT, CHILD}
        assert WRONG_PARENT not in text
        assert all("cwd" not in t and "source" not in t for t in selected["threads"])
        assert "private_extension" not in text
        db.execute("UPDATE threads SET cwd=? WHERE id=?", ("/non-experiment-private-workspace", ROOT))
        db.commit()
        invalid_cwd = run(COLLECTOR, [*common, "--output", str(fixture / "wrong-cwd.json")])
        assert invalid_cwd.returncode != 0 and "outside the explicitly selected experiment cwd" in invalid_cwd.stderr
        assert not (fixture / "wrong-cwd.json").exists()
        db.execute("UPDATE threads SET cwd=? WHERE id=?", (EXPERIMENT_CWD, ROOT))
        db.commit()
        wrong_root_args = common.copy()
        wrong_root_args[wrong_root_args.index("--root-thread") + 1] = WRONG_PARENT
        invalid_root = run(COLLECTOR, [*wrong_root_args, "--output", str(fixture / "wrong-root.json")])
        assert invalid_root.returncode != 0 and "native CLI source" in invalid_root.stderr
        assert not (fixture / "wrong-root.json").exists()
        db.close()
    assert not fixture.exists()
    report = {"verified_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "old_commit": "153a4ff", "red": "old collector actually exported fixture plaintext canary",
              "green": "current collector omitted canary from unknown tool input/output/arguments, message, source and Hook fields",
              "wrong_parent": "not selected; absent private rollout was never opened",
              "wrong_cwd": "rejected without producing output", "non_cli_root": "rejected without producing output",
              "fixture_cleanup": "temporary isolated SQLite/rollout/Hook/source fixtures removed", "native_model_calls": 0}
    output = BASE / "docs/implementation/g0/native-return/runner-privacy-regression.json"
    output.write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report))


if __name__ == "__main__":
    main()
