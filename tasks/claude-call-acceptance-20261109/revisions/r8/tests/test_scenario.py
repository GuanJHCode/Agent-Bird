import importlib.util
import json
import os
from pathlib import Path
import signal
import sqlite3
import subprocess
import sys
import time

import pytest
from test_t12_harness import prepared, evidence, fixture_binding, PYTHON

ROOT=Path(__file__).resolve().parents[1]
sys.path.insert(0,str(ROOT/'scripts'))
import scenario as s


def test_native_kernel_identity_matches_self():
    identity=s.identity(os.getpid())
    assert identity['pid']==os.getpid() and identity['ppid']==os.getppid()
    assert identity['pgid']==os.getpgid(0) and identity['birth'][0]>0
    assert Path(identity['executable']).is_absolute()
    assert s.identity(os.getpid())['executable']==identity['executable']


def launch_inputs(prepared,tmp_path):
    fixture_binding(prepared,tmp_path,'pass\n')
    skill=tmp_path/'SKILL.md';skill.write_text('fixture installed skill')
    return dict(contract=prepared,provider='codex',cli=str(Path(PYTHON).resolve()),cli_sha=s.h.file_sha(PYTHON),
                skill=str(skill),skill_sha=s.h.file_sha(skill),options={},run_dir=tmp_path/'scenario')


@pytest.mark.parametrize('extra',[{'args':['--dangerously-bypass-approvals-and-sandbox']},{'model':'gpt-expensive'},{'budget':999999},{'env':{'KEY':'never-read'}},{'tools':'shell'},{'disable_feature':'other'}])
def test_parent_argument_override_refused(prepared,tmp_path,extra):
    values=launch_inputs(prepared,tmp_path);values['options']=extra
    with pytest.raises(s.h.Refused):s.build_launch(**values)


def test_codex_launch_is_pinned_isolated_and_frozen(prepared,tmp_path):
    values=launch_inputs(prepared,tmp_path)
    features=tmp_path/'features.txt';features.write_text('multi_agent experimental true\n')
    values['options']={'disable_feature':'multi_agent','feature_list_path':str(features),'feature_list_sha256':s.h.file_sha(features)}
    command,stdin,binpath=s.build_launch(**values)
    assert command[:3]==[prepared['binding']['wrapper']['path'],'controller','start']
    assert '--ignore-user-config' in command and '--disable' in command
    assert command[command.index('-m')+1]=='gpt-6-luna'
    assert command[command.index('-s')+1]=='workspace-write' and stdin==b''
    assert Path(binpath,'codex').resolve()==Path(PYTHON).resolve()
    prompt=(Path(values['run_dir'])/'parent-prompt.txt').read_text()
    assert prepared['dispatch']['request_id'] in prompt and 'request_changes' in prompt and 'NOT_EXERCISED' in prompt
    assert 'ACK is transport bookkeeping' in prompt
    config=json.loads((Path(values['run_dir'])/'mcp.json').read_text())
    assert list(config['mcpServers'])==['agent_bird']
    assert '--execute-live' in config['mcpServers']['agent_bird']['args']
    overrides=[command[i+1] for i,value in enumerate(command[:-1]) if value=='-c']
    mcp_override=next(value for value in overrides if value.startswith('mcp_servers.agent_bird='))
    server=__import__('tomllib').loads('mcp_servers.agent_bird='+mcp_override.split('=',1)[1])['mcp_servers']['agent_bird']
    assert server['enabled_tools']==list(s.AGENT_BIRD_TOOL_NAMES)
    assert server['default_tools_approval_mode']==s.CODEX_MCP_DEFAULT_APPROVAL=='approve'
    assert len(s.AGENT_BIRD_TOOL_NAMES)==9 and len(set(s.AGENT_BIRD_TOOL_NAMES))==9
    assert set(server)=={'command','args','env_vars','enabled_tools','default_tools_approval_mode'}
    assert not any(name in ('*','shell','exec','filesystem') for name in server['enabled_tools'])
    generated=json.loads(Path(prepared['binding']['codex_args']['path']).read_text())
    expected=json.loads(generated[5].split('=',1)[1])
    assert server['env_vars']==expected==['AGENT_BIRD_OWNER_REQUEST','FIXTURE_AUTH_TOKEN']
    assert all('FIXTURE_SECRET_VALUE' not in item for item in config['mcpServers']['agent_bird']['args'])


@pytest.mark.parametrize('override',[
    {'enabled_tools':['dispatch','*']},
    {'default_tools_approval_mode':'bypass'},
    {'approval_mode':'auto'},
    {'permission_mode':'bypassPermissions'},
    {'extra_mcp_tool':'invented'},
])
def test_codex_caller_cannot_override_private_server_policy(prepared,tmp_path,override):
    values=launch_inputs(prepared,tmp_path);values['options']=override
    with pytest.raises(s.h.Refused,match='unsupported_parent_argument'):
        s.build_launch(**values)


@pytest.mark.parametrize('attack',['missing','wrong_config','malicious_env_name'])
def test_product_codex_args_are_required_and_strict(prepared,tmp_path,attack):
    fixture_binding(prepared,tmp_path,'pass\n')
    binding=prepared['binding']['codex_args'];sidecar=Path(binding['path'])
    argv=json.loads(sidecar.read_text())
    if attack=='missing':
        sidecar.unlink()
        with pytest.raises(s.h.Refused,match='codex_args'):s.h.require_binding(prepared)
    else:
        if attack=='wrong_config':
            value=json.loads(argv[3].split('=',1)[1]);value[-1]=str(Path(value[-1]).with_name('other.json'))
            argv[3]='mcp_servers.agent_bird.args='+json.dumps(value)
        else:
            argv[5]='mcp_servers.agent_bird.env_vars='+json.dumps(['AGENT_BIRD_OWNER_REQUEST','TOKEN=FIXTURE_SECRET_VALUE'])
        sidecar.write_text(json.dumps(argv));binding['sha256']=s.h.file_sha(sidecar)
        with pytest.raises(s.h.Refused,match='invalid_product_codex_args') as error:s.h.require_binding(prepared)
        assert 'FIXTURE_SECRET_VALUE' not in str(error.value)


