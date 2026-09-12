"""Bounded helper contract tests; no native service or model is contacted."""
import copy
import asyncio
import hashlib
import json
import os
import pathlib
import stat
import tempfile
import unittest
import warnings
from unittest.mock import patch
from types import SimpleNamespace

import native_standalone as probe
import delivery_audit
from owner_context import capture_owner_context

ROOT = "01900000-0000-7000-8000-000000000001"
TURN = "01900000-0000-7000-8000-000000000002"
BOOT = "01900000-0000-4000-8000-000000000003"
SESSION = "01900000-aaaa-7000-8000-000000000006"
NONCE = "sd-qual-08"


_SYNTHETIC_HOME = pathlib.Path(tempfile.mkdtemp(prefix="g0-owner-home-")).resolve()
_SYNTHETIC_HOME.chmod(0o700)
_SYNTHETIC_AUTH = _SYNTHETIC_HOME / "auth.json"
_SYNTHETIC_AUTH.write_text("{\"profile\":\"standalone-test\"}\n")
_SYNTHETIC_AUTH.chmod(0o600)
_SYNTHETIC_OWNER_CONTEXT = capture_owner_context(_SYNTHETIC_HOME, requires_openai_auth=True, config_storage_mode="codex_home")


def legacy_binding():
    return {"version": 1, "nonce": NONCE, "controller_thread_id": ROOT, "controller_epoch": 1,
            "expected_cwd": "/private/g0/repo", "prior_turn_id": TURN, "native_cli_version": "0.154.0",
            "boot_id": BOOT, "service": {"pid": 1234, "uid": os.getuid(), "birth": "Fri Sep 11 10:00:00 2026",
            "comm": "codex", "executable_path": "/private/g0/codex", "socket_path": "/private/g0/native.sock", "socket_dev": 1,
            "socket_ino": 2, "native_binary_sha256": "4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc"},
            "tui": {"pid": 1235, "uid": os.getuid(), "birth": "Fri Sep 11 10:00:01 2026", "comm": "codex", "executable_path": "/private/g0/codex",
                    "native_binary_sha256": "4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc"},
            "go_binary": "/private/g0/lab", "go_binary_sha256": "feb9b9ad969abca5ff35db28c0f0d8d920dc0778d7aac68a90952900e989a285",
            "job_dir": "/private/g0/job", "controller_evidence_sha256": "a" * 64}


def binding():
    return probe.attach_owner_context(legacy_binding(), _SYNTHETIC_OWNER_CONTEXT)


def go_inspection():
    result = {"version": 1, "status": "completed", "nonce": NONCE, "count": 1}
    event = {"version": 1, "nonce": NONCE, "controller_thread": ROOT, "task_revision": 1, "result": result}
    digest = hashlib.sha256(json.dumps(event, separators=(",", ":")).encode()).hexdigest()
    return {"version": 1, "status": "completed", "nonce": NONCE, "controller_thread": ROOT,
            "revision": 1, "task_revision": 1, "cancelled": False, "event_hash": digest, "result": result, "ack": None}


def thread_result(source="vscode"):
    return {"thread": {"id": ROOT, "sessionId": SESSION, "parentThreadId": None, "forkedFromId": None, "source": source, "cliVersion": "0.154.0",
            "cwd": "/private/g0/repo", "historyMode": "legacy", "ephemeral": False, "status": {"type": "idle"},
            "turns": [{"id": TURN, "status": "completed", "itemsView": "full", "items": [
                {"id": "exec_01", "type": "commandExecution", "status": "completed", "exitCode": 0, "command": "PRIVATE_DO_NOT_EXPORT", "aggregatedOutput": "PRIVATE_DO_NOT_EXPORT"},
                {"id": "ready_01", "type": "agentMessage", "phase": "final_answer", "text": "SD_QUAL_08_READY"}]}]}}


