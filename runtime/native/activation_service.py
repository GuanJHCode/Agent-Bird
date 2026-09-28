"""Auth-scoped socket activation with a private backend process per admitted peer.

No token, RPC body or inherited environment is recorded.  Auth confinement is
provided by the separately reviewed auth_isolation launch-plan adapter.
"""
from __future__ import annotations
import re

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

_RUNTIME = Path(__file__).resolve().parent
if str(_RUNTIME) not in sys.path:
    sys.path.insert(0, str(_RUNTIME))
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


def _publish_receipt(path: Path, value: dict, *, expected_parent=None) -> None:
    """Publish one owner-created receipt without path-based replacement."""
    parent = path.parent
    directory_fd = os.open(parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    parent_info = os.fstat(directory_fd)
    if not stat.S_ISDIR(parent_info.st_mode) or parent_info.st_uid != os.getuid() or stat.S_IMODE(parent_info.st_mode) & 0o077:
        os.close(directory_fd)
        raise ValueError('receipt parent is not private')
    if expected_parent is not None and (parent_info.st_dev, parent_info.st_ino, parent_info.st_uid, parent_info.st_mode) != tuple(expected_parent):
        os.close(directory_fd)
        raise ValueError('receipt parent identity changed')
    stage_name = f".{path.name}.{uuid.uuid4().hex}.tmp"
    final_name = path.name
    data = (json.dumps(value, sort_keys=True, indent=2) + '\n').encode()
    descriptor = None
    created_stage = False
    stage_identity = None
    try:
        current = os.stat(parent, follow_symlinks=False)
        if _file_identity(current)[:4] != _file_identity(parent_info)[:4]:
            raise ValueError('receipt parent identity changed')
        descriptor = os.open(stage_name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=directory_fd)
        created_stage = True
        os.write(descriptor, data)
        os.fsync(descriptor)
        before = os.fstat(descriptor)
        if not stat.S_ISREG(before.st_mode) or before.st_uid != os.getuid() or stat.S_IMODE(before.st_mode) != 0o600 or before.st_nlink != 1 or before.st_size != len(data):
            raise ValueError('receipt stage identity')
        stage_identity = (before.st_dev, before.st_ino)
        os.link(stage_name, final_name, src_dir_fd=directory_fd, dst_dir_fd=directory_fd, follow_symlinks=False)
        staged = os.fstat(descriptor)
        final = os.stat(final_name, dir_fd=directory_fd, follow_symlinks=False)
        final_fd = os.open(final_name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=directory_fd)
        try:
            same_file = os.path.sameopenfile(descriptor, final_fd)
        finally:
            os.close(final_fd)
        if not stat.S_ISREG(final.st_mode) or not same_file:
            raise ValueError('receipt final identity')
        if (final.st_dev, final.st_ino, final.st_uid, final.st_mode) != (staged.st_dev, staged.st_ino, staged.st_uid, staged.st_mode):
            raise ValueError('receipt final identity')
        current = os.stat(parent, follow_symlinks=False)
        if _file_identity(current)[:4] != _file_identity(parent_info)[:4]:
            raise ValueError('receipt parent identity changed')
        os.fsync(directory_fd)
    finally:
        # A failed final validation is deliberately left in place as UNKNOWN
        # evidence.  Never stat-then-unlink the public receipt name.
        #
        # For our stage, first atomically move the current directory entry to
        # a fresh quarantine name.  Only the moved inode matching the still
        # open creation FD may be removed; a replaced/symlinked stage remains
        # in quarantine for diagnosis.
        cleanup_error = None
        if created_stage and stage_identity is not None and descriptor is not None:
            quarantine_name = f".{path.name}.{uuid.uuid4().hex}.quarantine"
            quarantine_dir_fd = None
            quarantine_entry_fd = None
            try:
                os.mkdir(quarantine_name, 0o700, dir_fd=directory_fd)
                quarantine_dir_fd = os.open(quarantine_name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW,
                    dir_fd=directory_fd)
                quarantine_info = os.fstat(quarantine_dir_fd)
                if (not stat.S_ISDIR(quarantine_info.st_mode)
                        or quarantine_info.st_uid != os.getuid()
                        or stat.S_IMODE(quarantine_info.st_mode) != 0o700):
                    raise RuntimeError('receipt stage ownership unknown')
                # Rename the current stage entry into the private directory.
                # The source path is never stat-then-unlinked.
                os.rename(stage_name, 'entry', src_dir_fd=directory_fd, dst_dir_fd=quarantine_dir_fd)
                quarantine_entry_fd = os.open('entry', os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK,
                    dir_fd=quarantine_dir_fd)
                moved = os.fstat(quarantine_entry_fd)
                original = os.fstat(descriptor)
                if (moved.st_dev, moved.st_ino) != (original.st_dev, original.st_ino):
                    raise RuntimeError('receipt stage ownership unknown')
                os.close(quarantine_entry_fd)
                quarantine_entry_fd = None
                os.unlink('entry', dir_fd=quarantine_dir_fd)
                os.fsync(quarantine_dir_fd)
                os.close(quarantine_dir_fd)
                quarantine_dir_fd = None
                os.rmdir(quarantine_name, dir_fd=directory_fd)
                os.fsync(directory_fd)
            except FileNotFoundError:
                pass
            except RuntimeError as exc:
                cleanup_error = exc
            except OSError:
                cleanup_error = RuntimeError('receipt stage ownership unknown')
            finally:
                if quarantine_entry_fd is not None:
                    os.close(quarantine_entry_fd)
                if quarantine_dir_fd is not None:
                    os.close(quarantine_dir_fd)
        if descriptor is not None:
            os.close(descriptor)
        os.close(directory_fd)
        if cleanup_error is not None:
            raise cleanup_error


def _peer(peer) -> dict:
    return {key:getattr(peer,key,None) for key in ('pid','uid','birth','executable','complete','source')}


def _same_process(peer,pid,birth,executable) -> bool:
    return bool(peer.complete and peer.pid==pid and peer.uid==os.getuid() and peer.birth==birth
                and peer.executable and os.path.realpath(peer.executable)==os.path.realpath(executable))


def _original_process_gone(pid,birth,executable):
    if type(pid) is not int or pid<=0 or not birth or not executable:raise ValueError('original identity incomplete')
    current_birth,current_executable=_process_metadata(pid)
    if current_birth and current_executable:return current_birth!=birth
    if current_birth is None and current_executable is None:
        try:os.kill(pid,0)
        except ProcessLookupError:return True
        except PermissionError:pass
    raise ValueError('original process state unknown')


def native_tui_policy(value):
    if value is None:return None
    expected={'version':1,'resume_owned_thread':True,'initial_history_discovery':True}
    if (not isinstance(value,dict) or value!=expected or type(value.get('version')) is not int
        or any(type(value.get(k)) is not bool for k in ('resume_owned_thread','initial_history_discovery'))):
        raise ValueError('invalid native TUI policy')
    return dict(expected)


def _chain_wire_gates(first, second):
    def chained(data):
        forwarded=first(data)
        if not isinstance(forwarded,bytes): raise ValueError('wire gate must return bytes')
        if not forwarded:return b''
        result=second(forwarded)
        if not isinstance(result,bytes): raise ValueError('wire gate must return bytes')
        return result
    return chained


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
                elif method=='thread/resume':
                    expected=state['lease'].get('expected_resume_thread_id')
                    if (not state['initialized'] or state['thread'] or not self._thread_id(expected)
                        or row.get('params',{}).get('threadId')!=expected):state['protocol_valid']=False
                    state['thread']['resume_request_id']=request_id
                elif method=='skills/list' and state['thread'].get('resume_response'):
                    state.setdefault('resume_skills_pending',set()).add(request_id)
                    state['thread']['skills_request_id']=request_id
                elif method=='thread/start':
                    if state['lease'].get('expected_resume_thread_id') is not None:state['protocol_valid']=False
                    if not state['initialized'] or state['thread'].get('id') is not None:
                        state['protocol_valid']=False
                    if row.get('params',{}).get('cwd')!=state['lease'].get('expected_cwd'):
                        state['protocol_valid']=False
                    state['thread']['start_request_id']=request_id
            elif event=='rpc_response' and method=='thread/resume':
                thread=row.get('thread',{});expected=state['lease'].get('expected_resume_thread_id')
                if (row.get('ok') is not True or row.get('direction')!='server' or row.get('request_direction')!='client'
                    or state['thread'].get('resume_request_id')!=request_id or state['thread'].get('resume_response')
                    or not self._thread_id(expected) or thread.get('id')!=expected or thread.get('cwd')!=state['lease'].get('expected_cwd')):
                    state['protocol_valid']=False
                else:state['thread'].update(id=expected,resume_response=True)
            elif event=='rpc_response' and method=='skills/list' and state['thread'].get('resume_response'):
                pending=state.get('resume_skills_pending',set())
                if request_id in pending:
                    pending.remove(request_id)
                    if row.get('ok') is True and row.get('direction')=='server' and row.get('request_direction')=='client':
                        state['thread']['resume_barrier']=True
                    else:state['protocol_valid']=False
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
            and (state['thread'].get('id')==state['thread'].get('started_id') or state['thread'].get('resume_barrier') is True)
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
    def __init__(self,manifest: dict,manifest_sha256: str,*,auth_wire_gate_factory=None):
        import auth_isolation
        if auth_wire_gate_factory is not None and not callable(auth_wire_gate_factory):
            raise ValueError('auth wire gate factory must be callable')
        # Trial auth filtering is enabled only by the trusted service entrypoint
        # passing a process-local factory.  The wire and manifest cannot toggle it.
        self._auth_wire_gate_factory=auth_wire_gate_factory
        self.auth=auth_isolation
        if manifest.get('version')!=1: raise ValueError('unsupported activation manifest')
        self.native_tui_policy=native_tui_policy(manifest.get('native_tui_policy'))
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
        self._publish_service_start()
        self.sink=MetadataSink(lease_for_connection=self._protocol_lease,publish_ready=self._publish_ready)
        self.backend_records=[]
        self._admissions={}
        self._helper_admissions={}
        self._wire_rejections=[]
        self._owner_leases={}
        helper_pins=manifest.get('owner_helper',{})
        self._helper_pins=helper_pins if isinstance(helper_pins,dict) else {}
        if self._helper_pins:
            required_helper_pins=('executable','executable_sha256','source_path','source_sha256')
            if any(not isinstance(self._helper_pins.get(key),str) or not self._helper_pins[key] for key in required_helper_pins):
                raise ValueError('incomplete owner-helper source policy')
            source_path=Path(self._helper_pins['source_path'])
            _canonical_path(source_path)
            if self._helper_pins['source_sha256']!=_sha(source_path):
                raise ValueError('owner-helper source changed')
        self._profile_locks={name:asyncio.Lock() for name in self.contexts}
        self._closed=False
        self._close_task=None
        self.proxy=ProxyServer(self.public_socket,None,self.sink,authorize_peer=self._admit,
            backend_factory=self._backend,stop_on_frontend_eof=True,wait_for_frontend_data=True,callback_timeout=LOCAL_SECONDS,
            wire_gate_factory=self._wire_gate_factory)

    def _publish_service_start(self):
        birth, executable = _process_metadata(os.getpid())
        if not birth or not executable:
            raise ValueError('service identity incomplete')
        executable_path = Path(executable).resolve()
        identity = {'pid': os.getpid(), 'uid': os.getuid(), 'birth': birth,
                    'executable': executable, 'executable_sha256': _sha(executable_path)}
        self._service_identity = identity
        _publish_receipt(self.receipt_dir/'service-start.json', {
            'version': 1, 'activation_id': self.activation_id,
            'manifest_sha256': self.manifest_sha256, 'service_identity': identity,
            'started_raw_ns': time.clock_gettime_ns(time.CLOCK_MONOTONIC_RAW)},
            expected_parent=self._directory_pins[self.receipt_dir])

    def _protocol_lease(self,event):
        for record in self.backend_records:
            if (record.get('state')=='connected' and record.get('pid')==event.backend_peer.pid
                and record.get('birth')==event.backend_peer.birth
                and record['frontend']==_peer(event.frontend_peer)):
                context=self.contexts[record['profile_id']]
                return {'lease_id':record['lease_id'],'profile_id':record['profile_id'],
                    'expected_home':str(context.spec.codex_home),'expected_cwd':str(context.spec.workspace),
                    **({'expected_resume_thread_id':record['expected_resume_thread_id']} if record.get('expected_resume_thread_id') else {})}
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
            owner_lease=OwnerLease(
                profile_id=record['profile_id'],owner_context_sha256=owner_context_sha256,
                lease_id=record['lease_id'],owner_connection_id=record['owner_connection_id'],
                owner_epoch=record['owner_epoch'],owner_thread_id=thread_id,
                private_socket=record['private_socket'],backend_pid=record['pid'],
                backend_birth=record['birth'],helper_identity=None)
            row['owner_lease']={
                'profile_id':record['profile_id'],'owner_context_sha256':owner_context_sha256,
                'lease_id':record['lease_id'],'owner_connection_id':record['owner_connection_id'],
                'owner_epoch':record['owner_epoch'],'owner_thread_id':thread_id,
                'private_socket':record['private_socket'],'backend_pid':record['pid'],
                'backend_birth':record['birth'],'backend_executable_sha256':_sha(self.executable),
                'private_socket_identity':list(record.get('private_socket_identity', ())),
                'service_identity':dict(self._service_identity)}
        _publish_receipt(self.receipt_dir/f"ready-{protocol['lease_id']}.json", row,
            expected_parent=self._directory_pins[self.receipt_dir])
        if record is not None and 'owner_lease' in row:
            self._owner_leases[record['lease_id']]=owner_lease

    @classmethod
    def from_manifest(cls,path: Path,*,auth_wire_gate_factory=None):
        manifest,digest=_private_json(Path(path))
        return cls(manifest,digest,auth_wire_gate_factory=auth_wire_gate_factory)

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
        def rejected(value):
            # Gate callbacks contain only fixed codes and numeric frame metadata;
            # rejected bytes never enter MetadataSink or this receipt.
            if len(self._wire_rejections)<64:
                self._wire_rejections.append(dict(value,frontend_pid=frontend_peer.pid,
                    connection_id=connection_id,epoch=epoch))
        auth_gates=None
        auth_factory=getattr(self,'_auth_wire_gate_factory',None)
        if auth_factory is not None:
            auth_gates=auth_factory(frontend_peer,backend_peer,connection_id,epoch,rejected)
            if (not isinstance(auth_gates,tuple) or len(auth_gates)!=2
                    or not all(callable(gate) for gate in auth_gates)):
                raise ValueError('auth wire gate factory returned invalid gates')
        admission=self._helper_admissions.get((frontend_peer.pid,frontend_peer.birth,frontend_peer.executable))
        if admission is None:
            return auth_gates
        grant,_=admission
        pending=set()
        helper_gates=(NativeWebSocketGate(grant['owner_thread_id'],server=False,pending_ids=pending,on_reject=rejected),
            NativeWebSocketGate(grant['owner_thread_id'],server=True,pending_ids=pending,on_reject=rejected))
        if auth_gates is None:return helper_gates
        return tuple(_chain_wire_gates(auth_gate,helper_gate)
            for auth_gate,helper_gate in zip(auth_gates,helper_gates))

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

    def _admission_diagnostic(self,peer,reason,*,accepted=False,exc=None):
        # Literal call-site reason codes only; never exception text, paths,
        # RPC bodies or arbitrary grant values. Bounded per service instance.
        rows=getattr(self,'_admission_diagnostics',None)
        if rows is None: rows=self._admission_diagnostics=[]
        if len(rows)<64:
            rows.append({'frontend_pid':peer.pid,'reason':reason,'accepted':accepted,
                'failure_type':type(exc).__name__ if exc is not None else None})
        return accepted

    def _helper_admission(self, peer, grant, grant_sha):
        required={'version','role','profile_id','owner_context_sha256','lease_id','owner_connection_id',
            'owner_epoch','owner_thread_id','private_socket','helper_pid','helper_uid','helper_birth',
            'helper_executable','helper_executable_sha256','helper_source_sha256'}
        if set(grant)!=required or type(grant.get('version')) is not int or grant.get('version')!=1 or grant.get('role')!='owner-helper': return self._admission_diagnostic(peer,'helper_shape')
        if grant.get('helper_pid')!=peer.pid or grant.get('helper_uid')!=peer.uid or grant.get('helper_birth')!=peer.birth: return self._admission_diagnostic(peer,'helper_process')
        if grant.get('helper_executable')!=peer.executable or not peer.complete: return self._admission_diagnostic(peer,'helper_executable')
        pins=self._helper_pins
        if not all(isinstance(pins.get(key),str) and pins.get(key) for key in ('executable','executable_sha256','source_path','source_sha256')): return self._admission_diagnostic(peer,'helper_policy_shape')
        if grant['helper_executable']!=pins['executable'] or grant['helper_source_sha256']!=pins['source_sha256']: return self._admission_diagnostic(peer,'helper_policy_pin')
        try:
            source_path=Path(pins['source_path'])
            _canonical_path(source_path)
            if _sha(source_path)!=pins['source_sha256']: return self._admission_diagnostic(peer,'helper_source_hash')
            if grant['helper_executable_sha256']!=pins['executable_sha256'] or _sha(Path(peer.executable))!=pins['executable_sha256']: return self._admission_diagnostic(peer,'helper_executable_hash')
            current_birth,current_executable=_process_metadata(peer.pid)
            if current_birth!=peer.birth or current_executable!=peer.executable: return self._admission_diagnostic(peer,'process_changed')
        except (OSError,ValueError) as exc: return self._admission_diagnostic(peer,'helper_pin_check',exc=exc)
        record=self._record_for_lease(grant['lease_id']); lease=self._owner_leases.get(grant['lease_id'])
        if record is None or lease is None or record.get('state')!='connected': return self._admission_diagnostic(peer,'helper_owner_inactive')
        context=self.contexts.get(grant['profile_id'])
        if context is None or record.get('profile_id')!=grant['profile_id']: return self._admission_diagnostic(peer,'helper_profile')
        if grant['owner_context_sha256']!=self._owner_context_sha(context): return self._admission_diagnostic(peer,'helper_context')
        if grant['private_socket']!=record.get('private_socket') or grant['private_socket']!=lease.private_socket: return self._admission_diagnostic(peer,'helper_socket_path')
        if grant['owner_connection_id']!=record.get('owner_connection_id') or grant['owner_epoch']!=record.get('owner_epoch') or grant['owner_thread_id']!=record.get('owner_thread_id'): return self._admission_diagnostic(peer,'helper_owner_binding')
        try:
            info=os.lstat(grant['private_socket']); frozen=record.get('private_socket_identity')
            if frozen is None or (info.st_dev,info.st_ino,info.st_uid,info.st_mode)!=tuple(frozen): return self._admission_diagnostic(peer,'helper_socket_identity')
            identity=HelperIdentity(pid=peer.pid,uid=peer.uid,birth=peer.birth,executable=peer.executable,
                executable_sha256=grant['helper_executable_sha256'],source_sha256=grant['helper_source_sha256'])
            lease.validate_grant(OwnerHelperGrant(**grant),identity)
        except (OSError,ValueError,KeyError,TypeError) as exc: return self._admission_diagnostic(peer,'helper_lease_check',exc=exc)
        key=(peer.pid,peer.birth,peer.executable); previous=self._helper_admissions.get(key)
        if previous is not None and previous[1]!=grant_sha: return self._admission_diagnostic(peer,'helper_grant_changed')
        self._helper_admissions[key]=(dict(grant),grant_sha)
        return self._admission_diagnostic(peer,'helper_admitted',accepted=True)

    def _admit(self,peer):
        if not peer.complete or peer.uid!=os.getuid(): return self._admission_diagnostic(peer,'peer_incomplete_or_uid')
        check_stage='roots'
        try:
            self._check_roots()
            check_stage='process'
            current_birth,current_executable=_process_metadata(peer.pid)
            if current_birth!=peer.birth or current_executable!=peer.executable: return self._admission_diagnostic(peer,'process_changed')
            check_stage='grant_read'
            grant,digest=_private_json(self.grants_dir/f'{peer.pid}.json')
            if grant.get('role')=='owner-helper':
                check_stage='helper_admission'
                return self._helper_admission(peer,grant,digest)
            check_stage='owner_grant_check'
            profile=grant.get('profile_id')
            allowed=(grant.get('version')==1 and grant.get('pid')==peer.pid and grant.get('uid')==peer.uid
                and grant.get('birth')==peer.birth and grant.get('expected_executable')==peer.executable
                and grant.get('executable_sha256')==_sha(Path(peer.executable)) and profile in self.contexts)
            if not allowed: return self._admission_diagnostic(peer,'owner_grant')
            key=(peer.pid,peer.birth,peer.executable)
            previous=self._admissions.setdefault(key,(profile,digest))
            return previous==(profile,digest)
        except (OSError,ValueError,KeyError,TypeError) as exc:
            return self._admission_diagnostic(peer,check_stage,exc=exc)

    async def _output_digest(self,stream):
        digest=hashlib.sha256(); size=0
        while chunk:=await stream.read(65536): size+=len(chunk); digest.update(chunk)
        return {'bytes':size,'sha256':digest.hexdigest()}

    def _resume_owner_proof(self,proof,resume_id,profile,context):
        if not isinstance(proof,dict) or set(proof)!={'activation_id','lease_id','ready_sha256','backend_sha256'}:
            raise ValueError('resume owner proof missing')
        activation=proof['activation_id'];lease=proof['lease_id']
        if not isinstance(activation,str) or re.fullmatch('[0-9a-f]{32}',activation) is None or not isinstance(lease,str) or re.fullmatch('[0-9a-f]{12}',lease) is None:
            raise ValueError('resume proof identity invalid')
        parent=self.state_dir/activation;_private_directory(parent)
        ready,ready_sha=_private_json(parent/f'ready-{lease}.json')
        final,final_sha=_private_json(parent/f'backend-{lease}.json')
        if ready_sha!=proof['ready_sha256'] or final_sha!=proof['backend_sha256']:
            raise ValueError('resume proof changed')
        binding=ready.get('owner_lease',{})
        if (ready.get('activation_id')!=activation or ready.get('manifest_sha256')!=self.manifest_sha256
            or binding.get('lease_id')!=lease or binding.get('owner_thread_id')!=resume_id or binding.get('profile_id')!=profile
            or binding.get('owner_context_sha256')!=self._owner_context_sha(context)
            or any(final.get(k)!=binding.get(k) for k in ('lease_id','profile_id','owner_thread_id','owner_context_sha256','owner_connection_id','owner_epoch','private_socket'))
            or final.get('pid')!=binding.get('backend_pid') or final.get('birth')!=binding.get('backend_birth')
            or final.get('frontend')!=ready.get('frontend') or final.get('state')!='closed' or not final.get('process_stopped') or not final.get('socket_removed')
            or final.get('cleanup_failure') or final.get('guardian',{}).get('child_reaped') is not True):
            raise ValueError('resume proof owner/cleanup mismatch')
        if (any(row.get('state')!='closed' for row in final.get('helpers',[]))
            or not _original_process_gone(final['pid'],final['birth'],final['executable'])
            or not _original_process_gone(final['frontend']['pid'],final['frontend']['birth'],final['frontend']['executable'])
            or os.path.lexists(final['private_socket'])):raise ValueError('resume owner resources remain')
        service=binding.get('service_identity',{})
        if activation!=self.activation_id and not _original_process_gone(service['pid'],service['birth'],service['executable']):raise ValueError('prior service remains')

    @asynccontextmanager
    async def _backend(self,frontend):
        key=(frontend.pid,frontend.birth,frontend.executable)
        if key in self._helper_admissions:
            grant,grant_sha=self._helper_admissions[key]
            profile=grant['profile_id']; context=self.contexts[profile]
            try:self._check_runtime_policy()
            except (OSError,ValueError,KeyError,TypeError) as exc:
                self._admission_diagnostic(frontend,'helper_runtime_policy',exc=exc)
                raise
            if not self._admit(frontend) or self._helper_admissions[key][1]!=grant_sha:
                raise ValueError('helper grant changed before connection')
            async with self._leased_helper(frontend,grant,context,grant_sha) as lease:
                yield lease
            return
        # Admission is reusable for a no-RPC startup probe, but cannot be remapped.
        profile,grant_sha=self._admissions[key]
        context=self.contexts[profile]
        grant,current_sha=_private_json(self.grants_dir/f'{frontend.pid}.json')
        if current_sha!=grant_sha:raise ValueError('owner grant changed before launch')
        resume_id=grant.get('expected_resume_thread_id')
        if resume_id is not None:
            if self.native_tui_policy is None or not MetadataSink._thread_id(resume_id):raise ValueError('resume policy absent')
            self._resume_owner_proof(grant.get('resume_owner_proof'),resume_id,profile,context)
        start=time.monotonic(); deadline=start+LOCAL_SECONDS
        lock=self._profile_locks[profile]
        await asyncio.wait_for(lock.acquire(),max(0,deadline-time.monotonic()))
        try:
            self._check_runtime_policy()
            if not self._admit(frontend) or self._admissions[key]!=(profile,grant_sha):
                raise ValueError('frontend grant changed before backend start')
            async with self._leased_backend(frontend,profile,grant_sha,context,start,deadline,resume_id) as lease:
                yield lease
        finally:
            lock.release()

    @asynccontextmanager
    async def _leased_helper(self,frontend,grant,context,grant_sha):
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
            helper=await lease.open_helper_transport(OwnerHelperGrant(**grant),identity)
            helper_record['state']='connected'
            backend_socket=helper.writer.get_extra_info('socket')
            backend_peer=peer_identity(backend_socket)
            if not backend_peer.complete or not _same_process(backend_peer,record['pid'],record['birth'],self.executable):
                raise ValueError('helper backend peer identity mismatch')
            info=os.lstat(grant['private_socket'])
            frozen=record.get('private_socket_identity')
            if frozen is None or (info.st_dev,info.st_ino,info.st_uid,info.st_mode)!=tuple(frozen):
                raise ValueError('helper backend socket identity changed')
            _publish_receipt(self.receipt_dir/f"helper-ready-{grant['lease_id']}-{frontend.pid}.json", {
                'version':1,'activation_id':self.activation_id,'manifest_sha256':self.manifest_sha256,
                'grant_sha256':grant_sha,
                'service_identity':dict(self._service_identity),
                'owner_lease':{
                    'profile_id':record['profile_id'],'owner_context_sha256':grant['owner_context_sha256'],
                    'lease_id':grant['lease_id'],'owner_connection_id':grant['owner_connection_id'],
                    'owner_epoch':grant['owner_epoch'],'owner_thread_id':grant['owner_thread_id'],
                    'private_socket':grant['private_socket'],'backend_pid':record['pid'],
                    'backend_birth':record['birth'],'backend_executable_sha256':_sha(self.executable),
                    'private_socket_identity':list(frozen)},
                'frontend_peer':_peer(frontend),'backend_peer':_peer(backend_peer)},
                expected_parent=self._directory_pins[self.receipt_dir])
            yield BackendConnection(helper.reader,helper.writer,
                lambda peer:_same_process(peer,record['pid'],record['birth'],self.executable))
        finally:
            if helper is not None:
                await helper.close()
            helper_record['state']='closed'

    @asynccontextmanager
    async def _leased_backend(self,frontend,profile,grant_sha,context,start,deadline,resume_id=None):
        lease_id=uuid.uuid4().hex[:12]
        lease_dir=self.receipt_dir/lease_id
        lease_dir.mkdir(mode=0o700)
        private_socket=context.spec.backend_socket.parent/f'b-{lease_id}.sock'
        _private_directory(private_socket.parent)
        if len(os.fsencode(private_socket))>=104: raise ValueError('private Unix socket path is too long')
        if os.path.lexists(private_socket): raise FileExistsError('private backend path already exists')
        record={'lease_id':lease_id,'profile_id':profile,'grant_sha256':grant_sha,'frontend':_peer(frontend),
            'private_socket':str(private_socket),'state':'starting','process_stopped':False,'socket_removed':False}
        if resume_id is not None:record['expected_resume_thread_id']=resume_id
        self.backend_records.append(record)
        process=None; birth=None; creation_birth=None; identity=None; outputs=[]; writer=None; launch_attempted=False
        owner_connected_path=self.receipt_dir/f'owner-connected-{lease_id}-{frontend.pid}.json'
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
            socket_info = private_socket.lstat()
            if (socket_info.st_dev, socket_info.st_ino, socket_info.st_uid, socket_info.st_mode) != identity:
                raise ValueError('backend socket identity changed before owner receipt')
            backend_peer_object = peer_identity(writer.get_extra_info('socket'))
            if not backend_peer_object.complete or not _same_process(backend_peer_object, process.pid, birth, self.executable):
                raise ValueError('backend peer identity mismatch before owner receipt')
            backend_peer = _peer(backend_peer_object)
            record.update(state='connected',birth=birth,executable=str(self.executable),
                private_socket_identity=identity,backend_peer=backend_peer)
            _publish_receipt(owner_connected_path, {
                'version':1,'activation_id':self.activation_id,'manifest_sha256':self.manifest_sha256,
                'service_identity':dict(self._service_identity),
                'state':'connected',
                'profile_id':profile,'owner_context_sha256':self._owner_context_sha(context),
                'lease_id':lease_id,'frontend_peer':_peer(frontend),'grant_sha256':grant_sha,
                'private_socket':str(private_socket),'private_socket_identity':list(identity),
                'backend_pid':process.pid,'backend_birth':birth,
                'backend_executable_sha256':_sha(self.executable),'backend_peer':backend_peer},
                expected_parent=self._directory_pins[self.receipt_dir])
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
            _publish_receipt(self.receipt_dir/f'backend-{lease_id}.json',record,
                expected_parent=self._directory_pins[self.receipt_dir])

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
            'admission_diagnostics':list(getattr(self,'_admission_diagnostics',[])),
            'wire_rejections':list(getattr(self,'_wire_rejections',[])),
            'empty_probe_eofs':self.proxy.empty_probe_eofs,'public_socket_owned':False,'native_registration_verified':False,'stop_signals':getattr(self,'stop_signals',[])}

    async def close(self):
        if self._close_task is None:
            self._closed=True
            self._cleanup_deadline=time.monotonic()+LOCAL_SECONDS
            self._close_task=asyncio.create_task(self._finish_close())
        await asyncio.shield(self._close_task)

    async def _finish_close(self):
        await self.proxy.close()
        self._check_roots()
        _publish_receipt(self.receipt_dir/'activation.json',self.snapshot(),
            expected_parent=self._directory_pins[self.receipt_dir])
