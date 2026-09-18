"""Offline stream backend; exposes only synthetic home/PID metadata."""
import asyncio
import json
import os
import signal
import sys

async def main():
    if '--fail' in sys.argv:
        raise SystemExit(7)
    stop = asyncio.Event()
    loop = asyncio.get_running_loop()
    loop.add_signal_handler(signal.SIGTERM, stop.set)
    if '--wait-before-listen' in sys.argv:
        await stop.wait(); return
    clients = set()
    async def echo(reader, writer):
        clients.add(writer)
        try:
            if await reader.read(1)!=b'G':return
            writer.write(json.dumps({'pid':os.getpid(), 'home':os.environ.get('HOME'),
                'codex_home':os.environ.get('CODEX_HOME'), 'cwd':os.getcwd(),
                'auth_env_present':any(name in os.environ for name in ('OPENAI_API_KEY','CODEX_API_KEY','AWS_SECRET_ACCESS_KEY'))}).encode()+b'\n')
            await writer.drain()
            while data := await reader.read(65536):
                writer.write(data); await writer.drain()
        finally:
            clients.discard(writer); writer.close()
    server = await asyncio.start_unix_server(echo, path=sys.argv[1])
    os.chmod(sys.argv[1], 0o600)
    await stop.wait()
    server.close()
    for writer in list(clients): writer.close()
    await server.wait_closed()

asyncio.run(main())
