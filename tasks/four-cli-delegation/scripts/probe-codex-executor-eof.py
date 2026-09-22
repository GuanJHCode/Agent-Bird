#!/usr/bin/env python3
"""No model: check pinned executor EOF cleanup of a tool and grandchild."""
import hashlib
import json
import os
from pathlib import Path
import selectors
import signal
import subprocess
import sys
import tempfile
import time

binary = Path(sys.argv[1]).resolve(strict=True)
assert hashlib.sha256(binary.read_bytes()).hexdigest() == '4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc'
root = Path(tempfile.mkdtemp(prefix='bird-executor-eof-', dir='/private/tmp'))
crash = len(sys.argv) > 2 and sys.argv[2] == '--host-crash'
if crash:
    host = os.fork()
    if host:
        # The child owns every executor pipe. SIGKILL closes them without cleanup.
        deadline = time.monotonic()+30
        status = None
        while time.monotonic()<deadline:
            pid, value = os.waitpid(host, os.WNOHANG)
            if pid:
                status=value
                break
            time.sleep(.02)
        if status is None:
            os.kill(host,signal.SIGKILL)
            _,status=os.waitpid(host,0)
        before=json.loads((root/'crash-before.json').read_text())
        deadline=time.monotonic()+22
        while time.monotonic()<deadline:
            alive=[]
            for pid in before['pids']:
                try: os.kill(pid,0);alive.append(pid)
                except ProcessLookupError: pass
            if not alive: break
            time.sleep(.02)
        result={'model_calls':0,'root':str(root),'host_killed':os.WIFSIGNALED(status) and os.WTERMSIG(status)==signal.SIGKILL,'before':before['before'],'surviving_pids':alive,'cleanup_seconds':round(22-(deadline-time.monotonic()),3)}
        result['status']='pass' if result['host_killed'] and not alive and result['cleanup_seconds']<5 else 'fail'
        out=root/'result.json';out.write_text(json.dumps(result,indent=2));out.chmod(0o600)
        print(json.dumps(result))
        sys.exit(0 if result['status']=='pass' else 1)
policy = '(version 1)(allow default)(deny network*)(deny file-write*)(allow file-write* (literal "/dev/null") (subpath '+json.dumps(str(root))+'))'
p = subprocess.Popen(['/usr/bin/sandbox-exec', '-p', policy, str(binary), 'exec-server', '--listen', 'stdio'], cwd=root, env={'PATH':'/usr/bin:/bin','HOME':str(root),'CODEX_HOME':str(root),'TMPDIR':str(root)}, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, start_new_session=True)
sel = selectors.DefaultSelector()
sel.register(p.stdout, selectors.EVENT_READ)
buffer = b''
def request(value, expected=None):
    global buffer
    p.stdin.write(json.dumps(value).encode()+b'\n');p.stdin.flush()
    if expected is None: return
    deadline=time.monotonic()+8
    while time.monotonic()<deadline:
        if b'\n' not in buffer:
            assert sel.select(max(0,deadline-time.monotonic())), 'response timeout'
            chunk=os.read(p.stdout.fileno(),65536)
            assert chunk, 'executor exited'
            buffer+=chunk
        while b'\n' in buffer:
            raw,buffer=buffer.split(b'\n',1)
            value=json.loads(raw)
            if value.get('id')==expected:
                assert 'result' in value, value
                return value['result']
    raise TimeoutError('response')

result={'model_calls':0,'root':str(root)}
pids=[]
try:
    request({'id':1,'method':'initialize','params':{'clientName':'bird-eof-probe'}},1)
    request({'method':'initialized','params':{}})
    code='import os,subprocess,time,json\np=subprocess.Popen(["/bin/sleep","20"])\nopen("pids.json","w").write(json.dumps([os.getpid(),p.pid]))\ntime.sleep(20)'
    request({'id':2,'method':'process/start','params':{'processId':'owned-probe','argv':['/usr/bin/python3','-c',code],'cwd':root.as_uri(),'env':{},'tty':False,'sandbox':None}},2)
    deadline=time.monotonic()+5
    while not (root/'pids.json').exists() and time.monotonic()<deadline: time.sleep(.02)
    pids=json.loads((root/'pids.json').read_text())
    # Capture live identity evidence before triggering the transport failure.
    result['before']=subprocess.check_output(['/bin/ps','-o','pid=,ppid=,pgid=,lstart=','-p',','.join(map(str,[p.pid]+pids))],text=True).strip().splitlines()
    if crash:
        (root/'crash-before.json').write_text(json.dumps({'before':result['before'],'pids':[p.pid]+pids}))
        os.kill(os.getpid(),signal.SIGKILL)
    p.stdin.close()
    try:
        p.wait(timeout=5)
        result['executor_exit']=p.returncode
    except subprocess.TimeoutExpired: result['executor_exit']='timeout'
    alive=[]
    for pid in pids:
        try: os.kill(pid,0);alive.append(pid)
        except ProcessLookupError: pass
    result['surviving_tool_pids']=alive
    result['status']='pass' if p.poll()==0 and not alive else 'fail'
finally:
    if p.poll() is None:
        os.killpg(p.pid,signal.SIGKILL);p.wait(timeout=3)
    # The bounded probe descendants self-exit at 20s; do not signal from stale PID files.
    deadline=time.monotonic()+22
    while time.monotonic()<deadline:
        alive=[]
        for pid in pids:
            try: os.kill(pid,0);alive.append(pid)
            except ProcessLookupError: pass
        if not alive: break
        time.sleep(.05)
    result['cleanup_confirmed']=not alive if pids else True
    sel.close()
    out=root/'result.json';out.write_text(json.dumps(result,indent=2));out.chmod(0o600)
    print(json.dumps(result))
    assert result.get('status')=='pass' and result['cleanup_confirmed'], 'native EOF did not clean the tool tree'
