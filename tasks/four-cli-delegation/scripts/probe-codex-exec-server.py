#!/usr/bin/env python3
"""Zero-model feasibility probe: standalone fixed executor under one OS sandbox."""
import base64
import hashlib
import json
import os
from pathlib import Path
import selectors
import signal
import socket
import subprocess
import sys
import tempfile
import time

binary = Path(sys.argv[1]).resolve(strict=True)
assert hashlib.sha256(binary.read_bytes()).hexdigest() == '4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc'
root = Path(tempfile.mkdtemp(prefix='bird-executor-probe-', dir='/private/tmp'))
root.chmod(0o700)
work, home, protected = [root / n for n in ('work', 'home', 'protected')]
for p in (work, home, protected): p.mkdir(mode=0o700)
(home / 'config.toml').write_text('')
(protected / 'sentinel').write_text('unchanged')
listener = socket.socket()
listener.bind(('127.0.0.1', 0))
listener.listen(2)
port = listener.getsockname()[1]
control = socket.create_connection(('127.0.0.1', port), timeout=2)
control.close()
policy = '\n'.join(['(version 1)', '(allow default)', '(deny file-write*)', '(deny network*)', '(allow file-write* (literal "/dev/null"))', f'(allow file-write* (subpath {json.dumps(str(work))}))', f'(deny file-read* (subpath {json.dumps(str(protected))}))'])
env = {'PATH': '/usr/bin:/bin', 'HOME': str(home), 'CODEX_HOME': str(home), 'TMPDIR': str(work)}
p = subprocess.Popen(['/usr/bin/sandbox-exec', '-p', policy, str(binary), 'exec-server', '--listen', 'stdio'], cwd=work, env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
selector = selectors.DefaultSelector()
selector.register(p.stdout, selectors.EVENT_READ)
pending, buffer, sequence = [], b'', 0

def receive():
    global buffer
    deadline = time.monotonic() + 10
    while b'\n' not in buffer:
        if time.monotonic() >= deadline or not selector.select(max(0, deadline-time.monotonic())): raise TimeoutError('executor response')
        chunk = os.read(p.stdout.fileno(), 65536)
        if not chunk: raise RuntimeError('executor exited')
        buffer += chunk
        if len(buffer) > 1024*1024: raise RuntimeError('oversized response')
    line, buffer = buffer.split(b'\n', 1)
    return json.loads(line)

def send(method, params, request=True):
    global sequence
    sequence += 1
    msg = dict(method=method, params=params)
    if request: msg['id'] = sequence
    p.stdin.write((json.dumps(msg)+'\n').encode()); p.stdin.flush()
    if not request: return None
    while True:
        r=receive()
        if r.get('id') == sequence: return r
        pending.append(r)

results = {'model_calls': 0, 'root': str(root)}
try:
    init = send('initialize', {'clientName':'agent-bird-probe'})
    assert 'result' in init, init
    send('initialized', {}, False)
    path = (work/'calc.py').as_uri()
    data = base64.b64encode(b'def add(a,b):\n    return a+b\n').decode()
    r = send('fs/writeFile', {'path':path,'dataBase64':data,'sandbox':None})
    results['write_response'] = r
    assert 'result' in r, r
    r = send('fs/readFile', {'path':path,'sandbox':None})
    assert r.get('result',{}).get('dataBase64') == data, r
    results['workspace_write_read'] = True
    r = send('fs/writeFile', {'path':(protected/'sentinel').as_uri(),'dataBase64':data,'sandbox':None})
    assert 'error' in r, r
    r = send('fs/readFile', {'path':(protected/'sentinel').as_uri(),'sandbox':None})
    assert 'error' in r, r
    results['protected_write_read_denied'] = True
    code = f'import socket,sys\ntry:\n socket.create_connection(("127.0.0.1",{port}),timeout=1)\nexcept OSError:\n sys.exit(0)\nsys.exit(9)'
    r = send('process/start', {'processId':'network-probe','argv':['/usr/bin/python3','-c',code], 'cwd':work.as_uri(),'env':{},'tty':False,'arg0':None,'sandbox':None})
    assert 'result' in r, r
    deadline = time.monotonic()+10
    while time.monotonic() < deadline:
        r = pending.pop(0) if pending else receive()
        if r.get('method') == 'process/exited':
            results['network_exit'] = r['params']
            assert r['params'].get('exitCode') == 0, r
            break
    else: raise TimeoutError('network command exit')
    assert (protected/'sentinel').read_text()=='unchanged'
    results['status']='pass'
finally:
    os.killpg(p.pid, signal.SIGTERM)
    try: p.wait(timeout=2)
    except subprocess.TimeoutExpired:
        os.killpg(p.pid, signal.SIGKILL);p.wait(timeout=2)
    selector.close();listener.close()
    results['executor_exit']=p.returncode
    out=root/'result.json';out.write_text(json.dumps(results,indent=2));out.chmod(0o600)
    print(json.dumps(results))
