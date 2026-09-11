"""One fixed sd-qual-01 qualification controller; not a product launcher."""
import datetime as dt
import json
import os
from pathlib import Path
import re
import sqlite3
import native_sd_collect as c
from native_sd_collect import require_live_processes, same_process


def native_ready(root,turn,since,through,now=None):
    if not c.valid_id(root) or not c.valid_id(turn): raise ValueError('exact root and turn required')
    lo,hi=c.window(since,through,now or dt.datetime.now(dt.timezone.utc))
    db=sqlite3.connect('file:'+str(c.CODEX_HOME/'state_5.sqlite')+'?mode=ro',uri=True)
    try: row=db.execute('SELECT id,source,cwd,cli_version,rollout_path FROM threads WHERE id=?',(root,)).fetchone()
    finally: db.close()
    if row is None: return None  # First-turn registration can still be pending.
    c.thread_fields(row[:4],root)
    path=Path(row[4])
    if path.is_symlink() or (c.CODEX_HOME/'sessions').resolve() not in path.resolve().parents:
        raise ValueError('unexpected native rollout path')
    if not path.exists(): return None
    if not path.is_file() or path.stat().st_size>16*1024*1024:
        raise ValueError('unsupported bounded native rollout')
    found=[]
    with path.open() as stream:
        for line in stream:
            # Decode only the needed event class; no response/reasoning body is retained.
            if '"event_msg"' not in line: continue
            try: event=json.loads(line)
            except json.JSONDecodeError: continue  # A concurrent last append may be incomplete.
            if event.get('type')!='event_msg': continue
            payload=event.get('payload',{})
            if (type(payload) is not dict or payload.get('type')!='task_complete'
                    or payload.get('turn_id')!=turn or payload.get('last_agent_message')!='SD_QUAL_01_READY'):
                continue
            if lo<=c.epoch_ns(event.get('timestamp'))<=hi:
                found.append({'at':event['timestamp'],'turn_id':turn,'type':'task_complete',
                              'last_agent_message':'SD_QUAL_01_READY'})
    if len(found)>1: raise ValueError('ambiguous native completion event')
    return found[0] if found else None


def validate_process_identity(value):
    keys={'pid','ppid','uid','comm','native_executable_matches','started_at_local'}
    if type(value) is not dict or set(value)!=keys:
        raise ValueError('complete native process schema required')
    if not c.integer(value['pid']) or not c.integer(value['ppid'],0) or type(value['uid']) is not int or value['uid']!=os.getuid():
        raise ValueError('invalid native process numbers')
    if value['comm']!='codex' or value['native_executable_matches'] is not True:
        raise ValueError('unexpected native executable identity')
    birth=value['started_at_local']
    if type(birth) is not str or not re.fullmatch(r'[A-Z][a-z]{2} [A-Z][a-z]{2} [0-9]{1,2} [0-9]{2}:[0-9]{2}:[0-9]{2} [0-9]{4}',birth):
        raise ValueError('invalid native birth timestamp')
    dt.datetime.strptime(birth,'%a %b %d %H:%M:%S %Y')


def validate_socket_identity(value):
    keys={'path','exists','dev','ino','uid','mode','service_holds_path'}
    if type(value) is not dict or set(value)!=keys:
        raise ValueError('complete native socket schema required')
    if (value['exists'] is not True or value['service_holds_path'] is not True or value['path']!=str(c.SOCKET)
            or value['mode']!='0600' or type(value['uid']) is not int or value['uid']!=os.getuid()
            or not c.integer(value['dev'],1,2**63-1) or not c.integer(value['ino'],1,2**63-1)):
        raise ValueError('invalid native socket identity')

def require_owned_pair(service,tui,socket,original):
    if type(original) is not dict or set(original)!={'service','tui','socket'}:
        raise ValueError('complete original ownership binding required')
    for value in (service,tui,original['service'],original['tui']): validate_process_identity(value)
    for value in (socket,original['socket']): validate_socket_identity(value)
    require_live_processes(service,tui)
    if not same_process(service,original['service']) or not same_process(tui,original['tui']):
        raise ValueError('native creation identity changed')
    if not socket.get('exists') or socket.get('service_holds_path') is not True:
        raise ValueError('owned native socket is not live')
    if any(socket.get(k)!=original['socket'].get(k) for k in ('dev','ino','uid','mode')):
        raise ValueError('native socket identity changed')
    return True
