"""Explicit private transport experiment; run() is only for root-gated invocation.

No default/global socket, TUI, LaunchAgent, real credentials or external model.
The synthetic provider and profile remain owned by PreparedSyntheticFixture.
"""
from __future__ import annotations
import asyncio
from dataclasses import dataclass,asdict
import hashlib
import importlib.metadata
import json
import logging
import os
from pathlib import Path
import re
import socket
import stat
import sys
import time

ROOT=Path(__file__).resolve().parents[3]
for path in (ROOT/'tasks/g0-tui-proxy/scripts',ROOT/'tasks/g0-auth-preserving-activation/scripts'):
    sys.path.insert(0,str(path))
import auth_isolation as isolation
import proxy_transport
import owned_child_guard
from owned_child_guard import GuardedChild
import synthetic_native_fixture
import synthetic_responses

NATIVE_SHA256='4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc'
LOCAL_SECONDS=10.0
TOTAL_SECONDS=120.0
INITIAL='G0_SYNTHETIC_READY'
TOOL_OUTPUT='G0_SYNTHETIC_TOOL_RESULT'
NOTIFICATION_SOURCE=ROOT/'tasks/g0-completion/data/source/codex-rs/app-server-protocol/src/protocol/common.rs'
NOTIFICATION_SOURCE_SHA256='6aa47ec984c9198ea797bf63b784cf6a88cc0ab85efb0a4ac75e01aabffc2cfa'
SAFE_METHODS={'initialize','initialized','thread/start','thread/started','turn/start','turn/started',
    'turn/completed','thread/read','item/started','item/completed','item/agentMessage/delta',
    'thread/status/changed','thread/tokenUsage/updated','thread/name/updated','thread/settings/updated',
    'skills/changed','mcpServer/startupStatus/updated','account/updated','account/rateLimits/updated',
    'hook/started','hook/completed','warning','configWarning','deprecationNotice','app/list/updated',
    'rawResponseItem/completed','rawResponse/completed','remoteControl/status/changed'}


def now(): return time.clock_gettime(time.CLOCK_MONOTONIC_RAW)
def encoded(value): return json.dumps(value,sort_keys=True,separators=(',',':')).encode()
def digest(value): return hashlib.sha256(value).hexdigest()
def require(value,reason):
    if not value: raise ValueError(reason)
def remaining(deadline):
    left=deadline-now()
    if left<=0: raise TimeoutError('original deadline elapsed')
    return left


def source_pins():
    paths=[Path(__file__).resolve(),Path(sys.executable).resolve(),Path('/usr/bin/sandbox-exec'),NOTIFICATION_SOURCE]
    paths += [Path(module.__file__).resolve() for module in (isolation,proxy_transport,owned_child_guard,
        synthetic_native_fixture,synthetic_responses)]
    return {str(path):isolation._hash_stable_file(path) for path in paths}


