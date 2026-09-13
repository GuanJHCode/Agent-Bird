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
