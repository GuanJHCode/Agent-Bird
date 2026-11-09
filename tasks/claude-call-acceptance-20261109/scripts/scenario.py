#!/usr/bin/env python3
"""T12 scenario runner: preparation is free; real launch needs --execute-live."""
import argparse
import ctypes
import json
import os
from pathlib import Path
import signal
import sqlite3
import subprocess
import sys
import threading
import time

import t12_harness as h
import r5_policy as policy

HERE = Path(__file__).resolve().parent
CODEX_DISABLE_FEATURES={'shell_tool','unified_exec','multi_agent','multi_agent_v2','plugins','hooks','apps'}
AGENT_BIRD_TOOL_NAMES=(
    'dispatch','inspect','wait','collect','request_changes','accept','ack','stop','fixture_validate_candidate',
)
CLAUDE_ALLOWED_TOOLS=tuple('mcp__agent_bird__'+name for name in AGENT_BIRD_TOOL_NAMES)
CODEX_MCP_DEFAULT_APPROVAL='approve'


def parent_prompt(contract):
    return '''You are the REAL PARENT reviewing one bounded Agent Bird delegation. The worker's “don't delegate” rule applies to the worker; YOU must dispatch using the configured original Agent Bird MCP tools.
Read this complete frozen original contract. Call dispatch with exactly contract.dispatch, preserving request_id, provider/model/reasoning, budget group/limits, brief, paths and ALL acceptance. Only identical request replay is allowed; never create another request ID/group/task/model or use another delegation route. No deliberate defects. The original repo HEAD/files must remain unchanged until root's separate FF finish.
Use inspect/wait/collect from the original product tools. For a candidate, call harness-only fixture_validate_candidate with original cli_handle returned by dispatch and the candidate_oid you observed in inspect. Read its full frozen contract, actual diff, review tuple, OID/tree and test stdout/stderr/exit. You, the parent, decide based on those facts. Never treat a worker's claimed tests or the helper's existence as success. If all original conditions pass, explicitly call the real accept tool using the original handle and current review token. Separately ACK actionable collection using the genuine delivery_id, collection_proof_sha256 and exact decisions. ACK is transport bookkeeping, never acceptance.
If original acceptance fails, give specific factual feedback through request_changes using the genuine review token, retaining every original acceptance requirement. At most one necessary rework, still in the original total budget; review revised candidate the same way. No new requirements or injected faults in this run. First attempt correct means natural LIVE-03 NOT_EXERCISED, not a manufactured defect. If blocked or budget expires, preserve original handle/IDs/state; do not retry with fresh identities, switch accounts or upgrade models.
Finish with concise actual acceptance/ACK status, candidate OID and natural-rework status. Do not integrate; root has a separate private FF step. Product API has eight tools; fixture_validate_candidate is a ninth T12-only validation helper, not HostValidationEvidence.
FROZEN CONTRACT:\n''' + json.dumps(contract, indent=2, ensure_ascii=False)


