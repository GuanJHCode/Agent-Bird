#!/usr/bin/env python3
"""T12 acceptance fixture. Never launches a model without --execute-live."""
import argparse
import base64
import hmac
import secrets
import stat
import sqlite3
import time
import re
import copy
import hashlib
import json
import os
from pathlib import Path
import resource
import subprocess
import sys
import tempfile
import threading
import select
import time

PATHS = ['calc.py', 'arithmetic/__init__.py', 'arithmetic/ops.py']
BRIEF = ('Implement add(a,b) in arithmetic/ops.py; re-export it from arithmetic/__init__.py; '
         'calc.py delegates add to that package and provides sum_values(iterable). '
         'sum_values uses add; empty iterable returns 0; generators work. Only modify the three authorized files.')
ACCEPTANCE = ['add: positive, negative, zero and decimal numbers',
              'calc.add delegates to arithmetic.ops.add through package export',
              'sum_values: positive/negative/zero/decimal, empty iterable and single-use generator',
              'exactly calc.py, arithmetic/__init__.py, arithmetic/ops.py; original workspace HEAD unchanged']
CHECK = '''import runpy, sys\nsys.path.insert(0, CANDIDATE)\nfrom arithmetic import add\nimport arithmetic.ops as ops\ncalc = runpy.run_path(CANDIDATE + "/calc.py")\nassert add is ops.add\nfor a,b,want in [(2,3,5),(-2,3,1),(0,0,0),(1.5,2.25,3.75)]:\n    assert add(a,b)==want\n    assert calc['add'](a,b)==want\nfor values,want in [([1,2,3],6),([-2,3],1),([0,0],0),([1.5,2.25],3.75),([],0)]:\n    assert calc['sum_values'](values)==want\nassert calc['sum_values']((v for v in [1,-2,3.5]))==2.5\ncalls=[]\ndef traced_add(a,b):\n    calls.append((a,b))\n    return a+b\ncalc['sum_values'].__globals__['add']=traced_add\nassert calc['sum_values']([4,5])==9 and len(calls)==2\n# Exercise delegation behavior rather than accepting an unrelated duplicate implementation.\nassert add.__module__ == 'arithmetic.ops'\nprint('frozen acceptance assertions completed')\n'''

SEATBELT_BOOTSTRAP = '''import ctypes, runpy, sys
library=ctypes.CDLL('/usr/lib/libsandbox.dylib',use_errno=True)
initialize=library.sandbox_init
initialize.argtypes=[ctypes.c_char_p,ctypes.c_uint64,ctypes.POINTER(ctypes.c_char_p)]
initialize.restype=ctypes.c_int
error=ctypes.c_char_p()
policy=open(sys.argv[1],'rb').read()
if initialize(policy,0,ctypes.byref(error)) != 0: raise SystemExit('validation_sandbox_initialization_failed')
runpy.run_path(sys.argv[2],run_name='__main__')
'''

class Refused(ValueError):
    pass

def sha(data):
    return hashlib.sha256(data).hexdigest()

def file_sha(path):
    return sha(Path(path).read_bytes())

def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=False)

def private_json(path, value):
    with open(path, 'x', encoding='utf-8') as out:
        os.chmod(path, 0o600)
        json.dump(value, out, indent=2, ensure_ascii=False)
        out.write('\n')

def git(repo, *args):
    env = {'PATH': '/usr/bin:/bin', 'HOME': '/nonexistent', 'GIT_CONFIG_NOSYSTEM': '1',
           'GIT_CONFIG_GLOBAL': '/dev/null', 'GIT_OPTIONAL_LOCKS': '0'}
    return subprocess.run(['/usr/bin/git', '-c', 'core.hooksPath=/dev/null', '-C', str(repo), *args],
                          env=env, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                          timeout=10).stdout.decode().strip()

def snapshot(repo):
    return {'head': git(repo, 'rev-parse', 'HEAD'), 'status': git(repo, 'status', '--porcelain=v1', '--untracked-files=all')}

