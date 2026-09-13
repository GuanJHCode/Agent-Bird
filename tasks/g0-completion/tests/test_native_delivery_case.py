"""Real private ActivationService/process/Go combination with a Python backend."""
import asyncio
import hashlib
import json
import os
from pathlib import Path
import sys
import tempfile
import pytest

ROOT=Path(__file__).resolve().parents[3]
for path in (ROOT/'tasks/g0-completion/scripts',ROOT/'tasks/g0-auth-preserving-activation/scripts',ROOT/'tasks/g0-proxy-continuation/cold-start/scripts'):
    sys.path.insert(0,str(path))
from synthetic_responses import SyntheticEndpoint
from synthetic_native_fixture import prepare as prepare_synthetic
import auth_isolation as isolation

PYTHON=Path('/opt/homebrew/Cellar/python@3.13/3.13.5/Frameworks/Python.framework/Versions/3.13/Resources/Python.app/Contents/MacOS/Python')
GO=Path('/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go')


def implementation():
    assert (ROOT/'tasks/g0-completion/scripts/native_delivery_case.py').is_file(), 'combined execution chain missing'
    import native_delivery_case
    return native_delivery_case


def make_prepared(root,endpoint):
    spec=isolation.IsolationSpec(root,root/'h',root/'c',root/'w','delivery-fixture',root/'p.sock',root/'b'/'b.sock',
        (root.parent/f'{root.name}-protected',),(root.parent/f'{root.name}-protected'/'auth.json',),owned_loopback_port=endpoint.port)
    return prepare_synthetic(endpoint,spec,executable=PYTHON)


def prepare_case(runner,prepared,case_root,monkeypatch,mode):
    monkeypatch.setattr(runner.transport,'NATIVE_SHA256',hashlib.sha256(PYTHON.read_bytes()).hexdigest())
    plan=runner.prepare_delivery(prepared,case_root/'case',python_executable=PYTHON,go_executable=GO,
        expected_native_sha256=hashlib.sha256(PYTHON.read_bytes()).hexdigest(),expected_source_pins=runner.source_pins())
    spawn=runner.activation_service.GuardedChild.spawn
    async def backend(argv,**kwargs):
        assert argv[:2]==['/usr/bin/sandbox-exec','-f']
        assert argv[3:6]==[str(PYTHON),'app-server','--listen']
        socket_path=argv[6].removeprefix('unix://')
        command=argv[:3]+[str(PYTHON),'-I','-B',str(ROOT/'tasks/g0-completion/tests/fixtures/native_delivery_backend.py'),socket_path,mode]
        return await spawn(command,**kwargs)
    monkeypatch.setattr(runner.activation_service.GuardedChild,'spawn',backend)
    return plan


def test_actual_private_service_owner_helper_and_go_ack_pipeline(monkeypatch,tmp_path):
    runner=implementation()
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw,SyntheticEndpoint() as endpoint:
            prepared=make_prepared(Path(raw),endpoint);plan=prepare_case(runner,prepared,tmp_path,monkeypatch,'success')
            result=await runner.run_delivery(plan,prepared)
            assert result['status']=='passed',result.get('failure')
            assert result['python_owner_verified'] and not result['ordinary_tui_verified']
            assert not result['durable_history_verified'] and result['business_ack_verified']
            assert result['owner']['pid']!=result['helper']['pid']!=os.getpid()
            assert result['helper_proof']['public_peer']['pid']==os.getpid()
            assert result['helper_proof']['public_peer']['pid']!=result['service']['backend_records'][0]['pid']
            assert len(result['service']['backend_records'])==1
            assert result['service']['backend_records'][0]['owner_thread_id']==result['owner']['thread_id']
            assert len(result['service']['backend_records'][0]['helpers'])==1
            assert result['go_status']['business_status']=='complete'
            assert result['go_status']['ack_count']==1 and result['go_status']['send_allowed'] is False
            assert result['ack']['records'][0]['effect_count']==1
            assert result['synthetic_endpoint']['accepted_requests']==2
            assert result['cleanup']['owner_exit']==0 and result['cleanup']['helper_exit']==0
            assert result['cleanup']['helper_eof'] and result['cleanup']['public_socket_removed']
            assert all(row['process_stopped'] and row['socket_removed'] for row in result['service']['backend_records'])
            assert (prepared.spec.workspace/'go-task'/'batch-ack-event_result.json').is_file()
            binding=json.loads((prepared.spec.workspace/'go-task/delivery-owner-binding.json').read_text())
            assert binding['owner_context_sha256']==result['lease']['owner_context_sha256']
            assert result['cleanup']['helper_eof_raw']>=result['cleanup']['owner_close_sent_raw']
            assert (plan.case_dir/'result.json').stat().st_mode&0o777==0o600
            with pytest.raises(FileExistsError):await runner.run_delivery(plan,prepared)
    asyncio.run(asyncio.wait_for(scenario(),120))


