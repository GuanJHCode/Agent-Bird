"""Read-only audit of native-tui-04's stopped synthetic profile.

No RPC, Go command, native launch or claim/ACK mutation. The new receipt proves
persisted-record consistency, not a third recovery run or power-loss durability.
"""
from __future__ import annotations
import argparse,hashlib,json,os,re,stat,sys,time
from pathlib import Path
ROOT=Path(__file__).resolve().parents[3]
sys.path[:0]=[str(ROOT/'tasks/g0-completion/scripts'),str(ROOT/'tasks/g0-proxy-continuation/cold-start/scripts')]
from delivery_adapter import DeliveryBinding,native_envelope_from_go_intent
from activation_service import _original_process_gone


def require(value,code):
    if not value:raise ValueError(code)


def sha(raw):return hashlib.sha256(raw).hexdigest()
def canonical(value):return json.dumps(value,sort_keys=True,separators=(',',':'),ensure_ascii=False).encode()


def decode(raw):
    def pairs(items):
        value={}
        for key,item in items:
            require(key not in value,'duplicate_json_key');value[key]=item
        return value
    def constant(_):raise ValueError('nonfinite_json')
    return json.loads(raw,object_pairs_hook=pairs,parse_constant=constant)


class Sources:
    """Bounded files opened through anchored directory FDs, rechecked at end."""
    def __init__(self):self.files={};self.deadline=time.monotonic()+10
    def __enter__(self):return self
    def __exit__(self,*_):pass
    def read(self,path,*,private):
        require(time.monotonic()<self.deadline,'audit_deadline')
        path=Path(path);require(path.is_absolute() and path.resolve()==path,'source_alias')
        fd=os.open('/',os.O_RDONLY|os.O_DIRECTORY);directories=[]
        try:
            for name in path.parts[1:-1]:
                child=os.open(name,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW,dir_fd=fd)
                os.close(fd);fd=child;info=os.fstat(fd)
                directories.append((info.st_dev,info.st_ino,info.st_mode,info.st_uid))
            parent=os.fstat(fd)
            require(parent.st_uid==os.getuid() and not stat.S_IMODE(parent.st_mode)&0o022,'source_parent_unsafe')
            file_fd=os.open(path.name,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK,dir_fd=fd)
            try:
                before=os.fstat(file_fd)
                identity=lambda s:tuple(getattr(s,k) for k in ('st_dev','st_ino','st_mode','st_uid','st_nlink','st_size','st_mtime_ns','st_ctime_ns'))
                require(stat.S_ISREG(before.st_mode) and before.st_uid==os.getuid() and before.st_nlink==1
                    and (stat.S_IMODE(before.st_mode)==0o600 if private else not stat.S_IMODE(before.st_mode)&0o022)
                    and 0<before.st_size<=4*1024*1024,'source_file_unsafe')
                chunks=[];left=before.st_size
                while left:
                    chunk=os.read(file_fd,min(left,65536));require(chunk,'source_short_read');chunks.append(chunk);left-=len(chunk)
                require(not os.read(file_fd,1) and identity(before)==identity(os.fstat(file_fd)),'source_changed')
                require(identity(before)==identity(os.stat(path.name,dir_fd=fd,follow_symlinks=False)),'source_replaced')
                raw=b''.join(chunks);pin=(directories,identity(before),sha(raw))
                old=self.files.get(str(path));require(old is None or old['pin']==pin,'source_drift')
                self.files[str(path)]={'pin':pin,'private':private};return raw
            finally:os.close(file_fd)
        finally:os.close(fd)
    def json(self,path,*,private=True):return decode(self.read(path,private=private))
    def verify(self):
        for path,record in list(self.files.items()):self.read(Path(path),private=record['private'])
    def hashes(self):return {path:record['pin'][2] for path,record in self.files.items()}