def require_binding(contract):
    for field in ('package', 'wrapper', 'python', 'mcp_config', 'codex_args', 'installed_binary'):
        item = contract['binding'].get(field)
        try:
            valid=item and Path(item['path']).is_absolute() and file_sha(item['path']) == item['sha256']
        except OSError:
            valid=False
        if not valid:
            raise Refused('unbound_or_changed_' + field)
    config_path=Path(contract['binding']['mcp_config']['path'])
    sidecar=config_path.with_name('codex-args.json')
    if str(sidecar)!=contract['binding']['codex_args']['path']:
        raise Refused('codex_args_not_adjacent_to_mcp_config')
    configuration = json.loads(Path(contract['binding']['mcp_config']['path']).read_text())
    if configuration['state_dir'] != contract['state_dir'] or configuration['workspace'] != contract['dispatch']['directory']:
        raise Refused('backend_configuration_scope')
    if contract['dispatch']['provider'] not in configuration['providers']:
        raise Refused('backend_provider_scope')
    wrapper=Path(contract['binding']['wrapper']['path'])
    if not os.access(wrapper, os.X_OK):
        raise Refused('wrapper_not_executable')
    # Product generated argv is the authoritative Codex MCP configuration.
    # Parse structurally, retain only variable names, and never report any values.
    try:
        argv=json.loads(sidecar.read_text())
        if not isinstance(argv,list) or len(argv)!=6 or any(not isinstance(x,str) for x in argv):
            raise ValueError
        if [argv[0],argv[2],argv[4]] != ['-c','-c','-c']:
            raise ValueError
        if not (argv[1].startswith('mcp_servers.agent_bird.command=') and
                argv[3].startswith('mcp_servers.agent_bird.args=') and
                argv[5].startswith('mcp_servers.agent_bird.env_vars=')):
            raise ValueError
        command=json.loads(argv[1].split('=',1)[1])
        backend_args=json.loads(argv[3].split('=',1)[1])
        env_names=json.loads(argv[5].split('=',1)[1])
        installed_binary=wrapper.parent.parent/'bin/codex-orchestrator'
        bound_binary=Path(contract['binding']['installed_binary']['path'])
        if bound_binary!=installed_binary:
            raise ValueError
        if command!=str(installed_binary) or not installed_binary.is_file():
            raise ValueError
        if backend_args!=['mcp','serve','--config',str(config_path)]:
            raise ValueError
        if (not isinstance(env_names,list) or not env_names or
            any(not isinstance(n,str) or not re.fullmatch(r'[A-Za-z_][A-Za-z0-9_]*',n) for n in env_names) or
            len(set(env_names))!=len(env_names) or 'AGENT_BIRD_OWNER_REQUEST' not in env_names):
            raise ValueError
    except (OSError,ValueError,TypeError,json.JSONDecodeError):
        raise Refused('invalid_product_codex_args') from None


def codex_env_names(contract):
    """Return only keys from the currently bound product generated argv."""
    require_binding(contract)
    argv=json.loads(Path(contract['binding']['codex_args']['path']).read_text())
    return json.loads(argv[5].split('=',1)[1])

class Policy:
    """Single frozen dispatch admission, with all identical replays forwarded to Go."""
    def __init__(self, contract):
        self.frozen = canonical(contract['dispatch'])
    def check(self, message):
        if message.get('method') == 'tools/call' and message.get('params', {}).get('name') == 'dispatch':
            if canonical(message['params'].get('arguments')) != self.frozen:
                raise Refused('dispatch_differs_from_frozen_contract')


def python_runtime(python):
    return subprocess.check_output([str(python),'-I','-S','-c','import sys;print(sys.base_prefix)'],env={'PATH':'/usr/bin:/bin'},timeout=5).decode().strip()


def seatbelt_policy(python,runtime,candidate,scratch):
    policy = '''(version 1)
(allow default)
(deny file-read*)
(deny file-write*)
(deny network*)
(deny process-fork)
(deny process-exec)
(deny process-info*)
(deny sysctl-read)
(deny mach-lookup)
'''
    for path in ('/System', '/usr/lib', runtime, candidate, scratch):
        policy += '(allow file-read* (subpath ' + json.dumps(str(path)) + '))\n'
    for path in (python, '/dev/null', '/dev/random', '/dev/urandom'):
        policy += '(allow file-read* (literal ' + json.dumps(str(path)) + '))\n'
    # Candidate files and frozen check cannot be written, including by ctypes.
    policy += '(allow file-write* (literal "/dev/null"))\n'
    return policy


def sandbox_candidate(candidate, python, scratch_parent, timeout=15, cpu=10):
    if sys.platform != 'darwin' or not Path('/usr/bin/sandbox-exec').is_file():
        raise Refused('validation_sandbox_unavailable')
    candidate, python = Path(candidate).resolve(), Path(python).resolve()
    # Trusted process resolves the interpreter's stdlib before candidate execution.
    runtime=python_runtime(python)
    with tempfile.TemporaryDirectory(prefix='validation-', dir=scratch_parent) as temporary:
        scratch = Path(temporary).resolve()
        os.chmod(scratch, 0o700)
        test = 'CANDIDATE = ' + repr(str(candidate)) + '\n' + CHECK
        (scratch/'check.py').write_text(test)
        policy=seatbelt_policy(python,runtime,candidate,scratch)
        (scratch/'policy.sb').write_text(policy)
        command = [str(python), '-I', '-S', '-B', '-c', SEATBELT_BOOTSTRAP, str(scratch/'policy.sb'), str(scratch/'check.py')]
        def limits():
            resource.setrlimit(resource.RLIMIT_CPU, (cpu, cpu))
            resource.setrlimit(resource.RLIMIT_FSIZE, (1024*1024, 1024*1024))
            resource.setrlimit(resource.RLIMIT_NOFILE, (64,64))
        with (scratch/'stdout').open('wb') as out, (scratch/'stderr').open('wb') as err:
            try:
                result = subprocess.run(command, cwd=candidate, stdin=subprocess.DEVNULL, stdout=out, stderr=err,
                                        env={'PATH':'/usr/bin:/bin','LANG':'C','HOME':str(scratch),'TMPDIR':str(scratch)},
                                        timeout=timeout, preexec_fn=limits)
                code = result.returncode
            except subprocess.TimeoutExpired:
                code = 124
        stdout, stderr = (scratch/'stdout').read_bytes(), (scratch/'stderr').read_bytes()
        return {'sandbox':'macos-seatbelt', 'command':command,'candidate_directory':str(candidate),'validation_directory':str(scratch),
                'runtime_directory':runtime,'policy':policy,'policy_sha256':sha(policy.encode()),'test_script_sha256':sha(test.encode()), 'executable_sha256':file_sha(python),
                'frozen_test_sha256':sha(CHECK.encode()), 'exit_code':code,
                'stdout':stdout[:65536].decode(errors='replace'), 'stderr':stderr[:65536].decode(errors='replace'),
                'stdout_sha256':sha(stdout), 'stderr_sha256':sha(stderr),
                'stdout_base64':base64.b64encode(stdout).decode(),'stderr_base64':base64.b64encode(stderr).decode(),
                'stdout_bytes':len(stdout), 'stderr_bytes':len(stderr), 'cpu_limit_seconds':cpu,'wall_limit_seconds':timeout}


