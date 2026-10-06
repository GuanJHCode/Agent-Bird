#!/usr/bin/env python3
"""Retain uncached raw Go evidence and explicit test counts for Task8."""
import collections,hashlib,json,os,pathlib,re,subprocess,sys,time
root=pathlib.Path(__file__).resolve().parents[3]
out=root/'tasks/dag-rework/data'
module=root/'tools/orchestrator'
go='/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go'
mode=sys.argv[1]
args=[go,'test','-json','-count=1','-p','1']
if mode.startswith('race'):args+=['-race']
if mode.startswith(('normal','race')):
 args+=['./internal/store','-timeout=12m']
 if mode.startswith('race'):
  files=['dag_rework_test.go','rework_test.go','rework_migration_test.go','decision_attempt_chain_test.go','decision_attempt_legacy_test.go','decision_attempt_transaction_test.go','decision_attempt_review_fix_test.go','policy_closure_test.go','policy_retry_test.go','policy_hold_quota_test.go','attempt_budget_contract_test.go','fifo_batch_test.go','dependency_inputs_test.go','dependency_review_retry_test.go','config_generation_test.go','provider_controls_test.go']
  if mode.startswith('race-affected'):files=['dag_rework_test.go','rework_test.go','decision_attempt_chain_test.go','decision_attempt_transaction_test.go']
  names=[]
  for f in files:names+=re.findall(r'^func (Test\w+)\(', (module/'internal/store'/f).read_text(),re.M)
  args+=['-run','^('+'|'.join(names)+')$']
elif mode.startswith('native'):
 args+=['./cmd/orchestrator','-run','^(TestCLIVersionedDAGRework|TestCLIReworkCandidateDevelopmentDAG)$','-timeout=180s']
elif mode.startswith('git'):
 args+=['./internal/gitops','-run','^(TestIntegrationUsesCommonDirectoryOSLock|TestIntegrateTargetDriftIsUnknownAndDoesNotRetry|TestManagedIntegrationKeepsUncertainAfterTargetWasChanged|TestIntegrateRequiresReviewBindingAndUsesProtectedFastForward)$','-timeout=120s']
else:raise SystemExit('unknown mode')
env=os.environ.copy();env['GOMAXPROCS']='2';env['PATH']=str(pathlib.Path(go).parent)+':'+env.get('PATH','')
source_commit=subprocess.check_output(['git','rev-parse','HEAD'],cwd=root,text=True).strip()
source_dirty=subprocess.check_output(['git','status','--porcelain','--','tools/orchestrator'],cwd=root,text=True).strip()
start=time.time();raw=out/(mode+'.jsonl')
with raw.open('wb') as log:r=subprocess.run(args,cwd=module,env=env,stdout=log,stderr=subprocess.STDOUT,timeout=800)
counts=collections.Counter();top=collections.Counter();packages=[]
for line in raw.read_text().splitlines():
 try:d=json.loads(line)
 except ValueError:continue
 if d.get('Action') in ('pass','fail','skip'):
  if d.get('Test'):
   counts[d['Action']]+=1
   if '/' not in d['Test']:top[d['Action']]+=1
  else:packages.append({k:d[k] for k in ('Package','Action','Elapsed') if k in d})
receipt=dict(source_commit=source_commit,source_dirty=source_dirty,command=args,cwd=str(module),exit=r.returncode,seconds=time.time()-start,sha256=hashlib.sha256(raw.read_bytes()).hexdigest(),named=dict(counts),top=dict(top),packages=packages)
(out/(mode+'-manifest.json')).write_text(json.dumps(receipt,indent=2)+'\n');print(json.dumps(receipt));raise SystemExit(r.returncode)