def verify_rollout(raw,thread_id,workspace,turn_ids,envelope_text):
    require(raw.endswith(b'\n') and len(turn_ids)==2 and len(set(turn_ids))==2,'rollout_incomplete')
    lines=raw.splitlines();require(1<=len(lines)<=4096,'rollout_line_bound')
    rows=[decode(line) for line in lines]
    require(all(isinstance(row,dict) and isinstance(row.get('payload'),dict) for row in rows),'rollout_shape')
    metadata=[row['payload'] for row in rows if row.get('type')=='session_meta']
    require(len(metadata)==1 and rows[0].get('type')=='session_meta','rollout_metadata_count')
    meta=metadata[0]
    require(meta.get('id')==thread_id and meta.get('cwd')==workspace and meta.get('cli_version')=='0.154.0'
        and meta.get('history_mode')=='paginated','rollout_owner')
    active=None;started=[];completed=[];contexts=[];items=[];outputs=[]
    outer={'session_meta','event_msg','response_item','turn_context','world_state','token_usage_record'}
    for row in rows[1:]:
        kind=row.get('type');p=row['payload'];require(kind in outer and kind!='session_meta','rollout_unknown_record')
        if kind=='response_item':require(p.get('type') in {'message','function_call_output'},'rollout_unknown_response_item')
        if kind=='event_msg':
            typ=p.get('type')
            require(typ in {'task_started','task_complete','item_completed','token_count'},'rollout_unknown_event')
            if typ=='task_started':
                require(active is None and len(started)<2 and p.get('turn_id')==turn_ids[len(started)],'rollout_turn_order')
                active=p['turn_id'];started.append(active)
            elif typ=='task_complete':
                require(active is not None and p.get('turn_id')==active and p.get('last_agent_message')==('READY' if len(completed)==0 else 'SYNTHETIC_COMPLETE'),'rollout_completion')
                completed.append(active);active=None
            elif typ=='item_completed':
                require(active is not None and p.get('thread_id')==thread_id and p.get('turn_id')==active,'rollout_item_owner')
                item=p.get('item');require(isinstance(item,dict) and isinstance(item.get('id'),str),'rollout_item')
                items.append((active,item))
        elif kind=='turn_context':
            require(active is not None and p.get('turn_id')==active and p.get('cwd')==workspace,'rollout_context')
            contexts.append(active)
        elif kind=='response_item' and p.get('type')=='function_call_output':
            require(active==turn_ids[1],'rollout_output_turn');outputs.append(p)
    require(active is None and started==completed==contexts==turn_ids,'rollout_incomplete_turns')
    require(len(items)==4 and [turn for turn,_ in items]==[turn_ids[0]]*2+[turn_ids[1]]*2,'rollout_item_chain')
    require(len({item['id'] for _,item in items})==4,'rollout_duplicate_item')
    user,first,tool,last=[item for _,item in items]
    require(user.get('type')=='UserMessage' and user.get('content')==[{'type':'text','text':'G0_SYNTHETIC_READY','text_elements':[]}],'rollout_initial_input')
    for item,text in ((first,'READY'),(last,'SYNTHETIC_COMPLETE')):
        require(item.get('type')=='AgentMessage' and item.get('content')==[{'type':'Text','text':text}],'rollout_agent_output')
    require(tool.get('type')=='FunctionCallOutput' and len(outputs)==1,'rollout_tool_count')
    for item in [tool,*outputs]:
        require(item.get('name')=='g0_delivery' and item.get('namespace')=='orchestration' and item.get('output')==envelope_text,'rollout_exact_envelope')
    return {'turn_ids':turn_ids,'complete_turns':2,'completed_items':4,'item_ids':[item['id'] for _,item in items],
        'exact_tool_output_records':2,'envelope_sha256':sha(envelope_text.encode()),'rollout_sha256':sha(raw),'rollout_bytes':len(raw)}


