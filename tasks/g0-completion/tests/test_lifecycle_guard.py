"""Real dummy controller death; no Codex/model/launchctl or real credentials."""
from contextlib import ExitStack
import asyncio
import json
import select
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import tempfile
import time

ROOT=Path(__file__).resolve().parents[3]
COLD=ROOT/'tasks/g0-proxy-continuation/cold-start'
for directory in (ROOT/'tasks/g0-tui-proxy/scripts',ROOT/'tasks/g0-auth-preserving-activation/scripts',COLD/'tests'):
    sys.path.insert(0,str(directory))
import proxy_transport
import owned_child_guard as guard
from test_activation_service import policy,register


def alive(pid,birth):
    return proxy_transport._process_metadata(pid)[0]==birth


def wait_absent(pid,birth,seconds=3):
    deadline=time.monotonic()+seconds
    while alive(pid,birth) and time.monotonic()<deadline: time.sleep(.02)
    return not alive(pid,birth)


def test_service_sigkill_stops_its_real_backend_without_finally():
    with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw, ExitStack() as roots:
        root=Path(raw); public=root/'p.sock'; binary=proxy_transport._process_metadata(os.getpid())[1]
        manifest,profiles,grants=policy(root,public,binary,roots)
        listener=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM); listener.bind(str(public)); listener.listen(4)
        public_identity=(public.stat().st_dev,public.stat().st_ino)
        frontend=subprocess.Popen([binary,'-B',str(COLD/'tests/fixtures/fake_frontend.py'),str(public)],
            stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,
            env={'PATH':'/usr/bin:/bin','HOME':str(root),'CODEX_HOME':str(root/'dummy-c')})
        service=None; backend_pid=None; backend_birth=None
        try:
            assert frontend.stdout.readline()==b'ready\n'
            register(grants,frontend,'a',binary)
            service=subprocess.Popen([binary,'-I','-B',str(COLD/'scripts/projectproxy_launchd_entrypoint.py'),
                '--manifest',str(manifest),'--listener-fd',str(listener.fileno())],pass_fds=(listener.fileno(),),
                stdout=subprocess.PIPE,stderr=subprocess.PIPE,env={'PATH':'/usr/bin:/bin','HOME':str(root)})
            frontend.stdin.write(b'{"action":"connect"}\n'); frontend.stdin.flush()
            assert select.select([frontend.stdout],[],[],3)[0], 'dummy backend startup timed out'
            header=json.loads(frontend.stdout.readline())
            backend_pid=header['pid']; backend_birth=proxy_transport._process_metadata(backend_pid)[0]
            assert backend_pid!=service.pid and backend_birth
            task_record=Path(profiles['a']['workspace'])/'progress.json'
            task_record.write_text('{"completed_steps":1}\n')
            before=task_record.read_bytes()
            service.kill(); service.wait(timeout=2)
            assert service.returncode==-signal.SIGKILL
            assert wait_absent(backend_pid,backend_birth), 'service SIGKILL left its registered backend alive'
            assert public.exists() and (public.stat().st_dev,public.stat().st_ino)==public_identity
            receipts=list((root/'state').glob('*/*/guardian-final.json'))
            assert len(receipts)==1
            outcome=json.loads(receipts[0].read_text())
            assert outcome['owner_pid']==service.pid and outcome['child_pid']==backend_pid
            assert outcome['guardian_pid'] not in (service.pid,backend_pid)
            assert outcome['reason']=='controller_eof' and outcome['child_reaped'] is True
            assert outcome['tree_stop_unproven'] is True and outcome['restart_allowed'] is False
            assert receipts[0].stat().st_mode&0o777==0o600
            assert task_record.read_bytes()==before
        finally:
            if service is not None and service.poll() is None: service.terminate(); service.wait(timeout=3)
            if frontend.poll() is None: frontend.terminate(); frontend.wait(timeout=2)
            if backend_pid and backend_birth and alive(backend_pid,backend_birth):
                os.kill(backend_pid,signal.SIGTERM)
                assert wait_absent(backend_pid,backend_birth), 'dummy fixture cleanup failed'
            listener.close()


async def guarded_dummy(directory,code):
    directory.mkdir(mode=0o700)
    binary=proxy_transport._process_metadata(os.getpid())[1]
    return await guard.GuardedChild.spawn([binary,'-B','-c',code],cwd=directory,
        env={'PATH':'/usr/bin:/bin','HOME':str(directory)},receipt_dir=directory,
        stdin=asyncio.subprocess.DEVNULL,stdout=asyncio.subprocess.PIPE,stderr=asyncio.subprocess.PIPE)


