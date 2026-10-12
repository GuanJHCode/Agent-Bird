import importlib.util
import json
import hashlib
from pathlib import Path
import unittest
from unittest import mock
from types import SimpleNamespace
import tempfile

source = Path(__file__).parents[1] / 'scripts/agent-bird.py'
spec = importlib.util.spec_from_file_location('bird_launcher', source)
launcher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(launcher)

class LaunchAdaptersTest(unittest.TestCase):
    def test_process_local_mcp_owner_and_protections(self):
        for provider in ('codex', 'claude', 'agy'):
            with self.subTest(provider=provider), tempfile.TemporaryDirectory() as directory:
                self.check_adapter(Path(directory), provider)

    def test_candidate_checksums_close_outer_adapters(self):
        with tempfile.TemporaryDirectory() as directory:
            package = Path(directory)
            files = {}
            for name in ('agent-bird.py', 'grok-plugin-acp.py'):
                (package / name).write_text(name)
                files[name] = hashlib.sha256(name.encode()).hexdigest()
            (package / 'candidate-checksums.json').write_text(json.dumps({'version': 1, 'files': files}))
            self.assertEqual(launcher.verified_candidate(package), files)
            (package / 'grok-plugin-acp.py').write_text('changed')
            with self.assertRaisesRegex(ValueError, 'checksum'):
                launcher.verified_candidate(package)
            (package / 'grok-plugin-acp.py').write_text('grok-plugin-acp.py')
            (package / 'unexpected').write_text('no')
            with self.assertRaisesRegex(ValueError, 'files_mismatch'):
                launcher.verified_candidate(package)

    def test_setup_prepares_state_for_grok_before_controller(self):
        with tempfile.TemporaryDirectory(dir='/private/tmp') as directory:
            base = Path(directory).resolve()
            package = base / 'package'; package.mkdir()
            files = {}
            for name in ('agent-bird.py', 'grok-plugin-acp.py'):
                (package / name).write_text(name)
                files[name] = hashlib.sha256(name.encode()).hexdigest()
            (package / 'candidate-checksums.json').write_text(json.dumps({'version': 1, 'files': files}))
            runtime = base / 'run'
            args = SimpleNamespace(runtime=str(runtime), package=str(package), provider_binary=['grok=/bin/sh'])
            with mock.patch.object(launcher, 'run_json', side_effect=[{'binary': '/installed/binary'}, {'profile_supported': False}]), mock.patch('builtins.print'):
                launcher.setup(args)
            self.assertTrue((runtime / 'state').is_dir())
            self.assertEqual((runtime / 'state').stat().st_mode & 0o777, 0o700)
            installed = json.loads((runtime / 'installation.json').read_text())
            self.assertEqual(Path(installed['adapters']), runtime / 'adapters')
            self.assertTrue((runtime / 'adapters/grok-plugin-acp.py').is_file())

    def check_adapter(self, tmp_path, provider):
        project = tmp_path / 'project'
        session = tmp_path / 'session'
        project.mkdir(); session.mkdir()
        before = list(project.iterdir())
        cmd, cwd, payload = launcher.launch_args(provider, '/installed/bin/codex-orchestrator', '/state', '/locks', project, 'delegate', session)
        assert cmd[:7] == ['/installed/bin/codex-orchestrator', 'controller', 'start', '--provider', provider, '--state-dir', '/state']
        assert list(project.iterdir()) == before
        assert not any('bypass' in value or 'dangerously' in value for value in cmd)
        if provider == 'codex':
            assert cmd[cmd.index('--sandbox') + 1] == 'read-only'
            assert 'mcp_servers.agent_bird.env_vars=["AGENT_BIRD_OWNER_REQUEST"]' in cmd
        else:
            config = session / ('mcp.json' if provider == 'claude' else '.agents/mcp_config.json')
            entry = json.loads(config.read_text())['mcpServers']['agent_bird']
            assert entry['args'] == ['mcp', '--state-dir', '/state', '--provider-locks', '/locks']
            assert config.stat().st_mode & 0o777 == 0o600
        if provider == 'agy':
            assert '--print=' in cmd and cwd == session
            assert json.loads(payload)['message']['content'] == 'delegate'

if __name__ == "__main__":
    unittest.main()
