#!/usr/bin/env python3
"""Build and verify one exact candidate in task-owned directories only."""
import hashlib
import json
import os
import pathlib
import platform
import re
import stat
import subprocess
import sys

label, expected_source, go_arg, python_arg = sys.argv[1:]
if not re.fullmatch(r'[a-z0-9-]+', label) or not re.fullmatch(r'[a-f0-9]{40}', expected_source):
    raise SystemExit('invalid label/source')
task = pathlib.Path(__file__).resolve().parents[1]
repo = task.parents[1]
go = pathlib.Path(go_arg).resolve(strict=True)
python = pathlib.Path(python_arg).resolve(strict=True)
if platform.system() != 'Darwin' or platform.machine() != 'arm64':
    raise SystemExit('this acceptance requires native macOS arm64')
def git(*args):
    return subprocess.check_output(['git', *args], cwd=repo, text=True).strip()
if git('rev-parse', 'HEAD') != expected_source or git('status', '--porcelain'):
    raise SystemExit('source must be the requested clean snapshot')
os.umask(0o077)
for parent in (task / 'data', task / 'tmp'):
    parent.mkdir(mode=0o700, exist_ok=True)
    info = parent.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o700:
        raise SystemExit('task output parent must be an owner-only directory')
evidence = task / 'data' / label
work = task / 'tmp' / label
evidence.mkdir(mode=0o700)
work.mkdir(mode=0o700)
env = os.environ.copy()
build_environment = dict(TMPDIR='/private/tmp', GOTMPDIR='/private/tmp', GOTOOLCHAIN='local',
                         GOPROXY='off', GOSUMDB='off', GOENV='off', GOWORK='off', GOFLAGS='',
                         GOOS='darwin', GOARCH='arm64', GOARM64='v8.0', CGO_ENABLED='0', GOEXPERIMENT='',
                         PATH=str(go.parent) + ':/usr/bin:/bin:/usr/sbin:/sbin')
env.update(build_environment)
commands = []
def run(name, argv, cwd=repo):
    argv = [str(x) for x in argv]
    stdout, stderr = evidence / (name + '.stdout'), evidence / (name + '.stderr')
    item = {'name': name, 'command': argv, 'cwd': str(cwd)}
    commands.append(item)
    with stdout.open('xb') as out, stderr.open('xb') as err:
        try:
            proc = subprocess.run(argv, cwd=cwd, env=env, stdout=out, stderr=err, timeout=240)
            item['exit'] = proc.returncode
        except subprocess.TimeoutExpired:
            item['timeout'] = True
            raise
    (evidence / (name + '.exit')).write_text(str(proc.returncode) + '\n')
    if proc.returncode:
        raise RuntimeError(name + ' failed; raw output retained')
    return stdout.read_bytes()
def sha(path):
    h = hashlib.sha256()
    with path.open('rb') as f:
        for block in iter(lambda: f.read(1 << 20), b''):
            h.update(block)
    return h.hexdigest()
def inventory(root):
    out = {}
    for p in sorted(root.rglob('*')):
        info = p.lstat()
        if stat.S_ISLNK(info.st_mode) or info.st_uid != os.getuid():
            raise RuntimeError('unsafe owned artifact: ' + str(p))
        if p.is_dir():
            if stat.S_IMODE(info.st_mode) != 0o700:
                raise RuntimeError('non-private directory: ' + str(p))
            continue
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
            raise RuntimeError('unexpected artifact: ' + str(p))
        out[p.relative_to(root).as_posix()] = {'sha256': sha(p), 'mode': stat.S_IMODE(info.st_mode)}
    return out
