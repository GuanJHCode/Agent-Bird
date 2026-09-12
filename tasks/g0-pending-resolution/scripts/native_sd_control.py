"""One fixed sd-qual-02 qualification controller; not a product launcher."""
import datetime as dt
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import sqlite3
import subprocess
import sys
import time
import native_sd_collect as c
from native_sd_collect import require_live_processes, same_process
from native_sd_pty import NativePTY, InputSequence, stamp
from native_sd_clock import CLOCK_IMPL, continuous_ns, clock_pair

CASE = c.BASE/'tasks/g0-pending-resolution/data/sd-qual-02'
JOB = c.REPO/'tasks/pending-resolution/data/sd-qual-02'
HELPER = c.BASE/'tasks/g0-pending-resolution/scripts/native_standalone.py'
AUDIT = HELPER.with_name('delivery_audit.py')
DOCS = Path(__file__).resolve().parents[1]/'docs'


def failure_fields(error,substage):
    stages={'start_service','service_wait_socket','start_tui','tui_wait_native','status_send','status_wait',
            'origin_capture','initial_paste','initial_submit','initial_hook_scan','ready_lookup','pending_before',
            'ready_reread','pending_after','await_job_result','job_read','identity_before_prepare','controller_evidence',
            'binding','prepare','send-once','helper_finished'}
    if isinstance(error,c.EpochWindowError): code='epoch_correlation_inconclusive'
    elif isinstance(error,TimeoutError): code='continuous_deadline_exceeded'
    elif isinstance(error,sqlite3.Error): code='sqlite_read_failed'
    elif isinstance(error,subprocess.SubprocessError): code='local_child_failed'
    elif isinstance(error,OSError): code='local_os_error'
    else: code='evidence_validation_rejected'
    frames=[]; cursor=error.__traceback__
    allowed={'native_sd_control.py','native_sd_collect.py','native_sd_pty.py','native_sd_clock.py'}
    while cursor:
        name=Path(cursor.tb_frame.f_code.co_filename).name
        if name in allowed:
            frames.append({'file':name,'line':cursor.tb_lineno,'function':cursor.tb_frame.f_code.co_name})
        cursor=cursor.tb_next
    return {'code':code,'substage':substage if substage in stages else 'unclassified',
            'at':stamp(),'clock':clock_pair(),'source_frames':frames}


def read_send_attempt(root,boot_id,binding_sha,begin_ns):
    path=CASE/'send-attempt.json'
    if (not c.valid_id(root) or not c.valid_id(boot_id) or type(binding_sha) is not str
            or not re.fullmatch(r'[0-9a-f]{64}',binding_sha) or not c.integer(begin_ns,0,2**63-1)
            or not path.is_file() or path.is_symlink() or path.stat().st_size>4096 or path.stat().st_mode&0o777!=0o600):
        raise ValueError('missing or invalid same-case send attempt')
    raw=path.read_bytes(); attempt=json.loads(raw)
    expected={'version','status','nonce','controller_thread_id','binding_sha256','payload_sha256',
              'boot_id','clock_impl','mono_ns','wall_ns'}
    if (type(attempt) is not dict or set(attempt)!=expected or type(attempt['version']) is not int or attempt['version']!=1
            or attempt['status']!='send_intent' or attempt['nonce']!=c.NONCE or attempt['controller_thread_id']!=root
            or attempt['binding_sha256']!=binding_sha or type(attempt['payload_sha256']) is not str
            or not re.fullmatch(r'[0-9a-f]{64}',attempt['payload_sha256']) or attempt['boot_id']!=boot_id
            or attempt['clock_impl']!=CLOCK_IMPL
            or type(attempt['mono_ns']) is not int or not begin_ns<=attempt['mono_ns']<=min(continuous_ns(),begin_ns+10000000000)
            or type(attempt['wall_ns']) is not int or attempt['wall_ns']<0):
        raise ValueError('invalid original helper attempt clock/binding')
    deadline=attempt['mono_ns']+120000000000
    if continuous_ns()>deadline: raise TimeoutError('original attempt model window ended')
    return {'attempt_mono_ns':attempt['mono_ns'],'attempt_wall_ns':attempt['wall_ns'],
            'deadline_mono_ns':deadline,'attempt_sha256':hashlib.sha256(raw).hexdigest()}


