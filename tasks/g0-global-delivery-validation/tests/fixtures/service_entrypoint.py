"""Unit-only real service/guardian with an actual Python backend instead of Codex."""
from pathlib import Path
import sys
ROOT=Path(__file__).resolve().parents[4]
sys.path.insert(0,str(ROOT/'tasks/g0-proxy-continuation/cold-start/scripts'))
import projectproxy_launchd_entrypoint as entry
original=entry.ActivationService.__module__
import activation_service
spawn=activation_service.GuardedChild.spawn
async def dummy_backend(argv,**kwargs):
    assert argv[:2]==['/usr/bin/sandbox-exec','-f']
    assert argv[4:6]==['app-server','--listen']
    socket_path=argv[6].removeprefix('unix://')
    marker=Path(kwargs['cwd'])/'dummy-mode.txt'
    mode=marker.read_text() if marker.exists() else 'owner-only-events'
    assert mode in ('owner-only-events','bad-history')
    command=argv[:3]+[argv[3],'-I','-B',str(ROOT/'tasks/g0-completion/tests/fixtures/native_delivery_backend.py'),socket_path,mode]
    return await spawn(command,**kwargs)
activation_service.GuardedChild.spawn=dummy_backend
raise SystemExit(entry.main())