def expected_plugin(binary, version):
    # Independent, fixed package contract; never derive this list from the
    # package script or its self-generated manifest.
    plugin_files = [
        '.codex-plugin/plugin.json', 'defaults.json', 'skills/orchestrate/SKILL.md',
        'scripts/invoke.sh', 'scripts/agent-bird-hook', 'scripts/agent-bird-agy-hook',
        'hooks/agy-proposed-hooks.json', 'scripts/native-product-bridge.py',
        'lib/native_bridge/__init__.py', 'lib/native_bridge/bridge.py',
        'lib/trial/auth_guard.py', 'lib/trial/auth_wire.py', 'lib/trial/rpc_policy.py',
    ]
    runtime_files = [
        'activation_service.py', 'auth_isolation.py', 'delivery_adapter.py',
        'delivery_audit.py', 'launch_activation.py', 'owned_child_guard.py',
        'owner_helper.py', 'projectproxy_launchd_entrypoint.py',
        'proxy_observer.py', 'proxy_transport.py', 'receipt_store.py',
    ]
    expected, sources = {}, {}
    def derived(rel, value, mode=0o600, sort_keys=True, ensure_ascii=True):
        raw = (json.dumps(value, indent=2, sort_keys=sort_keys, ensure_ascii=ensure_ascii) + '\n').encode()
        expected[rel] = {'sha256': hashlib.sha256(raw).hexdigest(), 'mode': mode}
    for rel in plugin_files:
        source = repo / 'plugins/codex-orchestrator' / rel
        sources[rel] = {'source': str(source.relative_to(repo)), 'sha256': sha(source)}
        if rel == '.codex-plugin/plugin.json':
            value = json.loads(source.read_text())
            value['version'] = version
            derived(rel, value, sort_keys=False, ensure_ascii=False)
        else:
            expected[rel] = {'sha256': sha(source), 'mode': 0o700 if rel.startswith('scripts/') else 0o600}
    runtime_records = []
    for name in runtime_files:
        source = repo / 'runtime/native' / name
        rel = 'runtime/g0/' + name
        mode = 0o700 if name == 'projectproxy_launchd_entrypoint.py' else 0o644
        sources[rel] = {'source': str(source.relative_to(repo)), 'sha256': sha(source)}
        expected[rel] = {'sha256': sha(source), 'mode': mode}
        runtime_records.append({'logical_id': name[:-3], 'path': name, 'sha256': sha(source), 'mode': mode})
    expected['bin/codex-orchestrator'] = {'sha256': sha(binary), 'mode': 0o700}
    derived('runtime/g0/runtime-manifest.json', {'version': 1, 'interpreter': {'path': str(python), 'sha256': sha(python)}, 'files': runtime_records})
    payload = [{'path': rel, **value} for rel, value in sorted(expected.items())]
    derived('payload-manifest.json', {'schema_version': 1, 'kind': 'full-native-hooks', 'files': payload})
    return expected, sources
version = '0.1.0-closure.' + expected_source[:12]
receipt = {'source': expected_source, 'tree': git('rev-parse', 'HEAD^{tree}'),
           'orchestrator_tree': git('rev-parse', 'HEAD:tools/orchestrator'),
           'version': version, 'go': str(go), 'python': str(python), 'python_sha256': sha(python),
           'platform': platform.platform(), 'environment': build_environment,
           'commands': commands, 'accepted': False}
