"""Auth-scoped socket activation with a private backend process per admitted peer.

No token, RPC body or inherited environment is recorded.  Auth confinement is
provided by the separately reviewed auth_isolation launch-plan adapter.
"""
from __future__ import annotations

import asyncio
from contextlib import asynccontextmanager
import hashlib
import json
import os
from pathlib import Path
import socket
import stat
import sys
import time
import uuid

_REPO = Path(__file__).resolve().parents[4]
for _directory in (_REPO/'tasks/g0-tui-proxy/scripts', _REPO/'tasks/g0-auth-preserving-activation/scripts', _REPO/'tasks/g0-completion/scripts'):
    if str(_directory) not in sys.path:
        sys.path.insert(0,str(_directory))
from proxy_transport import BackendConnection, CaptureSink, ProxyServer, _process_metadata, peer_identity
from proxy_observer import Observer
from owner_helper import HelperIdentity, NativeWebSocketGate, OwnerHelperGrant, OwnerLease
from owned_child_guard import GuardedChild

LOCAL_SECONDS = 10.0


def _canonical_path(path: Path) -> None:
    if not path.is_absolute() or Path(os.path.abspath(path))!=path or path.resolve()!=path:
        raise ValueError('policy path must be absolute without aliases or symlinks')
    if any(part.is_symlink() for part in (path,*path.parents)):
        raise ValueError('symlink policy component is forbidden')


