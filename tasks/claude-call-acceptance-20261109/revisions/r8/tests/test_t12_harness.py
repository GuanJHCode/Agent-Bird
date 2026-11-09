import copy
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys

import pytest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('harness', ROOT/'scripts/t12_harness.py')
h = importlib.util.module_from_spec(spec)
spec.loader.exec_module(h)
PYTHON = '/private/tmp/agent-bird-cross-cli-5c9jyn8o/python-venv/bin/python'
GOOD = {'calc.py': 'from arithmetic import add\ndef sum_values(values):\n    total=0\n    for value in values:\n        total=add(total,value)\n    return total\n',
        'arithmetic/__init__.py':'from .ops import add\n', 'arithmetic/ops.py':'def add(a,b):\n    return a+b\n'}

@pytest.fixture
def prepared(tmp_path):
    h.prepare(tmp_path/'prepared')
    area = tmp_path/'prepared/codex-to-claude'
    return json.loads((area/'contract.json').read_text())

def request(contract):
    return {'jsonrpc':'2.0','id':1,'method':'tools/call','params':{'name':'dispatch','arguments':copy.deepcopy(contract['dispatch'])}}

def test_identical_retry_forwarded_twice(prepared):
    p=h.Policy(prepared)
    forwarded=[]
    for _ in range(2):
        message=request(prepared);p.check(message);forwarded.append(message)
    assert len(forwarded)==2 and forwarded[0]==forwarded[1]

@pytest.mark.parametrize('field,value', [('request_id','new-id'),('model','expensive'),('reasoning','high'),('prompt','new brief'),('paths',['calc.py']),('acceptance',['weaker']),('provider','codex'),('directory','/tmp')])
def test_policy_rejects_changed_before_forward(prepared,field,value):
    message=request(prepared);message['params']['arguments'][field]=value
    forwarded=[]
    with pytest.raises(h.Refused):
        h.Policy(prepared).check(message);forwarded.append(message)
    assert forwarded==[]

@pytest.mark.parametrize('field,value',[('per_segment_ms',90001),('total_ms',90001),('max_attempts',3),('group_id','fresh')])
def test_policy_budget_before_forward(prepared,field,value):
    message=request(prepared);message['params']['arguments']['budget'][field]=value
    with pytest.raises(h.Refused): h.Policy(prepared).check(message)


def evidence(contract):
    repo=Path(contract['dispatch']['directory'])
    for name,source in GOOD.items():
        target=repo/name;target.parent.mkdir(exist_ok=True);target.write_text(source)
    h.git(repo,'add','--',*h.PATHS);h.git(repo,'commit','-qm','fixture candidate')
    oid=h.git(repo,'rev-parse','HEAD');tree=h.git(repo,'rev-parse','HEAD^{tree}')
    h.git(repo,'checkout','-q',contract['base_oid'])
    candidate={'repo_root':str(repo),'base_oid':contract['base_oid'],'candidate_oid':oid,'tree_oid':tree, 'binding':{'run_id':'r','task_id':'t','segment_id':'s','work_revision':1,'attempt_id':'a'}}
    artifact=Path(contract['state_dir'])/'fixture-artifact.json'
    artifact.write_text(json.dumps({'candidate':candidate}));artifact.chmod(0o600);body=artifact.read_bytes()
    binding={'candidate_oid':oid,'event_id':'e','event_revision':1,'event_hash':h.sha(body),'action_slot':'slot','work_revision':1,'segment_id':'s'}
    event={'run_id':'r','task_id':'t','attempt_id':'a','kind':'result','event_id':'e','event_revision':1,'payload_hash':h.sha(body),'action_slot':'slot','work_revision':1,'segment_id':'s',
           'artifact':{'path':str(artifact),'size':len(body),'sha256':h.sha(body)}}
    handle={'state_dir':contract['state_dir'],'run_id':'r','task_ids':['t']}
    summary={'run_id':'r','tasks':[{'task_id':'t','active_segments':0,'reviewable_result':binding}]}
    return handle,summary,[{'events':[event]}],oid

def test_evidence_valid_binds_oid_tree_artifact_tuple(prepared):
    handle,summary,pages,oid=evidence(prepared)
    candidate,binding,event=h.verify_evidence(prepared,handle,summary,pages,oid)
    assert candidate['candidate_oid']==oid
    assert h.snapshot(prepared['dispatch']['directory'])=={'head':prepared['base_oid'],'status':''}

