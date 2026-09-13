"""Real dummy controller death; no Codex/model/launchctl or real credentials."""
from contextlib import ExitStack
import json
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
            header=json.loads(frontend.stdout.readline())
            backend_pid=header['pid']; backend_birth=proxy_transport._process_metadata(backend_pid)[0]
            assert backend_pid!=service.pid and backend_birth
            service.kill(); service.wait(timeout=2)
            assert service.returncode==-signal.SIGKILL
            assert wait_absent(backend_pid,backend_birth), 'service SIGKILL left its registered backend alive'
            assert public.exists() and (public.stat().st_dev,public.stat().st_ino)==public_identity
        finally:
            if service is not None and service.poll() is None: service.terminate(); service.wait(timeout=3)
            if frontend.poll() is None: frontend.terminate(); frontend.wait(timeout=2)
            if backend_pid and backend_birth and alive(backend_pid,backend_birth):
                os.kill(backend_pid,signal.SIGTERM)
                assert wait_absent(backend_pid,backend_birth), 'dummy fixture cleanup failed'
            listener.close()
