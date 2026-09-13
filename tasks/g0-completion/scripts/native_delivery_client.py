"""Pinned Python owner/helper processes for the private delivery experiment.

No native executable, real credential lookup, global config or LaunchAgent.
The helper keeps capability and history proof in its own adapter instance.
"""
from __future__ import annotations
import asyncio
from dataclasses import asdict
import hashlib
import json
import os
from pathlib import Path
import sys

ROOT=Path(__file__).resolve().parents[3]
for path in (ROOT/'tasks/g0-completion/scripts',ROOT/'tasks/g0-tui-proxy/scripts',ROOT/'tasks/g0-auth-preserving-activation/scripts',ROOT/'tasks/g0-proxy-continuation/cold-start/scripts'):
    sys.path.insert(0,str(path))
import activation_service
import proxy_transport
import owner_helper
from delivery_adapter import DeliveryAdapter,DeliveryBinding
import synthetic_native_transport as transport


def emit(value):print(json.dumps(value,sort_keys=True,separators=(',',':')),flush=True)
def sha(path):return activation_service._sha(Path(path))
def require(value,reason):transport.require(value,reason)
STDIN=None
CURRENT_STAGE='control'


async def control(end):
    timeout=transport.remaining(end)
    raw=await asyncio.wait_for(STDIN.readline(),timeout)
    require(raw and len(raw)<=65536,'controller EOF or oversized command')
    value=json.loads(raw);require(isinstance(value,dict),'invalid control command');return value


def validate_controller_parent(config):
    expected=config.get('controller_peer',config['proxy_peer'])
    require(os.getppid()==expected['pid'] and expected['uid']==os.getuid(),'controller parent identity changed')
    birth,executable=proxy_transport._process_metadata(expected['pid'])
    require(birth==expected['birth'] and executable==expected['executable'],'controller parent process changed')
    require(sha(executable)==expected['executable_sha256'],'controller parent executable changed')


def external_admission_mode(config,manifest):
    policy=manifest.get('external_admission')
    require(config.get('external_admission')==policy,'external admission policy changed')
    if policy is None:return None
    require(isinstance(policy,dict) and set(policy)=={'version','mode'} and type(policy['version']) is int and policy['version']==1
        and policy['mode'] in ('launchd','inherited_fd'),'invalid external admission policy')
    return policy['mode']


async def wait_parent_admission(config,peer,mode,manifest):
    if mode is None:return
    transport.remaining(config['local_deadline'])
    emit({'event':'public_connected','role':config['role'],'pid':os.getpid(),'profile_id':config['profile_id'],
        'manifest_sha256':config['manifest_sha256'],'grant_sha256':config['grant_sha256'],'public_peer':peer})
    admitted=await control(config['local_deadline'])
    require(set(admitted)=={'action','role','lease_id','manifest_sha256','grant_sha256','receipt_sha256','activation_id','receipt_name'}
        and admitted['action']=='admitted' and admitted['role']==config['role']
        and admitted['manifest_sha256']==config['manifest_sha256'] and admitted['grant_sha256']==config['grant_sha256'],'parent admission binding changed')
    require(type(admitted['lease_id']) is str and len(admitted['lease_id'])==12 and all(c in '0123456789abcdef' for c in admitted['lease_id']),'parent admission lease invalid')
    require(type(admitted['receipt_sha256']) is str and len(admitted['receipt_sha256'])==64 and all(c in '0123456789abcdef' for c in admitted['receipt_sha256']),'parent admission receipt invalid')
    if config['role']=='helper':require(admitted['lease_id']==config['lease_id'],'parent helper lease changed')
    require(type(admitted['activation_id']) is str and len(admitted['activation_id'])==32 and all(c in '0123456789abcdef' for c in admitted['activation_id']),'parent activation invalid')
    name=(f"owner-connected-{admitted['lease_id']}-{os.getpid()}.json" if config['role']=='owner' else f"helper-ready-{admitted['lease_id']}-{os.getpid()}.json")
    require(admitted['receipt_name']==name,'parent receipt name mismatch')
    receipt_source=ROOT/'tasks/g0-global-delivery-validation/scripts/receipt_store.py'
    require(manifest['file_pins'].get(str(receipt_source))==sha(receipt_source),'admission reader is not pinned')
    sys.path.insert(0,str(receipt_source.parent))
    from receipt_store import ReceiptStore
    store=ReceiptStore(Path(manifest['state_dir']),activation_id=admitted['activation_id'])
    try:
        captured=store.read(name)
        require(captured is not None and store.activation_id==admitted['activation_id'],'admission receipt missing')
        row,digest=captured
        require(digest==admitted['receipt_sha256'] and row.get('manifest_sha256')==config['manifest_sha256']
            and row.get('activation_id')==admitted['activation_id'] and row.get('grant_sha256')==config['grant_sha256'],'admission receipt hash/binding changed')
        bound=row if config['role']=='owner' else row['owner_lease']
        require(bound.get('lease_id')==admitted['lease_id'] and bound.get('profile_id')==config['profile_id']
            and bound.get('owner_context_sha256')==config['owner_context_sha256'],'admission receipt owner context changed')
        require(row.get('service_identity')==config['proxy_peer'],'admission receipt service changed')
        actual_birth,actual_executable=proxy_transport._process_metadata(os.getpid())
        frontend=row.get('frontend_peer',{})
        require(frontend.get('pid')==os.getpid() and frontend.get('uid')==os.getuid() and frontend.get('birth')==actual_birth
            and frontend.get('executable')==actual_executable and frontend.get('complete') is True,'admission receipt frontend changed')
    finally:store.close()
    validate_controller_parent(config);transport.remaining(config['local_deadline'])


