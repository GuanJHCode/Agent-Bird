#!/usr/bin/env python3
"""Compile and vet a fixed candidate, retaining each command's actual output."""
import concurrent.futures, hashlib, json, os, pathlib, subprocess
base = pathlib.Path(__file__).resolve().parents[1]
root = base.parents[1]
out = base / 'data' / 'build-final'
binaries = base / 'tmp' / 'linux'
if out.exists(): raise SystemExit('refuse to overwrite build evidence')
out.mkdir(parents=True)
binaries.mkdir(parents=True, exist_ok=True)
go = '/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go'
env = os.environ.copy()
env['TMPDIR'] = '/private/tmp'
env['PATH'] = str(pathlib.Path(go).parent) + ':' + env['PATH']
head = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=root, text=True).strip()
jobs = [
 ('build', ['build', './...'], {}),
 ('vet', ['vet', './internal/contract', './internal/ipc', './internal/store', './internal/coordinator', './internal/host', './cmd/orchestrator'], {}),
 ('linux-store', ['test', '-c', '-o', str(binaries / 'store-final.test'), './internal/store'], {'GOOS':'linux', 'GOARCH':'arm64', 'CGO_ENABLED':'0'}),
 ('linux-native', ['test', '-c', '-o', str(binaries / 'native-final.test'), './cmd/orchestrator'], {'GOOS':'linux', 'GOARCH':'arm64', 'CGO_ENABLED':'0'}),
]
def run(job):
 label, args, extra = job
 with (out / (label+'.stdout')).open('wb') as stdout, (out / (label+'.stderr')).open('wb') as stderr:
  result = subprocess.run([go, *args], cwd=root / 'tools/orchestrator', env={**env, **extra}, stdout=stdout, stderr=stderr)
 (out / (label+'.exit')).write_text(str(result.returncode)+'\n')
 row = {'label':label, 'source':head, 'command':[go,*args], 'environment':{'TMPDIR':'/private/tmp', **extra}, 'exit':result.returncode}
 for kind in ['stdout','stderr']:
  data=(out/(label+'.'+kind)).read_bytes()
  row[kind+'_bytes']=len(data)
  row[kind+'_sha256']=hashlib.sha256(data).hexdigest()
 if '-o' in args and result.returncode==0:
  binary=pathlib.Path(args[args.index('-o')+1])
  row['binary']=str(binary)
  row['binary_sha256']=hashlib.sha256(binary.read_bytes()).hexdigest()
 (out/(label+'.json')).write_text(json.dumps(row,indent=2)+'\n')
 print(json.dumps(row), flush=True)
 return result.returncode
with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
 results=list(pool.map(run,jobs))
raise SystemExit(1 if any(results) else 0)
