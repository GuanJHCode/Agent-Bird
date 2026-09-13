"""Small synthetic files for the stopped-profile audit; no native or model."""
import hashlib,json,os,subprocess,sys,tempfile
from pathlib import Path
import pytest

SCRIPT=Path(__file__).resolve().parents[1]/'scripts/native_durable_history.py'


def records():
    rows=[{'type':'session_meta','payload':{'id':'thread','cwd':'/synthetic/work','cli_version':'0.154.0','history_mode':'paginated'}}]
    envelope='{"fixed":"result"}'
    for turn_id,text,item in (
        ('turn_1','READY',{'type':'UserMessage','id':'u1','content':[{'type':'text','text':'G0_SYNTHETIC_READY','text_elements':[]}]}),
        ('turn_2','SYNTHETIC_COMPLETE',{'type':'FunctionCallOutput','id':'f1','name':'g0_delivery','namespace':'orchestration','output':envelope}),
    ):
        rows.append({'type':'event_msg','payload':{'type':'task_started','turn_id':turn_id}})
        rows.append({'type':'turn_context','payload':{'turn_id':turn_id,'cwd':'/synthetic/work'}})
        if turn_id=='turn_2':rows.append({'type':'response_item','payload':{'type':'function_call_output','name':'g0_delivery','namespace':'orchestration','output':envelope}})
        for value in (item,{'type':'AgentMessage','id':'m'+turn_id,'content':[{'type':'Text','text':text}]}):
            rows.append({'type':'event_msg','payload':{'type':'item_completed','thread_id':'thread','turn_id':turn_id,'item':value}})
        rows.append({'type':'event_msg','payload':{'type':'task_complete','turn_id':turn_id,'last_agent_message':text}})
    return rows,envelope


def module():
    assert SCRIPT.exists(),'stopped-profile durable audit not implemented'
    sys.path.insert(0,str(SCRIPT.parent))
    import native_durable_history
    return native_durable_history


@pytest.mark.parametrize('fault',[None,'truncated','envelope','third_turn','wrong_thread'])
def test_persisted_complete_chain_rejects_truncation_or_drift(tmp_path,fault):
    audit=module();rows,envelope=records()
    if fault=='envelope':rows[-4]['payload']['output']='changed'
    if fault=='third_turn':rows.append({'type':'event_msg','payload':{'type':'task_started','turn_id':'turn_3'}})
    if fault=='wrong_thread':rows[0]['payload']['id']='other'
    raw=b''.join((json.dumps(row)+'\n').encode() for row in rows)
    if fault=='truncated':raw=raw[:-2]
    path=tmp_path/'rollout.jsonl';path.write_bytes(raw);path.chmod(0o600)
    with audit.Sources() as sources:
        contents=sources.read(path.resolve(),private=True)
        if fault:
            with pytest.raises(ValueError):audit.verify_rollout(contents,'thread','/synthetic/work',['turn_1','turn_2'],envelope)
        else:
            result=audit.verify_rollout(contents,'thread','/synthetic/work',['turn_1','turn_2'],envelope)
            assert result['complete_turns']==2 and result['completed_items']==4
            assert result['exact_tool_output_records']==2
            assert result['turn_ids']==['turn_1','turn_2']
            sources.verify()


def test_stable_sources_reject_parent_alias_and_changed_file(tmp_path):
    audit=module();path=(tmp_path/'original').resolve();path.mkdir(mode=0o700)
    file=path/'r.json';file.write_bytes(b'{}\n');file.chmod(0o600)
    alias=tmp_path/'alias';alias.symlink_to(path,target_is_directory=True)
    with audit.Sources() as sources:
        with pytest.raises(ValueError):sources.read(alias/'r.json',private=True)
        sources.read(file,private=True)
        file.write_bytes(b'{"changed":true}\n')
        with pytest.raises(ValueError):sources.verify()


