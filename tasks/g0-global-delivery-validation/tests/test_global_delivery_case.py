"""No native or launchctl: actual FD service/clients/guardian/HTTP and real Go."""
import asyncio,json,os,socket,subprocess,sys,tempfile,threading
from pathlib import Path
import pytest
ROOT=Path(__file__).resolve().parents[3]
for p in (ROOT/'tasks/g0-global-delivery-validation/scripts',ROOT/'tasks/g0-completion/scripts',ROOT/'tasks/g0-auth-preserving-activation/scripts'):
    sys.path.insert(0,str(p))
from synthetic_responses import SyntheticEndpoint
from synthetic_native_fixture import prepare as prepare_synthetic
from auth_isolation import IsolationSpec
PYTHON=Path('/opt/homebrew/Cellar/python@3.13/3.13.5/Frameworks/Python.framework/Versions/3.13/Resources/Python.app/Contents/MacOS/Python')
GO=Path('/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go')


def implementation():
    import global_delivery_case
    return global_delivery_case




def prepare(runner,root,endpoint,caseparent,monkeypatch,supervisor):
    home=supervisor/'h'
    for p in (home,home/'Library',home/'Library/LaunchAgents',home/'.codex',home/'.codex/app-server-control'):p.mkdir(mode=0o700)
    profile=root
    protected=caseparent/'credentials';protected.mkdir(mode=0o700)
    inventory=caseparent/'inventory.txt'
    paths=[]
    for i in range(14):
        p=protected/f'fake-{i}';p.write_text(f'synthetic-{i}');p.chmod(0o600);paths.append(p)
    inventory.write_text(''.join(str(p)+'\n' for p in paths));inventory.chmod(0o600)
    spec=IsolationSpec(profile,profile/'h',profile/'c',profile/'w','global-synthetic',home/'.codex/app-server-control/app-server-control.sock',profile/'b/b.sock',(protected,),tuple(paths),owned_loopback_port=endpoint.port)
    prepared=prepare_synthetic(endpoint,spec,executable=PYTHON)
    monkeypatch.setattr(runner.base.transport,'NATIVE_SHA256',runner.base.sha(PYTHON))
    monkeypatch.setattr(runner,'ENTRYPOINT',ROOT/'tasks/g0-global-delivery-validation/tests/fixtures/service_entrypoint.py')
    plan=runner.prepare_global(prepared,caseparent/'case',python_executable=PYTHON,go_executable=GO,
        activation_home=home,credential_manifest=inventory,scheduler_mode='inherited_fd',
        expected_native_sha256=runner.base.sha(PYTHON),expected_source_pins=runner.source_pins())
    return prepared,plan


def test_actual_external_service_transaction_and_go_delivery(monkeypatch,tmp_path):
    runner=implementation()
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw,tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as sup,SyntheticEndpoint() as endpoint:
            prepared,plan=prepare(runner,Path(raw),endpoint,tmp_path,monkeypatch,Path(sup))
            result=await runner.run_global(plan,prepared)
            assert result['status']=='passed',(result.get('failure'),result.get('ungranted_probe_peer'))
            assert result['transaction']['state']=='REVOKED' and result['transaction']['auth_after']['unchanged']
            assert result['service_identity']['pid'] not in (os.getpid(),result['owner']['pid'],result['helper']['pid'])
            assert result['ungranted_probe']['rpc_bytes_sent']==0 and result['ungranted_probe']['backend_lease_directories']==0
            assert result['ack']['records'][0]['effect_count']==1
            assert result['synthetic_endpoint']['accepted_requests']==2
            assert result['cleanup']['backend_stopped'] and result['cleanup']['service_stopped'] and result['cleanup']['public_socket_removed']
            calls=result['transaction']['launchctl']['calls']
            assert sum(row['command']=='bootstrap' for row in calls)==sum(row['command']=='bootout' for row in calls)==1
            assert '--listener-fd' not in plan.spec.program_arguments
            assert not result['real_launchctl_used'] and not result['ordinary_tui_verified']
    asyncio.run(asyncio.wait_for(scenario(),150))


