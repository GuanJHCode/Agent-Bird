"""Dummy ordinary CLI behind the real probe's pre-exec gate and controlling PTY.

Only the real service writes readiness/final receipts. This process has no
supervisor receipt path and does not create any files.
"""
import json
import os
import socket
import struct
import sys
import tty
import termios

MODE=sys.argv[1]


def send(sock,value):
    data=json.dumps(value,separators=(',',':')).encode(); mask=b'\x01\x02\x03\x04'
    prefix=bytes([0x81,0x80|len(data)]) if len(data)<126 else b'\x81\xfe'+struct.pack('>H',len(data))
    sock.sendall(prefix+mask+bytes(byte^mask[index%4] for index,byte in enumerate(data)))


def exact(sock,size):
    data=b''
    while len(data)<size:
        chunk=sock.recv(size-len(data))
        if not chunk: raise EOFError()
        data+=chunk
    return data


def receive(sock):
    first,second=exact(sock,2); size=second&127
    if size==126: size=struct.unpack('>H',exact(sock,2))[0]
    if size==127: raise ValueError('fixture message too large')
    return json.loads(exact(sock,size))


def main():
    tty.setraw(0,termios.TCSANOW)
    os.write(1,b'\x1b[6n')
    terminal=b''
    # Deliberately require the real probe to drain PTY during readiness.
    while b'\x1b[1;1R' not in terminal:
        terminal+=os.read(0,4096)
    path=os.path.join(os.environ['CODEX_HOME'],'app-server-control','app-server-control.sock')
    probe=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM); probe.connect(path); probe.close()
    sock=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM); sock.connect(path)
    sock.sendall(b'GET / HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n')
    header=b''
    while not header.endswith(b'\r\n\r\n'): header+=exact(sock,1)
    send(sock,{'id':1,'method':'initialize','params':{'clientInfo':{'name':'dummy-tui','version':'0.154.0'}}})
    assert receive(sock)['id']==1
    send(sock,{'method':'initialized'})
    send(sock,{'id':2,'method':'thread/start','params':{'cwd':os.getcwd()}})
    started=receive(sock); notification=receive(sock)
    thread=started['result']['thread']['id']
    assert notification['params']['thread']['id']==thread
    os.write(1,b'\r\nDummy CLI ready\r\n')
    terminal=b''
    while True:
        terminal+=os.read(0,4096)
        while b'\r' in terminal:
            command,terminal=terminal.split(b'\r',1)
            command=command.replace(b'\x1b[200~',b'').replace(b'\x1b[201~',b'')
            if command==b'/status':
                os.write(1,f'\r\nSession: {thread}\r\n'.encode())
                if MODE=='final-fail':
                    send(sock,{'id':3,'method':'turn/start','params':{'input':[]}})
                    assert receive(sock)['id']==3
            elif command==b'/quit':
                sock.close(); return 0
            elif command:
                return 64

raise SystemExit(main())