def build_launch(contract, provider, cli, cli_sha, skill, skill_sha, options, run_dir):
    h.require_binding(contract)
    if provider not in ('codex','claude') or provider == contract['dispatch']['provider']:
        raise h.Refused('parent_direction_mismatch')
    for path, digest, label in [(cli,cli_sha,'parent_cli'),(skill,skill_sha,'installed_skill')]:
        if not Path(path).is_absolute() or str(Path(path).resolve()) != str(path) or h.file_sha(path) != digest:
            raise h.Refused('pin_mismatch_' + label)
    if not os.access(cli,os.X_OK): raise h.Refused('parent_cli_not_executable')
    # This intentionally accepts a tiny typed options object, never raw CLI overrides.
    allowed = {'feature_list_path','feature_list_sha256','disable_feature','disable_features'} if provider=='codex' else set()
    if set(options)-allowed: raise h.Refused('unsupported_parent_argument')
    run_dir=Path(run_dir).resolve()
    run_dir.mkdir(mode=0o700,parents=True,exist_ok=False)
    (run_dir/'bin').mkdir(mode=0o700)
    (run_dir/'bin'/provider).symlink_to(cli)
    proxy_argv=['-B',str(HERE/'t12_harness.py'),'proxy','--contract',str(run_dir/'contract.json'),'--execute-live','--',
                contract['binding']['wrapper']['path'],'mcp','serve','--config',contract['binding']['mcp_config']['path']]
    h.private_json(run_dir/'contract.json',contract)
    server={'command':contract['binding']['python']['path'],'args':proxy_argv}
    h.private_json(run_dir/'mcp.json',{'mcpServers':{'agent_bird':server}})
    prompt=parent_prompt(contract)
    (run_dir/'parent-prompt.txt').write_text(prompt);(run_dir/'parent-prompt.txt').chmod(0o600)
    if provider=='codex':
        disabled=options.get('disable_features',[options.get('disable_feature')])
        if not isinstance(disabled,list) or not disabled or not all(isinstance(item,str) for item in disabled) or not set(disabled)<=CODEX_DISABLE_FEATURES or 'multi_agent' not in disabled or len(set(disabled))!=len(disabled):
            raise h.Refused('verified_multi_agent_feature_required')
        features=Path(options['feature_list_path'])
        if h.file_sha(features)!=options['feature_list_sha256']:
            raise h.Refused('feature_list_pin')
        available={line.split()[0] for line in features.read_text().splitlines() if line.split()}
        if not set(disabled)<=available:
            raise h.Refused('multi_agent_feature_missing')
        codex_env_vars=h.codex_env_names(contract)
        toml=('{command='+json.dumps(server['command'])+',args='+json.dumps(server['args'])+
              ',env_vars='+json.dumps(codex_env_vars)+',enabled_tools='+json.dumps(list(AGENT_BIRD_TOOL_NAMES))+
              ',default_tools_approval_mode='+json.dumps(CODEX_MCP_DEFAULT_APPROVAL)+'}')
        args=['exec','--ignore-user-config',*[part for item in disabled for part in ('--disable',item)],'--json','-m','gpt-6-luna',
              '-c','model_reasoning_effort="low"','-c','mcp_servers.agent_bird='+toml,
              '-s','workspace-write','-C',contract['dispatch']['directory'],
              '--output-last-message',str(run_dir/'parent-last-message.txt'),prompt]
        stdin=b''
    else:
        args=policy.claude_parent_args(str(run_dir/'mcp.json'))
        stdin=(json.dumps({'type':'user','message':{'role':'user','content':[{'type':'text','text':prompt}]}})+'\n').encode()
    command=[contract['binding']['wrapper']['path'],'controller','start','--provider',provider,'--state-dir',contract['state_dir'],
             '--skill-path',skill,'--',*args]
    # Do not serialize environment values or full process argv to lifecycle evidence.
    h.private_json(run_dir/'launch.json',{'provider':provider,'parent_cli':cli,'parent_cli_sha256':cli_sha,
                   'skill':skill,'skill_sha256':skill_sha,'command':command,'stdin_sha256':h.sha(stdin),'wall_ms':180000})
    return command,stdin,str(run_dir/'bin')