@pytest.mark.parametrize('fault',['owner-context','helper-birth','final-helper-state'])
def test_real_chain_rejects_receipt_identity_or_cleanup_tamper(monkeypatch,tmp_path,fault):
    runner=implementation();original=runner.ReceiptStore.read
    def tampered(store,name):
        result=original(store,name)
        if result is None:return None
        value,digest=result;value=json.loads(json.dumps(value))
        if fault=='owner-context' and name.startswith('ready-'):value['owner_lease']['owner_context_sha256']='f'*64
        if fault=='helper-birth' and name.startswith('helper-ready-'):value['frontend_peer']['birth']='wrong-birth'
        if fault=='final-helper-state' and name=='activation.json':value['backend_records'][0]['helpers'][0]['state']='connected'
        return value,digest
    monkeypatch.setattr(runner.ReceiptStore,'read',tampered)
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw,tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as sup,SyntheticEndpoint() as endpoint:
            prepared,plan=prepare(runner,Path(raw),endpoint,tmp_path,monkeypatch,Path(sup))
            result=await runner.run_global(plan,prepared)
            assert result['status']=='unknown'
            assert result['business_ack_verified']==(fault=='final-helper-state')
            assert result['transaction']['state']=='REVOKED' and not plan.spec.socket_path.exists()
    asyncio.run(asyncio.wait_for(scenario(),150))


@pytest.mark.parametrize('fault',['history','revoke'])
def test_real_chain_failure_never_false_passes_or_reboots(monkeypatch,tmp_path,fault):
    runner=implementation()
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw,tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as sup,SyntheticEndpoint() as endpoint:
            prepared,plan=prepare(runner,Path(raw),endpoint,tmp_path,monkeypatch,Path(sup))
            if fault=='history':(prepared.spec.workspace/'dummy-mode.txt').write_text('bad-history')
            if fault=='revoke':monkeypatch.setattr(plan.scheduler,'bootout',lambda *args:1)
            try:
                result=await runner.run_global(plan,prepared)
                assert result['status']=='unknown'
                assert result['business_ack_verified']==(fault=='revoke')
                assert (plan.case/'register-final.json').exists() and (plan.case/'revoke-final.json').exists()
                assert sum(row['command']=='bootstrap' for row in result['transaction']['launchctl']['calls'])==1
                if fault=='history':assert not (prepared.spec.workspace/'go-task/batch-ack-event_result.json').exists()
                else:assert result['cleanup']['revoke_failure_type']=='UnknownStateError'
            finally:
                if plan.scheduler.listener is not None:plan.scheduler.listener.close()
    asyncio.run(asyncio.wait_for(scenario(),150))


def test_service_started_before_register_return_is_valid(monkeypatch,tmp_path):
    import time
    runner=implementation();observed={}
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw,tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as sup,SyntheticEndpoint() as endpoint:
            prepared,plan=prepare(runner,Path(raw),endpoint,tmp_path,monkeypatch,Path(sup))
            register=plan.transaction.register
            def delayed_register():
                value=register();store=runner.ReceiptStore(plan.state_dir);end=runner.now()+2
                try:
                    while True:
                        runner.remaining(end);started=store.read('service-start.json')
                        if started is not None:break
                        time.sleep(.01)
                    observed['service_started_raw_ns']=started[0]['started_raw_ns']
                    # Keep the service demonstrably started before register
                    # returns, without starting a backend or sending RPC.
                    time.sleep(.03)
                    observed['register_return_raw']=runner.now()
                finally:store.close()
                return value
            monkeypatch.setattr(plan.transaction,'register',delayed_register)
            result=await runner.run_global(plan,prepared)
            register_start=json.loads((plan.case/'register-start.json').read_text())['started_raw']
            assert register_start*1e9<=observed['service_started_raw_ns']<observed['register_return_raw']*1e9
            assert observed['register_return_raw']<=result['started_raw']
            assert result['status']=='passed',result.get('failure')
            assert result['business_ack_verified'] and result['ack']['records'][0]['effect_count']==1
            assert result['transaction']['state']=='REVOKED'
            assert result['cleanup']['backend_stopped'] and result['cleanup']['service_stopped'] and result['cleanup']['public_socket_removed']
    asyncio.run(asyncio.wait_for(scenario(),150))


@pytest.mark.parametrize('diagnostic',['uid-zero','missing-metadata','complete-other-peer'])
def test_ungranted_probe_metadata_is_diagnostic_only(monkeypatch,tmp_path,diagnostic):
    from dataclasses import replace
    runner=implementation();original=runner.base.proxy_transport.peer_identity;calls=[]
    def probe_metadata(sock):
        observed=original(sock);calls.append(True)
        if diagnostic=='complete-other-peer':
            controller=runner.base.peer_for_process(os.getpid())
            return replace(observed,pid=controller['pid'],uid=0,birth=controller['birth'],executable=controller['executable'],
                pid_available=True,uid_available=True,birth_available=True,executable_available=True)
        return replace(observed,pid=None,uid=0 if diagnostic=='uid-zero' else None,birth=None,executable=None,
            pid_available=False,uid_available=diagnostic=='uid-zero',birth_available=False,executable_available=False)
    # Only this controller's post-EOF diagnostic reader is changed. The
    # independent service, owner and helper retain their real OS peer checks.
    monkeypatch.setattr(runner.base.proxy_transport,'peer_identity',probe_metadata)
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw,tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as sup,SyntheticEndpoint() as endpoint:
            prepared,plan=prepare(runner,Path(raw),endpoint,tmp_path,monkeypatch,Path(sup))
            result=await runner.run_global(plan,prepared)
            assert result['status']=='passed',result.get('failure')
            assert calls==[True]
            assert result['ungranted_probe_peer']['uid']==(None if diagnostic=='missing-metadata' else 0)
            assert result['ungranted_probe']=={'rpc_bytes_sent':0,'eof':True,'backend_lease_directories':0}
            assert result['service_identity']['uid']==os.getuid()
            assert result['owner']['public_peer']['uid']==result['helper_proof']['public_peer']['uid']==os.getuid()
            assert result['ack']['records'][0]['effect_count']==1 and result['transaction']['state']=='REVOKED'
    asyncio.run(asyncio.wait_for(scenario(),150))


