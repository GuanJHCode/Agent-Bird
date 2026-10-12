#!/usr/bin/env python3
"""User-local candidate installer and thin Provider launch adapters. No global config."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile

PROVIDERS = ('codex', 'claude', 'agy', 'grok')
PROVIDER_IDS = dict(zip(PROVIDERS, ('codex-cli', 'claude-code', 'antigravity-cli', 'grok-build')))


def private_json(path, value):
    with path.open('x', encoding='utf-8') as stream:
        json.dump(value, stream, ensure_ascii=False, indent=2)
        stream.write('\n')
    path.chmod(0o600)


def run_json(args):
    result = subprocess.run(args, capture_output=True, text=True, timeout=45)
    if result.returncode:
        raise RuntimeError(result.stderr.strip() or result.stdout.strip() or 'command_failed')
    return json.loads(result.stdout)


def verified_candidate(package):
    manifest = json.loads((package / 'candidate-checksums.json').read_text())
    files = manifest.get('files', {})
    if manifest.get('version') != 1 or not isinstance(files, dict) or not files:
        raise ValueError('candidate_manifest_invalid')
    actual = {p.relative_to(package).as_posix() for p in package.rglob('*') if p.is_file()}
    if actual != set(files) | {'candidate-checksums.json'} or any(p.is_symlink() for p in package.rglob('*')):
        raise ValueError('candidate_files_mismatch')
    for name, digest in files.items():
        if Path(name).is_absolute() or '..' in Path(name).parts or hashlib.sha256((package / name).read_bytes()).hexdigest() != digest:
            raise ValueError('candidate_checksum_mismatch')
    for name in ('agent-bird.py', 'grok-plugin-acp.py'):
        if name not in files:
            raise ValueError('candidate_adapter_missing')
    return files


def setup(args):
    runtime = Path(args.runtime).expanduser()
    if not runtime.is_absolute() or runtime.resolve() != runtime or len(os.fsencode(str(runtime / 'state/coordinator.sock'))) > 100:
        raise ValueError('runtime must be a short canonical absolute path, e.g. /Users/you/.bird1')
    package = Path(args.package).resolve(strict=True)
    files = verified_candidate(package)
    runtime.mkdir(mode=0o700)  # Never replace an existing installation or configuration.
    (runtime / 'state').mkdir(mode=0o700)
    adapters = runtime / 'adapters'
    adapters.mkdir(mode=0o700)
    for name in ('agent-bird.py', 'grok-plugin-acp.py'):
        data = (package / name).read_bytes()
        if hashlib.sha256(data).hexdigest() != files[name]:
            raise ValueError('candidate_adapter_changed')
        with (adapters / name).open('xb') as stream:
            stream.write(data)
        (adapters / name).chmod(0o600)
        if hashlib.sha256((adapters / name).read_bytes()).hexdigest() != files[name]:
            raise ValueError('installed_adapter_checksum_mismatch')
    plugin = package / 'plugin'
    bootstrap = plugin / 'bin/codex-orchestrator'
    installed = run_json([str(bootstrap), 'plugin-run', '--plugin-root', str(plugin),
                          '--install-root', str(runtime / 'packages'), '--state-dir', str(runtime / 'state'), 'check'])
    binary = installed['binary']
    locks = runtime / 'locks'
    locks.mkdir(mode=0o700)
    report = {}
    for value in args.provider_binary:
        provider, sep, binary_path = value.partition('=')
        if not sep or provider not in PROVIDERS or provider in report:
            raise ValueError('use distinct --provider-binary provider=/absolute/path')
        executable = Path(binary_path).expanduser().resolve(strict=True)
        request = runtime / (provider + '-probe-request.json')
        private_json(request, {'provider': PROVIDER_IDS[provider], 'binary_path': str(executable)})
        probe = run_json([binary, 'provider-probe', '--request', str(request)])
        report[provider] = probe
        if probe.get('profile_supported'):
            # Explicit setup of the named executable pins its observed digest.
            lock_request = runtime / (provider + '-lock-request.json')
            private_json(lock_request, {'provider': PROVIDER_IDS[provider], 'binary_path': str(executable),
                                       'lock_file': str(locks / (provider + '.json')),
                                       'confirm_sha256': probe['lock']['binary']['sha256']})
            run_json([binary, 'provider-lock', '--request', str(lock_request)])
    private_json(runtime / 'doctor-request.json', {'destination_root': str(runtime / 'packages')})
    private_json(runtime / 'installation.json', {'version': 1, 'binary': binary, 'package': str(package), 'adapters': str(adapters), 'adapter_sha256': {n: files[n] for n in ('agent-bird.py', 'grok-plugin-acp.py')}, 'providers': report})
    print(json.dumps({'runtime': str(runtime), 'providers': report, 'authentication': 'not_checked'}, ensure_ascii=False, indent=2))


def launch_args(provider, binary, state, locks, directory, prompt, session):
    mcp_args = ['mcp', '--state-dir', str(state), '--provider-locks', str(locks)]
    command = [str(binary), 'controller', 'start', '--provider', provider, '--state-dir', str(state), '--']
    if provider == 'codex':
        return command + ['exec', '--skip-git-repo-check', '--sandbox', 'read-only', '--json', '-C', str(directory),
                          '-c', 'mcp_servers.agent_bird.command=' + json.dumps(str(binary)),
                          '-c', 'mcp_servers.agent_bird.args=' + json.dumps(mcp_args),
                          '-c', 'mcp_servers.agent_bird.env_vars=["AGENT_BIRD_OWNER_REQUEST"]',
                          '-c', 'mcp_servers.agent_bird.tool_timeout_sec=3600',
                          '-c', 'mcp_servers.agent_bird.tools.agent_bird_call.approval_mode="approve"', prompt], directory, None
    config = {'mcpServers': {'agent_bird': {'command': str(binary), 'args': mcp_args}}}
    if provider == 'claude':
        config_path = session / 'mcp.json'
        private_json(config_path, config)
        return command + ['-p', prompt, '--output-format', 'stream-json', '--verbose', '--strict-mcp-config',
                          '--mcp-config', str(config_path), '--allowedTools', 'mcp__agent_bird__agent_bird_call'], directory, None
    if provider == 'agy':
        (session / '.agents').mkdir(mode=0o700)
        private_json(session / '.agents/mcp_config.json', config)
        payload = json.dumps({'event': 'user', 'message': {'content': prompt}}) + '\n'
        return command + ['--print=', '--input-format', 'stream-json', '--output-format', 'stream-json',
                          '--add-dir', str(directory)], session, payload
    raise ValueError('Grok uses the process-local ACP plugin adapter')


def start(args):
    runtime = Path(args.runtime).expanduser().resolve(strict=True)
    installation = json.loads((runtime / 'installation.json').read_text())
    binary = Path(installation['binary'])
    directory = Path(args.directory).expanduser().resolve(strict=True)
    if not directory.is_dir():
        raise ValueError('directory must exist')
    if not shutil.which(args.provider):
        raise ValueError('source CLI is missing from PATH: ' + args.provider)
    state, locks = runtime / 'state', runtime / 'locks'
    prompt = ('Use the Agent Bird agent_bird_call MCP tool to perform the requested delegation. '
              'Pass the actual target Provider, task, and absolute directory. For edits use mode=workspace-write '
              'and exact paths. Do not recursively delegate. Report the actual result, failure_reason, '
              'changes_known, changed_files and candidate workspace. The project directory is ' + str(directory) + '.\n' + args.prompt)
    session = Path(tempfile.mkdtemp(prefix='source-', dir=runtime))
    if args.provider == 'grok':
        adapter = Path(installation['adapters']) / 'grok-plugin-acp.py'
        if adapter.is_symlink() or hashlib.sha256(adapter.read_bytes()).hexdigest() != installation['adapter_sha256']['grok-plugin-acp.py']:
            raise ValueError('installed_adapter_checksum_mismatch')
        command = [sys.executable, str(adapter), '--binary', str(binary), '--state-dir', str(state),
                   '--provider-locks', str(locks), '--directory', str(directory), '--prompt', prompt,
                   '--timeout-seconds', str(args.timeout_seconds)]
        cwd, payload = directory, None
    else:
        command, cwd, payload = launch_args(args.provider, binary, state, locks, directory, prompt, session)
    # Keep the owning controller alive, forward cancellation, then reap it.
    child = subprocess.Popen(command, cwd=cwd, stdin=subprocess.PIPE if payload else subprocess.DEVNULL, text=True)
    def cancel(signum, frame):
        child.send_signal(signum)
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, cancel)
    try:
        child.communicate(payload, timeout=args.timeout_seconds)
    except subprocess.TimeoutExpired:
        child.terminate()
        try:
            child.wait(timeout=15)
        except subprocess.TimeoutExpired:
            child.kill()
            child.wait(timeout=10)
        raise RuntimeError('source_timeout; inspect retained task handles before another call')
    return child.returncode


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='command', required=True)
    install = commands.add_parser('setup', help='install only into a new private runtime; pin explicitly selected target CLIs')
    install.add_argument('--runtime', required=True)
    install.add_argument('--package', default=str(Path(__file__).resolve().parent))
    install.add_argument('--provider-binary', action='append', required=True)
    launch = commands.add_parser('start', help='launch a source Provider with a temporary Agent Bird MCP plugin')
    launch.add_argument('--runtime', required=True)
    launch.add_argument('--provider', choices=PROVIDERS, required=True)
    launch.add_argument('--directory', required=True)
    launch.add_argument('--prompt', required=True)
    launch.add_argument('--timeout-seconds', type=int, default=300)
    check = commands.add_parser('check', help='inspect installation, pins and current source CLI dependencies; no login probe')
    check.add_argument('--runtime', required=True)
    args = parser.parse_args()
    if args.command == 'setup':
        setup(args)
    elif args.command == 'start':
        if not 1 <= args.timeout_seconds <= 7200:
            raise ValueError('timeout-seconds must be 1..7200')
        return start(args)
    else:
        runtime = Path(args.runtime).expanduser().resolve(strict=True)
        data = json.loads((runtime / 'installation.json').read_text())
        data['source_cli_paths'] = {p: shutil.which(p) for p in PROVIDERS}
        data['doctor'] = run_json([data['binary'], 'doctor', '--request', str(runtime / 'doctor-request.json')])
        data['authentication'] = 'not_checked'
        print(json.dumps(data, ensure_ascii=False, indent=2))
    return 0


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
        print(json.dumps({'status': 'failed', 'reason': str(error)}, ensure_ascii=False), file=sys.stderr)
        sys.exit(1)
