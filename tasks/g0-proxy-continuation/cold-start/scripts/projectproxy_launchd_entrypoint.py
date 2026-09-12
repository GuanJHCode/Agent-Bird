#!/usr/bin/env python3
"""Socket-activated per-peer proxy. Registration is a separate reviewed transaction."""
from __future__ import annotations

import argparse
import asyncio
import hashlib
import json
import os
from pathlib import Path
import socket
import signal
import sys

# -I ignores environment/module injection; import only the pinned script directory.
sys.path.insert(0,str(Path(__file__).resolve().parent))
os.environ.clear()
os.environ.update({'PATH':'/usr/bin:/bin','LANG':'C'})

from activation_service import ActivationService
from launch_activation import activate_listener


async def run(args):
    service=ActivationService.from_manifest(args.manifest)
    stopped=asyncio.Event()
    loop=asyncio.get_running_loop()
    received=[]
    def request_stop(signum):
        received.append(signum); stopped.set()
    for signum in (signal.SIGTERM,signal.SIGINT):
        loop.add_signal_handler(signum,request_stop,signum)
    serving=None; stop_wait=None
    stage='activate_listener'
    try:
        if args.listener_fd is None:
            listener=activate_listener('Listener')
        else:
            # Offline parent-owned FD path; the LaunchAgent argv never supplies it.
            listener=socket.socket(fileno=os.dup(args.listener_fd))
            os.set_inheritable(listener.fileno(),False)
        stage='serve'
        serving=asyncio.create_task(service.serve_until_idle(listener))
        stop_wait=asyncio.create_task(stopped.wait())
        await asyncio.wait((serving,stop_wait),return_when=asyncio.FIRST_COMPLETED)
        if serving.done(): await serving
    except BaseException as exc:
        service.record_startup_failure(stage,exc)
        raise
    finally:
        service.stop_signals=received
        await asyncio.wait_for(service.close(),10.0)
        for task in (serving,stop_wait):
            if task is not None and not task.done(): task.cancel()
        await asyncio.gather(*(task for task in (serving,stop_wait) if task is not None),return_exceptions=True)
        for signum in (signal.SIGTERM,signal.SIGINT): loop.remove_signal_handler(signum)
    complete=all(record.get('process_stopped') and record.get('state')!='failed'
                 and not record.get('cleanup_failure') for record in service.backend_records)
    summary={'activation_id':service.activation_id,'backend_count':len(service.backend_records),
        'all_backends_stopped':all(record.get('process_stopped') for record in service.backend_records),
        'status':'closed' if complete else 'failed','public_socket_owned':False}
    print(json.dumps(summary,sort_keys=True))
    return 0 if complete else 1


def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--manifest',type=Path,required=True)
    parser.add_argument('--listener-fd',type=int,help='offline inherited-FD test only; omitted by LaunchAgent')
    args=parser.parse_args(argv)
    try:
        return asyncio.run(run(args))
    except Exception as exc:
        print(json.dumps({'status':'failed','failure_type':type(exc).__name__,'errno':exc.errno if isinstance(exc,OSError) else None,
            'message_sha256':hashlib.sha256((type(exc).__name__+':'+str(exc)).encode()).hexdigest()}))
        return 1


if __name__=='__main__':
    raise SystemExit(main())
