#!/usr/bin/env python3
"""Finite, typed observer of only this task's foreground shutdown probes."""
import argparse
import datetime
import json
import pathlib
import re
import sqlite3
import subprocess
import time
import uuid
from native_process_identity import same_process


def identity(pid):
    proc = subprocess.run(["ps", "-p", str(pid), "-o", "pid=,ppid=,pgid=,lstart=,comm="], text=True, capture_output=True)
    if proc.returncode != 0:
        return None
    parts = proc.stdout.split(maxsplit=8)
    if len(parts) != 9 or any(not x.isascii() or not x.isdecimal() for x in parts[:3]):
        raise SystemExit("invalid process identity")
    born = " ".join(parts[3:8]); datetime.datetime.strptime(born, "%a %b %d %H:%M:%S %Y")
    executable = pathlib.Path(parts[8].strip()).name
    if not re.fullmatch(r"codex|Python|python[0-9.]*", executable):
        raise SystemExit("unexpected probe executable")
    return {"pid": int(parts[0]), "ppid": int(parts[1]), "pgid": int(parts[2]),
            "started_at_local": born, "executable_basename": executable}


def stamp():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--root", required=True)
parser.add_argument("--native-pid", required=True, type=int)
parser.add_argument("--nonce", required=True)
parser.add_argument("--mode", choices=("ready", "stopped"), required=True)
parser.add_argument("--deadline", required=True)
parser.add_argument("--output", type=pathlib.Path, required=True)
parser.add_argument("--ready", type=pathlib.Path)
parser.add_argument("--action-file", type=pathlib.Path)
args = parser.parse_args()
assert str(uuid.UUID(args.root)) == args.root and 0 < args.native_pid < 2**31
match = re.fullmatch(r"rr-probe-(quit|kill)-([0-9]{2})", args.nonce)
assert match
repo = pathlib.Path("/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/test-repo")
directory = repo / ("tasks/retained-resume/data/native-probe-" + match[1] + "-" + match[2])
assert directory.stat().st_mode & 0o777 == 0o700
deadline = datetime.datetime.fromisoformat(args.deadline.replace("Z", "+00:00"))
assert 0 < (deadline - datetime.datetime.now(datetime.timezone.utc)).total_seconds() <= 120
db = sqlite3.connect("file:/Users/guanjunhui/.codex/state_5.sqlite?mode=ro", uri=True)
row = db.execute("SELECT source,cwd,rollout_path FROM threads WHERE id=?", (args.root,)).fetchone()
assert row and row[0] == "cli" and pathlib.Path(row[1]) == repo
rollout = pathlib.Path(row[2]); assert pathlib.Path('/Users/guanjunhui/.codex/sessions') in rollout.parents
db.close()
native = identity(args.native_pid) if args.mode == "ready" else None
if args.mode == "ready":
    assert native and native['executable_basename'] == 'codex'
else:
    ready = json.loads(args.ready.read_text())
    assert ready['root_thread'] == args.root and ready['nonce'] == args.nonce
    native = ready['native_identity']
observations = []; last = None
action = None
while datetime.datetime.now(datetime.timezone.utc) < deadline:
    if args.mode == 'stopped' and action is None and args.action_file and args.action_file.exists():
        action = json.loads(args.action_file.read_text())
        assert set(action) == {'root_thread','nonce','native_pid','action','at'}
        assert action['root_thread'] == args.root and action['nonce'] == args.nonce
        assert type(action['native_pid']) is int and action['native_pid'] == args.native_pid
        assert action['action'] in ('quit','SIGKILL') and type(action['at']) is str and len(action['at']) <= 32
        deadline = datetime.datetime.fromisoformat(action['at']) + datetime.timedelta(seconds=10)
    meta_path = directory / "meta.json"
    if not meta_path.exists():
        time.sleep(0.1); continue
    meta = json.loads(meta_path.read_text())
    assert set(meta) == {'version','nonce','controller_thread','pid','ppid','pgid','duration_seconds'}
    assert type(meta['version']) is int and meta['version'] == 1 and meta['nonce'] == args.nonce and meta['controller_thread'] == args.root
    assert all(type(meta[k]) is int and 0 < meta[k] < 2**31 for k in ('pid','ppid','pgid')) and meta['duration_seconds'] == 90
    events = []
    event_path = directory / 'events.jsonl'
    if event_path.exists():
        for line in event_path.read_text().splitlines():
            value = json.loads(line)
            assert set(value) in ({'version','nonce','status','at'},{'version','nonce','status','at','signal'})
            assert type(value['version']) is int and value['version'] == 1 and value['nonce'] == args.nonce
            assert value['status'] in ('running','interrupted','completed')
            assert type(value['at']) is str and len(value['at']) <= 32
            datetime.datetime.fromisoformat(value['at'])
            assert value.get('signal') in (None,'SIGTERM','SIGHUP','SIGINT')
            events.append(value)
    worker = identity(meta['pid'])
    if args.mode == 'ready':
        final_seen = False
        for line in rollout.open():
            event = json.loads(line); payload = event.get('payload',{})
            if event.get('type') == 'event_msg' and payload.get('type') == 'task_complete' and payload.get('last_agent_message') == args.nonce + '-WAITING':
                final_seen = True
        if worker and events and events[-1]['status'] == 'running' and final_seen:
            assert identity(args.native_pid) == native
            result = {'at':stamp(),'root_thread':args.root,'nonce':args.nonce,'native_identity':native,'worker_identity':worker,'metadata':meta,'final_waiting_seen':True,'probe_events':events}
            args.output.write_text(json.dumps(result,indent=2)+'\n'); print(json.dumps(result)); break
    else:
        state = (same_process(identity(args.native_pid), native), same_process(worker, ready['worker_identity']), events)
        if state != last:
            observations.append({'at':stamp(),'native_same_identity_alive':state[0],'worker_same_identity_alive':state[1],
                                 'worker_current_ppid':worker['ppid'] if state[1] else None,'probe_events':events})
            last = state
        if not state[0] and not state[1]:
            break
    time.sleep(0.1)
else:
    if args.mode == 'ready':
        raise SystemExit('probe not ready within original model window')
if args.mode == 'stopped':
    result = {'at':stamp(),'root_thread':args.root,'nonce':args.nonce,'action':action,'observations':observations,'both_original_processes_gone':bool(action and observations and not observations[-1]['native_same_identity_alive'] and not observations[-1]['worker_same_identity_alive'])}
    args.output.write_text(json.dumps(result,indent=2)+'\n'); print(json.dumps(result))
