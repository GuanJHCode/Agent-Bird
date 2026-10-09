#!/usr/bin/env python3
"""Run an owned, offline Linux test container and retain terminal evidence."""
import hashlib
import json
import pathlib
import re
import subprocess
import sys

label, binary_arg, mode, expected_arg = sys.argv[1:]
modes = {
    'owner-grant': ('^(TestOwnerGrantPublicationFailureDoesNotAuthorize|TestOwnerGrantPublicationCanonicalAncestorAlias)$', ('TestOwnerGrantPublicationFailureDoesNotAuthorize', 'TestOwnerGrantPublicationCanonicalAncestorAlias')),
}
if mode not in modes:
    raise SystemExit('unknown test mode')
pattern, prefixes = modes[mode]
if not re.fullmatch(r'[a-z0-9-]+', label):
    raise SystemExit('invalid label')
expected = json.loads(pathlib.Path(expected_arg).read_text())
if not isinstance(expected, list) or not expected or len(set(expected)) != len(expected) or any(not isinstance(name, str) or not name.startswith(prefixes) for name in expected):
    raise SystemExit('invalid expected test set')
base = pathlib.Path(__file__).resolve().parents[1] / 'data' / 'linux'
base.mkdir(parents=True, exist_ok=True)
binary = pathlib.Path(binary_arg).resolve(strict=True)
if not binary.is_file() or binary.is_symlink():
    raise SystemExit('invalid binary')
binary_sha256 = hashlib.sha256(binary.read_bytes()).hexdigest()
receipt = base / (label + '-receipt.json')
log = base / (label + '.log')
if receipt.exists() or log.exists():
    raise SystemExit('refuse to replace existing evidence')
image = 'sha256:6ab0b6e7381779332f97b8ca76193e45b0756f38d4c0dcda72dbb3c32061ab99'
create = ['docker', 'create', '--pull=never', '--init', '--name', 'root-owner-grant-' + label,
          '--network', 'none', '--read-only', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges',
          '--user', '65534:65534', '--pids-limit', '96', '--memory', '768m', '--cpus', '2',
          '--tmpfs', '/tmp:rw,noexec,nosuid,nodev,size=128m,mode=1777',
          '--tmpfs', '/private/tmp:rw,exec,nosuid,nodev,size=384m,mode=1777',
          '--env', 'TMPDIR=/private/tmp', '--mount',
          'type=bind,source='+str(binary.parent)+',target=/test,readonly', '--entrypoint', '/test/'+binary.name,
          image, '-test.run='+pattern, '-test.v', '-test.count=1', '-test.timeout=300s']
created = subprocess.run(create, capture_output=True, text=True, timeout=20)
if created.returncode:
    (base / (label + '-create.stdout')).write_text(created.stdout)
    (base / (label + '-create.stderr')).write_text(created.stderr)
    (base / (label + '-create.exit')).write_text(str(created.returncode)+'\n')
    raise SystemExit(created.returncode)
cid = created.stdout.strip()
if not re.fullmatch(r'[a-f0-9]{64}', cid):
    raise SystemExit('missing exact container identity')
result = {'label': label, 'create_command': create, 'cid': cid, 'expected_tests': expected,
          'binary_sha256': binary_sha256, 'state': 'created'}
passed = False
try:
    (base / (label + '-create.stdout')).write_text(created.stdout)
    (base / (label + '-create.stderr')).write_text(created.stderr)
    (base / (label + '-create.exit')).write_text(str(created.returncode)+'\n')
    receipt.write_text(json.dumps(result, indent=2) + '\n')
    with log.open('wb') as out:
        run = subprocess.run(['docker', 'start', '--attach', cid], stdout=out, stderr=subprocess.STDOUT, timeout=330)
    result['start_exit'] = run.returncode
    inspection = subprocess.run(['docker', 'inspect', cid], capture_output=True, text=True, check=True, timeout=20)
    meta = json.loads(inspection.stdout)[0]
    raw = log.read_bytes()
    body = raw.decode(errors='replace')
    result.update(state=meta['State'], image=meta['Image'],
                  pass_tests=re.findall(r'^\s*--- PASS: (\S+)', body, re.M),
                  fail_tests=re.findall(r'^\s*--- FAIL: (\S+)', body, re.M),
                  skip_tests=re.findall(r'^\s*--- SKIP: (\S+)', body, re.M),
                  log_sha256=hashlib.sha256(raw).hexdigest())
    passed = (run.returncode == 0 and not meta['State']['Running'] and meta['State']['ExitCode'] == 0
              and not meta['State']['OOMKilled'] and sorted(result['pass_tests']) == sorted(expected)
              and not result['fail_tests'] and not result['skip_tests'])
except Exception as exc:
    result['runner_error'] = repr(exc)
finally:
    # Only the exact CID created above is eligible for forced cleanup. A
    # timeout or failed observation remains a failure regardless of removal.
    try:
        removed = subprocess.run(['docker', 'rm', '-f', cid], capture_output=True, text=True, timeout=20)
        result.update(remove_exit=removed.returncode, remove_output=removed.stdout + removed.stderr)
    except Exception as exc:
        result['remove_error'] = repr(exc)
    try:
        absent = subprocess.run(['docker', 'inspect', cid], capture_output=True, text=True, timeout=20)
        result.update(post_remove_inspect_exit=absent.returncode,
                      post_remove_inspect=absent.stdout + absent.stderr)
        result['removed'] = result.get('remove_exit') == 0 and absent.returncode != 0 and 'No such' in absent.stderr
    except Exception as exc:
        result['inspect_error'] = repr(exc)
    result['accepted'] = passed and result.get('removed', False)
    receipt.write_text(json.dumps(result, indent=2) + '\n')
print(json.dumps({'label': label, 'cid': cid, 'accepted': result['accepted'],
                  'pass': len(result.get('pass_tests', [])), 'fail': len(result.get('fail_tests', [])),
                  'skip': len(result.get('skip_tests', [])), 'removed': result.get('removed', False)}))
raise SystemExit(0 if result['accepted'] else 1)
