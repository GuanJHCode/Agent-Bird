"""G0-only transport evidence tests; no native service/model is contacted."""
import copy
import hashlib
import json
import pathlib
import subprocess
import sys
import tempfile
import unittest

SCRIPT = pathlib.Path(__file__).with_name("delivery_audit.py")


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False)


def fixture():
    body = {"version": 1, "delivery_id": "delivery_01", "controller_thread_id": "root_01",
            "controller_epoch": 1, "events": [
                {"event_id": "progress_01", "event_revision": 1, "kind": "progress", "payload_hash": "a" * 64, "action_slot": "slot_progress_01"},
                {"event_id": "question_01", "event_revision": 2, "kind": "question", "payload_hash": "b" * 64, "action_slot": "slot_question_01"}]}
    intent = {**body, "payload_hash": hashlib.sha256(canonical(body).encode()).hexdigest()}
    item = {"type": "functionCallOutput", "id": "item_01", "name": "g0_delivery", "namespace": "orchestration", "output": canonical(intent)}
    read = {"request": {"id": 10, "method": "thread/turns/list", "params": {"threadId": "root_01", "itemsView": "full", "cursor": None}},
            "response": {"id": 10, "result": {"data": [{"id": "turn_01", "status": "completed", "itemsView": "full", "items": [item]}], "nextCursor": None, "backwardsCursor": "anchor_01"}}}
    return {"version": 1, "intent": intent, "reads": [read], "acks": []}


def ack(data, event=0, decision="handled", command="command_01"):
    intent = data["intent"]
    e = intent["events"][event]
    return {"delivery_id": intent["delivery_id"], "controller_thread_id": intent["controller_thread_id"],
            "controller_epoch": intent["controller_epoch"], "event_id": e["event_id"], "event_revision": e["event_revision"],
            "event_hash": e["payload_hash"], "action_slot": e["action_slot"], "decision": decision, "command_id": command,
            "decision_count": 1, "effect_count": 1 if decision == "handled" else 0}