def test_guardian_escalates_ignored_term_and_reports_actual_child_reaping():
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-life-',dir='/private/tmp') as raw:
            child=await guarded_dummy(Path(raw)/'receipt',
                "import signal,time;signal.signal(signal.SIGTERM,signal.SIG_IGN);print('ready',flush=True);time.sleep(60)")
            assert await asyncio.wait_for(child.stdout.readline(),3)==b'ready\n'
            outcome=await child.stop(3)
            assert outcome['state']=='stopped' and outcome['child_reaped']
            assert outcome['term_requested'] and outcome['kill_requested'] and outcome['child_returncode']==-signal.SIGKILL
            assert outcome['child_pid']==child.pid and outcome['guardian_pid']==child.guardian_pid
            assert outcome['restart_allowed'] is False
    asyncio.run(asyncio.wait_for(scenario(),10))


def test_guardian_damage_reports_unknown_instead_of_claiming_cleanup():
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-life-',dir='/private/tmp') as raw:
            child=await guarded_dummy(Path(raw)/'receipt',"import time;print('ready',flush=True);time.sleep(60)")
            assert await asyncio.wait_for(child.stdout.readline(),3)==b'ready\n'
            birth=proxy_transport._process_metadata(child.pid)[0]
            try:
                os.kill(child.guardian_pid,signal.SIGKILL)
                deadline=time.monotonic()+3
                while child._supervisor.returncode is None and time.monotonic()<deadline:
                    await asyncio.sleep(.01)
                assert child._supervisor.returncode==-signal.SIGKILL
                outcome=await child.stop(1)
                assert outcome['state']=='unknown' and outcome['child_reaped'] is False
                assert outcome['restart_allowed'] is False and alive(child.pid,birth)
            finally:
                if alive(child.pid,birth): os.kill(child.pid,signal.SIGTERM)
                assert wait_absent(child.pid,birth)
    asyncio.run(asyncio.wait_for(scenario(),10))


def test_escaped_grandchild_is_explicitly_unproven_and_never_auto_restartable():
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-life-',dir='/private/tmp') as raw:
            code=("import subprocess,sys,time; p=subprocess.Popen([sys.executable,'-B','-c','import time;time.sleep(60)'],"
                  "start_new_session=True,stdin=subprocess.DEVNULL,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL);"
                  "print(p.pid,flush=True);time.sleep(60)")
            child=await guarded_dummy(Path(raw)/'receipt',code)
            grandchild=int(await asyncio.wait_for(child.stdout.readline(),3))
            birth=proxy_transport._process_metadata(grandchild)[0]
            try:
                outcome=await child.stop(3)
                assert outcome['child_reaped'] and outcome['tree_stop_unproven']
                assert not outcome['restart_allowed'] and alive(grandchild,birth)
            finally:
                if alive(grandchild,birth): os.kill(grandchild,signal.SIGTERM)
                assert wait_absent(grandchild,birth)
    asyncio.run(asyncio.wait_for(scenario(),10))


def test_wrong_pid_control_message_never_signals_the_unrelated_process():
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-life-',dir='/private/tmp') as raw:
            root=Path(raw)
            child=await guarded_dummy(root/'owned',"import time;print('ready',flush=True);time.sleep(60)")
            other=await guarded_dummy(root/'other',"import time;print('ready',flush=True);time.sleep(60)")
            await asyncio.wait_for(child.stdout.readline(),3);await asyncio.wait_for(other.stdout.readline(),3)
            birth=proxy_transport._process_metadata(other.pid)[0]
            try:
                guard._send(child._channel,{'nonce':child._initial['nonce'],'child_pid':other.pid,'action':'kill'})
                await asyncio.wait_for(child.wait(),3)
                outcome=child.outcome()
                assert outcome['state']=='unknown' and outcome['reason']=='invalid_control_message'
                assert outcome['child_pid']==child.pid and outcome['child_reaped']
                assert alive(other.pid,birth)
            finally:
                await child.stop(1);await other.stop(3)
    asyncio.run(asyncio.wait_for(scenario(),10))


def test_receipt_parent_replacement_preserves_records_on_the_owned_directory():
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-life-',dir='/private/tmp') as raw:
            root=Path(raw);directory=root/'receipt'
            child=await guarded_dummy(directory,"import time;print('ready',flush=True);time.sleep(60)")
            await asyncio.wait_for(child.stdout.readline(),3)
            retained=root/'retained';directory.rename(retained);directory.mkdir(mode=0o700)
            sentinel=directory/'unrelated';sentinel.write_text('preserve')
            outcome=await child.stop(3)
            assert (retained/'guardian-final.json').exists()
            assert not (directory/'guardian-final.json').exists() and sentinel.read_text()=='preserve'
            assert outcome['child_reaped'] and outcome['receipt_location_unchanged'] is False
            assert outcome['state']=='unknown' and outcome['restart_allowed'] is False
    asyncio.run(asyncio.wait_for(scenario(),10))


