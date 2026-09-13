"""Actual dummy sandbox/backend/UDS/WS/HTTP chain; no native CLI execution."""
import asyncio
import hashlib
import json
import os
from pathlib import Path
import sys
import tempfile
import pytest

ROOT=Path(__file__).resolve().parents[3]
for path in (ROOT/'tasks/g0-completion/scripts',ROOT/'tasks/g0-tui-proxy/scripts',ROOT/'tasks/g0-auth-preserving-activation/scripts'):
    sys.path.insert(0,str(path))
import auth_isolation as isolation
import proxy_transport
from synthetic_responses import SyntheticEndpoint
from synthetic_native_fixture import prepare
BINARY=Path('/opt/homebrew/Cellar/python@3.13/3.13.5/Frameworks/Python.framework/Versions/3.13/Resources/Python.app/Contents/MacOS/Python')


def implementation():
    assert (ROOT/'tasks/g0-completion/scripts/synthetic_native_transport.py').is_file(), 'transport execution chain missing'
    import synthetic_native_transport
    return synthetic_native_transport


def prepared_fixture(root,endpoint):
    spec=isolation.IsolationSpec(root,root/'h',root/'c',root/'w','transport-dummy',root/'p.sock',root/'b'/'backend.sock',
        (root.parent/f'{root.name}-protected',),(root.parent/f'{root.name}-protected'/'auth.json',),owned_loopback_port=endpoint.port)
    return prepare(endpoint,spec,executable=BINARY)


def setup_plan(runner,prepared,monkeypatch,mode):
    digest=hashlib.sha256(BINARY.read_bytes()).hexdigest()
    monkeypatch.setattr(runner,'NATIVE_SHA256',digest)
    plan=runner.prepare_transport(prepared,prepared.spec.task_root/'case',native_executable=BINARY,
        expected_native_sha256=digest,expected_source_pins=runner.source_pins())
    real_spawn=runner.GuardedChild.spawn
    async def spawn(argv,**kwargs):
        assert argv[:2]==['/usr/bin/sandbox-exec','-f']
        assert argv[3:]==[str(BINARY),'app-server','--listen','unix://'+str(prepared.spec.backend_socket)]
        replacement=argv[:3]+[str(BINARY),'-I','-B',str(Path(__file__).parent/'fixtures/synthetic_rpc_backend.py'),
            str(prepared.spec.backend_socket),mode]
        return await real_spawn(replacement,**kwargs)
    monkeypatch.setattr(runner.GuardedChild,'spawn',spawn)
    return plan


def test_complete_dummy_transport_proves_same_thread_two_turns_and_history(monkeypatch):
    runner=implementation()
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw,SyntheticEndpoint() as endpoint:
            prepared=prepared_fixture(Path(raw),endpoint);plan=setup_plan(runner,prepared,monkeypatch,'success')
            result=await runner.run_transport(plan,prepared)
            assert result['status']=='passed',result
            assert result['thread_id']=='synthetic-thread-1'
            assert result['turn_ids']==['synthetic-turn-1','synthetic-turn-2']
            assert result['history_verified'] is True and result['business_ack_verified'] is False
            assert result['cleanup']['child_reaped'] and result['cleanup']['socket_removed']
            assert result['backend']['pid']!=result['backend']['guardian_pid']
            assert result['initialize_home_match'] and result['synthetic_endpoint']['accepted_requests']==2
            assert (prepared.spec.workspace/'dummy-history.json').exists()
            assert not prepared.spec.backend_socket.exists()
            assert (plan.case_dir/'result.json').stat().st_mode&0o777==0o600
            methods=[item['method'] for item in result['envelopes'] if item['direction']=='client']
            assert methods==['initialize','initialized','thread/start','turn/start','turn/start','thread/read']
            assert all('params' not in item and 'result' not in item for item in result['envelopes'])
            with pytest.raises(FileExistsError): await runner.run_transport(plan,prepared)
    asyncio.run(asyncio.wait_for(scenario(),10))


