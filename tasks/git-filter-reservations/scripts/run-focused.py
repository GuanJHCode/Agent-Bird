#!/usr/bin/env python3
import json, os, pathlib, subprocess, sys
root=pathlib.Path(__file__).resolve().parents[3]
name,*command=sys.argv[1:]
data=root/'tasks/git-filter-reservations/data'
paths={s:data/(name+'.'+s) for s in ('stdout.jsonl','stderr.txt','exit.txt','command.json')}
if any(p.exists() for p in paths.values()):raise SystemExit('refusing to overwrite evidence')
os.umask(0o077)
env=os.environ.copy();env['TMPDIR']='/private/tmp';env['PATH']='/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin:'+env['PATH']
paths['command.json'].write_text(json.dumps({'command':command,'cwd':str(root),'head':subprocess.check_output(['git','rev-parse','HEAD'],cwd=root,text=True).strip(),'dirty':subprocess.check_output(['git','status','--short'],cwd=root,text=True),'go_version':subprocess.check_output(['go','version'],env=env,text=True).strip()},indent=2)+'\n')
with paths['stdout.jsonl'].open('x') as out,paths['stderr.txt'].open('x') as err:r=subprocess.run(command,cwd=root,env=env,stdout=out,stderr=err)
paths['exit.txt'].write_text(str(r.returncode)+'\n');print(json.dumps({'name':name,'exit':r.returncode}));raise SystemExit(r.returncode)
