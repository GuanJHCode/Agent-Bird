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
for _directory in (_REPO/'tasks/g0-tui-proxy/scripts', _REPO/'tasks/g0-auth-preserving-activation/scripts'):
    if str(_directory) not in sys.path:
        sys.path.insert(0,str(_directory))
from proxy_transport import BackendConnection, CaptureSink, ProxyServer, _process_metadata

LOCAL_SECONDS = 10.0


def _sha(path: Path) -> str:
    digest=hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda:stream.read(1024*1024),b''): digest.update(chunk)
    return digest.hexdigest()


def _private_directory(path: Path) -> None:
    info=path.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid!=os.getuid() or info.st_mode & 0o077:
        raise ValueError('expected an existing owner-only real directory')


def _private_json(path: Path) -> tuple[dict,str]:
    descriptor=os.open(path,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
    with os.fdopen(descriptor,'rb') as stream:
        info=os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_uid!=os.getuid() or info.st_mode & 0o077 or info.st_size>1024*1024:
            raise ValueError('expected a bounded owner-only policy file')
        data=stream.read(1024*1024+1)
        after=os.fstat(stream.fileno())
        fields=('st_dev','st_ino','st_uid','st_mode','st_size','st_mtime_ns','st_ctime_ns')
        if any(getattr(info,key)!=getattr(after,key) for key in fields) or len(data)!=info.st_size:
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


class MetadataSink(CaptureSink):
    def __init__(self):
        self.events=[]
        self.streams={}

    def on_connect(self,event):
        self.events.append({'kind':'connect','connection_id':event.connection_id,'epoch':event.epoch,
            'frontend':_peer(event.frontend_peer),'backend':_peer(event.backend_peer)})

    def on_data(self,event):
        key=(event.connection_id,event.direction)
        count,digest=self.streams.setdefault(key,[0,hashlib.sha256()])
        digest.update(event.data)
        self.streams[key][0]=count+len(event.data)

    def on_lifecycle(self,event):
        self.events.append({'kind':event.kind,'connection_id':event.connection_id,'epoch':event.epoch,'direction':event.direction})

    def on_gap(self,event):
        self.events.append({'kind':'gap','connection_id':event.connection_id,'epoch':event.epoch,'reason':event.reason,'fatal':event.fatal})

    def snapshot(self):
        return {'events':list(self.events),'streams':[{'connection_id':key[0],'direction':key[1],
            'bytes':value[0],'sha256':value[1].hexdigest()} for key,value in sorted(self.streams.items())]}


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
        self.activation_id=uuid.uuid4().hex
        self.receipt_dir=self.state_dir/self.activation_id
        self.receipt_dir.mkdir(mode=0o700)
        self.sink=MetadataSink()
        self.backend_records=[]
        self._admissions={}
        self._profile_locks={name:asyncio.Lock() for name in self.contexts}
        self._closed=False
        self.proxy=ProxyServer(self.public_socket,None,self.sink,authorize_peer=self._admit,
            backend_factory=self._backend,stop_on_frontend_eof=True,callback_timeout=LOCAL_SECONDS)

    @classmethod
    def from_manifest(cls,path: Path):
        manifest,digest=_private_json(Path(path))
        return cls(manifest,digest)

    def _check_pins(self):
        if _sha(self.executable)!=self.manifest['backend_executable_sha256']:
            raise ValueError('backend executable changed')
        if any(_sha(path)!=expected for path,expected in self.file_pins.items()):
            raise ValueError('backend implementation changed')

    def _admit(self,peer):
        if not peer.complete or peer.uid!=os.getuid(): return False
        try:
            grant,digest=_private_json(self.grants_dir/f'{peer.pid}.json')
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
        # Admission is reusable for a no-RPC startup probe, but cannot be remapped.
        key=(frontend.pid,frontend.birth,frontend.executable)
        profile,grant_sha=self._admissions[key]
        context=self.contexts[profile]
        start=time.monotonic(); deadline=start+LOCAL_SECONDS
        lock=self._profile_locks[profile]
        await asyncio.wait_for(lock.acquire(),max(0,deadline-time.monotonic()))
        try:
            if not self._admit(frontend) or self._admissions[key]!=(profile,grant_sha):
                raise ValueError('frontend grant changed before backend start')
            async with self._leased_backend(frontend,profile,grant_sha,context,start,deadline) as lease:
                yield lease
        finally:
            lock.release()

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
        process=None; birth=None; creation_birth=None; identity=None; outputs=[]; writer=None
        try:
            self._check_pins()
            argv=[argument.replace('{socket_path}',str(private_socket)) for argument in self.argv]
            plan=self.auth.build_backend_launch(context,argv,private_socket)
            sandbox_path=lease_dir/'backend.sbpl'
            fd=os.open(sandbox_path,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600)
            with os.fdopen(fd,'w') as stream: stream.write(plan['sandbox_profile'])
            if time.monotonic()>=deadline: raise TimeoutError('backend start deadline elapsed before spawn')
            launch=['/usr/bin/sandbox-exec','-f',str(sandbox_path),*plan['argv']]
            spawn=asyncio.create_task(asyncio.create_subprocess_exec(*launch,cwd=plan['cwd'],env=plan['env'],
                stdin=asyncio.subprocess.DEVNULL,stdout=asyncio.subprocess.PIPE,stderr=asyncio.subprocess.PIPE,
                start_new_session=True,umask=0o077))
            try:
                process=await asyncio.shield(spawn)
            except asyncio.CancelledError:
                process=await spawn
                raise
            finally:
                if process is not None:
                    creation_birth,creation_executable=_process_metadata(process.pid)
                    record.update(pid=process.pid,creation_birth=creation_birth,creation_executable=creation_executable)
                    outputs=[asyncio.create_task(self._output_digest(stream)) for stream in (process.stdout,process.stderr)]
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
            record.update(state='connected',birth=birth,executable=str(self.executable))
            yield BackendConnection(reader,writer,lambda peer:_same_process(peer,process.pid,birth,self.executable))
        except BaseException as exc:
            record.update(state='failed',failure_type=type(exc).__name__)
            raise
        finally:
            cleanup_deadline=time.monotonic()+LOCAL_SECONDS
            if writer is not None: writer.close()
            if process is not None:
                if process.returncode is None:
                    current_birth,_=_process_metadata(process.pid)
                    # sandbox-exec may still be transitioning to the expected image.
                    # The creation PID/birth remains the owned identity across exec.
                    if creation_birth and current_birth==creation_birth:
                        process.terminate()
                        try: await asyncio.wait_for(process.wait(),1.0)
                        except TimeoutError:
                            check_birth,_=_process_metadata(process.pid)
                            if check_birth==creation_birth:
                                process.kill()
                                try: await asyncio.wait_for(process.wait(),max(0,cleanup_deadline-time.monotonic()))
                                except TimeoutError: record['cleanup_failure']='owned_backend_stop_timeout'
                    else: record['cleanup_failure']='backend_process_identity_unknown'
                record['returncode']=process.returncode
                record['process_stopped']=process.returncode is not None
                try:
                    digests=await asyncio.wait_for(asyncio.gather(*outputs),min(1.0,max(0,cleanup_deadline-time.monotonic())))
                    record['stdout'],record['stderr']=digests
                except TimeoutError:
                    record['output_digest_complete']=False
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
            record['elapsed_seconds']=time.monotonic()-start
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
            'public_socket_owned':False,'native_registration_verified':False}

    async def close(self):
        if self._closed: return
        self._closed=True
        await self.proxy.close()
        _write_json(self.receipt_dir/'activation.json',self.snapshot())