@pytest.mark.parametrize('mode',['bad-history','drop-helper-receipt'])
def test_unknown_delivery_never_acknowledges_or_resends(monkeypatch,tmp_path,mode):
    runner=implementation()
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw,SyntheticEndpoint() as endpoint:
            prepared=make_prepared(Path(raw),endpoint);plan=prepare_case(runner,prepared,tmp_path,monkeypatch,mode)
            result=await runner.run_delivery(plan,prepared)
            assert result['status']=='unknown' and not result['business_ack_verified']
            assert not (prepared.spec.workspace/'go-task'/'batch-ack-event_result.json').exists()
            assert len(result['service']['backend_records'])==1
            assert result['synthetic_endpoint']['accepted_requests']<=2
            assert all(row['process_stopped'] and row['socket_removed'] for row in result['service']['backend_records'])
            assert result['cleanup']['public_socket_removed']
    asyncio.run(asyncio.wait_for(scenario(),120))


def test_helper_eof_before_owner_close_retains_ack_but_cannot_pass(monkeypatch,tmp_path):
    import time
    runner=implementation();original=runner._go
    def delayed_status(binary,args,env,timeout=10):
        if args[0]=='batch-status':time.sleep(.1)
        return original(binary,args,env,timeout)
    monkeypatch.setattr(runner,'_go',delayed_status)
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw,SyntheticEndpoint() as endpoint:
            prepared=make_prepared(Path(raw),endpoint);plan=prepare_case(runner,prepared,tmp_path,monkeypatch,'early-helper-eof')
            result=await runner.run_delivery(plan,prepared)
            assert result['status']=='unknown'
            assert result['business_ack_verified'] and result['ack']['records'][0]['effect_count']==1
            assert result['cleanup']['helper_eof_raw']<result['cleanup']['owner_close_sent_raw']
            assert result['cleanup']['public_socket_removed']
    asyncio.run(asyncio.wait_for(scenario(),120))


def test_stop_client_refuses_wrong_birth_for_real_child():
    runner=implementation()
    async def scenario():
        child=await asyncio.create_subprocess_exec(str(PYTHON),'-I','-B','-c','import time; time.sleep(30)',stdin=asyncio.subprocess.PIPE)
        actual=runner.peer_for_process(child.pid)
        try:
            with pytest.raises(ValueError,match='identity'):
                await runner.stop_client(child,runner.transport.now()+3,{**actual,'birth':'wrong-birth'})
            assert child.returncode is None and runner.peer_for_process(child.pid)==actual
        finally:
            child.kill();await child.wait()
    asyncio.run(scenario())


