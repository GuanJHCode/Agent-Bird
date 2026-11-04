"""Opt-in exact native tool-boundary regression; no model or real credentials."""
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import threading

import pytest

ROOT=Path(__file__).resolve().parents[2]
sys.path.insert(0,str(ROOT/'plugins/codex-orchestrator/lib'))
from trial.codex_exec import (
    NATIVE_ADMISSIONS,
    NATIVE_COMPANIONS,
    render_native_config,
    verify_binary,
)
import test_native_auth_tripwire as fixture
from trial import codex_exec


@pytest.mark.skipif(os.environ.get('CODEX_RUN_NATIVE_TOOL_POLICY')!='1',reason='requires exact-bundle native tool policy opt-in')
@pytest.mark.parametrize('role', ['reviewer', 'implementer'])
def test_fixed_native_named_profile_preserves_host_and_auth_boundaries(tmp_path,monkeypatch,role):
    binary=os.environ['CODEX_TRIAL_BUNDLE_BINARY']
    version=os.environ.get('CODEX_TRIAL_NATIVE_VERSION','0.154.0')
    pin=fixture._load_candidate_pin(binary,version)
    if pin.companion_sha256 is not None:
        monkeypatch.setitem(NATIVE_ADMISSIONS,version,pin.sha256)
        monkeypatch.setitem(NATIVE_COMPANIONS,version,pin.companion_sha256)
    verify_binary(binary,version,pin.sha256)
    root=tmp_path.resolve();state=root/'state';work=root/'work';work.mkdir(mode=0o700)
    scratch=state/'host-spool/launch/attempt/segment/scratch';scratch.mkdir(mode=0o700,parents=True)
    home=scratch/'codex-home';home.mkdir(mode=0o700)
    for name in ('tmp',): (home/name).mkdir(mode=0o700)
    for name in ('sqlite','native-tmp'): (scratch/name).mkdir(mode=0o700)
    cap=state/'host-spool/launch/.reports/own/capability.json';cap.parent.mkdir(mode=0o700,parents=True);(cap.parent/'artifacts').mkdir(mode=0o700)
    original=root/'original-auth.json'
    protected=[state/'host-spool/sibling',state/'control/secret',state/'owners/secret',state/'host-bootstrap/secret',state/'state.db',state/'state.db-wal',state/'state.db-shm',original,home/'auth.json']
    for path in protected:
        path.parent.mkdir(mode=0o700,parents=True,exist_ok=True);path.write_text('SYNTHETIC_SECRET');path.chmod(0o600)
    cap.write_text('SYNTHETIC_CAP');cap.chmod(0o600);(work/'TOKEN.txt').write_text('OFFLINE_MARKER')
    policy={'version':2,'role':role,'timeout_ms':15000,'scratch':str(scratch),'state_root':str(state),'report_capability':str(cap)}
    (work/'CHANGE.txt').write_text('old')
    git=root/'repo.git';git.mkdir(mode=0o700);private=git/'worktrees/work';private.mkdir(mode=0o700,parents=True)
    (work/'.git').write_text('gitdir: '+str(private));(git/'config').write_text('metadata');(private/'HEAD').write_text('metadata')
    if role == 'implementer':
        policy.update(workspace=str(work),write_paths=[str(work/'CHANGE.txt')],git_paths=[str(work/'.git'),str(git),str(private)])
    codex_exec.validate_host_policy(policy,scratch)

    (home/'config.toml').write_text(render_native_config(policy,original,home));(home/'config.toml').chmod(0o600);protected.append(home/'config.toml')
    listener=socket.socket();listener.bind(('127.0.0.1',0));listener.listen(3);listener.settimeout(0.1)
    connections=[];done=threading.Event()
    def listen():
        while not done.is_set():
            try:connection,_=listener.accept();connections.append(True);connection.close()
            except TimeoutError:pass
    thread=threading.Thread(target=listen);thread.start()
    script='''test "$CODEX_SANDBOX" = seatbelt || exit 10
[ "$(/bin/cat "$1/TOKEN.txt")" = OFFLINE_MARKER ] || exit 11
workspace=$1; scratch=$2; cap=$3; port=$4; role=$5; shift 5
if [ "$role" = implementer ]; then printf changed > "$workspace/CHANGE.txt" || exit 16; else if (printf changed > "$workspace/CHANGE.txt") 2>/dev/null; then exit 17; fi; fi
for metadata in "$workspace/.git" "$workspace/../repo.git/config" "$workspace/../repo.git/worktrees/work/HEAD"; do if (printf forbidden > "$metadata") 2>/dev/null; then exit 18; fi; done
[ "$(/bin/cat "$cap")" = SYNTHETIC_CAP ] || exit 12
for secret in "$@"; do if /bin/cat "$secret" >/dev/null 2>&1; then exit 13; fi; done
if (printf forbidden > "$workspace/forbidden") 2>/dev/null; then exit 14; fi
printf allowed > "$scratch/allowed"
printf allowed > "$(/usr/bin/dirname "$cap")/artifacts/allowed"
if /usr/bin/nc -G 1 -w 1 -z 127.0.0.1 "$port" >/dev/null 2>&1; then exit 15; fi
printf 'NATIVE_POLICY_PASS\\n'
'''
    env={'PATH':'/usr/bin:/bin:/usr/sbin:/sbin','HOME':str(root),'CODEX_HOME':str(home),'CODEX_SQLITE_HOME':str(scratch/'sqlite'),'TMPDIR':str(scratch/'native-tmp')}
    try:
        result=subprocess.run([binary,'sandbox','--permission-profile',('orchestrator_workspace_write' if role=='implementer' else 'orchestrator_readonly'),'-C',str(work),'--','/bin/sh','-c',script,'probe',str(work),str(scratch),str(cap),str(listener.getsockname()[1]),role,*[str(x) for x in protected]],env=env,capture_output=True,timeout=25)
        assert result.returncode==0,result.stderr.decode(errors='replace')
        assert result.stdout==b'NATIVE_POLICY_PASS\n'
        assert not connections
    finally:
        done.set();thread.join(2);listener.close()
