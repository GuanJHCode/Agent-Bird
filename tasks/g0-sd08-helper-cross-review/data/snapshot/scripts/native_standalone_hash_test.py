"""Actual sd03 prepared bytes, isolated test Case and fake wire; no native run."""
import ast
import copy
import hashlib
import importlib
import json
import os
from pathlib import Path
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(Path(os.environ.get("G0_SD_HELPER_SCRIPTS", str(HERE)))))
import native_standalone as helper
sys.path.insert(0, str(Path(os.environ.get("G0_SD_CONTROLLER_SCRIPTS", str(HERE)))))
control = importlib.import_module("native_sd_control")
FIXTURE = json.loads((HERE.parent / "docs/standalone-helper-sd03-prepared-fixture.json").read_bytes())


class BindingHashBoundaryTest(unittest.TestCase):
    def test_real_writer_helper_attempt_and_controller_reader_share_one_binding_digest(self):
        original = {name: raw.encode() for name, raw in FIXTURE["files_utf8"].items()}
        self.assertEqual(set(original), {"binding.json", "controller-evidence.json", "producer.json", "intent.json", "prepare-meta.json"})
        for name, raw in original.items():
            self.assertEqual(hashlib.sha256(raw).hexdigest(), FIXTURE["original_sha256"][name])
        binding = json.loads(original["binding.json"])
        prepared = json.loads(original["prepare-meta.json"])
        producer = json.loads(original["producer.json"])
        self.assertEqual(helper.ACTIVE_NONCE, "sd-qual-08")
        self.assertEqual(binding["nonce"], "sd-qual-03")
        self.assertEqual(prepared["binding_sha256"], "dab2c091901e473b5783ea1a43c5a11600f9541a2d308d95dedc18deeb73d15c")

        # Execute the actual production assignment without launching its controller.
        # This catches a correct new digest function that the caller forgot to use.
        tree = ast.parse(Path(control.__file__).read_bytes())
        callers = [n for n in ast.walk(tree) if isinstance(n, ast.FunctionDef) and n.name == "helper"]
        self.assertEqual(len(callers), 1)
        assignments = [n for n in ast.walk(callers[0]) if isinstance(n, ast.Assign) and
                       any(isinstance(target, ast.Name) and target.id == "binding_sha" for target in n.targets)]
        self.assertEqual(len(assignments), 1)
        digest_expression = compile(ast.Expression(assignments[0].value), "controller_actual_binding_digest", "eval")

        sent = []
        class Wire:
            async def send(_wire, raw):
                request = json.loads(raw)
                method = request["method"]
                sent.append(method)
                if method == "initialized":
                    return
                if method == "initialize":
                    result = {"platformOs": "macos", "platformFamily": "unix", "userAgent": "offline", "codexHome": "offline"}
                elif method == "server/diagnostics":
                    result = {"process": {"id": binding["service"]["pid"]}}
                elif method == "remoteControl/status/read":
                    result = {"status": "disabled"}
                elif method == "turn/start":
                    self.assertEqual(request["params"], {"threadId": binding["controller_thread_id"], "input": [],
                        "toolOutput": {"name": "g0_delivery", "namespace": "orchestration", "output": helper.canonical(json.loads(original["intent.json"]))}})
                    result = {"turn": {"id": "offline_receipt_turn", "status": "inProgress"}}
                elif method == "thread/read" and "turn/start" not in sent:
                    self.assertEqual(request["params"], {"threadId": binding["controller_thread_id"], "includeTurns": True})
                    result = {"thread": {"id": binding["controller_thread_id"], "sessionId": prepared["session_id"],
                        "source": "vscode", "cliVersion": binding["native_cli_version"], "cwd": binding["expected_cwd"],
                        "parentThreadId": None, "forkedFromId": None, "ephemeral": False, "historyMode": "paginated",
                        "status": {"type": "idle"}, "turns": [{"id": binding["prior_turn_id"], "status": "completed", "itemsView": "full",
                        "items": [{"id": "offline_completed_tool", "type": "commandExecution", "status": "completed", "exitCode": 0},
                                  {"id": "offline_ready", "type": "agentMessage", "phase": "final_answer", "text": "SD_QUAL_03_READY"}]}]}}
                elif method == "thread/read":
                    _wire.reply = {"id": request["id"], "error": {"code": -32601, "message": "offline_stop_after_publish"}}
                    return
                else:
                    raise AssertionError("unexpected RPC in isolated hash fixture")
                _wire.reply = {"id": request["id"], "result": result}
            async def recv(_wire):
                return json.dumps(_wire.reply)
            async def close(_wire):
                pass
        async def connect(rpc):
            rpc.ws = Wire()
            return rpc
        def clock():
            # New test time; the original attempt and its model window are absent.
            return {"boot_id": binding["boot_id"], "clock_impl": helper.CLOCK_IMPL,
                    "mono_ns": helper.continuous_ns(), "wall_ns": time.time_ns()}
        def go(_binding, verb, _deadline, allow_ack=False):
            self.assertEqual(verb, "inspect")
            return copy.deepcopy(producer["inspection"])

        task_tmp = HERE.parent / "tmp"
        task_tmp.mkdir(mode=0o700, exist_ok=True)
        with tempfile.TemporaryDirectory(prefix="binding-hash-", dir=task_tmp) as directory:
            case = Path(directory)
            control.private_json(case / "binding.json", binding)
            self.assertEqual((case / "binding.json").read_bytes(), original["binding.json"])
            self.assertEqual(len(original["binding.json"]), 1668)
            for name, raw in original.items():
                if name != "binding.json":
                    path = case / name
                    path.write_bytes(raw)
                    path.chmod(0o600)
            self.assertFalse((case / "send-attempt.json").exists())
            # Select sd03 and its CLI fixture only here; production pins remain unchanged.
            with (patch.object(helper, "ACTIVE_NONCE", "sd-qual-03"), patch.object(control.c, "NONCE", "sd-qual-03"),
                  patch.object(helper, "CLI_VERSION", binding["native_cli_version"]),
                  patch.object(helper, "CLI_SHA", binding["service"]["native_binary_sha256"]),
                  patch.object(control, "CASE", case), patch.object(helper.Rpc, "__aenter__", connect),
                  patch.object(helper, "verify_machine"), patch.object(helper, "current_clock", side_effect=clock),
                  patch.object(helper, "go_read", side_effect=go)):
                self.assertEqual(binding["nonce"], control.c.NONCE)
                args = ["send-once", "--case", directory, "--root", binding["controller_thread_id"], "--nonce", binding["nonce"]]
                began = control.continuous_ns()
                result = helper.main(args)
                self.assertEqual(result["status"], "uncertain")
                self.assertEqual(result["reason"], "native_rpc_error")
                attempt_raw = (case / "send-attempt.json").read_bytes()
                attempt = json.loads(attempt_raw)
                self.assertEqual(attempt["status"], "send_intent")
                self.assertEqual(attempt["binding_sha256"], prepared["binding_sha256"])
                self.assertEqual(helper.main(args)["reason"], "send_intent_exists")
                self.assertEqual((case / "send-attempt.json").read_bytes(), attempt_raw)
                self.assertEqual(sent.count("turn/start"), 1)
                for name, raw in original.items():
                    self.assertEqual((case / name).read_bytes(), raw)
                controller_hash = eval(digest_expression, vars(control))
                try:
                    window = control.read_send_attempt(binding["controller_thread_id"], binding["boot_id"], controller_hash, began)
                except ValueError as error:
                    self.fail("controller rejected the real helper publication: " + str(error) +
                              "; controller_hash=" + controller_hash + "; helper_hash=" + attempt["binding_sha256"])
                self.assertEqual(controller_hash, prepared["binding_sha256"])
                self.assertNotEqual(controller_hash, hashlib.sha256(original["binding.json"]).hexdigest())
                self.assertEqual(window["deadline_mono_ns"], attempt["mono_ns"] + 120_000_000_000)
                self.assertEqual(window["attempt_sha256"], hashlib.sha256(attempt_raw).hexdigest())


if __name__ == "__main__":
    unittest.main()