def validate_config(config,role):
    manifest,digest=activation_service._private_json(Path(config['manifest_path']))
    require(digest==config['manifest_sha256'],'manifest changed')
    require(manifest['public_socket']==config['public_socket'],'public socket mapping changed')
    require(all(sha(path)==value for path,value in manifest['file_pins'].items()),'source pins changed')
    external_admission_mode(config,manifest)
    policy=activation_service.native_tui_policy(manifest.get('native_tui_policy'))
    require(config.get('native_tui_policy')==policy,'native TUI policy changed')
    grant,digest=activation_service._private_json(Path(config['grant_path']))
    require(digest==config['grant_sha256'] and grant['profile_id']==config['profile_id'],'grant changed')
    birth,executable=proxy_transport._process_metadata(os.getpid())
    prefix='helper_' if role=='helper' else ''
    require(grant[prefix+'pid']==os.getpid() and grant[prefix+'uid']==os.getuid() and grant[prefix+'birth']==birth,'grant PID identity changed')
    require(grant['helper_executable' if role=='helper' else 'expected_executable']==executable,'grant executable changed')
    require(sha(executable)==grant['helper_executable_sha256' if role=='helper' else 'executable_sha256'],'executable SHA changed')
    require(transport._socket_identity(Path(config['public_socket']))==tuple(config['public_socket_identity']),'public inode changed')
    validate_controller_parent(config)
    if role=='helper':
        require(grant['role']=='owner-helper' and grant['owner_thread_id']==config['thread_id'] and grant['lease_id']==config['lease_id'],'helper owner lease changed')
    return manifest,grant


async def open_public(config,mode=None):
    timeout=transport.remaining(config['local_deadline'])
    reader,writer=await asyncio.wait_for(owner_helper._open_websocket(config['public_socket']),timeout)
    peer=proxy_transport.peer_identity(writer.get_extra_info('socket'))
    expected=config['proxy_peer']
    if mode!='launchd':
        require(peer.complete and all(getattr(peer,key)==expected[key] for key in ('pid','uid','birth','executable')),'public proxy peer mismatch')
        require(sha(peer.executable)==expected['executable_sha256'],'public proxy executable changed')
    require(transport._socket_identity(Path(config['public_socket']))==tuple(config['public_socket_identity']),'public inode changed after connect')
    transport.remaining(config['local_deadline']);return reader,writer,asdict(peer)


class OwnerWire:
    def __init__(self,reader,writer):self.reader=reader;self.writer=writer
    async def send(self,text):self.writer.write(owner_helper._websocket_frame(text.encode()));await self.writer.drain()
    async def recv(self):return (await owner_helper._read_server_text(self.reader)).decode()
    async def close(self):self.writer.close();await self.writer.wait_closed()