def verify_evidence(contract, handle, summary, pages, observed_oid):
    if Path(handle['state_dir']).resolve() != Path(contract['state_dir']).resolve():
        raise Refused('handle_state_scope')
    if summary['run_id'] != handle['run_id'] or len(handle['task_ids']) != 1:
        raise Refused('run_scope')
    task = next(t for t in summary['tasks'] if t['task_id'] == handle['task_ids'][0])
    binding = task['reviewable_result']
    if binding['candidate_oid'] != observed_oid or task['active_segments'] != 0:
        raise Refused('candidate_oid_or_active_mismatch')
    matches = [e for page in pages for e in page['events'] if e['event_id'] == binding['event_id']]
    if len(matches) != 1:
        raise Refused('original_event_missing')
    event = matches[0]
    for field, source in [('event_revision','event_revision'),('event_hash','payload_hash'),('action_slot','action_slot'),
                          ('work_revision','work_revision'),('segment_id','segment_id')]:
        if binding.get(field) != event.get(source):
            raise Refused('review_tuple_mismatch_' + field)
    if event['run_id'] != handle['run_id'] or event['task_id'] != task['task_id'] or event['kind'] != 'result':
        raise Refused('event_scope')
    artifact = event['artifact']
    path = Path(artifact['path'])
    if not path.is_absolute() or path.is_symlink() or not path.resolve().is_relative_to(Path(contract['state_dir']).resolve()):
        raise Refused('artifact_scope')
    if not 0 < artifact['size'] <= 1024*1024 or path.stat().st_size != artifact['size']:
        raise Refused('artifact_size')
    body = private_read(path,limit=1024*1024)[0]
    if sha(body) != artifact['sha256'] or sha(body) != event['payload_hash']:
        raise Refused('artifact_sha_mismatch')
    candidate = json.loads(body)['candidate']
    source = candidate.get('binding', {})
    for key in ('run_id','task_id','segment_id','work_revision','attempt_id'):
        if source.get(key) != event.get(key) or source.get(key) is None:
            raise Refused('candidate_binding_mismatch_' + key)
    repo = Path(contract['dispatch']['directory']).resolve()
    if Path(candidate['repo_root']).resolve() != repo or candidate['candidate_oid'] != observed_oid:
        raise Refused('candidate_repo_or_oid')
    if git(repo, 'rev-parse', observed_oid+'^{commit}') != observed_oid:
        raise Refused('candidate_not_commit')
    if git(repo, 'rev-parse', observed_oid+'^{tree}') != candidate['tree_oid']:
        raise Refused('tree_oid_mismatch')
    verify_lineage(contract,handle,event,candidate)
    changed = git(repo, 'diff', '--name-only', contract['base_oid'], observed_oid).splitlines()
    revision_changed=git(repo,'diff','--name-only',candidate['base_oid'],observed_oid).splitlines()
    if set(changed) != set(PATHS) or not set(revision_changed)<=set(PATHS):
        raise Refused('candidate_paths_mismatch')
    # Reject links/submodules and unexpected files before materializing candidate.
    entries = git(repo, 'ls-tree', '-r', observed_oid).splitlines()
    if {entry.split('\t',1)[1] for entry in entries}!=set(PATHS):raise Refused('candidate_files_missing_or_extra')
    if any(not e.startswith('100644 blob ') for e in entries):
        raise Refused('non_regular_candidate_file')
    return candidate, binding, event


