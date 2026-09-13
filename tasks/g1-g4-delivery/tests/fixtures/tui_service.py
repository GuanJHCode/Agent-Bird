"""Unit-only real service/guardian with an actual Python backend instead of Codex."""
from pathlib import Path
import sys
ROOT=Path(__file__).resolve().parents[4]
sys.path.insert(0,str(ROOT/'tasks/g0-proxy-continuation/cold-start/scripts'))
import projectproxy_launchd_entrypoint as entry
original=entry.ActivationService.__module__
import activation_service
# Test-only evidence that the real final call sites use the immutable publisher.
import json,os
publish=activation_service._publish_receipt
def observed_publish(path,value,**kwargs):
    publish(path,value,**kwargs)
    if path.name=='activation.json' or path.name.startswith('backend-'):
        fd=os.open(path.parents[2]/'published-finals.jsonl',os.O_WRONLY|os.O_APPEND|os.O_CREAT,0o600)
        try:os.write(fd,(json.dumps({'name':path.name})+'\n').encode())
        finally:os.close(fd)
activation_service._publish_receipt=observed_publish
spawn=activation_service.GuardedChild.spawn
async def dummy_backend(argv,**kwargs):
    assert argv[:2]==['/usr/bin/sandbox-exec','-f']
    assert argv[4:6]==['app-server','--listen']
    socket_path=argv[6].removeprefix('unix://')
    marker=Path(kwargs['cwd'])/'dummy-mode.txt'
    mode=marker.read_text() if marker.exists() else 'owner-only-events'
    assert mode in ('owner-only-events','bad-history','initial-ambiguous','wire-notice')
    command=argv[:3]+[argv[3],'-I','-B',str(ROOT/'tasks/g1-g4-delivery/tests/fixtures/tui_backend.py'),socket_path,mode]
    return await spawn(command,**kwargs)
activation_service.GuardedChild.spawn=dummy_backend
raise SystemExit(entry.main())
