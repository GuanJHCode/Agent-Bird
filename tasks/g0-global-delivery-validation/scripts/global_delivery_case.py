"""Root-invoked transaction + external socket-activated delivery experiment.

No registration or native launch on import/prepare. The existing transaction is
synchronous and bounded per phase; only root may invoke the real run once.
"""
from __future__ import annotations
import asyncio
from dataclasses import dataclass,asdict
import hashlib,json,os,pwd,re,signal,socket,stat,subprocess,sys
from pathlib import Path

ROOT=Path(__file__).resolve().parents[3]
for path in (ROOT/'tasks/g0-completion/scripts',ROOT/'tasks/g0-auth-preserving-activation/scripts',ROOT/'tasks/g0-proxy-continuation/cold-start/scripts'):
    sys.path.insert(0,str(path))
import native_delivery_case as base
import auth_isolation
import activation_transaction as transaction_module
from activation_transaction import ActivationSpec,ActivationTransaction,SubprocessLaunchctl
from native_activation_fixture import FixtureAuthGuard,_load_credential_inventory
from receipt_store import ReceiptStore
from inherited_fd_scheduler import InheritedFdScheduler

ENTRYPOINT=ROOT/'tasks/g0-proxy-continuation/cold-start/scripts/projectproxy_launchd_entrypoint.py'
BUILD=ROOT/'tasks/g0-global-delivery-validation/tmp/build'
REGISTER_SECONDS=30.;REVOKE_SECONDS=30.;TOTAL_SECONDS=120.;CLEANUP_SECONDS=10.
require=base.require;now=base.transport.now;remaining=base.transport.remaining;sha=base.sha;write=base.write


def source_pins(native_tui_policy=None):
    pins=base.source_pins()
    paths=[Path(__file__).resolve(),Path(__file__).with_name('receipt_store.py'),Path(__file__).with_name('inherited_fd_scheduler.py'),ENTRYPOINT,
        ROOT/'tasks/g0-proxy-continuation/cold-start/scripts/launch_activation.py',ROOT/'tasks/g0-completion/scripts/native_delivery_client.py',
        Path(transaction_module.__file__).resolve(),ROOT/'tasks/g0-auth-preserving-activation/scripts/native_activation_fixture.py']
    if base.activation_service.native_tui_policy(native_tui_policy) is not None:
        paths += [ROOT/'tasks/g1-g4-delivery/scripts'/name for name in ('native_tui_case.py','native_tui_session.py','native_resource_sampler.py')]
        paths += [ROOT/'tasks/g0-auth-preserving-activation/scripts/native_activation_probe.py',ROOT/'tasks/g0-tui-proxy/scripts/proxy_native_runtime.py']
    return {**pins,**{str(p):sha(p) for p in paths}}


def admission_policy(mode):
    require(mode in ('launchd','inherited_fd'),'invalid scheduler admission mode')
    return {'version':1,'mode':mode}


class DeadlineLaunchctl(SubprocessLaunchctl):
    phase_end=None
    def _run(self,*args):
        require(self.phase_end is not None,'launchctl phase missing')
        original=self.timeout;self.timeout=min(original,remaining(self.phase_end))
        try:return super()._run(*args)
        finally:self.timeout=original


@dataclass
class GlobalPlan:
    case:Path
    case_identity:tuple
    manifest_path:Path
    manifest_sha256:str
    plan_sha256:str
    source_hashes:dict
    spec:ActivationSpec
    transaction:ActivationTransaction
    python:Path
    go_binary:Path
    go_sha256:str
    controller_peer:dict
    grants_dir:Path
    state_dir:Path
    scheduler_mode:str
    scheduler:object
    native_tui_policy:dict|None=None


