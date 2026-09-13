"""Executable cold-start chain using isolated Python fixture processes and real UDS."""
import asyncio
from contextlib import ExitStack
import base64
import hashlib
import json
import os
from pathlib import Path
import socket
import signal
import sys
import tempfile

import pytest

ROOT=Path(__file__).resolve().parents[4]
sys.path.insert(0,str(ROOT/'tasks/g0-tui-proxy/scripts'))
sys.path.insert(0,str(ROOT/'tasks/g0-proxy-continuation/cold-start/scripts'))
sys.path.insert(0,os.environ.get('AUTH_ISOLATION_SCRIPTS',str(ROOT/'tasks/g0-auth-preserving-activation/scripts')))
import proxy_transport as transport
try:
    import activation_service as service_module
except ModuleNotFoundError:
    service_module=None

FIXTURES=Path(__file__).parent/'fixtures'
CANARY=b'opaque synthetic stream must never be stored as raw body\x00\xff'


def module():
    assert service_module is not None, 'executable per-frontend activation service is missing'
    return service_module


def digest(path): return hashlib.sha256(Path(path).read_bytes()).hexdigest()

def private_json(path,data):
    path.write_text(json.dumps(data)); path.chmod(0o600)


async def eventually(predicate):
    async with asyncio.timeout(3):
        while not predicate(): await asyncio.sleep(.01)


async def client(binary,public,home):
    home.mkdir(mode=0o700)
    process=await asyncio.create_subprocess_exec(binary,'-B',str(FIXTURES/'fake_frontend.py'),str(public),
        stdin=asyncio.subprocess.PIPE,stdout=asyncio.subprocess.PIPE,stderr=asyncio.subprocess.DEVNULL,
        env={'PATH':'/usr/bin:/bin','HOME':str(home),'CODEX_HOME':str(home/'c'),'PYTHONDONTWRITEBYTECODE':'1'})
    assert await process.stdout.readline()==b'ready\n'
    return process


async def ask(process,action,**values):
    process.stdin.write(json.dumps({'action':action,**values}).encode()+b'\n'); await process.stdin.drain()
    return (await process.stdout.readline()).decode().strip()