def _diagnostic_known_method(method):
    # Diagnostic projection only: never contributes to SAFE_METHODS/admission.
    # Read and hash the same bounded bytes from the pinned 0.154 source.
    try:
        fd=os.open(NOTIFICATION_SOURCE,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
        try:
            if not stat.S_ISREG(os.fstat(fd).st_mode):return None
            raw=os.read(fd,512*1024+1)
        finally:os.close(fd)
        if digest(raw)!=NOTIFICATION_SOURCE_SHA256:return None
        body=raw.decode().split('server_notification_definitions! {',1)[1].split('\n}\n',1)[0]
        names=set(re.findall(r'\b\w+\s*=>\s*"([^"]+)"',body))
        names.update(re.findall(r'#\[serde\(rename = "([^"]+)"\)\]',body))
        return method if method in names else None
    except (OSError,UnicodeError,IndexError):return None


def _write(directory_fd,name,value):
    data=encoded(value)+b'\n'
    fd=os.open(name,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600,dir_fd=directory_fd)
    with os.fdopen(fd,'wb') as stream:
        stream.write(data);stream.flush();os.fsync(stream.fileno())
    os.fsync(directory_fd)


def _dir_identity(path):
    isolation._reject_symlink_components(path);isolation._owned_private_dir(path)
    info=path.stat();return(info.st_dev,info.st_ino,info.st_mode,info.st_uid)


def _open_case(path,identity):
    fd=os.open(path,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
    info=os.fstat(fd)
    if (info.st_dev,info.st_ino,info.st_mode,info.st_uid)!=identity:
        os.close(fd);raise ValueError('case directory changed during open')
    return fd


def _native_check(native,expected_sha):
    require(native.is_absolute() and native.resolve()==native,'native must be canonical')
    require(expected_sha==NATIVE_SHA256 and isolation._hash_stable_file(native)==NATIVE_SHA256,'fixed native SHA mismatch')
    info=native.stat()
    require(info.st_uid in (0,os.getuid()) and not info.st_mode&0o022 and info.st_mode&0o111,'unsafe native artifact')
    return (info.st_dev,info.st_ino,info.st_mode,info.st_uid)


def _socket_identity(path, directory_fd=None):
    info=path.lstat() if directory_fd is None else os.stat(path.name,dir_fd=directory_fd,follow_symlinks=False)
    require(stat.S_ISSOCK(info.st_mode) and info.st_uid==os.getuid() and not info.st_mode&0o077,'unsafe private socket')
    return (info.st_dev,info.st_ino,info.st_mode,info.st_uid)


@dataclass(frozen=True)
class TransportPlan:
    case_dir:Path
    case_identity:tuple
    native:Path
    native_identity:tuple
    source_hashes:dict
    manifest_sha256:str


def prepare_transport(prepared,case_dir,*,native_executable,expected_native_sha256,expected_source_pins):
    require(expected_source_pins and expected_source_pins==source_pins(),'source pins missing or changed')
    native=Path(native_executable)
    native_identity=_native_check(native,expected_native_sha256)
    require(prepared.context.expected_executable==native and prepared.context.expected_executable_sha256==NATIVE_SHA256,'profile executable mismatch')
    prepared.verify()
    require(not prepared.endpoint._started,'endpoint already started')
    require(not os.path.lexists(prepared.spec.backend_socket),'private socket already exists')
    require(importlib.metadata.version('websockets')=='16.0','websockets version mismatch')
    case=Path(case_dir)
    require(case.is_absolute() and case.resolve()==case,'case must be canonical')
    _dir_identity(case.parent);case.mkdir(mode=0o700)
    identity=_dir_identity(case)
    plan=isolation.build_backend_launch(prepared.context,[str(native),'app-server','--listen','unix://'+str(prepared.spec.backend_socket)])
    policy_path=prepared.spec.task_root/'synthetic.sb'
    require(policy_path.read_text()==plan['sandbox_profile'],'frozen policy mismatch')
    launch=['/usr/bin/sandbox-exec','-f',str(policy_path),*plan['argv']]
    manifest={'version':1,'synthetic_only':True,'business_ack_verified':False,'argv':launch,
        'native_sha256':NATIVE_SHA256,'native_identity':native_identity,'source_pins':expected_source_pins,'websockets_version':'16.0',
        'policy_sha256':digest(plan['sandbox_profile'].encode()),'environment_sha256':digest(encoded(plan['env'])),
        'cwd':plan['cwd'],'private_socket':plan['private_socket'],'fixture_plan_sha256':isolation._hash_stable_file(prepared.spec.task_root/'synthetic-plan.json'),
        'local_seconds':LOCAL_SECONDS,'total_seconds':TOTAL_SECONDS,'clock':'CLOCK_MONOTONIC_RAW',
        'initial_text':INITIAL,'tool_output':{'name':'g0_delivery','namespace':'orchestration','output':TOOL_OUTPUT}}
    fd=_open_case(case,identity)
    try:_write(fd,'plan.json',manifest)
    finally:os.close(fd)
    return TransportPlan(case,identity,native,native_identity,dict(expected_source_pins),isolation._hash_stable_file(case/'plan.json'))


class _Rpc:
    def __init__(self,ws,deadline,records,startup_deadline):
        self.ws=ws;self.deadline=deadline;self.records=records;self.next_id=1;self.pending=[];self.frames=0;self.bytes=0
        self.startup_deadline=startup_deadline

    def record(self,direction,packet):
        raw=encoded(packet)
        method=packet.get('method')
        safe_method=method if isinstance(method,str) and method in SAFE_METHODS else ('unlisted' if method is not None else None)
        summary={'direction':direction,'method':safe_method,'request_id':packet.get('id') if type(packet.get('id')) is int else None,
            'bytes':len(raw),'sha256':digest(raw)}
        if isinstance(method,str) and safe_method=='unlisted':
            name=method.encode()
            summary.update(unknown_method_sha256=digest(name),method_length=len(name))
            known=_diagnostic_known_method(method)
            if known is not None:summary['diagnostic_known_method']=known
        self.records.append(summary)

    async def receive(self,end):
        raw=await asyncio.wait_for(self.ws.recv(),remaining(end));remaining(end)
        require(isinstance(raw,str),'nontext WS frame')
        self.frames+=1;self.bytes+=len(raw.encode())
        require(self.frames<=512 and self.bytes<=4*1024*1024,'RPC receive budget exceeded')
        packet=json.loads(raw);require(isinstance(packet,dict),'invalid RPC envelope');self.record('server',packet)
        if 'method' in packet:
            require('id' not in packet,'unexpected server request')
            require(isinstance(packet['method'],str) and packet['method'] in SAFE_METHODS,'unlisted notification')
            if packet['method']=='remoteControl/status/changed':
                # Fixed v2/remote_control.rs:30,161. Validate opaque status
                # metadata only; no identity fields enter the safe record.
                params=packet.get('params')
                required={'status','serverName','installationId'}
                require(isinstance(params,dict) and required<=set(params)<=required|{'environmentId'},'invalid remote status shape')
                require(params['status'] in ('disabled','connecting','connected','errored')
                    and all(isinstance(params[name],str) for name in ('serverName','installationId'))
                    and (params.get('environmentId') is None or isinstance(params['environmentId'],str)),
                    'invalid remote status payload')
        return packet

    async def call(self,method,params):
        end=min(self.deadline,now()+LOCAL_SECONDS,self.startup_deadline or self.deadline)
        packet={'id':self.next_id,'method':method,'params':params};self.next_id+=1
        self.record('client',packet)
        await asyncio.wait_for(self.ws.send(encoded(packet).decode()),remaining(end));remaining(end)
        while True:
            reply=await self.receive(end)
            remaining(end)
            if 'method' in reply:self.pending.append(reply);continue
            require(type(reply.get('id')) is int and reply['id']==packet['id'],'RPC id mismatch')
            require(set(reply)=={'id','result'},'RPC failed or malformed')
            return reply['result']

    async def initialized(self):
        packet={'method':'initialized'};self.record('client',packet)
        end=min(self.deadline,now()+LOCAL_SECONDS,self.startup_deadline or self.deadline)
        await asyncio.wait_for(self.ws.send(encoded(packet).decode()),remaining(end));remaining(end)

    async def completed(self,thread_id,turn_id):
        while True:
            packet=self.pending.pop(0) if self.pending else await self.receive(self.deadline)
            require('method' in packet,'unpaired RPC response')
            if packet['method']!='turn/completed':continue
            params=packet.get('params',{});turn=params.get('turn',{})
            require(params.get('threadId')==thread_id and turn.get('id')==turn_id,'completed identity mismatch')
            require(turn.get('status')=='completed' and not turn.get('error'),'turn not completed')
            remaining(self.deadline);return turn


def _history(thread,thread_id,turn_ids,cwd):
    require(thread.get('id')==thread_id and thread.get('cwd')==str(cwd),'history thread mismatch')
    require(thread.get('historyMode')=='legacy' and thread.get('ephemeral') is False,'history mode or ephemeral flag mismatch')
    turns=thread.get('turns');require(isinstance(turns,list) and len(turns)==2,'history turn count mismatch')
    ids=[]
    for index,turn in enumerate(turns):
        require(turn.get('id')==turn_ids[index] and turn.get('status')=='completed' and not turn.get('error'),'history turn mismatch')
        items=turn.get('items');require(isinstance(items,list) and len(items)==2,'history item count mismatch')
        expected='userMessage' if index==0 else 'functionCallOutput'
        inputs=[x for x in items if x.get('type')==expected];messages=[x for x in items if x.get('type')=='agentMessage']
        require(len(inputs)==len(messages)==1,'history item type mismatch')
        require(messages[0].get('text')==('READY' if index==0 else 'SYNTHETIC_COMPLETE'),'history synthetic output mismatch')
        if index==0:
            content=inputs[0].get('content')
            require(isinstance(content,list) and len(content)==1 and content[0].get('type')=='text' and content[0].get('text')==INITIAL,'history initial input mismatch')
        else:
            require(inputs[0].get('name')=='g0_delivery' and inputs[0].get('namespace')=='orchestration'
                and inputs[0].get('output')==TOOL_OUTPUT,'history tool output mismatch')
        for item in items:
            ident=item.get('id');require(isinstance(ident,str) and ident and ident not in ids,'history item identity missing or duplicate');ids.append(ident)
    return ids


async def _drain(stream):
    count=0;sha=hashlib.sha256()
    while data:=await stream.read(65536):count+=len(data);sha.update(data)
    return {'bytes':count,'sha256':sha.hexdigest()}


async def run_transport(plan,prepared):
    require(_dir_identity(plan.case_dir)==plan.case_identity,'case directory changed')
    directory_fd=_open_case(plan.case_dir,plan.case_identity)
    try:_write(directory_fd,'attempt.json',{'started_raw':now(),'automatic_retry':False})
    except BaseException:os.close(directory_fd);raise
    started=now();deadline=started+TOTAL_SECONDS;local_end=min(deadline,started+LOCAL_SECONDS)
    result={'version':1,'status':'unknown','synthetic_only':True,'business_ack_verified':False,'history_verified':False,'durable_history_verified':False,
        'started_raw':started,'deadline_raw':deadline,'envelopes':[],'turn_ids':[],'backend':None}
    child=None;ws=None;raw=None;socket_identity=None;peer_verified=False;outputs=[];stage='preflight';backend_fd=None
    try:
        require(plan.source_hashes==source_pins(),'source pins changed')
        require(_native_check(plan.native,NATIVE_SHA256)==plan.native_identity,'native artifact identity changed')
        require(importlib.metadata.version('websockets')=='16.0','websockets version changed')
        require(isolation._hash_stable_file(plan.case_dir/'plan.json')==plan.manifest_sha256,'transport plan changed')
        prepared.verify();require(not prepared.endpoint._started,'endpoint already started')
        require(not os.path.lexists(prepared.spec.backend_socket),'private socket appeared')
        backend_fd=_open_case(prepared.spec.backend_socket.parent,prepared._directories[prepared.spec.backend_socket.parent])
        manifest=json.loads((plan.case_dir/'plan.json').read_bytes())
        launch=isolation.build_backend_launch(prepared.context,manifest['argv'][3:])
        require(digest(encoded(launch['env']))==manifest['environment_sha256'],'environment changed')
        remaining(local_end)
        prepared.start_endpoint();remaining(local_end)
        stage='spawn'
        result['spawn_started_raw']=now();remaining(local_end)
        child=await GuardedChild.spawn(manifest['argv'],cwd=launch['cwd'],env=launch['env'],receipt_dir=plan.case_dir,
            stdin=asyncio.subprocess.DEVNULL,stdout=asyncio.subprocess.PIPE,stderr=asyncio.subprocess.PIPE)
        outputs=[asyncio.create_task(_drain(stream)) for stream in (child.stdout,child.stderr)]
        require(child.identity_known,'backend identity unknown')
        result['backend']={'pid':child.pid,'birth':child._initial['child_birth'],'guardian_pid':child.guardian_pid}
        while not os.path.lexists(prepared.spec.backend_socket):
            require(child.returncode is None,'backend exited before listen');remaining(local_end);await asyncio.sleep(.01)
        socket_identity=_socket_identity(prepared.spec.backend_socket,backend_fd);remaining(local_end)
        stage='connect'
        raw=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM);raw.setblocking(False)
        await asyncio.wait_for(asyncio.get_running_loop().sock_connect(raw,str(prepared.spec.backend_socket)),remaining(local_end))
        peer=proxy_transport.peer_identity(raw)
        require(peer.complete and peer.pid==child.pid and peer.uid==os.getuid() and peer.birth==child._initial['child_birth']
            and peer.executable==str(plan.native),'backend peer mismatch')
        result['backend']['peer']=asdict(peer);peer_verified=True
        from websockets.asyncio.client import unix_connect
        logger=logging.Logger('synthetic-private',level=logging.CRITICAL+1);logger.addHandler(logging.NullHandler())
        ws=await unix_connect(uri='ws://localhost/',sock=raw,compression=None,proxy=None,ping_interval=None,
            open_timeout=remaining(local_end),close_timeout=1,max_size=1024*1024,max_queue=4,logger=logger)
        remaining(local_end)
        rpc=_Rpc(ws,deadline,result['envelopes'],local_end)
        stage='initialize'
        init=await rpc.call('initialize',{'clientInfo':{'name':'g0_synthetic_transport','version':'1'},'capabilities':{'experimentalApi':True}})
        require(init.get('codexHome')==str(prepared.spec.codex_home),'initialize home mismatch');result['initialize_home_match']=True
        await rpc.initialized()
        stage='thread/start'
        created=await rpc.call('thread/start',{'cwd':str(prepared.spec.workspace),'model':'gpt-5.6-luna','modelProvider':'synthetic',
            'ephemeral':False,'historyMode':'legacy','approvalPolicy':'never'})
        remaining(local_end);rpc.startup_deadline=None
        thread=created.get('thread',{});thread_id=thread.get('id')
        require(isinstance(thread_id,str) and thread_id and thread.get('cwd')==str(prepared.spec.workspace),'thread owner mismatch')
        result['thread_id']=thread_id
        for index in range(2):
            stage=f'turn/start/{index+1}'
            params={'threadId':thread_id,'input':[{'type':'text','text':INITIAL}] if index==0 else []}
            if index==1:params['toolOutput']={'name':'g0_delivery','namespace':'orchestration','output':TOOL_OUTPUT}
            receipt=await rpc.call('turn/start',params);turn_id=receipt.get('turn',{}).get('id')
            require(isinstance(turn_id,str) and turn_id and turn_id not in result['turn_ids'],'turn receipt identity invalid')
            result['turn_ids'].append(turn_id)
            stage=f'turn/completed/{index+1}';await rpc.completed(thread_id,turn_id)
        stage='thread/read'
        history=await rpc.call('thread/read',{'threadId':thread_id,'includeTurns':True})
        result['item_ids']=_history(history.get('thread',{}),thread_id,result['turn_ids'],prepared.spec.workspace)
        result['history_verified']=True;result['history_sha256']=digest(encoded(history))
        require(prepared.endpoint.snapshot()['accepted_requests']==2,'synthetic response count mismatch')
        prepared.verify()
        require(plan.source_hashes==source_pins(),'source pins changed during run')
        require(_native_check(plan.native,NATIVE_SHA256)==plan.native_identity,'native artifact changed during run')
        result['posthash_verified']=True;remaining(deadline);result['status']='passed'
    except Exception as exc:
        result['failure']={'stage':stage,'type':type(exc).__name__,'errno':getattr(exc,'errno',None),'message_sha256':digest(str(exc).encode())}
    finally:
        if ws is not None:
            try:await asyncio.wait_for(ws.close(),min(1,max(.001,deadline-now())))
            except Exception:pass
        elif raw is not None:raw.close()
        cleanup={'child_reaped':False,'socket_removed':False,'tree_stop_unproven':True,'restart_allowed':False}
        if child is not None:
            outcome=await child.stop(min(LOCAL_SECONDS,max(0,deadline-now())))
            cleanup.update(outcome)
            if outcome.get('state')!='stopped':result['status']='unknown'
            try:result['output_digests']=await asyncio.wait_for(asyncio.gather(*outputs),min(1,max(.001,deadline-now())))
            except Exception:result['output_digests_complete']=False
        if peer_verified and cleanup['child_reaped'] and socket_identity is not None:
            try:
                require(_socket_identity(prepared.spec.backend_socket,backend_fd)==socket_identity,'private socket identity changed')
                require(_dir_identity(prepared.spec.backend_socket.parent)==prepared._directories[prepared.spec.backend_socket.parent],'private socket parent changed')
                os.unlink(prepared.spec.backend_socket.name,dir_fd=backend_fd);cleanup['socket_removed']=True
            except FileNotFoundError:cleanup['socket_removed']=True
            except Exception:result['status']='unknown'
        result['cleanup']=cleanup
        if result['status']=='passed' and not (cleanup['child_reaped'] and cleanup['socket_removed']):result['status']='unknown'
        result['synthetic_endpoint']=prepared.endpoint.snapshot()
        try:prepared.endpoint.close()
        except Exception as exc:
            result['status']='unknown'
            result['endpoint_close_failure']={'type':type(exc).__name__,'errno':getattr(exc,'errno',None)}
        result['finished_raw']=now()
        if result['finished_raw']>=deadline:
            result['status']='unknown';result['deadline_exceeded']=True
        try:_write(directory_fd,'result.json',result)
        finally:
            os.close(directory_fd)
            if backend_fd is not None:os.close(backend_fd)
    return result