def test_codex_args_digest_change_refused(prepared,tmp_path):
    fixture_binding(prepared,tmp_path,'pass\n')
    s.h.require_binding(prepared)
    Path(prepared['binding']['codex_args']['path']).write_text('[]')
    with pytest.raises(s.h.Refused,match='codex_args'):s.h.require_binding(prepared)


def test_claude_keeps_default_model_without_bare(prepared,tmp_path):
    prepared['dispatch']['provider']='codex';prepared['dispatch']['model']='gpt-6-luna'
    values=launch_inputs(prepared,tmp_path);values['provider']='claude'
    config=Path(prepared['binding']['mcp_config']['path']);config.write_text(json.dumps({'state_dir':prepared['state_dir'],'workspace':prepared['dispatch']['directory'],'providers':{'codex':'fixture-lock'}}))
    prepared['binding']['mcp_config']['sha256']=s.h.file_sha(config)
    command,stdin,_=s.build_launch(**values)
    assert '--model' not in command and '--bare' not in command
    assert '--strict-mcp-config' in command and command[command.index('--tools')+1]==''
    assert command[command.index('--permission-mode')+1]=='dontAsk'
    allowed_index=command.index('--allowedTools')+1
    assert tuple(command[allowed_index:allowed_index+len(s.CLAUDE_ALLOWED_TOOLS)])==s.CLAUDE_ALLOWED_TOOLS
    assert len(s.CLAUDE_ALLOWED_TOOLS)==9 and len(set(s.CLAUDE_ALLOWED_TOOLS))==9
    assert all(name.startswith('mcp__agent_bird__') for name in s.CLAUDE_ALLOWED_TOOLS)
    assert not any(value in command for value in ('--dangerously-skip-permissions','bypassPermissions','*','--permission-prompts'))
    assert json.loads(stdin)['message']['role']=='user'


@pytest.mark.parametrize('override',[
    {'allowed_tools':['mcp__agent_bird__dispatch','*']},
    {'allowedTools':['mcp__agent_bird__dispatch','mcp__other__run']},
    {'permission_mode':'bypassPermissions'},
    {'permission_prompts':'none'},
    {'tools':'*'},
    {'extra_mcp_tool':'mcp__agent_bird__invented'},
])
def test_claude_caller_cannot_override_permission_allowlist(prepared,tmp_path,override):
    prepared['dispatch']['provider']='codex';prepared['dispatch']['model']='gpt-6-luna'
    values=launch_inputs(prepared,tmp_path);values['provider']='claude';values['options']=override
    config=Path(prepared['binding']['mcp_config']['path'])
    config.write_text(json.dumps({'state_dir':prepared['state_dir'],'workspace':prepared['dispatch']['directory'],'providers':{'codex':'fixture-lock'}}))
    prepared['binding']['mcp_config']['sha256']=s.h.file_sha(config)
    with pytest.raises(s.h.Refused,match='unsupported_parent_argument'):
        s.build_launch(**values)


def test_no_execute_live_flag_never_starts_wrapper(prepared,tmp_path):
    contract=tmp_path/'unbound.json';contract.write_text(json.dumps(prepared))
    result=subprocess.run([PYTHON,'-B',str(ROOT/'scripts/scenario.py'),'launch','--contract',str(contract)],capture_output=True,text=True,timeout=5)
    assert result.returncode!=0 and 'execute_live_required' in result.stderr


def test_native_cleanup_owns_detached_descendants_not_sibling(tmp_path):
    sibling=subprocess.Popen([PYTHON,'-c','import time;time.sleep(20)'],start_new_session=True)
    run=tmp_path/'run';run.mkdir()
    controller=tmp_path/'fixture-controller.py'
    child='import signal,time;signal.signal(signal.SIGTERM,signal.SIG_IGN);time.sleep(20)'
    controller.write_text('import subprocess,sys,time,signal\nsubprocess.Popen([sys.executable,"-c",'+repr(child)+'],start_new_session=True)\nsignal.signal(signal.SIGTERM,signal.SIG_IGN)\nwhile True: time.sleep(.1)\n')
    try:
        result=s.run_owned([PYTHON,str(controller)],b'',os.environ.copy(),tmp_path,run,ttl=1.5)
        (run/'fixture-lifecycle.json').write_text(json.dumps(result,indent=2))
        assert result['status']=='BLOCKED' and result['timed_out']
        assert len(result['owned_identities'])>=2,result
        assert len({i['pgid'] for i in result['owned_identities']})>=2,result
        assert result['remaining_owned_pids']==[] and result['output_channels_closed'],result
        assert result['wall_seconds']<3,result
        assert sibling.poll() is None
        assert any(item['signal']=='SIGKILL' for item in result['cleanup_signals'])
    finally:
        sibling.terminate();sibling.wait(timeout=3)