def require_auth_preflight(contract, provider, cli_sha, path, digest):
    if not path or not digest or h.file_sha(path)!=digest:
        raise h.Refused('explicit_pinned_auth_preflight_required')
    report=json.loads(Path(path).read_text())
    allowed={'status','approved_for_bounded_live','parent_provider','worker_provider','parent_cli_sha256','package_sha256','dispatch_sha256','checked_unix','expires_unix','providers','source_report','source_report_sha256'}
    if set(report)-allowed:raise h.Refused('unknown_auth_preflight_field')
    if any(set(item)-{'status','route'} for item in report.get('providers',{}).values()):
        raise h.Refused('unknown_auth_preflight_provider_field')
    if report.get('status')!='READY' or report.get('approved_for_bounded_live') is not True:
        raise h.Refused('auth_preflight_not_ready')
    if report.get('parent_provider')!=provider or report.get('worker_provider')!=contract['dispatch']['provider']:
        raise h.Refused('auth_preflight_direction')
    if report.get('package_sha256')!=contract['binding']['package']['sha256'] or report.get('parent_cli_sha256')!=cli_sha:
        raise h.Refused('auth_preflight_package_or_cli')
    if report.get('dispatch_sha256')!=h.sha(h.canonical(contract['dispatch']).encode()):
        raise h.Refused('auth_preflight_dispatch')
    now=time.time()
    if not now-3600<=report.get('checked_unix',0)<=now or not now<report.get('expires_unix',0)<=report['checked_unix']+3600:
        raise h.Refused('auth_preflight_stale')
    providers=report.get('providers',{})
    if set(providers)!={'codex','claude'}:raise h.Refused('unknown_auth_preflight_provider')
    for name,route in [('codex','chatgpt-openai-default'),('claude','third-party-cli-default')]:
        if providers.get(name,{}).get('status')!='READY' or providers[name].get('route')!=route:
            raise h.Refused('auth_preflight_provider_not_ready_'+name)
    source=report.get('source_report')
    if not source or h.file_sha(source)!=report.get('source_report_sha256'):
        raise h.Refused('auth_preflight_source_report_pin')
    return {'auth_preflight_sha256':digest,'source_report_sha256':report['source_report_sha256']}


# Finite scenario-specific names only; values never enter argv/config/report.
REDACTION_ENV_NAMES=('ANTHROPIC_AUTH_TOKEN','ANTHROPIC_API_KEY','OPENAI_API_KEY',
                     'CODEX_API_KEY','OPENAI_ACCESS_TOKEN','ANTHROPIC_BASE_URL','OPENAI_BASE_URL')
LOG_CAP=4*1024*1024


class StreamRedactor:
    """Retain at most longest-secret-minus-one raw tail between fixed-size reads."""
    def __init__(self, values):
        self.values=sorted({value for value in values if value},key=lambda value:(-len(value),value))
        self.longest=max((len(value) for value in self.values),default=1)
        self.tail=b''
        self.count=0
    def feed(self, chunk, eof=False):
        data=self.tail+chunk
        safe=len(data) if eof else max(0,len(data)-self.longest+1)
        output=[];cursor=0
        while cursor<safe:
            matches=[(data.find(value,cursor),value) for value in self.values]
            matches=[item for item in matches if item[0]>=cursor and item[0]<safe]
            if not matches:
                output.append(data[cursor:safe]);cursor=safe;break
            start,value=min(matches,key=lambda item:(item[0],-len(item[1])))
            output.extend([data[cursor:start],b'[REDACTED]'])
            cursor=start+len(value);self.count+=1
        self.tail=data[cursor:]
        return b''.join(output)


def redaction_material(env):
    names=[];values=[]
    for name in REDACTION_ENV_NAMES:
        value=env.get(name)
        if value:
            encoded=os.fsencode(value)
            if len(encoded)>65536:raise h.Refused('redaction_value_too_large_'+name)
            names.append(name);values.append(encoded)
    return names,values


def drain_redacted(stream,dest,values,cap=LOG_CAP):
    redactor=StreamRedactor(values)
    remaining=cap
    while True:
        chunk=stream.read(65536)
        sanitized=redactor.feed(chunk,eof=not chunk)
        if remaining:
            selected=sanitized[:remaining]
            dest.write(selected);remaining-=len(selected)
        if not chunk:break
    return redactor.count


class BSDInfo(ctypes.Structure):
    _fields_=[(name,ctypes.c_uint32) for name in ['flags','status','xstatus','pid','ppid','uid','gid','ruid','rgid','svuid','svgid','rfu']]+[
        ('comm',ctypes.c_char*16),('name',ctypes.c_char*32)]+[(name,ctypes.c_uint32) for name in ['nfiles','pgid','pjobc','e_tdev','e_tpgid','nice']]+[
        ('start_sec',ctypes.c_uint64),('start_usec',ctypes.c_uint64)]