@pytest.mark.parametrize('attack',['oid','sha','tuple','tree','run','artifact_scope','active'])
def test_evidence_rejects_tamper(prepared,attack):
    handle,summary,pages,oid=evidence(prepared)
    event=pages[0]['events'][0]
    if attack=='oid': oid='0'*40
    if attack=='sha': event['artifact']['sha256']='0'*64
    if attack=='tuple': event['event_revision']=2
    if attack=='tree':
        artifact=Path(event['artifact']['path']);body=json.loads(artifact.read_text());body['candidate']['tree_oid']='0'*40
        artifact.write_text(json.dumps(body));data=artifact.read_bytes()
        event['artifact'].update(size=len(data),sha256=h.sha(data));event['payload_hash']=h.sha(data)
        summary['tasks'][0]['reviewable_result']['event_hash']=h.sha(data)
    if attack=='run': handle['run_id']='other'
    if attack=='artifact_scope': event['artifact']['path']='/etc/passwd'
    if attack=='active': summary['tasks'][0]['active_segments']=1
    with pytest.raises((h.Refused,subprocess.CalledProcessError)): h.verify_evidence(prepared,handle,summary,pages,oid)

@pytest.fixture
def candidate(tmp_path):
    target=tmp_path/'candidate';target.mkdir()
    for name,source in GOOD.items():
        path=target/name;path.parent.mkdir(exist_ok=True);path.write_text(source)
    return target

@pytest.mark.skipif(sys.platform!='darwin',reason='real Seatbelt requires macOS')
def test_seatbelt_good(candidate,tmp_path):
    before={p.relative_to(candidate).as_posix():h.file_sha(p) for p in candidate.rglob('*.py')}
    result=h.sandbox_candidate(candidate,PYTHON,tmp_path)
    assert result['exit_code']==0,result
    assert result['stdout_bytes']>0
    assert before=={p.relative_to(candidate).as_posix():h.file_sha(p) for p in candidate.rglob('*.py')}

@pytest.mark.parametrize('attack', ['read','write','network','fork','exec','process_info','mach','replace_tests','replace_candidate'])
@pytest.mark.skipif(sys.platform!='darwin',reason='real Seatbelt requires macOS')
def test_seatbelt_real_denials(candidate,tmp_path,attack):
    sentinel=tmp_path/'outside-sentinel';sentinel.write_text('private-fixture-sentinel')
    operations={
      'read':f'open({str(sentinel)!r}).read()',
      'write':f'open({str(sentinel)!r},"w").write("changed")',
      'network':'__import__("socket").socket().connect(("127.0.0.1",9))',
      'fork':'__import__("os").fork()',
      'exec':'__import__("os").execv("/usr/bin/true",["true"])',
      'process_info':'''import ctypes, os
lib=ctypes.CDLL('/usr/lib/libproc.dylib')
buf=ctypes.create_string_buffer(4096)
assert lib.proc_pidpath(os.getpid(),buf,4096)>0, 'process_info_denied'
''',
      'mach':'''import ctypes
lib=ctypes.CDLL('/usr/lib/libSystem.B.dylib')
port=ctypes.c_uint()
assert lib.bootstrap_look_up(ctypes.c_uint.in_dll(lib,'bootstrap_port').value,b'com.apple.cfprefsd.daemon',ctypes.byref(port))==0, 'mach_denied'
''',
      'replace_tests':'import sys\nopen(sys.argv[2],"w").write("print(123)")',
      'replace_candidate':f'open({str(candidate/"calc.py")!r},"w").write("pass")'}
    (candidate/'arithmetic/ops.py').write_text(operations[attack]+'\ndef add(a,b): return a+b\n')
    result=h.sandbox_candidate(candidate,PYTHON,tmp_path)
    assert result['exit_code']!=0,result
    assert sentinel.read_text()=='private-fixture-sentinel'

@pytest.mark.parametrize('source,timeout,cpu', [('while True: pass',3,1),('while True: print("x"*10000)',3,1),('__import__("time").sleep(10)',0.2,2)])
@pytest.mark.skipif(sys.platform!='darwin',reason='real Seatbelt requires macOS')
def test_seatbelt_output_cpu_wall_bounded(candidate,tmp_path,source,timeout,cpu):
    (candidate/'arithmetic/ops.py').write_text(source+'\ndef add(a,b): return a+b\n')
    result=h.sandbox_candidate(candidate,PYTHON,tmp_path,timeout=timeout,cpu=cpu)
    assert result['exit_code']!=0,result
    assert result['stdout_bytes']<=1024*1024
    assert len(result['stdout'])<=65536


