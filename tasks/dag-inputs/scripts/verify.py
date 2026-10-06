#!/usr/bin/env python3
"""Run bounded pure tests against the current snapshot, retaining raw evidence."""
import hashlib,json,os,pathlib,re,subprocess,sys,time
root=pathlib.Path(__file__).resolve().parents[3]
module=root/'tools/orchestrator'
out=root/'tasks/dag-inputs/data'
go='/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go'
# These suites use SQLite/controlled files; no Provider or subprocess actors.
files=['candidate_input_test.go','dependency_inputs_test.go','dependency_input_migration_test.go','dependency_review_retry_test.go','review_retry_test.go','rework_test.go','rework_migration_test.go','policy_migration_test.go','original_owner_migration_test.go','continuation_migration_test.go','bridge_schema_test.go','config_abandon_test.go','core_recovery_scope_test.go','decision_attempt_transaction_test.go','decision_attempt_review_fix_test.go','quota_bench_test.go','provider_controls_test.go']
names=[]
for f in files:names+=re.findall(r'^func (Test\w+)\(', (module/'internal/store'/f).read_text(),re.M)
for f in [module/'internal/host/dependency_inputs_test.go',module/'cmd/orchestrator/dependency_inputs_test.go']:names+=re.findall(r'^func (Test\w+)\(',f.read_text(),re.M)
mode=sys.argv[1] if len(sys.argv)>1 else 'normal'
if mode=='race-final':names=[n for n in names if n.startswith('TestDependency') or n.startswith('TestCandidatePlan')]
args=[go,'test','-json','-count=1','-p','1']+(['-race'] if mode.startswith('race') else [])+['./internal/store','./internal/host','./cmd/orchestrator','-run','^('+'|'.join(names)+')$']
env=os.environ.copy();env['GOMAXPROCS']='2'
start=time.time();raw=out/(mode+'.jsonl')
with raw.open('wb') as log:result=subprocess.run(args,cwd=module,env=env,stdout=log,stderr=subprocess.STDOUT)
counts={key:0 for key in ('pass','fail','skip')};top=0;packages=[]
for l in raw.read_text().splitlines():
 try:r=json.loads(l)
 except ValueError:continue
 if r.get('Action') in counts:
  if r.get('Test'):
   counts[r['Action']]+=1
   if '/' not in r['Test']:top+=1
  else:packages.append({k:r[k] for k in ('Package','Action','Elapsed') if k in r})
receipt=dict(command=args,cwd=str(module),exit_code=result.returncode,seconds=time.time()-start,sha256=hashlib.sha256(raw.read_bytes()).hexdigest(),counts=counts,top_level=top,packages=packages)
(out/(mode+'-receipt.json')).write_text(json.dumps(receipt,indent=2)+'\n')
print(json.dumps(receipt,ensure_ascii=False))
sys.exit(result.returncode)
