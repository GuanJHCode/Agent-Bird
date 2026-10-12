import hashlib
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock
from types import SimpleNamespace

SOURCE = Path(__file__).with_name('codex-local-plugin.py')


def load():
    spec = importlib.util.spec_from_file_location('local_plugin', SOURCE)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class LocalPluginTest(unittest.TestCase):
    def fixture(self, root):
        runtime = root / 'runtime'; runtime.mkdir(mode=0o700)
        binary = runtime / 'packages/versions/1.0.0-rc.4/bin/codex-orchestrator'
        binary.parent.mkdir(parents=True, mode=0o700)
        binary.write_text('#!/bin/sh\nexit 0\n'); binary.chmod(0o700)
        locks = runtime / 'locks'; locks.mkdir(mode=0o700)
        lock = locks / 'claude.json'; lock.write_text('{}'); lock.chmod(0o600)
        return runtime, binary, lock

    def build(self, module, root):
        runtime, binary, lock = self.fixture(root)
        record = {'version': 1, 'runtime': str(runtime), 'binary': str(binary), 'binary_sha256': hashlib.sha256(binary.read_bytes()).hexdigest(), 'code_sha': 'a' * 40, 'providers': {'claude': {'path': str(lock), 'sha256': hashlib.sha256(lock.read_bytes()).hexdigest()}}}
        with mock.patch.object(module, 'inspect_installation', return_value=record):
            plugin = module.build_plugin(runtime, root / 'marketplace', '1.0.0-rc.4')
        return plugin, record

    def test_build_binds_versioned_runtime_and_has_native_skill(self):
        module = load()
        with tempfile.TemporaryDirectory() as temp:
            plugin, record = self.build(module, Path(temp).resolve())
            self.assertEqual(module.load_binding(plugin), record)
            manifest = json.loads((plugin / '.codex-plugin/plugin.json').read_text())
            self.assertEqual(manifest['version'], '1.0.0-rc.4')
            self.assertTrue((plugin / 'scripts/agent-bird').stat().st_mode & 0o100)
            skill = (plugin / 'skills/agent-bird/SKILL.md').read_text()
            self.assertIn('call --provider', skill)
            self.assertNotIn('controller start', skill)

    def test_changed_runtime_or_pin_is_rejected(self):
        for key in ('binary', 'pin'):
            with self.subTest(key=key), tempfile.TemporaryDirectory() as temp:
                module = load(); plugin, record = self.build(module, Path(temp).resolve())
                path = Path(record['binary'] if key == 'binary' else record['providers']['claude']['path'])
                path.write_text('changed')
                with self.assertRaisesRegex(ValueError, 'binding_changed'):
                    module.load_binding(plugin)

    def test_call_uses_fixed_pin_compact_view_and_private_request(self):
        module = load()
        with tempfile.TemporaryDirectory() as temp:
            plugin, record = self.build(module, Path(temp).resolve())
            project = Path(temp).resolve() / 'project'; project.mkdir()
            args = SimpleNamespace(provider='claude', directory=str(project), task='review only', mode='read-only', path=[], timeout_ms=120000)
            with mock.patch.object(module.os, 'execv') as run:
                module.delegate(plugin, args)
            binary, argv = run.call_args.args
            self.assertEqual(binary, record['binary'])
            self.assertEqual(argv[1:3], ['call', '--compact'])
            request = Path(argv[argv.index('--request') + 1])
            self.assertEqual(request.stat().st_mode & 0o777, 0o600)
            self.assertEqual(request.parent.stat().st_mode & 0o777, 0o700)
            body = json.loads(request.read_text())
            self.assertEqual(body['provider_lock'], record['providers']['claude']['path'])
            self.assertEqual(body['task'], 'review only')

    def test_unconfigured_target_fails_before_request_or_execution(self):
        module = load()
        with tempfile.TemporaryDirectory() as temp:
            plugin, record = self.build(module, Path(temp).resolve())
            args = SimpleNamespace(provider='agy')
            with mock.patch.object(module.os, 'execv') as run:
                with self.assertRaisesRegex(ValueError, 'provider_not_configured'):
                    module.delegate(plugin, args)
            run.assert_not_called()
            self.assertFalse((Path(record['runtime']) / 'plugin-requests').exists())


if __name__ == '__main__':
    unittest.main()