def test_unbound_cannot_live_or_validate(prepared):
    with pytest.raises((h.Refused,KeyError)): h.require_binding(prepared)
    with pytest.raises(h.Refused,match='execute_live_required'): h.proxy(prepared,[],False)


def test_checker_and_policy_have_no_business_mutations():
    import inspect
    source=inspect.getsource(h.validate_candidate)
    assert "public('inspect')" in source and "public('collect'" in source
    for action in ['accept','ack','request-changes','dispatch','submit','run','stop']:
        assert f"public('{action}'" not in source


def fixture_binding(prepared,tmp_path,body):
    installed=tmp_path/'fixture-installed-root'
    (installed/'scripts').mkdir(parents=True,exist_ok=True)
    (installed/'bin').mkdir(exist_ok=True)
    binary=installed/'bin/codex-orchestrator';binary.write_text('FIXTURE ONLY\n');binary.chmod(0o700)
    wrapper=installed/'scripts/invoke.sh'
    wrapper.write_text('#!'+PYTHON+'\n'+body)
    wrapper.chmod(0o700)
    package=tmp_path/'fixture-package';package.write_text('FIXTURE ONLY; no production package')
    configdir=tmp_path/'fixture-mcp-config';configdir.mkdir(exist_ok=True)
    configuration=configdir/'mcp_config.json'
    configuration.write_text(json.dumps({'state_dir':prepared['state_dir'],'workspace':prepared['dispatch']['directory'],'providers':{'claude':'fixture-lock'}}))
    codex_args=configuration.with_name('codex-args.json')
    env_names=['AGENT_BIRD_OWNER_REQUEST','FIXTURE_AUTH_TOKEN']
    generated=['-c','mcp_servers.agent_bird.command='+json.dumps(str(binary)),
               '-c','mcp_servers.agent_bird.args='+json.dumps(['mcp','serve','--config',str(configuration)]),
               '-c','mcp_servers.agent_bird.env_vars='+json.dumps(env_names)]
    codex_args.write_text(json.dumps(generated))
    prepared['binding']={key:{'path':str(Path(path).resolve()),'sha256':h.file_sha(path)} for key,path in [('wrapper',wrapper),('package',package),('python',PYTHON),('mcp_config',configuration),('codex_args',codex_args),('installed_binary',binary)]}
    return wrapper


def test_checker_public_cli_full_path_no_decisions(prepared,tmp_path):
    handle,summary,pages,oid=evidence(prepared)
    log=tmp_path/'public-calls.jsonl'
    wrapper=fixture_binding(prepared,tmp_path, '''import sys,json
from pathlib import Path
with open('''+repr(str(log))+''','a') as out: out.write(json.dumps(sys.argv[1:])+'\\n')
action=sys.argv[2]
if action=='inspect': print('''+repr(json.dumps(summary))+''')
elif action=='collect': print('''+repr(json.dumps(pages[0]))+''')
else: raise SystemExit('unexpected fixture mutation')
''')
    path=Path(prepared['state_dir'])/'fixture-handle.json';path.write_text(json.dumps(handle));path.chmod(0o600)
    h.validation_session(prepared,create=True)
    result=h.validate_candidate(prepared,str(path),oid,str(path))
    assert result['candidate_oid']==oid and result['validation']['exit_code']==0
    calls=[json.loads(line) for line in log.read_text().splitlines()]
    assert [call[:2] for call in calls]==[['task','inspect'],['task','collect'],['task','inspect']]
    assert result['workspace_before']==result['workspace_after']