def require_helper_success(mode,exit_code,root,boot_id,binding_sha,begin_ns):
    if exit_code!=0: raise ValueError('helper qualification rejected or uncertain')
    if mode=='send-once': return read_send_attempt(root,boot_id,binding_sha,begin_ns)
    return None


class OwnedService:
    """Ordinary native listener, with no terminal whose close could stop other clients."""
    def __init__(self):
        self.started_at=stamp()
        self.child=subprocess.Popen(['codex','app-server','--listen','unix://'],cwd=c.REPO,
                                    stdin=subprocess.DEVNULL,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,
                                    start_new_session=True)
        self.pid=self.child.pid
    def _poll(self): return self.child.poll()
    def read(self,seconds): time.sleep(seconds)
    def wait(self,seconds):
        try: return self.child.wait(timeout=seconds)
        except subprocess.TimeoutExpired: return None
    def evidence(self):
        return {'pid':self.pid,'started_at':self.started_at,'exit_code':self._poll(),'stdio':'devnull'}
    def close(self):
        if self._poll() is None: raise ValueError('refuse implicit live service shutdown')


def lab_read_fields(raw):
    if type(raw) is not str or len(raw)>2048: raise ValueError('invalid lab read size')
    value=json.loads(raw)
    if (type(value) is not dict or set(value)!={'version','status','nonce','count'}
            or type(value['version']) is not int or value['version']!=1 or value['nonce']!=c.NONCE
            or type(value['count']) is not int or value['status'] not in ('pending','completed')
            or value['count']!={'pending':0,'completed':1}[value['status']]):
        raise ValueError('invalid fixed lab read envelope')
    return value


def native_ready(root,turn,since,through,now=None):
    if not c.valid_id(root) or not c.valid_id(turn): raise ValueError('exact root and turn required')
    lo,hi=c.window(since,through,now or dt.datetime.now(dt.timezone.utc))
    db=sqlite3.connect('file:'+str(c.CODEX_HOME/'state_5.sqlite')+'?mode=ro',uri=True)
    try: row=db.execute('SELECT id,source,cwd,cli_version,rollout_path FROM threads WHERE id=?',(root,)).fetchone()
    finally: db.close()
    if row is None: return None  # First-turn registration can still be pending.
    c.thread_fields(row[:4],root)
    path=Path(row[4])
    if path.is_symlink() or (c.CODEX_HOME/'sessions').resolve() not in path.resolve().parents:
        raise ValueError('unexpected native rollout path')
    if not path.exists(): return None
    if not path.is_file() or path.stat().st_size>16*1024*1024:
        raise ValueError('unsupported bounded native rollout')
    found=[]
    with path.open() as stream:
        for line in stream:
            # Decode only the needed event class; no response/reasoning body is retained.
            if '"event_msg"' not in line: continue
            try: event=json.loads(line)
            except json.JSONDecodeError: continue  # A concurrent last append may be incomplete.
            if event.get('type')!='event_msg': continue
            payload=event.get('payload',{})
            if (type(payload) is not dict or payload.get('type')!='task_complete'
                    or payload.get('turn_id')!=turn or payload.get('last_agent_message')!='SD_QUAL_02_READY'):
                continue
            if lo<=c.epoch_ns(event.get('timestamp'))<=hi:
                found.append({'at':event['timestamp'],'turn_id':turn,'type':'task_complete',
                              'last_agent_message':'SD_QUAL_02_READY'})
    if len(found)>1: raise ValueError('ambiguous native completion event')
    return found[0] if found else None


def validate_process_identity(value):
    keys={'pid','ppid','uid','comm','native_executable_matches','started_at_local'}
    if type(value) is not dict or set(value)!=keys:
        raise ValueError('complete native process schema required')
    if not c.integer(value['pid']) or not c.integer(value['ppid'],0) or type(value['uid']) is not int or value['uid']!=os.getuid():
        raise ValueError('invalid native process numbers')
    if value['comm']!='codex' or value['native_executable_matches'] is not True:
        raise ValueError('unexpected native executable identity')
    birth=value['started_at_local']
    if type(birth) is not str or not re.fullmatch(r'[A-Z][a-z]{2} [A-Z][a-z]{2} [0-9]{1,2} [0-9]{2}:[0-9]{2}:[0-9]{2} [0-9]{4}',birth):
        raise ValueError('invalid native birth timestamp')
    dt.datetime.strptime(birth,'%a %b %d %H:%M:%S %Y')


