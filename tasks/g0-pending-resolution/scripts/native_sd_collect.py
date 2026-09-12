"""Fixed sd-qual-02 local evidence. No RPC, raw log, or model history export."""
import argparse
import ctypes
import datetime as dt
import hashlib
import json
import os
import re
import sqlite3
import stat
import subprocess
import sys
import time
import uuid
from pathlib import Path
from native_sd_clock import continuous_ns

BASE = Path('/Users/guanjunhui/code/ai/codex-cli-orchestration-design')
REPO = Path('/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/test-repo')
CODEX_HOME = Path('/Users/guanjunhui/.codex')
CLI = Path('/Users/guanjunhui/.local/bin/codex')
CLI_SHA = 'b973d440acac501fd2594a43e7ca9ce41e0a65b9dfb28d0d7a7837c99e1261e3'
GO = BASE / 'tasks/g0-retained-resume/data/g0-return-lab-retained-darwin-arm64'
GO_SHA = 'feb9b9ad969abca5ff35db28c0f0d8d920dc0778d7aac68a90952900e989a285'
SOCKET = CODEX_HOME / 'app-server-control/app-server-control.sock'
HOOKS = BASE / 'tasks/g0-macos/data/hook-events'
NONCE = 'sd-qual-02'
UUID_RE = r'[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}'
STAMP_RE = r'[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{1,9})?(?:Z|\+00:00)'


def epoch_ns(value):
    if type(value) is not str or not re.fullmatch(STAMP_RE,value):
        raise ValueError('explicit UTC timestamp required')
    seconds = int(dt.datetime.fromisoformat(value[:19]+'+00:00').timestamp())
    fraction = re.match(r'\.([0-9]+)',value[19:])
    return seconds*1000000000 + (int(fraction[1].ljust(9,'0')) if fraction else 0)


def window(since, through, now):
    lo, hi = epoch_ns(since), epoch_ns(through)
    if now.tzinfo is None:
        raise EpochWindowError('invalid original observation window')
    delta = hi-lo
    current = epoch_ns(now.astimezone(dt.timezone.utc).isoformat(timespec='microseconds'))
    if not 0 < delta <= 120000000000 or hi > current:
        raise EpochWindowError('invalid original observation window')
    return lo, hi


class EpochWindowError(ValueError):
    code = 'epoch_correlation_inconclusive'


def valid_id(value):
    return type(value) is str and re.fullmatch(UUID_RE,value) is not None and str(uuid.UUID(value)) == value


def integer(value, low=1, high=2**31-1):
    return type(value) is int and low <= value <= high


def process_fields(text, pid, uid):
    parts = text.strip().split(maxsplit=8)
    if len(parts) != 9 or not all(re.fullmatch(r'[0-9]+',x) for x in parts[:3]):
        raise ValueError('invalid process fields')
    actual, parent, owner = map(int,parts[:3]); birth = ' '.join(parts[3:8])
    dt.datetime.strptime(birth,'%a %b %d %H:%M:%S %Y')
    if (not integer(actual) or not integer(parent,0) or actual != pid or owner != uid
            or parts[8] not in ('codex',str(CLI),str(CLI.resolve()))):
        raise ValueError('unexpected process identity')
    return {'pid':actual,'ppid':parent,'uid':owner,'started_at_local':birth,'comm':parts[8]}


def same_process(current, original):
    return current is not None and all(current.get(k) == original.get(k) for k in ('pid','started_at_local'))


def require_live_processes(service,tui):
    if service is None or tui is None or service['pid'] == tui['pid']:
        raise ValueError('both distinct native processes must be live')
    return True


def thread_fields(row, root):
    # Ordinary `codex app-server` uses SessionSource::VSCode; this is not a client identity.
    if not valid_id(root) or not row or tuple(row) != (root,'vscode',str(REPO),'0.153.4'):
        raise ValueError('not the fixed native experiment thread')
    return {'id':root,'source':'vscode','experiment_cwd_matches':True,'cli_version':'0.153.4'}


def hook_fields(value, root, service, lo, hi):
    if type(value) is not dict or value.get('session_id') != root or value.get('ppid') != service:
        return None
    event = value.get('hook_event_name')
    if event not in ('SessionStart','UserPromptSubmit','SessionEnd'):
        return None
    if not integer(value.get('pid')) or not integer(value.get('ppid')):
        return None
    try:
        if not lo <= epoch_ns(value.get('recorded_at')) <= hi: return None
    except (ValueError,TypeError): return None
    turn = value.get('turn_id')
    if (turn is not None and not valid_id(turn)) or (event == 'UserPromptSubmit' and turn is None):
        return None
    out = {k:value[k] for k in ('session_id','hook_event_name','pid','ppid','recorded_at')}
    if turn is not None: out['turn_id'] = turn
    if value.get('source') in ('startup','resume','clear','compact'): out['source'] = value['source']
    return out