def verify_lineage(contract,handle,event,candidate):
    repo=contract['dispatch']['directory'];revision=event['work_revision']
    lineage={'frozen_base_oid':contract['base_oid'],'revision_base_oid':candidate['base_oid'],'work_revision':revision}
    if revision==1:
        if candidate['base_oid']!=contract['base_oid']:raise Refused('base_oid_mismatch')
    elif revision==2:
        dbpath=Path(contract['state_dir'])/'state.db'
        info=dbpath.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_uid!=os.getuid() or stat.S_IMODE(info.st_mode)!=0o600 or info.st_nlink!=1 or str(dbpath.resolve())!=str(dbpath):raise Refused('lineage_database_untrusted')
        with sqlite3.connect(dbpath.as_uri()+'?mode=ro',uri=True,timeout=2) as db:
            db.execute('PRAGMA query_only=ON');db.execute('BEGIN')
            rows=db.execute('SELECT e.body_json,e.event_revision,e.action_slot,r.source_event_id,r.source_event_revision,r.source_event_hash,r.source_segment_id,r.next_attempt_no,r.action_slot,r.command_id FROM retry_decisions r JOIN runtime_events e ON e.event_id=r.source_event_id AND e.task_id=r.task_id WHERE r.task_id=? AND r.work_revision=?',(event['task_id'],revision-1)).fetchall()
            attempt=db.execute('SELECT attempt_no FROM attempts WHERE id=? AND task_id=?',(event['attempt_id'],event['task_id'])).fetchone()
        if len(rows)!=1 or attempt!=(2,):raise Refused('missing_or_ambiguous_rework_lineage')
        row=rows[0];previous=json.loads(row[0]);previous['event_revision']=row[1];previous['action_slot']=row[2]
        if previous['run_id']!=handle['run_id'] or previous['task_id']!=event['task_id'] or previous['work_revision']!=revision-1 or previous['kind']!='result' or (previous['event_id'],previous['event_revision'],previous['payload_hash'],previous['segment_id'],attempt[0])!=tuple(row[3:8]) or previous['action_slot']!=row[8]:raise Refused('rework_source_tuple_mismatch')
        artifact=previous['artifact'];path=Path(artifact['path'])
        if not path.is_relative_to(Path(contract['state_dir'])):raise Refused('rework_artifact_scope')
        body=private_read(path,limit=1024*1024)[0]
        if len(body)!=artifact['size'] or sha(body)!=artifact['sha256'] or sha(body)!=previous['payload_hash']:raise Refused('rework_artifact_hash')
        prior=json.loads(body)['candidate']
        for field in ('run_id','task_id','attempt_id','segment_id','work_revision'):
            if prior['binding'].get(field)!=previous[field]:raise Refused('rework_artifact_binding')
        if prior['repo_root']!=repo or prior['base_oid']!=contract['base_oid'] or prior['candidate_oid']!=candidate['base_oid'] or git(repo,'rev-parse',prior['candidate_oid']+'^{tree}')!=prior['tree_oid']:raise Refused('rework_previous_candidate_mismatch')
        lineage.update(predecessor_event=previous,predecessor_candidate_oid=prior['candidate_oid'],next_attempt_no=attempt[0],retry_command_id=row[9])
    else:raise Refused('unsupported_work_revision')
    git(repo,'merge-base','--is-ancestor',candidate['base_oid'],candidate['candidate_oid'])
    return lineage


def file_identity(info):
    return {'device':info.st_dev,'inode':info.st_ino,'uid':info.st_uid,'mode':stat.S_IMODE(info.st_mode),'links':info.st_nlink}


def canonical_ancestors(path):
    path=Path(path)
    if not path.is_absolute() or str(path.resolve())!=str(path):raise Refused('noncanonical_private_path')
    result=[]
    for parent in [path,*path.parents]:
        info=parent.lstat()
        if not stat.S_ISDIR(info.st_mode) or stat.S_ISLNK(info.st_mode):raise Refused('private_ancestor_not_directory')
        result.append({'path':str(parent),'device':info.st_dev,'inode':info.st_ino,'uid':info.st_uid,'mode':stat.S_IMODE(info.st_mode)})
    return result


def private_read(path,limit=4*1024*1024):
    path=Path(path);canonical_ancestors(path.parent)
    fd=os.open(path,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
    try:
        before=os.fstat(fd)
        if not stat.S_ISREG(before.st_mode) or before.st_uid!=os.getuid() or stat.S_IMODE(before.st_mode)!=0o600 or before.st_nlink!=1 or not 0<before.st_size<=limit:
            raise Refused('untrusted_private_file')
        data=b''
        while len(data)<=limit:
            part=os.read(fd,min(65536,limit+1-len(data)))
            if not part:break
            data+=part
        after=os.fstat(fd);named=path.lstat()
        if file_identity(before)!=file_identity(after) or file_identity(before)!=file_identity(named) or before.st_mtime_ns!=after.st_mtime_ns or before.st_size!=after.st_size or len(data)!=before.st_size:
            raise Refused('private_file_changed')
        return data,file_identity(before)
    finally:os.close(fd)


def private_claim(path):
    canonical_ancestors(Path(path).parent)
    try:fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600)
    except FileExistsError:raise Refused('private_canonical_path_preoccupied')
    os.fchmod(fd,0o600)
    return fd,file_identity(os.fstat(fd))