def validate_socket_identity(value):
    keys={'path','exists','dev','ino','uid','mode','service_holds_path'}
    if type(value) is not dict or set(value)!=keys:
        raise ValueError('complete native socket schema required')
    if (value['exists'] is not True or value['service_holds_path'] is not True or value['path']!=str(c.SOCKET)
            or value['mode']!='0600' or type(value['uid']) is not int or value['uid']!=os.getuid()
            or not c.integer(value['dev'],1,2**63-1) or not c.integer(value['ino'],1,2**63-1)):
        raise ValueError('invalid native socket identity')

def require_owned_pair(service,tui,socket,original):
    if type(original) is not dict or set(original)!={'service','tui','socket'}:
        raise ValueError('complete original ownership binding required')
    for value in (service,tui,original['service'],original['tui']): validate_process_identity(value)
    for value in (socket,original['socket']): validate_socket_identity(value)
    require_live_processes(service,tui)
    if not same_process(service,original['service']) or not same_process(tui,original['tui']):
        raise ValueError('native creation identity changed')
    if not socket.get('exists') or socket.get('service_holds_path') is not True:
        raise ValueError('owned native socket is not live')
    if any(socket.get(k)!=original['socket'].get(k) for k in ('dev','ino','uid','mode')):
        raise ValueError('native socket identity changed')
    return True


def private_json(path,value):
    data=(json.dumps(value,ensure_ascii=False,indent=2)+'\n').encode()
    fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
    with os.fdopen(fd,'wb') as stream:
        stream.write(data); stream.flush(); os.fsync(stream.fileno())


def read_job():
    began=stamp(); start=continuous_ns()
    proc=subprocess.run([str(c.GO),'read','--dir',str(JOB),'--nonce',c.NONCE],
                        capture_output=True,text=True,timeout=2)
    if proc.returncode!=0: raise ValueError('fixed lab read failed')
    elapsed=continuous_ns()-start
    if elapsed>10000000000: raise TimeoutError('local lab read exceeded continuous ten seconds')
    return {'before':began,'after':stamp(),'elapsed_seconds':elapsed/1000000000,
            'binary_sha256':c.GO_SHA,'output':lab_read_fields(proc.stdout),
            'stdout_sha256':hashlib.sha256(proc.stdout.encode()).hexdigest(),'exit_code':0}


def fixed_prompt():
    start=f'{c.GO} start --dir {JOB} --nonce {c.NONCE} --delay 40s --controller-thread "$CODEX_THREAD_ID"'
    inspect=f'{c.GO} inspect --dir {JOB} --nonce {c.NONCE}'
    ack=f'{c.GO} ack --dir {JOB} --nonce {c.NONCE} --controller-thread "$CODEX_THREAD_ID" --revision 1 --event-hash ACTUAL_EVENT_HASH --command-id sd-qual-02-ack-1 --decision handled'
    return ('这是用户已授权的固定原生资格实验，仅有一个合成任务 sd-qual-02，目录已预建且工具已审查。'
            '现在用原生 exec_command 实际执行且仅执行一次以下命令：`'+start+'`。'
            '该命令快速返回 started 后，你必须立刻结束当前响应，final 只写 SD_QUAL_02_READY。'
            '当前轮不要等待结果、不要读/轮询作业、不要调用 sleep/wait、不要创建 Goal 或子Agent，不要改文件/配置/权限，也不要使用MCP。'
            '之后只有当本线程收到代表 sd-qual-02 完成的原生 standalone toolOutput 时才执行下一步：'
            '用真实工具执行 `'+inspect+'`，核对 nonce、controller_thread、revision=1、completed result和未取消，取得实际 event_hash；'
            '把 ACTUAL_EVENT_HASH 替换为该真实返回字段，再执行 `'+ack+'`。'
            '必须使用真实原生 CODEX_THREAD_ID，不得伪造身份或再次start。'
            '看到真实 acknowledged 且 effect_count=1 后，final 只写 SD_QUAL_02_ACK。'
            '现在只做首次start与READY final，后续外部工具结果由合法原生通道送达，无需你主动继续。')


