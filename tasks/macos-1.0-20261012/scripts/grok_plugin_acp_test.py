#!/usr/bin/env python3
"""Regression coverage for grok-plugin-acp.py without invoking a model."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import textwrap
import unittest

SCRIPT = Path(__file__).with_name("grok-plugin-acp.py")


class GrokPluginACPTest(unittest.TestCase):
    def test_process_local_plugin_and_acp_lifecycle(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            workdir = root / "project"
            state = root / "state"
            locks = root / "locks"
            workdir.mkdir()
            state.mkdir()
            locks.mkdir()
            skill = root / "skill.md"
            skill.write_text("skill\n")
            trace = root / "trace.json"
            controller = root / "fake-controller.py"
            controller.write_text(textwrap.dedent("""\
                #!/usr/bin/env python3
                import json, os, pathlib, sys
                trace = pathlib.Path(os.environ["ACP_TEST_TRACE"])
                args = sys.argv[1:]
                marker = args.index("--")
                grok_args = args[marker + 1:]
                plugin = pathlib.Path(grok_args[grok_args.index("--plugin-dir") + 1])
                manifest = json.loads((plugin / ".grok-plugin" / "plugin.json").read_text())
                seen = {"args": args, "manifest": manifest}
                trace.write_text(json.dumps(seen))
                for raw in sys.stdin:
                    message = json.loads(raw)
                    method = message.get("method")
                    if method == "initialize":
                        result = {"protocolVersion": 1, "serverCapabilities": {}, "serverInfo": {"name": "fake", "version": "1"}}
                    elif method == "session/new":
                        seen["session_new"] = message["params"]
                        trace.write_text(json.dumps(seen))
                        result = {"sessionId": "session-1"}
                    elif method == "session/prompt":
                        print(json.dumps({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": "session-1", "update": {"sessionUpdate": "agent_message_chunk", "content": {"type": "text", "text": "Bird ready"}}}}), flush=True)
                        result = {}
                    else:
                        raise SystemExit("unexpected method " + repr(method))
                    print(json.dumps({"jsonrpc": "2.0", "id": message["id"], "result": result}), flush=True)
            """))
            controller.chmod(0o700)
            env = os.environ | {"ACP_TEST_TRACE": str(trace)}
            result = subprocess.run([
                sys.executable, str(SCRIPT),
                "--binary", str(controller),
                "--skill-path", str(skill),
                "--state-dir", str(state),
                "--provider-locks", str(locks),
                "--directory", str(workdir),
                "--prompt", "call Bird once",
                "--timeout", "10",
            ], text=True, capture_output=True, env=env, check=False, timeout=20)
            self.assertEqual(result.returncode, 0, result.stderr)
            summary = json.loads(result.stdout)
            self.assertEqual(summary["session_id"], "session-1")
            self.assertEqual(summary["updates"][0]["update"]["sessionUpdate"], "agent_message_chunk")
            observed = json.loads(trace.read_text())
            self.assertEqual(observed["args"][:4], ["controller", "start", "--provider", "grok"])
            self.assertIn("--no-leader", observed["args"])
            self.assertEqual(observed["manifest"]["mcpServers"]["agent_bird"]["command"], str(controller.resolve()))
            self.assertEqual(observed["manifest"]["mcpServers"]["agent_bird"]["args"], ["mcp", "--state-dir", str(state.resolve()), "--provider-locks", str(locks.resolve())])
            self.assertEqual(observed["manifest"]["mcpServers"]["agent_bird"]["cwd"], str(workdir.resolve()))
            self.assertEqual(observed["session_new"]["mcpServers"], [])

    def test_timeout_terminates_controller_process_group(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            workdir = root / "project"; state = root / "state"; locks = root / "locks"
            for path in (workdir, state, locks): path.mkdir()
            skill = root / "skill.md"; skill.write_text("skill\n")
            pid_file = root / "pid"
            controller = root / "sleeping-controller.py"
            controller.write_text(textwrap.dedent("""\
                #!/usr/bin/env python3
                import os, pathlib, sys, time
                pathlib.Path(os.environ["ACP_TEST_PID"]).write_text(str(os.getpid()))
                sys.stdin.readline()
                time.sleep(60)
            """)); controller.chmod(0o700)
            result = subprocess.run([
                sys.executable, str(SCRIPT), "--binary", str(controller), "--skill-path", str(skill),
                "--state-dir", str(state), "--provider-locks", str(locks), "--directory", str(workdir),
                "--prompt", "never reaches model", "--timeout-seconds", "1",
            ], text=True, capture_output=True, env=os.environ | {"ACP_TEST_PID": str(pid_file)}, check=False, timeout=15)
            self.assertEqual(result.returncode, 1)
            self.assertIn("timed out waiting for ACP initialize", result.stderr)
            pid = int(pid_file.read_text())
            with self.assertRaises(ProcessLookupError):
                os.kill(pid, 0)


if __name__ == "__main__":
    unittest.main()
