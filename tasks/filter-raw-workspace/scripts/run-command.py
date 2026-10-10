#!/usr/bin/env python3
import json, os, pathlib, re, subprocess, sys
root=pathlib.Path(__file__).resolve().parents[3]
label,target,*argv=sys.argv[1:]
if not re.fullmatch(r'[a-z0-9-]+',label) or target not in ('darwin','linux'):raise SystemExit('invalid label/target')
data=root/'tasks/filter-raw-workspace/data'
paths={s:data/(label+'.'+s) for s in ('command.json','stdout','stderr','exit')}
if any(p.exists() for p in paths.values()):raise SystemExit('refuse to overwrite evidence')
fixed={'TMPDIR':'/private/tmp','GOTOOLCHAIN':'local','GOENV':'off','GOWORK':'off','GOFLAGS':'','GOPROXY':'off','GOSUMDB':'off','GOOS':target,'GOARCH':'arm64','GOARM64':'v8.0','CGO_ENABLED':'0' if target=='linux' else '1'}
env=os.environ.copy();env.update(fixed)
paths['command.json'].write_text(json.dumps({'source':subprocess.check_output(['git','rev-parse','HEAD'],cwd=root,text=True).strip(),'dirty':subprocess.check_output(['git','status','--short'],cwd=root,text=True),'argv':argv,'cwd':str(root/'tools/orchestrator'),'environment':fixed},indent=2)+'\n')
with paths['stdout'].open('xb') as out,paths['stderr'].open('xb') as err:r=subprocess.run(argv,cwd=root/'tools/orchestrator',env=env,stdout=out,stderr=err)
paths['exit'].write_text(str(r.returncode)+'\n');print(json.dumps({'label':label,'exit':r.returncode}));raise SystemExit(r.returncode)