def test_public_socket_replacement_during_quarantine_is_retained(monkeypatch,tmp_path):
    import socket
    runner=implementation();short=tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp');tmp_path=Path(short.name);public=tmp_path/'p.sock'
    held=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM);held.bind(str(public));os.chmod(public,0o600)
    identity=runner.transport._socket_identity(public);parent=runner.transport._dir_identity(tmp_path);receipt={}
    rename=os.rename
    def raced(src,dst,**kwargs):
        public.unlink();public.write_bytes(b'replacement-must-survive')
        return rename(src,dst,**kwargs)
    monkeypatch.setattr(runner.os,'rename',raced)
    try:
        with pytest.raises(ValueError,match='socket identity'):
            runner.remove_public_socket(public,identity,parent,receipt)
        assert Path(receipt['public_socket_quarantine']).read_bytes()==b'replacement-must-survive'
        assert not receipt.get('public_socket_removed',False)
    finally:held.close();short.cleanup()


def test_original_deadline_late_go_does_not_start_successor(monkeypatch):
    runner=implementation();clock=[1.0];called=[]
    monkeypatch.setattr(runner.transport,'now',lambda:clock[0])
    def slow(*args,**kwargs):called.append(args[1][0]);clock[0]=12.;return {'status':'completed'}
    monkeypatch.setattr(runner,'_go',slow)
    async def scenario():
        with pytest.raises(TimeoutError):
            await runner.go_within(Path('/unused'),['start'],{},10.)
            await runner.go_within(Path('/unused'),['wait'],{},10.)
    asyncio.run(scenario());assert called==['start']


def test_expired_control_deadline_writes_no_bytes():
    from types import SimpleNamespace
    runner=implementation();writes=[]
    async def drain():pass
    child=SimpleNamespace(stdin=SimpleNamespace(write=writes.append,drain=drain))
    async def scenario():
        with pytest.raises(TimeoutError):await runner.send(child,{'action':'close'},runner.transport.now()-1)
    asyncio.run(scenario());assert writes==[]


def test_child_safe_error_survives_parent_without_raw_body():
    from types import SimpleNamespace
    runner=implementation()
    async def scenario():
        stream=asyncio.StreamReader();secret='secret-test-error-message-never-export'
        row={'event':'error','stage':'helper-open-public','type':'IncompleteReadError','errno':None,'message_sha256':hashlib.sha256(secret.encode()).hexdigest()}
        stream.feed_data(json.dumps(row).encode()+b'\n');stream.feed_eof()
        with pytest.raises(runner.SafeClientFailure) as captured:
            await runner.line(SimpleNamespace(stdout=stream),runner.transport.now()+1)
        assert captured.value.child_error==row
        assert secret not in str(captured.value)
    asyncio.run(scenario())


def test_actual_helper_admission_eof_keeps_original_safe_error(monkeypatch,tmp_path):
    runner=implementation()
    original=runner.activation_service.ActivationService._helper_admission
    def mismatched_context(service,peer,grant,digest):
        return original(service,peer,{**grant,'owner_context_sha256':'f'*64},digest)
    monkeypatch.setattr(runner.activation_service.ActivationService,'_helper_admission',mismatched_context)
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw,SyntheticEndpoint() as endpoint:
            prepared=make_prepared(Path(raw),endpoint);plan=prepare_case(runner,prepared,tmp_path,monkeypatch,'success')
            result=await runner.run_delivery(plan,prepared)
            assert result['status']=='unknown' and not result['business_ack_verified']
            error=result['failure']['child_error']
            assert error['stage']=='helper-open-public' and error['type']=='IncompleteReadError'
            assert result['service']['admission_diagnostics'][-1]['reason']=='helper_context'
            assert result['postflight']=={'fixture_valid':True,'source_pins_valid':True}
            assert len(error['message_sha256'])==64
            assert (prepared.spec.workspace/'go-task/batch-send-claim.json').exists()
            assert result['synthetic_endpoint']['accepted_requests']==1
            assert not result['service']['backend_records'][0].get('helpers')
            assert result['cleanup']['public_socket_removed']
    asyncio.run(asyncio.wait_for(scenario(),120))