class DeliveryAuditTests(unittest.TestCase):
    def run_bundle(self, data, code=0, raw=None):
        with tempfile.TemporaryDirectory(prefix="g0-delivery-audit-") as directory:
            path = pathlib.Path(directory) / "capture.json"
            path.write_bytes(raw if raw is not None else canonical(data).encode())
            run = subprocess.run([sys.executable, str(SCRIPT), "--input", str(path)], capture_output=True, timeout=3)
        self.assertEqual(run.returncode, code, run.stderr.decode(errors="replace"))
        self.assertLess(len(run.stdout) + len(run.stderr), 8192)
        value = json.loads(run.stdout if code == 0 else run.stderr)
        if code:
            self.assertEqual(run.stdout, b"")
        return value, run

    # Catches confusing displayed tool content with business processing or native
    # source authenticity, and withholding later questions behind missing ACKs.
    def test_exact_history_match_is_separate_from_business_ack(self):
        value, _ = self.run_bundle(fixture())
        self.assertEqual(value["transport_evidence"], "exact_history_marker")
        self.assertEqual(value["transport_gate"], "history_match_observed")
        self.assertEqual(value["pending_event_ids"], ["progress_01", "question_01"])
        self.assertFalse(value["source_service_verified"])
        self.assertFalse(value["resend_authorized"])

    # A complete local ACK does not prove standalone native transport happened.
    def test_absence_and_all_acks_still_leave_transport_uncertain(self):
        data = fixture()
        data["reads"][0]["response"]["result"]["data"] = []
        data["acks"] = [ack(data), ack(data, 1, "waiting_user")]
        value, _ = self.run_bundle(data)
        self.assertEqual(value["transport_evidence"], "uncertain")
        self.assertFalse(value["absence_proof"])
        self.assertTrue(value["business_ack_complete"])
        self.assertEqual(value["waiting_user_event_ids"], ["question_01"])

    def test_partial_acks_and_changed_command_uuid_use_one_declared_slot(self):
        data = fixture()
        data["acks"] = [ack(data), ack(data, command="different_uuid")]
        value, _ = self.run_bundle(data)
        self.assertEqual(value["acknowledged_event_ids"], ["progress_01"])
        self.assertEqual(value["pending_event_ids"], ["question_01"])
        self.assertEqual(value["unique_ack_slots"], 1)
        self.assertEqual(value["transport_gate"], "history_match_observed")

    # Turn-start receipts and arbitrary marker-bearing assistant text are not a
    # read of persisted functionCallOutput and cannot authorize resend.
    def test_only_history_read_methods_and_typed_tool_items_can_match(self):
        data = fixture()
        data["reads"][0]["request"]["method"] = "turn/start"
        self.run_bundle(data, 2)
        data = fixture()
        item = data["reads"][0]["response"]["result"]["data"][0]["items"][0]
        data["reads"][0]["response"]["result"]["data"][0]["items"] = [{"id": "assistant_01", "type": "agentMessage", "text": item["output"]}]
        value, _ = self.run_bundle(data)
        self.assertEqual(value["transport_evidence"], "uncertain")

    def test_second_page_matches_and_missing_page_never_proves_absence(self):
        data = fixture()
        first = copy.deepcopy(data["reads"][0])
        first["response"]["result"]["data"] = []
        first["response"]["result"]["nextCursor"] = "page_two"
        second = data["reads"][0]
        second["request"]["id"] = second["response"]["id"] = 11
        second["request"]["params"]["cursor"] = "page_two"
        data["reads"] = [first, second]
        value, _ = self.run_bundle(data)
        self.assertEqual(value["transport_evidence"], "exact_history_marker")
        self.assertTrue(value["captured_page_chain_complete"])
        data["reads"] = [first]
        value, _ = self.run_bundle(data)
        self.assertFalse(value["captured_page_chain_complete"])
        self.assertEqual(value["transport_evidence"], "uncertain")

    def test_summary_compact_and_broken_cursors_remain_visible(self):
        data = fixture()
        data["reads"][0]["request"]["params"]["itemsView"] = "summary"
        value, _ = self.run_bundle(data)
        self.assertEqual(value["transport_evidence"], "uncertain")
        self.assertFalse(value["captured_page_chain_complete"])
        data["reads"][0]["response"]["result"]["data"] = []
        value, _ = self.run_bundle(data)
        self.assertFalse(value["captured_page_chain_complete"])
        data = fixture()
        data["reads"][0]["response"]["result"]["data"][0]["items"] = [{"id": "compact_01", "type": "contextCompaction"}]
        value, _ = self.run_bundle(data)
        self.assertTrue(value["compaction_seen"])
        self.assertFalse(value["absence_proof"])
        data["reads"][0]["request"]["params"]["cursor"] = "unseen_prior_page"
        value, _ = self.run_bundle(data)
        self.assertFalse(value["captured_page_chain_complete"])

    def test_thread_read_and_item_pages_keep_original_rpc_binding(self):
        data = fixture()
        turn = data["reads"][0]["response"]["result"]["data"][0]
        for method, params, result in [
            ("thread/read", {"threadId": "root_01", "includeTurns": True}, {"thread": {"id": "root_01", "historyMode": "legacy", "turns": [turn]}}),
            ("thread/items/list", {"threadId": "root_01", "cursor": None}, {"data": [{"turnId": "turn_01", "item": turn["items"][0]}], "nextCursor": None}),
        ]:
            with self.subTest(method=method):
                data["reads"] = [{"request": {"id": 20, "method": method, "params": params}, "response": {"id": 20, "result": result}}]
                value, _ = self.run_bundle(data)
                self.assertEqual(value["transport_evidence"], "exact_history_marker")
                data["reads"][0]["response"]["id"] = 21
                self.run_bundle(data, 2)

    def test_mismatched_binding_or_unknown_fields_are_rejected(self):
        for field, value in [("delivery_id", "other"), ("controller_thread_id", "other"), ("controller_epoch", 2), ("payload_hash", "c" * 64), ("extra", "canary_never_echo")]:
            data = fixture()
            item = data["reads"][0]["response"]["result"]["data"][0]["items"][0]
            wrong = json.loads(item["output"])
            wrong[field] = value
            item["output"] = canonical(wrong)
            result, run = self.run_bundle(data, 2)
            self.assertEqual(result["status"], "error")
            self.assertNotIn(b"canary_never_echo", run.stderr)
        data = fixture()
        data["reads"][0]["request"]["params"]["threadId"] = "other"
        self.run_bundle(data, 2)
        data = fixture()
        data["queue_empty"] = data["fenced"] = True
        self.run_bundle(data, 2)

    def test_ack_conflicts_and_version_hash_slot_changes_are_rejected(self):
        for field, value in [("event_revision", 9), ("event_hash", "c" * 64), ("action_slot", "new_slot"), ("controller_epoch", 2), ("effect_count", True), ("event_id", "foreign"), ("delivery_id", "foreign")]:
            data = fixture()
            wrong = ack(data)
            wrong[field] = value
            data["acks"] = [wrong]
            self.run_bundle(data, 2)
        data = fixture()
        data["acks"] = [ack(data), ack(data, decision="rejected", command="different_uuid")]
        self.run_bundle(data, 2)

    def test_duplicate_displays_are_counted_without_multiplying_ack_slots(self):
        data = fixture()
        items = data["reads"][0]["response"]["result"]["data"][0]["items"]
        duplicate = copy.deepcopy(items[0])
        duplicate["id"] = "item_02"
        items.append(duplicate)
        data["acks"] = [ack(data), ack(data, command="changed_uuid")]
        value, _ = self.run_bundle(data)
        self.assertEqual(value["matched_history_items"], 2)
        self.assertEqual(value["duplicate_displays"], 1)
        self.assertEqual(value["unique_ack_slots"], 1)

    def test_malicious_raw_duplicate_keys_and_size_limits_do_not_echo_payload(self):
        for raw in [b'{"version":1,"version":1}', b'__import__("os").system("canary_never_echo")', b'{"x":NaN}', b"x" * 65537, b"[" * 1000 + b"]" * 1000]:
            result, run = self.run_bundle(None, 2, raw)
            self.assertEqual(result["status"], "error")
            self.assertNotIn(b"canary_never_echo", run.stdout + run.stderr)

    def test_real_retained_custom_tool_record_is_not_standalone_delivery(self):
        root = SCRIPT.parents[3]
        capture = json.loads((root / "tasks/g0-retained-resume/docs/native-retained-quit-04-continued-thread.json").read_text())
        # Preserve the real filtered record's type/fields; never translate it to
        # functionCallOutput. The surrounding read is a test fixture only.
        event = next(e for e in capture["events"] if e["type"] == "custom_tool_call_output")
        data = fixture()
        data["reads"][0]["response"]["result"]["data"][0]["items"] = [event]
        self.run_bundle(data, 2)

    def test_valid_other_delivery_is_ignored_but_reused_item_id_conflict_is_rejected(self):
        data = fixture()
        item = data["reads"][0]["response"]["result"]["data"][0]["items"][0]
        other = copy.deepcopy(data["intent"])
        other["delivery_id"] = "older_delivery"
        body = {key: value for key, value in other.items() if key != "payload_hash"}
        other["payload_hash"] = hashlib.sha256(canonical(body).encode()).hexdigest()
        replacement = {**item, "output": canonical(other)}
        data["reads"][0]["response"]["result"]["data"][0]["items"] = [replacement]
        value, _ = self.run_bundle(data)
        self.assertEqual(value["transport_evidence"], "uncertain")
        data["reads"][0]["response"]["result"]["data"][0]["items"] = [item, replacement]
        self.run_bundle(data, 2)

    def test_native_item_identity_is_stable_before_filtering_unrelated_content(self):
        for kind in ("agentMessage", "other_tool"):
            for reverse in (False, True):
                with self.subTest(kind=kind, reverse=reverse):
                    data = fixture()
                    item = data["reads"][0]["response"]["result"]["data"][0]["items"][0]
                    changed = {"id": "item_01", "type": "agentMessage", "text": "unrelated"} if kind == "agentMessage" else {**item, "name": "other_tool"}
                    items = [changed, item] if reverse else [item, changed]
                    data["reads"][0]["response"]["result"]["data"][0]["items"] = items
                    self.run_bundle(data, 2)
        # Unrelated assistant items legitimately gain text between reads. Only
        # stable item identity is required; this is not a full-body freeze.
        data = fixture()
        items = data["reads"][0]["response"]["result"]["data"][0]["items"]
        items.extend([{"id": "agent_01", "type": "agentMessage", "text": "first"},
                      {"id": "agent_01", "type": "agentMessage", "text": "first, then more"}])
        value, _ = self.run_bundle(data)
        self.assertEqual(value["transport_evidence"], "exact_history_marker")

    def test_pages_after_a_terminated_traversal_are_not_one_complete_chain(self):
        for present in (False, True):
            with self.subTest(marker_present=present):
                data = fixture()
                if not present:
                    data["reads"][0]["response"]["result"]["data"] = []
                another = copy.deepcopy(data["reads"][0])
                another["request"]["id"] = another["response"]["id"] = 11
                data["reads"].append(another)
                value, _ = self.run_bundle(data)
                self.assertFalse(value["captured_page_chain_complete"])
                self.assertEqual(value["transport_evidence"], "exact_history_marker" if present else "uncertain")
                self.assertFalse(value["absence_proof"])
                self.assertFalse(value["resend_authorized"])


if __name__ == "__main__":
    unittest.main()
