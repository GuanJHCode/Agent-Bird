#!/usr/bin/env python3
"""F12 idle30 acceptance; controlled fake worker only."""
import argparse, hashlib, importlib.util, json, os, pathlib, sqlite3, stat, subprocess, sys, time, uuid
ROOT=pathlib.Path(__file__).resolve().parents[1]
SOURCE=pathlib.Path('/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/capability-closure-20261006/tmp/integration/tasks/resource-soak/tmp/native-build/candidate')
CTRL=ROOT/'scripts/controller.py'; SOAK=ROOT.parent/'resource-soak/scripts/resource_soak.py'; SOAKT=ROOT.parent/'resource-soak/data/thresholds.json'
TH=json.loads((ROOT/'data/thresholds.json').read_text())
class Fail(RuntimeError): pass
def digest(p):
    with p.open('rb') as f:return hashlib.file_digest(f,'sha256').hexdigest()
def cp(src,dst,want,mode=0o500):
    fd=os.open(src,os.O_RDONLY|os.O_NOFOLLOW)
    try:
        before=os.fstat(fd); h=hashlib.sha256()
        with os.fdopen(os.open(dst,os.O_WRONLY|os.O_CREAT|os.O_EXCL,mode),'wb') as out:
            while b:=os.read(fd,1048576): h.update(b);out.write(b)
            out.flush();os.fsync(out.fileno())
        after=os.fstat(fd)
        if (before.st_dev,before.st_ino,before.st_size,before.st_mtime_ns)!=(after.st_dev,after.st_ino,after.st_size,after.st_mtime_ns) or h.hexdigest()!=want: raise Fail('source drift')
    finally: os.close(fd)
    d=os.open(dst.parent,os.O_RDONLY);os.fsync(d);os.close(d)
def output(p): return os.fdopen(os.open(p,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600),'wb')
def ps():
    p=subprocess.run(['/bin/ps','-axo','pid=,ppid=,rss=,time=,stat=,command='],capture_output=True,text=True,check=True,timeout=2);r={}
    for x in p.stdout.splitlines():
        f=x.strip().split(None,5)
        if len(f)==6 and all(y.isdigit() for y in f[:3]):r[int(f[0])]={'pid':int(f[0]),'argv':f[5],'stat':f[4]}
    return r
def birth(pid):
    p=subprocess.run(['/bin/ps','-o','lstart=','-p',str(pid)],capture_output=True,text=True,timeout=2)
    if p.returncode==1 and not p.stdout.strip():return None
    if p.returncode or not p.stdout.strip():raise Fail('birth UNKNOWN')
    return p.stdout.strip()
def live(i,rows):
    return birth(i['pid'])==i['birth'] and i['pid'] in rows and rows[i['pid']]['argv']==i['argv']
def remember(c,pid,role):
    rows=ps(); b=birth(pid)
    if not b or pid not in rows or rows[pid]['stat'].startswith('Z'):raise Fail(role+' not live')
    c['ids'][role]={'pid':pid,'birth':b,'argv':rows[pid]['argv']}
def idle(db):
    return db.execute("""SELECT (SELECT COUNT(*) FROM segment_runtime WHERE status IN ('launch_requested','prepared','spawned','running','stopping','unknown'))+(SELECT COUNT(*) FROM tasks WHERE status IN ('queued','ready','resume_queued','running','stopping','unknown','waiting_question','interrupted','result_ready'))+(SELECT COUNT(*) FROM runtime_events WHERE delivery_status='pending')+(SELECT COUNT(*) FROM report_events WHERE delivery_status IN ('pending','pending_session'))+(SELECT COUNT(*) FROM stop_runtime WHERE status IN ('pending','accepted'))+(SELECT COUNT(*) FROM runtime_hosts WHERE status='ready')+(SELECT COUNT(*) FROM decision_attempt_bindings WHERE attempt_id IS NULL AND retired=0)""").fetchone()[0]