@pytest.mark.parametrize('mode,accepted',[('wrong-home',0),('lost-second-receipt',1),('bad-history',2)],ids=['wrong-home','uncertain-no-resend','history-mismatch'])
def test_failure_never_passes_or_resends(monkeypatch,mode,accepted):
    runner=implementation()
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw,SyntheticEndpoint() as endpoint:
            prepared=prepared_fixture(Path(raw),endpoint);plan=setup_plan(runner,prepared,monkeypatch,mode)
            result=await runner.run_transport(plan,prepared)
            assert result['status']=='unknown'
            assert result['synthetic_endpoint']['accepted_requests']==accepted
            assert result['cleanup']['child_reaped'] and result['cleanup']['socket_removed']
            sends=[item for item in result['envelopes'] if item['direction']=='client' and item['method']=='turn/start']
            assert len(sends)==(0 if mode=='wrong-home' else 2)
            assert 'params' not in json.loads((plan.case_dir/'result.json').read_text())
    asyncio.run(asyncio.wait_for(scenario(),10))


def test_preflight_drift_or_missing_pins_stops_before_backend_launch(monkeypatch):
    runner=implementation()
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw,SyntheticEndpoint() as endpoint:
            prepared=prepared_fixture(Path(raw),endpoint);plan=setup_plan(runner,prepared,monkeypatch,'success')
            (prepared.spec.codex_home/'config.toml').write_text('model_provider="outside"')
            result=await runner.run_transport(plan,prepared)
            assert result['status']=='unknown' and result.get('backend') is None
            assert endpoint.snapshot()['attempts']==0
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw,SyntheticEndpoint() as endpoint:
            prepared=prepared_fixture(Path(raw),endpoint)
            with pytest.raises(ValueError,match='pins'):
                runner.prepare_transport(prepared,Path(raw)/'case',native_executable=BINARY,
                    expected_native_sha256=hashlib.sha256(BINARY.read_bytes()).hexdigest(),expected_source_pins={})
            assert not (Path(raw)/'case').exists()
    asyncio.run(asyncio.wait_for(scenario(),10))


def test_unexpected_server_request_is_unknown_and_never_stores_its_raw_identifiers(monkeypatch):
    runner=implementation()
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw,SyntheticEndpoint() as endpoint:
            prepared=prepared_fixture(Path(raw),endpoint);plan=setup_plan(runner,prepared,monkeypatch,'server-request')
            result=await runner.run_transport(plan,prepared)
            assert result['status']=='unknown' and result['synthetic_endpoint']['accepted_requests']==0
            stored=(plan.case_dir/'result.json').read_text()
            assert 'untrusted-private-marker' not in stored
            assert result['cleanup']['child_reaped']
    asyncio.run(asyncio.wait_for(scenario(),10))


def test_pre_spawn_original_deadline_cannot_be_reset_by_preflight(monkeypatch):
    runner=implementation()
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw,SyntheticEndpoint() as endpoint:
            prepared=prepared_fixture(Path(raw),endpoint);plan=setup_plan(runner,prepared,monkeypatch,'success')
            clock=[100.0];monkeypatch.setattr(runner,'now',lambda:clock[0])
            verify=prepared.verify
            def late_verify():verify();clock[0]=111.0
            monkeypatch.setattr(prepared,'verify',late_verify)
            result=await runner.run_transport(plan,prepared)
            assert result['status']=='unknown' and result['backend'] is None
            assert 'spawn_started_raw' not in result and endpoint.snapshot()['attempts']==0
    asyncio.run(asyncio.wait_for(scenario(),10))