def policy(root,public,binary,roots,*,fail=False):
    import auth_isolation as auth
    grants=root/'grants'; grants.mkdir(mode=0o700)
    state=root/'state'; state.mkdir(mode=0o700)
    protected=root/'protected'; protected.mkdir(mode=0o700)
    profiles={}
    for name in ('a','b'):
        profile_root=Path(roots.enter_context(tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp')))
        spec=auth.IsolationSpec(task_root=profile_root,home=profile_root/'h',codex_home=profile_root/'c',workspace=profile_root/'w',
            profile_id=name,public_socket=public,backend_socket=profile_root/'backend/b.sock',protected_paths=(protected,),
            protected_read_paths=(protected,),allowed_executables=(Path(binary),))
        auth.prepare_isolated_home(spec)
        profiles[name]={'task_root':str(profile_root),'home':str(spec.home),'codex_home':str(spec.codex_home),
            'workspace':str(spec.workspace),'public_socket':str(public),'backend_socket':str(spec.backend_socket),
            'expected_executable':binary,'expected_executable_sha256':digest(binary),'protected_paths':[str(protected)],'protected_read_paths':[str(protected)]}
    isolation=root/'isolation.json'; private_json(isolation,{'version':1,'profiles':profiles})
    argv=[binary,'-B',str(FIXTURES/'fake_backend.py'),'{socket_path}']+(['--fail'] if fail else [])
    manifest=root/'service.json'; private_json(manifest,{'version':1,'public_socket':str(public),
        'state_dir':str(state),'grants_dir':str(grants),'isolation_manifest':str(isolation),
        'isolation_manifest_sha256':digest(isolation),'backend_argv':argv,'backend_executable_sha256':digest(binary),
        'file_pins':{str(FIXTURES/'fake_backend.py'):digest(FIXTURES/'fake_backend.py')},'idle_seconds':.2})
    return manifest,profiles,grants


def register(grants,process,profile_id,binary):
    birth,executable=transport._process_metadata(process.pid)
    assert birth and executable==binary
    private_json(grants/f'{process.pid}.json',{'version':1,'pid':process.pid,'uid':os.getuid(),'birth':birth,
        'expected_executable':binary,'executable_sha256':digest(binary),'profile_id':profile_id})


def run(coro): return asyncio.run(asyncio.wait_for(coro,10))


def test_two_registered_frontends_get_distinct_processes_and_auth_homes():
    p=module()
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw, ExitStack() as roots:
            root=Path(raw); public=root/'p.sock'; binary=transport._process_metadata(os.getpid())[1]
            manifest,profiles,grants=policy(root,public,binary,roots)
            listener=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM); listener.bind(str(public)); listener.listen(8)
            before=public.stat(); clients=[]; proxy=p.ActivationService.from_manifest(manifest)
            try:
                for name in ('a','b'):
                    child=await client(binary,public,root/f'frontend-{name}'); clients.append(child); register(grants,child,name,binary)
                await proxy.start(socket.socket(fileno=os.dup(listener.fileno())))
                headers=[json.loads(await ask(child,'connect')) for child in clients]
                assert len({row['pid'] for row in headers})==2
                for name,row in zip(('a','b'),headers):
                    assert row['home']==profiles[name]['home'] and row['codex_home']==profiles[name]['codex_home']
                    assert row['auth_env_present'] is False
                assert base64.b64decode(await ask(clients[0],'send',data=base64.b64encode(CANARY).decode()))==CANARY
                clients[0].stdin.write(b'{"action":"close"}\n'); await clients[0].stdin.drain(); await clients[0].wait()
                await eventually(lambda: len([r for r in proxy.backend_records if r.get('process_stopped')])==1)
                assert base64.b64decode(await ask(clients[1],'send',data=base64.b64encode(b'still-owned-b').decode()))==b'still-owned-b'
                clients[1].stdin.write(b'{"action":"close"}\n'); await clients[1].stdin.drain(); await clients[1].wait()
                await eventually(lambda: len([r for r in proxy.backend_records if r.get('process_stopped')])==2)
            finally:
                for child in clients:
                    if child.returncode is None: child.terminate(); await child.wait()
                await proxy.close(); listener.close()
            assert public.exists() and (public.stat().st_dev,public.stat().st_ino)==(before.st_dev,before.st_ino)
            assert all(r['process_stopped'] and r['socket_removed'] for r in proxy.backend_records)
            serialized=json.dumps(proxy.snapshot())
            assert CANARY.decode('utf-8','replace') not in serialized
            assert len(proxy.backend_records)==2
    run(scenario())


def test_unregistered_process_cannot_spawn_a_backend():
    p=module()
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw, ExitStack() as roots:
            root=Path(raw); public=root/'p.sock'; binary=transport._process_metadata(os.getpid())[1]
            manifest,profiles,grants=policy(root,public,binary,roots)
            listener=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM); listener.bind(str(public)); listener.listen(4)
            proxy=p.ActivationService.from_manifest(manifest); child=await client(binary,public,root/'f')
            try:
                await proxy.start(socket.socket(fileno=os.dup(listener.fileno())))
                assert await ask(child,'connect')=='closed'
                assert proxy.backend_records==[]
            finally:
                child.terminate(); await child.wait(); await proxy.close(); listener.close()
    run(scenario())


def test_real_backend_start_failure_is_stopped_and_recorded_without_public_unlink():
    p=module()
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw, ExitStack() as roots:
            root=Path(raw); public=root/'p.sock'; binary=transport._process_metadata(os.getpid())[1]
            manifest,profiles,grants=policy(root,public,binary,roots,fail=True)
            listener=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM); listener.bind(str(public)); listener.listen(4)
            proxy=p.ActivationService.from_manifest(manifest); child=await client(binary,public,root/'f'); register(grants,child,'a',binary)
            try:
                await proxy.start(socket.socket(fileno=os.dup(listener.fileno())))
                assert await ask(child,'connect')=='closed'
                await eventually(lambda: bool(proxy.backend_records) and proxy.backend_records[0].get('process_stopped'))
            finally:
                child.terminate(); await child.wait(); await proxy.close(); listener.close()
            assert proxy.backend_records[0]['state']=='failed'
            assert proxy.backend_records[0]['returncode']==7
            assert public.exists()
    run(scenario())