class HelperChannel:
    def __init__(self,connection,deadline):self.connection=connection;self.deadline=deadline;self.envelopes=[];self.frames=0;self.bytes=0
    def record(self,direction,packet):
        raw=transport.encoded(packet);self.bytes+=len(raw);self.frames+=1
        require(self.frames<=512 and self.bytes<=4*1024*1024,'helper wire budget')
        method=packet.get('method')
        self.envelopes.append({'direction':direction,'method':method if method in owner_helper._SERVER_NOTIFICATIONS or method in ('initialize','initialized','turn/start','thread/read','thread/turns/list') else None,
            'request_id':packet.get('id') if type(packet.get('id')) is int else None,'sha256':hashlib.sha256(raw).hexdigest(),'bytes':len(raw)})
    async def send_rpc(self,packet):
        self.record('client',packet)
        timeout=min(10,transport.remaining(self.deadline))
        await asyncio.wait_for(self.connection.send_rpc(packet),timeout)
        transport.remaining(self.deadline)
    async def read_rpc(self):
        timeout=transport.remaining(self.deadline)
        packet=await asyncio.wait_for(self.connection.read_rpc(),timeout)
        self.record('server',packet);transport.remaining(self.deadline);return packet


async def owner(config):
    manifest,_=validate_config(config,'owner');mode=external_admission_mode(config,manifest)
    reader,writer,peer=await open_public(config,mode)
    wire=OwnerWire(reader,writer);records=[];drain=None
    try:
        await wait_parent_admission(config,peer,mode,manifest)
        rpc=transport._Rpc(wire,config['deadline'],records,config['local_deadline'])
        initialized=await rpc.call('initialize',{'clientInfo':{'name':'g0-python-owner','version':'1'},'capabilities':{'experimentalApi':True}})
        require(initialized.get('codexHome')==config['codex_home'],'owner initialize home mismatch')
        await rpc.initialized()
        response=await rpc.call('thread/start',{'cwd':config['workspace'],'model':'gpt-5.6-luna','modelProvider':'synthetic','ephemeral':False,'historyMode':'legacy','approvalPolicy':'never'})
        thread=response['thread'];thread_id=thread['id'];require(thread.get('cwd')==config['workspace'],'owner workspace mismatch')
        while True:
            packet=rpc.pending.pop(0) if rpc.pending else await rpc.receive(config['local_deadline'])
            if packet.get('method')=='thread/started':
                require(packet.get('params',{}).get('thread',{}).get('id')==thread_id,'owner started thread mismatch');break
        emit({'event':'thread_ready','thread_id':thread_id,'public_peer':peer,'home_match':True})
        command=await control(config['deadline']);require(command=={'action':'initial_turn'},'initial turn was not authorized')
        rpc.startup_deadline=None
        receipt=await rpc.call('turn/start',{'threadId':thread_id,'input':[{'type':'text','text':'G0_SYNTHETIC_READY'}]})
        turn_id=receipt['turn']['id'];await rpc.completed(thread_id,turn_id)
        emit({'event':'initial_ready','thread_id':thread_id,'initial_turn_id':turn_id,'home_match':True})
        async def receive_notifications():
            while True:
                packet=await rpc.receive(config['deadline']);require('method' in packet,'unpaired owner response')
        drain=asyncio.create_task(receive_notifications())
        while True:
            command=await control(config['cleanup_deadline'])
            if command.get('action')=='status':
                require(not drain.done(),'owner stream lost')
                emit({'event':'owner_live','thread_id':thread_id,'initial_turn_id':turn_id})
            elif command=={'action':'close'}:
                drain.cancel();await asyncio.gather(drain,return_exceptions=True);drain=None
                timeout=min(10,transport.remaining(config['cleanup_deadline']))
                await asyncio.wait_for(wire.close(),timeout)
                emit({'event':'owner_closed','thread_id':thread_id,'initial_turn_id':turn_id,'envelopes':records});return
            else:raise ValueError('unsupported owner command')
    finally:
        if drain is not None:drain.cancel();await asyncio.gather(drain,return_exceptions=True)
        writer.close()
        try:await asyncio.wait_for(writer.wait_closed(),1)
        except Exception:pass