def prepare_global(prepared,case_dir,*,python_executable,go_executable,activation_home,credential_manifest,
                   expected_native_sha256,expected_source_pins,scheduler_mode='launchd',native_tui_policy=None):
    policy=base.activation_service.native_tui_policy(native_tui_policy)
    require(policy is None or scheduler_mode=='inherited_fd','native TUI policy is private only')
    require(expected_source_pins==source_pins(policy) and expected_source_pins,'source pins missing or changed')
    prepared.verify();require(not prepared.endpoint._started,'endpoint already started')
    python=Path(python_executable);go=Path(go_executable)
    require(base.peer_for_process(os.getpid())['executable']==str(python),'controller must use the pinned safe interpreter')
    for tool in (python,go):
        require(tool.is_absolute() and tool.resolve()==tool,'tool must be canonical')
        info=tool.stat();require(stat.S_ISREG(info.st_mode) and info.st_uid in (0,os.getuid()) and not info.st_mode&0o022 and info.st_mode&0o111,'unsafe executable')
    native=prepared.context.expected_executable;base.transport._native_check(native,expected_native_sha256)
    require(scheduler_mode in ('launchd','inherited_fd'),'unknown scheduler mode')
    home=Path(activation_home)
    require(scheduler_mode!='launchd' or home==Path(pwd.getpwuid(os.getuid()).pw_dir).resolve(),'real activation home mismatch')
    launchctl=('/bin/launchctl',) if scheduler_mode=='launchd' else (str(python),'-I','-B',str(Path(__file__).with_name('inherited_fd_scheduler.py')))
    public=home/'.codex/app-server-control/app-server-control.sock'
    require(public==prepared.spec.public_socket and len(os.fsencode(public))<104,'activation public endpoint mismatch')
    inventory,digest=_load_credential_inventory(Path(credential_manifest))
    require(tuple(prepared.spec.protected_read_paths)==inventory,'profile credential protection differs from frozen inventory')
    case=Path(case_dir);require(re.fullmatch('[A-Za-z0-9_-]{1,38}',case.name) is not None,'case identifier exceeds Go envelope scope');base.transport._dir_identity(case.parent);case.mkdir(mode=0o700)
    for name in ('state','grants','bin'):(case/name).mkdir(mode=0o700)
    BUILD.mkdir(parents=True,exist_ok=True)
    for name in ('cache','modcache','tests'):(BUILD/name).mkdir(exist_ok=True)
    caches={str(path):base.cache_identity(path) for path in (BUILD,BUILD/'cache',BUILD/'modcache')}
    env=auth_isolation.build_clean_environment(prepared.spec,{})
    env.update(GOENV='off',GOPROXY='off',GOSUMDB='off',GOTOOLCHAIN='local',GOCACHE=str(BUILD/'cache'),GOMODCACHE=str(BUILD/'modcache'))
    binary=case/'bin/g0-return-lab'
    built=subprocess.run([str(go),'build','-buildvcs=false','-trimpath','-o',str(binary),'.'],cwd=ROOT/'tools/g0-return-lab',env=env,capture_output=True,timeout=120,check=False)
    require(built.returncode==0,'Go build failed')
    require(all(base.cache_identity(Path(p))==v for p,v in caches.items()),'Go cache identity changed')
    pins={**expected_source_pins,str(python):sha(python),str(go):sha(go),str(binary):sha(binary),str(credential_manifest):digest}
    manifest={'version':1,'external_admission':admission_policy(scheduler_mode),'public_socket':str(public),'state_dir':str(case/'state'),'grants_dir':str(case/'grants'),
        'isolation_manifest':prepared.manifest['isolation_manifest'],'isolation_manifest_sha256':sha(prepared.manifest['isolation_manifest']),
        'backend_argv':[str(native),'app-server','--listen','unix://{socket_path}'],'backend_executable_sha256':expected_native_sha256,
        'file_pins':pins,'idle_seconds':10,'owner_helper':{'executable':str(python),'executable_sha256':sha(python),
        'source_path':str(Path(base.owner_helper.__file__).resolve()),'source_sha256':sha(base.owner_helper.__file__)}}
    if policy is not None:manifest['native_tui_policy']=policy
    manifest_path=case/'service.json';write(manifest_path,manifest)
    argv=(str(python),'-I','-B',str(ENTRYPOINT),'--manifest',str(manifest_path))
    spec=ActivationSpec(home=home,plist_path=home/'Library/LaunchAgents/org.codex.orchestration.proxy.plist',socket_path=public,
        label='org.codex.orchestration.proxy',domain=f'gui/{os.getuid()}',program_arguments=argv,startup_sha256=sha(ENTRYPOINT),
        txn_id=case.name,launchctl=tuple(launchctl),manifest_path=manifest_path,manifest_sha256=sha(manifest_path),
        artifact_hashes=tuple(pins.items()),lease_path=case/'grants/registration.lease.json',startup_path=ENTRYPOINT)
    txn=ActivationTransaction(spec,auth_guard=FixtureAuthGuard(inventory,Path(credential_manifest),digest))
    txn.launchd=DeadlineLaunchctl(spec.launchctl,env=spec.launchctl_env) if scheduler_mode=='launchd' else InheritedFdScheduler(spec,now)
    peer=base.peer_for_process(os.getpid())
    plan={'version':1,'program_arguments':argv,'native_sha256':expected_native_sha256,'source_pins':pins,
        'manifest_sha256':sha(manifest_path),'fixture_sha256':sha(prepared.spec.task_root/'synthetic-plan.json'),
        'controller_peer':peer,'scheduler_mode':scheduler_mode,'register_seconds':REGISTER_SECONDS,'delivery_total_seconds':TOTAL_SECONDS,
        'delivery_business_seconds':TOTAL_SECONDS-CLEANUP_SECONDS,'revoke_seconds':REVOKE_SECONDS,
        'go_binary':str(binary),'go_sha256':sha(binary),'build_cache_identities':caches,'build_timeout_seconds':120,
        'ordinary_tui_verified':False,'durable_history_verified':False,'external_model_calls':0}
    write(case/'plan.json',plan)
    return GlobalPlan(case,base.transport._dir_identity(case),manifest_path,sha(manifest_path),sha(case/'plan.json'),
        expected_source_pins,spec,txn,python,binary,sha(binary),peer,case/'grants',case/'state',scheduler_mode,txn.launchd,policy)


