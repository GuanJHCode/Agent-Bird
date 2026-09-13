import os,sys,tempfile
from pathlib import Path
from types import SimpleNamespace
import pytest
ROOT=Path(__file__).resolve().parents[3]
for path in (ROOT/'tasks/g0-global-delivery-validation/scripts',ROOT/'tasks/g0-tui-proxy/scripts',ROOT/'tasks/g0-completion/scripts'):
    sys.path.insert(0,str(path))
import inherited_fd_scheduler as module
from synthetic_native_transport import now
PYTHON=Path('/opt/homebrew/Cellar/python@3.13/3.13.5/Frameworks/Python.framework/Versions/3.13/Resources/Python.app/Contents/MacOS/Python')


@pytest.mark.parametrize('fault',['popen','missing-identity'])
def test_bootstrap_failure_closes_owned_fd_and_reaps_created_child(monkeypatch,fault):
    with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw:
        root=Path(raw);home=root/'h';home.mkdir(mode=0o700);script=root/'child.py';script.write_text('import time; time.sleep(.1)\n')
        spec=SimpleNamespace(home=home,socket_path=home/'p.sock',program_arguments=(str(PYTHON),'-I','-B',str(script)),label='test',plist_path=home/'job')
        scheduler=module.InheritedFdScheduler(spec,now);scheduler.phase_end=now()+3
        if fault=='popen':
            def reject(*args,**kwargs):raise OSError('controlled failure')
            monkeypatch.setattr(module.subprocess,'Popen',reject)
        else:monkeypatch.setattr(module,'_process_metadata',lambda pid:(None,None))
        try:
            with pytest.raises((OSError,ValueError)):scheduler.bootstrap('fake',spec.plist_path)
            assert scheduler.listener.fileno()==-1 and not spec.socket_path.exists()
            if scheduler.process is not None:assert scheduler.process.poll() is not None
        finally:
            if scheduler.process is not None:scheduler.process.wait(timeout=2)
            if scheduler.listener is not None:scheduler.listener.close()