def verify_history(history,capability,initial_turn_id,packets,*,paginated=False):
    responses=[packet for packet in packets if packet.get('id')==2]
    require(len(responses)==1 and 'error' not in responses[0],'delivery receipt missing')
    turn_id=responses[0]['result']['turn']['id'];require(turn_id!=initial_turn_id,'delivery reused initial turn')
    completed=[packet for packet in packets if packet.get('method')=='turn/completed']
    require(len(completed)<=1 and all(packet['params']['turn'].get('id')==turn_id and packet['params']['turn'].get('status')=='completed' for packet in completed),'delivery completion mismatch')
    for packet in packets:
        if packet.get('method') in ('turn/started','turn/completed'):
            require(packet['params']['turn']['id']==turn_id,'delivery turn identity changed')
        if packet.get('method') in ('item/started','item/completed','item/agentMessage/delta'):
            require(packet['params']['turnId']==turn_id,'delivery item turn changed')
    thread=history['response']['result']['thread'];turns=thread.get('turns',[])
    require(thread.get('id')==capability.binding.owner_thread_id and thread.get('historyMode')==('paginated' if paginated else 'legacy'),'history owner/mode mismatch')
    require(len(turns)==2 and [turn['id'] for turn in turns]==[initial_turn_id,turn_id],'history turn chain mismatch')
    for index,turn in enumerate(turns):
        require(turn.get('status')=='completed' and not turn.get('error'),'history incomplete')
        messages=[item for item in turn.get('items',[]) if item.get('type')=='agentMessage']
        require(len(messages)==1 and messages[0].get('text')==('READY' if index==0 else 'SYNTHETIC_COMPLETE'),'synthetic history message mismatch')
    return turn_id


async def read_completed_history(channel,capability,initial_turn_id,packets,deadline,*,paginated=False):
    receipts=[packet for packet in packets if packet.get('id')==2]
    require(len(receipts)==1 and 'error' not in receipts[0],'delivery receipt missing')
    delivery_turn_id=receipts[0]['result']['turn']['id']
    require(delivery_turn_id!=initial_turn_id,'delivery reused initial turn')
    history_end=min(deadline,transport.now()+10)
    for attempt in range(20):
        request={'id':3+attempt,'method':'thread/read','params':{'threadId':capability.binding.owner_thread_id,'includeTurns':True}}
        timeout=transport.remaining(history_end)
        await asyncio.wait_for(channel.send_rpc(request),timeout)
        while True:
            timeout=transport.remaining(history_end)
            response=await asyncio.wait_for(channel.read_rpc(),timeout)
            transport.remaining(history_end)
            if response.get('id')==request['id']:break
            require('method' in response,'unpaired history response');packets.append(response)
        require('error' not in response,'history read rejected')
        thread=response['result']['thread'];turns=thread.get('turns')
        require(thread.get('id')==capability.binding.owner_thread_id and thread.get('historyMode')==('paginated' if paginated else 'legacy'),'history owner/mode mismatch')
        require(isinstance(turns,list) and 1<=len(turns)<=2 and turns[0].get('id')==initial_turn_id,'history turn chain mismatch')
        require(turns[0].get('status')=='completed' and not turns[0].get('error'),'initial history incomplete')
        if len(turns)==2:
            require(turns[1].get('id')==delivery_turn_id and turns[1].get('status') in ('inProgress','completed') and not turns[1].get('error'),'delivery history identity or status mismatch')
            if turns[1]['status']=='completed':
                history={'request':request,'response':response}
                verify_history(history,capability,initial_turn_id,packets,paginated=paginated)
                return history,delivery_turn_id,{'source':'same_service_thread_read','read_count':attempt+1,
                    'request_id':request['id'],'notification_completion_observed':any(p.get('method')=='turn/completed' for p in packets)}
        if attempt<19:
            timeout=min(.1,transport.remaining(history_end));await asyncio.sleep(timeout)
    raise TimeoutError('bounded history attempts exhausted')


async def discover_initial_history(channel,config):
    request={'id':1001,'method':'thread/read','params':{'threadId':config['thread_id'],'includeTurns':True}}
    end=min(config['deadline'],config['local_deadline'])
    timeout=transport.remaining(end);await asyncio.wait_for(channel.send_rpc(request),timeout)
    while True:
        timeout=transport.remaining(end);packet=await asyncio.wait_for(channel.read_rpc(),timeout)
        if packet.get('id')==1001:break
        require('method' in packet,'initial history unpaired response')
    require('error' not in packet,'initial history rejected')
    thread=packet['result']['thread'];turns=thread.get('turns')
    require(thread.get('id')==config['thread_id'] and thread.get('cwd')==config['workspace'] and thread.get('historyMode')=='paginated','initial history identity changed')
    require(isinstance(turns,list) and len(turns)==1,'initial history ambiguous')
    turn=turns[0];require(isinstance(turn.get('id'),str) and turn['id'] and turn.get('status')=='completed' and not turn.get('error'),'initial history incomplete')
    items=turn.get('items',[]);messages=[item for item in items if item.get('type')=='agentMessage']
    inputs=[item for item in items if item.get('type')=='userMessage']
    require(len(messages)==1 and messages[0].get('text')=='READY' and len(inputs)==1,'initial history marker mismatch')
    content=inputs[0].get('content')
    require(isinstance(content,list) and len(content)==1 and isinstance(content[0],dict)
        and set(content[0])<= {'type','text','text_elements'} and content[0].get('type')=='text'
        and content[0].get('text')=='G0_SYNTHETIC_READY' and content[0].get('text_elements',[])==[],'initial input mismatch')
    transport.remaining(end)
    return turn['id'],hashlib.sha256(transport.encoded({'request':request,'response':packet})).hexdigest()