def binding_process(identity):
    validate_process_identity(identity)
    proc=subprocess.run(['ps','-p',str(identity['pid']),'-o','comm='],text=True,capture_output=True,timeout=2)
    comm=proc.stdout.strip()
    if proc.returncode or comm not in ('codex',str(c.CLI),str(c.CLI.resolve())):
        raise ValueError('unrecognized fixed native comm')
    return {'pid':identity['pid'],'uid':identity['uid'],'birth':identity['started_at_local'],
            'comm':comm,'native_binary_sha256':c.CLI_SHA}


def matching_prompt_hook(root,service,since,through):
    lo,hi=c.window(since,through,dt.datetime.now(dt.timezone.utc)); found=[]
    for path in c.HOOKS.glob('*.json'):
        if path.is_symlink() or not path.is_file() or path.stat().st_size>8192: continue
        if not lo-2000000000<=path.stat().st_mtime_ns<=hi+2000000000: continue
        try: item=c.hook_fields(json.loads(path.read_text()),root,service,lo,hi)
        except (ValueError,OSError): continue
        if item and item['hook_event_name']=='UserPromptSubmit': found.append(item)
    if len(found)>1: raise ValueError('more than one root user prompt in initial window')
    return found[0] if found else None


def run(helper_sha,audit_sha):
    # Hashes are explicit values from the independent review; paths are fixed here.
    for path,expected in ((HELPER,helper_sha),(AUDIT,audit_sha),(c.CLI.resolve(),c.CLI_SHA),(c.GO,c.GO_SHA)):
        if not re.fullmatch(r'[0-9a-f]{64}',expected) or path.is_symlink() or not path.is_file() or c.sha(path)!=expected:
            raise ValueError('reviewed executable/source SHA mismatch')
    if c.snapshot_socket()['exists']: raise ValueError('default socket occupied')
    for path in (CASE,JOB):
        if path.exists() or path.is_symlink(): raise ValueError('fixed case directory already occupied')
        path.parent.mkdir(parents=True,exist_ok=True)
        path.mkdir(mode=0o700)
        if path.stat().st_mode&0o777!=0o700: raise ValueError('private case mode required')
    protected_before=c.protected_files()
    boot=subprocess.run(['sysctl','-n','kern.bootsessionuuid'],text=True,capture_output=True,timeout=2)
    boot_id=boot.stdout.strip().lower()
    if boot.returncode or not c.valid_id(boot_id): raise ValueError('boot identity unavailable')
    record={'version':1,'nonce':c.NONCE,'started_at':stamp(),'stage':'start_service','substage':'start_service','started_clock':clock_pair(),
            'helper_sha256':helper_sha,'audit_sha256':audit_sha,'protected_before':protected_before,
            'service_argv':['codex','app-server','--listen','unix://'],'tui_argv':['codex'],
            'scope':'one fixed owned PTY; not generic current-UI attachment or full G0'}
    service=tui=None; original=None; sequence=InputSequence(fixed_prompt()); helper_proc=None
    helper_ok=False; job_completed=False

    def note(stage):
        record['stage']=stage
        record['substage']=stage
        print(json.dumps({'nonce':c.NONCE,'stage':stage,'at':stamp(),
                          'clock':clock_pair(),
                          'service_pid':service.pid if service else None,'tui_pid':tui.pid if tui else None}),flush=True)

    def pump(seconds=.1):
        if service: service.read(seconds)
        if tui: tui.read(seconds)

    def pair():
        result={'service':c.process(service.pid),'tui':c.process(tui.pid),'socket':c.snapshot_socket(service.pid)}
        require_owned_pair(result['service'],result['tui'],result['socket'],original)
        return result

    def helper(mode,timeout):
        nonlocal helper_proc
        note(mode); begin_ns=continuous_ns(); wall=stamp()
        binding_sha=c.sha(CASE/'binding.json')
        deadline_ns=begin_ns+int(timeout*1000000000); attempt_seen=False
        helper_proc=subprocess.Popen([sys.executable,'-B',str(HELPER),mode,'--case',str(CASE),'--root',record['root'],
                                      '--nonce',c.NONCE],stdout=subprocess.PIPE,stderr=subprocess.PIPE)
        if mode=='send-once': deadline_ns=begin_ns+10000000000  # The preflight has its own local bound.
        while helper_proc.poll() is None and continuous_ns()<deadline_ns:
            pump(.1)
            attempt_path=CASE/'send-attempt.json'
            if mode=='send-once' and not attempt_seen and attempt_path.exists():
                try: model_window=read_send_attempt(record['root'],boot_id,binding_sha,begin_ns)
                except json.JSONDecodeError: continue  # O_EXCL publication can still be writing; original local deadline remains.
                deadline_ns=model_window['deadline_mono_ns']; attempt_seen=True
                record['callback_window']=model_window
        if helper_proc.poll() is None:
            helper_proc.terminate(); helper_proc.wait(timeout=2)
            raise TimeoutError('helper original observation window ended')
        stdout,stderr=helper_proc.communicate(timeout=2)
        if (mode!='send-once' or attempt_seen) and continuous_ns()>deadline_ns:
            raise TimeoutError('helper returned beyond original continuous deadline')
        record.setdefault('helper_calls',[]).append({'mode':mode,'started_at':wall,'ended_at':stamp(),
            'elapsed_seconds':(continuous_ns()-begin_ns)/1000000000,'exit_code':helper_proc.returncode,
            'stdout_bytes':len(stdout),'stdout_sha256':hashlib.sha256(stdout).hexdigest(),
            'stderr_bytes':len(stderr),'stderr_sha256':hashlib.sha256(stderr).hexdigest()})
        completed_window=require_helper_success(mode,helper_proc.returncode,record['root'],boot_id,binding_sha,begin_ns)
        if mode=='send-once':
            if attempt_seen and record['callback_window']!=completed_window: raise ValueError('send attempt changed during observation')
            record['callback_window']=completed_window
            deadline_ns=completed_window['deadline_mono_ns']
        if continuous_ns()>deadline_ns:
            raise TimeoutError('helper success validation exceeded original continuous deadline')

    try:
        service=OwnedService()
        record['substage']='service_wait_socket'
        deadline=continuous_ns()+10000000000; service_identity=None
        while continuous_ns()<deadline:
            service.read(.1)
            if continuous_ns()>=deadline: break
            try:
                current=c.process(service.pid); sock=c.snapshot_socket(service.pid)
                if current and sock.get('service_holds_path') is True:
                    validate_process_identity(current); validate_socket_identity(sock)
                    service_identity=current; break
            except ValueError: pass  # Only the new child can still be between fork and exec.
        if continuous_ns()>=deadline: raise TimeoutError('native service startup exceeded ten seconds')
        if service_identity is None: raise TimeoutError('native service did not become ready in ten seconds')
        note('start_tui'); tui=NativePTY(['codex'],str(c.REPO)); deadline=continuous_ns()+10000000000
        record['substage']='tui_wait_native'
        while continuous_ns()<deadline:
            pump(.1)
            if continuous_ns()>=deadline: break
            try:
                if c.process(tui.pid) and tui.byte_count: break
            except ValueError: pass
        else: raise TimeoutError('native TUI startup exceeded ten seconds')
        if continuous_ns()>=deadline: raise TimeoutError('native TUI startup exceeded ten seconds')
        record['substage']='status_send'; tui.send(sequence,'status'); record['substage']='status_wait'
        while continuous_ns()<deadline and 'status_root' not in tui.evidence(): pump(.1)
        if continuous_ns()>=deadline: raise TimeoutError('native status exceeded ten seconds')
        status=tui.evidence()
        if 'status_root' not in status or status.get('ambiguous_status_root'): raise ValueError('unsupported initial native status shape')
        record['root']=status['status_root']; record['initial_status']=status
        record['substage']='origin_capture'
        original={'service':c.process(service.pid),'tui':c.process(tui.pid),'socket':c.snapshot_socket(service.pid)}
        require_owned_pair(original['service'],original['tui'],original['socket'],original)
        record['origin']=original
        note('initial_paste'); tui.send(sequence,'initial-paste')
        note('initial'); record['substage']='initial_submit'; since=stamp(); initial_mono=continuous_ns(); tui.send(sequence,'initial-submit')
        initial_deadline=initial_mono+120000000000
        record['initial_window']={'since':since,'clock_impl':CLOCK_IMPL,'mono_ns':initial_mono,'deadline_mono_ns':initial_deadline,
                                  'epoch_correlation_end':(dt.datetime.fromisoformat(since)+dt.timedelta(seconds=120)).isoformat()}
        private_json(CASE/'initial-submit.json',{'nonce':c.NONCE,'root':record['root'],'window':record['initial_window'],'inputs':tui.evidence()['inputs']})
        pending_barrier=None; prompt_hook=None
        while continuous_ns()<initial_deadline:
            pump(.1)
            if continuous_ns()>=initial_deadline: break
            record['substage']='initial_hook_scan'; prompt_hook=matching_prompt_hook(record['root'],service.pid,since,stamp())
            if prompt_hook is None or not (JOB/'job.json').exists(): continue
            record['substage']='ready_lookup'; ready=native_ready(record['root'],prompt_hook['turn_id'],since,stamp())
            if ready is None: continue
            record['substage']='pending_before'; before=read_job()
            observed_before=stamp()
            record['substage']='ready_reread'
            exact=native_ready(record['root'],prompt_hook['turn_id'],since,stamp())
            observed_after=stamp(); record['substage']='pending_after'; after=read_job()
            if continuous_ns()>=initial_deadline: raise TimeoutError('pending barrier exceeded original continuous window')
            if before['output']['status']!='pending' or after['output']['status']!='pending' or exact is None:
                raise ValueError('no native-final-before-job-completion barrier within fixed sample')
            pending_barrier={'go_before':before,'native_task_complete':exact,'native_observed_before':observed_before,
                             'native_observed_after':observed_after,'go_after':after}
            break
        if pending_barrier is None: raise TimeoutError('initial native READY did not qualify within original window')
        record['pending_barrier']=pending_barrier; note('await_job_result')
        while continuous_ns()<initial_deadline:
            pump(.1)
            if continuous_ns()>=initial_deadline: break
            if (JOB/'result.json').exists():
                record['substage']='job_read'; final_read=read_job()
                if continuous_ns()>=initial_deadline: break
                if final_read['output']['status']=='completed': job_completed=True; record['client_completed_read']=final_read; break
        if not job_completed: raise TimeoutError('job result missing within initial window')
        record['substage']='identity_before_prepare'; before_send=pair(); through=stamp()
        record['substage']='controller_evidence'
        local=c.local_evidence(record['root'],service.pid,*c.window(since,through,dt.datetime.now(dt.timezone.utc)))
        evidence={'version':1,'nonce':c.NONCE,'root':record['root'],'initial_window':{'since':since,'through':through},
                  'origin':original,'before_send':before_send,'pty':tui.evidence(),'pending_barrier':pending_barrier,
                  'completed_read':record['client_completed_read'],'local':local}
        private_json(CASE/'controller-evidence.json',evidence)
        record['substage']='binding'; service_binding=binding_process(before_send['service'])
        service_binding.update(socket_path=str(c.SOCKET),socket_dev=before_send['socket']['dev'],socket_ino=before_send['socket']['ino'])
        binding={'version':1,'nonce':c.NONCE,'controller_thread_id':record['root'],'controller_epoch':1,
                 'expected_cwd':str(c.REPO),'prior_turn_id':prompt_hook['turn_id'],'native_cli_version':'0.153.4',
                 'boot_id':boot_id,'service':service_binding,'tui':binding_process(before_send['tui']),
                 'go_binary':str(c.GO),'go_binary_sha256':c.GO_SHA,'job_dir':str(JOB),
                 'controller_evidence_sha256':c.sha(CASE/'controller-evidence.json')}
        private_json(CASE/'binding.json',binding)
        pair(); helper('prepare',10)
        pair(); helper('send-once',120)  # Outer deadline is replaced only by the immutable attempt's original mono_ns.
        helper_ok=True; note('helper_finished')
    except (ValueError,OSError,sqlite3.Error,subprocess.SubprocessError,TimeoutError) as exc:
        record['failure']=failure_fields(exc,record['substage'])
    finally:
        # This fixture never starts a second job or resends a delivery during cleanup.
        record['helper_completed_without_error']=helper_ok
        record['job_completed_before_cleanup']=job_completed
        if helper_proc and helper_proc.poll() is None:
            helper_proc.terminate(); helper_proc.wait(timeout=2)
        # A failed initial phase can still have the single <=40s owned job in flight.
        cleanup_start=continuous_ns()
        if not job_completed and (JOB/'job.json').exists():
            while continuous_ns()-cleanup_start<45000000000:
                try:
                    if read_job()['output']['status']=='completed': job_completed=True; break
                except (ValueError,subprocess.SubprocessError): pass
                pump(.1)
        record['job_completed_at_cleanup']=job_completed
        # The helper does not expose its server connection IDs. Client names or row counts
        # cannot authorize killing the shared listener; this controller never stops it.
        record['cleanup_connection_observation']={'automatic_service_stop':False,
            'reason':'helper_server_connection_ids_not_registered','listener_retained_for_main_agent':True}
        if service and record.get('root') and record.get('initial_window'):
            try:
                now=dt.datetime.now(dt.timezone.utc)
                start=max(dt.datetime.fromisoformat(record['initial_window']['since']),now-dt.timedelta(seconds=120))
                recent=c.local_evidence(record['root'],service.pid,*c.window(start.isoformat(),now.isoformat(),now))
                rows=recent['native_protocol']
                if 'evidence' in locals(): rows=evidence['local']['native_protocol']+rows
                clients=[row['fields'] for row in rows]
                tui_connections={v['connection_id'] for v in clients if v.get('client_name')=='codex-tui' and 'connection_id' in v}
                unknown=any(v.get('unrecognized_client_name') is True for v in clients)
                record['cleanup_connection_observation']={'known_tui_connection_count':len(tui_connections),
                    'unrecognized_client_observed':unknown,'automatic_service_stop':False,
                    'reason':'helper_server_connection_ids_not_registered','listener_retained_for_main_agent':True,
                    'scope':'request-span metadata does not establish complete client ownership'}
            except (ValueError,OSError,sqlite3.Error):
                record['cleanup_connection_observation'].update(reason='connection_evidence_unavailable')
        if tui and tui._poll() is None:
            current=c.process(tui.pid)
            if current and (original is None or c.same_process(current,original['tui'])):
                tui.send(sequence,'quit'); tui.wait(10)
        record['service_process']=service.evidence() if service else None
        record['retained_service_pid']=service.pid if service and service._poll() is None else None
        record['tui_pty']=tui.evidence() if tui else None
        record['protected_after']=c.protected_files()
        record['protected_unchanged']=record['protected_after']==protected_before
        record['socket_after']=c.snapshot_socket()
        record['ended_at']=stamp()
        record['ended_clock']=clock_pair()
        record['all_owned_native_exited']=all(p is None or p._poll() is not None for p in (service,tui))
        private_json(CASE/'controller-result.json',record)
        private_json(DOCS/'native-sd-qual-02-controller.json',record)
        for p in (service,tui):
            if p and p._poll() is not None: p.close()
        print(json.dumps({'nonce':c.NONCE,'stage':'finished','helper_ok':helper_ok,
                          'all_owned_native_exited':record['all_owned_native_exited'],
                          'retained_service_pid':record['retained_service_pid'],
                          'evidence':str(CASE/'controller-result.json')}),flush=True)
    return 0 if helper_ok and (tui is None or tui._poll() is not None) else 2


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--helper-sha',required=True); p.add_argument('--audit-sha',required=True)
    args=p.parse_args()
    return run(args.helper_sha,args.audit_sha)


if __name__=='__main__': sys.exit(main())