def private_publish(fd,path,value,identity):
    try:
        body=canonical(value).encode()+b'\n';position=0
        while position<len(body):position+=os.write(fd,body[position:])
        os.fsync(fd)
        if file_identity(os.fstat(fd))!=identity or file_identity(Path(path).lstat())!=identity:
            raise Refused('private_publication_replaced')
    finally:os.close(fd)


def validation_session(contract,create=False):
    scratch=Path(contract['scratch_dir'])
    ancestors=canonical_ancestors(scratch)
    for parent in (scratch,scratch.parent):
        info=parent.stat()
        if info.st_uid!=os.getuid() or stat.S_IMODE(info.st_mode)!=0o700:raise Refused('private_scratch_permissions')
    path=scratch/'validation-session.json'
    if create:
        fd,identity=private_claim(path)
        session={'version':1,'session_id':secrets.token_hex(32),'key':secrets.token_hex(32),
                 'contract_sha256':sha(canonical(contract).encode()),'ancestors':ancestors,'file_identity':identity}
        private_publish(fd,path,session,identity)
    data,identity=private_read(path)
    session=json.loads(data)
    if session['version']!=1 or session['file_identity']!=identity or session['ancestors']!=ancestors or session['contract_sha256']!=sha(canonical(contract).encode()) or len(bytes.fromhex(session['key']))!=32:
        raise Refused('validation_session_mismatch')
    return session


def validation_receipt_path(contract,binding):
    return Path(contract['scratch_dir'])/('validation-'+sha(canonical(binding).encode())+'.json')


def source_blobs(repo,oid):
    inventory=[]
    for entry in git(repo,'ls-tree','-r',oid).splitlines():
        meta,name=entry.split('\t',1);mode,kind,blob=meta.split()
        if name not in PATHS or mode!='100644' or kind!='blob':raise Refused('source_blob_scope')
        body=subprocess.check_output(['/usr/bin/git','-C',str(repo),'cat-file','blob',blob],timeout=5)
        inventory.append({'path':name,'blob_oid':blob,'sha256':sha(body),'size':len(body)})
    if {item['path'] for item in inventory}!=set(PATHS):raise Refused('source_blob_inventory')
    return inventory


def receipt_signature(session,body):
    return hmac.new(bytes.fromhex(session['key']),canonical(body).encode(),hashlib.sha256).hexdigest()


def verify_validation_receipt(contract,cli_handle,handle,binding,event):
    session=validation_session(contract)
    path=validation_receipt_path(contract,binding)
    raw,identity=private_read(path)
    evidence=json.loads(raw);signature=evidence.pop('signature',None)
    if not isinstance(signature,str) or not hmac.compare_digest(signature,receipt_signature(session,evidence)):
        raise Refused('validation_receipt_signature')
    if evidence['receipt_file_identity']!=identity or evidence['session_id']!=session['session_id'] or evidence['contract_sha256']!=session['contract_sha256']:
        raise Refused('validation_receipt_identity')
    if evidence['evidence_level']!='parent-level-frozen-candidate-fixture':raise Refused('validation_receipt_scope')
    if evidence['contract']!=contract or evidence['review_tuple']!=binding or evidence['original_event']!=event or evidence['cli_handle_sha256']!=file_sha(cli_handle):
        raise Refused('validation_receipt_source_binding')
    candidate,_,_=verify_evidence(contract,handle,{'run_id':handle['run_id'],'tasks':[{'task_id':handle['task_ids'][0],'active_segments':0,'reviewable_result':binding}]},[{'events':[event]}],binding['candidate_oid'])
    repo=contract['dispatch']['directory'];blobs=source_blobs(repo,binding['candidate_oid'])
    if evidence['candidate_oid']!=candidate['candidate_oid'] or evidence['tree_oid']!=candidate['tree_oid'] or evidence['source_blobs']!=blobs or evidence['source_blob_digest']!=sha(canonical(blobs).encode()):
        raise Refused('validation_receipt_source_blobs')
    expected={'head':contract['base_oid'],'status':''}
    if evidence['workspace_before']!=expected or evidence['workspace_after']!=expected or snapshot(repo)!=expected:
        raise Refused('validation_receipt_workspace')
    if evidence['lineage']!=verify_lineage(contract,handle,event,candidate) or evidence['frozen_base_oid']!=contract['base_oid'] or evidence['revision_base_oid']!=candidate['base_oid']:
        raise Refused('validation_receipt_lineage')
    if evidence['diff']!=git(repo,'diff','--no-ext-diff','--no-textconv',candidate['base_oid'],candidate['candidate_oid']) or evidence['frozen_diff']!=git(repo,'diff','--no-ext-diff','--no-textconv',contract['base_oid'],candidate['candidate_oid']):raise Refused('validation_receipt_diff')
    value=evidence['validation'];python=contract['binding']['python']['path']
    candidate_dir=Path(value['candidate_directory']);validation_dir=Path(value['validation_directory'])
    if candidate_dir.parent!=Path(contract['scratch_dir']) or validation_dir.parent!=Path(contract['scratch_dir']) or candidate_dir==validation_dir:
        raise Refused('validation_receipt_workdirs')
    command=[python,'-I','-S','-B','-c',SEATBELT_BOOTSTRAP,str(validation_dir/'policy.sb'),str(validation_dir/'check.py')]
    if value['sandbox']!='macos-seatbelt' or value['command']!=command or value['executable_sha256']!=contract['binding']['python']['sha256'] or file_sha(python)!=value['executable_sha256'] or value['frozen_test_sha256']!=sha(CHECK.encode()) or value['test_script_sha256']!=sha(('CANDIDATE = '+repr(str(candidate_dir))+'\n'+CHECK).encode()):
        raise Refused('validation_receipt_execution')
    if value['exit_code']!=0 or value['cpu_limit_seconds']!=10 or value['wall_limit_seconds']!=15 or not evidence['validation_started_unix']<=evidence['validation_finished_unix']<=time.time():
        raise Refused('validation_receipt_result')
    for stream in ('stdout','stderr'):
        data=base64.b64decode(value[stream+'_base64'],validate=True)
        if len(data)>1024*1024 or len(data)!=value[stream+'_bytes'] or sha(data)!=value[stream+'_sha256'] or value[stream]!=data[:65536].decode(errors='replace'):
            raise Refused('validation_receipt_logs')
    if not base64.b64decode(value['stdout_base64'],validate=True).endswith(b'frozen acceptance assertions completed\n'):
        raise Refused('validation_receipt_checks_not_completed')
    # Source-authenticated policy must retain every OS denial and grant neither signer nor its parent.
    policy=value['policy']
    if value['runtime_directory']!=python_runtime(python) or policy!=seatbelt_policy(python,value['runtime_directory'],candidate_dir,validation_dir) or value['policy_sha256']!=sha(policy.encode()) or '(subpath '+json.dumps(contract['scratch_dir'])+')' in policy:
        raise Refused('validation_receipt_policy')
    for rule in ('file-read*','file-write*','network*','process-fork','process-exec','process-info*','sysctl-read','mach-lookup'):
        if '(deny '+rule+')' not in policy:raise Refused('validation_receipt_policy_denial')
    evidence['signature']=signature
    return evidence,path,sha(raw)