def ui_status(text):
    # This parser reports observed /status identity, never general displayed-thread liveness.
    # The caller supplies only the fresh pre-prompt /status response range.
    text = re.sub(r'\x1b\[[0-9;]*[Hf]','\n',text)
    text = re.sub(r'\x1b\[[0-?]*[ -/]*[@-~]','',text)
    ids = set(re.findall(r'(?m)^[ \t│|]*Session:[ \t]*('+UUID_RE+r')[ \t│|]*\r?$',text))
    return {'status_root':next(iter(ids))} if len(ids) == 1 else None


def log_fields(body):
    choices = {'transport':{'unix_socket','in-process'},'client_name':{'codex-tui','g0-native-standalone'},
               'client_version':{'0.153.4','0.1.0'},'api_version':{'v2'},
               'method':{'initialize','thread/start','thread/read','turn/start','skills/list','hooks/list',
                         'remoteControl/status/read','server/diagnostics','thread/loaded/list'}}
    out = {}
    for key, allowed in choices.items():
        values = set(re.findall(r'\b'+key+r'=(?:"([^"\s]{1,80})"|([^\s{}"]{1,80}))',body))
        values = {a or b for a,b in values}
        if len(values) == 1 and next(iter(values)) in allowed: out[key] = next(iter(values))
        elif key=='client_name' and values: out['unrecognized_client_name'] = True
    connections = set(re.findall(r'\bconnection_id="?([0-9]{1,10})"?(?=[\s}]|$)',body))
    if len(connections) == 1 and integer(int(next(iter(connections))),0):
        out['connection_id'] = int(next(iter(connections)))
    return out


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def native_executable_path(pid):
    if not integer(pid): raise ValueError('invalid PID')
    lib = ctypes.CDLL('/usr/lib/libproc.dylib'); buf = ctypes.create_string_buffer(4096)
    count = lib.proc_pidpath(pid,buf,len(buf))
    observed = os.fsdecode(buf.value)
    if not 0 < count < len(buf) or not Path(observed).is_absolute() or observed != str(CLI.resolve()):
        raise ValueError('unexpected native executable path')
    return observed


def process(pid):
    if not integer(pid): raise ValueError('invalid PID')
    proc = subprocess.run(['ps','-p',str(pid),'-o','pid=,ppid=,uid=,lstart=,comm='],
                          text=True,capture_output=True,timeout=2,env=dict(os.environ,LC_ALL='C'))
    if proc.returncode: return None
    result = process_fields(proc.stdout,pid,os.getuid())
    result['executable_path'] = native_executable_path(pid)
    result['native_executable_matches'] = True
    return result


def snapshot_socket(service=None):
    if not os.path.lexists(SOCKET): return {'path':str(SOCKET),'exists':False}
    info = SOCKET.lstat()
    if not stat.S_ISSOCK(info.st_mode) or info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o600:
        raise ValueError('unexpected socket kind/owner/mode')
    out = {'path':str(SOCKET),'exists':True,'dev':info.st_dev,'ino':info.st_ino,'uid':info.st_uid,'mode':'0600'}
    if service is not None:
        proc = subprocess.run(['lsof','-a','-p',str(service),'-U','-Fn'],text=True,capture_output=True,timeout=2)
        out['service_holds_path'] = proc.returncode == 0 and 'n'+str(SOCKET) in proc.stdout.splitlines()
    return out


def protected_files():
    paths = {'global_config':CODEX_HOME/'config.toml', 'login_profile':Path('/Users/guanjunhui/.zprofile'),
             'test_config':REPO/'.codex/config.toml','test_hooks':REPO/'.codex/hooks.json',
             'trusted_hook_binary':BASE/'tasks/g0-macos/data/g0-hook-darwin-arm64',
             'existing_daemon_settings':CODEX_HOME/'app-server-daemon/settings.json'}
    result = {}
    for key,path in paths.items():
        result[key] = {'exists':path.exists()}
        if path.exists(): result[key].update(sha256=sha(path),bytes=path.stat().st_size)
    return result


