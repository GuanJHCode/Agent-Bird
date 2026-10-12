#!/usr/bin/env python3
"""Build a version-bound Codex skill plugin; delegate through the native owner."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import stat
import subprocess
import sys
import tempfile

PROVIDERS = ('codex', 'claude', 'agy', 'grok')


def digest_file(path, maximum=512 << 20):
    path = Path(path)
    if not path.is_absolute() or path.resolve(strict=True) != path:
        raise ValueError('binding_path_untrusted')
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, 'rb') as stream:
        before = os.fstat(stream.fileno())
        if not stat.S_ISREG(before.st_mode) or before.st_uid != os.getuid() or before.st_nlink != 1 or before.st_mode & 0o022 or before.st_size > maximum:
            raise ValueError('binding_file_untrusted')
        digest = hashlib.sha256()
        remaining = maximum
        while True:
            block = stream.read(min(65536, remaining + 1))
            if not block:
                break
            remaining -= len(block)
            if remaining < 0:
                raise ValueError('binding_changed')
            digest.update(block)
        signature = lambda info: (info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns, info.st_ctime_ns)
        if signature(before) != signature(os.fstat(stream.fileno())) or signature(before) != signature(path.lstat()):
            raise ValueError('binding_changed')
    return digest.hexdigest()


def private_directory(path):
    path = Path(path)
    info = path.lstat()
    if path.resolve(strict=True) != path or not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o700:
        raise ValueError('runtime_directory_untrusted')
    return path


def read_json(path):
    # Configuration contains paths/hashes only; it grants no owner identity.
    digest_file(path, 1 << 20)
    return json.loads(Path(path).read_text())


def write_private(path, value):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w') as stream:
        json.dump(value, stream, ensure_ascii=False, indent=2)
        stream.write('\n')


def run_json(argv):
    result = subprocess.run(argv, capture_output=True, text=True, timeout=45, check=True)
    return json.loads(result.stdout)


def inspect_installation(runtime):
    runtime = private_directory(runtime)
    installation = read_json(runtime / 'installation.json')
    binary = Path(installation['binary'])
    binary.relative_to(runtime / 'packages/versions')
    digest = digest_file(binary)
    report = run_json([str(binary), 'doctor', '--request', str(runtime / 'doctor-request.json')])
    if report.get('status') != 'ok' or report.get('result', {}).get('healthy') is not True:
        raise ValueError('installed_package_unhealthy')
    platform = run_json([str(binary), 'platform', 'capabilities'])
    if platform.get('code_modified') is not False or not re.fullmatch('[0-9a-f]{40}', platform.get('code_sha', '')):
        raise ValueError('installed_build_unqualified')
    pins = {}
    for provider in PROVIDERS:
        if provider in installation['providers']:
            pin = runtime / 'locks' / (provider + '.json')
            if read_json(pin) != installation['providers'][provider]['lock']:
                raise ValueError('provider_pin_changed')
            pins[provider] = {'path': str(pin), 'sha256': digest_file(pin, 65536)}
    if not pins:
        raise ValueError('provider_not_configured')
    return {'version': 1, 'runtime': str(runtime), 'binary': str(binary), 'binary_sha256': digest, 'code_sha': platform['code_sha'], 'providers': pins}


def load_binding(plugin):
    record = read_json(Path(plugin) / 'binding.json')
    if record.get('version') != 1 or not re.fullmatch('[0-9a-f]{40}', record.get('code_sha', '')):
        raise ValueError('binding_invalid')
    runtime = private_directory(record['runtime'])
    binary = Path(record['binary'])
    binary.relative_to(runtime / 'packages/versions')
    if digest_file(binary) != record['binary_sha256']:
        raise ValueError('binding_changed')
    if not os.access(binary, os.X_OK):
        raise ValueError('binding_binary_not_executable')
    for provider, pin in record['providers'].items():
        if provider not in PROVIDERS or Path(pin['path']) != runtime / 'locks' / (provider + '.json'):
            raise ValueError('binding_invalid')
        if digest_file(pin['path'], 65536) != pin['sha256']:
            raise ValueError('binding_changed')
    return record


SKILL = '''---
name: agent-bird
description: Use when the user asks Agent Bird, Codex, Claude, Grok or AGY to delegate an independent task or review to another Provider.
---

# Agent Bird

Codex, Claude, Grok and AGY are peers; use the requested target. This installed
entry binds one immutable runtime. Ordinary native Codex sessions use their own
verified process ancestry and thread; do not create or copy an owner request.

Resolve the plugin root two directories above this skill directory. Run its
absolute `scripts/agent-bird resolve` once for the current unit of work and retain
the returned version/code SHA binding. A failure is a blocker, never permission
to search for another runtime or weaken protections.

Execute the subtask yourself using this plugin entry (do not just show a command):

```
<plugin-root>/scripts/agent-bird call --provider claude --directory /absolute/project --task 'Review the code; return concise findings.'
```

Targets are `codex`, `claude`, `grok`, `agy`; only configured pins are available.
For an explicitly authorized edit add `--mode workspace-write --path file.txt`
(repeat `--path` for each exact relative path). The default is read-only. Optional
`--timeout-ms` is 1..3600000, default 120000. Keep tasks concise and independent.

Run one subtask at a time. No recursive delegation, automatic retries, parallel
native/Bird children or silent target changes. Keep the foreground invocation
alive until it returns; if the shell tool yields a process session, wait on that
same process. Cancellation must reach the original process and retain its handle.

Read the JSON status, failure_reason, process_exited, changes_known, changed_files
and workspace. A shell exit code or target prose is not success/exit proof. Writes
are isolated candidates; never overwrite the original or merge automatically.
The result excerpt is capped at 4KiB; result_truncated/hash/bytes describe the full
result retained by the handle. Report truncation instead of inventing details.
Missing ownership, changed pin, unknown exit or unresolved prior call is a blocker.
Do not install, change account/model/permissions, delete runtime state or borrow a
controller to get around a failure. Existing task handles keep their original
runtime binding even after a plugin upgrade.
'''


def build_plugin(runtime, output, version):
    if not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?', version):
        raise ValueError('version_invalid')
    record = inspect_installation(Path(runtime))
    output = Path(output)
    if not output.is_absolute() or output.resolve() != output:
        raise ValueError('output_path_invalid')
    output.mkdir(mode=0o700)
    plugin = output / 'plugins/agent-bird-local'
    for rel in ('.codex-plugin', 'skills/agent-bird', 'scripts'):
        (plugin / rel).mkdir(parents=True, mode=0o700)
    write_private(plugin / 'binding.json', record)
    write_private(plugin / '.codex-plugin/plugin.json', {'name': 'agent-bird-local', 'version': version, 'description': 'Version-bound Agent Bird calls from ordinary Codex sessions.', 'skills': './skills/', 'interface': {'displayName': 'Agent Bird', 'shortDescription': 'Call pinned Providers from this session.'}})
    (plugin / 'skills/agent-bird/SKILL.md').write_text(SKILL)
    entry = plugin / 'scripts/agent-bird.py'; entry.write_bytes(Path(__file__).read_bytes()); entry.chmod(0o600)
    wrapper = plugin / 'scripts/agent-bird'
    wrapper.write_text('#!/bin/sh\nset -eu\nroot=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)\nexec ' + shlex.quote(str(Path(sys.executable).resolve())) + ' "$root/scripts/agent-bird.py" "$@"\n')
    wrapper.chmod(0o700)
    market = output / '.agents/plugins'; market.mkdir(parents=True, mode=0o700)
    write_private(market / 'marketplace.json', {'name': 'agent-bird-local', 'interface': {'displayName': 'Agent Bird Local'}, 'plugins': [{'name': 'agent-bird-local', 'source': {'source': 'local', 'path': './plugins/agent-bird-local'}, 'policy': {'installation': 'AVAILABLE', 'authentication': 'ON_INSTALL'}, 'category': 'Productivity'}]})
    return plugin


def delegate(plugin, args):
    binding = load_binding(plugin)
    if args.provider not in binding['providers']:
        raise ValueError('provider_not_configured')
    directory = Path(args.directory)
    if not directory.is_absolute() or directory.resolve(strict=True) != directory or not directory.is_dir():
        raise ValueError('directory_must_be_canonical')
    if not args.task.strip() or len(args.task.encode()) > 60000 or '\0' in args.task or not 1 <= args.timeout_ms <= 3600000:
        raise ValueError('call_request_invalid')
    requests = Path(binding['runtime']) / 'plugin-requests'
    requests.mkdir(mode=0o700, exist_ok=True)
    private_directory(requests)
    fd, path = tempfile.mkstemp(prefix='call-', suffix='.json', dir=requests)
    with os.fdopen(fd, 'w') as stream:
        json.dump({'version': 1, 'provider': args.provider, 'task': args.task, 'directory': str(directory), 'mode': args.mode, 'paths': args.path, 'timeout_ms': args.timeout_ms, 'provider_lock': binding['providers'][args.provider]['path']}, stream)
    binary = binding['binary']
    # Preserve the real native environment/ancestry and process signals. The Go
    # core validates owner/worker exclusion, pins, workspace, budget and Wait.
    os.execv(binary, [binary, 'call', '--compact', '--request', path, '--state-dir', str(Path(binding['runtime']) / 'state')])


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='command', required=True)
    build = commands.add_parser('build')
    build.add_argument('--runtime', required=True)
    build.add_argument('--out', required=True)
    build.add_argument('--version', default='1.0.0-rc.4')
    commands.add_parser('resolve')
    call = commands.add_parser('call')
    call.add_argument('--provider', choices=PROVIDERS, required=True)
    call.add_argument('--directory', required=True)
    call.add_argument('--task', required=True)
    call.add_argument('--mode', choices=('read-only', 'workspace-write'), default='read-only')
    call.add_argument('--path', action='append', default=[])
    call.add_argument('--timeout-ms', type=int, default=120000)
    args = parser.parse_args()
    plugin = Path(__file__).resolve().parent.parent
    if args.command == 'build':
        print(json.dumps({'plugin': str(build_plugin(args.runtime, args.out, args.version))}))
    elif args.command == 'resolve':
        print(json.dumps(load_binding(plugin)))
    else:
        delegate(plugin, args)


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        print(json.dumps({'status': 'failed', 'failure_reason': str(error)}), file=sys.stderr)
        sys.exit(1)
