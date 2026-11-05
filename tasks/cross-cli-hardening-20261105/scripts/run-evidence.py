#!/usr/bin/env python3
"""Run a bounded task command and retain exact output, source identity and counts."""
import argparse, datetime, hashlib, json, os, pathlib, subprocess, time
p=argparse.ArgumentParser();p.add_argument('--name',required=True);p.add_argument('--cwd',required=True);p.add_argument('--output',required=True);p.add_argument('--timeout',type=int,default=900);p.add_argument('command',nargs=argparse.REMAINDER);a=p.parse_args()
cmd=a.command[1:] if a.command[:1]==['--'] else a.command
out=pathlib.Path(a.output);out.mkdir(parents=True,exist_ok=True)
log=out/(a.name+'.log');report=out/(a.name+'.json')
if log.exists() or report.exists(): raise SystemExit('evidence name already exists')
def git(*args): return subprocess.check_output(['git','-C',a.cwd,*args])
commit=git('rev-parse','HEAD').decode().strip();diff=git('diff','HEAD','--binary')
source_root=pathlib.Path(git('rev-parse','--show-toplevel').decode().strip())
source_hash=hashlib.sha256()
for name in sorted(set(subprocess.check_output(['git','-C',str(source_root),'ls-files','-c','-o','--exclude-standard','-z']).decode().split('\0'))):
 if not name or not (name.startswith(('tools/','plugins/','runtime/')) or ('/scripts/' in name and name.startswith('tasks/'))): continue
 path=source_root/name
 if path.is_file() and not path.is_symlink():
  source_hash.update(name.encode()+b'\0'+hashlib.sha256(path.read_bytes()).digest())
environment={key:os.environ[key] for key in ['PATH','GOENV','GOTOOLCHAIN','GOPROXY','GOCACHE','TMPDIR'] if key in os.environ}
started=datetime.datetime.now(datetime.timezone.utc).isoformat();begin=time.monotonic()
with log.open('xb') as f:
 try: result=subprocess.run(cmd,cwd=a.cwd,stdout=f,stderr=subprocess.STDOUT,timeout=a.timeout);code=result.returncode
 except subprocess.TimeoutExpired: code=124
counts=dict(passed=None,failed=None,errors=None,skipped=None)
if '-json' in cmd and 'test' in cmd:
 counts=dict(passed=0,failed=0,errors=None,skipped=0)
 package_failures=0
 for line in log.read_text(errors='replace').splitlines():
  try: event=json.loads(line)
  except ValueError: continue
  act=event.get('Action')
  if event.get('Test') and act in ['pass','fail','skip']: counts[{'pass':'passed','fail':'failed','skip':'skipped'}[act]]+=1
  if not event.get('Test') and act=='fail': package_failures+=1
record=dict(command=cmd,cwd=a.cwd,start=started,elapsed_seconds=round(time.monotonic()-begin,3),exit_code=code,source_commit=commit,tracked_diff_sha256=hashlib.sha256(diff).hexdigest(),counts=counts,package_failures=locals().get('package_failures'),environment=environment,production_files_sha256=source_hash.hexdigest(),log_sha256=hashlib.sha256(log.read_bytes()).hexdigest())
report.write_text(json.dumps(record,indent=2)+'\n');print(json.dumps(record));raise SystemExit(code)
