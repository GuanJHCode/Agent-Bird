"""Fixed four-sample read-only runtime observer; never starts or stops work."""
import argparse
import datetime
import fcntl
import hashlib
import json
import os
import pathlib
import re
import sqlite3
import stat
import subprocess
import time
import uuid
from native_process_identity import same_process
from native_retained_collect import APPROVED_BINARY, APPROVED_SHA256, REPO, valid_binary, valid_window
from native_retained_data import valid_snapshot

TASK = pathlib.Path(__file__).resolve().parents[1]
NONCES = {'rr-retained-quit-01','rr-retained-quit-02','rr-retained-quit-03','rr-retained-kill-01'}


def now():
    return datetime.datetime.now(datetime.timezone.utc)


def binary_check(phase):
    if not valid_binary(APPROVED_BINARY):
        raise ValueError('approved binary changed')
    info = pathlib.Path(APPROVED_BINARY).stat()
    return {'phase':phase,'at':now().isoformat(),'sha256':APPROVED_SHA256,'inode':info.st_ino,'size':info.st_size}


def identity(pid):
    found = subprocess.run(['ps','-p',str(pid),'-o','pid=,ppid=,pgid=,lstart=,comm='],capture_output=True,text=True)
    if found.returncode != 0:
        return None
    parts = found.stdout.split(maxsplit=8)
    if len(parts) != 9 or any(not x.isascii() or not x.isdecimal() for x in parts[:3]):
        raise ValueError('invalid process identity')
    born = ' '.join(parts[3:8]); datetime.datetime.strptime(born,'%a %b %d %H:%M:%S %Y')
    executable = pathlib.Path(parts[8].strip()).name
    label = 'codex' if executable == 'codex' else 'retained_binary' if executable == pathlib.Path(APPROVED_BINARY).name else 'other'
    return {'pid':int(parts[0]),'ppid':int(parts[1]),'pgid':int(parts[2]),'started_at_local':born,'executable':label}


def valid_identity(value):
    if type(value) is not dict or set(value) != {'pid','ppid','pgid','started_at_local','executable'}:
        return False
    if any(type(value[k]) is not int or not 0 < value[k] < 2**31 for k in ('pid','ppid','pgid')):
        return False
    if value['executable'] not in ('codex','retained_binary','other') or type(value['started_at_local']) is not str or len(value['started_at_local']) > 24:
        return False
    try:
        datetime.datetime.strptime(value['started_at_local'],'%a %b %d %H:%M:%S %Y'); return True
    except ValueError:
        return False


def lock_state(path):
    before = path.lstat()
    if not stat.S_ISREG(before.st_mode) or before.st_mode & 0o777 != 0o600 or before.st_uid != os.getuid() or before.st_nlink != 1:
        raise ValueError('invalid stable lock')
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        after = os.fstat(fd)
        if (before.st_dev,before.st_ino) != (after.st_dev,after.st_ino):
            raise ValueError('lock inode changed')
        available = False
        try:
            fcntl.flock(fd,fcntl.LOCK_EX | fcntl.LOCK_NB); available = True
        except BlockingIOError:
            pass
        finally:
            if available:
                fcntl.flock(fd,fcntl.LOCK_UN)
        return {'available':available,'device':after.st_dev,'inode':after.st_ino}
    finally:
        os.close(fd)


def action_deadline(action, root, nonce, pid, begin, now):
    if (type(action) is not dict or set(action) != {'root_thread','nonce','native_pid','action','at'}
            or action['root_thread'] != root or action['nonce'] != nonce
            or type(action['native_pid']) is not int or action['native_pid'] != pid
            or action['action'] not in ('quit','SIGKILL')):
        raise ValueError('invalid action record')
    _, instant = valid_window(begin.isoformat(),action['at'],now)
    end = instant + datetime.timedelta(seconds=10)
    if now >= end:
        raise ValueError('action was not observed inside its fixed ten-second window')
    return end


def pre_action_live(origin, view):
    if view is None:
        return False
    native=view['native_identity']; worker=view['worker_identity']
    return bool(same_process(native,origin['native']) and native['executable']=='codex'
                and same_process(worker,origin['worker']) and worker['executable']=='retained_binary'
                and worker['ppid']==native['pid'] and view['lock']['available'] is False
                and view['snapshot']['worker_pid']==origin['worker']['pid']
                and view['snapshot']['completed_steps']<view['snapshot']['total_steps'])


