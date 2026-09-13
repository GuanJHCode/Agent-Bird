"""Test-only Python app-server stand-in; real UDS/WS and synthetic HTTP."""
import asyncio
import base64
import hashlib
import http.client
import json
import os
from pathlib import Path
import signal
import struct
import sys
import tomllib
from urllib.parse import urlsplit

SOCKET=Path(sys.argv[1]);MODE=sys.argv[2];THREAD='synthetic-thread-1'


def frame(value):
    data=json.dumps(value,separators=(',',':')).encode()
    if len(data)<126:return bytes([0x81,len(data)])+data
    return b'\x81\x7e'+struct.pack('>H',len(data))+data


async def receive(reader):
    first,second=await reader.readexactly(2)
    length=second&127
    if length==126:length=struct.unpack('>H',await reader.readexactly(2))[0]
    elif length==127:raise ValueError('fixture frame bound')
    mask=await reader.readexactly(4) if second&128 else None
    data=await reader.readexactly(length)
    if first&15==8:raise EOFError()
    if mask:data=bytes(byte^mask[index%4] for index,byte in enumerate(data))
    return json.loads(data)


def generated(marker):
    config=tomllib.loads((Path(os.environ['CODEX_HOME'])/'config.toml').read_text())
    url=urlsplit(config['model_providers']['synthetic']['base_url'])
    client=http.client.HTTPConnection(url.hostname,url.port,timeout=2)
    try:
        data=json.dumps({'model':'gpt-5.6-luna','stream':True,'input':[{'type':'message','role':'user','content':[{'type':'input_text','text':marker}]}]})
        client.request('POST','/responses',data,{'Content-Type':'application/json'})
        response=client.getresponse();raw=response.read()
        if response.status!=200:raise ValueError('synthetic response failed')
        events=[json.loads(line[6:]) for line in raw.splitlines() if line.startswith(b'data: ')]
        return events[-2]['item']['content'][0]['text']
    finally:client.close()


async def main():
    stop=asyncio.Event();clients=set();turns=[]
    asyncio.get_running_loop().add_signal_handler(signal.SIGTERM,stop.set)
    async def connection(reader,writer):
        clients.add(writer)
        try:
            header=await reader.readuntil(b'\r\n\r\n')
            key=next(line.split(b':',1)[1].strip() for line in header.split(b'\r\n') if line.lower().startswith(b'sec-websocket-key:'))
            accept=base64.b64encode(hashlib.sha1(key+b'258EAFA5-E914-47DA-95CA-C5AB0DC85B11').digest())
            writer.write(b'HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: '+accept+b'\r\n\r\n');await writer.drain()
            while True:
                packet=await receive(reader);method=packet.get('method');params=packet.get('params',{})
                if method=='initialized':continue
                if method=='initialize':
                    if MODE=='unlisted-notification':
                        writer.write(frame({'method':'untrusted-private-marker-notification','params':{'text':'untrusted-private-marker-body'}}));await writer.drain()
                    if MODE=='server-request':
                        writer.write(frame({'id':'untrusted-private-marker-id','method':'untrusted-private-marker-method','params':{'text':'untrusted-private-marker-body'}}));await writer.drain()
                    result={'codexHome':os.environ['CODEX_HOME']+('-wrong' if MODE=='wrong-home' else ''),'userAgent':'synthetic/0.154.0'}
                elif method=='thread/start':
                    if MODE.startswith('remote-status'):
                        params={'status':'disabled','serverName':'private-remote-server','installationId':'private-remote-installation','environmentId':None}
                        if MODE=='remote-status-invalid-status':params['status']='enabled'
                        if MODE=='remote-status-missing-name':params.pop('serverName')
                        if MODE=='remote-status-bad-environment':params['environmentId']={'token':'private-remote-token'}
                        notice={'method':'remoteControl/status/changed','params':params,'emittedAtMs':1234}
                        if MODE=='remote-status-server-request':notice['id']=999
                        writer.write(frame(notice));await writer.drain()
                    if MODE=='official-unlisted-notification':
                        writer.write(frame({'method':'thread/goal/updated','params':{'threadId':THREAD,'goal':None},'emittedAtMs':1234}));await writer.drain()
                    result={'thread':{'id':THREAD,'cwd':os.getcwd(),'ephemeral':False,'historyMode':'legacy','turns':[]}}
                elif method=='turn/start':
                    if params['threadId']!=THREAD:raise ValueError('wrong thread')
                    index=len(turns)+1
                    if index==2 and MODE=='lost-second-receipt':return
                    marker=params['input'][0]['text'] if index==1 else params['toolOutput']['output']
                    if index==2 and params['input']!=[]:raise ValueError('toolOutput must not add user input')
                    turn_id=f'synthetic-turn-{index}'
                    writer.write(frame({'id':packet['id'],'result':{'turn':{'id':turn_id,'status':'inProgress','items':[],'error':None}}}));await writer.drain()
                    text=await asyncio.to_thread(generated,marker)
                    item=({'type':'userMessage','id':'input-1','content':params['input']} if index==1 else
                        {'type':'functionCallOutput','id':'input-2',**params['toolOutput']})
                    turn={'id':turn_id,'status':'completed','error':None,'items':[item,{'type':'agentMessage','id':f'message-{index}','text':text}]}
                    turns.append(turn)
                    (Path.cwd()/'dummy-history.json').write_text(json.dumps(turns))
                    writer.write(frame({'method':'turn/completed','params':{'threadId':THREAD,'turn':turn}}));await writer.drain();continue
                elif method=='thread/read':
                    if params!={'threadId':THREAD,'includeTurns':True}:raise ValueError('full history required')
                    stored=json.loads((Path.cwd()/'dummy-history.json').read_text())
                    if MODE=='bad-history':stored[1]['items'][1]['text']='wrong-synthetic-text'
                    result={'thread':{'id':THREAD,'cwd':os.getcwd(),'ephemeral':False,'historyMode':'legacy','turns':stored}}
                else:raise ValueError('unexpected method')
                writer.write(frame({'id':packet['id'],'result':result}));await writer.drain()
        except (asyncio.IncompleteReadError,ConnectionError,ValueError,StopIteration,EOFError):pass
        finally:clients.discard(writer);writer.close()
    server=await asyncio.start_unix_server(connection,path=str(SOCKET));os.chmod(SOCKET,0o600)
    await stop.wait();server.close()
    for writer in list(clients):writer.close()
    await server.wait_closed()

asyncio.run(main())
