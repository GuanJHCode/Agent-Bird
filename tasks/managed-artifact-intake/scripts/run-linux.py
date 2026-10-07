#!/usr/bin/env python3
"""Run an owned, offline Linux test container and retain terminal evidence."""
import hashlib
import json
import pathlib
import re
import subprocess
import sys

label, binary_arg, pattern = sys.argv[1:]
if not re.fullmatch(r'[a-z0-9-]+', label):
    raise SystemExit('invalid label')
base = pathlib.Path(__file__).resolve().parents[1] / 'data' / 'linux'
binary = pathlib.Path(binary_arg).resolve()
receipt = base / (label + '-receipt.json')
log = base / (label + '.log')
if receipt.exists() or log.exists():
    raise SystemExit('refuse to replace existing evidence')
image = 'sha256:6ab0b6e7381779332f97b8ca76193e45b0756f38d4c0dcda72dbb3c32061ab99'
create = ['docker', 'create', '--pull=never', '--init', '--name', 'root-artifact-intake-' + label,
          '--network', 'none', '--read-only', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges',
          '--user', '65534:65534', '--pids-limit', '96', '--memory', '512m', '--cpus', '2',
          '--tmpfs', '/tmp:rw,noexec,nosuid,nodev,size=128m,mode=1777', '--mount',
          'type=bind,source='+str(binary.parent)+',target=/test,readonly', '--entrypoint', '/test/'+binary.name,
          image, '-test.run='+pattern, '-test.v', '-test.count=1', '-test.timeout=120s']
created = subprocess.run(create, capture_output=True, text=True, check=True, timeout=20)
cid = created.stdout.strip()
if not re.fullmatch(r'[a-f0-9]{64}', cid):
    raise SystemExit('missing exact container identity')
result = {'label':label, 'create_command':create, 'cid':cid,
          'binary_sha256':hashlib.sha256(binary.read_bytes()).hexdigest(), 'state':'created'}
receipt.write_text(json.dumps(result, indent=2)+'\n')
with log.open('wb') as out:
    run = subprocess.run(['docker','start','--attach',cid], stdout=out, stderr=subprocess.STDOUT, timeout=150)
inspection = subprocess.run(['docker','inspect',cid], capture_output=True, text=True, check=True, timeout=20)
meta = json.loads(inspection.stdout)[0]
raw = log.read_bytes()
body = raw.decode(errors='replace')
result.update(start_exit=run.returncode, state=meta['State'], image=meta['Image'],
              pass_tests=re.findall(r'^\s*--- PASS: (\S+)',body,re.M),
              fail_tests=re.findall(r'^\s*--- FAIL: (\S+)',body,re.M),
              skip_tests=re.findall(r'^\s*--- SKIP: (\S+)',body,re.M),
              log_sha256=hashlib.sha256(raw).hexdigest())
receipt.write_text(json.dumps(result,indent=2)+'\n')
if meta['State']['Running']:
    raise SystemExit('container still running: '+cid)
removed = subprocess.run(['docker','rm',cid],capture_output=True,text=True,timeout=20)
absent = subprocess.run(['docker','inspect',cid],capture_output=True,text=True,timeout=20)
result.update(remove_exit=removed.returncode, post_remove_inspect_exit=absent.returncode,
              post_remove_inspect=absent.stdout+absent.stderr)
receipt.write_text(json.dumps(result,indent=2)+'\n')
print(json.dumps({'label':label,'cid':cid,'exit':meta['State']['ExitCode'],
                  'pass':len(result['pass_tests']),'fail':len(result['fail_tests']),
                  'skip':len(result['skip_tests']),'removed':removed.returncode==0 and absent.returncode!=0}))
raise SystemExit(0 if run.returncode==0 and meta['State']['ExitCode']==0 and not meta['State']['OOMKilled'] and removed.returncode==0 and absent.returncode!=0 else 1)