def test_global_children_wait_for_receipt_admission_before_rpc_or_claim(monkeypatch,tmp_path):
    runner=implementation();original_line=runner.base.line;observed=[];workspace={}
    async def inspect_line(child,end):
        row=await original_line(child,end)
        if row.get('event')=='public_connected':
            await asyncio.sleep(.08)
            methods=workspace['path']/'dummy-methods.json'
            calls=json.loads(methods.read_text()) if methods.exists() else []
            assert [item['method'] for item in calls]==([] if row['role']=='owner' else ['initialize','initialized','thread/start','turn/start'])
            for name in ('batch-intent.json','batch-send-claim.json','delivery-owner-binding.json'):
                assert not (workspace['path']/'go-task'/name).exists()
            observed.append(row['role'])
        return row
    monkeypatch.setattr(runner.base,'line',inspect_line)
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw,tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as sup,SyntheticEndpoint() as endpoint:
            prepared,plan=prepare(runner,Path(raw),endpoint,tmp_path,monkeypatch,Path(sup));workspace['path']=prepared.spec.workspace
            result=await runner.run_global(plan,prepared)
            assert observed==['owner','helper']
            assert result['status']=='passed',result.get('failure')
            assert result['ack']['records'][0]['effect_count']==1
            assert result['owner_admission']['lease_id']==result['lease']['lease_id']==result['helper_admission']['lease_id']
    asyncio.run(asyncio.wait_for(scenario(),150))


@pytest.mark.parametrize('fault',['owner-missing','owner-frontend','owner-socket','helper-missing','helper-grant','helper-frontend'])
def test_receipt_gate_failure_has_no_client_rpc_or_claim(monkeypatch,tmp_path,fault):
    runner=implementation();read=runner.ReceiptStore.read;children=runner.ReceiptStore.children;admit=runner.admit_public_child
    async def bounded(*args,**kwargs):
        values=list(args);values[7]=min(values[7],runner.now()+1)
        return await admit(*values,**kwargs)
    monkeypatch.setattr(runner,'admit_public_child',bounded)
    def filtered(store):
        names=children(store)
        return [n for n in names if not (fault=='owner-missing' and n.startswith('owner-connected-'))]
    monkeypatch.setattr(runner.ReceiptStore,'children',filtered)
    def changed(store,name):
        if fault=='helper-missing' and name.startswith('helper-ready-'):return None
        found=read(store,name)
        if found is None:return None
        row,digest=found;row=json.loads(json.dumps(row))
        if fault=='owner-frontend' and name.startswith('owner-connected-'):row['frontend_peer']['birth']='wrong-frontend'
        if fault=='owner-socket' and name.startswith('owner-connected-'):row['private_socket_identity'][1]+=1
        if fault=='helper-grant' and name.startswith('helper-ready-'):row['grant_sha256']='f'*64
        if fault=='helper-frontend' and name.startswith('helper-ready-'):row['frontend_peer']['birth']='wrong-frontend'
        return row,digest
    monkeypatch.setattr(runner.ReceiptStore,'read',changed)
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw,tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as sup,SyntheticEndpoint() as endpoint:
            prepared,plan=prepare(runner,Path(raw),endpoint,tmp_path,monkeypatch,Path(sup))
            result=await runner.run_global(plan,prepared)
            assert result['status']=='unknown' and not result['business_ack_verified']
            methods=prepared.spec.workspace/'dummy-methods.json'
            calls=json.loads(methods.read_text()) if methods.exists() else []
            assert [item['method'] for item in calls]==([] if fault.startswith('owner-') else ['initialize','initialized','thread/start','turn/start'])
            for name in ('batch-intent.json','batch-send-claim.json','delivery-owner-binding.json'):
                assert not (prepared.spec.workspace/'go-task'/name).exists()
    asyncio.run(asyncio.wait_for(scenario(),40))