def _sha(path: Path) -> str:
    _canonical_path(path)
    descriptor=os.open(path,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
    digest=hashlib.sha256(); size=0
    with os.fdopen(descriptor,'rb') as stream:
        before=os.fstat(stream.fileno())
        if not stat.S_ISREG(before.st_mode): raise ValueError('pin is not a regular file')
        for chunk in iter(lambda:stream.read(1024*1024),b''):
            digest.update(chunk); size+=len(chunk)
        after=os.fstat(stream.fileno())
    if _file_identity(before)!=_file_identity(after) or size!=before.st_size:
        raise ValueError('pinned file changed while hashing')
    return digest.hexdigest()


def _file_identity(info):
    return tuple(getattr(info,key) for key in ('st_dev','st_ino','st_uid','st_mode','st_size','st_mtime_ns','st_ctime_ns'))


def _private_directory(path: Path):
    _canonical_path(path)
    info=path.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid!=os.getuid() or info.st_mode & 0o077:
        raise ValueError('expected an existing owner-only real directory')
    return (info.st_dev,info.st_ino,info.st_uid,info.st_mode)


def _private_json(path: Path) -> tuple[dict,str]:
    _canonical_path(path)
    descriptor=os.open(path,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
    with os.fdopen(descriptor,'rb') as stream:
        info=os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_uid!=os.getuid() or info.st_mode & 0o077 or info.st_size>1024*1024:
            raise ValueError('expected a bounded owner-only policy file')
        data=stream.read(1024*1024+1)
        after=os.fstat(stream.fileno())
        if _file_identity(info)!=_file_identity(after) or len(data)!=info.st_size:
            raise ValueError('policy changed while reading')
    result=json.loads(data)
    if not isinstance(result,dict): raise ValueError('policy must be an object')
    return result,hashlib.sha256(data).hexdigest()


def _write_json(path: Path, value: dict) -> None:
    fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600)
    with os.fdopen(fd,'w') as stream:
        json.dump(value,stream,sort_keys=True,indent=2); stream.write('\n')


def _peer(peer) -> dict:
    return {key:getattr(peer,key,None) for key in ('pid','uid','birth','executable','complete','source')}


def _same_process(peer,pid,birth,executable) -> bool:
    return bool(peer.complete and peer.pid==pid and peer.uid==os.getuid() and peer.birth==birth
                and peer.executable and os.path.realpath(peer.executable)==os.path.realpath(executable))


class _ActivationObserver(Observer):
    """Reuse the fixed bounded decoder; add only initialize home equality/digest."""
    def __init__(self,expected_home,**kwargs):
        self.expected_home=expected_home
        super().__init__(**kwargs)

    def _process_response(self,connection,direction,value):
        request_id=value.get('id')
        matched=(direction=='server' and self._valid_id(request_id)
                 and connection.pending.get(('client',request_id))=='initialize')
        super()._process_response(connection,direction,value)
        if matched and connection.valid and 'error' not in value:
            result=value.get('result')
            home=self._safe_string(result.get('codexHome')) if isinstance(result,dict) else None
            self._emit('initialize_home',connection,direction,request_id=request_id,
                home_match=home is not None and home==self.expected_home(connection.epoch),
                home_sha256=hashlib.sha256(home.encode()).hexdigest() if home is not None else None)

    def complete_boundary(self,connection_id):
        connection=self._connections.get(connection_id)
        return bool(connection and connection.valid and not connection.closed
            and all(parser.handshake_done and not parser.buffer and parser.fragment_opcode is None
                    for parser in connection.directions.values()))


class MetadataSink(CaptureSink):
    def __init__(self,*,lease_for_connection=None,publish_ready=None,observer_limits=None):
        self.events=[]
        self.streams={}
        self._leases=lease_for_connection
        self._publish_ready=publish_ready
        self._protocol={}
        self._observer=_ActivationObserver(self._expected_home,emit=self._protocol_event,**(observer_limits or {}))

    def _expected_home(self,epoch):
        return self._protocol.get(epoch,{}).get('lease',{}).get('expected_home')

    def on_connect(self,event):
        self.events.append({'kind':'connect','connection_id':event.connection_id,'epoch':event.epoch,
            'frontend':_peer(event.frontend_peer),'backend':_peer(event.backend_peer)})
        lease=self._leases(event) if self._leases is not None else None
        self._protocol[event.epoch]={'connection_id':event.connection_id,'epoch':event.epoch,
            'lease':lease or {},'frontend':_peer(event.frontend_peer),'backend':_peer(event.backend_peer),
            'protocol_valid':lease is not None,'closed':False,'upgrades':set(),'initialize':{},
            'initialized':False,'thread':{},'turn_counts':{'start':0,'steer':0},'rpc':[],
            'ready_published':False}
        self._observer.open(event.connection_id,conn_epoch=event.epoch)

    def on_data(self,event):
        key=(event.connection_id,event.direction)
        count,digest=self.streams.setdefault(key,[0,hashlib.sha256()])
        digest.update(event.data)
        self.streams[key][0]=count+len(event.data)
        state=self._protocol.get(event.epoch)
        if state is None or state['connection_id']!=event.connection_id:
            return
        direction={'frontend_to_backend':'client','backend_to_frontend':'server'}.get(event.direction)
        self._observer.feed(event.connection_id,direction,event.data)
        # Inspect only after the whole chunk parsed, so trailing invalid/partial
        # frames in the same callback cannot publish an early ready receipt.
        if self._ready(state) and self._observer.complete_boundary(event.connection_id):
            if self._publish_ready is not None:
                self._publish_ready(dict(self._public_protocol(state),ready_published=True,milestone_only=True))
            state['ready_published']=True

    def _protocol_event(self,row):
        state=self._protocol.get(row.get('conn_epoch'))
        if state is None: return
        event=row.get('event'); method=row.get('method'); request_id=row.get('request_id')
        if event=='gap':
            state['protocol_valid']=False
            state['failure_reason']=row.get('reason','decoder_gap')
        elif event=='connection_close': state['closed']=True
        elif event=='websocket_upgrade': state['upgrades'].add(row.get('direction'))
        elif event=='initialize_home':
            state['initialize'].update(request_id=request_id,home_match=row['home_match'],home_sha256=row['home_sha256'])
            if not row['home_match']: state['protocol_valid']=False
        elif event in ('rpc','rpc_response','rpc_unknown'):
            if len(state['rpc'])>=4096:
                state['protocol_valid']=False
                return
            state['rpc'].append({key:row[key] for key in ('event','direction','method','request_id','request_direction','ok','response') if key in row})
            if method in ('turn/start','turn/steer') and not row.get('response') and event!='rpc_response':
                state['turn_counts'][method.split('/')[1]]+=1
            if event=='rpc_unknown' and row.get('response') and method is None:
                state['protocol_valid']=False
            if event=='rpc' and row.get('direction')=='client':
                if method=='initialize':
                    if 'request_id' in state['initialize']: state['protocol_valid']=False
                    state['initialize']['request_id']=request_id
                elif method=='initialized':
                    if state['initialized'] or state['initialize'].get('home_match') is not True:
                        state['protocol_valid']=False
                    state['initialized']=True
                elif method=='thread/start':
                    if not state['initialized'] or state['thread'].get('id') is not None:
                        state['protocol_valid']=False
                    if row.get('params',{}).get('cwd')!=state['lease'].get('expected_cwd'):
                        state['protocol_valid']=False
                    state['thread']['start_request_id']=request_id
            elif event=='rpc_response' and method=='thread/start' and row.get('ok') is True:
                thread=row.get('thread',{}); identifier=thread.get('id')
                if (row.get('direction')!='server' or row.get('request_direction')!='client'
                    or state['thread'].get('start_request_id')!=request_id
                    or not self._thread_id(identifier) or thread.get('cwd')!=state['lease'].get('expected_cwd')
                    or state['thread'].get('id') is not None):
                    state['protocol_valid']=False
                else: state['thread']['id']=identifier
            elif event=='rpc' and method=='thread/started':
                identifier=row.get('thread',{}).get('id')
                if (row.get('direction')!='server' or not self._thread_id(identifier)
                    or state['thread'].get('id')!=identifier or 'started_id' in state['thread']):
                    state['protocol_valid']=False
                else: state['thread']['started_id']=identifier

    @staticmethod
    def _thread_id(value):
        try: return isinstance(value,str) and str(uuid.UUID(value))==value
        except ValueError: return False

    @staticmethod
    def _ready(state):
        return (state['protocol_valid'] and not state['closed'] and not state['ready_published']
            and state['upgrades']=={'client','server'} and state['initialized']
            and state['initialize'].get('home_match') is True and state['thread'].get('id') is not None
            and state['thread'].get('id')==state['thread'].get('started_id')
            and not any(state['turn_counts'].values()))

    @staticmethod
    def _public_protocol(state):
        return {**{key:state[key] for key in ('connection_id','epoch','frontend','backend','protocol_valid','closed','initialized','ready_published')},
            'lease_id':state['lease'].get('lease_id'),'profile_id':state['lease'].get('profile_id'),
            'initialize':dict(state['initialize']),'thread':dict(state['thread']),
            'turn_counts':dict(state['turn_counts']),'zero_turns':not any(state['turn_counts'].values()),
            'rpc':list(state['rpc']),**({'failure_reason':state['failure_reason']} if 'failure_reason' in state else {})}

    def protocol_for_lease(self,lease_id):
        return [self._public_protocol(state) for state in self._protocol.values() if state['lease'].get('lease_id')==lease_id]

    def on_lifecycle(self,event):
        self.events.append({'kind':event.kind,'connection_id':event.connection_id,'epoch':event.epoch,'direction':event.direction})
        if event.kind in ('eof','disconnect'):
            self._observer.close(event.connection_id)

    def on_gap(self,event):
        self.events.append({'kind':'gap','connection_id':event.connection_id,'epoch':event.epoch,'reason':event.reason,'fatal':event.fatal})
        state=self._protocol.get(event.epoch)
        if state is not None: state['protocol_valid']=False

    def snapshot(self):
        return {'events':list(self.events),'streams':[{'connection_id':key[0],'direction':key[1],
            'bytes':value[0],'sha256':value[1].hexdigest()} for key,value in sorted(self.streams.items())],
            'protocol':[self._public_protocol(state) for state in self._protocol.values()]}


class ActivationService:
    def __init__(self,manifest: dict,manifest_sha256: str):
        import auth_isolation
        self.auth=auth_isolation
        if manifest.get('version')!=1: raise ValueError('unsupported activation manifest')
        self.manifest=manifest
        self.manifest_sha256=manifest_sha256
        self.public_socket=Path(manifest['public_socket'])
        self.state_dir=Path(manifest['state_dir'])
        self.grants_dir=Path(manifest['grants_dir'])
        self.isolation_path=Path(manifest['isolation_manifest'])
        if not all(p.is_absolute() for p in (self.public_socket,self.state_dir,self.grants_dir,self.isolation_path)):
            raise ValueError('activation paths must be absolute')
        if len(os.fsencode(self.public_socket))>=104: raise ValueError('public Unix socket path is too long')
        for path in (self.state_dir,self.grants_dir): _private_directory(path)
        profiles,actual=_private_json(self.isolation_path)
        if actual!=manifest['isolation_manifest_sha256']: raise ValueError('isolation manifest changed')
        self.contexts={name:self.auth.load_isolation_context(self.isolation_path,name) for name in profiles['profiles']}
        if _sha(self.isolation_path)!=actual: raise ValueError('isolation manifest changed while loading')
        self.argv=tuple(manifest['backend_argv'])
        if not self.argv or any(not isinstance(a,str) or not a for a in self.argv): raise ValueError('invalid backend argv')
        if sum(a.count('{socket_path}') for a in self.argv)!=1: raise ValueError('backend requires exactly one private socket argument')
        self.executable=Path(self.argv[0]).resolve()
        if _sha(self.executable)!=manifest['backend_executable_sha256']: raise ValueError('backend executable changed')
        self.file_pins={Path(name):value for name,value in manifest.get('file_pins',{}).items()}
        self._check_pins()
        self.idle_seconds=float(manifest.get('idle_seconds',LOCAL_SECONDS))
        if not 0<self.idle_seconds<=LOCAL_SECONDS: raise ValueError('idle window exceeds local budget')
        for context in self.contexts.values():
            if context.spec.public_socket!=self.public_socket: raise ValueError('profile public endpoint mismatch')
            if context.expected_executable.resolve()!=self.executable: raise ValueError('profile backend executable mismatch')
            for path in (context.spec.task_root,context.spec.home,context.spec.codex_home,context.spec.workspace): _private_directory(path)
        self._directory_pins={path:_private_directory(path) for path in (self.state_dir,self.grants_dir)}
        self._config_pins={}
        for context in self.contexts.values():
            for path in (context.spec.task_root,context.spec.home,context.spec.codex_home,context.spec.workspace,context.spec.backend_socket.parent):
                self._directory_pins[path]=_private_directory(path)
            config=context.spec.codex_home/'config.toml'
            self._config_pins[config]=_sha(config)
        self.activation_id=uuid.uuid4().hex
        self.receipt_dir=self.state_dir/self.activation_id
        self.receipt_dir.mkdir(mode=0o700)
        self._directory_pins[self.receipt_dir]=_private_directory(self.receipt_dir)
        self.sink=MetadataSink(lease_for_connection=self._protocol_lease,publish_ready=self._publish_ready)
        self.backend_records=[]
        self._admissions={}
        self._helper_admissions={}
        self._owner_leases={}
        helper_pins=manifest.get('owner_helper',{})
        self._helper_pins=helper_pins if isinstance(helper_pins,dict) else {}
        self._profile_locks={name:asyncio.Lock() for name in self.contexts}
        self._closed=False
        self._close_task=None
        self.proxy=ProxyServer(self.public_socket,None,self.sink,authorize_peer=self._admit,
            backend_factory=self._backend,stop_on_frontend_eof=True,callback_timeout=LOCAL_SECONDS,
            wire_gate_factory=self._wire_gate_factory)

    def _protocol_lease(self,event):
        for record in self.backend_records:
            if (record.get('state')=='connected' and record.get('pid')==event.backend_peer.pid
                and record.get('birth')==event.backend_peer.birth
                and record['frontend']==_peer(event.frontend_peer)):
                context=self.contexts[record['profile_id']]
                return {'lease_id':record['lease_id'],'profile_id':record['profile_id'],
                    'expected_home':str(context.spec.codex_home),'expected_cwd':str(context.spec.workspace)}
        return None

    def record_startup_failure(self,stage,exc):
        self._check_roots()
        _write_json(self.receipt_dir/'startup-failure.json',{
            'version':1,'activation_id':self.activation_id,'manifest_sha256':self.manifest_sha256,
            'stage':stage,'failure_type':type(exc).__name__,
            'errno':exc.errno if isinstance(exc,OSError) else None})

    def _publish_ready(self,protocol):
        self._check_runtime_policy()
        row=dict(protocol,version=1,activation_id=self.activation_id,manifest_sha256=self.manifest_sha256)
        record=self._record_for_lease(protocol.get('lease_id'))
        thread_id=protocol.get('thread',{}).get('id') if isinstance(protocol.get('thread'),dict) else None
        if (record is not None and record.get('state')=='connected' and isinstance(thread_id,str)
                and all(key in record for key in ('private_socket','pid','birth'))):
            context=self.contexts[record['profile_id']]
            owner_context_sha256=self._owner_context_sha(context)
            record.update(owner_connection_id=protocol.get('connection_id'),owner_epoch=protocol.get('epoch'),
                owner_thread_id=thread_id,owner_context_sha256=owner_context_sha256)
            self._owner_leases[record['lease_id']]=OwnerLease(
                profile_id=record['profile_id'],owner_context_sha256=owner_context_sha256,
                lease_id=record['lease_id'],owner_connection_id=record['owner_connection_id'],
                owner_epoch=record['owner_epoch'],owner_thread_id=thread_id,
                private_socket=record['private_socket'],backend_pid=record['pid'],
                backend_birth=record['birth'],helper_identity=None)
        path=self.receipt_dir/f"ready-{protocol['lease_id']}.json"
        stage=self.receipt_dir/f".ready-{uuid.uuid4().hex}.json"
        try:
            _write_json(stage,row)
            with stage.open('rb') as stream: os.fsync(stream.fileno())
            os.link(stage,path,follow_symlinks=False)
        finally:
            if stage.exists(): stage.unlink()

    @classmethod
    def from_manifest(cls,path: Path):
        manifest,digest=_private_json(Path(path))
        return cls(manifest,digest)

    def _check_pins(self):
        if _sha(self.executable)!=self.manifest['backend_executable_sha256']:
            raise ValueError('backend executable changed')
        if any(_sha(path)!=expected for path,expected in self.file_pins.items()):
            raise ValueError('backend implementation changed')

    def _check_roots(self):
        if any(_private_directory(path)!=identity for path,identity in self._directory_pins.items()):
            raise ValueError('activation directory identity changed')

    def _check_runtime_policy(self):
        self._check_roots()
        if _private_json(self.isolation_path)[1]!=self.manifest['isolation_manifest_sha256']:
            raise ValueError('isolation manifest changed after admission')
        if any(_sha(path)!=digest for path,digest in self._config_pins.items()):
            raise ValueError('isolated profile configuration changed')
        self._check_pins()

    def _wire_gate_factory(self, frontend_peer, backend_peer, connection_id, epoch):
        admission=self._helper_admissions.get((frontend_peer.pid,frontend_peer.birth,frontend_peer.executable))
        if admission is None:
            return None
        grant,_=admission
        pending=set()
        return (NativeWebSocketGate(grant['owner_thread_id'],server=False,pending_ids=pending),
            NativeWebSocketGate(grant['owner_thread_id'],server=True,pending_ids=pending))

    def _owner_context_sha(self, context):
        config_path=context.spec.codex_home/'config.toml'
        config_sha=self._config_pins.get(config_path) or _sha(config_path)
        value={'profile_id':context.spec.profile_id,'home':str(context.spec.home),
            'codex_home':str(context.spec.codex_home),'workspace':str(context.spec.workspace),
            'backend_socket':str(context.spec.backend_socket),'config_sha256':config_sha,
            'expected_executable_sha256':context.expected_executable_sha256}
        return hashlib.sha256(json.dumps(value,sort_keys=True,separators=(',',':')).encode()).hexdigest()

    def _record_for_lease(self, lease_id):
        return next((record for record in self.backend_records if record.get('lease_id')==lease_id), None)

    def _helper_admission(self, peer, grant, grant_sha):
        required={'version','role','profile_id','owner_context_sha256','lease_id','owner_connection_id',
            'owner_epoch','owner_thread_id','private_socket','helper_pid','helper_uid','helper_birth',
            'helper_executable','helper_executable_sha256','helper_source_sha256'}
        if set(grant)!=required or type(grant.get('version')) is not int or grant.get('version')!=1 or grant.get('role')!='owner-helper': return False
        if grant.get('helper_pid')!=peer.pid or grant.get('helper_uid')!=peer.uid or grant.get('helper_birth')!=peer.birth: return False
        if grant.get('helper_executable')!=peer.executable or not peer.complete: return False
        pins=self._helper_pins
        if not all(isinstance(pins.get(key),str) and pins.get(key) for key in ('executable','executable_sha256','source_sha256')): return False
        if grant['helper_executable']!=pins['executable'] or grant['helper_source_sha256']!=pins['source_sha256']: return False
        try:
            if grant['helper_executable_sha256']!=pins['executable_sha256'] or _sha(Path(peer.executable))!=pins['executable_sha256']: return False
            current_birth,current_executable=_process_metadata(peer.pid)
            if current_birth!=peer.birth or current_executable!=peer.executable: return False
        except (OSError,ValueError): return False
        record=self._record_for_lease(grant['lease_id']); lease=self._owner_leases.get(grant['lease_id'])
        if record is None or lease is None or record.get('state')!='connected': return False
        context=self.contexts.get(grant['profile_id'])
        if context is None or record.get('profile_id')!=grant['profile_id']: return False
        if grant['owner_context_sha256']!=self._owner_context_sha(context): return False
        if grant['private_socket']!=record.get('private_socket') or grant['private_socket']!=lease.private_socket: return False
        if grant['owner_connection_id']!=record.get('owner_connection_id') or grant['owner_epoch']!=record.get('owner_epoch') or grant['owner_thread_id']!=record.get('owner_thread_id'): return False
        try:
            info=os.lstat(grant['private_socket']); frozen=record.get('private_socket_identity')
            if frozen is None or (info.st_dev,info.st_ino,info.st_uid,info.st_mode)!=tuple(frozen): return False
            identity=HelperIdentity(pid=peer.pid,uid=peer.uid,birth=peer.birth,executable=peer.executable,
                executable_sha256=grant['helper_executable_sha256'],source_sha256=grant['helper_source_sha256'])
            lease.validate_grant(OwnerHelperGrant(**grant),identity)
        except (OSError,ValueError,KeyError,TypeError): return False
        key=(peer.pid,peer.birth,peer.executable); previous=self._helper_admissions.get(key)
        if previous is not None and previous[1]!=grant_sha: return False
        self._helper_admissions[key]=(dict(grant),grant_sha)
        return True

    def _admit(self,peer):
        if not peer.complete or peer.uid!=os.getuid(): return False
        try:
            self._check_roots()
            current_birth,current_executable=_process_metadata(peer.pid)
            if current_birth!=peer.birth or current_executable!=peer.executable: return False
            grant,digest=_private_json(self.grants_dir/f'{peer.pid}.json')
            if grant.get('role')=='owner-helper':
                return self._helper_admission(peer,grant,digest)
            profile=grant.get('profile_id')
            allowed=(grant.get('version')==1 and grant.get('pid')==peer.pid and grant.get('uid')==peer.uid
                and grant.get('birth')==peer.birth and grant.get('expected_executable')==peer.executable
                and grant.get('executable_sha256')==_sha(Path(peer.executable)) and profile in self.contexts)
            if not allowed: return False
            key=(peer.pid,peer.birth,peer.executable)
            previous=self._admissions.setdefault(key,(profile,digest))
            return previous==(profile,digest)
        except (OSError,ValueError,KeyError,TypeError):
            return False

    async def _output_digest(self,stream):
        digest=hashlib.sha256(); size=0
        while chunk:=await stream.read(65536): size+=len(chunk); digest.update(chunk)
        return {'bytes':size,'sha256':digest.hexdigest()}

    @asynccontextmanager
    async def _backend(self,frontend):
        key=(frontend.pid,frontend.birth,frontend.executable)
        if key in self._helper_admissions:
            grant,grant_sha=self._helper_admissions[key]
            profile=grant['profile_id']; context=self.contexts[profile]
            self._check_runtime_policy()
            if not self._admit(frontend) or self._helper_admissions[key][1]!=grant_sha:
                raise ValueError('helper grant changed before connection')
            async with self._leased_helper(frontend,grant,context) as lease:
                yield lease
            return
        # Admission is reusable for a no-RPC startup probe, but cannot be remapped.
        profile,grant_sha=self._admissions[key]
        context=self.contexts[profile]
        start=time.monotonic(); deadline=start+LOCAL_SECONDS
        lock=self._profile_locks[profile]
        await asyncio.wait_for(lock.acquire(),max(0,deadline-time.monotonic()))
        try:
            self._check_runtime_policy()
            if not self._admit(frontend) or self._admissions[key]!=(profile,grant_sha):
                raise ValueError('frontend grant changed before backend start')
            async with self._leased_backend(frontend,profile,grant_sha,context,start,deadline) as lease:
                yield lease
        finally:
            lock.release()

    @asynccontextmanager
    async def _leased_helper(self,frontend,grant,context):
        lease=self._owner_leases.get(grant['lease_id'])
        record=self._record_for_lease(grant['lease_id'])
        if lease is None or record is None or record.get('state')!='connected':
            raise ValueError('owner lease is not active')
        identity=HelperIdentity(pid=frontend.pid,uid=frontend.uid,birth=frontend.birth,executable=frontend.executable,
            executable_sha256=grant['helper_executable_sha256'],source_sha256=grant['helper_source_sha256'])
        helper=None
        helper_record={'role':'owner-helper','lease_id':grant['lease_id'],
            'owner_connection_id':grant['owner_connection_id'],'owner_epoch':grant['owner_epoch'],
            'owner_thread_id':grant['owner_thread_id'],'frontend':_peer(frontend),'state':'opening'}
        record.setdefault('helpers',[]).append(helper_record)
        try:
            helper=await lease.open_helper(OwnerHelperGrant(**grant),identity)
            helper_record['state']='connected'
            backend_socket=helper.writer.get_extra_info('socket')
            backend_peer=peer_identity(backend_socket)
            if not backend_peer.complete or not _same_process(backend_peer,record['pid'],record['birth'],self.executable):
                raise ValueError('helper backend peer identity mismatch')
            info=os.lstat(grant['private_socket'])
            frozen=record.get('private_socket_identity')
            if frozen is None or (info.st_dev,info.st_ino,info.st_uid,info.st_mode)!=tuple(frozen):
                raise ValueError('helper backend socket identity changed')
            yield BackendConnection(helper.reader,helper.writer,
                lambda peer:_same_process(peer,record['pid'],record['birth'],self.executable))
        finally:
            if helper is not None:
                await helper.close()
            helper_record['state']='closed'

    @asynccontextmanager
    async def _leased_backend(self,frontend,profile,grant_sha,context,start,deadline):
        lease_id=uuid.uuid4().hex[:12]
        lease_dir=self.receipt_dir/lease_id
        lease_dir.mkdir(mode=0o700)
        private_socket=context.spec.backend_socket.parent/f'b-{lease_id}.sock'
        _private_directory(private_socket.parent)
        if len(os.fsencode(private_socket))>=104: raise ValueError('private Unix socket path is too long')
        if os.path.lexists(private_socket): raise FileExistsError('private backend path already exists')
        record={'lease_id':lease_id,'profile_id':profile,'grant_sha256':grant_sha,'frontend':_peer(frontend),
            'private_socket':str(private_socket),'state':'starting','process_stopped':False,'socket_removed':False}
        self.backend_records.append(record)
        process=None; birth=None; creation_birth=None; identity=None; outputs=[]; writer=None; launch_attempted=False
        try:
            self._check_pins()
            argv=[argument.replace('{socket_path}',str(private_socket)) for argument in self.argv]
            plan=self.auth.build_backend_launch(context,argv,private_socket)
            sandbox_path=lease_dir/'backend.sbpl'
            fd=os.open(sandbox_path,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600)
            with os.fdopen(fd,'w') as stream: stream.write(plan['sandbox_profile'])
            if time.monotonic()>=deadline: raise TimeoutError('backend start deadline elapsed before spawn')
            launch=['/usr/bin/sandbox-exec','-f',str(sandbox_path),*plan['argv']]
            launch_attempted=True
            spawn=asyncio.create_task(GuardedChild.spawn(launch,cwd=plan['cwd'],env=plan['env'],receipt_dir=lease_dir,
                stdin=asyncio.subprocess.DEVNULL,stdout=asyncio.subprocess.PIPE,stderr=asyncio.subprocess.PIPE,
                ))
            try:
                process=await asyncio.shield(spawn)
            except asyncio.CancelledError:
                process=await spawn
                raise
            finally:
                if process is not None:
                    creation_birth,creation_executable=_process_metadata(process.pid)
                    record.update(pid=process.pid,guardian_pid=process.guardian_pid,creation_birth=creation_birth,creation_executable=creation_executable)
                    outputs=[asyncio.create_task(self._output_digest(stream)) for stream in (process.stdout,process.stderr)]
            if not process.identity_known:
                raise OSError('guardian child identity is unknown')
            if not creation_birth and process.returncode is None:
                raise OSError('owned child creation identity unavailable')
            while time.monotonic()<deadline:
                if process.returncode is not None: raise OSError('isolated backend exited before accepting')
                observed_birth,executable=_process_metadata(process.pid)
                if observed_birth==creation_birth and executable and os.path.realpath(executable)==str(self.executable): birth=observed_birth
                if birth and private_socket.exists():
                    info=private_socket.lstat()
                    if not stat.S_ISSOCK(info.st_mode) or info.st_uid!=os.getuid() or info.st_mode&0o077:
                        raise ValueError('backend socket is not private and owned')
                    identity=(info.st_dev,info.st_ino,info.st_uid,info.st_mode)
                    try:
                        reader,writer=await asyncio.wait_for(asyncio.open_unix_connection(private_socket),max(0,deadline-time.monotonic()))
                        break
                    except (ConnectionRefusedError,FileNotFoundError): pass
                await asyncio.sleep(min(.01,max(0,deadline-time.monotonic())))
            else: raise TimeoutError('isolated backend exceeded original local start window')
            record.update(state='connected',birth=birth,executable=str(self.executable),
                private_socket_identity=identity,backend_peer=_peer(peer_identity(writer.get_extra_info('socket'))))
            yield BackendConnection(reader,writer,lambda peer:_same_process(peer,process.pid,birth,self.executable))
        except BaseException as exc:
            if isinstance(exc,asyncio.CancelledError) and self._closed:
                record['state']='closing'
            else:
                record.update(state='failed',failure_type=type(exc).__name__)
            raise
        finally:
            cleanup_deadline=getattr(self,'_cleanup_deadline',time.monotonic()+LOCAL_SECONDS)
            owner_lease=self._owner_leases.pop(lease_id,None)
            if owner_lease is not None:
                await owner_lease.close()
            for key, admission in tuple(self._helper_admissions.items()):
                if admission[0].get('lease_id')==lease_id:
                    self._helper_admissions.pop(key,None)
            if writer is not None: writer.close()
            if process is not None:
                guarded=await process.stop(max(0,cleanup_deadline-time.monotonic()))
                record['guardian']=guarded
                record['returncode']=guarded.get('child_returncode')
                record['process_stopped']=guarded.get('child_reaped') is True
                record['tree_stop_unproven']=True
                record['restart_allowed']=False
                if guarded.get('state')!='stopped': record['cleanup_failure']='guardian_ownership_unknown'
                try:
                    digests=await asyncio.wait_for(asyncio.gather(*outputs),min(1.0,max(0,cleanup_deadline-time.monotonic())))
                    record['stdout'],record['stderr']=digests
                except TimeoutError:
                    record['output_digest_complete']=False
            elif launch_attempted:
                record['cleanup_failure']='guardian_launch_unproven'
                record['process_stopped']=False
                record['restart_allowed']=False
            else: record['process_stopped']=True
            if writer is not None:
                try: await asyncio.wait_for(writer.wait_closed(),min(1.0,max(0,cleanup_deadline-time.monotonic())))
                except (TimeoutError,ConnectionError,OSError):
                    writer.transport.abort()
            if identity is not None:
                try:
                    info=private_socket.lstat()
                    if (info.st_dev,info.st_ino,info.st_uid,info.st_mode)==identity:
                        private_socket.unlink(); record['socket_removed']=True
                    else: record['cleanup_failure']='backend_socket_identity_changed'
                except FileNotFoundError: record['socket_removed']=True
            elif not os.path.lexists(private_socket): record['socket_removed']=True
            if record['state']!='failed': record['state']='closed'
            record['protocol']=self.sink.protocol_for_lease(lease_id)
            record['elapsed_seconds']=time.monotonic()-start
            self._check_roots()
            _write_json(self.receipt_dir/f'backend-{lease_id}.json',record)

    async def start(self,listener: socket.socket):
        await self.proxy.start(inherited_listener=listener)
        return self

    async def serve_until_idle(self,listener: socket.socket):
        await self.start(listener)
        idle_since=time.monotonic()
        while True:
            if self.proxy.active_frontends: idle_since=time.monotonic()
            elif time.monotonic()-idle_since>=self.idle_seconds: break
            await asyncio.sleep(.01)
        await self.close()

    def snapshot(self):
        return {'activation_id':self.activation_id,'manifest_sha256':self.manifest_sha256,
            'backend_records':self.backend_records,'transport':self.sink.snapshot(),
            'public_socket_owned':False,'native_registration_verified':False,'stop_signals':getattr(self,'stop_signals',[])}

    async def close(self):
        if self._close_task is None:
            self._closed=True
            self._cleanup_deadline=time.monotonic()+LOCAL_SECONDS
            self._close_task=asyncio.create_task(self._finish_close())
        await asyncio.shield(self._close_task)

    async def _finish_close(self):
        await self.proxy.close()
        self._check_roots()
        _write_json(self.receipt_dir/'activation.json',self.snapshot())
