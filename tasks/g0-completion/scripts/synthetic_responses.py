"""Bounded local Responses fixture. It generates fixed text, never calls a model.

Only summaries are retained. The listener is bound at construction and remains
owned through prepare/run; there is no hostname or arbitrary port input.
"""
from __future__ import annotations
import hashlib
import json
import os
import select
import socket
import subprocess
import threading
import time

MAX_BODY = 1024 * 1024
MAX_HEADER = 16 * 1024
MAX_ATTEMPTS = 8
LOCAL_SECONDS = 10.0
RETURN_SECONDS = 120.0


def _birth(pid):
    result = subprocess.run(['/bin/ps','-p',str(pid),'-o','lstart='],capture_output=True,text=True,timeout=1)
    if result.returncode or not result.stdout.strip(): raise ValueError('listener owner birth unavailable')
    return result.stdout.strip()


def _sse(index, text):
    response_id = f'resp_g0_synthetic_{index}'
    item_id = f'msg_g0_synthetic_{index}'
    item = {'id':item_id,'type':'message','role':'assistant','content':[{'type':'output_text','text':text}]}
    response = {'id':response_id,'status':'completed','output':[item],'end_turn':True,
        'usage':{'input_tokens':0,'output_tokens':0,'total_tokens':0}}
    events = [
        {'type':'response.created','response':{'id':response_id,'status':'in_progress'}},
        {'type':'response.output_item.added','output_index':0,'item':dict(item,content=[])},
        {'type':'response.output_text.delta','item_id':item_id,'output_index':0,'content_index':0,'delta':text},
        {'type':'response.output_item.done','output_index':0,'item':item},
        {'type':'response.completed','response':response},
    ]
    return b''.join(b'data: '+json.dumps(event,separators=(',',':')).encode()+b'\n\n' for event in events)


def _strings(value, depth=0):
    if depth > 32: raise ValueError('input nesting exceeds bound')
    if isinstance(value,str): yield value
    elif isinstance(value,list):
        for item in value: yield from _strings(item,depth+1)
    elif isinstance(value,dict):
        for item in value.values(): yield from _strings(item,depth+1)


