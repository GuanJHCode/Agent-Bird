"""Bounded helper contract tests; no native service or model is contacted."""
import copy
import asyncio
import hashlib
import json
import os
import pathlib
import tempfile
import unittest
from unittest.mock import patch

import native_standalone as probe

ROOT = "01900000-0000-7000-8000-000000000001"
TURN = "01900000-0000-7000-8000-000000000002"
BOOT = "01900000-0000-4000-8000-000000000003"
NONCE = "sd-qual-01"


def binding():
    return {"version": 1, "nonce": NONCE, "controller_thread_id": ROOT, "controller_epoch": 1,
            "expected_cwd": "/private/g0/repo", "prior_turn_id": TURN, "native_cli_version": "0.153.4",
            "boot_id": BOOT, "service": {"pid": 1234, "uid": os.getuid(), "birth": "Fri Sep 11 10:00:00 2026",
            "comm": "/private/g0/codex", "socket_path": "/private/g0/native.sock", "socket_dev": 1,
            "socket_ino": 2, "native_binary_sha256": "b973d440acac501fd2594a43e7ca9ce41e0a65b9dfb28d0d7a7837c99e1261e3"},
            "tui": {"pid": 1235, "uid": os.getuid(), "birth": "Fri Sep 11 10:00:01 2026", "comm": "/private/g0/codex",
                    "native_binary_sha256": "b973d440acac501fd2594a43e7ca9ce41e0a65b9dfb28d0d7a7837c99e1261e3"},
            "go_binary": "/private/g0/lab", "go_binary_sha256": "feb9b9ad969abca5ff35db28c0f0d8d920dc0778d7aac68a90952900e989a285",
            "job_dir": "/private/g0/job", "controller_evidence_sha256": "a" * 64}


def go_inspection():
    result = {"version": 1, "status": "completed", "nonce": NONCE, "count": 1}
    event = {"version": 1, "nonce": NONCE, "controller_thread": ROOT, "task_revision": 1, "result": result}
    digest = hashlib.sha256(json.dumps(event, separators=(",", ":")).encode()).hexdigest()
    return {"version": 1, "status": "completed", "nonce": NONCE, "controller_thread": ROOT,
            "revision": 1, "task_revision": 1, "cancelled": False, "event_hash": digest, "result": result, "ack": None}


def thread_result():
    return {"thread": {"id": ROOT, "sessionId": ROOT, "source": "cli", "cliVersion": "0.153.4",
            "cwd": "/private/g0/repo", "historyMode": "legacy", "ephemeral": False, "status": {"type": "idle"},
            "turns": [{"id": TURN, "status": "completed", "itemsView": "full", "items": [
                {"id": "exec_01", "type": "commandExecution", "status": "completed", "exitCode": 0, "command": "PRIVATE_DO_NOT_EXPORT", "aggregatedOutput": "PRIVATE_DO_NOT_EXPORT"},
                {"id": "ready_01", "type": "agentMessage", "phase": "final_answer", "text": "SD_QUAL_01_READY"}]}]}}


