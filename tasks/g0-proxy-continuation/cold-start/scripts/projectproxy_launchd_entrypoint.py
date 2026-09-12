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
import sys

# -I ignores environment/module injection; import only the pinned script directory.
sys.path.insert(0,str(Path(__file__).resolve().parent))
os.environ.clear()
os.environ.update({'PATH':'/usr/bin:/bin','LANG':'C'})

from activation_service import ActivationService
from launch_activation import activate_listener


async def run(args):
    service=ActivationService.from_manifest(args.manifest)
    try:
        if args.listener_fd is None:
            listener=activate_listener('Listener')
        else:
            # Offline parent-owned FD path; the LaunchAgent argv never supplies it.
            listener=socket.socket(fileno=os.dup(args.listener_fd))
            os.set_inheritable(listener.fileno(),False)
        await service.serve_until_idle(listener)
    finally:
        await service.close()
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
        print(json.dumps({'status':'failed','failure_type':type(exc).__name__,
            'message_sha256':hashlib.sha256((type(exc).__name__+':'+str(exc)).encode()).hexdigest()}))
        return 1


if __name__=='__main__':
    raise SystemExit(main())