def verify_go(result,artifacts):
    intent=artifacts['batch-intent.json'];proof=result['helper_proof'];lease=result['resumed_lease']
    binding=DeliveryBinding(lease['profile_id'],result['tui_thread_id'],lease['owner_epoch'],'native-tui-04',1)
    envelope=native_envelope_from_go_intent(intent,binding)
    require(sha(canonical(intent))==proof['go_intent_sha256'] and sha(canonical(envelope))==proof['native_envelope_sha256'],'original_proof_envelope')
    require(len(intent['events'])==1 and intent['events'][0]['event_id']=='event_result','single_event_required')
    event=intent['events'][0];ack=artifacts['batch-ack-event_result.json']
    require(canonical(ack)==canonical(result['ack']['records'][0]) and result['ack']['business_ack_complete'] is True
        and len(result['ack']['records'])==1 and ack['effect_count']==ack['decision_count']==1
        and ack['status']=='acknowledged' and ack['decision']=='handled','ack_record_mismatch')
    for key in ('delivery_id','nonce','controller_thread','controller_epoch','revision'):
        require(ack[key]==intent[key],'ack_owner_mismatch')
    require(ack['event_hash']==event['payload_hash'] and ack['event_id']==event['event_id'] and ack['event_revision']==event['event_revision'] and ack['action_slot']==event['action_slot'],'ack_event_mismatch')
    require(artifacts['batch-send-claim.json']=={'version':1,'delivery_id':intent['delivery_id'],'nonce':intent['nonce'],'controller_thread':intent['controller_thread'],'revision':1,'status':'claimed'},'claim_mismatch')
    require(artifacts['delivery-owner-binding.json']=={'version':1,'owner_context_sha256':lease['owner_context_sha256'],'nonce':intent['nonce'],'controller_thread':intent['controller_thread'],'revision':1},'context_mismatch')
    job=artifacts['job.json'];go_result=artifacts['result.json']
    require(go_result=={'version':1,'status':'completed','nonce':intent['nonce'],'count':1} and job['controller_thread']==intent['controller_thread'] and job['task_revision']==1,'producer_result_mismatch')
    event_envelope={'version':1,'nonce':intent['nonce'],'controller_thread':intent['controller_thread'],'task_revision':job['task_revision'],
        'result':{'version':1,'status':'completed','nonce':intent['nonce'],'count':1}}
    require(sha(json.dumps(event_envelope,separators=(',',':')).encode())==event['payload_hash'],'producer_hash_mismatch')
    require(artifacts['control.json']=={'version':1,'nonce':intent['nonce'],'controller_thread':intent['controller_thread'],'revision':1,'cancelled':False},'control_changed')
    require(result['go_status']['ack_count']==1 and result['go_status']['send_allowed'] is False,'original_ack_incomplete')
    return canonical(envelope).decode()


