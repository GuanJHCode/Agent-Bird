"""Replay the frozen controller writer into the frozen helper's real local entry.

The existing native initial-phase fixture supplies dead process/clock boundaries.
Only its CLI path is selected during writer replay; production pin values stay
unchanged. Stop at Rpc.__aenter__, before any native connection or RPC response.
"""
import ast
import copy
import hashlib
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

BASE = Path(__file__).resolve().parents[1]
SNAPSHOT = BASE / "data/snapshot"
sys.path.insert(0, str(SNAPSHOT / "scripts"))
import native_sd_control as control
import native_sd_initial_test as initial
import native_standalone as helper


def no_native_network(event, _args):
    if event == "socket.connect":
        raise AssertionError("this review must not connect to a native service")


sys.addaudithook(no_native_network)


class CurrentBindingGate(unittest.TestCase):
    def test_real_current_writer_and_real_main_accept_bound_extended_evidence(self):
        self.assertEqual(control.c.NONCE, helper.ACTIVE_NONCE)
        self.assertEqual(helper.ACTIVE_NONCE, "sd-qual-08")
        self.assertEqual(control.c.CLI_VERSION, helper.CLI_VERSION)
        self.assertEqual(helper.CLI_VERSION, "0.154.0")
        self.assertEqual(control.c.CLI_SHA, helper.CLI_SHA)
        self.assertEqual(control.c.GO_SHA, helper.GO_SHA)
        self.assertEqual(control.CLOCK_IMPL, helper.CLOCK_IMPL)
        self.assertEqual((helper.READ_SECONDS, helper.MODEL_SECONDS), (10, 120))
        # Read-only pin verification; neither executable is launched here.
        for path, expected in ((control.c.CLI.resolve(), helper.CLI_SHA),
                               (control.c.GO, helper.GO_SHA)):
            self.assertEqual(hashlib.sha256(path.read_bytes()).hexdigest(), expected)

        captured = initial.InitialTests().run_initial()
        self.assertEqual(captured["helper_attempts"], ["prepare"])
        files = captured["files"]
        evidence = json.loads(files["controller-evidence.json"])
        launch = json.loads(files["launch-intent.json"])
        origin = json.loads(files["native-origin.json"])
        self.assertEqual(evidence["nonce"], "sd-qual-08")
        self.assertEqual(evidence["session_start"]["session_id"], evidence["candidate_hook"]["session_id"])
        self.assertEqual(evidence["launch_intent_sha256"], hashlib.sha256(files["launch-intent.json"]).hexdigest())
        self.assertEqual(evidence["origin_sha256"], hashlib.sha256(files["native-origin.json"]).hexdigest())
        self.assertEqual(launch["initial_input"]["action"], "initial-argv")
        self.assertEqual(launch["initial_window"]["mono_ns"], 0)
        self.assertEqual(launch["initial_window"]["deadline_mono_ns"], 120_000_000_000)
        self.assertGreater(origin["clock"]["mono_ns"], launch["initial_window"]["mono_ns"])

        tree = ast.parse(Path(control.__file__).read_bytes())
        run = next(node for node in tree.body if isinstance(node, ast.FunctionDef) and node.name == "run")
        body = next(node.body for node in run.body if isinstance(node, ast.Try))
        start = next(index for index, node in enumerate(body) if isinstance(node, ast.Assign)
                     and any(isinstance(target, ast.Name) and target.id == "service_binding" for target in node.targets))
        # Execute the actual binding_process call, socket extension, binding dict
        # construction and private_json publication, not a duplicate hand-built schema.
        writer = body[start:start + 4]
        self.assertIsInstance(writer[-1], ast.Expr)
        self.assertEqual(writer[-1].value.func.id, "private_json")
        writer_code = compile(ast.Module(body=writer, type_ignores=[]), str(control.__file__), "exec")
        call_helper = next(node for node in run.body if isinstance(node, ast.FunctionDef) and node.name == "helper")
        popen = [node for node in ast.walk(call_helper) if isinstance(node, ast.Call)
                 and isinstance(node.func, ast.Attribute) and node.func.attr == "Popen"]
        self.assertEqual(len(popen), 1)
        argv_code = compile(ast.Expression(popen[0].args[0]), str(control.__file__), "eval")

        identities = {evidence["before_send"][role]["pid"]: evidence["before_send"][role]
                      for role in ("service", "tui")}
        synthetic_cli = Path(evidence["before_send"]["service"]["executable_path"])
        root = evidence["root"]
        boot = json.loads(files["binding.json"])["boot_id"]
        with tempfile.TemporaryDirectory(prefix="sd08-current-main-", dir=SNAPSHOT / "tmp") as directory:
            case = Path(directory)
            path = case / "controller-evidence.json"
            path.write_bytes(files["controller-evidence.json"])
            path.chmod(0o600)
            namespace = dict(vars(control), CASE=case, JOB=case / "job", HELPER=Path(helper.__file__),
                             before_send=evidence["before_send"], record=captured["record"],
                             prompt_hook=evidence["candidate_hook"], boot_id=boot, mode="prepare")
            with patch.object(control.c, "CLI", synthetic_cli), \
                    patch.object(control.c, "process", side_effect=lambda pid: copy.deepcopy(identities[pid])):
                exec(writer_code, namespace)
            binding = namespace["binding"]
            binding_raw = (case / "binding.json").read_bytes()
            self.assertEqual(helper.validate_binding(binding, root, "sd-qual-08"), binding)
            self.assertEqual(binding["controller_evidence_sha256"], hashlib.sha256(path.read_bytes()).hexdigest())
            digest = control.canonical_binding_sha(case / "binding.json")
            argv = eval(argv_code, namespace)
            self.assertEqual(argv[:3], [sys.executable, "-B", str(Path(helper.__file__))])

            entered = []
            real_run = helper.run
            async def observe_run(verb, local_case, actual_binding, binding_hash):
                self.assertEqual(verb, "prepare")
                self.assertEqual(actual_binding, binding)
                self.assertEqual(binding_hash, digest)
                return await real_run(verb, local_case, actual_binding, binding_hash)
            async def stop_before_transport(rpc):
                entered.append(copy.deepcopy(rpc.binding))
                raise helper.ProbeError("offline_transport_boundary")
            with patch.object(helper, "run", side_effect=observe_run), \
                    patch.object(helper, "verify_machine"), \
                    patch.object(helper, "continuous_ns", return_value=9_000_000_000), \
                    patch.object(helper.Rpc, "__aenter__", stop_before_transport):
                result = helper.main(argv[3:])
                self.assertEqual(result["reason"], "offline_transport_boundary")
                self.assertEqual(result["status"], "rejected")
                self.assertEqual(entered, [binding])
                self.assertFalse(result["resend_authorized"])
                self.assertFalse((case / "send-attempt.json").exists())
                self.assertFalse((case / "prepare-meta.json").exists())
                path.write_bytes(files["controller-evidence.json"] + b" ")
                with self.assertRaisesRegex(helper.ProbeError, "^controller_evidence_changed$"):
                    helper.main(argv[3:])
                path.write_bytes(files["controller-evidence.json"])
                old = dict(binding, nonce="sd-qual-07")
                (case / "binding.json").write_bytes((helper.canonical(old) + "\n").encode())
                old_args = list(argv[3:]); old_args[-1] = "sd-qual-07"
                with self.assertRaisesRegex(helper.ProbeError, "^root_nonce_mismatch$"):
                    helper.main(old_args)
                (case / "binding.json").write_bytes(binding_raw)
                self.assertEqual(entered, [binding])

        (BASE / "docs/current-chain.json").write_text(json.dumps({
            "helper_commit": "60e13d4e8b47bd461607544f6aedfbfcf3283878",
            "native_commit": "7bbc66b968bbf55041f7c74e587b7978fae14fe0",
            "status": "PASS",
            "scope": "actual controller writer and serialization to real helper.main local validation; stopped before transport",
            "initial_phase": "actual control.run under its existing offline process/clock fixture",
            "binding_writer": "actual production AST and binding_process; only synthetic dead-process CLI path selected",
            "runtime_pins_unchanged": True,
            "extended_controller_evidence_keys_accepted": ["launch_intent_sha256", "origin_sha256", "session_start", "candidate_hook"],
            "binding_canonical_sha256": digest,
            "evidence_raw_sha256": binding["controller_evidence_sha256"],
            "modified_evidence_rejected": True,
            "consumed_sd07_rejected": True,
            "network_connections": 0,
            "current_prepare_or_send_success_claimed": False
        }, indent=2) + "\n")


if __name__ == "__main__":
    unittest.main(verbosity=2)