@pytest.mark.parametrize('ending',['idle','SIGTERM','SIGINT'])
def test_entrypoint_runs_inherited_listener_through_real_backend_and_idle_exit(ending):
    p=module()
    entry=ROOT/'tasks/g0-proxy-continuation/cold-start/scripts/projectproxy_launchd_entrypoint.py'
    assert entry.exists(), 'a working offline entrypoint must connect activation FD to backend lifecycle'
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw, ExitStack() as roots:
            root=Path(raw); public=root/'p.sock'; binary=transport._process_metadata(os.getpid())[1]
            manifest,profiles,grants=policy(root,public,binary,roots)
            listener=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM); listener.bind(str(public)); listener.listen(4)
            child=await client(binary,public,root/'f'); register(grants,child,'a',binary)
            environment={'PATH':'/usr/bin:/bin','HOME':str(root),'PYTHONDONTWRITEBYTECODE':'1',
                'PYTHONPATH':str(root/'untrusted-python-modules'),'OPENAI_API_KEY':'synthetic-must-not-inherit'}
            launch=await asyncio.create_subprocess_exec(binary,'-I','-B',str(entry),'--manifest',str(manifest),
                '--listener-fd',str(listener.fileno()),pass_fds=(listener.fileno(),),env=environment,
                stdout=asyncio.subprocess.PIPE,stderr=asyncio.subprocess.PIPE)
            try:
                connection=asyncio.create_task(ask(child,'connect'))
                exit_wait=asyncio.create_task(launch.wait())
                done,_=await asyncio.wait((connection,exit_wait),return_when=asyncio.FIRST_COMPLETED)
                if exit_wait in done:
                    raise AssertionError('entrypoint exited before frontend admission: '+(await launch.stdout.read()).decode())
                exit_wait.cancel(); await asyncio.gather(exit_wait,return_exceptions=True)
                header=json.loads(await connection)
                header_birth,_=transport._process_metadata(header['pid'])
                assert header['codex_home']==profiles['a']['codex_home']
                assert base64.b64decode(await ask(child,'send',data=base64.b64encode(CANARY).decode()))==CANARY
                if ending=='idle':
                    child.stdin.write(b'{"action":"close"}\n'); await child.stdin.drain(); await child.wait()
                else:
                    launch.send_signal(getattr(signal,ending))
                stdout,stderr=await asyncio.wait_for(launch.communicate(),3)
                assert launch.returncode==0, stderr.decode()
                summary=json.loads(stdout)
                assert summary['backend_count']==1 and summary['all_backends_stopped'] is True
                artifacts=list((root/'state').glob('*/activation.json'))
                assert len(artifacts)==1
                receipt=json.loads(artifacts[0].read_text())
                assert all(r['process_stopped'] and r['socket_removed'] for r in receipt['backend_records'])
                for row in receipt['backend_records']:
                    assert transport._process_metadata(row['pid'])[0]!=row['creation_birth']
                assert CANARY.decode('utf-8','replace') not in artifacts[0].read_text()
            finally:
                if 'header' in locals():
                    owned_birth,_=transport._process_metadata(header['pid'])
                    if owned_birth and owned_birth==header_birth:
                        try: os.kill(header['pid'],signal.SIGTERM)
                        except ProcessLookupError: pass
                for process in (child,launch):
                    if process.returncode is None: process.terminate(); await process.wait()
                listener.close()
            assert public.exists()
    run(scenario())


def test_same_profile_serializes_and_probe_does_not_consume_grant():
    p=module()
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw, ExitStack() as roots:
            root=Path(raw); public=root/'p.sock'; binary=transport._process_metadata(os.getpid())[1]
            manifest,profiles,grants=policy(root,public,binary,roots)
            listener=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM); listener.bind(str(public)); listener.listen(8)
            proxy=p.ActivationService.from_manifest(manifest); clients=[]
            try:
                for index in range(2):
                    child=await client(binary,public,root/f'f{index}'); clients.append(child); register(grants,child,'a',binary)
                await proxy.start(socket.socket(fileno=os.dup(listener.fileno())))
                assert await ask(clients[0],'probe')=='probed'
                await eventually(lambda: proxy.proxy.active_frontends==1)
                assert proxy.backend_records==[]
                assert await ask(clients[0],'close_probe')=='probe-closed'
                await eventually(lambda: proxy.proxy.empty_probe_eofs==1)
                first=json.loads(await ask(clients[0],'connect'))
                second=asyncio.create_task(ask(clients[1],'connect'))
                await asyncio.sleep(.1)
                assert not second.done(), 'same profile spawned concurrent writers to one home'
                await ask(clients[0],'disconnect')
                next_header=json.loads(await second)
                assert next_header['pid']!=first['pid']
                await ask(clients[1],'disconnect')
                await eventually(lambda: all(r.get('process_stopped') for r in proxy.backend_records))
            finally:
                for child in clients:
                    if child.returncode is None: child.terminate(); await child.wait()
                await proxy.close(); listener.close()
            assert len(proxy.backend_records)==2
            expected_parent=Path(profiles['a']['backend_socket']).parent
            assert all(Path(r['private_socket']).parent==expected_parent for r in proxy.backend_records)
            assert all(r['process_stopped'] and r['socket_removed'] for r in proxy.backend_records)
    run(scenario())


