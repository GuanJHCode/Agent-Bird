"""Real controlling PTY + default-socket WS, only fixed commands for tests."""
import asyncio,json,os,sys,tty
from pathlib import Path
ROOT=Path(__file__).resolve().parents[4]
sys.path[:0]=[str(ROOT/'tasks/g0-completion/scripts'),str(ROOT/'tasks/g0-proxy-continuation/cold-start/scripts')]
from owner_helper import _open_websocket
from owner_helper import _websocket_frame,_read_server_text
class OwnerWire:
    def __init__(self,reader,writer):self.reader=reader;self.writer=writer
    async def send(self,text):self.writer.write(_websocket_frame(text.encode()));await self.writer.drain()
    async def recv(self):return (await _read_server_text(self.reader)).decode()
    async def close(self):self.writer.close();await self.writer.wait_closed()


async def main():
    tty.setraw(sys.stdin.fileno());pending={};next_id=0;completed={}
    public=str(Path(os.environ['CODEX_HOME'])/'app-server-control/app-server-control.sock')
    # Ordinary Codex probes the default socket with no application bytes.
    probe_reader,probe_writer=await asyncio.open_unix_connection(public)
    await asyncio.sleep(.05)
    probe_writer.write_eof();assert await probe_reader.read()==b''
    probe_writer.close();await probe_writer.wait_closed()
    reader,writer=await _open_websocket(public)
    wire=OwnerWire(reader,writer)
    async def receive():
        while True:
            packet=json.loads(await wire.recv())
            if packet.get('id') in pending:pending.pop(packet['id']).set_result(packet['result'])
            if packet.get('method')=='turn/completed':
                turn=packet['params']['turn'];print('\r\n'+turn['items'][-1]['text']+'\r\n',flush=True)
                completed.setdefault(turn['id'],asyncio.Event()).set()
    receiver=asyncio.create_task(receive())
    async def call(method,params):
        nonlocal next_id
        next_id+=1;future=asyncio.get_running_loop().create_future();pending[next_id]=future
        await wire.send(json.dumps({'id':next_id,'method':method,'params':params}));return await future
    await call('initialize',{'clientInfo':{'name':'owned-dummy-tui','version':'1'},'capabilities':{'experimentalApi':True}})
    await wire.send(json.dumps({'method':'initialized'}))
    if sys.argv[1:]:
        assert sys.argv[1]=='resume' and len(sys.argv)==3
        thread=(await call('thread/resume',{'threadId':sys.argv[2]}))['thread']
        # Actual 0.154 resume sends two post-response skills requests before
        # either response arrives (startup refresh and numeric request).
        await asyncio.gather(call('skills/list',{}),call('skills/list',{}))
    else:thread=(await call('thread/start',{'cwd':os.getcwd(),'ephemeral':False,'historyMode':'legacy'}))['thread']
    stdin=asyncio.StreamReader();loop=asyncio.get_running_loop()
    def receive_input():
        data=os.read(sys.stdin.fileno(),4096)
        if data:stdin.feed_data(data)
        else:stdin.feed_eof()
    loop.add_reader(sys.stdin.fileno(),receive_input)
    try:
        while True:
            line=(await stdin.readuntil(b'\r')).replace(b'\x1b[200~',b'').replace(b'\x1b[201~',b'').strip().decode()
            if line=='/quit':return
            if line=='/status':print('\r\nSession: '+thread['id']+'\r\n',flush=True)
            elif line=='G0_SYNTHETIC_READY':
                turn=(await call('turn/start',{'threadId':thread['id'],'input':[{'type':'text','text':line}]}))['turn']
                await completed.setdefault(turn['id'],asyncio.Event()).wait()
            else:raise ValueError('unplanned PTY input')
    finally:
        loop.remove_reader(sys.stdin.fileno());receiver.cancel();await asyncio.gather(receiver,return_exceptions=True)
        await wire.close()
asyncio.run(main())
