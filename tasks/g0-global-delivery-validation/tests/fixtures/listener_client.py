"""Client-only provenance simulation; service-side kernel identities stay real."""
import asyncio
from dataclasses import replace
from pathlib import Path
import sys
ROOT=Path(__file__).resolve().parents[4]
sys.path.insert(0,str(ROOT/'tasks/g0-completion/scripts'))
import native_delivery_client as client
original=client.proxy_transport.peer_identity
def listener_identity(sock):
    peer=original(sock)
    return replace(peer,pid=1,uid=0,birth='controlled-listener',executable='/controlled/listener',
        pid_available=True,uid_available=True,birth_available=True,executable_available=True)
client.proxy_transport.peer_identity=listener_identity
raise SystemExit(asyncio.run(client.main()))
