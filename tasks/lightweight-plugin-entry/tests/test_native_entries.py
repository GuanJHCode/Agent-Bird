import json
import pathlib
import subprocess
import sys
import tempfile
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[3]
BUILDER = ROOT / "tasks/lightweight-plugin-entry/scripts/build-native-entries.py"


class NativeEntriesTest(unittest.TestCase):
    def test_builds_four_skill_only_entries_with_honest_owner_boundaries(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = pathlib.Path(temporary) / "entries"
            subprocess.run([sys.executable, str(BUILDER), "--output", str(output)], check=True)
            codex = json.loads((output / "codex/.codex-plugin/plugin.json").read_text())
            claude = json.loads((output / "claude/.claude-plugin/plugin.json").read_text())
            grok = json.loads((output / "grok/plugin.json").read_text())
            agy = json.loads((output / "agy/plugin.json").read_text())
            self.assertEqual(codex["name"], "codex-orchestrator")
            self.assertEqual(claude["name"], "agent-bird-claude")
            self.assertEqual(grok["name"], "agent-bird-grok")
            self.assertEqual(agy["name"], "agent-bird-agy")
            for provider in ("codex", "claude", "grok", "agy"):
                skill = (output / provider / "skills/agent-bird/SKILL.md").read_text()
                self.assertIn("agent-bird task", skill)
                self.assertNotIn("goal", skill.lower())
                self.assertNotIn("mcp", skill.lower())
            for provider in ("claude", "grok", "agy"):
                skill = (output / provider / "skills/agent-bird/SKILL.md").read_text()
                self.assertIn(f"agent-bird controller start --provider {provider}", skill)

    def test_codex_wrapper_accepts_controller_without_broadening_commands(self):
        wrapper = ROOT / "plugins/codex-orchestrator/scripts/invoke.sh"
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            binary = root / "bin/codex-orchestrator"
            binary.parent.mkdir()
            capture = root / "captured"
            binary.write_text("#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$CAPTURE\"\n")
            binary.chmod(0o700)
            environment = {"PLUGIN_ROOT": str(root), "CAPTURE": str(capture)}
            subprocess.run([str(wrapper), "controller", "start", "--provider", "grok"], env=environment, check=True)
            self.assertEqual(capture.read_text(), "controller start --provider grok\n")
            denied = subprocess.run([str(wrapper), "routing", "status"], env=environment, capture_output=True)
            self.assertEqual(denied.returncode, 2)


if __name__ == "__main__":
    unittest.main()
