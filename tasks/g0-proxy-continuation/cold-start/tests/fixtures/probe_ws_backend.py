"""Dummy app-server protocol peer; never imports or starts Codex/model code."""
import asyncio
import base64
import hashlib
import json
import os
import signal
import struct
import sys
import uuid

THREAD=str(uuid.uuid4())
MODE=sys.argv[2]
STOP=None


def frame(value):
    data=json.dumps(value,separators=(',',':')).encode()
    header=bytes([0x81,len(data)]) if len(data)<126 else b'\x81\x7e'+struct.pack('>H',len(data))
    return header+data


async def receive(reader):
    first,second=await reader.readexactly(2)
    length=second&127
    if length==126: length=struct.unpack('>H',await reader.readexactly(2))[0]
    if length==127: raise ValueError('fixture frame too large')
    mask=await reader.readexactly(4) if second&128 else None
    data=await reader.readexactly(length)
    if mask: data=bytes(byte^mask[index%4] for index,byte in enumerate(data))
    return json.loads(data)


async def main():
    stop=asyncio.Event(); clients=set()
    asyncio.get_running_loop().add_signal_handler(signal.SIGTERM,stop.set)
    async def connection(reader,writer):
        clients.add(writer)
        try:
            header=await reader.readuntil(b'\r\n\r\n')
            key=next(line.split(b':',1)[1].strip() for line in header.split(b'\r\n') if line.lower().startswith(b'sec-websocket-key:'))
            accept=base64.b64encode(hashlib.sha1(key+b'258EAFA5-E914-47DA-95CA-C5AB0DC85B11').digest())
            writer.write(b'HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: '+accept+b'\r\n\r\n')
            await writer.drain()
            while True:
                message=await receive(reader); method=message.get('method')
                if method=='initialize':
                    home=os.environ['CODEX_HOME']+('-wrong' if MODE=='readiness-fail' else '')
                    writer.write(frame({'id':message['id'],'result':{'codexHome':home,'userAgent':'dummy/0.154.0'}}))
                elif method=='thread/start':
                    thread={'id':THREAD,'cwd':os.getcwd(),'ephemeral':True,'turns':[]}
                    writer.write(frame({'id':message['id'],'result':{'thread':thread}}))
                    writer.write(frame({'method':'thread/started','params':{'thread':thread}}))
                elif 'id' in message:
                    writer.write(frame({'id':message['id'],'result':{}}))
                await writer.drain()
        except (asyncio.IncompleteReadError,ConnectionError,ValueError,StopIteration):
            pass
        finally:
            clients.discard(writer); writer.close()
    server=await asyncio.start_unix_server(connection,path=sys.argv[1])
    os.chmod(sys.argv[1],0o600)
    await stop.wait()
    server.close()
    for writer in list(clients): writer.close()
    await server.wait_closed()

asyncio.run(main())
