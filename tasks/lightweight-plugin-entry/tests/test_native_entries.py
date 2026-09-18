import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[3]
BUILDER = ROOT / 'tasks/lightweight-plugin-entry/scripts/build-native-entries.py'
PACKAGER = ROOT / 'tasks/mvp-simple-install/scripts/package.py'

class NativeEntriesTest(unittest.TestCase):
    def fixture(self, root):
        binary = root / 'fake-binary'
        binary.write_text('#!/bin/sh\nprintf "%s\\n" "$@" > "$CAPTURE"\n')
        binary.chmod(0o700)
        runtime = root / 'portable'
        subprocess.run([sys.executable, str(PACKAGER), '--binary', str(binary), '--version', '0.2.0-test.1', '--out', str(runtime)], check=True, capture_output=True)
        return runtime

    def build(self, root, runtime):
        return subprocess.run([sys.executable, str(BUILDER), '--output', str(root / 'entries'), '--runtime', str(runtime)], capture_output=True, text=True)

    def test_relocated_packages_use_bundled_runtime_and_preserve_arguments(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            runtime = self.fixture(root)
            result = self.build(root, runtime)
            self.assertEqual(result.returncode, 0, result.stderr)
            installed = root / 'installed with spaces'
            shutil.copytree(root / 'entries', installed)
            shutil.rmtree(runtime)
            shutil.rmtree(root / 'entries')
            manifests = {'codex-orchestrator': '.codex-plugin/plugin.json', 'claude': '.claude-plugin/plugin.json', 'grok': '.grok-plugin/plugin.json', 'agy': 'plugin.json'}
            for provider, manifest in manifests.items():
                package = installed / provider
                self.assertEqual(json.loads((package / manifest).read_text())['version'], '0.2.0-test.1')
                capture = root / 'captured'
                args = ['task', 'inspect', '--handle', 'literal $HOME `id`\nwith spaces']
                environment = dict(os.environ, CAPTURE=str(capture))
                result = subprocess.run([str(package / 'scripts/agent-bird'), *args], env=environment, capture_output=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                expected = ['plugin-run', '--plugin-root', str(package / 'runtime/plugins/codex-orchestrator'), '--', *args]
                self.assertEqual(capture.read_text(), '\n'.join(expected) + '\n')
                denied = subprocess.run([str(package / 'scripts/agent-bird'), 'arbitrary-command'], capture_output=True)
                self.assertEqual(denied.returncode, 2)
                skill = (package / 'skills/agent-bird/SKILL.md').read_text()
                self.assertNotIn('--runtime', skill)
                self.assertTrue((package / 'skills/agent-bird/references/protocol.md').is_file())
                if provider == 'codex-orchestrator':
                    metadata = json.loads((package / manifest).read_text())
                    self.assertIsInstance(metadata['author'], dict)
                    self.assertIsInstance(metadata['interface'], dict)
                    self.assertIn('calling native Codex session', skill)
                    self.assertNotIn('--provider codex-orchestrator', skill)
                else:
                    self.assertIn(f'--provider {provider}', skill)
                self.assertNotIn('--provider codex-orchestrator', skill)

    def test_rejects_tampered_or_symlink_runtime_before_output(self):
        for kind in ('tampered', 'symlink'):
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as temporary:
                root = pathlib.Path(temporary)
                runtime = self.fixture(root)
                binary = runtime / 'plugins/codex-orchestrator/bin/codex-orchestrator'
                if kind == 'tampered':
                    binary.write_text('tampered')
                else:
                    binary.unlink()
                    binary.symlink_to(root / 'fake-binary')
                self.assertNotEqual(self.build(root, runtime).returncode, 0)
                self.assertFalse((root / 'entries').exists())

    def test_rejects_existing_output_without_removing_it(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            runtime = self.fixture(root)
            output = root / 'entries'
            output.mkdir()
            (output / 'keep').write_text('preserve')
            self.assertNotEqual(self.build(root, runtime).returncode, 0)
            self.assertEqual((output / 'keep').read_text(), 'preserve')

if __name__ == '__main__':
    unittest.main()