def test_changed_grant_cannot_remap_an_admitted_process():
    p=module()
    with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw, ExitStack() as roots:
        root=Path(raw); public=root/'p.sock'; binary=transport._process_metadata(os.getpid())[1]
        manifest,profiles,grants=policy(root,public,binary,roots)
        proxy=p.ActivationService.from_manifest(manifest)
        birth,executable=transport._process_metadata(os.getpid())
        peer=transport.PeerIdentity(pid=os.getpid(),uid=os.getuid(),birth=birth,executable=executable,pid_available=True,uid_available=True,birth_available=True,executable_available=True,source='test-owned')
        class Current: pid=os.getpid()
        register(grants,Current(),'a',binary)
        assert proxy._admit(peer)
        register(grants,Current(),'b',binary)
        assert not proxy._admit(peer)


def test_policy_reader_does_not_block_on_fifo(tmp_path,monkeypatch):
    p=module(); path=tmp_path/'fifo'; os.mkfifo(path,0o600)
    original=p.os.open
    def checked_open(name,flags,*args):
        assert flags & os.O_NONBLOCK, 'policy file open can block before fstat rejects FIFO'
        return original(name,flags,*args)
    monkeypatch.setattr(p.os,'open',checked_open)
    with pytest.raises(ValueError): p._private_json(path)


@pytest.mark.parametrize('changed',['profile_home','backend_dir','state_root','grants_alias','manifest','config'])
def test_runtime_policy_or_directory_drift_refuses_backend(changed):
    p=module()
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw, ExitStack() as roots:
            root=Path(raw); public=root/'p.sock'; binary=transport._process_metadata(os.getpid())[1]
            manifest,profiles,grants=policy(root,public,binary,roots)
            listener=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM); listener.bind(str(public)); listener.listen(4)
            proxy=p.ActivationService.from_manifest(manifest); child=await client(binary,public,root/'f'); register(grants,child,'a',binary)
            moved=None; target=None
            try:
                if changed in ('profile_home','backend_dir','state_root','grants_alias'):
                    target={'profile_home':Path(profiles['a']['home']),'backend_dir':Path(profiles['a']['backend_socket']).parent,
                            'state_root':root/'state','grants_alias':grants}[changed]
                    moved=target.with_name(target.name+'-original'); target.rename(moved)
                    if changed=='grants_alias': target.symlink_to(moved,target_is_directory=True)
                    else: target.mkdir(mode=0o700)
                elif changed=='manifest':
                    (root/'isolation.json').write_text((root/'isolation.json').read_text()+' ')
                else:
                    with (Path(profiles['a']['codex_home'])/'config.toml').open('a') as stream: stream.write('\n# drift\n')
                await proxy.start(socket.socket(fileno=os.dup(listener.fileno())))
                assert await ask(child,'connect')=='closed'
                assert not any(row.get('pid') for row in proxy.backend_records)
            finally:
                child.terminate(); await child.wait()
                if target is not None:
                    if target.is_symlink(): target.unlink()
                    else: target.rmdir()
                    moved.rename(target)
                await proxy.close(); listener.close()
    run(scenario())


def test_shutdown_during_pre_ready_child_exec_transition_keeps_creation_ownership(monkeypatch):
    p=module()
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw, ExitStack() as roots:
            root=Path(raw); public=root/'p.sock'; binary=transport._process_metadata(os.getpid())[1]
            manifest,profiles,grants=policy(root,public,binary,roots)
            value=json.loads(manifest.read_text()); value['backend_argv'].append('--wait-before-listen'); private_json(manifest,value)
            listener=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM); listener.bind(str(public)); listener.listen(4)
            proxy=p.ActivationService.from_manifest(manifest); child=await client(binary,public,root/'f'); register(grants,child,'a',binary)
            original=p._process_metadata
            def before_exec(pid):
                birth,executable=original(pid)
                return birth,executable if pid==child.pid else '/synthetic-pre-exec-image'
            monkeypatch.setattr(p,'_process_metadata',before_exec)
            try:
                await proxy.start(socket.socket(fileno=os.dup(listener.fileno())))
                connection=asyncio.create_task(ask(child,'connect'))
                await eventually(lambda: bool(proxy.backend_records) and proxy.backend_records[0].get('creation_birth'))
                await asyncio.wait_for(proxy.close(),3)
                assert await connection=='closed'
                record=proxy.backend_records[0]
                assert record['creation_executable']=='/synthetic-pre-exec-image'
                assert record['process_stopped'] and record['socket_removed']
                assert original(record['pid'])[0]!=record['creation_birth']
                assert public.exists()
            finally:
                if child.returncode is None: child.terminate(); await child.wait()
                await proxy.close(); listener.close()
    run(scenario())


