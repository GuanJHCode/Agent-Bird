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


async def control(end):
    raw=await asyncio.wait_for(STDIN.readline(),transport.remaining(end))
    require(raw and len(raw)<=65536,'controller EOF or oversized command')
    value=json.loads(raw);require(isinstance(value,dict),'invalid control command');return value


def validate_config(config,role):
    manifest,digest=activation_service._private_json(Path(config['manifest_path']))
    require(digest==config['manifest_sha256'],'manifest changed')
    require(manifest['public_socket']==config['public_socket'],'public socket mapping changed')
    require(all(sha(path)==value for path,value in manifest['file_pins'].items()),'source pins changed')
    grant,digest=activation_service._private_json(Path(config['grant_path']))
    require(digest==config['grant_sha256'] and grant['profile_id']==config['profile_id'],'grant changed')
    birth,executable=proxy_transport._process_metadata(os.getpid())
    prefix='helper_' if role=='helper' else ''
    require(grant[prefix+'pid']==os.getpid() and grant[prefix+'uid']==os.getuid() and grant[prefix+'birth']==birth,'grant PID identity changed')
    require(grant['helper_executable' if role=='helper' else 'expected_executable']==executable,'grant executable changed')
    require(sha(executable)==grant['helper_executable_sha256' if role=='helper' else 'executable_sha256'],'executable SHA changed')
    require(transport._socket_identity(Path(config['public_socket']))==tuple(config['public_socket_identity']),'public inode changed')
    require(os.getppid()==config['proxy_peer']['pid'],'proxy is not the original controller')
    if role=='helper':
        require(grant['role']=='owner-helper' and grant['owner_thread_id']==config['thread_id'] and grant['lease_id']==config['lease_id'],'helper owner lease changed')
    return manifest


async def open_public(config):
    reader,writer=await asyncio.wait_for(owner_helper._open_websocket(config['public_socket']),transport.remaining(config['local_deadline']))
    peer=proxy_transport.peer_identity(writer.get_extra_info('socket'))
    expected=config['proxy_peer']
    require(peer.complete and all(getattr(peer,key)==expected[key] for key in ('pid','uid','birth','executable')),'public proxy peer mismatch')
    require(sha(peer.executable)==expected['executable_sha256'],'public proxy executable changed')
    require(transport._socket_identity(Path(config['public_socket']))==tuple(config['public_socket_identity']),'public inode changed after connect')
    return reader,writer,asdict(peer)


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
        self.envelopes.append({'direction':direction,'method':method if method in owner_helper._SERVER_NOTIFICATIONS or method in ('initialize','initialized','turn/start','thread/read') else None,
            'request_id':packet.get('id') if type(packet.get('id')) is int else None,'sha256':hashlib.sha256(raw).hexdigest(),'bytes':len(raw)})
    async def send_rpc(self,packet):
        self.record('client',packet)
        await asyncio.wait_for(self.connection.send_rpc(packet),min(10,transport.remaining(self.deadline)))
        transport.remaining(self.deadline)
    async def read_rpc(self):
        packet=await asyncio.wait_for(self.connection.read_rpc(),transport.remaining(self.deadline))
        self.record('server',packet);transport.remaining(self.deadline);return packet


async def owner(config):
    validate_config(config,'owner');reader,writer,peer=await open_public(config)
    wire=OwnerWire(reader,writer);records=[];drain=None
    try:
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
            command=await control(config['deadline'])
            if command.get('action')=='status':
                require(not drain.done(),'owner stream lost')
                emit({'event':'owner_live','thread_id':thread_id,'initial_turn_id':turn_id})
            elif command=={'action':'close'}:
                drain.cancel();await asyncio.gather(drain,return_exceptions=True);drain=None
                await asyncio.wait_for(wire.close(),min(10,transport.remaining(config['deadline'])))
                emit({'event':'owner_closed','thread_id':thread_id,'initial_turn_id':turn_id,'envelopes':records});return
            else:raise ValueError('unsupported owner command')
    finally:
        if drain is not None:drain.cancel();await asyncio.gather(drain,return_exceptions=True)
        writer.close()
        try:await asyncio.wait_for(writer.wait_closed(),1)
        except Exception:pass


def verify_history(history,capability,initial_turn_id,packets):
    responses=[packet for packet in packets if packet.get('id')==2]
    require(len(responses)==1 and 'error' not in responses[0],'delivery receipt missing')
    turn_id=responses[0]['result']['turn']['id'];require(turn_id!=initial_turn_id,'delivery reused initial turn')
    completed=[packet for packet in packets if packet.get('method')=='turn/completed']
    require(len(completed)==1 and completed[0]['params']['turn'].get('id')==turn_id and completed[0]['params']['turn'].get('status')=='completed','delivery completion mismatch')
    for packet in packets:
        if packet.get('method') in ('turn/started','turn/completed'):
            require(packet['params']['turn']['id']==turn_id,'delivery turn identity changed')
        if packet.get('method') in ('item/started','item/completed','item/agentMessage/delta'):
            require(packet['params']['turnId']==turn_id,'delivery item turn changed')
    thread=history['response']['result']['thread'];turns=thread.get('turns',[])
    require(thread.get('id')==capability.binding.owner_thread_id and thread.get('historyMode')=='legacy','history owner/mode mismatch')
    require(len(turns)==2 and [turn['id'] for turn in turns]==[initial_turn_id,turn_id],'history turn chain mismatch')
    for index,turn in enumerate(turns):
        require(turn.get('status')=='completed' and not turn.get('error'),'history incomplete')
        messages=[item for item in turn.get('items',[]) if item.get('type')=='agentMessage']
        require(len(messages)==1 and messages[0].get('text')==('READY' if index==0 else 'SYNTHETIC_COMPLETE'),'synthetic history message mismatch')
    return turn_id