class SyntheticEndpoint:
    def __init__(self, *, window_seconds=RETURN_SECONDS):
        if not 0 < window_seconds <= RETURN_SECONDS: raise ValueError('invalid synthetic return window')
        self.listener = socket.socket(socket.AF_INET,socket.SOCK_STREAM)
        self.listener.bind(('127.0.0.1',0));self.listener.listen(4)
        self.port = self.listener.getsockname()[1]
        self.window_seconds = window_seconds
        self.owner_pid = os.getpid()
        self.owner_birth = _birth(self.owner_pid)
        self._identity = self.identity()
        self._thread = None;self._stop = threading.Event();self._lock = threading.Lock()
        self._started = False;self._deadline = None;self._accepted = 0;self._attempts = 0;self._records = []
        self._active = None

    def identity(self):
        if os.getpid() != self.owner_pid: raise ValueError('listener owner changed')
        if _birth(self.owner_pid) != self.owner_birth: raise ValueError('listener owner birth changed')
        if self.listener.family != socket.AF_INET or self.listener.type != socket.SOCK_STREAM:
            raise ValueError('listener type changed')
        if self.listener.getsockname() != ('127.0.0.1',self.port): raise ValueError('listener address changed')
        # macOS rejects SO_ACCEPTCONN with ENOPROTOOPT. This socket is created,
        # bound and listen()ed here, never supplied by an external caller.
        info = os.fstat(self.listener.fileno())
        return {'owner_pid':self.owner_pid,'owner_birth':self.owner_birth,'fd':self.listener.fileno(),'dev':info.st_dev,
            'ino':info.st_ino,'mode':info.st_mode,'host':'127.0.0.1','port':self.port}

    def verify(self):
        if self.identity() != self._identity: raise ValueError('listener identity changed')

    def start(self):
        self.verify()
        if self._started: raise ValueError('endpoint already started')
        self._started = True;self._deadline = time.monotonic()+self.window_seconds
        self._thread = threading.Thread(target=self._serve,daemon=True);self._thread.start()

    def _serve(self):
        while not self._stop.is_set() and time.monotonic() < self._deadline and self._attempts < MAX_ATTEMPTS:
            if not select.select([self.listener],[],[],min(.05,max(0,self._deadline-time.monotonic())))[0]: continue
            client,_ = self.listener.accept()
            with client:
                with self._lock:
                    if self._stop.is_set(): break
                    self._active = client
                try:
                    client.settimeout(min(LOCAL_SECONDS,max(.001,self._deadline-time.monotonic())))
                    self._handle(client)
                except (OSError,ValueError,RecursionError): pass
                finally:
                    with self._lock: self._active = None

    def _reply(self, client, status, body=b''):
        kind = b'text/event-stream' if status == 200 else b'application/json'
        response = b'HTTP/1.1 '+str(status).encode()+b' Fixture\r\nContent-Type: '+kind
        response += b'\r\nContent-Length: '+str(len(body)).encode()+b'\r\nConnection: close\r\n\r\n'+body
        client.sendall(response)

    def _handle(self, client):
        with self._lock:
            self._attempts += 1
            attempt = self._attempts
        if attempt > MAX_ATTEMPTS: return self._reply(client,429)
        if time.monotonic() >= self._deadline: return self._reply(client,408)
        data = bytearray()
        local_end = min(self._deadline,time.monotonic()+LOCAL_SECONDS)
        while b'\r\n\r\n' not in data:
            if time.monotonic() >= local_end: return self._reply(client,408)
            if len(data) >= MAX_HEADER: return self._reply(client,431)
            client.settimeout(max(.001,local_end-time.monotonic()))
            chunk = client.recv(1)
            if not chunk: return
            data.extend(chunk)
        try:
            lines = bytes(data[:-4]).decode('ascii').split('\r\n')
            method,path,version = lines[0].split(' ')
            headers = {}
            for line in lines[1:]:
                key,value = line.split(':',1);key=key.strip().lower()
                if key in headers: raise ValueError('duplicate header')
                headers[key] = value.strip()
            if method != 'POST' or path not in ('/responses','/v1/responses') or version != 'HTTP/1.1':
                return self._reply(client,404)
            if any(name in headers for name in ('authorization','api-key','x-api-key','cookie')):
                return self._reply(client,403)
            if 'transfer-encoding' in headers or headers.get('content-type','').split(';')[0] != 'application/json':
                return self._reply(client,400)
            length = int(headers['content-length'])
            if length < 0: raise ValueError('negative length')
        except (ValueError,KeyError,UnicodeError): return self._reply(client,400)
        if length > MAX_BODY: return self._reply(client,413)
        body = bytearray()
        while len(body) < length:
            if time.monotonic() >= local_end: return self._reply(client,408)
            client.settimeout(max(.001,local_end-time.monotonic()))
            chunk = client.recv(min(65536,length-len(body)))
            if not chunk: return
            body.extend(chunk)
        status = 400;stage = None
        try:
            request = json.loads(body)
            if not isinstance(request,dict) or request.get('stream') is not True or request.get('model') != 'gpt-5.6-luna':
                raise ValueError('request outside synthetic contract')
            if not isinstance(request.get('input'),list): raise ValueError('input must be array')
            markers = tuple(_strings(request['input']))
            with self._lock: stage = self._accepted
            expected = 'G0_SYNTHETIC_READY' if stage == 0 else 'G0_SYNTHETIC_TOOL_RESULT'
            status = 200 if stage < 2 and any(expected in item for item in markers) else 409
        except (ValueError,RecursionError): pass
        if time.monotonic() >= self._deadline: status = 408
        summary = {'attempt':attempt,'bytes':len(body),'sha256':hashlib.sha256(body).hexdigest(),'status':status,
            'stage':stage,'synthetic_only':True}
        with self._lock:
            self._records.append(summary)
            if status == 200: self._accepted += 1
        self._reply(client,status,_sse(stage+1,'READY' if stage == 0 else 'SYNTHETIC_COMPLETE') if status == 200 else b'')

    def snapshot(self):
        with self._lock:
            return {'version':1,'synthetic_only':True,'external_model_calls':0,'accepted_requests':self._accepted,
                'attempts':self._attempts,'requests':[dict(item) for item in self._records]}

    def close(self):
        self._stop.set()
        with self._lock:
            if self._active is not None:
                try: self._active.shutdown(socket.SHUT_RDWR)
                except OSError: pass
        if self._thread is not None:
            self._thread.join(LOCAL_SECONDS)
            if self._thread.is_alive(): raise TimeoutError('synthetic endpoint did not stop')
        self.listener.close()

    def __enter__(self): return self
    def __exit__(self,*_): self.close()