def test_invalid_inherited_fd_records_safe_startup_errno_without_raw_exception():
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw, ExitStack() as roots:
            root=Path(raw); public=root/'p.sock'; binary=transport._process_metadata(os.getpid())[1]
            manifest,profiles,grants=policy(root,public,binary,roots)
            entry=ROOT/'tasks/g0-proxy-continuation/cold-start/scripts/projectproxy_launchd_entrypoint.py'
            child=await asyncio.create_subprocess_exec(binary,'-I','-B',str(entry),'--manifest',str(manifest),
                '--listener-fd','-1',env={'PATH':'/usr/bin:/bin','HOME':str(root)},
                stdout=asyncio.subprocess.PIPE,stderr=asyncio.subprocess.PIPE)
            stdout,stderr=await asyncio.wait_for(child.communicate(),3)
            assert child.returncode==1
            receipts=list((root/'state').glob('*/startup-failure.json'))
            assert len(receipts)==1
            failure=json.loads(receipts[0].read_text())
            assert failure['stage']=='activate_listener' and failure['errno']==9
            assert failure['failure_type']=='OSError'
            assert 'message' not in failure and 'raw' not in failure
            assert receipts[0].stat().st_mode&0o777==0o600
            assert not public.exists()
    run(scenario())


def test_service_publishes_owner_only_ready_receipt_before_shutdown():
    from types import SimpleNamespace as NS
    from test_activation_protocol import CLIENT,SERVER,THREAD,frame
    p=module()
    with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw, ExitStack() as roots:
        root=Path(raw); public=root/'p.sock'; binary=transport._process_metadata(os.getpid())[1]
        manifest,profiles,grants=policy(root,public,binary,roots)
        proxy=p.ActivationService.from_manifest(manifest)
        birth,executable=transport._process_metadata(os.getpid())
        peer=transport.PeerIdentity(pid=os.getpid(),uid=os.getuid(),birth=birth,executable=executable,
            pid_available=True,uid_available=True,birth_available=True,executable_available=True,source='offline-owned')
        proxy.backend_records.append({'state':'connected','pid':peer.pid,'birth':peer.birth,
            'frontend':p._peer(peer),'profile_id':'a','lease_id':'fixture-lease'})
        sink=proxy.sink
        sink.on_connect(NS(connection_id='fixture-c',epoch=1,frontend_peer=peer,backend_peer=peer))
        def send(data,client=False):
            sink.on_data(NS(connection_id='fixture-c',epoch=1,direction='frontend_to_backend' if client else 'backend_to_frontend',data=data))
        send(CLIENT,True); send(SERVER)
        send(frame({'id':1,'method':'initialize'},True),True)
        send(frame({'id':1,'result':{'codexHome':profiles['a']['codex_home'],'auth':CANARY.decode('utf-8','replace')}}))
        send(frame({'method':'initialized'},True),True)
        cwd=profiles['a']['workspace']
        send(frame({'id':2,'method':'thread/start','params':{'cwd':cwd}},True),True)
        send(frame({'id':2,'result':{'thread':{'id':THREAD,'cwd':cwd}}}))
        assert not list(proxy.receipt_dir.glob('ready-*.json'))
        send(frame({'method':'thread/started','params':{'thread':{'id':THREAD,'cwd':cwd}}}))
        ready=proxy.receipt_dir/'ready-fixture-lease.json'
        row=json.loads(ready.read_text())
        assert row['activation_id']==proxy.activation_id and row['version']==1
        assert row['manifest_sha256']==digest(manifest)
        assert row['profile_id']=='a' and row['initialize']['home_match'] is True
        assert ready.stat().st_mode&0o777==0o600
        assert not list(proxy.receipt_dir.glob('.ready-*'))
        assert not (proxy.receipt_dir/'activation.json').exists()
        assert profiles['a']['codex_home'] not in ready.read_text()
        assert CANARY.decode('utf-8','replace') not in ready.read_text()