def validate_candidate(contract, cli_handle, observed_oid, allowed_handle):
    require_binding(contract)
    if not allowed_handle or cli_handle != allowed_handle:
        raise Refused('unobserved_dispatch_handle')
    path = Path(cli_handle)
    if path.is_symlink() or not path.resolve().is_relative_to(Path(contract['state_dir']).resolve()):
        raise Refused('handle_scope')
    handle = json.loads(private_read(path)[0])
    if contract.get('frozen_test_sha256') != sha(CHECK.encode()):
        raise Refused('frozen_tests_changed')
    wrapper = contract['binding']['wrapper']['path']
    def public(action, *extra):
        result = subprocess.run([wrapper, 'task', action, '--handle', cli_handle, *extra], stdout=subprocess.PIPE,
                                stderr=subprocess.PIPE, timeout=15, check=True)
        if len(result.stdout)>2*1024*1024:
            raise Refused('public_output_too_large')
        return json.loads(result.stdout)
    summary = public('inspect')
    pages, cursor = [], ''
    for _ in range(16):
        page = public('collect', *(['--cursor',cursor] if cursor else []))
        pages.append(page)
        cursor = page.get('next_cursor','')
        if not cursor: break
    else: raise Refused('collect_page_limit')
    candidate, binding, event = verify_evidence(contract, handle, summary, pages, observed_oid)
    repo = Path(contract['dispatch']['directory'])
    session=validation_session(contract)
    receipt_path=validation_receipt_path(contract,binding)
    receipt_fd,receipt_identity=private_claim(receipt_path)
    try:
        started=time.time()
        before = snapshot(repo)
        if before['head'] != contract['base_oid'] or before['status']:
            raise Refused('original_workspace_changed')
        # Detached validation copy: export exact Git blobs; never execute worktree hooks or candidate setup.
        # Avoid worktree registration changes in the repository under observation.
        with tempfile.TemporaryDirectory(prefix='candidate-', dir=contract['scratch_dir']) as temporary:
            detached = Path(temporary)
            for entry in git(repo,'ls-tree','-r',observed_oid).splitlines():
                meta, name = entry.split('\t',1)
                if name not in PATHS: raise Refused('unexpected_candidate_file')
                target = detached/name
                target.parent.mkdir(parents=True,exist_ok=True)
                target.write_bytes(subprocess.check_output(['/usr/bin/git','-C',str(repo),'cat-file','blob',meta.split()[2]],timeout=5))
            validation = sandbox_candidate(detached, contract['binding']['python']['path'], contract['scratch_dir'])
        latest = public('inspect')
        latest_task = next(t for t in latest['tasks'] if t['task_id'] == handle['task_ids'][0])
        if latest_task.get('reviewable_result') != binding or latest_task['active_segments'] != 0:
            raise Refused('review_changed_during_validation')
        after = snapshot(repo)
        if before != after: raise Refused('original_workspace_mutated')
        blobs=source_blobs(repo,observed_oid)
        result={'evidence_level':'parent-level-frozen-candidate-fixture', 'contract':contract, 'review_tuple':binding,
                'original_event':event, 'candidate_oid':observed_oid,'tree_oid':candidate['tree_oid'],
                'materialization':'safe detached copy from exact Git blob OIDs; no index/ref/worktree registration writes',
                'diff':git(repo,'diff','--no-ext-diff','--no-textconv',candidate['base_oid'],observed_oid),
                'validation':validation,'workspace_before':before,'workspace_after':after,
                'decision':'PARENT_MUST_REVIEW; no accept, ACK or integration performed',
                'cli_handle_sha256':file_sha(cli_handle),'session_id':session['session_id'],'contract_sha256':session['contract_sha256'],
                'receipt_file_identity':receipt_identity,'frozen_base_oid':contract['base_oid'],'revision_base_oid':candidate['base_oid'],
                'lineage':verify_lineage(contract,handle,event,candidate),'frozen_diff':git(repo,'diff','--no-ext-diff','--no-textconv',contract['base_oid'],observed_oid),'source_blobs':blobs,'source_blob_digest':sha(canonical(blobs).encode()),
                'validation_started_unix':started,'validation_finished_unix':time.time()}
        result['signature']=receipt_signature(session,result)
        private_publish(receipt_fd,receipt_path,result,receipt_identity)
        result['validation_evidence_path']=str(receipt_path);result['validation_evidence_sha256']=file_sha(receipt_path)
        return result

    finally:
        try:os.close(receipt_fd)
        except OSError:pass


