#!/usr/bin/env python3
import copy
import json
import unittest
from native_retained_data import safe_output, valid_snapshot


ROOT = "00000000-0000-4000-8000-000000000001"
NONCE = "rr-retained-quit-01"
SNAPSHOT = {"version": 1, "status": "running", "nonce": NONCE, "controller_thread": ROOT,
    "revision": 1, "segment": 1, "completed_steps": 2, "total_steps": 12, "interval_ms": 4000,
    "worker_pid": 123, "segment_started_at": "2026-09-11T00:00:00Z", "updated_at": "2026-09-11T00:00:08.123456789Z",
    "effect_count": 2, "liveness_checked": False}


class RetainedDataTest(unittest.TestCase):
    def test_real_wrapper_keeps_only_exact_typed_snapshot(self):
        self.assertTrue(valid_snapshot(SNAPSHOT, ROOT, NONCE))
        envelope = json.dumps({"output": json.dumps(SNAPSHOT) + "\n", "exit_code": 0})
        self.assertEqual(safe_output(envelope, ROOT, NONCE)["snapshots"], [SNAPSHOT])

    def test_unknown_nested_fields_and_private_canary_never_export(self):
        bad = copy.deepcopy(SNAPSHOT); bad["private"] = "CANARY_PRIVATE_TEXT"
        text = json.dumps({"output": json.dumps(bad), "private_extension": "CANARY_PRIVATE_TEXT"})
        result = safe_output(text, ROOT, NONCE)
        self.assertNotIn("snapshots", result)
        self.assertNotIn("CANARY_PRIVATE_TEXT", json.dumps(result))
        self.assertFalse(valid_snapshot(dict(SNAPSHOT, updated_at="CANARY_PRIVATE_TEXT"), ROOT, NONCE))

    def test_type_identity_and_contract_mismatches_rejected(self):
        bad_values = [{"version": True}, {"worker_pid": True}, {"liveness_checked": 0},
                      {"liveness_checked": True}, {"controller_thread": "CANARY_PRIVATE_TEXT"},
                      {"nonce": "wrong_nonce"}, {"effect_count": 3}, {"total_steps": 33},
                      {"interval_ms": 10001}, {"status": "completed"}, {"segment": 0},
                      {"segment_started_at": "2026-09-11T00:00:08.123456789Z", "updated_at": "2026-09-11T00:00:08.123456788Z"},
                      {"segment_started_at": "2030-09-11T00:00:00Z", "updated_at": "2020-09-11T00:00:00Z"},
                      {"segment_started_at": "9999-09-11T00:00:00Z", "updated_at": "9999-09-11T00:00:01Z"}]
        for change in bad_values:
            with self.subTest(change=change):
                self.assertFalse(valid_snapshot(dict(SNAPSHOT, **change), ROOT, NONCE))

    def test_jsonl_completion_and_fixed_error_are_separate(self):
        completed = dict(SNAPSHOT, status="completed", completed_steps=12, effect_count=12)
        result = safe_output(json.dumps({"output": json.dumps(SNAPSHOT) + "\n" + json.dumps(completed), "exit_code": 0}), ROOT, NONCE)
        self.assertEqual(result["snapshots"], [SNAPSHOT, completed])
        error = safe_output(json.dumps({"version": 1, "status": "error", "error": "parent_exited"}), ROOT, NONCE)
        self.assertEqual(error["errors"], ["parent_exited"])
        private = safe_output(json.dumps({"version": 1, "status": "error", "error": "CANARY_PRIVATE_TEXT"}), ROOT, NONCE)
        self.assertNotIn("errors", private)
        self.assertNotIn("CANARY_PRIVATE_TEXT", json.dumps(private))


if __name__ == "__main__":
    unittest.main()