def test_whole_stopped_synthetic_artifact_chain_is_read_only(tmp_path):
    audit=module()
    from proxy_transport import _process_metadata
    child=subprocess.Popen(['/bin/sleep','20'])
    try:birth,exe=_process_metadata(child.pid)
    finally:child.terminate();child.wait(timeout=2)
    assert birth and exe
    peer={'pid':child.pid,'birth':birth,'executable':exe,'uid':os.getuid()}
    # Literal fixture artifacts model the persisted format; this does not claim
    # a new real Go ACK. Production evidence comes from root's original case.
    def encoded(value):return json.dumps(value,sort_keys=True,separators=(',',':')).encode()
    def digest(raw):return hashlib.sha256(raw).hexdigest()
    def write(path,value):
        path.parent.mkdir(mode=0o700,parents=True,exist_ok=True)
        path.write_bytes(encoded(value));path.chmod(0o600)
    with tempfile.TemporaryDirectory(prefix='g0-auth-tuir-',dir='/private/tmp') as private:
        profile=Path(private);case=tmp_path.resolve()/'native-tui-04';runtime=profile/case.name
        nonce=case.name;thread='thread';delivery='G0_SYNTHETIC_TOOL_RESULT_'+nonce
        go_result={'version':1,'status':'completed','nonce':nonce,'count':1}
        event_data={'version':1,'nonce':nonce,'controller_thread':thread,'task_revision':1,'result':go_result}
        event_hash=digest(json.dumps(event_data,separators=(',',':')).encode())
        event={'event_id':'event_result','event_revision':1,'kind':'result','payload_hash':event_hash,'action_slot':'ack_result'}
        intent={'version':1,'delivery_id':delivery,'nonce':nonce,'controller_thread':thread,'controller_epoch':1,'revision':1,'state':'','events':[event],'payload_hash':''}
        intent['payload_hash']=digest(json.dumps(intent,separators=(',',':')).encode());intent['state']='prepared'
        envelope={'version':1,'delivery_id':delivery,'controller_thread_id':thread,'controller_epoch':1,'events':[event]}
        envelope['payload_hash']=digest(encoded(envelope));envelope_text=encoded(envelope).decode()
        ack={'version':1,'delivery_id':delivery,'nonce':nonce,'controller_thread':thread,'controller_epoch':1,'revision':1,
            'status':'acknowledged','decision':'handled','decision_count':1,'effect_count':1,'event_id':'event_result',
            'event_revision':1,'action_slot':'ack_result','event_hash':event_hash,'command_id':'handled_'+nonce}
        claim={'version':1,'delivery_id':delivery,'nonce':nonce,'controller_thread':thread,'revision':1,'status':'claimed'}
        artifacts={'batch-intent.json':intent,'batch-send-claim.json':claim,'batch-ack-event_result.json':ack,
            'delivery-owner-binding.json':{'version':1,'owner_context_sha256':'a'*64,'nonce':nonce,'controller_thread':thread,'revision':1},
            'control.json':{'version':1,'nonce':nonce,'controller_thread':thread,'revision':1,'cancelled':False},
            'job.json':{'controller_thread':thread,'task_revision':1},'result.json':go_result}
        for name,value in artifacts.items():write(profile/'w/go-task'/name,value)
        leases=[]
        for index in range(2):
            lease={'profile_id':'synthetic','lease_id':f'lease{index}','owner_epoch':1,'owner_context_sha256':'a'*64,
                'owner_thread_id':thread,'service_identity':peer,'backend_pid':peer['pid'],'backend_birth':birth,
                'private_socket':str(profile/f'absent{index}.sock')};leases.append(lease)
            folder=runtime/'state'/f'activation{index}'
            write(folder/'service-start.json',{'service_identity':peer})
            write(folder/f"backend-{lease['lease_id']}.json",{**lease,'pid':peer['pid'],'birth':birth,'executable':exe,
                'frontend':peer,'state':'closed','process_stopped':True,'socket_removed':True,
                'guardian':{'child_reaped':True,'state':'stopped'},
                'helpers':[] if index==0 else [{'frontend':peer,'state':'closed','lease_id':lease['lease_id']}]})
        result={'status':'passed','ordinary_tui_verified':True,'business_ack_verified':True,'external_model_calls':0,
            'cleanup':{'tui_exits':[0,0],'helper_exit':0},'tui_thread_id':thread,'resumed_thread_id':thread,
            'first_lease':leases[0],'resumed_lease':leases[1],'tui_sessions':[{'frontend':peer},{'frontend':peer}],
            'helper_proof':{'initial_turn_id':'turn_1','delivery_turn_id':'turn_2','go_intent_sha256':digest(encoded(intent)),
                'native_envelope_sha256':digest(encoded(envelope)),'claim_sha256':digest(encoded(claim))},
            'ack':{'business_ack_complete':True,'records':[ack]},'go_status':{'ack_count':1,'send_allowed':False}}
        origin={'case':nonce,'profile_root':str(profile),'runtime_case':str(runtime)}
        write(case/'result.json',result);write(case/'orchestration.json',origin)
        rows,_=records()
        for row in rows:
            p=row['payload']
            if 'cwd' in p:p['cwd']=str(profile/'w')
            if p.get('type')=='function_call_output':p['output']=envelope_text
            if p.get('type')=='item_completed' and p['item']['type']=='FunctionCallOutput':p['item']['output']=envelope_text
        rollout=profile/'c/sessions/2026/09/13/rollout.jsonl';rollout.parent.mkdir(mode=0o700,parents=True)
        rollout.write_bytes(b''.join(encoded(row)+b'\n' for row in rows));rollout.chmod(0o600)
        before={str(p):p.read_bytes() for root in (profile,case) for p in root.rglob('*') if p.is_file()}
        result_sha=digest(encoded(result));origin_sha=digest(encoded(origin))
        report=audit.audit_case(case,result_sha,origin_sha)
        assert report['durable_history_verified'] and report['ack_count']==report['effect_count']==1
        assert report['third_resume_exercised'] is False and report['rpc_calls']==report['go_commands']==0
        assert all(Path(path).read_bytes()==value for path,value in before.items())
        for count in (2,True):
            artifacts['batch-ack-event_result.json']['effect_count']=count
            write(profile/'w/go-task/batch-ack-event_result.json',artifacts['batch-ack-event_result.json'])
            with pytest.raises(ValueError,match='ack_record_mismatch'):
                audit.audit_case(case,result_sha,origin_sha)