def prepare(root, package=None, wrapper=None, python=None):
    root = Path(root).resolve()
    root.mkdir(mode=0o700,parents=True,exist_ok=False)
    os.chmod(root,0o700)
    binding = {}
    for key, path in [('package',package),('wrapper',wrapper),('python',python)]:
        if path: binding[key] = {'path':str(Path(path).resolve()),'sha256':file_sha(path)}
    for direction, provider, model, reasoning in [('codex-to-claude','claude','cli-default',''),('claude-to-codex','codex','gpt-6-luna','low')]:
        area = root/direction
        area.mkdir(mode=0o700)
        repo = area/'repo'
        repo.mkdir(mode=0o700)
        git(repo,'init','-q')
        git(repo,'config','user.name','T12 Fixture')
        git(repo,'config','user.email','t12@example.invalid')
        (repo/'calc.py').write_text('def add(a, b):\n    raise NotImplementedError\n')
        git(repo,'add','calc.py')
        git(repo,'commit','-qm','fixture baseline')
        for name in ('state','scratch'): (area/name).mkdir(mode=0o700)
        dispatch = {'request_id':'t12-'+direction, 'provider':provider,'role':'implementer','directory':str(repo),
                    'prompt':BRIEF,'paths':PATHS,'acceptance':ACCEPTANCE,'model':model,
                    'budget':{'per_segment_ms':60000,'total_ms':90000,'max_attempts':2,'group_id':'t12-'+direction}}
        if reasoning: dispatch['reasoning']=reasoning
        contract={'version':1,'status':'PREPARED_NOT_LIVE','wall_ms':180000,'binding':binding,
                  'state_dir':str(area/'state'),'scratch_dir':str(area/'scratch'),'base_oid':git(repo,'rev-parse','HEAD'),
                  'dispatch':dispatch, 'frozen_test_sha256':sha(CHECK.encode())}
        private_json(area/'contract.json',contract)
    return str(root)