async def capture_paginated_history(channel,config,initial_turn_id,delivery_turn_id,end):
    end=min(config['deadline'],end);cursor=None;seen=set();reads=[];turns=[]
    for index in range(8):
        request={'id':2001+index,'method':'thread/turns/list','params':{'threadId':config['thread_id'],
            'cursor':cursor,'limit':2,'sortDirection':'asc','itemsView':'full'}}
        timeout=transport.remaining(end);await asyncio.wait_for(channel.send_rpc(request),timeout)
        while True:
            timeout=transport.remaining(end);response=await asyncio.wait_for(channel.read_rpc(),timeout)
            if response.get('id')==request['id']:break
            require('method' in response,'unpaired page response')
        require('error' not in response,'history page rejected')
        result=response['result'];page=result.get('data')
        require(isinstance(page,list) and len(page)<=2,'history page size')
        turns.extend(page);require(len(turns)<=2,'history page chain grew')
        require(all(turn.get('itemsView')=='full' and turn.get('status')=='completed' and not turn.get('error') for turn in page),'history page incomplete')
        reads.append({'request':request,'response':response})
        cursor=result.get('nextCursor')
        if cursor is None:
            require([turn.get('id') for turn in turns]==[initial_turn_id,delivery_turn_id],'paged turn chain changed')
            transport.remaining(end);return reads
        require(type(cursor) is str and 0<len(cursor)<=256 and cursor not in seen,'history cursor repeated or invalid')
        seen.add(cursor)
    raise ValueError('history page limit exceeded')