def frozen(plan,prepared):
    require(plan.transaction.launchd is plan.scheduler and plan.transaction.spec==plan.spec,'scheduler instance or spec replaced')
    if plan.scheduler_mode=='inherited_fd':
        require(type(plan.scheduler) is InheritedFdScheduler and plan.spec.home.is_relative_to(Path('/private/tmp')),'private scheduler replaced')
        plan.scheduler.verify()
    require(plan.scheduler_mode!='launchd' or (type(plan.scheduler) is DeadlineLaunchctl and plan.spec.launchctl==('/bin/launchctl',) and plan.spec.home==Path(pwd.getpwuid(os.getuid()).pw_dir).resolve()),'real scheduler replaced')
    require(plan.source_hashes==source_pins(plan.native_tui_policy) and sha(plan.manifest_path)==plan.manifest_sha256 and sha(plan.case/'plan.json')==plan.plan_sha256,'frozen source or manifest changed')
    require(base.peer_for_process(os.getpid())==plan.controller_peer,'controller identity changed')
    require(sha(plan.go_binary)==plan.go_sha256,'Go artifact changed');prepared.verify()
    manifest,_=base.activation_service._private_json(plan.manifest_path)
    require(manifest.get('native_tui_policy')==plan.native_tui_policy,'frozen native TUI policy changed')
    require(plan.native_tui_policy is None or plan.scheduler_mode=='inherited_fd','native TUI scheduler changed')
    require(manifest.get('external_admission')==admission_policy(plan.scheduler_mode),'frozen admission mode changed')


def transaction_phase(plan,name,operation,seconds):
    start=now();end=start+seconds;plan.transaction.launchd.phase_end=end
    row={'phase':name,'started_raw':start,'deadline_raw':end}
    write(plan.case/f'{name}-start.json',row)
    try:
        operation();remaining(end)
    except BaseException as exc:
        row.update(failure_type=type(exc).__name__,errno=getattr(exc,'errno',None),message_sha256=hashlib.sha256(str(exc).encode()).hexdigest())
        raise
    finally:
        row.update(finished_raw=now(),receipt=json.loads(json.dumps(plan.transaction.receipt)))
        write(plan.case/f'{name}-final.json',row)
    return row


async def receipt(store,name,end):
    while True:
        remaining(end);found=store.read(name)
        if found is not None:remaining(end);return found
        await asyncio.sleep(.01)


def common(row,store,plan):
    require(row.get('activation_id')==store.activation_id and row.get('manifest_sha256')==plan.manifest_sha256,'service receipt binding mismatch')


def peer_matches(actual,expected):
    return all(actual.get(k)==expected.get(k) for k in ('pid','uid','birth','executable','executable_sha256'))


def process_gone(peer):
    return base.proxy_transport._process_metadata(peer['pid'])==(None,None)


class SpawnClientFailure(ValueError):
    def __init__(self,record):
        self.spawn_record=record
        super().__init__('client startup failed')


async def spawn_client(plan,prepared,role,env,end,cleanup_end=None):
    remaining(end)
    child=await asyncio.create_subprocess_exec(str(plan.python),'-I','-B',str(base.CLIENT),role,cwd=prepared.spec.workspace,
        env=env,stdin=asyncio.subprocess.PIPE,stdout=asyncio.subprocess.PIPE,stderr=asyncio.subprocess.PIPE)
    output=asyncio.create_task(base.transport._drain(child.stderr));peer=None
    try:
        peer=base.peer_for_process(child.pid)
        require((await base.line(child,end)).get('event')=='boot','client boot failed')
        require(peer['executable']==str(plan.python),'client executable mismatch')
        return child,peer,output
    except BaseException as exc:
        record={'pid':child.pid,'role':role,'failure_type':type(exc).__name__,'stopped':False}
        if peer is not None:record['identity']=peer
        limit=min(cleanup_end if cleanup_end is not None else end,now()+3)
        try:
            record['exit']=await base.stop_client(child,limit,peer)
            record['stopped']=process_gone({'pid':child.pid})
            timeout=remaining(limit);record['stderr']=await asyncio.wait_for(output,timeout)
        except Exception:record['cleanup_unproven']=True
        raise SpawnClientFailure(record) from None


def config(plan,prepared,role,peer,proxy,public_identity,grant,business,startup,total):
    path=plan.grants_dir/f"{peer['pid']}.json";write(path,grant)
    return {'role':role,'native_tui_policy':plan.native_tui_policy,'external_admission':admission_policy(plan.scheduler_mode),'manifest_path':str(plan.manifest_path),'manifest_sha256':plan.manifest_sha256,
        'grant_path':str(path),'grant_sha256':sha(path),'profile_id':prepared.spec.profile_id,
        'public_socket':str(plan.spec.socket_path),'public_socket_identity':public_identity,'proxy_peer':proxy,
        'controller_peer':plan.controller_peer,'owner_context_sha256':context_sha(prepared),'codex_home':str(prepared.spec.codex_home),'workspace':str(prepared.spec.workspace),
        'deadline':business,'local_deadline':startup,'cleanup_deadline':total}