def local_evidence(root,service,lo,hi):
    db = sqlite3.connect('file:'+str(CODEX_HOME/'state_5.sqlite')+'?mode=ro',uri=True)
    try: row = db.execute('SELECT id,source,cwd,cli_version FROM threads WHERE id=?',(root,)).fetchone()
    finally: db.close()
    result = {'thread':thread_fields(row,root),'hooks':[],'native_protocol':[]}
    for path in HOOKS.glob('*.json'):
        if path.is_symlink() or not path.is_file() or path.stat().st_size > 8192: continue
        # Skip old/unrelated files before decoding; retained records still require exact root/service.
        if not lo-2000000000 <= path.stat().st_mtime_ns <= hi+2000000000: continue
        try: item = hook_fields(json.loads(path.read_text()),root,service,lo,hi)
        except (ValueError,OSError): continue
        if item is not None: result['hooks'].append(item)
    result['hooks'].sort(key=lambda x:x['recorded_at'])
    db = sqlite3.connect('file:'+str(CODEX_HOME/'logs_2.sqlite')+'?mode=ro',uri=True)
    try:
        rows = db.execute('SELECT ts,ts_nanos,target,thread_id,process_uuid,feedback_log_body FROM logs '
                          'WHERE process_uuid LIKE ? AND ts>=? AND ts<=? '
                          'AND target IN (?,?) ORDER BY id LIMIT 5001',
                          (f'pid:{service}:%',lo//1000000000,hi//1000000000,'codex_config::loader::layer_io','codex_app_server::outgoing_message')).fetchall()
    finally: db.close()
    if len(rows) > 5000: raise ValueError('native log row bound exceeded')
    for sec,nanos,target,thread,process_uuid,body in rows:
        if not integer(sec,0,2**63-1) or not integer(nanos,0,999999999): continue
        if not lo <= sec*1000000000+nanos <= hi or not re.fullmatch('pid:'+str(service)+':'+UUID_RE,process_uuid): continue
        if type(body) is not str or len(body) > 65536: continue
        fields = log_fields(body)
        if not fields: continue
        result['native_protocol'].append({'ts':sec,'ts_nanos':nanos,'target':target,'process_uuid':process_uuid,
                                          'exact_root':thread == root,'fields':fields})
    return result


def main():
    started = continuous_ns()
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('mode',choices=('preflight','status','snapshot'))
    p.add_argument('--nonce',required=True,choices=(NONCE,))
    p.add_argument('--root'); p.add_argument('--service-pid',type=int); p.add_argument('--tui-pid',type=int)
    p.add_argument('--since'); p.add_argument('--through'); p.add_argument('--output',type=Path,required=True)
    args = p.parse_args()
    docs = Path(__file__).resolve().parents[1]/'docs'
    if args.output.parent.resolve() != docs or not re.fullmatch(r'native-sd-qual-02-[a-z-]+\.json',args.output.name):
        raise ValueError('output must be the fixed native evidence location')
    out = {'version':1,'nonce':NONCE,'captured_at':dt.datetime.now(dt.timezone.utc).isoformat(),
           'scope':'fixed controller-owned TUI sample; not automatic attachment or complete G0'}
    if args.mode == 'status':
        raw = sys.stdin.read(1048577)
        if len(raw)>1048576: raise ValueError('status input too large')
        info = ui_status(raw)
        if info is None or (args.root is not None and info['status_root'] != args.root):
            raise ValueError('no unambiguous status root')
        out.update(info)
    else:
        if sha(CLI.resolve()) != CLI_SHA or sha(GO) != GO_SHA: raise ValueError('fixed binary SHA mismatch')
        out.update(native_sha256=CLI_SHA,go_sha256=GO_SHA,protected_files=protected_files())
        boot = subprocess.run(['sysctl','-n','kern.bootsessionuuid'],text=True,capture_output=True,timeout=2)
        value = boot.stdout.strip().lower()
        if boot.returncode or not valid_id(value): raise ValueError('missing boot identity')
        out['boot_id'] = value
        if args.mode == 'preflight':
            out['socket'] = snapshot_socket()
            if out['socket']['exists']: raise ValueError('default socket is occupied; do not start or replace service')
        else:
            if not valid_id(args.root): raise ValueError('explicit exact root UUID required')
            lo,hi = window(args.since,args.through,dt.datetime.now(dt.timezone.utc))
            out.update(root=args.root,window={'since':args.since,'through':args.through},
                       service=process(args.service_pid),tui=process(args.tui_pid),socket=snapshot_socket(args.service_pid))
            require_live_processes(out['service'],out['tui'])
            out.update(local_evidence(args.root,args.service_pid,lo,hi))
    elapsed=continuous_ns()-started
    out['local_elapsed_seconds'] = elapsed/1000000000
    if elapsed > 10000000000: raise TimeoutError('local observation exceeded ten seconds')
    with args.output.open('x') as f: json.dump(out,f,ensure_ascii=False,indent=2); f.write('\n')
    print(json.dumps({'status':'recorded','mode':args.mode,'nonce':NONCE,'file':str(args.output)}))


if __name__ == '__main__':
    try: main()
    except (ValueError,OSError,sqlite3.Error,subprocess.SubprocessError):
        print('{"status":"rejected","error":"invalid_evidence_or_environment"}',file=sys.stderr)
        sys.exit(2)
