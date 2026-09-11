#!/usr/bin/env python3
"""Narrow offline regression for the final, fixed native ACK sample."""
import copy
import hashlib
import json
import pathlib
import runpy
import unittest

COLLECT = runpy.run_path(str(pathlib.Path(__file__).with_name("runner_collect.py")))
NONCE = "nr-g0-goal-09"
ROOT = "01a08ea6-b1fb-79b1-a146-1158e247b8e4"
PREFIX = "/Users/guanjunhui/code/ai/codex-cli-orchestration-design/"
BINARY = PREFIX + "tasks/g0-native-return/tmp/host-impl/tools/g0-return-lab/data/g0-return-lab-ack"
DIRECTORY = PREFIX + "tasks/g0-macos/data/test-repo/tasks/native-return/data/runner-host-goal-09"
RESULT = {"version": 1, "status": "completed", "nonce": NONCE, "count": 1}
HASH = hashlib.sha256(json.dumps({"version": 1, "nonce": NONCE, "controller_thread": ROOT,
    "task_revision": 1, "result": RESULT}, separators=(",", ":")).encode()).hexdigest()
FIRST = "10000000-0000-4000-8000-000000000001"
SECOND = "10000000-0000-4000-8000-000000000002"
ACK = {"version": 1, "status": "acknowledged", "nonce": NONCE, "controller_thread": ROOT,
    "revision": 1, "event_hash": HASH, "command_id": FIRST, "decision": "handled",
    "decision_count": 1, "effect_count": 1}
INSPECT = {"version": 1, "status": "completed", "nonce": NONCE, "controller_thread": ROOT,
    "revision": 1, "task_revision": 1, "cancelled": False, "event_hash": HASH,
    "result": RESULT, "ack": ACK}


class AckEvidenceTest(unittest.TestCase):
    def export(self, objects, nonce=NONCE):
        output = "\n".join(json.dumps(obj) for obj in objects)
        return COLLECT["safe_output"](json.dumps({"output": output, "exit_code": 0}), nonce)

    def test_exact_actual_envelopes_are_preserved_in_order(self):
        objects = [ACK, INSPECT, INSPECT]
        self.assertEqual(self.export(objects).get("ack09_outputs"), objects)
        self.assertNotIn("ack09_outputs", self.export(objects, "nr-g0-goal-08"))

    def test_canary_unknown_keys_types_owner_and_hash_are_rejected(self):
        bad = []
        for obj in (ACK, INSPECT):
            extra = copy.deepcopy(obj); extra["private"] = "CANARY_PRIVATE_TEXT"; bad.append(extra)
            boolean = copy.deepcopy(obj); boolean["version"] = True; bad.append(boolean)
            owner = copy.deepcopy(obj); owner["controller_thread"] = "CANARY_PRIVATE_TEXT"; bad.append(owner)
            wrong_hash = copy.deepcopy(obj); wrong_hash["event_hash"] = "0" * 64; bad.append(wrong_hash)
        nested = copy.deepcopy(INSPECT); nested["ack"]["private"] = "CANARY_PRIVATE_TEXT"; bad.append(nested)
        for obj in bad:
            with self.subTest(keys=sorted(obj)):
                exported = self.export([obj])
                self.assertNotIn("ack09_outputs", exported)
                self.assertNotIn("CANARY_PRIVATE_TEXT", json.dumps(exported))

    def test_exact_ack_chain_exports_ids_and_redirect_without_raw_command(self):
        base = f'{BINARY} ack --dir {DIRECTORY} --nonce {NONCE} --controller-thread "$CODEX_THREAD_ID" --revision 1 --event-hash {HASH} --command-id '
        command = base + FIRST + " --decision handled > /dev/null && " + base + SECOND + " --decision handled"
        source = "text(await tools.exec_command({cmd:'" + command + "',max_output_tokens:6000,yield_time_ms:1000}));\n"
        result = COLLECT["safe_input"](source, NONCE)
        rows = result.get("ack09_commands", [])
        self.assertEqual([row["command_id"] for row in rows], [FIRST, SECOND])
        self.assertEqual([row["stdout_discarded"] for row in rows], [True, False])
        self.assertNotIn(BINARY, json.dumps(result))
        for unsafe in (source.replace(BINARY, "/tmp/CANARY_PRIVATE_TEXT"), source.replace("cmd:'", "cmd:'CODEX_THREAD_ID=fake ")):
            exported = COLLECT["safe_input"](unsafe, NONCE)
            self.assertNotIn("ack09_commands", exported)
            self.assertNotIn("CANARY_PRIVATE_TEXT", json.dumps(exported))

    def test_identity_value_is_only_the_fixed_root(self):
        good = COLLECT["safe_output"](json.dumps({"output": ROOT + "\n", "exit_code": 0}), NONCE)
        self.assertEqual(good.get("ack09_native_identity_value"), ROOT)
        bad = COLLECT["safe_output"](json.dumps({"output": "CANARY_PRIVATE_TEXT\n", "exit_code": 0}), NONCE)
        self.assertNotIn("ack09_native_identity_value", bad)
        self.assertNotIn("CANARY_PRIVATE_TEXT", json.dumps(bad))


if __name__ == "__main__":
    unittest.main()
