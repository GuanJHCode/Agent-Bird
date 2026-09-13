"""Root-gated private ActivationService + Python owner/helper + real Go adapter.

Preparation builds the real Go CLI. run_delivery is explicit and never called
on import. Native/TUI/global registration are not inferred from dummy tests.
"""
from __future__ import annotations
import asyncio
from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import socket
import stat
import subprocess
import sys

ROOT=Path(__file__).resolve().parents[3]
for path in (ROOT/'tasks/g0-completion/scripts',ROOT/'tasks/g0-proxy-continuation/cold-start/scripts',ROOT/'tasks/g0-tui-proxy/scripts',ROOT/'tasks/g0-auth-preserving-activation/scripts'):
    sys.path.insert(0,str(path))
import activation_service
import auth_isolation
import delivery_adapter
import owner_helper
import proxy_observer
import proxy_transport
import owned_child_guard
import synthetic_native_transport as transport
import synthetic_native_fixture
import synthetic_responses

CLIENT=Path(__file__).with_name('native_delivery_client.py')
GO_CACHE=Path('/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-completion/tmp/main-verify/cache')
GO_MOD_CACHE=GO_CACHE.parent/'modcache'


def require(value,reason):transport.require(value,reason)
def sha(path):return auth_isolation._hash_stable_file(Path(path))
def write(path,value):auth_isolation.write_private_manifest(value,Path(path))


def source_pins():
    paths=[Path(__file__).resolve(),CLIENT,Path(sys.executable).resolve()]
    paths += [Path(module.__file__).resolve() for module in (activation_service,auth_isolation,delivery_adapter,owner_helper,
        proxy_observer,proxy_transport,owned_child_guard,transport,synthetic_native_fixture,synthetic_responses)]
    paths += [ROOT/'tasks/g0-pending-resolution/scripts/delivery_audit.py',transport.NOTIFICATION_SOURCE]
    paths += list((ROOT/'tools/g0-return-lab').glob('*.go'))+[ROOT/'tools/g0-return-lab/go.mod']
    return {str(path):sha(path) for path in paths}


def peer_for_process(pid):
    birth,executable=proxy_transport._process_metadata(pid)
    require(birth and executable,'process identity unavailable')
    return {'pid':pid,'uid':os.getuid(),'birth':birth,'executable':executable,'executable_sha256':sha(executable)}


def _go(binary,args,env,timeout=10):
    completed=subprocess.run([str(binary),*args],env=env,capture_output=True,timeout=timeout,check=False)
    require(completed.returncode==0,'Go command rejected')
    value=json.loads(completed.stdout);require(isinstance(value,dict),'Go output shape');return value


@dataclass
class DeliveryPlan:
    case_dir:Path
    case_identity:tuple
    manifest_path:Path
    manifest_sha256:str
    prepared_pins:dict
    native_identity:tuple
    python:Path
    python_identity:tuple
    go_binary:Path
    go_sha256:str
    plan_sha256:str
    listener:socket.socket
    public_identity:tuple
    proxy_peer:dict


