"""Owned dummy backend extends existing fixture with restart-safe local history."""
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
import time
import tomllib
from urllib.parse import urlsplit

THREAD='01a097ff-f802-7b02-9159-4b2bd0552626'
SOCKET=Path(sys.argv[1]);MODE=sys.argv[2]


def frame(value):
    data=json.dumps(value,separators=(',',':')).encode()
    return (bytes((0x81,len(data))) if len(data)<126 else b'\x81\x7e'+struct.pack('>H',len(data)))+data


async def receive(reader):
    first,second=await reader.readexactly(2);length=second&127
    if length==126:length=struct.unpack('>H',await reader.readexactly(2))[0]
    elif length==127:raise ValueError('frame limit')
    if first&15==8:raise EOFError()
    if first&0x70 or not second&128:raise ValueError('wire flags')
    mask=await reader.readexactly(4);raw=await reader.readexactly(length)
    return json.loads(bytes(byte^mask[index%4] for index,byte in enumerate(raw)))


def generated(text):
    config=tomllib.loads((Path(os.environ['CODEX_HOME'])/'config.toml').read_text())
    url=urlsplit(config['model_providers']['synthetic']['base_url'])
    client=http.client.HTTPConnection(url.hostname,url.port,timeout=2)
    try:
        body={'model':'gpt-5.6-luna','stream':True,'input':[{'type':'message','role':'user','content':[{'type':'input_text','text':text}]}]}
        client.request('POST','/responses',json.dumps(body),{'Content-Type':'application/json'})
        response=client.getresponse();raw=response.read()
        if response.status!=200:raise ValueError('synthetic rejected')
        events=[json.loads(line[6:]) for line in raw.splitlines() if line.startswith(b'data: ')]
        return events[-2]['item']['content'][0]['text']
    finally:client.close()


def turn(index,status,items):
    return {'id':f'turn_{index}','status':status,'items':items,'itemsView':'full','error':None,
        'startedAt':None,'completedAt':None,'durationMs':None}


