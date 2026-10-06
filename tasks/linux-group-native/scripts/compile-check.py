#!/usr/bin/env python3
import json,pathlib,subprocess
root=pathlib.Path(__file__).resolve().parents[3];module=root/'tools/orchestrator';out=root/'tasks/linux-group-native/data';go='/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go'
receipts=[]
for name,platform,args in [('darwin-compile','darwin',['test','-c','-o',str(root/'tasks/linux-group-native/tmp/root-bin/process-darwin.test'),'./internal/process']),('linux-vet','linux',['vet','./internal/process'])]:
 command=['env','GOOS='+platform,'GOARCH=arm64','CGO_ENABLED=0','GOMAXPROCS=2',go]+args
 with (out/(name+'-explicit.log')).open('wb') as log:r=subprocess.run(command,cwd=module,stdout=log,stderr=subprocess.STDOUT)
 receipts.append(dict(command=command,cwd=str(module),exit_code=r.returncode))
 if r.returncode:break
(out/'compile-vet-explicit-receipt.json').write_text(json.dumps(receipts,indent=2)+'\n');print(receipts);raise SystemExit(receipts[-1]['exit_code'])