def test_stdio_proxy_transparent_and_guarded(prepared,tmp_path):
    log=tmp_path/'backend-messages.jsonl'
    wrapper=fixture_binding(prepared,tmp_path,'''import sys,json
for line in sys.stdin:
    value=json.loads(line)
    with open('''+repr(str(log))+''','a') as out: out.write(line)
    if 'id' not in value: continue
    if value.get('method')=='tools/list':
        result={'tools':[{'name':name,'inputSchema':{'fixture':'unchanged'}} for name in ['dispatch','inspect','wait','collect','request_changes','accept','ack','stop']]}
    elif value.get('method')=='initialize': result={'protocolVersion':'2024-11-05','capabilities':{'tools':{}}}
    else: result={'echo':value}
    print(json.dumps({'jsonrpc':'2.0','id':value['id'],'result':result}),flush=True)
''')
    contract=tmp_path/'bound-fixture-contract.json';contract.write_text(json.dumps(prepared))
    good=request(prepared)
    retry=copy.deepcopy(good);retry['id']=2
    bad=copy.deepcopy(good);bad['id']=3;bad['params']['arguments']['request_id']='unauthorized'
    messages=[{'jsonrpc':'2.0','id':10,'method':'initialize','params':{'protocolVersion':'2024-11-05','capabilities':{},'clientInfo':{'name':'fixture','version':'1'}}},
              {'jsonrpc':'2.0','method':'notifications/initialized'}, {'jsonrpc':'2.0','id':11,'method':'tools/list'},good,retry,bad,
              {'jsonrpc':'2.0','method':'notifications/cancelled','params':{'requestId':99,'reason':'fixture'}}]
    process=subprocess.run([PYTHON,'-B',str(ROOT/'scripts/t12_harness.py'),'proxy','--contract',str(contract),'--execute-live','--',str(wrapper),'mcp','serve','--config',prepared['binding']['mcp_config']['path']],
                           input=''.join(json.dumps(m)+'\n' for m in messages),stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,timeout=10)
    assert process.returncode==0,process.stderr
    responses=[json.loads(line) for line in process.stdout.splitlines()]
    assert len(responses)==5,responses
    by_id={r['id']:r for r in responses}
    assert by_id[3]['error']['message']=='dispatch_differs_from_frozen_contract'
    listed=by_id[11]['result']['tools']
    assert [t['name'] for t in listed][-1]=='fixture_validate_candidate'
    assert all(t['inputSchema']=={'fixture':'unchanged'} for t in listed[:8])
    forwarded=[json.loads(line) for line in log.read_text().splitlines()]
    assert forwarded==[m for m in messages if m is not bad]
    # Exactly caller-provided calls: no generated accept/ACK/rework/resubmit.
    assert len([m for m in forwarded if m.get('params',{}).get('name')=='dispatch'])==2


def test_prepare_cli_default_never_calls_provider(tmp_path):
    result=subprocess.run([PYTHON,'-B',str(ROOT/'scripts/t12_harness.py'),'prepare','--root',str(tmp_path/'prepared')],capture_output=True,text=True,timeout=10)
    assert result.returncode==0,result.stderr
    for name in ['codex-to-claude','claude-to-codex']:
        contract=json.loads((tmp_path/'prepared'/name/'contract.json').read_text())
        assert contract['status']=='PREPARED_NOT_LIVE' and contract['binding']=={}
        assert h.snapshot(contract['dispatch']['directory'])=={'head':contract['base_oid'],'status':''}

@pytest.mark.parametrize('attack',['package','wrapper','python','mcp_config','codex_args','scope'])
def test_binding_rejects_changed_material(prepared,tmp_path,attack):
    fixture_binding(prepared,tmp_path,'pass\n')
    h.require_binding(prepared)
    if attack=='scope':
        prepared['state_dir']=str(tmp_path/'other-state')
    else:
        prepared['binding'][attack]['sha256']='0'*64
    with pytest.raises(h.Refused): h.require_binding(prepared)


def test_bind_cli_preserves_frozen_contract(prepared,tmp_path):
    fixture_binding(prepared,tmp_path,'pass\n')
    source=tmp_path/'unbound.json';original=copy.deepcopy(prepared);original['binding']={};source.write_text(json.dumps(original))
    output=tmp_path/'bound.json'
    command=[PYTHON,'-B',str(ROOT/'scripts/t12_harness.py'),'bind','--contract',str(source),'--output',str(output)]
    for key,value in prepared['binding'].items():
        if key not in ('codex_args','installed_binary'): command += ['--'+key.replace('_','-'),value['path']]
    result=subprocess.run(command,capture_output=True,text=True,timeout=10)
    assert result.returncode==0,result.stderr
    bound=json.loads(output.read_text())
    assert bound['dispatch']==original['dispatch'] and bound['base_oid']==original['base_oid']
    assert output.stat().st_mode & 0o777==0o600
    assert json.loads(source.read_text())==original