async def helper(config):
    global CURRENT_STAGE
    CURRENT_STAGE='helper-validate'
    manifest,grant=validate_config(config,'helper');mode=external_admission_mode(config,manifest)
    discover=manifest.get('native_tui_policy') is not None
    require(not discover or mode=='inherited_fd','native TUI discovery is private only')
    transport.remaining(config['local_deadline'])
    require(os.environ.get('CODEX_THREAD_ID')==config['thread_id'],'Go injected owner context mismatch')
    adapter=DeliveryAdapter(config['go_binary'],expected_sha256=config['go_sha256'],owner_context_sha256=grant['owner_context_sha256'])
    binding=DeliveryBinding(config['profile_id'],config['thread_id'],config['owner_epoch'],config['nonce'],1)
    CURRENT_STAGE='helper-prepare-claim'
    capability=None
    if mode is None:capability=adapter.prepare_and_claim(binding,Path(config['task_dir']),config['delivery_id'],config['events'],env={'CODEX_THREAD_ID':config['thread_id']})
    CURRENT_STAGE='helper-open-public'
    reader,writer,peer=await open_public(config,mode)
    connection=owner_helper.OwnerHelperConnection(reader,writer,config['lease_id'],config['thread_id'])
    channel=HelperChannel(connection,config['deadline'])
    try:
        await wait_parent_admission(config,peer,mode,manifest)
        if mode is not None and not discover:
            CURRENT_STAGE='helper-prepare-claim'
            capability=adapter.prepare_and_claim(binding,Path(config['task_dir']),config['delivery_id'],config['events'],env={'CODEX_THREAD_ID':config['thread_id']})
        CURRENT_STAGE='helper-initialize'
        await channel.send_rpc({'id':1,'method':'initialize','params':{'clientInfo':{'name':'g0-native-standalone','version':'0.1.0'},'capabilities':{'experimentalApi':True}}})
        while True:
            timeout=transport.remaining(config['local_deadline'])
            packet=await asyncio.wait_for(channel.read_rpc(),timeout)
            if packet.get('id')==1:
                require(packet.get('result',{}).get('codexHome')==config['codex_home'],'helper initialize home mismatch');break
        transport.remaining(config['local_deadline'])
        await channel.send_rpc({'method':'initialized'})
        transport.remaining(config['local_deadline'])
        initial_history_sha=None
        if discover:
            require(config.get('initial_turn_id') is None,'TUI initial history must come from the backend')
            initial_turn,initial_history_sha=await discover_initial_history(channel,config)
            config=dict(config,initial_turn_id=initial_turn)
            CURRENT_STAGE='helper-prepare-claim'
            capability=adapter.prepare_and_claim(binding,Path(config['task_dir']),config['delivery_id'],config['events'],env={'CODEX_THREAD_ID':config['thread_id']})
        CURRENT_STAGE='helper-send'
        timeout=transport.remaining(config['deadline'])
        packets=await asyncio.wait_for(adapter.send_tool_output_once(channel,capability,request_id=2,wait_for_completion=False),timeout)
        CURRENT_STAGE='helper-history'
        history_deadline=min(config['deadline'],transport.now()+10)
        history,delivery_turn_id,history_observation=await read_completed_history(channel,capability,config['initial_turn_id'],packets,history_deadline,paginated=discover)
        reads=await capture_paginated_history(channel,config,config['initial_turn_id'],delivery_turn_id,history_deadline) if discover else [history]
        proof=adapter.audit_history(capability,reads)
        require(proof.summary['captured_page_chain_complete'] and proof.summary['matched_history_items']==1,'history proof incomplete')
        proof_row={'event':'history_proof','thread_id':config['thread_id'],'initial_turn_id':config['initial_turn_id'],'delivery_turn_id':delivery_turn_id,
            'lease_id':config['lease_id'],'public_peer':peer,'grant_sha256':config['grant_sha256'],'manifest_sha256':config['manifest_sha256'],
            'native_envelope_sha256':capability.native_envelope_sha256,'go_intent_sha256':capability.go_intent_sha256,'claim_sha256':capability.claim_sha256,
            'reads_sha256':proof.reads_sha256,'matched_event_ids':sorted(proof.matched_event_ids),'summary':proof.summary,'envelopes':channel.envelopes,'history_observation':history_observation,**({'initial_history_sha256':initial_history_sha} if initial_history_sha else {})}
        emit(proof_row)
        CURRENT_STAGE='helper-decision'
        decision=await control(config['deadline'])
        require(decision.get('action')=='decide' and decision.get('reads_sha256')==proof.reads_sha256,'explicit decision does not match proof')
        transport.remaining(config['deadline'])
        CURRENT_STAGE='helper-ack'
        ack=adapter.ack_confirmed(capability,proof,decision['decisions'],command_ids=decision['command_ids'],env={'CODEX_THREAD_ID':config['thread_id']})
        transport.remaining(config['deadline'])
        emit({'event':'ack','records':list(ack.records),'business_ack_complete':ack.business_ack_complete,
            'pending_event_ids':list(ack.pending_event_ids),'nonhandled_decisions':ack.nonhandled_decisions})
        CURRENT_STAGE='helper-eof';channel.deadline=config['cleanup_deadline']
        # Keep this helper attached. Only actual transport EOF after owner
        # closes qualifies, not an arbitrary protocol rejection.
        while True:
            try:await channel.read_rpc()
            except owner_helper.OwnerHelperAdmissionError as exc:
                require(str(exc)=='websocket_eof','helper failed before EOF')
                emit({'event':'helper_eof','lease_id':config['lease_id'],'eof_raw':transport.now()});return
    finally:
        try:await asyncio.wait_for(connection.close(),1)
        except Exception:pass


async def main():
    global STDIN,CURRENT_STAGE
    role=sys.argv[1];require(role in ('owner','helper'),'unknown Python client role')
    STDIN=asyncio.StreamReader(limit=65536)
    await asyncio.get_running_loop().connect_read_pipe(lambda:asyncio.StreamReaderProtocol(STDIN),sys.stdin.buffer)
    emit({'event':'boot','role':role,'pid':os.getpid()})
    stage='control'
    try:
        config=await control(transport.now()+10);require(config.get('role')==role,'role changed')
        stage=role;CURRENT_STAGE=role
        timeout=transport.remaining(config['cleanup_deadline'])
        await asyncio.wait_for(owner(config) if role=='owner' else helper(config),timeout)
        return 0
    except Exception as exc:
        emit({'event':'error','stage':CURRENT_STAGE,'type':type(exc).__name__,'errno':getattr(exc,'errno',None),'message_sha256':hashlib.sha256(str(exc).encode()).hexdigest()})
        return 1


if __name__=='__main__':raise SystemExit(asyncio.run(main()))