def identity(pid):
    if sys.platform!='darwin': raise h.Refused('darwin_identity_required')
    lib=ctypes.CDLL('/usr/lib/libproc.dylib',use_errno=True)
    info=BSDInfo()
    if lib.proc_pidinfo(pid,3,0,ctypes.byref(info),ctypes.sizeof(info)) != ctypes.sizeof(info): return None
    path=ctypes.create_string_buffer(4096)
    if lib.proc_pidpath(pid,path,len(path))<=0: return None
    return {'pid':pid,'ppid':info.ppid,'pgid':info.pgid,'birth':[info.start_sec,info.start_usec],
            'executable':os.fsdecode(path.value),'status':info.status}


def same_process(old,new):
    return new is not None and old['birth']==new['birth'] and old['executable']==new['executable']


def descendants(owned):
    # exec preserves kernel birth and ownership; record launcher -> runtime transitions.
    for pid,old in list(owned.items()):
        current=identity(pid)
        if current and current['birth']==old['birth'] and current['executable']!=old['executable']:
            history=old.setdefault('executable_history',[old['executable']])
            history.append(current['executable']);old['executable']=current['executable']
    # Only pid/ppid metadata is enumerated, never user process argv or environment.
    table=subprocess.check_output(['/bin/ps','-axo','pid=,ppid='],timeout=2).decode().splitlines()
    rows=[tuple(map(int,line.split())) for line in table if len(line.split())==2]
    changed=True
    while changed:
        changed=False
        for pid,ppid in rows:
            if pid in owned or ppid not in owned: continue
            if not same_process(owned[ppid],identity(ppid)): continue
            observed=identity(pid)
            if observed and observed['ppid']==ppid:
                owned[pid]=observed;changed=True


def alive_owned(owned):
    alive=[]
    for pid,old in owned.items():
        current=identity(pid)
        if same_process(old,current) and current['status']!=5: alive.append(pid)
    return alive


def unverified_owned(owned):
    unknown=[]
    for pid,old in owned.items():
        current=identity(pid)
        if current is None:
            try:os.kill(pid,0)
            except ProcessLookupError:continue
            except PermissionError:pass
            unknown.append(pid)
        elif current['birth']==old['birth'] and current['executable']!=old['executable']:
            unknown.append(pid)
    return unknown