def prepare_delivery(prepared,case_dir,*,python_executable,go_executable,expected_native_sha256,expected_source_pins):
    require(expected_source_pins and expected_source_pins==source_pins(),'source pins missing or changed')
    prepared.verify();require(not prepared.endpoint._started,'synthetic endpoint already started')
    native=prepared.context.expected_executable
    native_identity=transport._native_check(native,expected_native_sha256)
    python=Path(python_executable);go=Path(go_executable)
    for executable in (python,go):
        require(executable.is_absolute() and executable.resolve()==executable,'tool must be canonical')
        info=executable.stat();require(stat.S_ISREG(info.st_mode) and info.st_uid in (0,os.getuid()) and info.st_mode&0o111 and not info.st_mode&0o022,'unsafe tool executable')
    python_identity=(python.stat().st_dev,python.stat().st_ino,python.stat().st_mode,python.stat().st_uid)
    public=prepared.spec.public_socket
    require(public.parent==prepared.spec.task_root and not os.path.lexists(public),'public endpoint must be new task-private socket')
    case=Path(case_dir);transport._dir_identity(case.parent);case.mkdir(mode=0o700)
    for name in ('state','grants','bin'): (case/name).mkdir(mode=0o700)
    identity=transport._dir_identity(case)
    build_env=auth_isolation.build_clean_environment(prepared.spec,{})
    build_env.update(GOENV='off',GOPROXY='off',GOSUMDB='off',GOTOOLCHAIN='local',
        GOCACHE=str(GO_CACHE),GOMODCACHE=str(GO_MOD_CACHE))
    binary=case/'bin/g0-return-lab'
    built=subprocess.run([str(go),'build','-buildvcs=false','-trimpath','-o',str(binary),'.'],cwd=ROOT/'tools/g0-return-lab',env=build_env,capture_output=True,timeout=10,check=False)
    require(built.returncode==0,'Go build failed')
    binary_hash=sha(binary)
    pins={**expected_source_pins,str(python):sha(python),str(go):sha(go),str(binary):binary_hash}
    manifest={'version':1,'public_socket':str(public),'state_dir':str(case/'state'),'grants_dir':str(case/'grants'),
        'isolation_manifest':prepared.manifest['isolation_manifest'],'isolation_manifest_sha256':sha(prepared.manifest['isolation_manifest']),
        'backend_argv':[str(native),'app-server','--listen','unix://{socket_path}'],
        'backend_executable_sha256':expected_native_sha256,'file_pins':pins,'idle_seconds':10,
        'owner_helper':{'executable':str(python),'executable_sha256':sha(python),'source_path':str(Path(owner_helper.__file__).resolve()),'source_sha256':sha(owner_helper.__file__)}}
    manifest_path=case/'service.json';write(manifest_path,manifest)
    listener=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM)
    try:
        listener.bind(str(public));listener.listen(4);os.chmod(public,0o600)
        public_identity=transport._socket_identity(public);proxy_peer=peer_for_process(os.getpid())
        plan={'version':1,'synthetic_only':True,'ordinary_tui_verified':False,'durable_history_verified':False,
            'native_sha256':expected_native_sha256,'native_identity':native_identity,'source_pins':pins,
            'python_owner_argv':[str(python),'-I','-B',str(CLIENT),'owner'],
            'python_helper_argv':[str(python),'-I','-B',str(CLIENT),'helper'],
            'backend_argv':manifest['backend_argv'],'service_manifest_sha256':sha(manifest_path),
            'fixture_plan_sha256':sha(prepared.spec.task_root/'synthetic-plan.json'),
            'python_client_policy':'fixed-code-cleanenv-no-native-auth-manager','backend_policy':'auth-isolation-sandbox',
            'go_binary':str(binary),'go_sha256':binary_hash,'go_tool_sha256':sha(go),'build_cache':str(GO_CACHE),
            'build_stdout_sha256':hashlib.sha256(built.stdout).hexdigest(),'build_stderr_sha256':hashlib.sha256(built.stderr).hexdigest(),
            'proxy_peer':proxy_peer,'public_socket':str(public),'public_identity':public_identity,'total_seconds':120,'local_seconds':10}
        write(case/'plan.json',plan)
        return DeliveryPlan(case,identity,manifest_path,sha(manifest_path),dict(expected_source_pins),native_identity,
            python,python_identity,binary,binary_hash,sha(case/'plan.json'),listener,public_identity,proxy_peer)
    except BaseException:
        listener.close();raise


async def line(child,end):
    raw=await asyncio.wait_for(child.stdout.readline(),transport.remaining(end))
    require(raw and len(raw)<=65536,'client EOF or oversized receipt')
    value=json.loads(raw);require(isinstance(value,dict),'client receipt shape')
    if value.get('event')=='error':raise ValueError('client stage failed: '+str(value.get('stage'))+':'+str(value.get('type')))
    return value


async def send(child,value,end):
    child.stdin.write(transport.encoded(value)+b'\n')
    await asyncio.wait_for(child.stdin.drain(),transport.remaining(end));transport.remaining(end)


async def wait_until(predicate,end):
    while not predicate():transport.remaining(end);await asyncio.sleep(.01)


def client_config(plan,prepared,role,peer,deadline,local_deadline,grant):
    grant_path=plan.case_dir/'grants'/f"{peer['pid']}.json";write(grant_path,grant)
    return {'role':role,'manifest_path':str(plan.manifest_path),'manifest_sha256':plan.manifest_sha256,
        'grant_path':str(grant_path),'grant_sha256':sha(grant_path),'profile_id':prepared.spec.profile_id,
        'public_socket':str(prepared.spec.public_socket),'public_socket_identity':plan.public_identity,
        'proxy_peer':plan.proxy_peer,'codex_home':str(prepared.spec.codex_home),'workspace':str(prepared.spec.workspace),
        'deadline':deadline,'local_deadline':local_deadline}