def test_missing_birth_is_explicit_unknown_with_real_child_stopped(monkeypatch):
    original=asyncio.create_subprocess_exec
    async def missing_birth_guard(*argv,**kwargs):
        if len(argv)>3 and str(argv[3])==str(Path(guard.__file__).resolve()):
            code=("import sys;sys.path.insert(0,"+repr(str(Path(guard.__file__).parent))+ ");"
                  "import owned_child_guard as g;g._birth=lambda pid:None;"
                  "a=dict(zip(sys.argv[1::2],sys.argv[2::2]));"
                  "r=int(a['--receipt-fd']) if '--receipt-fd' in a else a['--receipt-dir'];"
                  "raise SystemExit(g.guardian_main(int(a['--guardian-fd']),r,int(a['--owner-pid'])))")
            return await original(argv[0],'-I','-B','-c',code,*argv[4:],**kwargs)
        return await original(*argv,**kwargs)
    monkeypatch.setattr(asyncio,'create_subprocess_exec',missing_birth_guard)
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-life-',dir='/private/tmp') as raw:
            child=await guarded_dummy(Path(raw)/'receipt',"import time;time.sleep(60)")
            outcome=await child.stop(3)
            assert outcome['state']=='unknown' and outcome['child_birth'] is None
            assert outcome['child_reaped'] and outcome['restart_allowed'] is False
    asyncio.run(asyncio.wait_for(scenario(),10))


def test_partial_control_frame_then_eof_stops_the_actual_child():
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-life-',dir='/private/tmp') as raw:
            child=await guarded_dummy(Path(raw)/'receipt',"import time;print('ready',flush=True);time.sleep(60)")
            await asyncio.wait_for(child.stdout.readline(),3)
            child._channel.sendall(b'{"action":')
            child._channel.shutdown(socket.SHUT_WR)
            await asyncio.wait_for(child.wait(),3)
            outcome=await child.stop(1)
            assert outcome['child_reaped'] and outcome['reason']=='controller_eof'
    asyncio.run(asyncio.wait_for(scenario(),10))


def test_naturally_exited_child_between_poll_and_terminate_keeps_real_exit_status(monkeypatch):
    binary=proxy_transport._process_metadata(os.getpid())[1]
    child=subprocess.Popen([binary,'-B','-c',"import time;print('ready',flush=True);time.sleep(.2)"],
        stdout=subprocess.PIPE,env={'PATH':'/usr/bin:/bin'})
    assert child.stdout.readline()==b'ready\n'
    terminate=child.terminate
    def exit_before_signal():
        child.wait(timeout=3)
        terminate()
    monkeypatch.setattr(child,'terminate',exit_before_signal)
    result=guard._stop(child)
    assert result['child_reaped'] and result['child_returncode']==0


def test_hardlinked_final_receipt_cannot_prove_cleanup():
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-life-',dir='/private/tmp') as raw:
            directory=Path(raw)/'receipt'
            child=await guarded_dummy(directory,"import time;print('ready',flush=True);time.sleep(.2)")
            await asyncio.wait_for(child.stdout.readline(),3)
            assert await asyncio.wait_for(child.wait(),3)==0
            os.link(directory/'guardian-final.json',Path(raw)/'extra-link')
            result=await child.stop(1)
            assert result['state']=='unknown' and result['child_reaped'] is False
    asyncio.run(asyncio.wait_for(scenario(),10))


def test_guardian_killed_before_identity_ack_does_not_claim_no_child(monkeypatch):
    original=asyncio.create_subprocess_exec;supervisors=[]
    async def paused_identity_guard(*argv,**kwargs):
        if len(argv)>3 and str(argv[3])==str(Path(guard.__file__).resolve()):
            code=("import sys,time;sys.path.insert(0,"+repr(str(Path(guard.__file__).parent))+ ");"
                  "import owned_child_guard as g;g._birth=lambda pid:time.sleep(60);"
                  "a=dict(zip(sys.argv[1::2],sys.argv[2::2]));"
                  "raise SystemExit(g.guardian_main(int(a['--guardian-fd']),int(a['--receipt-fd']),int(a['--owner-pid'])))")
            process=await original(argv[0],'-I','-B','-c',code,*argv[4:],**kwargs)
            supervisors.append(process);return process
        return await original(*argv,**kwargs)
    monkeypatch.setattr(asyncio,'create_subprocess_exec',paused_identity_guard)
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-life-',dir='/private/tmp') as raw:
            task=asyncio.create_task(guarded_dummy(Path(raw)/'receipt',"import os,time;print(os.getpid(),flush=True);time.sleep(60)"))
            while not supervisors: await asyncio.sleep(.01)
            supervisor=supervisors[0]
            pid=int(await asyncio.wait_for(supervisor.stdout.readline(),3));birth=proxy_transport._process_metadata(pid)[0]
            try:
                os.kill(supervisor.pid,signal.SIGKILL)
                try: await asyncio.wait_for(task,3)
                except guard.GuardianLostError: pass
                else: raise AssertionError('lost pre-ack guardian was accepted')
                assert alive(pid,birth), 'negative fixture must expose the unproven child'
                assert not (Path(raw)/'receipt/guardian-final.json').exists()
            finally:
                if alive(pid,birth): os.kill(pid,signal.SIGTERM)
                assert wait_absent(pid,birth)
    asyncio.run(asyncio.wait_for(scenario(),10))