async def main():
    stop=asyncio.Event();clients=set();turns=json.loads((Path.cwd()/'dummy-history.json').read_text()) if (Path.cwd()/'dummy-history.json').exists() else [];calls=json.loads((Path.cwd()/'dummy-methods.json').read_text()) if (Path.cwd()/'dummy-methods.json').exists() else [];owner_writer=None;history_reads=0
    asyncio.get_running_loop().add_signal_handler(signal.SIGTERM,stop.set)
    async def broadcast(method,params):
        packet=frame({'method':method,'params':params,'emittedAtMs':int(time.time()*1000)})
        for client in list(clients):
            if MODE in ('owner-only-events','history-never-completes') and client is not owner_writer:continue
            if not client.is_closing():client.write(packet)
        await asyncio.gather(*(client.drain() for client in list(clients) if not client.is_closing()),return_exceptions=True)
    async def connection(reader,writer):
        nonlocal owner_writer,history_reads
        try:
            header=await reader.readuntil(b'\r\n\r\n')
            key=next(line.split(b':',1)[1].strip() for line in header.split(b'\r\n') if line.lower().startswith(b'sec-websocket-key:'))
            accept=base64.b64encode(hashlib.sha1(key+b'258EAFA5-E914-47DA-95CA-C5AB0DC85B11').digest())
            writer.write(b'HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: '+accept+b'\r\n\r\n');await writer.drain()
            clients.add(writer)
            while True:
                packet=await receive(reader);method=packet.get('method');params=packet.get('params',{})
                calls.append({'method':method,'request_id':packet.get('id')})
                (Path.cwd()/'dummy-methods.json').write_text(json.dumps(calls))
                if method=='initialize':
                    writer.write(frame({'method':'account/rateLimits/updated','params':{'rateLimits':{}},'emittedAtMs':1}))
                    result={'codexHome':os.environ['CODEX_HOME'],'userAgent':'controlled-python-backend'}
                elif method=='initialized':continue
                elif method=='thread/start':
                    owner_writer=writer
                    thread={'id':THREAD,'cwd':os.getcwd(),'ephemeral':False,'historyMode':'paginated','turns':[]}
                    writer.write(frame({'id':packet['id'],'result':{'thread':thread}}))
                    writer.write(frame({'method':'thread/started','params':{'thread':thread},'emittedAtMs':2}));await writer.drain();continue
                elif method=='thread/resume':
                    if params['threadId']!=THREAD or len(turns)!=1:raise ValueError('unowned resume')
                    owner_writer=writer
                    result={'thread':{'id':THREAD,'cwd':os.getcwd(),'ephemeral':False,'historyMode':'paginated','turns':turns}}
                elif method=='skills/list':result={'data':[]}
                elif method=='turn/start':
                    if params['threadId']!=THREAD:raise ValueError('wrong thread')
                    index=len(turns)+1
                    if index==2 and MODE=='drop-helper-receipt':return
                    if index==1:
                        text=params['input'][0]['text'];item={'type':'userMessage','id':'input_1','content':[dict(params['input'][0],text_elements=[])]}
                    else:
                        if params['input']!=[]:raise ValueError('not toolOutput')
                        text=params['toolOutput']['output'];item={'type':'functionCallOutput','id':'input_2',**params['toolOutput']}
                    current=turn(index,'inProgress',[])
                    writer.write(frame({'id':packet['id'],'result':{'turn':current}}));await writer.drain()
                    await broadcast('turn/started',{'threadId':THREAD,'turn':current})
                    await broadcast('item/started',{'threadId':THREAD,'turnId':current['id'],'item':item,'startedAtMs':1})
                    await broadcast('item/completed',{'threadId':THREAD,'turnId':current['id'],'item':item,'completedAtMs':2})
                    answer=await asyncio.to_thread(generated,text)
                    message={'type':'agentMessage','id':f'message_{index}','text':answer}
                    await broadcast('item/started',{'threadId':THREAD,'turnId':current['id'],'item':message,'startedAtMs':3})
                    await broadcast('item/agentMessage/delta',{'threadId':THREAD,'turnId':current['id'],'itemId':message['id'],'delta':answer})
                    await broadcast('item/completed',{'threadId':THREAD,'turnId':current['id'],'item':message,'completedAtMs':4})
                    completed=turn(index,'completed',[item,message]);turns.append(completed)
                    (Path.cwd()/'dummy-history.json').write_text(json.dumps(turns))
                    await broadcast('turn/completed',{'threadId':THREAD,'turn':completed});continue
                elif method=='thread/turns/list':
                    if params['threadId']!=THREAD or params['limit']!=2 or params['sortDirection']!='asc' or params['itemsView']!='full':raise ValueError('wrong pagination')
                    history=json.loads((Path.cwd()/'dummy-history.json').read_text())
                    index=0 if params['cursor'] is None else 1 if params['cursor']=='second' else -1
                    if index<0:raise ValueError('wrong cursor')
                    result={'data':history[index:index+1],'nextCursor':'second' if index==0 else None,'backwardsCursor':None}
                elif method=='thread/read':
                    # Fixed 0.154 thread_processor.rs:866-885 emits this notice
                    # before returning paginated includeTurns compatibility data.
                    notice={'method':'deprecationNotice','params':{'summary':'Full-history hydration is deprecated for paginated threads; omit `includeTurns` or set it to `false`, then page with `thread/turns/list` and `thread/items/list`.','details':None},'emittedAtMs':3}
                    if MODE=='wire-notice':notice['method']='private-canary-notification-do-not-export'
                    writer.write(frame(notice));await writer.drain()
                    history_reads+=1
                    if MODE=='history-never-completes':await asyncio.sleep(.2)
                    if params!={'threadId':THREAD,'includeTurns':True}:raise ValueError('wrong read')
                    history=json.loads((Path.cwd()/'dummy-history.json').read_text())
                    if len(history)>1 and (MODE=='history-never-completes' or MODE=='owner-only-events' and history_reads==1):
                        history[1]['status']='inProgress';history[1]['items']=[]
                    if MODE=='initial-ambiguous' and len(history)==1:history.append(dict(history[0],id='ambiguous_initial'))
                    if MODE=='bad-history':history[1]['items'][1]['text']='wrong-synthetic-result'
                    result={'thread':{'id':THREAD,'cwd':os.getcwd(),'historyMode':'paginated','ephemeral':False,'turns':history}}
                else:raise ValueError('unexpected method')
                writer.write(frame({'id':packet['id'],'result':result}));await writer.drain()
                if method=='thread/read' and MODE=='early-helper-eof':
                    until=asyncio.get_running_loop().time()+5
                    while not (Path.cwd()/'go-task/batch-ack-event_result.json').exists():
                        if asyncio.get_running_loop().time()>=until:raise ValueError('ACK wait timed out')
                        await asyncio.sleep(.001)
                    return
        except (asyncio.IncompleteReadError,ConnectionError,ValueError,StopIteration,EOFError):pass
        finally:
            clients.discard(writer);writer.close()
            try:await writer.wait_closed()
            except OSError:pass
    server=await asyncio.start_unix_server(connection,path=str(SOCKET));os.chmod(SOCKET,0o600)
    await stop.wait();server.close()
    for writer in list(clients):writer.close()
    await server.wait_closed()

asyncio.run(main())