PEER_KEYS={'pid','uid','birth','executable','executable_sha256'}
LEASE_KEYS={'profile_id','owner_context_sha256','lease_id','owner_connection_id','owner_epoch','owner_thread_id',
    'private_socket','backend_pid','backend_birth','backend_executable_sha256','private_socket_identity','service_identity'}


def check_peer(row,expected):
    require(isinstance(row,dict) and all(row.get(k)==expected[k] for k in ('pid','uid','birth','executable')) and row.get('complete',True) is True,'receipt peer mismatch')


def context_sha(prepared):
    spec=prepared.spec
    value={'profile_id':spec.profile_id,'home':str(spec.home),'codex_home':str(spec.codex_home),'workspace':str(spec.workspace),
        'backend_socket':str(spec.backend_socket),'config_sha256':sha(spec.codex_home/'config.toml'),
        'expected_executable_sha256':prepared.context.expected_executable_sha256}
    return hashlib.sha256(base.transport.encoded(value)).hexdigest()


def _validate_owner_connection(row,expected_owner_epoch):
    require(type(expected_owner_epoch) is int and expected_owner_epoch>0,'expected owner epoch invalid')
    require(type(row['owner_epoch']) is int and row['owner_epoch']==expected_owner_epoch
        and re.fullmatch(f'conn-{expected_owner_epoch}-[0-9a-f]{{12}}',row['owner_connection_id']) is not None,
        'owner connection epoch invalid')


def validate_lease(row,prepared,service_peer,*,expected_owner_epoch=1):
    require(isinstance(row,dict) and set(row)==LEASE_KEYS,'owner lease shape')
    require(row['profile_id']==prepared.spec.profile_id and row['owner_context_sha256']==context_sha(prepared),'owner context mismatch')
    require(type(row['lease_id']) is str and re.fullmatch('[0-9a-f]{12}',row['lease_id']) is not None,'lease id invalid')
    _validate_owner_connection(row,expected_owner_epoch)
    require(row['service_identity']==service_peer,'lease service identity mismatch')
    require(row['private_socket']==str(prepared.spec.backend_socket.parent/f"b-{row['lease_id']}.sock"),'lease socket path mismatch')
    require(row['backend_executable_sha256']==prepared.context.expected_executable_sha256,'lease backend SHA mismatch')
    native=base.peer_for_process(row['backend_pid'])
    require(native['birth']==row['backend_birth'] and native['executable']==str(prepared.context.expected_executable) and native['executable_sha256']==row['backend_executable_sha256'],'lease backend process mismatch')
    info=os.lstat(row['private_socket'])
    require(stat.S_ISSOCK(info.st_mode) and info.st_uid==os.getuid() and not info.st_mode&0o077 and list((info.st_dev,info.st_ino,info.st_uid,info.st_mode))==row['private_socket_identity'],'lease socket identity mismatch')
    return dict(row),native


CONNECTED_KEYS={'version','activation_id','manifest_sha256','service_identity','state','profile_id','owner_context_sha256',
    'lease_id','frontend_peer','grant_sha256','private_socket','private_socket_identity','backend_pid','backend_birth',
    'backend_executable_sha256','backend_peer'}
CONNECTED_LEASE_KEYS=LEASE_KEYS-{'owner_connection_id','owner_epoch','owner_thread_id'}


def check_connected_backend(row,prepared,service_peer):
    require(row['service_identity']==service_peer and row['profile_id']==prepared.spec.profile_id and row['owner_context_sha256']==context_sha(prepared),'connected profile/service mismatch')
    require(type(row['lease_id']) is str and re.fullmatch('[0-9a-f]{12}',row['lease_id']) is not None,'connected lease invalid')
    require(row['private_socket']==str(prepared.spec.backend_socket.parent/f"b-{row['lease_id']}.sock"),'connected backend path mismatch')
    require(row['backend_executable_sha256']==prepared.context.expected_executable_sha256,'connected backend SHA mismatch')
    native=base.peer_for_process(row['backend_pid'])
    require(native['birth']==row['backend_birth'] and native['executable']==str(prepared.context.expected_executable) and native['executable_sha256']==row['backend_executable_sha256'],'connected backend process mismatch')
    info=os.lstat(row['private_socket'])
    require(stat.S_ISSOCK(info.st_mode) and info.st_uid==os.getuid() and not info.st_mode&0o077 and list((info.st_dev,info.st_ino,info.st_uid,info.st_mode))==row['private_socket_identity'],'connected backend inode mismatch')
    return native


def validate_helper_ready(row,store,plan,prepared,service_peer,helper_peer,lease,grant_sha):
    common(row,store,plan)
    require(set(row)=={'version','activation_id','manifest_sha256','service_identity','grant_sha256','owner_lease','frontend_peer','backend_peer'}
        and row['version']==1 and row['service_identity']==service_peer and row['grant_sha256']==grant_sha,'helper ready shape/service/grant mismatch')
    require(row['owner_lease']=={k:v for k,v in lease.items() if k!='service_identity'},'helper live lease mismatch')
    _,native=validate_lease(lease,prepared,service_peer)
    check_peer(row['frontend_peer'],helper_peer);check_peer(row['backend_peer'],native)