def inspect_view(directory, root, nonce, native_pid):
    if not (directory/'retained-state.json').exists():
        return None
    call = subprocess.run([APPROVED_BINARY,'retained-inspect','--dir',str(directory),'--nonce',nonce],
                          capture_output=True,text=True,timeout=2)
    if call.returncode != 0:
        if call.stderr.strip() == '{"version":1,"status":"error","error":"snapshot_changed"}':
            return None
        raise ValueError('retained inspect failed')
    value = json.loads(call.stdout)
    if not valid_snapshot(value,root,nonce):
        raise ValueError('retained inspect schema or identity invalid')
    prefix = []
    for step in range(1,value['completed_steps']+1):
        path = directory / ('step-%03d.json' % step); info = path.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o777 != 0o600 or not 0 < info.st_size <= 32768:
            raise ValueError('invalid step record')
        prefix.append({'step':step,'sha256':hashlib.sha256(path.read_bytes()).hexdigest(),'bytes':info.st_size})
    return {'snapshot':value,'native_identity':identity(native_pid),'worker_identity':identity(value['worker_pid']),
            'lock':lock_state(directory/'retained.lock'),'effects':prefix}


def root_activity(path, since):
    state = 'unknown'; starts = []
    for line in path.open():
        event = json.loads(line); payload = event.get('payload',{})
        if event.get('type') != 'event_msg':
            continue
        if payload.get('type') == 'task_started':
            state = 'busy'
            if datetime.datetime.fromisoformat(event['timestamp'].replace('Z','+00:00')) >= since:
                starts.append(event['timestamp'])
        elif payload.get('type') in ('task_complete','turn_aborted'):
            state = 'idle'
    return {'last_turn_state':state,'task_started_since':starts}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for key in ('root','nonce','native-pid','mode','output'):
        parser.add_argument('--'+key,required=True)
    parser.add_argument('--deadline'); parser.add_argument('--since'); parser.add_argument('--reference'); parser.add_argument('--action-file')
    parser.add_argument('--segment',type=int,default=1); parser.add_argument('--min-steps',type=int,default=1)
    args = parser.parse_args(); assert str(uuid.UUID(args.root)) == args.root and args.nonce in NONCES
    pid = int(args.native_pid); assert 0 < pid < 2**31 and args.mode in ('ready','stop','idle','snapshot')
    assert args.segment in (1,2) and 1 <= args.min_steps <= 32
    directory = REPO / ('tasks/retained-resume/data/native-' + args.nonce.removeprefix('rr-'))
    assert directory.stat().st_mode & 0o777 == 0o700
    output = pathlib.Path(args.output).resolve(); assert output.parent == TASK/'docs' and output.name.startswith('native-')
    checks = [binary_check('observer_start')]; begin = now(); start_mono = time.monotonic()
    if args.mode in ('ready','stop'):
        assert args.deadline and args.deadline.endswith(('Z','+00:00'))
        deadline = datetime.datetime.fromisoformat(args.deadline.replace('Z','+00:00'))
        assert 0 < (deadline-begin).total_seconds() <= 120
    else:
        deadline = begin + datetime.timedelta(seconds=10)
    since = datetime.datetime.fromisoformat(args.since.replace('Z','+00:00')) if args.since else begin
    if args.since:
        valid_window(args.since,begin.isoformat(),begin)
    db = sqlite3.connect('file:/Users/guanjunhui/.codex/state_5.sqlite?mode=ro',uri=True)
    selected = db.execute('SELECT source,cwd,rollout_path FROM threads WHERE id=?',(args.root,)).fetchone(); db.close()
    assert selected and selected[0]=='cli' and pathlib.Path(selected[1])==REPO
    rollout = pathlib.Path(selected[2]).resolve(); assert pathlib.Path('/Users/guanjunhui/.codex/sessions') in rollout.parents
    reference = None; origin = None; action = None
    if args.mode in ('stop','idle'):
        refpath = pathlib.Path(args.reference).resolve(); assert refpath.parent==TASK/'docs' and refpath.name.startswith('native-')
        reference = json.loads(refpath.read_text()); assert reference['root_thread']==args.root and reference['nonce']==args.nonce
        origin = reference['origin']; assert set(origin)=={'native','worker'} and all(valid_identity(x) for x in origin.values())
        assert valid_snapshot(reference['view']['snapshot'],args.root,args.nonce)
    if args.mode=='stop':
        action_path=pathlib.Path(args.action_file).resolve(); assert action_path.parent==TASK/'docs' and action_path.name.startswith('native-')
        assert not action_path.exists() and origin['native']['pid']==pid
    initial_native = identity(pid)
    if args.mode in ('ready','idle'):
        assert initial_native and initial_native['executable']=='codex'
    armed_path=None; armed_sha=None
    if args.mode=='stop':
        first=inspect_view(directory,args.root,args.nonce,pid)
        if (not pre_action_live(origin,first)
                or first['snapshot']['segment']!=reference['view']['snapshot']['segment']
                or first['lock']!=reference['view']['lock']):
            raise ValueError('stop pre-action native/worker/lock no longer match the live ready proof')
        proof={'at':now().isoformat(),'root_thread':args.root,'nonce':args.nonce,'origin':origin,'view':first,
               'binary_sha256':APPROVED_SHA256,'observer_began_at':begin.isoformat()}
        armed_path=output.with_name(output.stem+'-armed.json'); assert not armed_path.exists()
        raw=(json.dumps(proof,indent=2)+'\n').encode(); armed_sha=hashlib.sha256(raw).hexdigest()
        with armed_path.open('xb') as file: file.write(raw)
        print(json.dumps({'armed':True,'armed_file':str(armed_path),'pre_action_sha256':armed_sha,
                          'root_thread':args.root,'nonce':args.nonce}),flush=True)
    observations=[]; previous=None; view=None; success=False; stable=True
    while now()<deadline:
        if args.mode=='stop' and action is None and action_path.exists():
            action=json.loads(action_path.read_text())
            control_keys={'root_thread','nonce','native_pid','action','at'}
            assert set(action)==control_keys|{'precheck','pre_action_sha256','result'}
            deadline=action_deadline({k:action[k] for k in control_keys},args.root,args.nonce,pid,begin,now())
            assert action['pre_action_sha256']==armed_sha and action['result']=={'quit':'stdin_submitted','SIGKILL':'signal_sent'}[action['action']]
            check=action['precheck']; assert type(check)is dict and set(check)=={'at','native','worker','lock','snapshot'}
            _,checked_at=valid_window(begin.isoformat(),check['at'],now())
            acted_at=datetime.datetime.fromisoformat(action['at'].replace('Z','+00:00'))
            assert 0<=(acted_at-checked_at).total_seconds()<=2
            assert valid_identity(check['native']) and valid_identity(check['worker']) and valid_snapshot(check['snapshot'],args.root,args.nonce)
            assert type(check['lock'])is dict and set(check['lock'])=={'available','device','inode'}
            assert check['lock']==first['lock'] and type(check['lock']['available'])is bool
            assert all(type(check['lock'][key])is int for key in ('device','inode'))
            assert check['snapshot']['segment']==first['snapshot']['segment']
            assert pre_action_live(origin,{'native_identity':check['native'],'worker_identity':check['worker'],
                                          'lock':check['lock'],'snapshot':check['snapshot']})
        view=inspect_view(directory,args.root,args.nonce,pid)
        if view is None:
            time.sleep(0.1); continue
        if view!=previous:
            observations.append({'at':now().isoformat(),'view':view}); previous=view
        snap=view['snapshot']; worker=view['worker_identity']
        if args.mode=='ready' and snap['segment']==args.segment and args.min_steps<=snap['completed_steps']<snap['total_steps']:
            if (snap['status']=='running' and same_process(view['native_identity'],initial_native) and worker
                    and worker['executable']=='retained_binary' and worker['ppid']==pid and not view['lock']['available']):
                origin={'native':initial_native,'worker':worker}; checks.append(binary_check('ready'))
                success=True; break
        elif args.mode=='stop' and action is not None:
            old_worker=identity(origin['worker']['pid'])
            if (now()<=deadline and not same_process(view['native_identity'],origin['native'])
                    and not same_process(old_worker,origin['worker']) and view['lock']['available']
                    and snap['worker_pid']==origin['worker']['pid'] and snap['segment']==reference['view']['snapshot']['segment']
                    and (view['lock']['device'],view['lock']['inode'])==(reference['view']['lock']['device'],reference['view']['lock']['inode'])):
                success=True; break
        elif args.mode=='idle':
            baseline=reference['view']
            stable = stable and (same_process(view['native_identity'],initial_native)
                and not same_process(identity(origin['worker']['pid']),origin['worker']) and view['lock']['available']
                and (view['lock']['device'],view['lock']['inode'])==(baseline['lock']['device'],baseline['lock']['inode'])
                and snap==baseline['snapshot'] and view['effects']==baseline['effects'])
        elif args.mode=='snapshot':
            success=True; break
        time.sleep(0.1)
    activity=root_activity(rollout,since)
    if args.mode=='idle':
        success=stable and view is not None and (now()-begin).total_seconds()>=10 and not activity['task_started_since']
    checks.append(binary_check('observer_end'))
    result={'root_thread':args.root,'nonce':args.nonce,'mode':args.mode,'began_at':begin.isoformat(),'ended_at':now().isoformat(),
            'elapsed_seconds':round(time.monotonic()-start_mono,6),'deadline':deadline.isoformat(),'success':success,
            'origin':origin,'action':action,'view':view,'observations':observations,'root_activity':activity,'binary_checks':checks,
            'pre_action_file':str(armed_path) if armed_path else None,'pre_action_sha256':armed_sha,
            'scope':'single approved foreground fixture; process creation identity plus stable flock; no process-tree claim'}
    output.write_text(json.dumps(result,indent=2)+'\n'); print(json.dumps({'output':str(output),'success':success,'mode':args.mode,'view':view}))


if __name__=='__main__':
    main()