class NativeStandaloneTests(unittest.TestCase):
    def test_observe_writes_verified_sd08_ack_and_audit_accepts_business_evidence(self):
        docs = pathlib.Path(__file__).resolve().parent.parent / "docs"
        intent = json.loads((docs / "native-sd-qual-08-intent.json").read_bytes())
        observation = json.loads((docs / "native-sd-qual-08-send-observation.json").read_bytes())
        ack = json.loads((docs / "native-sd-qual-08-job-ack-r1.json").read_bytes())
        original_audit = (docs / "native-sd-qual-08-audit-input.json").read_bytes()
        root = intent["controller_thread_id"]
        value = binding()
        value.update(nonce=intent["delivery_id"][:-len("_delivery")], controller_thread_id=root)
        value["prior_turn_id"] = TURN
        inspection = copy.deepcopy(observation["go_inspection"])
        inspection["ack"] = ack
        thread = thread_result()
        thread["thread"].update(id=root, cwd=value["expected_cwd"], historyMode="paginated")
        thread["thread"]["turns"].append({"id": "new_turn", "status": "completed", "itemsView": "full", "items": [
            {"id": "delivery_01", "type": "functionCallOutput", "name": "g0_delivery",
             "namespace": "orchestration", "output": probe.canonical(intent)},
            {"id": "ack_final", "type": "agentMessage", "phase": "final_answer", "text": "SD_QUAL_08_ACK"}]})
        no_ack = copy.deepcopy(inspection)
        no_ack["ack"] = None
        now = {"boot_id": BOOT, "clock_impl": probe.CLOCK_IMPL, "mono_ns": 1_000_000_000, "wall_ns": 10_000_000_000}
        attempt = {"boot_id": BOOT, "clock_impl": probe.CLOCK_IMPL, "mono_ns": 0, "wall_ns": 0}

        class Reader:
            checks, frames, bytes, server_requests, last_hash = {}, 0, 0, 0, None
            last_request = {"id": 1, "method": "thread/read", "params": {"threadId": ROOT, "includeTurns": True}}
            def __init__(self): self.reads = 0
            async def call(self, method, params, intent=None):
                self.last_request = {"id": self.reads + 1, "method": method, "params": params}
                self.reads += 1
                return thread
            async def wake(self):
                return None

        reader = Reader()
        go_values = iter((no_ack, inspection))
        task_tmp = pathlib.Path(__file__).resolve().parent.parent / "tmp"
        task_tmp.mkdir(mode=0o700, exist_ok=True)
        with tempfile.TemporaryDirectory(prefix="sd08-ack-capture-", dir=task_tmp) as directory:
            with probe.Case(directory) as case, patch.object(probe, "verify_machine"), \
                 patch.object(probe, "current_clock", return_value=now), \
                 patch.object(probe, "go_read", side_effect=lambda _binding, _verb, _deadline, allow_ack=False, **_kwargs:
                              probe.validate_go(next(go_values), value, allow_ack=allow_ack)):
                result = asyncio.run(probe.observe(reader, case, value, intent, attempt, SESSION))
                self.assertEqual(result["status"], "observed")
                audit_input = json.loads((pathlib.Path(directory) / "audit-input.json").read_bytes())
                self.assertEqual(audit_input["acks"], [probe.audit_ack(intent, inspection)])
                audited = delivery_audit.audit(audit_input)
                self.assertTrue(audited["business_ack_complete"])
                self.assertEqual(audited["acknowledged_event_ids"], [intent["events"][0]["event_id"]])
                self.assertFalse(audited["source_service_verified"])
                self.assertFalse(audited["captured_page_chain_complete"])
                self.assertFalse(audited["absence_proof"])
                self.assertFalse(audited["resend_authorized"])
        self.assertEqual((docs / "native-sd-qual-08-audit-input.json").read_bytes(), original_audit)

    def test_sd08_verified_go_ack_projects_into_audit_record_without_mutating_history(self):
        docs = pathlib.Path(__file__).resolve().parent.parent / "docs"
        intent = json.loads((docs / "native-sd-qual-08-intent.json").read_bytes())
        observation = json.loads((docs / "native-sd-qual-08-send-observation.json").read_bytes())
        ack = json.loads((docs / "native-sd-qual-08-job-ack-r1.json").read_bytes())
        original_audit = (docs / "native-sd-qual-08-audit-input.json").read_bytes()
        original_hash = hashlib.sha256(original_audit).hexdigest()
        inspection = dict(observation["go_inspection"])
        inspection["ack"] = ack

        projected = probe.audit_ack(intent, inspection)
        self.assertEqual(projected, {
            "delivery_id": intent["delivery_id"], "controller_thread_id": intent["controller_thread_id"],
            "controller_epoch": intent["controller_epoch"], "event_id": intent["events"][0]["event_id"],
            "event_revision": 1, "event_hash": intent["events"][0]["payload_hash"], "action_slot": "ack_r1",
            "decision": "handled", "command_id": "sd-qual-08-ack-1", "decision_count": 1, "effect_count": 1})
        # The historical empty-ACK artifact remains byte-for-byte unchanged.
        self.assertEqual(hashlib.sha256((docs / "native-sd-qual-08-audit-input.json").read_bytes()).hexdigest(), original_hash)
        self.assertEqual((docs / "native-sd-qual-08-audit-input.json").read_bytes(), original_audit)

        missing = copy.deepcopy(inspection)
        missing["ack"] = None
        with self.assertRaises(probe.ProbeError):
            probe.audit_ack(intent, missing)
        for field, value in (("nonce", "other-root"), ("controller_thread", "other-root"),
                             ("revision", 2), ("event_hash", "0" * 64)):
            changed = copy.deepcopy(inspection)
            changed["ack"][field] = value
            with self.subTest(field=field), self.assertRaises(probe.ProbeError):
                probe.audit_ack(intent, changed)

    def test_current_cli_pin_rejects_old_or_mixed_bindings(self):
        good = binding()
        try:
            probe.validate_binding(good, ROOT, NONCE)
        except probe.ProbeError as error:
            self.fail("current installed CLI rejected: " + error.code)
        old_sha = "b973d440acac501fd2594a43e7ca9ce41e0a65b9dfb28d0d7a7837c99e1261e3"
        for version, old_roles in [("0.153.4", ("service", "tui")), ("0.153.4", ()),
                                   ("0.154.0", ("service",)), ("0.154.0", ("tui",)),
                                   ("0.154.0", ("service", "tui"))]:
            changed = copy.deepcopy(good)
            changed["native_cli_version"] = version
            for role in old_roles:
                changed[role]["native_binary_sha256"] = old_sha
            with self.subTest(version=version, old_roles=old_roles), self.assertRaisesRegex(probe.ProbeError, "^version_mismatch$"):
                probe.validate_binding(changed, ROOT, NONCE)

    def test_active_case_is_08_and_earlier_cases_are_rejected(self):
        value = binding()
        value["nonce"] = "sd-qual-08"
        try:
            probe.validate_binding(value, ROOT, "sd-qual-08")
        except probe.ProbeError as error:
            self.fail("registered new case rejected: " + error.code)
        for nonce in ("sd-qual-01", "sd-qual-02", "sd-qual-03", "sd-qual-04", "sd-qual-05", "sd-qual-06", "sd-qual-07"):
            value["nonce"] = nonce
            with self.subTest(nonce=nonce), self.assertRaises(probe.ProbeError):
                probe.validate_binding(value, ROOT, nonce)

    def test_fresh_family_requires_explicit_null_ancestry_and_uuid_session(self):
        changes = [("parentThreadId", "missing"), ("parentThreadId", ROOT), ("parentThreadId", False),
                   ("forkedFromId", "missing"), ("forkedFromId", ROOT),
                   ("ephemeral", "missing"), ("ephemeral", True),
                   ("sessionId", "missing"), ("sessionId", None), ("sessionId", "not-a-uuid"),
                   ("sessionId", SESSION.upper()), ("sessionId", {"body": "CANARY"})]
        for key, value in changes:
            data = thread_result()
            if value == "missing": del data["thread"][key]
            else: data["thread"][key] = value
            with self.subTest(key=key, value=value), self.assertRaises(probe.ProbeError):
                probe.filter_thread(data, binding(), None, require_ready=True)

    def test_prepare_pins_distinct_session_and_rechecks_it_before_send_and_during_observe(self):
        task_tmp = pathlib.Path(__file__).resolve().parent.parent / "tmp"
        task_tmp.mkdir(mode=0o700, exist_ok=True)
        for stage in ("missing_meta", "before_send", "observe"):
            calls, current_session = [], [SESSION]
            class Reader:
                checks, frames, bytes, server_requests, last_hash = {}, 0, 0, 0, None
                def __init__(self, *_, **__):
                    pass
                async def __aenter__(self):
                    return self
                async def __aexit__(self, *_):
                    pass
                async def initialize(self):
                    pass
                async def call(self, method, params, intent=None):
                    calls.append(method)
                    if method == "thread/read":
                        self.last_request = {"id": len(calls), "method": method, "params": params}
                        result = thread_result()
                        result["thread"]["sessionId"] = current_session[0]
                        return result
                    if method == "turn/start":
                        current_session[0] = BOOT  # Explicit synthetic session change after sending.
                        return {"turn": {"id": "new_turn", "status": "inProgress"}}
                    raise AssertionError("unexpected RPC in session fixture")
                async def wake(self):
                    raise probe.ProbeError("fixture_observation_end")
            def clock():
                return {"boot_id": BOOT, "clock_impl": probe.CLOCK_IMPL, "mono_ns": probe.continuous_ns(), "wall_ns": 0}
            def go(_binding, verb, _deadline, allow_ack=False, **_kwargs):
                value = go_inspection()
                return value["result"] if verb == "read" else value
            with self.subTest(stage=stage), tempfile.TemporaryDirectory(prefix="session-check-", dir=task_tmp) as directory:
                value = binding()
                value["controller_evidence_sha256"] = hashlib.sha256(b"{}\n").hexdigest()
                with probe.Case(directory) as case:
                    case.write_new("binding.json", value)
                    case.write_new("controller-evidence.json", {})
                with patch.object(probe, "Rpc", Reader), patch.object(probe, "verify_machine"), patch.object(probe, "current_clock", side_effect=clock), patch.object(probe, "go_read", side_effect=go):
                    args = ["prepare", "--case", directory, "--root", ROOT, "--nonce", NONCE]
                    result = probe.main(args)
                    self.assertEqual(result["status"], "prepared")
                    meta_path = pathlib.Path(directory) / "prepare-meta.json"
                    meta = json.loads(meta_path.read_bytes())
                    self.assertEqual(meta.get("session_id"), SESSION)
                    self.assertNotEqual(meta["session_id"], ROOT)
                    self.assertTrue(meta["prior_facts"]["fresh_root_family"])
                    if stage == "missing_meta":
                        del meta["session_id"]
                        meta_path.write_text(json.dumps(meta))
                    elif stage == "before_send":
                        current_session[0] = BOOT
                    args[0] = "send-once"
                    result = probe.main(args)
                    self.assertEqual(result["reason"], "invalid_session_id" if stage == "missing_meta" else "session_changed")
                    self.assertFalse(result["resend_authorized"])
                    if stage == "observe":
                        self.assertEqual(result["status"], "uncertain")
                        args[0] = "observe"
                        self.assertEqual(probe.main(args)["reason"], "session_changed")
                        self.assertEqual(calls.count("turn/start"), 1)
                    else:
                        self.assertEqual(result["status"], "rejected")
                        self.assertNotIn("turn/start", calls)
                        self.assertFalse((pathlib.Path(directory) / "send-attempt.json").exists())

    def test_native_rpc_error_keeps_only_matched_method_code_and_message_digest(self):
        message = "CANARY_RPC_MESSAGE_错误"
        class Wire:
            async def send(self, _raw):
                pass
            async def recv(self):
                return json.dumps({"id": 1, "error": {"code": -32601, "message": message, "data": {"body": "CANARY_RPC_DATA"}}})
        rpc = probe.Rpc(binding(), probe.continuous_ns() + 10_000_000_000)
        rpc.ws = Wire()
        with self.assertRaises(probe.ProbeError) as caught:
            asyncio.run(rpc.call("thread/read", {"threadId": ROOT, "includeTurns": True}))
        self.assertEqual(caught.exception.code, "native_rpc_error")
        self.assertEqual(getattr(caught.exception, "rpc_error", None), {
            "method": "thread/read", "request_id": 1, "code": -32601,
            "message_bytes": len(message.encode()), "message_sha256": hashlib.sha256(message.encode()).hexdigest()})
        self.assertNotIn("CANARY", probe.canonical(vars(caught.exception)))

    def test_native_rpc_error_rejects_untyped_details_and_unmatched_ids(self):
        bad = [{"code": True, "message": "CANARY"}, {"code": "-32601", "message": "CANARY"},
               {"code": 2**63, "message": "CANARY"}, {"code": -32601, "message": {"body": "CANARY"}},
               {"code": -32601, "message": "\ud800"}, {"code": -32601},
               {"code": -32601, "message": "CANARY", "unknown": "CANARY"}]
        for response_id, error, reason in [(1, value, "invalid_rpc_error") for value in bad] + [
                (99, {"code": -32601, "message": "CANARY"}, "rpc_response_id_mismatch")]:
            class Wire:
                async def send(self, _raw):
                    pass
                async def recv(self):
                    return json.dumps({"id": response_id, "error": error})
            rpc = probe.Rpc(binding(), probe.continuous_ns() + 10_000_000_000)
            rpc.ws = Wire()
            with self.subTest(reason=reason, response_id=response_id), self.assertRaises(probe.ProbeError) as caught:
                asyncio.run(rpc.call("thread/read", {"threadId": ROOT, "includeTurns": True}))
            self.assertEqual(caught.exception.code, reason)
            self.assertIsNone(getattr(caught.exception, "rpc_error", None))
            self.assertNotIn("CANARY", probe.canonical(vars(caught.exception)))

    def test_rpc_error_survives_prepare_send_and_observe_without_authorizing_resend(self):
        message = "CANARY_NATIVE_REJECTION"
        task_tmp = pathlib.Path(__file__).resolve().parent.parent / "tmp"
        task_tmp.mkdir(mode=0o700, exist_ok=True)
        for stage, method, request_id in (("initialize", "initialize", 1), ("prepare", "thread/read", 4),
                                          ("send", "turn/start", 5), ("observe", "thread/read", 6)):
            calls, active = [], [stage in ("initialize", "prepare")]
            class Wire:
                async def send(self, raw):
                    request = json.loads(raw)
                    calls.append(request["method"])
                    if request["method"] == "initialized":
                        return
                    reject = active[0] and request["method"] == method and request["id"] == request_id
                    if reject:
                        self.reply = {"id": request["id"], "error": {"code": -32601, "message": message, "data": {"body": "CANARY_DATA"}}}
                        return
                    results = {"initialize": {"platformOs": "macos", "platformFamily": "unix", "userAgent": "fixture", "codexHome": str(_SYNTHETIC_HOME)},
                               "server/diagnostics": {"process": {"id": 1234}}, "remoteControl/status/read": {"status": "disabled"},
                               "thread/read": thread_result(), "turn/start": {"turn": {"id": "new_turn", "status": "inProgress"}}}
                    self.reply = {"id": request["id"], "result": results[request["method"]]}
                async def recv(self):
                    return json.dumps(self.reply)
                async def close(self):
                    pass
            async def connect(rpc):
                rpc.ws = Wire()
                return rpc
            def clock():
                return {"boot_id": BOOT, "clock_impl": probe.CLOCK_IMPL, "mono_ns": probe.continuous_ns(), "wall_ns": 0}
            def go(_binding, verb, _deadline, allow_ack=False, **_kwargs):
                value = go_inspection()
                return value["result"] if verb == "read" else value
            with self.subTest(stage=stage), tempfile.TemporaryDirectory(prefix="rpc-error-", dir=task_tmp) as directory:
                value = binding()
                evidence = b"{}\n"
                value["controller_evidence_sha256"] = hashlib.sha256(evidence).hexdigest()
                with probe.Case(directory) as case:
                    case.write_new("binding.json", value)
                    case.write_new("controller-evidence.json", {})
                with patch.object(probe.Rpc, "__aenter__", connect), patch.object(probe, "verify_machine"), patch.object(probe, "current_clock", side_effect=clock), patch.object(probe, "go_read", side_effect=go):
                    args = ["prepare", "--case", directory, "--root", ROOT, "--nonce", NONCE]
                    result = probe.main(args)
                    if stage in ("send", "observe"):
                        self.assertEqual(result["status"], "prepared")
                        active[0] = True
                        args[0] = "send-once"
                        result = probe.main(args)
                    expected = {"method": method, "request_id": request_id, "code": -32601,
                                "message_bytes": len(message.encode()), "message_sha256": hashlib.sha256(message.encode()).hexdigest()}
                    self.assertEqual(result["status"], "rejected" if stage in ("initialize", "prepare") else "uncertain")
                    self.assertEqual(result["reason"], "native_rpc_error")
                    self.assertEqual(result.get("rpc_error"), expected)
                    self.assertFalse(result["resend_authorized"])
                    self.assertNotIn("CANARY", probe.canonical(result))
                    if stage in ("send", "observe"):
                        recorded = json.loads((pathlib.Path(directory) / "send-observation.json").read_bytes())
                        self.assertEqual(recorded["rpc_error"], expected)
                        attempt = (pathlib.Path(directory) / "send-attempt.json").read_bytes()
                        self.assertEqual(probe.main(args)["reason"], "send_intent_exists")
                        self.assertEqual((pathlib.Path(directory) / "send-attempt.json").read_bytes(), attempt)
                        self.assertEqual(calls.count("turn/start"), 1)
                    else:
                        self.assertFalse((pathlib.Path(directory) / "send-attempt.json").exists())

    def test_live_kernel_executable_must_match_for_each_role_even_without_rehash(self):
        value = binding()
        for role in ("service", "tui"):
            value[role].update(comm="codex", executable_path="/private/g0/codex")
        def ps(args, _timeout):
            process = next(value[role] for role in ("service", "tui") if str(value[role]["pid"]) == args[2])
            return f'{process["pid"]} {process["uid"]} {process["birth"]} {process["comm"]}\n'.encode()
        info = SimpleNamespace(st_mode=stat.S_IFSOCK | 0o600, st_uid=os.getuid(), st_dev=1, st_ino=2)
        for changed_role in ("service", "tui"):
            def kernel_path(pid):
                return "/unexpected/native" if pid == value[changed_role]["pid"] else "/private/g0/codex"
            with self.subTest(role=changed_role), patch.object(probe, "command_output", side_effect=ps), patch.object(probe, "current_clock", return_value={"boot_id": BOOT}), patch.object(probe.os, "lstat", return_value=info), patch.object(probe, "process_executable_path", side_effect=kernel_path, create=True):
                with self.assertRaises(probe.ProbeError):
                    probe.verify_machine(value, hashes=False)

    def test_comm_and_executable_path_are_separate_required_observations(self):
        value = binding()
        for role in ("service", "tui"):
            value[role]["comm"] = "codex"
            value[role]["executable_path"] = "/private/g0/codex"
        try:
            probe.validate_binding(value, ROOT, NONCE)
        except probe.ProbeError as error:
            self.fail("observed basename comm with separate executable rejected: " + error.code)
        for role in ("service", "tui"):
            for change in ("missing_executable", "relative_executable", "bad_comm"):
                changed = copy.deepcopy(value)
                if change == "missing_executable": del changed[role]["executable_path"]
                elif change == "relative_executable": changed[role]["executable_path"] = "codex"
                else: changed[role]["comm"] = "codex\nother"
                with self.subTest(role=role, change=change), self.assertRaises(probe.ProbeError):
                    probe.validate_binding(changed, ROOT, NONCE)

    def test_prepare_rechecks_deadline_after_final_publication_and_cannot_be_rerun(self):
        class Reader:
            checks = {}
            def __init__(self, *_, **__):
                pass
            async def __aenter__(self):
                return self
            async def __aexit__(self, *_args):
                pass
            async def initialize(self):
                pass
            async def call(_reader, method, params):
                self.assertEqual(method, "thread/read")
                self.assertEqual(params, {"threadId": ROOT, "includeTurns": True})
                return thread_result()

        def output(args, timeout=probe.READ_SECONDS):
            if args == ["/usr/sbin/sysctl", "-n", "kern.bootsessionuuid"]:
                return (BOOT + "\n").encode()
            self.assertIn(args, [[binding()["go_binary"], verb, "--dir", binding()["job_dir"], "--nonce", NONCE] for verb in ("read", "inspect")])
            inspection = go_inspection()
            return json.dumps(inspection["result"] if args[1] == "read" else inspection).encode()

        for delayed_file in ("producer.json", "prepare-meta.json"):
            clock = [100_000_000_000]
            class SlowPublication(probe.Case):
                def write_new(self, name, value):
                    super().write_new(name, value)
                    if name == delayed_file:
                        clock[0] = 111_000_000_000
            with self.subTest(delayed_file=delayed_file), tempfile.TemporaryDirectory(prefix="g0-prepare-deadline-") as directory:
                os.chmod(directory, 0o700)
                with SlowPublication(directory) as case, patch.object(probe, "continuous_ns", side_effect=lambda: clock[0]), patch.object(probe, "verify_machine"), patch.object(probe, "Rpc", Reader), patch.object(probe, "command_output", side_effect=output):
                    with self.assertRaises(probe.ProbeError) as caught:
                        asyncio.run(probe.run("prepare", case, binding(), "a" * 64))
                    self.assertEqual(caught.exception.code, "read_window_expired")
                    saved = {name: case.read_raw(name) for name in ("producer.json", "intent.json", "prepare-meta.json")}
                    with self.assertRaises(probe.ProbeError) as repeated:
                        asyncio.run(probe.run("prepare", case, binding(), "a" * 64))
                    self.assertEqual(repeated.exception.code, "artifact_exists")
                    self.assertEqual(saved, {name: case.read_raw(name) for name in saved})

    def test_new_helper_does_not_admit_the_old_case_even_with_matching_arguments(self):
        old = binding()
        old["nonce"] = "sd-qual-01"
        with self.assertRaises(probe.ProbeError):
            probe.validate_binding(old, ROOT, "sd-qual-01")

    def test_clock_record_uses_continuous_raw_time_instead_of_uptime(self):
        with patch.object(probe, "command_output", return_value=(BOOT + "\n").encode()), patch.object(probe.time, "clock_gettime_ns", return_value=100_000_000_000), patch.object(probe.time, "monotonic_ns", return_value=7_000_000_000), patch.object(probe.time, "time_ns", return_value=500_000_000_000):
            record = probe.current_clock()
        self.assertEqual(record["mono_ns"], 100_000_000_000)
        self.assertEqual(record["clock_impl"], "clock_gettime_ns(CLOCK_MONOTONIC_RAW)")
        self.assertEqual(record["wall_ns"], 500_000_000_000)

    def test_wall_jumps_do_not_change_the_original_continuous_120_second_budget(self):
        attempt = {"boot_id": BOOT, "clock_impl": "clock_gettime_ns(CLOCK_MONOTONIC_RAW)", "mono_ns": 100_000_000_000, "wall_ns": 1_000_000_000_000}
        for wall in (-86_400_000_000_000, 86_400_000_000_000):
            now = {**attempt, "mono_ns": 160_000_000_000, "wall_ns": wall}
            try:
                remaining = probe.remaining_window(attempt, now)
            except probe.ProbeError as error:
                self.fail("wall-only adjustment affected elapsed budget: " + error.code)
            self.assertEqual(remaining, 60.0)
            with self.assertRaises(probe.ProbeError):
                probe.remaining_window(attempt, {**now, "mono_ns": 220_000_000_000})

    def test_old_uptime_clock_records_are_rejected_without_conversion(self):
        old = {"boot_id": BOOT, "clock_impl": "mach_absolute_time()", "mono_ns": 100_000_000_000, "wall_ns": 1_000_000_000_000}
        with self.assertRaises(probe.ProbeError):
            probe.remaining_window(old, {**old, "mono_ns": 101_000_000_000, "wall_ns": 1_001_000_000_000})

    def test_system_sleep_elapsed_before_rpc_send_prevents_the_wire_write(self):
        class Wire:
            sent = False
            async def send(self, _message):
                self.sent = True
            async def recv(self):
                return '{"id":1,"result":{}}'
        wire = Wire()
        rpc = probe.Rpc(binding(), 220_000_000_000)
        rpc.ws = wire
        intent = probe.make_intent(binding(), go_inspection())
        params = {"threadId": ROOT, "input": [], "toolOutput": {"name": "g0_delivery", "namespace": "orchestration", "output": probe.canonical(intent)}}
        with warnings.catch_warnings(record=True) as caught, patch.object(probe.time, "monotonic", return_value=100.0), patch.object(probe.time, "clock_gettime_ns", return_value=221_000_000_000):
            warnings.simplefilter("always")
            with self.assertRaises(probe.ProbeError):
                asyncio.run(rpc.call("turn/start", params, intent=intent))
        self.assertEqual(caught, [])
        self.assertFalse(wire.sent)

    def test_response_received_after_continuous_deadline_cannot_be_success(self):
        clock = [100_000_000_000]
        class Wire:
            async def send(self, _message):
                pass
            async def recv(self):
                clock[0] = 111_000_000_000
                return '{"id":1,"result":{"accepted":true}}'
        rpc = probe.Rpc(binding(), 110_000_000_000)
        rpc.ws = Wire()
        with patch.object(probe.time, "monotonic", return_value=100.0), patch.object(probe.time, "clock_gettime_ns", side_effect=lambda _: clock[0]):
            with self.assertRaises(probe.ProbeError):
                asyncio.run(rpc.call("thread/read", {"threadId": ROOT, "includeTurns": True}))

    def test_shared_service_requires_exact_vscode_origin_and_rejects_other_sources(self):
        try:
            probe.filter_thread(thread_result("vscode"), binding(), None, require_ready=True)
        except probe.ProbeError as error:
            self.fail("fixed shared-service origin rejected: " + error.code)
        for source in ("cli", "appServer", "unknown", {"subAgent": {}}, None):
            with self.subTest(source=source), self.assertRaises(probe.ProbeError):
                probe.filter_thread(thread_result(source), binding(), None, require_ready=True)

    def test_prior_timestamp_is_exact_native_field_not_ready_text_time(self):
        data = thread_result()
        data["thread"]["turns"][0]["completedAt"] = 1790000000
        _, facts = probe.filter_thread(data, binding(), None, require_ready=True)
        self.assertEqual(facts.get("prior_completed_at"), 1790000000)
        data["thread"]["turns"][0]["completedAt"] = True
        with self.assertRaises(probe.ProbeError):
            probe.filter_thread(data, binding(), None, require_ready=True)

    def test_duplicate_turn_id_in_one_read_is_rejected_but_next_read_can_update(self):
        intent, data = probe.make_intent(binding(), go_inspection()), thread_result()
        data["thread"]["turns"].append({"id": "new_turn", "status": "completed", "itemsView": "full", "items": [
            {"id": "standalone_01", "type": "functionCallOutput", "name": "g0_delivery", "namespace": "orchestration", "output": probe.canonical(intent)},
            {"id": "ack_final", "type": "agentMessage", "phase": "final_answer", "text": "SD_QUAL_08_ACK"}]})
        probe.filter_thread(data, binding(), intent)
        changed = copy.deepcopy(data)
        changed["thread"]["turns"][-1]["status"] = "inProgress"
        probe.filter_thread(changed, binding(), intent)
        data["thread"]["turns"].append({"id": "new_turn", "status": "inProgress", "itemsView": "full", "items": [
            {"id": "new_input", "type": "userMessage", "content": []}]})
        with self.assertRaises(probe.ProbeError):
            probe.filter_thread(data, binding(), intent)

    def test_paginated_thread_read_full_compatibility_is_accepted_but_partial_is_rejected(self):
        data = thread_result()
        data["thread"]["historyMode"] = "paginated"
        try:
            filtered, facts = probe.filter_thread(data, binding(), None, require_ready=True)
        except probe.ProbeError as error:
            self.fail("native full compatibility response rejected: " + error.code)
        self.assertEqual(filtered["thread"]["historyMode"], "paginated")
        self.assertTrue(facts["root_idle"])
        for view in ("summary", "notLoaded", None):
            partial = copy.deepcopy(data)
            partial["thread"]["turns"][0]["itemsView"] = view
            with self.subTest(view=view), self.assertRaises(probe.ProbeError):
                probe.filter_thread(partial, binding(), None, require_ready=True)

    def test_send_intent_precedes_one_wire_write_and_receipt_is_not_history(self):
        class Wire:
            async def call(self, method, params, **_):
                self.attempt = case.read("send-attempt.json")
                self.params = params
                return {"turn": {"id": "new_turn", "status": "inProgress", "items": [{"text": "PRIVATE_DO_NOT_EXPORT"}], "itemsView": "notLoaded"}}
        wire = Wire()
        now = {"boot_id": BOOT, "clock_impl": "clock_gettime_ns(CLOCK_MONOTONIC_RAW)", "mono_ns": 1_000_000_000, "wall_ns": 10_000_000_000}
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
        now = {"boot_id": BOOT, "clock_impl": "clock_gettime_ns(CLOCK_MONOTONIC_RAW)", "mono_ns": 1_000_000_000, "wall_ns": 10_000_000_000}
        with tempfile.TemporaryDirectory(prefix="g0-standalone-case-") as directory:
            os.chmod(directory, 0o700)
            with probe.Case(directory) as case, patch.object(probe, "verify_machine", side_effect=probe.ProbeError("tui_identity_changed")), patch.object(probe, "current_clock", return_value=now):
                with self.assertRaises(probe.ProbeError):
                    asyncio.run(probe.issue_once(Wire(), case, binding(), probe.make_intent(binding(), go_inspection()), "b" * 64))
                self.assertEqual(case.read("send-attempt.json")["status"], "send_intent")

    def test_binding_rejects_wrong_owner_root_version_and_unknown_fields(self):
        good = binding()
        self.assertEqual(probe.validate_binding(good, ROOT, NONCE), good)
        for field, value in [("controller_thread_id", TURN), ("native_cli_version", "0.153.4"), ("nonce", "other"), ("attached", True)]:
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
        for place, field, value in [("thread", "source", "appServer"), ("thread", "cliVersion", "0.153.4"),
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

    def test_rpc_reader_rejects_wrong_response_id_without_exporting_raw_error(self):
        class Wire:
            async def send(self, _message):
                pass
            async def recv(self):
                return '{"id":99,"result":{"text":"PRIVATE_DO_NOT_EXPORT"}}'
        rpc = probe.Rpc(binding(), probe.time.clock_gettime_ns(probe.time.CLOCK_MONOTONIC_RAW) + 10_000_000_000)
        rpc.ws = Wire()
        with self.assertRaises(probe.ProbeError) as caught:
            asyncio.run(rpc.call("thread/read", {"threadId": ROOT, "includeTurns": True}))
        self.assertNotIn("PRIVATE_DO_NOT_EXPORT", str(caught.exception))

    def test_unknown_function_output_namespace_never_escapes_projection(self):
        data = thread_result()
        data["thread"]["turns"][0]["items"].append({"id": "other_output", "type": "functionCallOutput", "name": "other_tool",
            "namespace": {"secret": "PRIVATE_DO_NOT_EXPORT"}, "output": "PRIVATE_DO_NOT_EXPORT"})
        with self.assertRaises(probe.ProbeError):
            probe.filter_thread(data, binding(), None, require_ready=True)

    def test_completed_ack_does_not_qualify_while_owned_root_is_still_active(self):
        intent, inspection, data = probe.make_intent(binding(), go_inspection()), go_inspection(), thread_result()
        inspection["ack"] = {"version": 1, "status": "acknowledged", "nonce": NONCE, "controller_thread": ROOT,
            "revision": 1, "event_hash": inspection["event_hash"], "command_id": "00000000-0000-4000-8000-000000000001",
            "decision": "handled", "decision_count": 1, "effect_count": 1}
        data["thread"]["status"] = {"type": "active"}
        data["thread"]["turns"].append({"id": "new_turn", "status": "completed", "itemsView": "full", "items": [
            {"id": "standalone_01", "type": "functionCallOutput", "name": "g0_delivery", "namespace": "orchestration", "output": probe.canonical(intent)},
            {"id": "ack_final", "type": "agentMessage", "phase": "final_answer", "text": "SD_QUAL_08_ACK"}]})
        class Reader:
            checks, frames, bytes, server_requests, last_hash = {}, 1, 100, 0, None
            last_request = {"id": 4, "method": "thread/read", "params": {"threadId": ROOT, "includeTurns": True}}
            async def call(self, *_args):
                return data
            async def wake(self):
                raise probe.ProbeError("sample_ended")
        now = {"boot_id": BOOT, "clock_impl": "clock_gettime_ns(CLOCK_MONOTONIC_RAW)", "mono_ns": 1_000_000_000, "wall_ns": 10_000_000_000}
        with tempfile.TemporaryDirectory(prefix="g0-standalone-case-") as directory:
            os.chmod(directory, 0o700)
            with probe.Case(directory) as case, patch.object(probe, "verify_machine"), patch.object(probe, "current_clock", return_value=now), patch.object(probe, "go_read", return_value=inspection):
                result = asyncio.run(probe.observe(Reader(), case, binding(), intent, now, SESSION))
                self.assertEqual(result["status"], "uncertain")

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
        attempt = {"boot_id": BOOT, "clock_impl": "clock_gettime_ns(CLOCK_MONOTONIC_RAW)", "mono_ns": 1_000_000_000, "wall_ns": 10_000_000_000}
        now = {"boot_id": BOOT, "clock_impl": "clock_gettime_ns(CLOCK_MONOTONIC_RAW)", "mono_ns": 2_000_000_000, "wall_ns": 11_000_000_000}
        self.assertEqual(probe.remaining_window(attempt, now), 119.0)
        for field, value in [("boot_id", ROOT), ("clock_impl", "different"), ("mono_ns", 121_000_000_001)]:
            changed = dict(now)
            changed[field] = value
            with self.subTest(field=field), self.assertRaises(probe.ProbeError):
                probe.remaining_window(attempt, changed)


if __name__ == "__main__":
    unittest.main()
