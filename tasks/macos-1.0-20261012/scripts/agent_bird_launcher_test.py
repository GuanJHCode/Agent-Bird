import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('bird_launcher', Path(__file__).with_name('agent-bird.py'))
bird = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bird)


class LauncherTest(unittest.TestCase):
    def test_claude_delegation_has_no_builtin_tools_and_direct_mcp_discovery(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            args, cwd, payload = bird.launch_args('claude', Path('/binary'), root / 'state', root / 'locks', root, 'delegate', root)
            self.assertIn('--tools', args)
            self.assertIn('--disable-slash-commands', args)
            self.assertEqual(args[args.index('--tools') + 1], '')
            settings = json.loads(args[args.index('--settings') + 1])
            self.assertEqual(settings['env']['ENABLE_TOOL_SEARCH'], 'false')
            allowed = args[args.index('--allowedTools') + 1]
            self.assertIn('mcp__agent_bird__agent_bird_call', allowed)
            self.assertIn('mcp__agent_bird__agent_bird_result', allowed)
            self.assertIn('--strict-mcp-config', args)
            self.assertEqual(cwd, root)
            self.assertIsNone(payload)

    def test_codex_can_fetch_details_without_broadening_sandbox(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            args, _, _ = bird.launch_args('codex', Path('/binary'), root / 'state', root / 'locks', root, 'delegate', root)
            self.assertEqual(args[args.index('--sandbox') + 1], 'read-only')
            self.assertIn('mcp_servers.agent_bird.tools.agent_bird_result.approval_mode="approve"', args)

    def test_agy_uses_same_mcp_server_and_explicit_project(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            args, cwd, payload = bird.launch_args('agy', Path('/binary'), root / 'state', root / 'locks', Path('/project'), 'delegate', root)
            self.assertIn('--print=', args)
            self.assertEqual(args[args.index('--add-dir')+1], '/project')
            self.assertEqual(json.loads(payload)['message']['content'], 'delegate')
            config = json.loads((root / '.agents/mcp_config.json').read_text())
            self.assertEqual(config['mcpServers']['agent_bird']['args'][0], 'mcp')


if __name__ == '__main__':
    unittest.main()