def call(c,*a):
    n=c['n'];c['n']+=1;p=subprocess.Popen([str(c['bin']),*a],env=c['env'],stdin=subprocess.DEVNULL,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
    try:o,e=p.communicate(timeout=20)
    except subprocess.TimeoutExpired:p.kill();o,e=p.communicate();raise Fail('control timeout')
    c['soak'].atomic(c['raw']/f'{n:04d}-{a[0]}.stdout',o);c['soak'].atomic(c['raw']/f'{n:04d}-{a[0]}.stderr',e)
    c['soak'].atomic(c['raw']/f'{n:04d}-{a[0]}.wait.json',{'argv':list(a),'original_wait':p.returncode})
    if p.returncode:raise Fail('control failed '+a[0])
    return json.loads(o)
def main():
    ap=argparse.ArgumentParser();ap.add_argument('--freeze-manifest',type=pathlib.Path,required=True);ap.add_argument('--out',type=pathlib.Path,required=True);a=ap.parse_args()
    if sys.platform!='darwin' or a.out.exists() or not a.out.is_absolute():raise SystemExit('Darwin/new absolute output required')
    f=json.loads(a.freeze_manifest.read_text());src={'candidate_sha256':SOURCE,'runner_sha256':pathlib.Path(__file__),'controller_sha256':CTRL,'resource_soak_sha256':SOAK,'resource_soak_thresholds_sha256':SOAKT}
    if f.get('candidate_path')!=str(SOURCE) or f.get('thresholds')!=TH or any(f.get(k)!=digest(v) for k,v in src.items()):raise SystemExit('freeze mismatch')
    a.out.mkdir(mode=0o700);(a.out/'bin').mkdir(mode=0o700);(a.out/'lib/scripts').mkdir(parents=True,mode=0o700);(a.out/'lib/data').mkdir(mode=0o700);raw=a.out/'raw';raw.mkdir(mode=0o700)
    dst={'candidate_sha256':a.out/'bin/candidate','controller_sha256':a.out/'bin/controller.py','resource_soak_sha256':a.out/'lib/scripts/resource_soak.py','resource_soak_thresholds_sha256':a.out/'lib/data/thresholds.json'}
    for k in src:cp(src[k],dst[k],f[k])
    sp=importlib.util.spec_from_file_location('soak',dst['resource_soak_sha256']);soak=importlib.util.module_from_spec(sp);sp.loader.exec_module(soak)
    pins={k:soak.pin(v) for k,v in dst.items()};work=pathlib.Path('/private/tmp')/('resource-idle-'+uuid.uuid4().hex[:12]);work.mkdir(mode=0o700);state=work/'state';ready=work/'ready.json'
    env={'PATH':'/usr/bin:/bin:/usr/sbin:/sbin','LANG':'C','LC_ALL':'C','TMPDIR':str(work),'ORCHESTRATOR_ENABLE_TEST_FAKE':'1'};c={'bin':dst['candidate_sha256'],'raw':raw,'env':env,'n':0,'soak':soak,'ids':{}};s={'result':'failed','frozen':f,'pins_start':pins,'state_preserved':str(state)}
    db=None
    try:
      c['server']=subprocess.Popen([str(c['bin']),'serve','--state-dir',str(state)],env=env,stdin=subprocess.DEVNULL,stdout=output(raw/'server.stdout'),stderr=output(raw/'server.stderr'));remember(c,c['server'].pid,'coordinator')
      end=time.monotonic()+15
      while not (state/'coordinator.sock').exists():
       if c['server'].poll()!=None or time.monotonic()>end: raise Fail('server startup')
       time.sleep(.05)
      c['controller']=subprocess.Popen([sys.executable,str(dst['controller_sha256']),'--candidate',str(c['bin']),'--state',str(state),'--work',str(work),'--ready',str(ready),'--helper',str(dst['resource_soak_sha256']),'--raw',str(raw/'controller'),'--run-id','idle-'+uuid.uuid4().hex],env=env,stdin=subprocess.PIPE,stdout=output(raw/'origin.stdout'),stderr=output(raw/'origin.stderr'));remember(c,c['controller'].pid,'controller')
      end=time.monotonic()+20
      while not ready.exists():
       if c['controller'].poll()!=None or time.monotonic()>end: raise Fail('controller')
       time.sleep(.05)
      r=json.loads(ready.read_text());db=sqlite3.connect((state/'state.db').as_uri()+'?mode=ro',uri=True);db.row_factory=sqlite3.Row;db.execute('PRAGMA query_only=ON')
      # Enrollment, gated release, durable Exited0, collect and accept are deliberately explicit.
      row=None;end=time.monotonic()+20
      while time.monotonic()<end:
       row=db.execute("SELECT t.status,a.id,s.segment_id,s.host_id,tr.host_launch_id FROM tasks t JOIN task_runtime tr ON tr.task_id=t.id JOIN attempts a ON a.task_id=t.id JOIN segment_runtime s ON s.attempt_id=a.id WHERE t.run_id=? AND t.id=?",(r['run_id'],r['task_id'])).fetchone()
       if row and row['status']=='running':break
       time.sleep(.05)
      if not row:raise Fail('running receipt')
      ev=db.execute("SELECT body_json FROM runtime_events WHERE task_id=? AND attempt_id=? AND segment_id=? AND producer_id=? AND json_extract(body_json,'$.kind')='running'",(r['task_id'],row['id'],row['segment_id'],row['host_id'])).fetchone();w=json.loads(ev[0])['process'];remember(c,w['pid'],'worker')
      own=db.execute("SELECT owner_pid,owner_birth,current_host_pid,current_host_birth FROM execution_owners WHERE run_id=? AND launch_id=? AND is_current=1 AND phase='bound'",(r['run_id'],row['host_launch_id'])).fetchone()
      if not own:raise Fail('bound owner');remember(c,own['owner_pid'],'owner');remember(c,own['current_host_pid'],'host');soak.atomic(a.out/'identities.json',c['ids']);soak.atomic(work/'worker-release',b'release\\n')
      end=time.monotonic()+20
      while time.monotonic()<end:
       q=db.execute("SELECT t.status,e.body_json FROM tasks t JOIN attempts a ON a.task_id=t.id JOIN segment_runtime x ON x.attempt_id=a.id JOIN runtime_events e ON e.task_id=t.id AND e.attempt_id=a.id AND e.segment_id=x.segment_id WHERE t.id=? AND json_extract(e.body_json,'$.kind')='exited'",(r['task_id'],)).fetchone()
       if q and q['status']=='result_ready' and json.loads(q['body_json']).get('exit_code')==0:break
       time.sleep(.05)
      if not q:raise Fail('durable Exited0')
      got=call(c,'collect','--state-dir',str(state),'--task-id',r['task_id'],'--control-file',r['control_file']);e=[x for x in got['events'] if x['kind']=='result']
      if len(e)!=1: raise Fail('result')
      e=e[0]
      call(c,'accept','--state-dir',str(state),'--task-id',r['task_id'],'--control-file',r['control_file'],'--work-revision','1','--event-id',e['event_id'],'--event-revision',str(e['event_revision']),'--event-hash',e['payload_hash'],'--action-slot',e['action_slot'],'--decision','accept','--command-id','resource-idle-accept');at=time.monotonic()
      c['controller'].stdin.close()
      if c['controller'].wait(timeout=13)!=0:raise Fail('controller wait')
      samples=[];first=None;last=at
      with output(a.out/'idle-samples.jsonl') as out:
       while time.monotonic()<=at+32:
        now=time.monotonic();n=idle(db);alive=live(c['ids']['coordinator'],ps());z={'since_accept_seconds':now-at,'nonidle_count':n,'coordinator_alive':alive};samples.append(z);out.write((json.dumps(z)+'\\n').encode())
        if n:last=now
        elif first is None:first=now
        if not alive:
         if first is None or not 29<=now-first<=32:raise Fail('idle exit timing')
         break
        time.sleep(.05)
       else:raise Fail('no natural idle exit')
      if c['server'].wait(timeout=1)!=0:raise Fail('coordinator wait')
      s.update(result='passed',idle_window={'first_idle_monotonic':first,'last_busy_monotonic':last,'idle_to_exit_seconds':time.monotonic()-first,'samples':len(samples)})
    except BaseException as e:s['error']=repr(e)
    finally:
      if db:db.close()
      signals=[]
      for name in ('controller','server'):
       p=c.get(name)
       if p and p.poll() is None:p.terminate();signals.append(name);p.wait(timeout=15)
      s['cleanup']={'identity_signals':signals,'controller_original_wait':c.get('controller').returncode if c.get('controller') else None,'coordinator_original_wait':c.get('server').returncode if c.get('server') else None}
      s['pins_end']={k:soak.pin(v) for k,v in dst.items()};s['source_hashes_end']={k:digest(v) for k,v in src.items()}
      if signals or s['pins_end']!=pins or any(s['source_hashes_end'][k]!=f[k] for k in src):s['result']='failed'
      soak.atomic(a.out/'summary.json',s)
    print(json.dumps({'result':s['result'],'output':str(a.out)}));return 0 if s['result']=='passed' else 1
if __name__=='__main__':raise SystemExit(main())