def run_owned(command, stdin, env, cwd, run_dir, ttl=180, on_timeout=None):
    """Start a private controller session, TERM controller first, reap known descendants."""
    if ttl<=0 or ttl>180: raise h.Refused('scenario_ttl_out_of_range')
    redaction_names,redaction_values=redaction_material(env)
    run_dir=Path(run_dir)
    started=time.monotonic();deadline=started+ttl
    stop_at=deadline-min(6,ttl/3)
    owned={};cleanup=[];discovery_errors=[]
    def discover():
        try:descendants(owned)
        except (OSError,h.Refused,subprocess.SubprocessError) as err:discovery_errors.append(type(err).__name__)
    # Drain bounded output while allowing the controller to close naturally.
    with (run_dir/'parent.stdout').open('xb') as out, (run_dir/'parent.stderr').open('xb') as err:
        out_path=run_dir/'parent.stdout';err_path=run_dir/'parent.stderr'
        out_path.chmod(0o600);err_path.chmod(0o600)
        proc=subprocess.Popen(command,cwd=cwd,env=env,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,start_new_session=True)
        first=identity(proc.pid)
        if not first:
            # Child is still waitable: safe to kill our direct child before releasing its PID.
            proc.kill();proc.wait(timeout=3);raise h.Refused('controller_birth_unknown')
        owned[proc.pid]=first
        redaction_counts={'stdout':0,'stderr':0}
        def drain(stream,dest,name):
            redaction_counts[name]=drain_redacted(stream,dest,redaction_values)
        threads=[threading.Thread(target=drain,args=(proc.stdout,out,'stdout'),daemon=True),threading.Thread(target=drain,args=(proc.stderr,err,'stderr'),daemon=True)]
        for thread in threads:thread.start()
        timed_out=False
        try:
            try:proc.stdin.write(stdin);proc.stdin.close()
            except (OSError,BrokenPipeError) as err:discovery_errors.append('stdin_'+type(err).__name__)
            while True:
                discover()
                if proc.poll() is not None: break
                if time.monotonic()>=stop_at: timed_out=True;break
                time.sleep(.03)
        finally:
            if timed_out and on_timeout:
                try:on_timeout(max(0.1,min(2,deadline-time.monotonic()-3)))
                except Exception as err:discovery_errors.append('stop_callback_'+type(err).__name__)
            # Give Go first chance to execute its existing parent/child PGID cleanup.
            if proc.poll() is None and same_process(owned[proc.pid],identity(proc.pid)):
                os.kill(proc.pid,signal.SIGTERM);cleanup.append({'pid':proc.pid,'signal':'SIGTERM','reason':'controller_cleanup_first'})
            grace=min(time.monotonic()+2,deadline-min(1,ttl/3))
            while time.monotonic()<grace:
                discover()
                if not alive_owned(owned):break
                time.sleep(.03)
            for sig in (signal.SIGTERM,signal.SIGKILL):
                discover()
                for pid in alive_owned(owned):
                    current=identity(pid)
                    if same_process(owned[pid],current):
                        try: os.kill(pid,sig);cleanup.append({'pid':pid,'signal':sig.name,'reason':'recorded_birth_and_executable'})
                        except ProcessLookupError: pass
                until=min(time.monotonic()+min(.5,ttl/12),deadline-.15)
                while time.monotonic()<until and alive_owned(owned):time.sleep(.03)
            try:proc.wait(timeout=1)
            except subprocess.TimeoutExpired:pass
            for thread in threads:thread.join(timeout=max(0,min(.1,deadline-time.monotonic())))
        remaining=alive_owned(owned)
        unknown=unverified_owned(owned)
        output_not_closed=any(thread.is_alive() for thread in threads)
    return {'status':'BLOCKED' if timed_out or remaining or unknown or output_not_closed or discovery_errors or proc.returncode!=0 else 'PARENT_EXITED_NEEDS_POSTFLIGHT',
            'timed_out':timed_out,'discovery_errors':discovery_errors,'exit_code':proc.returncode,'wall_seconds':time.monotonic()-started,
            'owned_identities':list(owned.values()),'cleanup_signals':cleanup,'remaining_owned_pids':remaining,'unverified_owned_pids':unknown,'output_channels_closed':not output_not_closed,
            'stdout_sha256':h.file_sha(out_path),'stderr_sha256':h.file_sha(err_path),
            'redaction_applied':sum(redaction_counts.values())>0,'redaction_count':sum(redaction_counts.values()),
            'redaction_counts':redaction_counts,'redaction_env_names':redaction_names,
            'scope':'recorded controller descendants only; detached SourceHost requires public stop/ledger confirmation'}


def read_handle(contract):
    receipt=Path(contract['scratch_dir'])/'observed-dispatch.json'
    observed=json.loads(h.private_read(receipt)[0])
    if observed['dispatch_sha256']!=h.sha(h.canonical(contract['dispatch']).encode()) or observed['wrapper_sha256']!=contract['binding']['wrapper']['sha256']:
        raise h.Refused('observed_dispatch_binding')
    cli_handle=Path(observed['cli_handle'])
    if cli_handle.is_symlink() or not cli_handle.resolve().is_relative_to(Path(contract['state_dir']).resolve()):raise h.Refused('handle_scope')
    handle=json.loads(h.private_read(cli_handle)[0])
    if handle['state_dir']!=contract['state_dir'] or len(handle['task_ids'])!=1:raise h.Refused('handle_state_or_task_scope')
    return str(cli_handle),handle


def public(contract, action, handle, *args, timeout=15):
    result=subprocess.run([contract['binding']['wrapper']['path'],'task',action,'--handle',handle,*args],capture_output=True,timeout=timeout,check=True)
    if len(result.stdout)>2*1024*1024:raise h.Refused('public_result_too_large')
    return json.loads(result.stdout)