try:
    receipt['go_version'] = run('go-version', [go, 'version']).decode().strip()
    if receipt['go_version'] != 'go version go1.26.8 darwin/arm64':
        raise RuntimeError('acceptance requires pinned Go1.26.8 darwin/arm64')
    run('go-mod-verify', [go, 'mod', 'verify'], repo / 'tools/orchestrator')
    for suffix in ('a', 'b'):
        run('build-' + suffix, [go, 'build', '-trimpath', '-buildvcs=false', '-o', work / ('orchestrator-' + suffix), './cmd/orchestrator'], repo / 'tools/orchestrator')
    binary = work / 'orchestrator-a'
    if sha(binary) != sha(work / 'orchestrator-b'):
        raise RuntimeError('build bytes differ')
    receipt['binary_sha256'] = sha(binary)
    receipt['go_build_info'] = run('go-build-info', [go, 'version', '-m', binary]).decode()
    for suffix in ('a', 'b'):
        run('package-' + suffix, ['/bin/sh', repo / 'tasks/g1-g4-delivery/scripts/package-plugin.sh',
                                '--binary', work / ('orchestrator-' + suffix), '--python', python,
                                '--version', version, '--kind', 'full-native-hooks', '--out', work / ('package-' + suffix)])
    first, second = inventory(work / 'package-a'), inventory(work / 'package-b')
    if first != second:
        raise RuntimeError('package bytes/modes differ')
    receipt['package_files'] = first
    plugin = work / 'package-a/plugin'
    expected_payload, source_mapping = expected_plugin(binary, version)
    if inventory(plugin) != expected_payload:
        raise RuntimeError('package does not equal the independent source closure')
    receipt['source_mapping'] = source_mapping
    receipt['expected_payload'] = expected_payload
    package = json.loads((work / 'package-a/package-manifest.json').read_text())
    if package['version'] != version or package['binary_path'] != 'plugin/bin/codex-orchestrator':
        raise RuntimeError('package identity differs')
    declared = {x['path']: {'sha256': x['sha256'], 'mode': x['mode']} for x in package['files']}
    if declared != {k: v for k, v in first.items() if k != 'package-manifest.json'} or sha(plugin / 'bin/codex-orchestrator') != receipt['binary_sha256']:
        raise RuntimeError('package manifest/source mismatch')
    destination, data = work / 'install', work / 'runtime-data'
    data.mkdir(mode=0o700)
    sentinel = data / 'keep.txt'
    sentinel.write_text('task-owned runtime data must survive uninstall\n')
    def admin(name, operation, body):
        path = evidence / (name + '.request.json')
        with path.open('x') as f:
            json.dump(body, f, sort_keys=True)
        reply = json.loads(run(name, [binary, operation, '--request', path]))
        if reply.get('status') != 'ok' or reply.get('operation') != operation:
            raise RuntimeError('invalid admin receipt: ' + name)
        return reply['result']
    request = {'source_root': str(plugin), 'binary_path': str(plugin / 'bin/codex-orchestrator'),
               'destination_root': str(destination), 'data_root': str(data), 'version': version}
    installed = admin('install-first', 'install', request)
    installed_root = destination / 'versions' / version
    if installed['version_root'] != str(installed_root) or installed['binary_sha256'] != receipt['binary_sha256']:
        raise RuntimeError('installed identity mismatch')
    before = inventory(installed_root)
    if set(before) != set(expected_payload) | {'manifest.json'}:
        raise RuntimeError('installed closure has missing or extra files')
    for rel, value in expected_payload.items():
        if before.get(rel) != value:
            raise RuntimeError('installed payload mismatch: ' + rel)
    installed_binary = installed_root / 'bin/codex-orchestrator'
    info = installed_binary.stat()
    again = admin('install-replay', 'install', request)
    after_info = installed_binary.stat()
    if again != installed or inventory(installed_root) != before or (info.st_dev, info.st_ino, info.st_mtime_ns) != (after_info.st_dev, after_info.st_ino, after_info.st_mtime_ns):
        raise RuntimeError('exact installation replay changed state')
    doctor = admin('doctor-installed', 'doctor', {'destination_root': str(destination)})
    if not doctor['healthy'] or doctor['versions'] != [version] or doctor.get('issues') or doctor.get('pins'):
        raise RuntimeError('installed Doctor not healthy')
    removed = admin('uninstall-owned', 'uninstall', {'destination_root': str(destination), 'version': version})
    if removed['status'] != 'removed' or installed_root.exists() or sentinel.read_text() != 'task-owned runtime data must survive uninstall\n':
        raise RuntimeError('owned uninstall/data preservation failed')
    final = admin('doctor-after-uninstall', 'doctor', {'destination_root': str(destination)})
    if not final['healthy'] or final.get('versions') or final.get('issues') or final.get('pins'):
        raise RuntimeError('post-uninstall Doctor not healthy')
    if inventory(work / 'package-a') != first or sha(python) != receipt['python_sha256'] or git('rev-parse', 'HEAD') != expected_source or git('status', '--porcelain'):
        raise RuntimeError('input source/package changed during acceptance')
    receipt.update(installed_files=before, installed_binary_identity={'device': info.st_dev, 'inode': info.st_ino, 'mtime_ns': info.st_mtime_ns},
                   package_path=str(work / 'package-a'), data_preserved=True, accepted=True)
except Exception as exc:
    receipt['failure'] = repr(exc)
finally:
    (evidence / 'receipt.json').write_text(json.dumps(receipt, indent=2) + '\n')
print(json.dumps({k: receipt[k] for k in ('source', 'version', 'accepted')}))
raise SystemExit(0 if receipt['accepted'] else 1)