def ledger_fixture(prepared,tmp_path):
    handle,summary,pages,oid=evidence(prepared)
    binding=summary['tasks'][0]['reviewable_result']
    phase=tmp_path/'fixture-core-accepted.json';phase.write_text('false')
    wrapper=fixture_binding(prepared,tmp_path, '''import sys,json
from pathlib import Path
accepted=json.loads(Path('''+repr(str(phase))+''').read_text())
summary=json.loads('''+repr(json.dumps(summary))+''')
summary['tasks'][0]['status']='completed' if accepted else 'result_ready'
if sys.argv[2]=='inspect':print(json.dumps(summary))
elif sys.argv[2]=='collect':print('{"events":[]}' if accepted else '''+repr(json.dumps(pages[0]))+''')
else:raise SystemExit('fixture forbids business mutation')
''')
    handle_path=Path(prepared['state_dir'])/'fixture-handle.json';handle_path.write_text(json.dumps(handle));handle_path.chmod(0o600)
    s.h.private_json(Path(prepared['scratch_dir'])/'observed-dispatch.json',{'cli_handle':str(handle_path),
             'dispatch_sha256':s.h.sha(s.h.canonical(prepared['dispatch']).encode()),'wrapper_sha256':prepared['binding']['wrapper']['sha256']})
    s.h.validation_session(prepared,create=True)
    real_result=s.h.validate_candidate(prepared,str(handle_path),oid,str(handle_path))
    assert real_result['validation']['exit_code']==0
    validation=Path(real_result['validation_evidence_path'])
    phase.write_text('true')
    db=sqlite3.connect(Path(prepared['state_dir'])/'state.db')
    db.executescript('''CREATE TABLE tasks(id,run_id,status,max_attempts);CREATE TABLE task_runtime(task_id,budget_group_id);
CREATE TABLE group_budget_policies(budget_group_id,max_attempts,max_active_ms_per_segment,max_active_ms_total);
CREATE TABLE attempts(id,task_id);CREATE TABLE budget_runtime(budget_group_id,granted_ms,used_ms,status);
CREATE TABLE segment_runtime(attempt_id,status,outcome);CREATE TABLE runtime_events(event_id,task_id,body_json,event_revision,action_slot,delivery_status,payload_hash);
CREATE TABLE review_decisions(event_id,task_id,decision,event_revision,event_hash);CREATE TABLE runtime_decisions(event_id,event_revision,event_hash);
CREATE TABLE report_events(capability_id,delivery_status,action_slot);CREATE TABLE report_capabilities(capability_id,task_id);''')
    group=prepared['dispatch']['budget']['group_id']
    db.execute('INSERT INTO tasks VALUES(?,?,?,?)',('t','r','completed',2));db.execute('INSERT INTO task_runtime VALUES(?,?)',('t',group))
    db.execute('INSERT INTO group_budget_policies VALUES(?,?,?,?)',(group,2,90000,90000))
    db.execute('INSERT INTO attempts VALUES(?,?)',('a','t'));db.execute('INSERT INTO budget_runtime VALUES(?,?,?,?)',(group,60000,123,'settled'))
    db.execute('INSERT INTO segment_runtime VALUES(?,?,?)',('a','exited','result'))
    db.execute('INSERT INTO runtime_events VALUES(?,?,?,?,?,?,?)',('e','t',json.dumps(pages[0]['events'][0]),1,'slot','acked',binding['event_hash']))
    db.execute('INSERT INTO review_decisions VALUES(?,?,?,?,?)',('e','t','accept',1,binding['event_hash']))
    db.execute('INSERT INTO runtime_decisions VALUES(?,?,?)',('e',1,binding['event_hash']));db.commit();db.close()
    return validation,oid


def test_read_only_postflight_then_explicit_private_ff(prepared,tmp_path):
    validation,oid=ledger_fixture(prepared,tmp_path)
    result=s.finish(prepared,str(validation),s.h.file_sha(validation))
    assert result['status']=='POSTFLIGHT_PASS' and not result['integrated']
    assert result['active']==result['unknown']==result['reserved_ms']==result['actionable_pending']==0
    assert s.h.git(prepared['dispatch']['directory'],'rev-parse','HEAD')==prepared['base_oid']
    result=s.finish(prepared,str(validation),s.h.file_sha(validation),integrate_private=True)
    assert result['integrated'] and s.h.git(prepared['dispatch']['directory'],'rev-parse','HEAD')==oid


def test_single_segment_can_use_existing_ninety_second_total(prepared, tmp_path):
    validation, oid = ledger_fixture(prepared, tmp_path)
    with sqlite3.connect(Path(prepared['state_dir']) / 'state.db') as db:
        db.execute('UPDATE group_budget_policies SET max_active_ms_per_segment=90000')
        db.execute('UPDATE budget_runtime SET granted_ms=90000,used_ms=70000')
    result = s.postflight(prepared, str(validation), s.h.file_sha(validation))
    assert result['used_ms'] == 70000 and result['attempts'] == 1
    assert result['candidate_oid'] == oid and not result['integrated']
    assert result['parent_accept'] and result['transport_ack']


@pytest.mark.parametrize('attack',['different_candidate','active','unknown','reserved','pending','no_ack','no_accept','excess_attempts','overspend','wrong_group','wrong_tree'])
def test_postflight_never_false_pass(prepared,tmp_path,attack):
    validation,oid=ledger_fixture(prepared,tmp_path)
    db=sqlite3.connect(Path(prepared['state_dir'])/'state.db')
    commands={'active':"UPDATE segment_runtime SET status='running'",'unknown':"UPDATE segment_runtime SET status='unknown'",
      'reserved':"UPDATE budget_runtime SET status='reserved'",'pending':"UPDATE runtime_events SET delivery_status='pending'",
      'no_ack':"DELETE FROM runtime_decisions",'no_accept':"DELETE FROM review_decisions",
      'excess_attempts':"INSERT INTO attempts VALUES('b','t');INSERT INTO attempts VALUES('c','t')",
      'overspend':"UPDATE budget_runtime SET granted_ms=60000,used_ms=50000;INSERT INTO budget_runtime SELECT budget_group_id,60000,50000,'settled' FROM budget_runtime LIMIT 1",
      'wrong_group':"UPDATE task_runtime SET budget_group_id='other'"}
    if attack in commands:db.executescript(commands[attack])
    db.commit();db.close()
    if attack in ('different_candidate','wrong_tree'):
        body=json.loads(validation.read_text());body['candidate_oid' if attack=='different_candidate' else 'tree_oid']='0'*40;validation.write_text(json.dumps(body))
    with pytest.raises((s.h.Refused,subprocess.CalledProcessError)):s.postflight(prepared,str(validation),s.h.file_sha(validation))
    assert s.h.snapshot(prepared['dispatch']['directory'])=={'head':prepared['base_oid'],'status':''}