def audit_case(case_dir,result_sha,orchestration_sha):
    case_dir=Path(case_dir);require(case_dir.name=='native-tui-04','only_approved_synthetic_case')
    with Sources() as sources:
        raw=sources.read(case_dir/'result.json',private=False);require(sha(raw)==result_sha,'case_result_pin');result=decode(raw)
        raw=sources.read(case_dir/'orchestration.json',private=False);require(sha(raw)==orchestration_sha,'case_orchestration_pin');origin=decode(raw)
        require(result['status']=='passed' and result['ordinary_tui_verified'] is True and result['business_ack_verified'] is True
            and result['external_model_calls']==0 and result['cleanup']['tui_exits']==[0,0] and result['cleanup']['helper_exit']==0,'case_not_complete')
        profile=Path(origin['profile_root']);runtime=Path(origin['runtime_case'])
        require(profile.parent==Path('/private/tmp') and re.fullmatch(r'g0-auth-tuir-[a-z0-9_]+',profile.name)
            and origin['case']=='native-tui-04' and runtime==profile/'native-tui-04','synthetic_profile_scope')
        thread=result['tui_thread_id'];require(result['resumed_thread_id']==thread,'resumed_thread_changed')
        peers=[]
        for index,lease in enumerate((result['first_lease'],result['resumed_lease'])):
            matches=list((runtime/'state').glob('*/backend-'+lease['lease_id']+'.json'));require(len(matches)==1,'backend_receipt_count')
            final=sources.json(matches[0]);service=sources.json(matches[0].parent/'service-start.json')['service_identity']
            require(service==lease['service_identity'] and final['lease_id']==lease['lease_id'] and final['owner_thread_id']==thread
                and final['owner_context_sha256']==lease['owner_context_sha256'] and final['pid']==lease['backend_pid'] and final['birth']==lease['backend_birth']
                and final['state']=='closed' and final['process_stopped'] is True and final['socket_removed'] is True
                and final['guardian']['child_reaped'] is True and final['guardian']['state']=='stopped','backend_not_stopped')
            require(final['frontend']['pid']==result['tui_sessions'][index]['frontend']['pid'] and final['frontend']['birth']==result['tui_sessions'][index]['frontend']['birth'],'frontend_changed')
            helpers=final.get('helpers',[]);require(len(helpers)==index and all(h['state']=='closed' and h['lease_id']==lease['lease_id'] for h in helpers),'helper_not_closed')
            require(final['private_socket']==lease['private_socket'] and not os.path.lexists(final['private_socket']),'private_socket_remains')
            peers.extend([service,final['frontend'],dict(pid=final['pid'],birth=final['birth'],executable=final['executable']),*[h['frontend'] for h in helpers]])
        for peer in peers:require(_original_process_gone(peer['pid'],peer['birth'],peer['executable']),'original_process_remains')
        task=profile/'w/go-task';names=('batch-intent.json','batch-send-claim.json','batch-ack-event_result.json','delivery-owner-binding.json','control.json','job.json','result.json')
        artifacts={name:sources.json(task/name) for name in names}
        require(len(list(task.glob('batch-ack-*.json')))==1,'ack_file_count')
        require(sources.hashes()[str(task/'batch-send-claim.json')]==result['helper_proof']['claim_sha256'],'original_claim_pin')
        envelope=verify_go(result,artifacts)
        paths=list((profile/'c/sessions').glob('*/*/*/*.jsonl'));require(len(paths)==1,'rollout_count')
        turns=[result['helper_proof'][key] for key in ('initial_turn_id','delivery_turn_id')]
        verified=verify_rollout(sources.read(paths[0],private=True),thread,str(profile/'w'),turns,envelope)
        for source in (Path(__file__),ROOT/'tasks/g0-completion/scripts/delivery_adapter.py',
                ROOT/'tasks/g0-proxy-continuation/cold-start/scripts/activation_service.py',ROOT/'tasks/g0-tui-proxy/scripts/proxy_transport.py'):
            sources.read(source,private=False)
        sources.verify()
        for peer in peers:require(_original_process_gone(peer['pid'],peer['birth'],peer['executable']),'original_process_reappeared')
        require(time.monotonic()<sources.deadline,'audit_deadline')
        return {'version':1,'status':'passed','case':'native-tui-04','scope':'stopped_native_persisted_records',
            'durable_history_verified':True,'third_resume_exercised':False,'power_loss_durability_verified':False,
            'native_started':False,'rpc_calls':0,'go_commands':0,'new_claim_send_ack':False,'thread_id':thread,
            'ack_count':1,'effect_count':1,'original_identities_gone':len(peers),'history':verified,
            'input_sha256':sources.hashes(),'audit_source_sha256':sha(Path(__file__).read_bytes())}


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    for name in ('case-dir','result-sha256','orchestration-sha256','output-dir'):parser.add_argument('--'+name,required=True)
    args=parser.parse_args();out=Path(args.output_dir)
    require(out.is_absolute() and out.resolve()==out and out.parent==ROOT/'tasks/g1-g4-delivery/data'
        and re.fullmatch(r'durable-history-audit(?:-[a-z0-9]+)?',out.name),'audit_output_scope')
    out.mkdir(mode=0o700)
    try:report=audit_case(Path(args.case_dir),args.result_sha256,args.orchestration_sha256)
    except Exception as exc:report={'version':1,'status':'unknown','durable_history_verified':False,'failure_type':type(exc).__name__,'message_sha256':sha(str(exc).encode())}
    fd=os.open(out/'receipt.json',os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600)
    try:
        data=canonical(report)+b'\n';offset=0
        while offset<len(data):offset+=os.write(fd,data[offset:])
        os.fsync(fd)
    finally:os.close(fd)
    print(json.dumps({'status':report['status'],'durable_history_verified':report['durable_history_verified']}))
    return 0 if report['status']=='passed' else 1


if __name__=='__main__':raise SystemExit(main())