def proxy(contract, backend, execute_live):
    if not execute_live: raise Refused('execute_live_required')
    require_binding(contract)
    expected_backend = [contract['binding']['wrapper']['path'], 'mcp', 'serve', '--config', contract['binding']['mcp_config']['path']]
    if backend != expected_backend:
        raise Refused('backend_must_be_bound_installed_wrapper_mcp_serve')
    policy = Policy(contract)
    validation_session(contract,create=True)
    child = subprocess.Popen(backend, stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=sys.stderr)
    lock = threading.Lock()
    pending = set()
    allowed = {'handle':None}
    def send(value):
        with lock:
            sys.stdout.write(canonical(value)+'\n');sys.stdout.flush()
    def responses():
        while True:
            line = child.stdout.readline(1024*1024+1)
            if not line: break
            if len(line)>1024*1024:
                child.terminate();break
            message = json.loads(line)
            if message.get('id') in pending:
                result = message.get('result',{})
                structured = result.get('structuredContent',{})
                if not structured:
                    for part in result.get('content',[]):
                        if part.get('type')=='text':
                            try: structured=json.loads(part['text'])
                            except (ValueError,KeyError): pass
                if structured.get('cli_handle'):
                    allowed['handle']=structured['cli_handle']
                    receipt={'cli_handle':allowed['handle'],'dispatch_sha256':sha(canonical(contract['dispatch']).encode()),'wrapper_sha256':contract['binding']['wrapper']['sha256']}
                    receipt_path=Path(contract['scratch_dir'])/'observed-dispatch.json'
                    if receipt_path.exists():
                        if json.loads(receipt_path.read_text())!=receipt: raise Refused('dispatch_observation_conflict')
                    else: private_json(receipt_path,receipt)
                pending.discard(message['id'])
            if isinstance(message.get('result',{}).get('tools'),list):
                message['result']['tools'].append({'name':'fixture_validate_candidate','description':'T12 harness-only frozen candidate test; returns evidence, never decides.',
                 'inputSchema':{'type':'object','properties':{'cli_handle':{'type':'string'},'candidate_oid':{'type':'string'}},'required':['cli_handle','candidate_oid'],'additionalProperties':False}})
            send(message)
    thread=threading.Thread(target=responses,daemon=True);thread.start()
    def input_lines():
        deadline = time.monotonic() + contract['wall_ms']/1000
        data = b''
        while time.monotonic() < deadline:
            ready, _, _ = select.select([sys.stdin.fileno()], [], [], min(0.1, max(0,deadline-time.monotonic())))
            if not ready:
                if child.poll() is not None: return
                continue
            chunk = os.read(sys.stdin.fileno(),65536)
            if not chunk: return
            data += chunk
            if len(data)>1024*1024 and b'\n' not in data: raise Refused('message_too_large')
            while b'\n' in data:
                line,data=data.split(b'\n',1)
                if len(line)>1024*1024: raise Refused('message_too_large')
                yield line+b'\n'
        raise Refused('scenario_wall_budget_exhausted')
    try:
        for line in input_lines():
            message=json.loads(line)
            try:
                policy.check(message)
                params=message.get('params',{})
                if message.get('method')=='tools/call' and params.get('name')=='fixture_validate_candidate':
                    args=params['arguments']
                    if set(args)!= {'cli_handle','candidate_oid'}: raise Refused('invalid_fixture_arguments')
                    evidence=validate_candidate(contract,args['cli_handle'],args['candidate_oid'],allowed['handle'])
                    send({'jsonrpc':'2.0','id':message['id'],'result':{'content':[{'type':'text','text':canonical(evidence)}]}})
                    continue
                if message.get('method')=='tools/call' and params.get('name')=='dispatch': pending.add(message['id'])
                child.stdin.write(line);child.stdin.flush()
            except (Refused,KeyError,ValueError,subprocess.SubprocessError) as err:
                send({'jsonrpc':'2.0','id':message.get('id'),'error':{'code':-32602,'message':str(err)}})
    finally:
        child.stdin.close()
        try: child.wait(timeout=5)
        except subprocess.TimeoutExpired:
            child.terminate()
            try: child.wait(timeout=2)
            except subprocess.TimeoutExpired: child.kill();child.wait()
        thread.join(timeout=1)

def main():
    parser=argparse.ArgumentParser()
    parser.add_argument('action',choices=['prepare','bind','proxy'])
    parser.add_argument('--root')
    parser.add_argument('--package');parser.add_argument('--wrapper');parser.add_argument('--python')
    parser.add_argument('--contract');parser.add_argument('--execute-live',action='store_true')
    parser.add_argument('--mcp-config');parser.add_argument('--output')
    args, backend = parser.parse_known_args()
    if args.action=='prepare':
        if backend: raise Refused('unexpected_prepare_arguments')
        print(prepare(args.root,args.package,args.wrapper,args.python))
    elif args.action=='bind':
        if backend: raise Refused('unexpected_bind_arguments')
        contract=json.loads(Path(args.contract).read_text())
        for key,path in [('package',args.package),('wrapper',args.wrapper),('python',args.python),('mcp_config',args.mcp_config)]:
            if not path: raise Refused('missing_binding_'+key)
            path=str(Path(path).resolve())
            contract['binding'][key]={'path':path,'sha256':file_sha(path)}
        sidecar=Path(contract['binding']['mcp_config']['path']).with_name('codex-args.json')
        if not sidecar.is_file(): raise Refused('missing_binding_codex_args')
        contract['binding']['codex_args']={'path':str(sidecar),'sha256':file_sha(sidecar)}
        binary=Path(contract['binding']['wrapper']['path']).parent.parent/'bin/codex-orchestrator'
        if not binary.is_file(): raise Refused('missing_binding_installed_binary')
        contract['binding']['installed_binary']={'path':str(binary),'sha256':file_sha(binary)}
        require_binding(contract)
        private_json(args.output,contract)
        print(args.output)
    else: proxy(json.loads(Path(args.contract).read_text()),backend[1:] if backend[:1]==['--'] else backend,args.execute_live)

if __name__=='__main__':
    try: main()
    except Refused as err: raise SystemExit(str(err))