def test_proxy_persists_observed_handle_and_same_candidate_evidence(prepared,tmp_path):
    handle,summary,pages,oid=evidence(prepared)
    handle_path=Path(prepared['state_dir'])/'fixture-handle.json';handle_path.write_text(json.dumps(handle));handle_path.chmod(0o600)
    wrapper=fixture_binding(prepared,tmp_path,'''import json,sys
if sys.argv[1]=='task':
    if sys.argv[2]=='inspect':print('''+repr(json.dumps(summary))+''')
    elif sys.argv[2]=='collect':print('''+repr(json.dumps(pages[0]))+''')
    else:raise SystemExit('no fixture decisions')
else:
    for line in sys.stdin:
        value=json.loads(line)
        result={'content':[{'type':'text','text':json.dumps({'cli_handle':''' +repr(str(handle_path))+''','handle':'fixture-original-route'})}]}
        print(json.dumps({'jsonrpc':'2.0','id':value['id'],'result':result}),flush=True)
''')
    contract=tmp_path/'bound.json';contract.write_text(json.dumps(prepared))
    process=subprocess.Popen([PYTHON,'-B',str(ROOT/'scripts/t12_harness.py'),'proxy','--contract',str(contract),'--execute-live','--',str(wrapper),'mcp','serve','--config',prepared['binding']['mcp_config']['path']],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
    try:
        process.stdin.write(json.dumps({'jsonrpc':'2.0','id':1,'method':'tools/call','params':{'name':'dispatch','arguments':prepared['dispatch']}})+'\n');process.stdin.flush()
        assert json.loads(process.stdout.readline())['id']==1
        process.stdin.write(json.dumps({'jsonrpc':'2.0','id':2,'method':'tools/call','params':{'name':'fixture_validate_candidate','arguments':{'cli_handle':str(handle_path),'candidate_oid':oid}}})+'\n');process.stdin.flush()
        response=json.loads(process.stdout.readline())
        assert 'error' not in response,response
        returned=json.loads(response['result']['content'][0]['text'])
        path=Path(returned['validation_evidence_path'])
        assert path.is_relative_to(Path(prepared['scratch_dir']))
        assert s.h.file_sha(path)==returned['validation_evidence_sha256']
        persisted=json.loads(path.read_text())
        assert persisted['candidate_oid']==oid and persisted['validation']['exit_code']==0
        assert s.read_handle(prepared)[0]==str(handle_path)
    finally:
        process.stdin.close();process.stdin=None
        _,err=process.communicate(timeout=10)
    assert process.returncode==0,err


def preflight_fixture(prepared,tmp_path):
    source=tmp_path/'nonsecret-fixture-preflight-source.json';source.write_text('{"fixture":true,"models_called":false}')
    now=time.time()
    report={'status':'READY','approved_for_bounded_live':True,'parent_provider':'codex','worker_provider':'claude',
            'parent_cli_sha256':'fixture-cli-sha','package_sha256':prepared['binding']['package']['sha256'],
            'dispatch_sha256':s.h.sha(s.h.canonical(prepared['dispatch']).encode()),'checked_unix':now-1,'expires_unix':now+60,
            'providers':{'codex':{'status':'READY','route':'chatgpt-openai-default'},'claude':{'status':'READY','route':'third-party-cli-default'}},
            'source_report':str(source),'source_report_sha256':s.h.file_sha(source)}
    path=tmp_path/'auth-preflight.json';path.write_text(json.dumps(report))
    return path,report


@pytest.mark.parametrize('attack',['missing','blocked','unapproved','expired','wrong_package','wrong_cli','wrong_direction','wrong_dispatch','source_changed','claude_unavailable','codex_route'])
def test_auth_preflight_blocks_before_launch(prepared,tmp_path,attack):
    fixture_binding(prepared,tmp_path,'pass\n');path,report=preflight_fixture(prepared,tmp_path)
    if attack=='missing':path=None
    elif attack=='blocked':report['status']='BLOCKED'
    elif attack=='unapproved':del report['approved_for_bounded_live']
    elif attack=='expired':report['expires_unix']=time.time()-1
    elif attack=='wrong_package':report['package_sha256']='other'
    elif attack=='wrong_cli':report['parent_cli_sha256']='other'
    elif attack=='wrong_direction':report['worker_provider']='codex'
    elif attack=='wrong_dispatch':report['dispatch_sha256']='other'
    elif attack=='source_changed':Path(report['source_report']).write_text('changed')
    elif attack=='claude_unavailable':report['providers']['claude']['status']='BLOCKED'
    elif attack=='codex_route':report['providers']['codex']['route']='other'
    if path:path.write_text(json.dumps(report))
    with pytest.raises(s.h.Refused):s.require_auth_preflight(prepared,'codex','fixture-cli-sha',path,s.h.file_sha(path) if path else None)


def test_explicit_fixture_auth_preflight_reports_pins_only(prepared,tmp_path):
    fixture_binding(prepared,tmp_path,'pass\n');path,report=preflight_fixture(prepared,tmp_path)
    result=s.require_auth_preflight(prepared,'codex','fixture-cli-sha',path,s.h.file_sha(path))
    assert set(result)=={'auth_preflight_sha256','source_report_sha256'}


def test_ttl_callback_failure_still_cleans_owned_process(tmp_path):
    run=tmp_path/'callback-failure';run.mkdir()
    def fail(_):raise RuntimeError('fixture cleanup callback failed')
    result=s.run_owned([PYTHON,'-c','import time;time.sleep(20)'],b'',os.environ.copy(),tmp_path,run,ttl=1,on_timeout=fail)
    assert result['status']=='BLOCKED' and result['remaining_owned_pids']==[]
    assert 'stop_callback_RuntimeError' in result['discovery_errors']


def test_whole_launch_fake_parent_exits_without_dispatch_remains_blocked(prepared,tmp_path):
    calls=tmp_path/'fixture-wrapper-calls.jsonl'
    wrapper=fixture_binding(prepared,tmp_path,'''import sys,json,subprocess,shutil
with open('''+repr(str(calls))+''','a') as out:out.write(json.dumps(sys.argv[1:])+'\\n')
if sys.argv[1:3]!=['controller','start']:raise SystemExit('unexpected fixture business action')
provider=sys.argv[sys.argv.index('--provider')+1]
raise SystemExit(subprocess.run([shutil.which(provider),*sys.argv[sys.argv.index('--')+1:]]).returncode)
''')
    parent=tmp_path/'pinned-fixture-codex'
    parent.write_text('#!'+PYTHON+'\nprint("FIXTURE PARENT: no HTTP, no tool call, no decision")\n');parent.chmod(0o700)
    skill=tmp_path/'SKILL.md';skill.write_text('fixture skill')
    features=tmp_path/'features.txt';features.write_text(''.join(name+' experimental true\n' for name in sorted(s.CODEX_DISABLE_FEATURES)))
    options=tmp_path/'parent-options.json';options.write_text(json.dumps({'disable_features':sorted(s.CODEX_DISABLE_FEATURES),'feature_list_path':str(features),'feature_list_sha256':s.h.file_sha(features)}))
    contract=tmp_path/'contract.json';contract.write_text(json.dumps(prepared))
    auth,body=preflight_fixture(prepared,tmp_path);body['parent_cli_sha256']=s.h.file_sha(parent);auth.write_text(json.dumps(body))
    run=tmp_path/'whole-launch'
    result=subprocess.run([PYTHON,'-B',str(ROOT/'scripts/scenario.py'),'launch','--execute-live','--contract',str(contract),
       '--run-dir',str(run),'--provider','codex','--cli',str(parent),'--cli-sha256',s.h.file_sha(parent),
       '--skill',str(skill),'--skill-sha256',s.h.file_sha(skill),'--parent-options',str(options),
       '--auth-preflight',str(auth),'--auth-preflight-sha256',s.h.file_sha(auth)],capture_output=True,text=True,timeout=10)
    assert result.returncode==0,result.stderr
    report=json.loads((run/'scenario-result.json').read_text())
    assert report['status']=='BLOCKED' and report['exit_code']==0
    assert report['remaining_owned_pids']==[] and report['output_channels_closed']
    assert report['postflight_status'].startswith('BLOCKED_')
    recorded=[json.loads(line) for line in calls.read_text().splitlines()]
    assert len(recorded)==1 and recorded[0][:2]==['controller','start']


def test_cli_explicit_execute_still_requires_auth_preflight_before_preparation(prepared,tmp_path):
    contract=tmp_path/'unbound.json';contract.write_text(json.dumps(prepared));run=tmp_path/'must-not-exist'
    result=subprocess.run([PYTHON,'-B',str(ROOT/'scripts/scenario.py'),'launch','--execute-live','--contract',str(contract),'--run-dir',str(run)],capture_output=True,text=True,timeout=5)
    assert result.returncode!=0 and 'explicit_pinned_auth_preflight_required' in result.stderr
    assert not run.exists()


def test_missing_kernel_identity_is_unknown_not_confirmed_exit(monkeypatch):
    own={os.getpid():s.identity(os.getpid())}
    monkeypatch.setattr(s,'identity',lambda pid:None)
    assert s.unverified_owned(own)==[os.getpid()]


@pytest.mark.parametrize('cuts',[[],[1],[5],[10],list(range(1,28)),[65530,65536,65540]])
def test_stream_redaction_across_chunks_no_newline_and_eof(cuts):
    secret=b'PUBLIC_FIXTURE_TOKEN_123456789'
    source=b'A'*65525+secret+b':'+secret+b'END'
    redactor=s.StreamRedactor([secret,b'PUBLIC_FIXTURE_TOKEN'])
    chunks=[];position=0
    for cut in sorted(set(cuts)):
        if cut<=position or cut>=len(source):continue
        chunks.append(source[position:cut]);position=cut
    chunks.append(source[position:])
    sanitized=b''
    for chunk in chunks:
        sanitized+=redactor.feed(chunk)
        assert len(redactor.tail)<=len(secret)-1
    sanitized+=redactor.feed(b'',eof=True)
    assert secret not in sanitized and b'PUBLIC_FIXTURE_TOKEN' not in sanitized
    assert sanitized==b'A'*65525+b'[REDACTED]:[REDACTED]END'
    assert redactor.count==2 and redactor.tail==b''


@pytest.mark.parametrize('cap',[1,7,15,65530,65536,65550,66000])
def test_redaction_precedes_cap_and_no_tail_token_fragment(cap):
    import io
    secret=b'PUBLIC_CAP_SECRET_ABCDEFGHIJ'
    payload=b'Z'*65529+secret+secret
    destination=io.BytesIO()
    count=s.drain_redacted(io.BytesIO(payload),destination,[secret],cap=cap)
    expected=(b'Z'*65529+b'[REDACTED][REDACTED]')[:cap]
    assert destination.getvalue()==expected
    assert secret not in destination.getvalue() and b'PUBLIC_CAP' not in destination.getvalue()
    assert len(destination.getvalue())<=cap and count==2


def test_fake_cli_stdout_stderr_redacted_before_any_artifact_write(tmp_path):
    # Public sentinels only. Actual credentials are never supplied to fixtures.
    fake_token='PUBLIC_TEST_ONLY_SPLIT_TOKEN_987654321'
    fake_endpoint='https://public-fixture.invalid/gateway'
    script=tmp_path/'fake-echo-cli.py'
    script.write_text('''import os
for fd in (1,2):
    value=os.environ['ANTHROPIC_AUTH_TOKEN'].encode()
    endpoint=os.environ['ANTHROPIC_BASE_URL'].encode()
    os.write(fd,b'X'*65530)
    for piece in [value[:3],value[3:17],value[17:]]:os.write(fd,piece)
    os.write(fd,b'Y'*(4*1024*1024-65535))
    for piece in [value[:7],value[7:]]:os.write(fd,piece)
    os.write(fd,endpoint)
''')
    run=tmp_path/'redacted-run';run.mkdir()
    env={'PATH':'/usr/bin:/bin','ANTHROPIC_AUTH_TOKEN':fake_token,'ANTHROPIC_BASE_URL':fake_endpoint}
    result=s.run_owned([PYTHON,str(script)],b'',env,tmp_path,run,ttl=4)
    assert result['exit_code']==0 and result['remaining_owned_pids']==[] and result['unverified_owned_pids']==[]
    assert result['output_channels_closed'] and result['redaction_applied']
    assert result['redaction_counts']=={'stdout':3,'stderr':3}
    assert set(result['redaction_env_names'])=={'ANTHROPIC_AUTH_TOKEN','ANTHROPIC_BASE_URL'}
    (run/'report.json').write_text(json.dumps(result))
    for path in run.iterdir():
        data=path.read_bytes()
        assert fake_token.encode() not in data and fake_endpoint.encode() not in data
        if path.name.startswith('parent.'):assert len(data)<=s.LOG_CAP
    assert b'[REDACTED]' in (run/'parent.stdout').read_bytes()
    assert b'[REDACTED]' in (run/'parent.stderr').read_bytes()


def test_unknown_auth_receipt_fields_cannot_hold_raw_credentials(prepared,tmp_path):
    fixture_binding(prepared,tmp_path,'pass\n');path,report=preflight_fixture(prepared,tmp_path)
    report['raw_credentials']='PUBLIC_TEST_ONLY_FORBIDDEN_FIELD';path.write_text(json.dumps(report))
    with pytest.raises(s.h.Refused,match='unknown_auth_preflight_field'):
        s.require_auth_preflight(prepared,'codex','fixture-cli-sha',path,s.h.file_sha(path))


def test_unknown_preflight_provider_cannot_extend_nonsecret_schema(prepared,tmp_path):
    fixture_binding(prepared,tmp_path,'pass\n');path,report=preflight_fixture(prepared,tmp_path)
    report['providers']['unknown']={'status':'READY','route':'PUBLIC_TEST_ONLY_FORBIDDEN_ROUTE'};path.write_text(json.dumps(report))
    with pytest.raises(s.h.Refused,match='unknown_auth_preflight_provider'):
        s.require_auth_preflight(prepared,'codex','fixture-cli-sha',path,s.h.file_sha(path))


def ready_checker(prepared,tmp_path):
    handle,summary,pages,oid=evidence(prepared)
    fixture_binding(prepared,tmp_path,'''import sys
if sys.argv[2]=='inspect':print('''+repr(json.dumps(summary))+''')
elif sys.argv[2]=='collect':print('''+repr(json.dumps(pages[0]))+''')
else:raise SystemExit('fixture forbids business mutation')
''')
    path=Path(prepared['state_dir'])/'fixture-handle.json';path.write_text(json.dumps(handle));path.chmod(0o600)
    return path,summary,oid


def test_legacy_external_minimal_forgery_rejected_before_ff(prepared,tmp_path):
    actual,oid=ledger_fixture(prepared,tmp_path)
    legitimate=json.loads(actual.read_text())
    old=tmp_path/'old-forged-minimal.json'
    old.write_text(json.dumps({'contract':prepared,'validation':{'exit_code':0,'frozen_test_sha256':s.h.sha(s.h.CHECK.encode())},
                              'candidate_oid':oid,'tree_oid':legitimate['tree_oid'],'review_tuple':legitimate['review_tuple']}))
    with pytest.raises(s.h.Refused,match='not_canonical'):
        s.finish(prepared,str(old),s.h.file_sha(old),integrate_private=True)
    assert s.h.snapshot(prepared['dispatch']['directory'])=={'head':prepared['base_oid'],'status':''}


def test_canonical_path_preoccupation_rejects_before_candidate_exec(prepared,tmp_path,monkeypatch):
    path,summary,oid=ready_checker(prepared,tmp_path)
    s.h.validation_session(prepared,create=True)
    canonical=s.h.validation_receipt_path(prepared,summary['tasks'][0]['reviewable_result'])
    canonical.write_text('{"exit_code":0}');canonical.chmod(0o600)
    called=[];monkeypatch.setattr(s.h,'sandbox_candidate',lambda *args,**kwargs:called.append(args))
    with pytest.raises(s.h.Refused,match='preoccupied'):s.h.validate_candidate(prepared,str(path),oid,str(path))
    assert called==[] and canonical.read_text()=='{"exit_code":0}'


def test_session_preoccupation_is_not_adopted(prepared,tmp_path):
    path=Path(prepared['scratch_dir'])/'validation-session.json';path.write_text('not an owned session');path.chmod(0o600)
    with pytest.raises(s.h.Refused,match='preoccupied'):s.h.validation_session(prepared,create=True)
    assert path.read_text()=='not an owned session'


@pytest.mark.parametrize('attack',['symlink','hardlink','replacement','mode','scratch_replaced','parent_replaced','old_config'])
def test_private_receipt_filesystem_binding_rejects(prepared,tmp_path,attack):
    import shutil
    path,oid=ledger_fixture(prepared,tmp_path)
    original_sha=s.h.file_sha(path)
    if attack=='symlink':
        target=tmp_path/'copied.json';shutil.copyfile(path,target);path.unlink();path.symlink_to(target)
    elif attack=='hardlink':os.link(path,tmp_path/'hardlink.json')
    elif attack=='replacement':
        copy=tmp_path/'replacement.json';copy.write_bytes(path.read_bytes());copy.chmod(0o600);os.replace(copy,path)
    elif attack=='mode':path.chmod(0o644)
    elif attack in ('scratch_replaced','parent_replaced'):
        directory=Path(prepared['scratch_dir']) if attack=='scratch_replaced' else Path(prepared['scratch_dir']).parent
        moved=directory.with_name(directory.name+'-original');directory.rename(moved);shutil.copytree(moved,directory)
    elif attack=='old_config':
        config=Path(prepared['binding']['mcp_config']['path']);config.write_text(config.read_text()+'\n')
        prepared['binding']['mcp_config']['sha256']=s.h.file_sha(config)
    with pytest.raises((s.h.Refused,OSError,ValueError)):
        s.finish(prepared,str(path),original_sha,integrate_private=True)
    assert s.h.snapshot(prepared['dispatch']['directory'])=={'head':prepared['base_oid'],'status':''}


def resign_fixture_receipt(prepared,path,edit):
    # Test-only corruption of a genuinely executed checker body, to exercise semantic checks beyond HMAC.
    body=json.loads(path.read_text());body.pop('signature');edit(body)
    body['signature']=s.h.receipt_signature(s.h.validation_session(prepared),body)
    path.write_text(s.h.canonical(body)+'\n')


@pytest.mark.parametrize('attack',['missing_python_binding','wrong_python','missing_seatbelt','wrong_command','wrong_log_hash','truncated_log','workspace_before','workspace_after','source_blob','wrong_policy','artifact','old_run','old_segment','old_attempt','old_revision','old_config','old_session'])
def test_complete_receipt_semantics_reject_corruption_even_signed(prepared,tmp_path,attack):
    path,oid=ledger_fixture(prepared,tmp_path)
    def edit(body):
        if attack=='missing_python_binding':del body['contract']['binding']['python']
        elif attack=='wrong_python':body['validation']['executable_sha256']='0'*64
        elif attack=='missing_seatbelt':del body['validation']['sandbox']
        elif attack=='wrong_command':body['validation']['command'][0]='/usr/bin/python3'
        elif attack=='wrong_log_hash':body['validation']['stdout_sha256']='0'*64
        elif attack=='truncated_log':body['validation']['stdout_base64']=''
        elif attack=='workspace_before':body['workspace_before']['head']='0'*40
        elif attack=='workspace_after':body['workspace_after']['status']=' M calc.py'
        elif attack=='source_blob':body['source_blob_digest']='0'*64
        elif attack=='wrong_policy':
            body['validation']['policy']+='(allow file-read* (subpath '+json.dumps(prepared['scratch_dir'])+'))\n'
            body['validation']['policy_sha256']=s.h.sha(body['validation']['policy'].encode())
        elif attack=='artifact':body['original_event']['artifact']['sha256']='0'*64
        elif attack=='old_run':body['original_event']['run_id']='old-run'
        elif attack=='old_segment':body['original_event']['segment_id']='old-segment'
        elif attack=='old_attempt':body['original_event']['attempt_id']='old-attempt'
        elif attack=='old_revision':body['review_tuple']['work_revision']=99
        elif attack=='old_config':body['contract']['binding']['mcp_config']['sha256']='0'*64
        elif attack=='old_session':body['session_id']='old-session'
    resign_fixture_receipt(prepared,path,edit)
    with pytest.raises((s.h.Refused,KeyError,ValueError)):
        s.finish(prepared,str(path),s.h.file_sha(path),integrate_private=True)
    assert s.h.snapshot(prepared['dispatch']['directory'])=={'head':prepared['base_oid'],'status':''}


def test_private_receipt_uid_is_required(prepared,tmp_path,monkeypatch):
    path,oid=ledger_fixture(prepared,tmp_path)
    actual_uid=os.getuid();monkeypatch.setattr(s.h.os,'getuid',lambda:actual_uid+1)
    with pytest.raises(s.h.Refused):s.h.private_read(path)


def test_real_seatbelt_candidate_cannot_read_sibling_signer(tmp_path):
    candidate=tmp_path/'candidate';candidate.mkdir();(candidate/'arithmetic').mkdir()
    signer=tmp_path/'validation-session.json';signer.write_text('PUBLIC_FIXTURE_SIGNER_MUST_NOT_BE_READ');signer.chmod(0o600)
    (candidate/'arithmetic/__init__.py').write_text('from .ops import add\n')
    (candidate/'calc.py').write_text('from arithmetic import add\ndef sum_values(values):return sum(values)\n')
    (candidate/'arithmetic/ops.py').write_text('open('+repr(str(signer))+').read()\ndef add(a,b):return a+b\n')
    result=s.h.sandbox_candidate(candidate,PYTHON,tmp_path)
    assert result['exit_code']!=0
    assert 'PUBLIC_FIXTURE_SIGNER_MUST_NOT_BE_READ' not in result['stdout']+result['stderr']


def test_duplicate_real_receipt_is_not_silently_reused(prepared,tmp_path):
    path,summary,oid=ready_checker(prepared,tmp_path);s.h.validation_session(prepared,create=True)
    result=s.h.validate_candidate(prepared,str(path),oid,str(path))
    first=Path(result['validation_evidence_path']).read_bytes()
    with pytest.raises(s.h.Refused,match='preoccupied'):s.h.validate_candidate(prepared,str(path),oid,str(path))
    assert Path(result['validation_evidence_path']).read_bytes()==first


def test_codex_build_accepts_only_verified_repeated_safe_disables(prepared,tmp_path):
    values=launch_inputs(prepared,tmp_path)
    features=tmp_path/'features.txt';features.write_text(''.join(name+' experimental true\n' for name in sorted(s.CODEX_DISABLE_FEATURES)))
    values['options']={'disable_features':sorted(s.CODEX_DISABLE_FEATURES),'feature_list_path':str(features),'feature_list_sha256':s.h.file_sha(features)}
    command,_,_=s.build_launch(**values)
    assert {command[i+1] for i,value in enumerate(command) if value=='--disable'}==s.CODEX_DISABLE_FEATURES
    assert '--enable' not in command


def test_execute_live_codex_requires_all_seven_disables_before_any_launch(prepared,tmp_path):
    fixture_binding(prepared,tmp_path,'pass\n');auth,body=preflight_fixture(prepared,tmp_path)
    contract=tmp_path/'bound.json';contract.write_text(json.dumps(prepared));run=tmp_path/'must-not-launch'
    result=subprocess.run([PYTHON,'-B',str(ROOT/'scripts/scenario.py'),'launch','--execute-live','--contract',str(contract),
       '--provider','codex','--cli-sha256','fixture-cli-sha','--run-dir',str(run),'--auth-preflight',str(auth),'--auth-preflight-sha256',s.h.file_sha(auth)],capture_output=True,text=True,timeout=5)
    assert result.returncode!=0 and 'parent_tools_not_isolated' in result.stderr
    assert not run.exists()


def test_claude_bare_option_is_refused(prepared,tmp_path):
    prepared['dispatch']['provider']='codex';prepared['dispatch']['model']='gpt-6-luna'
    values=launch_inputs(prepared,tmp_path);values['provider']='claude';values['options']={'bare':True}
    config=Path(prepared['binding']['mcp_config']['path']);config.write_text(json.dumps({'state_dir':prepared['state_dir'],'workspace':prepared['dispatch']['directory'],'providers':{'codex':'fixture-lock'}}));prepared['binding']['mcp_config']['sha256']=s.h.file_sha(config)
    with pytest.raises(s.h.Refused,match='unsupported_parent_argument'):
        s.build_launch(**values)


def test_exit_zero_without_frozen_checks_completion_cannot_pass(prepared,tmp_path):
    path,oid=ledger_fixture(prepared,tmp_path)
    def edit(body):
        import base64
        value=b''
        body['validation'].update(stdout='',stdout_bytes=0,stdout_sha256=s.h.sha(value),stdout_base64=base64.b64encode(value).decode())
    resign_fixture_receipt(prepared,path,edit)
    with pytest.raises(s.h.Refused,match='checks_not_completed'):
        s.finish(prepared,str(path),s.h.file_sha(path),integrate_private=True)
    assert s.h.git(prepared['dispatch']['directory'],'rev-parse','HEAD')==prepared['base_oid']


def revision_checker_fixture(prepared,tmp_path,attack=None):
    import copy
    handle,summary1,pages1,oid1=evidence(prepared)
    repo=Path(prepared['dispatch']['directory'])
    s.h.git(repo,'checkout','-q',prepared['base_oid'] if attack=='fork' else oid1)
    if attack=='fork':
        from test_t12_harness import GOOD
        for name,content in GOOD.items():
            path=repo/name;path.parent.mkdir(exist_ok=True);path.write_text(content)
    (repo/'arithmetic/ops.py').write_text('def add(a,b):\n    return sum((a,b))\n')
    if attack=='missing':(repo/'arithmetic/__init__.py').unlink()
    if attack=='outside':(repo/'outside.txt').write_text('fixture unauthorized file')
    s.h.git(repo,'add','--all');s.h.git(repo,'commit','-qm','fixture revision two changes one declared file')
    oid2=s.h.git(repo,'rev-parse','HEAD');tree=s.h.git(repo,'rev-parse','HEAD^{tree}')
    revision_paths=s.h.git(repo,'diff','--name-only',oid1,oid2).splitlines()
    s.h.git(repo,'checkout','-q',prepared['base_oid'])
    candidate={'repo_root':str(repo),'base_oid':prepared['base_oid'] if attack=='mislineage' else oid1,'candidate_oid':oid2,'tree_oid':tree,
               'binding':{'run_id':'r','task_id':'t','segment_id':'s2','work_revision':2,'attempt_id':'a2'}}
    artifact=Path(prepared['state_dir'])/'revision2-artifact.json';artifact.write_text(json.dumps({'candidate':candidate}));artifact.chmod(0o600)
    payload=s.h.sha(artifact.read_bytes())
    event={'run_id':'r','task_id':'t','attempt_id':'a2','kind':'result','event_id':'e2','event_revision':2,'payload_hash':payload,'action_slot':'slot2','work_revision':2,'segment_id':'s2',
           'artifact':{'path':str(artifact),'size':artifact.stat().st_size,'sha256':payload}}
    binding={'candidate_oid':oid2,'event_id':'e2','event_revision':2,'event_hash':payload,'action_slot':'slot2','work_revision':2,'segment_id':'s2'}
    summary2={'run_id':'r','tasks':[{'task_id':'t','active_segments':0,'reviewable_result':binding}]}
    phase=tmp_path/'fixture-phase.json';phase.write_text('1')
    fixture_binding(prepared,tmp_path,'''import sys,json
from pathlib import Path
phase=json.loads(Path('''+repr(str(phase))+''').read_text())
if sys.argv[2]=='inspect':print('''+repr(json.dumps(summary1))+''' if phase==1 else '''+repr(json.dumps(summary2))+''')
elif sys.argv[2]=='collect':print('''+repr(json.dumps(pages1[0]))+''' if phase==1 else '''+repr(json.dumps({'events':[event]}))+''')
else:raise SystemExit('fixture forbids business mutation')
''')
    handle_path=Path(prepared['state_dir'])/'fixture-handle.json';handle_path.write_text(json.dumps(handle));handle_path.chmod(0o600)
    dbpath=Path(prepared['state_dir'])/'state.db';db=sqlite3.connect(dbpath)
    db.executescript('CREATE TABLE runtime_events(event_id,task_id,body_json,event_revision,action_slot);CREATE TABLE retry_decisions(task_id,work_revision,source_event_id,source_event_revision,source_event_hash,source_segment_id,next_attempt_no,action_slot,command_id);CREATE TABLE attempts(id,task_id,attempt_no);')
    old=pages1[0]['events'][0]
    db.execute('INSERT INTO runtime_events VALUES(?,?,?,?,?)',('e','t',json.dumps(old),1,'slot'))
    if attack!='missing_retry':db.execute('INSERT INTO retry_decisions VALUES(?,?,?,?,?,?,?,?,?)',('t',1,'e',1,'0'*64 if attack=='source_tuple' else old['payload_hash'],'s',2,'slot','fixture-request-changes'))
    db.execute('INSERT INTO attempts VALUES(?,?,?)',('a2','t',1 if attack=='attempt_no' else 2));db.commit();db.close();dbpath.chmod(0o600)
    s.h.validation_session(prepared,create=True)
    first=s.h.validate_candidate(prepared,str(handle_path),oid1,str(handle_path))
    assert first['validation']['exit_code']==0
    phase.write_text('2')
    if attack=='source_artifact':Path(old['artifact']['path']).write_text('{}')
    return handle_path,handle,event,binding,oid2,revision_paths


def test_real_revision_two_one_file_change_preserves_full_final_scope(prepared,tmp_path):
    path,handle,event,binding,oid,changed=revision_checker_fixture(prepared,tmp_path)
    assert changed==['arithmetic/ops.py']
    result=s.h.validate_candidate(prepared,str(path),oid,str(path))
    assert result['validation']['exit_code']==0
    assert result['frozen_base_oid']==prepared['base_oid']
    assert result['revision_base_oid']==result['lineage']['predecessor_candidate_oid']
    assert result['original_event']['attempt_id']=='a2' and result['review_tuple']['work_revision']==2
    receipt,canonical,digest=s.h.verify_validation_receipt(prepared,str(path),handle,binding,event)
    assert digest==result['validation_evidence_sha256'] and receipt['lineage']['retry_command_id']=='fixture-request-changes'
    assert {item['path'] for item in result['source_blobs']}==set(s.h.PATHS)
    assert s.h.snapshot(prepared['dispatch']['directory'])=={'head':prepared['base_oid'],'status':''}


@pytest.mark.parametrize('attack',['missing','outside','mislineage','source_tuple','source_artifact','missing_retry','attempt_no','fork'])
def test_revision_two_missing_outside_or_unproven_lineage_rejected(prepared,tmp_path,attack):
    path,handle,event,binding,oid,_=revision_checker_fixture(prepared,tmp_path,attack)
    with pytest.raises((s.h.Refused,subprocess.CalledProcessError)):
        s.h.validate_candidate(prepared,str(path),oid,str(path))
    assert s.h.snapshot(prepared['dispatch']['directory'])=={'head':prepared['base_oid'],'status':''}
