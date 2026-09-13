"""Actual isolated FD service, PTY processes, HTTP and Go; no Codex/launchctl."""
import asyncio,hashlib,json,os,sys,tempfile
from pathlib import Path
import pytest
ROOT=Path(__file__).resolve().parents[3]
sys.path[:0]=[str(ROOT/'tasks/g1-g4-delivery/scripts'),str(ROOT/'tasks/g0-global-delivery-validation/tests')]
from test_global_delivery_case import SyntheticEndpoint,PYTHON,GO


@pytest.mark.parametrize('fault',[None,'resume-proof','initial-history','wire-notice'])
def test_native_tui_quit_resume_same_thread_then_single_go_ack(monkeypatch,tmp_path,fault):
    assert (ROOT/'tasks/g1-g4-delivery/scripts/native_tui_case.py').exists(),'native PTY delivery glue missing'
    import native_tui_case as runner
    async def scenario():
        with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as root,tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as supervisor,SyntheticEndpoint() as endpoint:
            from test_global_delivery_case import prepare
            g=runner.g
            monkeypatch.setattr(g,'ENTRYPOINT',ROOT/'tasks/g1-g4-delivery/tests/fixtures/tui_service.py')
            monkeypatch.setattr(g.base.transport,'NATIVE_SHA256',g.sha(PYTHON))
            # Reuse the real profile/inventory factory, selecting the reviewed
            # optional mode at the existing prepare boundary.
            original_prepare=g.prepare_global
            def configured(*args,**kwargs):
                g.ENTRYPOINT=ROOT/'tasks/g1-g4-delivery/tests/fixtures/tui_service.py'
                kwargs.update(native_tui_policy=runner.POLICY,expected_source_pins=g.source_pins(runner.POLICY))
                return original_prepare(*args,**kwargs)
            monkeypatch.setattr(g,'prepare_global',configured)
            prepared,plan=prepare(g,Path(root),endpoint,tmp_path,monkeypatch,Path(supervisor))
            # prepare() sets its normal dummy entrypoint; use a direct fixture
            # path override only when invoked by that test helper.
            assert plan.native_tui_policy==runner.POLICY
            spawn=runner.session.probe._spawn_pty_gate
            def dummy_tui(argv,*args):
                argv=list(argv);i=argv.index('--');argv[i+1:i+1]=['-I','-B',str(ROOT/'tasks/g1-g4-delivery/tests/fixtures/dummy_tui.py')]
                return spawn(argv,*args)
            monkeypatch.setattr(runner.session.probe,'_spawn_pty_gate',dummy_tui)
            if fault=='resume-proof':
                start=runner.session.TuiSession.start
                def invalid_proof(tui,end,**kwargs):
                    if kwargs.get('resume_proof'):kwargs['resume_proof']={**kwargs['resume_proof'],'backend_sha256':'f'*64}
                    return start(tui,end,**kwargs)
                monkeypatch.setattr(runner.session.TuiSession,'start',invalid_proof)
            if fault=='initial-history':(prepared.spec.workspace/'dummy-mode.txt').write_text('initial-ambiguous')
            if fault=='wire-notice':(prepared.spec.workspace/'dummy-mode.txt').write_text('wire-notice')
            result=await runner.run_tui_case(plan,prepared)
            if fault:
                assert result['status']=='unknown' and not result['business_ack_verified']
                assert result['failure']['stage']==('tui-resume' if fault=='resume-proof' else 'helper-proof'),result['failure']
                assert result['synthetic_endpoint']['accepted_requests']==1
                for name in ('batch-intent.json','batch-send-claim.json','delivery-owner-binding.json'):
                    assert not (prepared.spec.workspace/'go-task'/name).exists()
                assert result['cleanup']['public_socket_removed'] and result['transaction']['state']=='REVOKED'
                if fault=='wire-notice':
                    finals=[json.loads(path.read_text()) for path in (plan.case/'state').glob('*/activation.json')]
                    rows=[row for final in finals for row in final.get('wire_rejections',[])]
                    assert len(rows)==1,rows
                    row=rows[0]
                    assert row['stage']=='helper_wire_server' and row['reason']=='server_notification_forbidden'
                    assert row['failure_type']=='OwnerHelperAdmissionError' and row['frame_opcode']==1
                    assert row['frame_payload_bytes']>0 and row['method_sha256']==hashlib.sha256(b'private-canary-notification-do-not-export').hexdigest()
                    assert row['frontend_pid']>0
                    assert row['connection_id'] and row['epoch']>0
                    assert 'private-canary-notification-do-not-export' not in json.dumps(finals)
                return
            assert result['status']=='passed',(result.get('failure'),result.get('proof_checks'))
            assert result['tui_thread_id']==result['resumed_thread_id']
            assert result['first_lease']['lease_id']!=result['resumed_lease']['lease_id']
            assert sum(e['method']=='thread/turns/list' and e['direction']=='client' for e in result['helper_proof']['envelopes'])==2
            assert result['ack']['records'][0]['effect_count']==1
            assert result['synthetic_endpoint']['accepted_requests']==2
            assert result['resume_zero_turns'] and result['pending_delivery_preserved']
            assert result['cleanup']['tui_exits']==[0,0] and result['cleanup']['backend_count']==2
            assert result['cleanup']['all_backends_stopped'] and result['cleanup']['service_stopped']
            assert result['transaction']['state']=='REVOKED' and not result['real_launchctl_used']
            assert result['go_status']['send_allowed'] is False
            publications=[json.loads(line)['name'] for line in (plan.case/'published-finals.jsonl').read_text().splitlines()]
            assert publications.count('activation.json')==2
            assert sum(name.startswith('backend-') for name in publications)==2
    asyncio.run(asyncio.wait_for(scenario(),150))
