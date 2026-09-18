"""A controller-gated test client; never imports Codex or auth libraries."""
import base64
import json
import socket
import sys

print('ready', flush=True)
client = None
for line in sys.stdin:
    request = json.loads(line)
    if request['action'] == 'probe':
        probe=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM); probe.connect(sys.argv[1])
        print('probed',flush=True)
    elif request['action'] == 'close_probe':
        probe.close(); print('probe-closed',flush=True)
    elif request['action'] == 'disconnect':
        stream.close(); client.close(); client=None
        print('disconnected',flush=True)
    elif request['action'] == 'connect':
        client = socket.socket(socket.AF_UNIX,socket.SOCK_STREAM)
        client.connect(sys.argv[1])
        stream = client.makefile('rb')
        try:
            client.sendall(b'G')  # Test protocol is client-first, like native WS.
            header = stream.readline()
        except OSError:header=b''
        print(header.decode().strip() or 'closed',flush=True)
    elif request['action'] == 'send':
        data=base64.b64decode(request['data']); client.sendall(data)
        reply=stream.read(len(data))
        print(base64.b64encode(reply).decode(),flush=True)
    else:
        if client is not None:
            stream.close(); client.close()
        break