async def admit_public_child(plan,prepared,store,child,peer,child_config,service_peer,end,lease=None):
    event=await base.line(child,end)
    require(set(event)=={'event','role','pid','profile_id','manifest_sha256','grant_sha256','public_peer'} and event['event']=='public_connected'
        and event['role']==child_config['role'] and event['pid']==peer['pid'] and event['profile_id']==prepared.spec.profile_id
        and event['manifest_sha256']==plan.manifest_sha256 and event['grant_sha256']==child_config['grant_sha256'],'public connection event mismatch')
    mode=admission_policy(plan.scheduler_mode)
    require(child_config.get('external_admission')==mode,'client gate mode mismatch')
    if mode['mode']=='inherited_fd':check_peer(event['public_peer'],service_peer)
    if child_config['role']=='owner':
        while True:
            remaining(end);names=[n for n in store.children() if re.fullmatch(r'owner-connected-[0-9a-f]{12}-'+str(peer['pid'])+r'\.json',n)]
            require(len(names)<=1,'multiple owner connected receipts')
            if names:break
            await asyncio.sleep(.01)
        receipt_name=names[0]
        row,digest=await receipt(store,receipt_name,end);common(row,store,plan)
        require(set(row)==CONNECTED_KEYS and row['version']==1 and row['state']=='connected' and row['grant_sha256']==child_config['grant_sha256'],'owner connected shape/grant mismatch')
        check_peer(row['frontend_peer'],peer);native=check_connected_backend(row,prepared,service_peer);check_peer(row['backend_peer'],native)
        connected={k:row[k] for k in CONNECTED_LEASE_KEYS}
    else:
        require(lease is not None,'helper owner lease absent')
        receipt_name=f"helper-ready-{lease['lease_id']}-{peer['pid']}.json"
        row,digest=await receipt(store,receipt_name,end)
        validate_helper_ready(row,store,plan,prepared,service_peer,peer,lease,child_config['grant_sha256'])
        connected={k:lease[k] for k in CONNECTED_LEASE_KEYS}
    require(peer_matches(base.peer_for_process(peer['pid']),peer),'frontend changed before admission')
    require(peer_matches(base.peer_for_process(service_peer['pid']),service_peer),'service changed before admission')
    plan.transaction.launchd.phase_end=end
    require(plan.transaction.verify()['job'].get('pid')==service_peer['pid'],'job changed before admission')
    frozen(plan,prepared);remaining(end)
    admitted={'action':'admitted','role':child_config['role'],'lease_id':connected['lease_id'],
        'manifest_sha256':plan.manifest_sha256,'grant_sha256':child_config['grant_sha256'],'receipt_sha256':digest,
        'activation_id':store.activation_id,'receipt_name':receipt_name}
    await base.send(child,admitted,end)
    return connected,{'lease_id':connected['lease_id'],'receipt_sha256':digest,'public_peer':event['public_peer'],'mode':mode['mode']}


def backend_projection(row,lease,owner_peer,helper_peer,*,require_helper):
    require(row.get('lease_id')==lease['lease_id'] and row.get('profile_id')==lease['profile_id'] and row.get('pid')==lease['backend_pid'] and row.get('birth')==lease['backend_birth'] and row.get('private_socket')==lease['private_socket'],'backend final identity')
    check_peer(row.get('frontend'),owner_peer)
    require(row.get('owner_thread_id')==lease['owner_thread_id'] and row.get('owner_context_sha256')==lease['owner_context_sha256'] and row.get('owner_connection_id')==lease['owner_connection_id'] and row.get('owner_epoch')==lease['owner_epoch'],'backend final owner binding')
    require(row.get('process_stopped') is True and row.get('socket_removed') is True and row.get('state')=='closed' and not row.get('cleanup_failure'),'backend cleanup incomplete')
    helpers=row.get('helpers',[])
    require(isinstance(helpers,list) and len(helpers)==(1 if require_helper else 0),'final helper count')
    if require_helper:
        check_peer(helpers[0].get('frontend'),helper_peer)
        require(helpers[0].get('state')=='closed' and helpers[0].get('lease_id')==lease['lease_id'] and helpers[0].get('owner_thread_id')==lease['owner_thread_id'],'final helper not closed')
    guard=row.get('guardian',{})
    require(guard.get('child_reaped') is True and guard.get('state')=='stopped','guardian cleanup unproven')
    require(process_gone({'pid':lease['backend_pid']}) and not os.path.lexists(lease['private_socket']),'backend remains')
    keys=('lease_id','profile_id','pid','birth','private_socket','owner_thread_id','owner_context_sha256','owner_connection_id','owner_epoch','state','process_stopped','socket_removed','returncode','tree_stop_unproven','restart_allowed')
    return {**{k:row[k] for k in keys if k in row},'helper_count':len(helpers),'guardian_state':guard['state'],'child_reaped':True}