def test_owned_binary_mode_drift_after_prepare_refuses_spawn(monkeypatch):
    import shutil
    runner=implementation()
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-bin-',dir='/private/tmp') as binary_dir:
            copied=Path(binary_dir)/'python';shutil.copy2(BINARY,copied);copied.chmod(0o755)
            monkeypatch.setattr(sys.modules[__name__],'BINARY',copied)
            with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw,SyntheticEndpoint() as endpoint:
                prepared=prepared_fixture(Path(raw),endpoint);plan=setup_plan(runner,prepared,monkeypatch,'success')
                copied.chmod(0o775)
                result=await runner.run_transport(plan,prepared)
                assert result['status']=='unknown' and result['backend'] is None
                assert 'spawn_started_raw' not in result
    asyncio.run(asyncio.wait_for(scenario(),10))


def test_case_replacement_between_check_and_open_never_writes_replacement(monkeypatch):
    runner=implementation()
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw,SyntheticEndpoint() as endpoint:
            prepared=prepared_fixture(Path(raw),endpoint);plan=setup_plan(runner,prepared,monkeypatch,'success')
            real_open=os.open;swapped=[False]
            def race(path,*args,**kwargs):
                if isinstance(path,(str,Path)) and Path(path)==plan.case_dir and not swapped[0]:
                    swapped[0]=True
                    plan.case_dir.rename(plan.case_dir.with_name('original-case'))
                    plan.case_dir.mkdir(mode=0o700);(plan.case_dir/'sentinel').write_text('owned-test-marker')
                return real_open(path,*args,**kwargs)
            monkeypatch.setattr(os,'open',race)
            with pytest.raises(ValueError,match='case'):
                await runner.run_transport(plan,prepared)
            assert sorted(p.name for p in plan.case_dir.iterdir())==['sentinel']
    asyncio.run(asyncio.wait_for(scenario(),10))


def test_unlisted_notification_cannot_enter_readiness(monkeypatch):
    runner=implementation()
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw,SyntheticEndpoint() as endpoint:
            prepared=prepared_fixture(Path(raw),endpoint);plan=setup_plan(runner,prepared,monkeypatch,'unlisted-notification')
            result=await runner.run_transport(plan,prepared)
            assert result['status']=='unknown' and endpoint.snapshot()['accepted_requests']==0
    asyncio.run(asyncio.wait_for(scenario(),10))


def test_initialize_and_thread_start_share_original_startup_deadline(monkeypatch):
    runner=implementation()
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw,SyntheticEndpoint() as endpoint:
            prepared=prepared_fixture(Path(raw),endpoint);plan=setup_plan(runner,prepared,monkeypatch,'success')
            clock=[100.0];monkeypatch.setattr(runner,'now',lambda:clock[0])
            receive=runner._Rpc.receive
            async def advancing_receive(self,end):
                packet=await receive(self,end)
                if packet.get('id')==1:clock[0]=105.0
                if packet.get('id')==2:clock[0]=111.0
                return packet
            monkeypatch.setattr(runner._Rpc,'receive',advancing_receive)
            result=await runner.run_transport(plan,prepared)
            assert result['status']=='unknown' and endpoint.snapshot()['accepted_requests']==0
            assert not any(item['method']=='turn/start' for item in result['envelopes'])
    asyncio.run(asyncio.wait_for(scenario(),10))


def test_endpoint_close_failure_still_preserves_unknown_result(monkeypatch):
    runner=implementation()
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw,SyntheticEndpoint() as endpoint:
            prepared=prepared_fixture(Path(raw),endpoint);plan=setup_plan(runner,prepared,monkeypatch,'success')
            close=endpoint.close
            def failing_close():close();raise OSError(5,'synthetic-close-failure')
            endpoint.close=failing_close
            try:result=await runner.run_transport(plan,prepared)
            finally:endpoint.close=close
            assert result['status']=='unknown' and result['cleanup']['child_reaped']
            assert json.loads((plan.case_dir/'result.json').read_text())['status']=='unknown'
            assert result['endpoint_close_failure']['errno']==5
    asyncio.run(asyncio.wait_for(scenario(),10))


