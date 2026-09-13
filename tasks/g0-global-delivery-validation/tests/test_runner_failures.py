import asyncio,json,os,sys
from pathlib import Path
from types import SimpleNamespace
import pytest
ROOT=Path(__file__).resolve().parents[3]
sys.path.insert(0,str(ROOT/'tasks/g0-global-delivery-validation/scripts'))
import global_delivery_case as runner
PYTHON=Path('/opt/homebrew/Cellar/python@3.13/3.13.5/Frameworks/Python.framework/Versions/3.13/Resources/Python.app/Contents/MacOS/Python')


def test_transaction_failure_writes_phase_receipt_without_message(tmp_path):
    plan=SimpleNamespace(case=tmp_path,transaction=SimpleNamespace(receipt={'state':'NEW'},launchd=SimpleNamespace()))
    def failed():raise ValueError('secret-message-never-export')
    with pytest.raises(ValueError):runner.transaction_phase(plan,'register',failed,1)
    record=json.loads((tmp_path/'register-final.json').read_text())
    assert record['failure_type']=='ValueError' and record['receipt']['state']=='NEW'
    assert 'secret-message-never-export' not in str(record)


def test_failed_real_child_boot_retains_owned_stop_record(monkeypatch,tmp_path):
    path=tmp_path/'bad_client.py';path.write_text("import json,os,time\nprint(json.dumps({'event':'bad_boot','pid':os.getpid()}),flush=True)\ntime.sleep(.3)\n")
    monkeypatch.setattr(runner.base,'CLIENT',path)
    plan=SimpleNamespace(python=PYTHON);prepared=SimpleNamespace(spec=SimpleNamespace(workspace=tmp_path))
    async def scenario():
        try:
            with pytest.raises(ValueError) as caught:
                await runner.spawn_client(plan,prepared,'owner',{'PATH':'/usr/bin:/bin'},runner.now()+1)
            assert caught.value.spawn_record['stopped'] is True
            assert runner.base.proxy_transport._process_metadata(caught.value.spawn_record['pid'])==(None,None)
        finally:await asyncio.sleep(.4)
    asyncio.run(scenario())


def test_actual_deadline_command_is_reaped_without_background_retry(tmp_path):
    import subprocess
    pid_path=tmp_path/'child-pid'
    code=f"import os,time;open({str(pid_path)!r},'w').write(str(os.getpid()));time.sleep(2)"
    launch=runner.DeadlineLaunchctl((str(PYTHON),'-I','-B','-c',code));launch.phase_end=runner.now()+.15
    with pytest.raises(subprocess.TimeoutExpired):launch.query('dummy')
    pid=int(pid_path.read_text());assert runner.base.proxy_transport._process_metadata(pid)==(None,None)
    pid_path.unlink();launch.phase_end=runner.now()-1
    with pytest.raises(TimeoutError):launch.query('dummy')
    assert not pid_path.exists()
