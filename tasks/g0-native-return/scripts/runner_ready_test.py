"""Host-only readiness regression fixtures, never native readiness evidence."""

import contextlib
import importlib.util
import io
import json
import pathlib
import sqlite3
import sys
import tempfile
import unittest
from unittest import mock


SCRIPT = pathlib.Path(__file__).with_name("runner_ready.py")
SPEC = importlib.util.spec_from_file_location("runner_ready_under_test", SCRIPT)
READY = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(READY)
ROOT = "00000000-0000-4000-8000-000000000001"
NONCE = "nr-g0-goal-99"
CANARY = "CANARY_PRIVATE_TEXT"
HOST = "/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-native-return/tmp/host-impl/tools/g0-return-lab/data/g0-return-lab"
GOAL = {"threadId": ROOT, "objective": NONCE, "status": "active", "tokensUsed": 0,
        "timeUsedSeconds": 0, "createdAt": 1, "updatedAt": 1}


class HostOnlyReadyRegression(unittest.TestCase):
    def run_fixture(self, overrides=None, *, create_store=True, owned=False, executable=HOST,
                    native_identity=None):
        task_tmp = SCRIPT.parents[1] / "tmp"
        task_tmp.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(prefix="runner-ready-regression-", dir=task_tmp) as temporary:
            fixture = pathlib.Path(temporary)
            test_repo = fixture / "test-repo"
            runtime = test_repo / "tasks/native-return/data/runner-host-goal-99"
            runtime.mkdir(parents=True, mode=0o700)
            runtime.chmod(0o700)
            codex_dir = fixture / "native"
            if create_store:
                sessions = codex_dir / "sessions"
                sessions.mkdir(parents=True)
                rollout = sessions / "root.jsonl"
                # Old readiness mistook this unrelated output for active Goal.
                event = {"timestamp": "2026-09-11T00:00:01Z", "type": "response_item", "payload": {
                    "type": "custom_tool_call_output", "call_id": "call_unrelated", "output": [
                        {"type": "input_text", "text": json.dumps({"unrelated": GOAL})}]}}
                rollout.write_text(json.dumps(event) + "\n")
                database = sqlite3.connect(codex_dir / "state_5.sqlite")
                database.execute("CREATE TABLE threads(id TEXT,rollout_path TEXT,cwd TEXT,source TEXT)")
                database.execute("INSERT INTO threads VALUES(?,?,?,?)", (ROOT, str(rollout), str(test_repo), "cli"))
                database.commit()
                database.close()
            job = {"version": 1, "status": "started", "nonce": NONCE, "worker_pid": 3333,
                   "created_at": "2026-09-11T00:00:00Z", "job_file": "job.json", "result_file": "result.json"}
            if owned:
                job.update(controller_thread=ROOT, task_revision=1)
            job.update(overrides or {})
            (runtime / "job.json").write_text(json.dumps(job))
            output = fixture / "ready.json"
            stdout = io.StringIO()
            # Only process-query I/O is doubled. Storage/validation are real;
            # these fixture PIDs never count as native liveness evidence.
            identities = {2222: native_identity or "2222 1 2222 Fri Sep 11 10:00:00 2026 codex",
                          3333: "3333 1 3333 Fri Sep 11 10:00:00 2026 " + executable}
            argv = [str(SCRIPT), "--root-thread", ROOT, "--native-pid", "2222", "--nonce", NONCE,
                    "--dir", str(runtime), "--output", str(output), "--timeout", "1"]
            error = None
            with mock.patch.object(READY, "TEST_REPO", test_repo), \
                 mock.patch.object(READY, "CODEX_DIR", codex_dir, create=True), \
                 mock.patch.object(READY, "identity", side_effect=lambda pid: identities.get(pid)), \
                 mock.patch.object(sys, "argv", argv), contextlib.redirect_stdout(stdout):
                try:
                    READY.main()
                except (Exception, SystemExit) as caught:
                    error = caught
            stored = output.read_text() if output.exists() else ""
            return error, stdout.getvalue(), stored

    def test_unrelated_goal_lookalike_never_becomes_a_goal_claim(self):
        error, _, stored = self.run_fixture()
        self.assertIsNone(error)
        record = json.loads(stored)
        self.assertNotIn("goal", record)
        self.assertIs(record["goal_checked"], False)
        self.assertEqual(record["job"]["nonce"], NONCE)

    def test_native_database_or_rollout_is_not_required(self):
        error, _, stored = self.run_fixture(create_store=False)
        self.assertIsNone(error)
        self.assertIs(json.loads(stored)["goal_checked"], False)

    def test_invalid_created_at_canary_is_rejected_without_export(self):
        error, stdout, stored = self.run_fixture({"created_at": CANARY})
        self.assertIsNotNone(error)
        self.assertNotIn(CANARY, stdout + stored + str(error))
        self.assertEqual(stored, "")

    def test_worker_pid_boolean_is_not_an_integer_identity(self):
        error, _, stored = self.run_fixture({"worker_pid": True})
        self.assertIsNotNone(error)
        self.assertEqual(stored, "")

    def test_owned_controller_must_match_declared_thread(self):
        error, _, stored = self.run_fixture({"controller_thread": "another_thread"}, owned=True)
        self.assertIsNotNone(error)
        self.assertEqual(stored, "")

    def test_owned_ack_worker_has_explicit_supported_identity(self):
        error, _, stored = self.run_fixture(owned=True, executable=HOST + "-ack")
        self.assertIsNone(error)
        record = json.loads(stored)
        self.assertEqual(record["job"]["controller_thread"], ROOT)
        self.assertEqual(record["job"]["task_revision"], 1)
        self.assertNotIn("created_at", record["job"])

    def test_unknown_job_fields_are_rejected(self):
        error, stdout, stored = self.run_fixture({"unknown": CANARY})
        self.assertIsNotNone(error)
        self.assertNotIn(CANARY, stdout + stored + str(error))
        self.assertEqual(stored, "")

    def test_malformed_native_process_identity_is_not_exported(self):
        error, stdout, stored = self.run_fixture(native_identity=CANARY + " codex")
        self.assertIsNotNone(error)
        self.assertNotIn(CANARY, stdout + stored + str(error))
        self.assertEqual(stored, "")


if __name__ == "__main__":
    unittest.main()