async def stop_client(child,end):
    if child is None:return None
    if child.stdin is not None:child.stdin.close()
    if child.returncode is None:
        try:await asyncio.wait_for(child.wait(),min(1,transport.remaining(end)))
        except TimeoutError:
            child.terminate()
            try:await asyncio.wait_for(child.wait(),min(1,transport.remaining(end)))
            except TimeoutError:child.kill();await asyncio.wait_for(child.wait(),transport.remaining(end))
    return child.returncode


async def run_delivery(plan,prepared):
    require(transport._dir_identity(plan.case_dir)==plan.case_identity,'case identity changed')
    directory_fd=transport._open_case(plan.case_dir,plan.case_identity)
    try:transport._write(directory_fd,'attempt.json',{'start_raw':transport.now(),'automatic_retry':False})
    except BaseException:os.close(directory_fd);raise
    start=transport.now();deadline=start+120;startup=min(deadline,start+10)
    result={'version':1,'status':'unknown','python_owner_verified':False,'ordinary_tui_verified':False,
        'durable_history_verified':False,'business_ack_verified':False,'started_raw':start,'deadline_raw':deadline}
    service=None;owner=None;helper=None;owner_identity=None;helper_identity=None;stage='preflight';helper_eof=False
    stderr_tasks=[];go_env=auth_isolation.build_clean_environment(prepared.spec,{})
    task=prepared.spec.workspace/'go-task';nonce=plan.case_dir.name
    try:
        require(plan.prepared_pins==source_pins() and plan.manifest_sha256==sha(plan.manifest_path) and plan.plan_sha256==sha(plan.case_dir/'plan.json'),'frozen source or manifest changed')
        require(transport._native_check(prepared.context.expected_executable,prepared.context.expected_executable_sha256)==plan.native_identity,'native artifact changed')
        info=plan.python.stat();require((info.st_dev,info.st_ino,info.st_mode,info.st_uid)==plan.python_identity,'Python artifact changed')
        require(sha(plan.go_binary)==plan.go_sha256 and peer_for_process(os.getpid())==plan.proxy_peer,'Go or controller identity changed')
        prepared.verify();require(transport._socket_identity(prepared.spec.public_socket)==plan.public_identity,'public socket changed')
        transport.remaining(startup);prepared.start_endpoint()
        service=activation_service.ActivationService.from_manifest(plan.manifest_path)
        await service.start(socket.socket(fileno=os.dup(plan.listener.fileno())))
        transport.remaining(startup);stage='owner-spawn'
        env=auth_isolation.build_clean_environment(prepared.spec,{})
        owner=await asyncio.create_subprocess_exec(str(plan.python),'-I','-B',str(CLIENT),'owner',cwd=prepared.spec.workspace,
            env=env,stdin=asyncio.subprocess.PIPE,stdout=asyncio.subprocess.PIPE,stderr=asyncio.subprocess.PIPE)
        stderr_tasks.append(asyncio.create_task(transport._drain(owner.stderr)))
        require((await line(owner,startup)).get('event')=='boot','owner boot failed')
        owner_identity=peer_for_process(owner.pid);require(owner_identity['executable']==str(plan.python),'owner executable mismatch')
        grant={'version':1,'pid':owner.pid,'uid':os.getuid(),'birth':owner_identity['birth'],'expected_executable':str(plan.python),
            'executable_sha256':sha(plan.python),'profile_id':prepared.spec.profile_id}
        config=client_config(plan,prepared,'owner',owner_identity,deadline,startup,grant)
        await send(owner,config,startup);stage='owner-thread'
        ready=await line(owner,startup);require(ready.get('event')=='thread_ready' and ready.get('home_match') is True,'owner thread not ready')
        await wait_until(lambda:len(service._owner_leases)==1,startup)
        lease=next(iter(service._owner_leases.values()));record=service._record_for_lease(lease.lease_id)
        require(lease.owner_thread_id==ready['thread_id'] and record['frontend']['pid']==owner.pid,'service owner mapping mismatch')
        result['lease']={key:getattr(lease,key) for key in ('profile_id','owner_context_sha256','lease_id','owner_connection_id','owner_epoch','owner_thread_id','private_socket','backend_pid','backend_birth')}
        await send(owner,{'action':'initial_turn'},startup);stage='owner-initial'
        initial=await line(owner,deadline);require(initial.get('event')=='initial_ready' and initial['thread_id']==lease.owner_thread_id,'initial turn incomplete')
        result['owner']={**owner_identity,**initial};result['python_owner_verified']=True
        require(prepared.endpoint.snapshot()['accepted_requests']==1,'initial synthetic count mismatch')
        stage='go-task';task.mkdir(mode=0o700);go_env['CODEX_THREAD_ID']=lease.owner_thread_id
        job=await asyncio.to_thread(_go,plan.go_binary,['start','--dir',str(task),'--nonce',nonce,'--delay','500ms','--controller-thread',lease.owner_thread_id],go_env)
        result['go_job']=job
        completed=await asyncio.to_thread(_go,plan.go_binary,['wait','--dir',str(task),'--nonce',nonce,'--timeout','2s'],go_env)
        require(completed.get('status')=='completed' and completed.get('count')==1,'Go task incomplete')
        inspected=await asyncio.to_thread(_go,plan.go_binary,['inspect','--dir',str(task),'--nonce',nonce],go_env)
        require(inspected.get('controller_thread')==lease.owner_thread_id and inspected.get('event_hash'),'Go result owner mismatch')
        result['go_inspect']=inspected
        events=[{'event_id':'event_result','event_revision':1,'kind':'result','payload_hash':inspected['event_hash'],'action_slot':'ack_result'}]
        helper_start=min(deadline,transport.now()+10);stage='helper-spawn'
        helper=await asyncio.create_subprocess_exec(str(plan.python),'-I','-B',str(CLIENT),'helper',cwd=prepared.spec.workspace,
            env=go_env,stdin=asyncio.subprocess.PIPE,stdout=asyncio.subprocess.PIPE,stderr=asyncio.subprocess.PIPE)
        stderr_tasks.append(asyncio.create_task(transport._drain(helper.stderr)))
        require((await line(helper,helper_start)).get('event')=='boot','helper boot failed')
        helper_identity=peer_for_process(helper.pid);require(helper_identity['executable']==str(plan.python),'helper executable mismatch')
        result['helper']=helper_identity
        grant={'version':1,'role':'owner-helper','profile_id':lease.profile_id,'owner_context_sha256':lease.owner_context_sha256,
            'lease_id':lease.lease_id,'owner_connection_id':lease.owner_connection_id,'owner_epoch':lease.owner_epoch,'owner_thread_id':lease.owner_thread_id,
            'private_socket':lease.private_socket,'helper_pid':helper.pid,'helper_uid':os.getuid(),'helper_birth':helper_identity['birth'],
            'helper_executable':str(plan.python),'helper_executable_sha256':sha(plan.python),'helper_source_sha256':sha(owner_helper.__file__)}
        config=client_config(plan,prepared,'helper',helper_identity,deadline,helper_start,grant)
        config.update(thread_id=lease.owner_thread_id,initial_turn_id=initial['initial_turn_id'],lease_id=lease.lease_id,
            owner_epoch=lease.owner_epoch,go_binary=str(plan.go_binary),go_sha256=plan.go_sha256,task_dir=str(task),nonce=nonce,
            delivery_id='G0_SYNTHETIC_TOOL_RESULT_'+nonce,events=events)
        await send(helper,config,helper_start);stage='helper-proof'
        proof=await line(helper,deadline);require(proof.get('event')=='history_proof','helper history absent')
        require(proof['thread_id']==lease.owner_thread_id and proof['initial_turn_id']==initial['initial_turn_id'] and proof['lease_id']==lease.lease_id,'helper proof owner mismatch')
        require(proof['manifest_sha256']==plan.manifest_sha256 and proof['grant_sha256']==config['grant_sha256'],'helper source binding mismatch')
        require(all(proof['public_peer'][key]==plan.proxy_peer[key] for key in ('pid','uid','birth','executable')),'helper public peer is not controller')
        require(proof['matched_event_ids']==['event_result'] and proof['summary']['captured_page_chain_complete'],'helper event proof mismatch')
        result['helper_proof']=proof
        require(len(service.backend_records)==1 and service._owner_leases.get(lease.lease_id) is lease and record['state']=='connected','owner backend changed')
        require(len(record.get('helpers',[]))==1 and record['helpers'][0]['frontend']['pid']==helper.pid and record['helpers'][0]['state']=='connected','helper did not reuse owner backend')
        await send(owner,{'action':'status'},min(deadline,transport.now()+10))
        live=await line(owner,min(deadline,transport.now()+10));require(live.get('event')=='owner_live' and live['thread_id']==lease.owner_thread_id,'owner no longer live')
        require(prepared.endpoint.snapshot()['accepted_requests']==2,'delivery synthetic count mismatch')
        prepared.verify();require(plan.prepared_pins==source_pins(),'source changed before handled decision')
        decision={'action':'decide','reads_sha256':proof['reads_sha256'],'decisions':{'event_result':'handled'},'command_ids':{'event_result':'handled_'+nonce}}
        write(plan.case_dir/'controller-handled-decision.json',decision)
        stage='go-ack';await send(helper,decision,min(deadline,transport.now()+10))
        ack=await line(helper,min(deadline,transport.now()+10));require(ack.get('event')=='ack' and ack.get('business_ack_complete') is True,'business ACK incomplete')
        require(len(ack['records'])==1 and ack['records'][0].get('status')=='acknowledged' and ack['records'][0].get('effect_count')==1,'Go ACK effect mismatch')
        result['ack']=ack;result['business_ack_verified']=True
        result['go_status']=await asyncio.to_thread(_go,plan.go_binary,['batch-status','--dir',str(task),'--nonce',nonce],go_env)
        require(result['go_status'].get('business_status')=='complete' and result['go_status'].get('send_allowed') is False,'Go status not complete')
        stage='normal-close';close_end=min(deadline,transport.now()+10)
        await send(owner,{'action':'close'},close_end)
        final_owner=await line(owner,close_end);require(final_owner.get('event')=='owner_closed','owner close missing')
        result['owner']['envelopes']=final_owner['envelopes']
        eof=await line(helper,close_end);require(eof.get('event')=='helper_eof' and eof['lease_id']==lease.lease_id,'helper EOF missing');helper_eof=True
        await asyncio.wait_for(owner.wait(),transport.remaining(close_end));await asyncio.wait_for(helper.wait(),transport.remaining(close_end))
        require(owner.returncode==helper.returncode==0,'client exit failed')
        await wait_until(lambda:record.get('process_stopped') and record.get('socket_removed'),close_end)
        prepared.verify();require(plan.prepared_pins==source_pins(),'source changed after delivery')
        transport.remaining(deadline);result['status']='passed'
    except Exception as exc:
        result['failure']={'stage':stage,'type':type(exc).__name__,'errno':getattr(exc,'errno',None),'message_sha256':hashlib.sha256(str(exc).encode()).hexdigest()}
    finally:
        cleanup={'helper_eof':helper_eof,'public_socket_removed':False,'restart_allowed':False}
        end=min(deadline,transport.now()+10)
        for role,child in (('owner',owner),('helper',helper)):
            try:cleanup[role+'_exit']=await stop_client(child,end)
            except Exception:result['status']='unknown';cleanup[role+'_stop_unproven']=True
        if service is not None:
            # Owner EOF already starts the service's backend cleanup. Let
            # that owned finally finish before close() cancels relay tasks.
            try:await wait_until(lambda:all(row.get('process_stopped') and row.get('socket_removed') for row in service.backend_records),end)
            except Exception:result['status']='unknown';cleanup['backend_stop_unproven']=True
            try:await asyncio.wait_for(service.close(),max(.001,end-transport.now()))
            except Exception:result['status']='unknown';cleanup['service_close_unproven']=True
            result['service']=service.snapshot()
        else:result['service']={'backend_records':[]}
        plan.listener.close()
        try:
            require(transport._socket_identity(prepared.spec.public_socket)==plan.public_identity,'private public socket identity changed')
            prepared.spec.public_socket.unlink();cleanup['public_socket_removed']=True
        except FileNotFoundError:cleanup['public_socket_removed']=True
        except Exception:result['status']='unknown'
        result['synthetic_endpoint']=prepared.endpoint.snapshot()
        try:prepared.endpoint.close()
        except Exception:result['status']='unknown';cleanup['endpoint_close_unproven']=True
        try:result['client_output_digests']=await asyncio.wait_for(asyncio.gather(*stderr_tasks),max(.001,min(1,deadline-transport.now())))
        except Exception:cleanup['output_unproven']=True
        result['cleanup']=cleanup;result['finished_raw']=transport.now()
        if result['finished_raw']>=deadline:result['status']='unknown';result['deadline_exceeded']=True
        try:transport._write(directory_fd,'result.json',result)
        finally:os.close(directory_fd)
    return result