async def stop_service(peer,store,plan,end,lease,owner_peer,helper_peer,require_helper):
    if peer is None:return False
    if not process_gone(peer):
        require(peer_matches(base.peer_for_process(peer['pid']),peer),'service stop identity changed')
        remaining(end)
        try:os.kill(peer['pid'],signal.SIGTERM)
        except ProcessLookupError:pass
    final,digest=await receipt(store,'activation.json',end);common(final,store,plan)
    while not process_gone(peer):remaining(end);await asyncio.sleep(.01)
    rows=final.get('backend_records')
    require(isinstance(rows,list) and len(rows)==(1 if lease is not None else 0),'activation final backend count')
    if lease is not None:backend_projection(rows[0],lease,owner_peer,helper_peer,require_helper=require_helper)
    return {'activation_id':store.activation_id,'manifest_sha256':plan.manifest_sha256,'backend_count':len(rows),'all_backends_stopped':True,'service_stopped':True},digest


async def run_global(plan,prepared):
    fd=base.transport._open_case(plan.case,plan.case_identity)
    try:base.transport._write(fd,'attempt.json',{'started_raw':now(),'automatic_retry':False})
    except BaseException:os.close(fd);raise
    result={'version':1,'status':'unknown','ordinary_tui_verified':False,'durable_history_verified':False,
        'business_ack_verified':False,'scheduler_mode':plan.scheduler_mode,'real_launchctl_used':plan.scheduler_mode=='launchd',
        'global_registration_verified':False,'external_model_calls':0}
    owner=helper=None;owner_peer=helper_peer=service_peer=None;store=ReceiptStore(plan.state_dir);outputs=[]
    lease=None;helper_live=False;total=None;stage='register';env=auth_isolation.build_clean_environment(prepared.spec,{})
    try:
        frozen(plan,prepared)
        def register():plan.transaction.prepare();return plan.transaction.register()
        result['registration']=transaction_phase(plan,'register',register,REGISTER_SECONDS)
        start=now();total=start+TOTAL_SECONDS;business=total-CLEANUP_SECONDS;startup=min(business,start+10)
        result.update(started_raw=start,deadline_raw=total,business_deadline_raw=business)
        prepared.start_endpoint();public_identity=base.transport._socket_identity(plan.spec.socket_path)
        require(not (plan.grants_dir/f'{os.getpid()}.json').exists(),'controller must have no frontend grant')
        stage='ungranted-activation';timeout=remaining(startup)
        reader,writer=await asyncio.wait_for(asyncio.open_unix_connection(plan.spec.socket_path),timeout)
        try:
            timeout=remaining(startup);require(await asyncio.wait_for(reader.read(1),timeout)==b'','ungranted probe returned data')
            raw_peer=base.proxy_transport.peer_identity(writer.get_extra_info('socket'))
            result['ungranted_probe_peer']=asdict(raw_peer)
            # Wakeup credentials are diagnostics; bind the actual service below.
        finally:writer.close();await writer.wait_closed()
        started,started_sha=await receipt(store,'service-start.json',startup);common(started,store,plan)
        require(set(started)=={'version','activation_id','manifest_sha256','service_identity','started_raw_ns'} and started['version']==1 and type(started['started_raw_ns']) is int and int(result['registration']['started_raw']*1e9)<=started['started_raw_ns']<=int(startup*1e9),'service start shape or time mismatch')
        service_peer=started['service_identity'];require(set(service_peer)==PEER_KEYS,'service identity shape')
        require(service_peer['executable']==str(plan.python) and service_peer['executable_sha256']==sha(plan.python),'service executable mismatch')
        require(peer_matches(base.peer_for_process(service_peer['pid']),service_peer),'service process mismatch')
        plan.transaction.launchd.phase_end=startup;job=plan.transaction.verify()['job']
        require(job.get('pid')==service_peer['pid'],'launchd job PID differs from service')
        require(not any((plan.state_dir/store.activation_id/name).is_dir() for name in store.children()),'ungranted probe spawned backend')
        result['service_identity']=service_peer;result['service_start_sha256']=started_sha
        result['ungranted_probe']={'rpc_bytes_sent':0,'eof':True,'backend_lease_directories':0}
        stage='owner-start';owner,owner_peer,out=await spawn_client(plan,prepared,'owner',env,startup,total);outputs.append(out)
        grant={'version':1,'pid':owner.pid,'uid':os.getuid(),'birth':owner_peer['birth'],'expected_executable':str(plan.python),
            'executable_sha256':sha(plan.python),'profile_id':prepared.spec.profile_id}
        owner_config=config(plan,prepared,'owner',owner_peer,service_peer,public_identity,grant,business,startup,total)
        await base.send(owner,owner_config,startup);stage='owner-admission'
        owner_connected,owner_admission=await admit_public_child(plan,prepared,store,owner,owner_peer,owner_config,service_peer,startup)
        result['owner_admission']=owner_admission
        ready=await base.line(owner,startup)
        require(ready.get('event')=='thread_ready' and ready.get('home_match') is True,'owner thread not ready')
        # The service is external: discover its immutable owner-ready receipt.
        while True:
            remaining(startup);names=[name for name in store.children() if name.startswith('ready-') and name.endswith('.json')]
            require(len(names)<=1,'multiple owner leases')
            if names:break
            await asyncio.sleep(.01)
        owner_ready,owner_sha=await receipt(store,names[0],startup);common(owner_ready,store,plan)
        check_peer(owner_ready.get('frontend'),owner_peer)
        require(owner_ready.get('ready_published') is True and owner_ready.get('zero_turns') is True and owner_ready.get('initialized') is True and owner_ready.get('closed') is False,'owner ready milestone invalid')
        checked,native_peer=validate_lease(owner_ready['owner_lease'],prepared,service_peer)
        check_peer(owner_ready.get('backend'),native_peer)
        require(checked['owner_thread_id']==ready['thread_id'] and ready['public_peer']==owner_admission['public_peer'],'owner ready thread/provenance mismatch')
        require(all(checked[k]==owner_connected[k] for k in CONNECTED_LEASE_KEYS),'owner ready changed connected lease/backend')
        lease=checked
        result['lease']=lease;result['owner_ready_sha256']=owner_sha
        await base.send(owner,{'action':'initial_turn'},startup);initial=await base.line(owner,business)
        require(initial.get('event')=='initial_ready' and initial['thread_id']==lease['owner_thread_id'],'initial turn failed')
        result['owner']={**owner_peer,**initial,'public_peer':ready['public_peer']}
        require(prepared.endpoint.snapshot()['accepted_requests']==1,'initial synthetic count mismatch')
        task=prepared.spec.workspace/'go-task';task.mkdir(mode=0o700);nonce=plan.case.name;env['CODEX_THREAD_ID']=lease['owner_thread_id']
        stage='go-task';result['go_job']=await base.go_within(plan.go_binary,['start','--dir',str(task),'--nonce',nonce,'--delay','500ms','--controller-thread',lease['owner_thread_id']],env,business)
        done=await base.go_within(plan.go_binary,['wait','--dir',str(task),'--nonce',nonce,'--timeout','2s'],env,business);require(done.get('status')=='completed','Go incomplete')
        inspected=await base.go_within(plan.go_binary,['inspect','--dir',str(task),'--nonce',nonce],env,business)
        require(inspected.get('controller_thread')==lease['owner_thread_id'] and inspected.get('event_hash'),'Go owner mismatch');result['go_inspect']=inspected
        stage='helper-start';helper_start=min(business,now()+10);helper,helper_peer,out=await spawn_client(plan,prepared,'helper',env,helper_start,total);outputs.append(out)
        result['helper']=helper_peer
        grant={k:lease[k] for k in ('profile_id','owner_context_sha256','lease_id','owner_connection_id','owner_epoch','owner_thread_id','private_socket')}
        grant.update(version=1,role='owner-helper',helper_pid=helper.pid,helper_uid=os.getuid(),helper_birth=helper_peer['birth'],
            helper_executable=str(plan.python),helper_executable_sha256=sha(plan.python),helper_source_sha256=sha(base.owner_helper.__file__))
        helper_config=config(plan,prepared,'helper',helper_peer,service_peer,public_identity,grant,business,helper_start,total)
        helper_config.update(thread_id=lease['owner_thread_id'],initial_turn_id=initial['initial_turn_id'],lease_id=lease['lease_id'],owner_epoch=lease['owner_epoch'],
            go_binary=str(plan.go_binary),go_sha256=plan.go_sha256,task_dir=str(task),nonce=nonce,delivery_id='G0_SYNTHETIC_TOOL_RESULT_'+nonce,
            events=[{'event_id':'event_result','event_revision':1,'kind':'result','payload_hash':inspected['event_hash'],'action_slot':'ack_result'}])
        await base.send(helper,helper_config,helper_start);stage='helper-admission'
        _,helper_admission=await admit_public_child(plan,prepared,store,helper,helper_peer,helper_config,service_peer,helper_start,lease)
        result['helper_admission']=helper_admission
        stage='helper-proof';proof=await base.line(helper,business)
        require(proof.get('event')=='history_proof' and proof['thread_id']==lease['owner_thread_id'] and proof['lease_id']==lease['lease_id'] and proof['initial_turn_id']==initial['initial_turn_id'],'helper proof binding')
        require(proof['grant_sha256']==helper_config['grant_sha256'] and proof['manifest_sha256']==plan.manifest_sha256,'helper policy changed')
        require(proof['public_peer']==helper_admission['public_peer'],'helper connection provenance changed')
        helper_ready,helper_sha=await receipt(store,f"helper-ready-{lease['lease_id']}-{helper.pid}.json",min(business,now()+10));common(helper_ready,store,plan)
        validate_helper_ready(helper_ready,store,plan,prepared,service_peer,helper_peer,lease,helper_config['grant_sha256'])
        require(helper_sha==helper_admission['receipt_sha256'],'helper admission receipt changed')
        helper_live=True
        require(peer_matches(base.peer_for_process(service_peer['pid']),service_peer),'service changed before ACK')
        require(base.proxy_transport._process_metadata(lease['backend_pid'])[0]==lease['backend_birth'],'backend changed before ACK')
        end=min(business,now()+10);await base.send(owner,{'action':'status'},end)
        live=await base.line(owner,end);require(live.get('event')=='owner_live' and live['thread_id']==lease['owner_thread_id'],'owner not live')
        require(proof['matched_event_ids']==['event_result'] and proof['summary']['captured_page_chain_complete'],'history proof incomplete')
        require(prepared.endpoint.snapshot()['accepted_requests']==2,'synthetic count mismatch');frozen(plan,prepared)
        result['helper_proof']=proof;result['helper_ready_sha256']=helper_sha
        decision={'action':'decide','reads_sha256':proof['reads_sha256'],'decisions':{'event_result':'handled'},'command_ids':{'event_result':'handled_'+nonce}}
        write(plan.case/'controller-handled-decision.json',decision);end=min(business,now()+10);stage='go-ack'
        await base.send(helper,decision,end);ack=await base.line(helper,end)
        require(ack.get('event')=='ack' and ack.get('business_ack_complete') is True and len(ack['records'])==1 and ack['records'][0].get('effect_count')==1,'ACK incomplete')
        result['ack']=ack;result['business_ack_verified']=True
        result['go_status']=await base.go_within(plan.go_binary,['batch-status','--dir',str(task),'--nonce',nonce],env,business)
        require(result['go_status'].get('business_status')=='complete' and result['go_status'].get('send_allowed') is False,'Go status incomplete')
        stage='normal-close';close_end=min(total,now()+10);close_sent=now();await base.send(owner,{'action':'close'},close_end)
        closed=await base.line(owner,close_end);require(closed.get('event')=='owner_closed','owner EOF absent');result['owner']['envelopes']=closed['envelopes']
        eof=await base.line(helper,close_end);require(eof.get('event')=='helper_eof' and eof.get('eof_raw',0)>=close_sent,'helper EOF too early or absent')
        result['normal_close']={'owner_close_sent_raw':close_sent,'helper_eof_raw':eof['eof_raw']};result['status']='passed'
    except Exception as exc:
        result['failure']={'stage':stage,'type':type(exc).__name__,'errno':getattr(exc,'errno',None),'message_sha256':hashlib.sha256(str(exc).encode()).hexdigest()}
        if isinstance(exc,base.SafeClientFailure):result['failure']['child_error']=exc.child_error
        if isinstance(exc,SpawnClientFailure):result['failure']['spawn_record']=exc.spawn_record
    finally:
        cleanup={'backend_stopped':False,'service_stopped':False,'public_socket_removed':False,'restart_allowed':False}
        end=min(total,now()+10) if total is not None else now()+10
        for role,child,peer in (('owner',owner,owner_peer),('helper',helper,helper_peer)):
            try:
                cleanup[role+'_exit']=await base.stop_client(child,end,peer)
                if result['status']=='passed' and cleanup[role+'_exit']!=0:result['status']='unknown'
            except Exception:result['status']='unknown';cleanup[role+'_stop_unproven']=True
        try:
            if lease is not None:
                final,digest=await receipt(store,f"backend-{lease['lease_id']}.json",end)
                projected=backend_projection(final,lease,owner_peer,helper_peer,require_helper=helper_live)
                result['backend_final']=projected;result['backend_final_sha256']=digest;cleanup['backend_stopped']=True
            stopped=await stop_service(service_peer,store,plan,end,lease,owner_peer,helper_peer,helper_live)
            if stopped:result['activation_final'],result['activation_final_sha256']=stopped;cleanup['service_stopped']=True
        except Exception as exc:result['status']='unknown';cleanup['service_cleanup_failure_type']=type(exc).__name__
        try:result['revocation']=transaction_phase(plan,'revoke',plan.transaction.revoke,REVOKE_SECONDS)
        except Exception as exc:result['status']='unknown';cleanup['revoke_failure_type']=type(exc).__name__
        result['transaction']=dict(plan.transaction.receipt)
        cleanup['public_socket_removed']=not os.path.lexists(plan.spec.socket_path)
        if result['transaction']['state']!='REVOKED' or not cleanup['public_socket_removed']:result['status']='unknown'
        try:frozen(plan,prepared);result['postflight']={'fixture_valid':True,'source_pins_valid':True}
        except Exception:result['status']='unknown';result['postflight']={'verified':False}
        result['synthetic_endpoint']=prepared.endpoint.snapshot()
        try:prepared.endpoint.close()
        except Exception:result['status']='unknown';cleanup['endpoint_close_unproven']=True
        try:result['client_output_digests']=await asyncio.wait_for(asyncio.gather(*outputs),1)
        except Exception:result['status']='unknown';cleanup['client_output_unproven']=True
        result['receipt_hashes']={name:pin[1] for name,pin in store.pins.items()};store.close()
        result['global_registration_verified']=result['status']=='passed' and plan.scheduler_mode=='launchd'
        result['scheduler_output_digests']=getattr(plan.scheduler,'digests',{})
        if hasattr(plan.scheduler,'startup_failure'):result['scheduler_startup_failure']=plan.scheduler.startup_failure
        result['cleanup']=cleanup;result['finished_raw']=now()
        try:base.transport._write(fd,'result.json',result)
        finally:os.close(fd)
    return result
