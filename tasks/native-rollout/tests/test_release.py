import hashlib
import json
import os
import pathlib
import subprocess
import sys
import tarfile
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[3]
BUILD = ROOT / 'tasks/native-rollout/scripts/build-release.py'

class ReleaseTest(unittest.TestCase):
    def build(self, root):
        binary = root / 'binary'
        binary.write_text('#!/bin/sh\nif [ "${1-}" = marketplace-check ]; then echo missing; exit 0; fi\nprintf "%s\\n" "$@" > "$CAPTURE"\n')
        binary.chmod(0o700)
        output = root / 'output'
        result = subprocess.run([sys.executable, str(BUILD), '--binary', str(binary), '--version', '0.2.0-preview.3', '--output', str(output)], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        return output

    def test_release_archive_has_working_relocated_entry_and_verified_checksum(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            output = self.build(root)
            archive = output / 'agent-bird-0.2.0-preview.3-macos-arm64.tar.gz'
            digest, name = (output / 'SHA256SUMS').read_text().strip().split('  ')
            self.assertEqual(name, archive.name)
            self.assertEqual(digest, hashlib.sha256(archive.read_bytes()).hexdigest())
            installed = root / 'location with spaces'
            installed.mkdir()
            with tarfile.open(archive) as tar:
                tar.extractall(installed, filter='data')
            package = installed / 'agent-bird'
            capture = root / 'capture'
            args = ['task', 'inspect', '--handle', 'literal $HOME `id`\nspaces']
            result = subprocess.run([str(package / 'agent-bird'), *args], env=dict(os.environ, CAPTURE=str(capture)), capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertTrue(capture.read_text().endswith('--\n' + '\n'.join(args) + '\n'))
            self.assertTrue((output / 'agent-bird.rb').is_file())
            self.assertTrue((package / 'LICENSE').is_file())
            self.assertTrue((package / 'install.command').is_file())

    def test_existing_output_is_preserved(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            output = self.build(root)
            before = (output / 'SHA256SUMS').read_bytes()
            result = subprocess.run([sys.executable, str(BUILD), '--binary', str(root / 'binary'), '--version', '0.2.0-preview.3', '--output', str(output)], capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual((output / 'SHA256SUMS').read_bytes(), before)


class InstallerTest(ReleaseTest):
    def prepare(self, root):
        output = self.build(root)
        package = output / 'agent-bird'
        commands = root / 'commands'
        commands.mkdir()
        uname = commands / 'uname'
        uname.write_text('#!/bin/sh\ncase "$1" in -s) echo Darwin ;; -m) echo arm64 ;; esac\n')
        uname.chmod(0o700)
        environment = dict(os.environ, PATH=str(commands)+':/usr/bin:/bin', AGENT_BIRD_INSTALL_ROOT=str(root / 'installed'), CALL_LOG=str(root / 'calls'))
        return package, commands, environment

    def test_each_cli_gets_only_its_native_install_commands(self):
        for provider in ('codex', 'claude', 'grok', 'agy'):
            with self.subTest(provider=provider), tempfile.TemporaryDirectory() as temp:
                root = pathlib.Path(temp).resolve()
                package, commands, environment = self.prepare(root)
                cli = commands / provider
                cli.write_text('#!/bin/sh\nprintf "%s\\n" "$@" >> "$CALL_LOG"\nprintf "END\\n" >> "$CALL_LOG"\n')
                cli.chmod(0o700)
                result = subprocess.run([str(package / 'install.command'), provider], env=environment, capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                destination = root / 'installed/versions/0.2.0-preview.3'
                self.assertTrue((destination / 'agent-bird').exists())
                if provider == 'codex':
                    expected = ['plugin','marketplace','add',str(destination),'END','plugin','add','codex-orchestrator@codex-bird','END']
                elif provider == 'claude':
                    expected = ['plugin','marketplace','add',str(destination),'--scope','user','END','plugin','install','agent-bird-claude@agent-bird','--scope','user','END']
                else:
                    expected = ['plugin','install',str(destination / 'plugins' / provider),'END']
                self.assertEqual((root / 'calls').read_text().splitlines(), expected)

    def test_missing_cli_makes_no_install_directory(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp).resolve()
            package, commands, environment = self.prepare(root)
            result = subprocess.run([str(package / 'install.command'), 'grok'], env=environment, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse((root / 'installed').exists())
            self.assertFalse((root / 'calls').exists())

    def test_symlink_install_parent_and_changed_package_are_rejected_before_cli(self):
        for kind in ('symlink-parent', 'symlink-trailing', 'dotdot', 'extra-file', 'extra-link', 'tamper'):
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as temp:
                root = pathlib.Path(temp).resolve()
                package, commands, environment = self.prepare(root)
                cli = commands / 'grok'
                cli.write_text('#!/bin/sh\necho called > "$CALL_LOG"\n')
                cli.chmod(0o700)
                outside = root / 'outside'
                outside.mkdir()
                (outside / 'keep').write_text('preserve')
                if kind in ('symlink-parent', 'symlink-trailing', 'dotdot'):
                    (root / 'installed').symlink_to(outside, target_is_directory=True)
                    if kind == 'symlink-trailing': environment['AGENT_BIRD_INSTALL_ROOT'] += '/'
                    if kind == 'dotdot': environment['AGENT_BIRD_INSTALL_ROOT'] += '/../elsewhere'
                elif kind == 'extra-file':
                    (package / 'plugins/grok/rogue-hook.sh').write_text('unlisted')
                elif kind == 'extra-link':
                    (package / 'plugins/grok/rogue').symlink_to(outside, target_is_directory=True)
                else:
                    (package / 'VERSION').write_text('0.2.0-tampered\n')
                result = subprocess.run([str(package / 'install.command'), 'grok'], env=environment, capture_output=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(list(outside.iterdir()), [outside / 'keep'])
                self.assertFalse((root / 'calls').exists())

if __name__ == '__main__':
    unittest.main()