def postflight(contract, validation_path, validation_sha):
    h.require_binding(contract)
    cli_handle,handle=read_handle(contract)
    summary=public(contract,'inspect',cli_handle)
    collection=public(contract,'collect',cli_handle)
    if summary['run_id']!=handle['run_id']:raise h.Refused('summary_run_scope')
    task=next(t for t in summary['tasks'] if t['task_id']==handle['task_ids'][0])
    if task['active_segments']!=0 or task['status']!='completed':raise h.Refused('public_task_not_completed_inactive')
    repo=contract['dispatch']['directory']
    if h.snapshot(repo)!={'head':contract['base_oid'],'status':''}:raise h.Refused('original_workspace_changed')
    dbpath=Path(contract['state_dir'])/'state.db'
    if dbpath.is_symlink():raise h.Refused('database_symlink')
    with sqlite3.connect(dbpath.resolve().as_uri()+'?mode=ro',uri=True,timeout=2) as db:
        db.execute('PRAGMA query_only=ON');db.execute('BEGIN')
        taskid=handle['task_ids'][0];runid=handle['run_id'];group=contract['dispatch']['budget']['group_id']
        accepted=db.execute("SELECT e.body_json,e.event_revision,e.action_slot FROM review_decisions d JOIN runtime_events e ON e.event_id=d.event_id AND e.task_id=d.task_id AND e.event_revision=d.event_revision AND e.payload_hash=d.event_hash WHERE d.task_id=? AND d.decision='accept'",(taskid,)).fetchall()
        if len(accepted)!=1:raise h.Refused('unique_parent_accept_required')
        source_event=json.loads(accepted[0][0]);source_event['event_revision']=accepted[0][1];source_event['action_slot']=accepted[0][2]
        artifact=json.loads(h.private_read(source_event['artifact']['path'],limit=1024*1024)[0])['candidate']
        binding={'segment_id':source_event['segment_id'],'candidate_oid':artifact['candidate_oid'],'event_id':source_event['event_id'],
                 'event_revision':source_event['event_revision'],'event_hash':source_event['payload_hash'],'action_slot':source_event['action_slot'],'work_revision':source_event['work_revision']}
        expected_path=h.validation_receipt_path(contract,binding)
        if not validation_path or str(Path(validation_path))!=str(expected_path):raise h.Refused('validation_evidence_not_canonical')
        evidence,canonical_path,actual_sha=h.verify_validation_receipt(contract,cli_handle,handle,binding,source_event)
        if validation_sha!=actual_sha:raise h.Refused('validation_evidence_sha')
        oid=evidence['candidate_oid']
        row=db.execute('SELECT t.run_id,t.status,t.max_attempts,r.budget_group_id FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE t.id=?',(taskid,)).fetchone()
        if row!=(runid,'completed',2,group):raise h.Refused('database_task_scope_or_state')
        group_tasks=db.execute('SELECT task_id FROM task_runtime WHERE budget_group_id=?',(group,)).fetchall()
        if group_tasks!=[(taskid,)]:raise h.Refused('unexpected_group_tasks')
        policy=db.execute('SELECT max_attempts,max_active_ms_per_segment,max_active_ms_total FROM group_budget_policies WHERE budget_group_id=?',(group,)).fetchone()
        if policy!=(2,60000,90000):raise h.Refused('budget_policy_changed')
        attempts=db.execute('SELECT COUNT(*) FROM attempts WHERE task_id=?',(taskid,)).fetchone()[0]
        reservations=db.execute('SELECT granted_ms,used_ms,status FROM budget_runtime WHERE budget_group_id=?',(group,)).fetchall()
        if not 1<=attempts<=2 or not reservations:raise h.Refused('attempts_or_reservations_missing')
        if any(status!='settled' or not 0<=used<=grant<=60000 for grant,used,status in reservations):raise h.Refused('reserved_or_unknown_budget')
        used=sum(row[1] for row in reservations)
        if used>90000:raise h.Refused('shared_budget_exceeded')
        states=db.execute('SELECT s.status,s.outcome FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=?',(taskid,)).fetchall()
        if not states or any(status!='exited' for status,_ in states):raise h.Refused('active_or_unknown_segments')
        event=db.execute('SELECT body_json,event_revision,action_slot,delivery_status FROM runtime_events WHERE event_id=? AND task_id=?',(binding['event_id'],taskid)).fetchone()
        if not event or event[1:]!=(binding['event_revision'],binding['action_slot'],'acked'):raise h.Refused('candidate_event_not_acked_or_binding')
        original=json.loads(event[0])
        if original['payload_hash']!=binding['event_hash'] or original['artifact']['sha256']!=binding['event_hash']:
            raise h.Refused('candidate_event_hash')
        artifact=Path(original['artifact']['path'])
        if artifact.is_symlink() or not artifact.resolve().is_relative_to(Path(contract['state_dir']).resolve()) or h.file_sha(artifact)!=binding['event_hash']:
            raise h.Refused('final_artifact_changed')
        candidate=json.loads(artifact.read_text())['candidate']
        if candidate['candidate_oid']!=oid or candidate['tree_oid']!=evidence['tree_oid']:raise h.Refused('different_final_candidate')
        decision=db.execute('SELECT decision,event_revision,event_hash FROM review_decisions WHERE event_id=? AND task_id=?',(binding['event_id'],taskid)).fetchone()
        if decision!=('accept',binding['event_revision'],binding['event_hash']):raise h.Refused('parent_accept_missing')
        ack=db.execute('SELECT event_revision,event_hash FROM runtime_decisions WHERE event_id=?',(binding['event_id'],)).fetchone()
        if ack!=(binding['event_revision'],binding['event_hash']):raise h.Refused('parent_ack_missing')
        pending=db.execute("SELECT COUNT(*) FROM runtime_events WHERE task_id=? AND delivery_status='pending' AND action_slot!=''",(taskid,)).fetchone()[0]
        pending+=db.execute("SELECT COUNT(*) FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id WHERE c.task_id=? AND e.delivery_status IN ('pending','pending_session') AND e.action_slot!=''",(taskid,)).fetchone()[0]
        if pending or any(e.get('action_slot') for e in (collection['events'] or [])) or collection.get('next_cursor'):
            raise h.Refused('actionable_pending_not_acked')
    return {'status':'POSTFLIGHT_PASS','cli_handle':cli_handle,'run_id':runid,'task_id':taskid,'candidate_oid':oid,'tree_oid':evidence['tree_oid'],
            'parent_accept':True,'transport_ack':True,'active':0,'unknown':0,'reserved_ms':0,'used_ms':used,'attempts':attempts,
            'actionable_pending':0,'validation_evidence_sha256':validation_sha,'integrated':False,
            'natural_rework':'NOT_EXERCISED' if attempts==1 else 'REQUIRES_PARENT_FEEDBACK_EVIDENCE'}