async def helper(config):
    validate_config(config,'helper')
    require(os.environ.get('CODEX_THREAD_ID')==config['thread_id'],'Go injected owner context mismatch')
    adapter=DeliveryAdapter(config['go_binary'],expected_sha256=config['go_sha256'])
    binding=DeliveryBinding(config['profile_id'],config['thread_id'],config['owner_epoch'],config['nonce'],1)
    capability=adapter.prepare_and_claim(binding,Path(config['task_dir']),config['delivery_id'],config['events'],env={'CODEX_THREAD_ID':config['thread_id']})
    reader,writer,peer=await open_public(config)
    connection=owner_helper.OwnerHelperConnection(reader,writer,config['lease_id'],config['thread_id'])
    channel=HelperChannel(connection,config['deadline'])
    try:
        await channel.send_rpc({'id':1,'method':'initialize','params':{'clientInfo':{'name':'g0-native-standalone','version':'0.1.0'},'capabilities':{'experimentalApi':True}}})
        while True:
            packet=await asyncio.wait_for(channel.read_rpc(),transport.remaining(config['local_deadline']))
            if packet.get('id')==1:
                require(packet.get('result',{}).get('codexHome')==config['codex_home'],'helper initialize home mismatch');break
        await channel.send_rpc({'method':'initialized'})
        packets=await asyncio.wait_for(adapter.send_tool_output_once(channel,capability,request_id=2),transport.remaining(config['deadline']))
        request={'id':3,'method':'thread/read','params':{'threadId':config['thread_id'],'includeTurns':True}}
        await channel.send_rpc(request)
        while True:
            response=await asyncio.wait_for(channel.read_rpc(),min(10,transport.remaining(config['deadline'])))
            if response.get('id')==3:break
        history={'request':request,'response':response}
        delivery_turn_id=verify_history(history,capability,config['initial_turn_id'],packets)
        proof=adapter.audit_history(capability,[history])
        require(proof.summary['captured_page_chain_complete'] and proof.summary['matched_history_items']==1,'history proof incomplete')
        proof_row={'event':'history_proof','thread_id':config['thread_id'],'initial_turn_id':config['initial_turn_id'],'delivery_turn_id':delivery_turn_id,
            'lease_id':config['lease_id'],'public_peer':peer,'grant_sha256':config['grant_sha256'],'manifest_sha256':config['manifest_sha256'],
            'native_envelope_sha256':capability.native_envelope_sha256,'go_intent_sha256':capability.go_intent_sha256,'claim_sha256':capability.claim_sha256,
            'reads_sha256':proof.reads_sha256,'matched_event_ids':sorted(proof.matched_event_ids),'summary':proof.summary,'envelopes':channel.envelopes}
        emit(proof_row)
        decision=await control(config['deadline'])
        require(decision.get('action')=='decide' and decision.get('reads_sha256')==proof.reads_sha256,'explicit decision does not match proof')
        transport.remaining(config['deadline'])
        ack=adapter.ack_confirmed(capability,proof,decision['decisions'],command_ids=decision['command_ids'],env={'CODEX_THREAD_ID':config['thread_id']})
        transport.remaining(config['deadline'])
        emit({'event':'ack','records':list(ack.records),'business_ack_complete':ack.business_ack_complete,
            'pending_event_ids':list(ack.pending_event_ids),'nonhandled_decisions':ack.nonhandled_decisions})
        # Keep this helper attached. Only actual transport EOF after owner
        # closes qualifies, not an arbitrary protocol rejection.
        while True:
            try:await channel.read_rpc()
            except owner_helper.OwnerHelperAdmissionError as exc:
                require(str(exc)=='websocket_eof','helper failed before EOF')
                emit({'event':'helper_eof','lease_id':config['lease_id']});return
    finally:
        try:await asyncio.wait_for(connection.close(),1)
        except Exception:pass


async def main():
    global STDIN
    role=sys.argv[1];require(role in ('owner','helper'),'unknown Python client role')
    STDIN=asyncio.StreamReader(limit=65536)
    await asyncio.get_running_loop().connect_read_pipe(lambda:asyncio.StreamReaderProtocol(STDIN),sys.stdin.buffer)
    emit({'event':'boot','role':role,'pid':os.getpid()})
    stage='control'
    try:
        config=await control(transport.now()+10);require(config.get('role')==role,'role changed')
        stage=role
        await asyncio.wait_for(owner(config) if role=='owner' else helper(config),transport.remaining(config['deadline']))
        return 0
    except Exception as exc:
        emit({'event':'error','stage':stage,'type':type(exc).__name__,'errno':getattr(exc,'errno',None),'message_sha256':hashlib.sha256(str(exc).encode()).hexdigest()})
        return 1


if __name__=='__main__':raise SystemExit(asyncio.run(main()))