class NativeStandaloneTests(unittest.TestCase):
    def test_prior_timestamp_is_exact_native_field_not_ready_text_time(self):
        data = thread_result()
        data["thread"]["turns"][0]["completedAt"] = 1790000000
        _, facts = probe.filter_thread(data, binding(), None, require_ready=True)
        self.assertEqual(facts.get("prior_completed_at"), 1790000000)
        data["thread"]["turns"][0]["completedAt"] = True
        with self.assertRaises(probe.ProbeError):
            probe.filter_thread(data, binding(), None, require_ready=True)

    def test_send_intent_precedes_one_wire_write_and_receipt_is_not_history(self):
        class Wire:
            async def call(self, method, params, **_):
                self.attempt = case.read("send-attempt.json")
                self.params = params
                return {"turn": {"id": "new_turn", "status": "inProgress", "items": [{"text": "PRIVATE_DO_NOT_EXPORT"}], "itemsView": "notLoaded"}}
        wire = Wire()
        now = {"boot_id": BOOT, "clock_impl": "mach_absolute_time()", "mono_ns": 1_000_000_000, "wall_ns": 10_000_000_000}
        with tempfile.TemporaryDirectory(prefix="g0-standalone-case-") as directory:
            os.chmod(directory, 0o700)
            with probe.Case(directory) as case, patch.object(probe, "verify_machine"), patch.object(probe, "current_clock", return_value=now):
                result = asyncio.run(probe.issue_once(wire, case, binding(), probe.make_intent(binding(), go_inspection()), "b" * 64))
                self.assertTrue(pathlib.Path(directory, "send-attempt.json").exists())
                self.assertEqual(wire.attempt["status"], "send_intent")
                self.assertFalse(result[1]["history_delivery_proven"])
                self.assertEqual(wire.params["input"], [])
                self.assertNotIn("PRIVATE_DO_NOT_EXPORT", probe.canonical(result))
                with self.assertRaises(probe.ProbeError):
                    asyncio.run(probe.issue_once(wire, case, binding(), probe.make_intent(binding(), go_inspection()), "b" * 64))

    def test_tui_dies_after_claim_no_send_and_intent_stays_non_retryable(self):
        class Wire:
            async def call(self, *_args, **_kwargs):
                raise AssertionError("no wire request is allowed after identity loss")
        now = {"boot_id": BOOT, "clock_impl": "mach_absolute_time()", "mono_ns": 1_000_000_000, "wall_ns": 10_000_000_000}
        with tempfile.TemporaryDirectory(prefix="g0-standalone-case-") as directory:
            os.chmod(directory, 0o700)
            with probe.Case(directory) as case, patch.object(probe, "verify_machine", side_effect=probe.ProbeError("tui_identity_changed")), patch.object(probe, "current_clock", return_value=now):
                with self.assertRaises(probe.ProbeError):
                    asyncio.run(probe.issue_once(Wire(), case, binding(), probe.make_intent(binding(), go_inspection()), "b" * 64))
                self.assertEqual(case.read("send-attempt.json")["status"], "send_intent")

    def test_binding_rejects_wrong_owner_root_version_and_unknown_fields(self):
        good = binding()
        self.assertEqual(probe.validate_binding(good, ROOT, NONCE), good)
        for field, value in [("controller_thread_id", TURN), ("native_cli_version", "0.154.0"), ("nonce", "other"), ("attached", True)]:
            changed = copy.deepcopy(good)
            changed[field] = value
            with self.subTest(field=field), self.assertRaises(probe.ProbeError):
                probe.validate_binding(changed, ROOT, NONCE)
        changed = binding()
        changed["service"]["uid"] += 1
        with self.assertRaises(probe.ProbeError):
            probe.validate_binding(changed, ROOT, NONCE)
        changed = binding()
        changed["tui"]["uid"] += 1
        with self.assertRaises(probe.ProbeError):
            probe.validate_binding(changed, ROOT, NONCE)

    def test_only_fixed_rpc_methods_and_minimal_turn_start_are_allowed(self):
        intent = probe.make_intent(binding(), go_inspection())
        params = {"threadId": ROOT, "input": [], "toolOutput": {"name": "g0_delivery", "namespace": "orchestration", "output": probe.canonical(intent)}}
        packet = probe.make_request(5, "turn/start", params, binding(), intent)
        self.assertEqual(set(packet["params"]), {"threadId", "input", "toolOutput"})
        self.assertEqual(packet["params"]["input"], [])
        for field, value in [("model", "unexpected"), ("approvalPolicy", "never"), ("cwd", "/elsewhere"), ("input", [{"type": "text", "text": "fake callback"}]), ("threadId", TURN)]:
            changed = copy.deepcopy(params)
            changed[field] = value
            with self.subTest(field=field), self.assertRaises(probe.ProbeError):
                probe.make_request(5, "turn/start", changed, binding(), intent)
        for method in ["thread/start", "thread/resume", "thread/inject_items", "remote/status/read", "account/read"]:
            with self.subTest(method=method), self.assertRaises(probe.ProbeError):
                probe.make_request(5, method, {}, binding(), intent)

    def test_go_result_envelope_is_bound_to_real_fixed_schema_and_hash(self):
        got = probe.make_intent(binding(), go_inspection())
        self.assertEqual(got["events"][0]["payload_hash"], go_inspection()["event_hash"])
        self.assertEqual(got["events"][0]["kind"], "result")
        for field, value in [("controller_thread", TURN), ("revision", 2), ("cancelled", True), ("event_hash", "0" * 64), ("unknown", "PRIVATE_DO_NOT_EXPORT")]:
            changed = go_inspection()
            changed[field] = value
            with self.subTest(field=field), self.assertRaises(probe.ProbeError):
                probe.make_intent(binding(), changed)

    def test_idle_requires_completed_prior_turn_and_terminal_tools(self):
        probe.filter_thread(thread_result(), binding(), None, require_ready=True)
        for place, field, value in [("thread", "source", "appServer"), ("thread", "cliVersion", "0.154.0"),
                                   ("thread", "status", {"type": "active"}), ("turn", "status", "inProgress"),
                                   ("tool", "status", "inProgress"), ("ready", "phase", "commentary")]:
            changed = thread_result()
            target = changed["thread"]
            if place != "thread":
                target = target["turns"][0]
                if place == "tool": target = target["items"][0]
                if place == "ready": target = target["items"][1]
            target[field] = value
            with self.subTest(place=place, field=field), self.assertRaises(probe.ProbeError):
                probe.filter_thread(changed, binding(), None, require_ready=True)

    def test_history_preserves_exact_standalone_and_never_exports_raw_text(self):
        intent = probe.make_intent(binding(), go_inspection())
        data = thread_result()
        item = {"id": "standalone_01", "type": "functionCallOutput", "name": "g0_delivery", "namespace": "orchestration", "output": probe.canonical(intent)}
        data["thread"]["turns"].append({"id": "01900000-0000-7000-8000-000000000004", "status": "completed", "itemsView": "full", "items": [item]})
        filtered, facts = probe.filter_thread(data, binding(), intent)
        self.assertTrue(facts["history_match"])
        self.assertNotIn("PRIVATE_DO_NOT_EXPORT", probe.canonical(filtered))
        self.assertEqual(filtered["thread"]["turns"][-1]["items"][0], item)
        bad = copy.deepcopy(data)
        bad["thread"]["turns"][-1]["items"][0]["output"] = '{"unknown":"PRIVATE_DO_NOT_EXPORT"}'
        with self.assertRaises(probe.ProbeError):
            probe.filter_thread(bad, binding(), intent)
        bad = copy.deepcopy(data)
        changed_intent = copy.deepcopy(intent)
        changed_intent["version"] = True
        bad["thread"]["turns"][-1]["items"][0]["output"] = probe.canonical(changed_intent)
        with self.assertRaises(probe.ProbeError):
            probe.filter_thread(bad, binding(), intent)

    def test_unknown_or_oversize_json_is_rejected_without_diagnostic_echo(self):
        for raw in [b'{"x":NaN}', b'{"id":1,"id":2}', b'x' * 65537, b'__import__("os")']:
            with self.subTest(size=len(raw)), self.assertRaises(probe.ProbeError) as caught:
                probe.decode(raw)
            self.assertNotIn("__import__", str(caught.exception))

    def test_send_claim_is_durable_intent_and_cannot_be_reused(self):
        with tempfile.TemporaryDirectory(prefix="g0-standalone-case-") as directory:
            os.chmod(directory, 0o700)
            with probe.Case(directory) as case:
                intent = {"status": "send_intent", "nonce": NONCE}
                case.write_new("send-attempt.json", intent)
                self.assertEqual(case.read("send-attempt.json"), intent)
                with self.assertRaises(probe.ProbeError):
                    case.write_new("send-attempt.json", {"status": "sent"})
                self.assertEqual(case.read("send-attempt.json")["status"], "send_intent")

    def test_original_model_window_rejects_boot_or_clock_change_and_never_extends(self):
        attempt = {"boot_id": BOOT, "clock_impl": "mach_absolute_time()", "mono_ns": 1_000_000_000, "wall_ns": 10_000_000_000}
        now = {"boot_id": BOOT, "clock_impl": "mach_absolute_time()", "mono_ns": 2_000_000_000, "wall_ns": 11_000_000_000}
        self.assertEqual(probe.remaining_window(attempt, now), 119.0)
        for field, value in [("boot_id", ROOT), ("clock_impl", "different"), ("wall_ns", 20_000_000_000), ("mono_ns", 121_000_000_001)]:
            changed = dict(now)
            changed[field] = value
            with self.subTest(field=field), self.assertRaises(probe.ProbeError):
                probe.remaining_window(attempt, changed)


if __name__ == "__main__":
    unittest.main()