def test_late_cleanup_cannot_report_pass_after_original_total_deadline(monkeypatch):
    runner=implementation()
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw,SyntheticEndpoint() as endpoint:
            prepared=prepared_fixture(Path(raw),endpoint);plan=setup_plan(runner,prepared,monkeypatch,'success')
            clock=[100.0];monkeypatch.setattr(runner,'now',lambda:clock[0])
            close=endpoint.close
            def late_close():close();clock[0]=221.0
            endpoint.close=late_close
            result=await runner.run_transport(plan,prepared)
            assert result['status']=='unknown' and result['deadline_exceeded'] is True
            assert result['cleanup']['child_reaped'] and result['history_verified']
    asyncio.run(asyncio.wait_for(scenario(),10))


@pytest.mark.parametrize('mode,method,known',[
    ('unlisted-notification','untrusted-private-marker-notification',None),
    ('official-unlisted-notification','thread/goal/updated','thread/goal/updated'),
],ids=['private-name-hash-only','official-name-diagnostic-only'])
def test_unknown_notification_diagnostic_never_admits_or_records_private_name(monkeypatch,mode,method,known):
    runner=implementation()
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw,SyntheticEndpoint() as endpoint:
            prepared=prepared_fixture(Path(raw),endpoint);plan=setup_plan(runner,prepared,monkeypatch,mode)
            result=await runner.run_transport(plan,prepared)
            assert result['status']=='unknown' and result['turn_ids']==[]
            assert endpoint.snapshot()['accepted_requests']==0 and result['cleanup']['child_reaped']
            records=[item for item in result['envelopes'] if item['method']=='unlisted']
            assert len(records)==1
            assert records[0]['unknown_method_sha256']==hashlib.sha256(method.encode()).hexdigest()
            assert records[0]['method_length']==len(method.encode())
            assert records[0].get('diagnostic_known_method')==known
            assert method not in runner.SAFE_METHODS
            assert 'untrusted-private-marker' not in (plan.case_dir/'result.json').read_text()
    asyncio.run(asyncio.wait_for(scenario(),10))


def test_remote_status_before_thread_start_response_is_opaque_background_notification(monkeypatch):
    runner=implementation()
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw,SyntheticEndpoint() as endpoint:
            prepared=prepared_fixture(Path(raw),endpoint);plan=setup_plan(runner,prepared,monkeypatch,'remote-status')
            result=await runner.run_transport(plan,prepared)
            assert result['status']=='passed' and result['history_verified']
            assert endpoint.snapshot()['accepted_requests']==2
            envelopes=result['envelopes']
            position=next(i for i,item in enumerate(envelopes) if item['method']=='remoteControl/status/changed')
            reply=next(i for i,item in enumerate(envelopes) if item['direction']=='server' and item['request_id']==2)
            assert position<reply
            assert set(envelopes[position])=={'direction','method','request_id','bytes','sha256'}
            assert not any(item['direction']=='client' and (item['method'] or '').startswith('remoteControl/') for item in envelopes)
            assert 'private-remote-' not in (plan.case_dir/'result.json').read_text()
            assert result['cleanup']['child_reaped'] and result['cleanup']['socket_removed']
    asyncio.run(asyncio.wait_for(scenario(),10))


@pytest.mark.parametrize('mode',['remote-status-invalid-status','remote-status-missing-name','remote-status-bad-environment','remote-status-server-request'])
def test_remote_status_malformed_payload_or_request_cannot_pass(monkeypatch,mode):
    runner=implementation()
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw,SyntheticEndpoint() as endpoint:
            prepared=prepared_fixture(Path(raw),endpoint);plan=setup_plan(runner,prepared,monkeypatch,mode)
            result=await runner.run_transport(plan,prepared)
            assert result['status']=='unknown' and result['turn_ids']==[]
            assert endpoint.snapshot()['accepted_requests']==0
            assert result['cleanup']['child_reaped'] and result['cleanup']['socket_removed']
            assert 'private-remote-' not in (plan.case_dir/'result.json').read_text()
    asyncio.run(asyncio.wait_for(scenario(),10))
