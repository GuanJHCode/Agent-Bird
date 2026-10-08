#!/usr/bin/env python3
import argparse,importlib.util,os,pathlib,subprocess,sys,json
def main():
 p=argparse.ArgumentParser();[p.add_argument('--'+x,required=True) for x in ('candidate','state','work','ready','helper','run-id','task-id')];a=p.parse_args()
 s=importlib.util.spec_from_file_location('soak',a.helper);m=importlib.util.module_from_spec(s);s.loader.exec_module(m)
 env={'PATH':'/usr/bin:/bin:/usr/sbin:/sbin','LANG':'C','LC_ALL':'C','TMPDIR':a.work,'ORCHESTRATOR_ENABLE_TEST_FAKE':'1'}
 def call(*x):
  q=subprocess.run([a.candidate,*x],env=env,capture_output=True,timeout=20);m.atomic(pathlib.Path(a.work)/('raw-'+x[0]+'.json'),{'argv':x,'original_wait':q.returncode,'stdout':q.stdout.decode(errors='replace'),'stderr':q.stderr.decode(errors='replace')})
  if q.returncode: raise RuntimeError(x[0])
  return json.loads(q.stdout)
 b=subprocess.run(['/bin/ps','-o','lstart=','-p',str(os.getpid())],capture_output=True,text=True,check=True).stdout.strip();o=call('owner-bind','--state-dir',a.state,'--request',str(pathlib.Path(a.work)/'owner.json')) if not m.atomic(pathlib.Path(a.work)/'owner.json',{'controller_thread':'host-capacity','origin_pid':os.getpid(),'origin_birth':b}) else None
 r={k:v for k,v in o.items() if k not in ('version','owner_capability')};r.update(run_id=a.run_id,plan_revision=1,owner_mode='native',delivery_mode='collect',tasks=[{'id':a.task_id,'max_attempts':1,'max_active_ms':30000,'completion_policy':'owner_review','adapter':{'kind':'fake','args':['/bin/sh','-c','while [ ! -f "$1" ]; do sleep .02; done; echo done','w',str(pathlib.Path(a.work)/'release')],'directory':a.work}}]);m.atomic(pathlib.Path(a.work)/'submit.json',r);z=call('submit','--state-dir',a.state,'--request',str(pathlib.Path(a.work)/'submit.json'));m.atomic(pathlib.Path(a.ready),{'run_id':a.run_id,'task_id':a.task_id,'control_file':z['control_file'],'origin_pid':os.getpid(),'origin_birth':b});sys.stdin.buffer.read(1)
if __name__=='__main__':main()