def finish(contract, evidence, evidence_sha, integrate_private=False):
    result=postflight(contract,evidence,evidence_sha)
    if integrate_private:
        repo=Path(contract['dispatch']['directory']).resolve()
        # Only prepare-owned repo layout with two direction siblings is eligible.
        if repo.name!='repo' or repo.parent.name not in ('codex-to-claude','claude-to-codex') or not (repo.parent/'contract.json').is_file():
            raise h.Refused('not_prepare_owned_private_repo')
        if (repo/'.git').is_symlink() or not (repo/'.git').is_dir() or Path(h.git(repo,'rev-parse','--absolute-git-dir')).resolve()!=repo/'.git':
            raise h.Refused('private_repo_git_metadata_not_owned')
        original=json.loads((repo.parent/'contract.json').read_text())
        if original['base_oid']!=contract['base_oid'] or original['dispatch']!=contract['dispatch']:
            raise h.Refused('private_repo_contract_changed')
        h.git(repo,'merge-base','--is-ancestor',contract['base_oid'],result['candidate_oid'])
        h.git(repo,'merge','--ff-only','--no-edit',result['candidate_oid'])
        if h.snapshot(repo)!={'head':result['candidate_oid'],'status':''}:raise h.Refused('private_ff_postcondition')
        result['integrated']=True
    return result


