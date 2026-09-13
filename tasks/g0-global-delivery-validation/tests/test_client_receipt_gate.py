"""Real independent FD service + pipe client; never a launchd transaction.

Only the child socket metadata is simulated. The manifest selects the protocol
being tested; run_global and its real-mode scheduler freeze are never bypassed.
"""
import asyncio,json,os,tempfile
from dataclasses import replace
from pathlib import Path
import pytest
from test_global_delivery_case import ROOT,PYTHON,prepare,implementation,SyntheticEndpoint


@pytest.mark.parametrize('variant',['launchd','inherited_fd','receipt_sha256','grant_sha256','manifest_sha256','lease_id','parent-birth'])
def test_actual_client_requires_receipt_before_rpc_with_listener_provenance(monkeypatch,tmp_path,variant):
    runner=implementation();monkeypatch.setattr(runner.base,'CLIENT',ROOT/'tasks/g0-global-delivery-validation/tests/fixtures/listener_client.py')
    mode='inherited_fd' if variant=='inherited_fd' else 'launchd'
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw,tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as sup,SyntheticEndpoint() as endpoint:
            prepared,plan=prepare(runner,Path(raw),endpoint,tmp_path,monkeypatch,Path(sup))
            manifest,_=runner.base.activation_service._private_json(plan.manifest_path)
            manifest['external_admission']={'version':1,'mode':mode}
            manifest_path=plan.case/'client-gate-service.json';runner.write(manifest_path,manifest);manifest_sha=runner.sha(manifest_path)
            spec=replace(plan.spec,manifest_path=manifest_path,manifest_sha256=manifest_sha,
                program_arguments=(str(PYTHON),'-I','-B',str(runner.ENTRYPOINT),'--manifest',str(manifest_path)))
            scheduler=runner.InheritedFdScheduler(spec,runner.now);scheduler.phase_end=runner.now()+10
            store=runner.ReceiptStore(plan.state_dir);child=None;peer=None;out=None
            try:
                scheduler.bootstrap('fake',spec.plist_path);end=runner.now()+8
                started,_=await runner.receipt(store,'service-start.json',end);service=started['service_identity']
                assert runner.peer_matches(runner.base.peer_for_process(service['pid']),service)
                prepared.start_endpoint()
                env=runner.auth_isolation.build_clean_environment(prepared.spec,{})
                child,peer,out=await runner.spawn_client(plan,prepared,'owner',env,end,end)
                grant={'version':1,'pid':child.pid,'uid':os.getuid(),'birth':peer['birth'],'expected_executable':str(PYTHON),
                    'executable_sha256':runner.sha(PYTHON),'profile_id':prepared.spec.profile_id}
                cfg=runner.config(plan,prepared,'owner',peer,service,runner.base.transport._socket_identity(spec.socket_path),grant,end,end,end)
                cfg.update(manifest_path=str(manifest_path),manifest_sha256=manifest_sha,external_admission=manifest['external_admission'])
                if variant=='parent-birth':cfg['controller_peer']={**cfg['controller_peer'],'birth':'wrong-parent-birth'}
                await runner.base.send(child,cfg,end)
                methods=prepared.spec.workspace/'dummy-methods.json'
                if mode=='inherited_fd' or variant=='parent-birth':
                    with pytest.raises(runner.base.SafeClientFailure):await runner.base.line(child,end)
                    assert not methods.exists();return
                connected=await runner.base.line(child,end)
                assert connected['event']=='public_connected' and connected['public_peer']['uid']==0 and connected['public_peer']['pid']==1
                await asyncio.sleep(.08);assert not methods.exists()
                names=[n for n in store.children() if n.startswith('owner-connected-') and n.endswith(f'-{child.pid}.json')]
                assert len(names)==1
                row,digest=await runner.receipt(store,names[0],end)
                assert row['manifest_sha256']==manifest_sha and row['grant_sha256']==cfg['grant_sha256']
                runner.check_peer(row['frontend_peer'],peer)
                native=runner.check_connected_backend(row,prepared,service);runner.check_peer(row['backend_peer'],native)
                admitted={'action':'admitted','role':'owner','lease_id':row['lease_id'],'activation_id':store.activation_id,
                    'receipt_name':names[0],'receipt_sha256':digest,'manifest_sha256':manifest_sha,'grant_sha256':cfg['grant_sha256']}
                if variant in ('receipt_sha256','grant_sha256','manifest_sha256'):admitted[variant]='f'*64
                if variant=='lease_id':
                    admitted['lease_id']='f'*12;admitted['receipt_name']=f"owner-connected-{'f'*12}-{child.pid}.json"
                await runner.base.send(child,admitted,end)
                if variant!='launchd':
                    with pytest.raises(runner.base.SafeClientFailure):await runner.base.line(child,end)
                    assert not methods.exists();return
                ready=await runner.base.line(child,end)
                assert ready['event']=='thread_ready' and ready['public_peer']==connected['public_peer']
                assert [x['method'] for x in json.loads(methods.read_text())]==['initialize','initialized','thread/start']
                await runner.base.send(child,{'action':'initial_turn'},end);assert (await runner.base.line(child,end))['event']=='initial_ready'
                await runner.base.send(child,{'action':'close'},end);assert (await runner.base.line(child,end))['event']=='owner_closed'
            finally:
                if child is not None:await runner.base.stop_client(child,runner.now()+3,peer)
                scheduler.phase_end=runner.now()+10;scheduler.bootout('fake',spec.plist_path)
                store.close();endpoint.close()
                if out is not None:await out
    asyncio.run(asyncio.wait_for(scenario(),40))