def main():
    parser=argparse.ArgumentParser()
    parser.add_argument('action',choices=['prepare-launch','launch','finish'])
    parser.add_argument('--contract',required=True);parser.add_argument('--run-dir')
    parser.add_argument('--provider',choices=['codex','claude']);parser.add_argument('--cli');parser.add_argument('--cli-sha256')
    parser.add_argument('--skill');parser.add_argument('--skill-sha256');parser.add_argument('--parent-options')
    parser.add_argument('--auth-preflight');parser.add_argument('--auth-preflight-sha256')
    parser.add_argument('--execute-live',action='store_true');parser.add_argument('--validation-evidence');parser.add_argument('--validation-sha256')
    parser.add_argument('--integrate-private',action='store_true')
    args=parser.parse_args();contract=json.loads(Path(args.contract).read_text())
    if args.action=='finish':
        result=finish(contract,args.validation_evidence,args.validation_sha256,args.integrate_private)
        print(json.dumps(result,indent=2));return
    if args.action=='launch' and not args.execute_live:raise h.Refused('execute_live_required')
    auth_binding={}
    if args.action=='launch':auth_binding=require_auth_preflight(contract,args.provider,args.cli_sha256,args.auth_preflight,args.auth_preflight_sha256)
    options=json.loads(Path(args.parent_options).read_text()) if args.parent_options else {}
    if args.action=='launch' and args.provider=='codex' and set(options.get('disable_features',[]))!=CODEX_DISABLE_FEATURES:
        raise h.Refused('parent_tools_not_isolated')
    command,stdin,binpath=build_launch(contract,args.provider,args.cli,args.cli_sha256,args.skill,args.skill_sha256,options,args.run_dir)
    if args.action=='prepare-launch':print(json.dumps({'status':'PREPARED_NOT_LIVE','run_dir':args.run_dir}));return
    env=os.environ.copy();env['PATH']=binpath+os.pathsep+env.get('PATH','/usr/bin:/bin')
    env['DISABLE_AUTOUPDATER']='1'
    env['CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC']='1'
    stop_result={}
    def stop_original(timeout):
        stop_deadline=time.monotonic()+timeout
        try:
            cli_handle,handle=read_handle(contract)
            summary=public(contract,'inspect',cli_handle,timeout=max(.05,stop_deadline-time.monotonic()))
            task=next(t for t in summary['tasks'] if t['task_id']==handle['task_ids'][0])
            public(contract,'stop',cli_handle,'--task-id',task['task_id'],'--work-revision',str(task['work_revision']),timeout=max(.05,stop_deadline-time.monotonic()))
            stop_result['stop_reinspection']=public(contract,'inspect',cli_handle,timeout=max(.05,stop_deadline-time.monotonic()))
        except (OSError,ValueError,KeyError,StopIteration,subprocess.SubprocessError) as err:
            stop_result['stop_status']='BLOCKED_'+type(err).__name__
    result=run_owned(command,stdin,env,policy.parent_cwd(args.run_dir,args.provider,contract['dispatch']['directory']),args.run_dir,on_timeout=stop_original)
    result.update(stop_result);result.update(auth_binding)
    # Read-only acceptance check after parent termination. Never invent a decision or ACK.
    try:
        candidates=list(Path(contract['scratch_dir']).glob('validation-*.json'))
        if not candidates or len(candidates)>4: raise h.Refused('missing_or_excess_validation_evidence')
        verified=[]
        for path in candidates:
            try: verified.append(postflight(contract,str(path),h.file_sha(path)))
            except (ValueError,KeyError,StopIteration,OSError,sqlite3.Error,subprocess.SubprocessError): pass
        if len(verified)!=1: raise h.Refused('no_unique_accepted_acked_validated_candidate')
        result['postflight']=verified[0]
        if result['status']=='PARENT_EXITED_NEEDS_POSTFLIGHT':result['status']='SCENARIO_PASS'
    except (ValueError,KeyError,StopIteration,OSError,sqlite3.Error,subprocess.SubprocessError) as err:
        result['status']='BLOCKED';result['postflight_status']='BLOCKED_'+type(err).__name__
        if not stop_result:stop_original(2);result.update(stop_result)
    h.private_json(Path(args.run_dir)/'scenario-result.json',result)
    print(json.dumps(result,indent=2))

if __name__=='__main__':
    try:main()
    except h.Refused as err:raise SystemExit(str(err))
