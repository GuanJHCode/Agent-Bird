"""Explicit private native TUI → restart → same-owner Go delivery experiment.

prepare builds/freezes only; run is a separate root-authorized operation. This
module never calls a real launchctl scheduler and has no automatic retry.
"""
from __future__ import annotations
import asyncio,hashlib,os,signal,sys
from pathlib import Path
ROOT=Path(__file__).resolve().parents[3]
sys.path.insert(0,str(ROOT/'tasks/g0-global-delivery-validation/scripts'))
import global_delivery_case as g
import native_tui_session as session
POLICY={'version':1,'resume_owned_thread':True,'initial_history_discovery':True}


def prepare_tui_case(prepared,case_dir,**kwargs):
    return g.prepare_global(prepared,case_dir,**kwargs,scheduler_mode='inherited_fd',native_tui_policy=POLICY,
        expected_source_pins=g.source_pins(POLICY))


async def service_start(plan,store,end,lower_bound,expected_pid=None):
    row,digest=await g.receipt(store,'service-start.json',end);g.common(row,store,plan)
    peer=row['service_identity']
    g.require(peer['uid']==os.getuid() and peer['executable']==str(plan.python) and peer['executable_sha256']==g.sha(plan.python),'service artifact mismatch')
    g.require(g.peer_matches(g.base.peer_for_process(peer['pid']),peer),'service kernel mismatch')
    plan.scheduler.phase_end=end
    g.require(plan.transaction.verify()['job'].get('pid')==peer['pid'] and (expected_pid is None or expected_pid==peer['pid']),'service job mismatch')
    g.require(lower_bound*1e9<=row['started_raw_ns']<=g.now()*1e9,'service start outside launch window')
    return peer,digest


def task_pins(task):
    return {path.name:g.sha(path) for path in task.iterdir() if path.is_file()}


async def run_tui_case(plan,prepared):
    g.require(plan.scheduler_mode=='inherited_fd' and plan.native_tui_policy==POLICY,'private native TUI policy required')
    g.frozen(plan,prepared);case_fd=g.base.transport._open_case(plan.case,plan.case_identity)
    try:g.base.transport._write(case_fd,'attempt.json',{'version':1,'source_pins':plan.source_hashes,'native_tui':True,'real_launchctl_used':False})
    except BaseException:os.close(case_fd);raise
    result={'status':'unknown','real_launchctl_used':False,'global_registration_verified':False,'ordinary_tui_verified':False,
        'durable_history_verified':False,'business_ack_verified':False,'external_model_calls':0}
    sessions=[];outputs=[];stores=[];store=g.ReceiptStore(plan.state_dir,pending_publication=True);stores.append(store)
    helper=helper_peer=service_peer=lease=alias=alias_identity=None;helper_live=False;total=None;stage='register';closed=[]
    try:
        def register():plan.transaction.prepare();return plan.transaction.register()
        registration=g.transaction_phase(plan,'register',register,g.REGISTER_SECONDS)
        result['registration']=registration;start=g.now();total=start+120;business=total-10
        result.update(started_raw=start,total_deadline_raw=total,business_deadline_raw=business)
        end=min(business,g.now()+10);stage='service-start'
        service_peer,digest=await service_start(plan,store,end,registration['started_raw'])
        result['service_identity']=service_peer;result['service_start_sha256']=digest
        alias=session.probe.install_default_socket_alias(prepared.context,plan.spec.socket_path)
        info=alias.lstat();alias_identity=(info.st_dev,info.st_ino)
        prepared.start_endpoint();env=g.auth_isolation.build_clean_environment(prepared.spec,{})
        stage='tui-start';tui=session.TuiSession(plan,prepared);sessions.append(tui);tui.start(end)
        lease,ready_sha,ready=await tui.ready(store,service_peer,end);result['first_lease']=lease;first_lease=lease;first_peer=tui.peer
        thread_id=lease['owner_thread_id'];result['tui_thread_id']=thread_id
        await tui.status(thread_id,end)
        g.remaining(business);tui.driver.command('G0_SYNTHETIC_READY');stage='initial-turn';await tui.text('READY',business)
        g.require(prepared.endpoint.snapshot()['accepted_requests']==1,'initial response count differs')
        task=prepared.spec.workspace/'go-task';task.mkdir(mode=0o700);nonce=plan.case.name;env['CODEX_THREAD_ID']=thread_id
        stage='go-task';result['go_job']=await g.base.go_within(plan.go_binary,['start','--dir',str(task),'--nonce',nonce,'--delay','500ms','--controller-thread',thread_id],env,business)
        done=await g.base.go_within(plan.go_binary,['wait','--dir',str(task),'--nonce',nonce,'--timeout','2s'],env,business)
        g.require(done.get('status')=='completed','Go task incomplete')
        inspected=await g.base.go_within(plan.go_binary,['inspect','--dir',str(task),'--nonce',nonce],env,business)
        g.require(inspected.get('controller_thread')==thread_id and inspected.get('event_hash'),'Go owner mismatch')
        pending=task_pins(task);g.require(not any(k in pending for k in ('batch-send-claim.json','batch-intent.json')),'delivery already claimed')
        end=min(business,g.now()+10);stage='first-quit';await tui.quit(end);await tui.cleanup(end)
        final,backend_sha=await g.receipt(store,f"backend-{lease['lease_id']}.json",end)
        closed.append(g.backend_projection(final,lease,first_peer,None,require_helper=False))
        stopped=await g.stop_service(service_peer,store,plan,end,lease,first_peer,None,False)
        result['first_service_final'],service_final_sha=stopped
        proof={'activation_id':store.activation_id,'lease_id':lease['lease_id'],'ready_sha256':ready_sha,'backend_sha256':backend_sha}
        g.write(plan.case/'resume-owner-proof.json',proof);result['resume_owner_proof']=proof
        old_activation=store.activation_id;old_peer=service_peer;store.close();service_peer=None;lease=None
        stage='service-successor';end=min(business,g.now()+10);restart_start=g.now();plan.scheduler.phase_end=end
        successor_pid=plan.scheduler.activate_successor(old_activation,service_final_sha)
        while True:
            g.remaining(end);names=os.listdir(plan.state_dir)
            new=[n for n in names if n!=old_activation]
            g.require(len(names)<=2 and len(new)<=1,'ambiguous successor activation')
            if new:break
            await asyncio.sleep(.01)
        store=g.ReceiptStore(plan.state_dir,activation_id=new[0],pending_publication=True);stores.append(store)
        service_peer,digest=await service_start(plan,store,end,restart_start,successor_pid)
        g.require(service_peer['pid']!=old_peer['pid'] and g.process_gone(old_peer),'service did not restart')
        result['successor_service_identity']=service_peer;result['successor_service_start_sha256']=digest
        stage='tui-resume';tui=session.TuiSession(plan,prepared);sessions.append(tui);tui.start(end,resume_thread_id=thread_id,resume_proof=proof)
        lease,ready_sha,ready=await tui.ready(store,service_peer,end)
        g.require(lease['owner_thread_id']==thread_id and lease['owner_context_sha256']==first_lease['owner_context_sha256'] and lease['lease_id']!=first_lease['lease_id'],'resumed owner changed')
        g.require(ready['thread'].get('resume_barrier') is True,'resume response barrier absent')
        result['resumed_lease']=lease;result['resumed_thread_id']=thread_id
        await tui.status(thread_id,end)
        g.require(prepared.endpoint.snapshot()['accepted_requests']==1 and ready['zero_turns'],'resume started a model turn')
        result['resume_zero_turns']=True;g.require(task_pins(task)==pending,'pending task changed while closed');result['pending_delivery_preserved']=True
        g.write(plan.case/'controller-delivery-decision.json',{'action':'deliver_pending_result','thread_id':thread_id,'owner_context_sha256':lease['owner_context_sha256'],'lease_id':lease['lease_id']})
        stage='helper-start';end=min(business,g.now()+10);helper,helper_peer,out=await g.spawn_client(plan,prepared,'helper',env,end,total);outputs.append(out)
        grant={k:lease[k] for k in ('profile_id','owner_context_sha256','lease_id','owner_connection_id','owner_epoch','owner_thread_id','private_socket')}
        grant.update(version=1,role='owner-helper',helper_pid=helper.pid,helper_uid=os.getuid(),helper_birth=helper_peer['birth'],
            helper_executable=str(plan.python),helper_executable_sha256=g.sha(plan.python),helper_source_sha256=g.sha(g.base.owner_helper.__file__))
        config=g.config(plan,prepared,'helper',helper_peer,service_peer,g.base.transport._socket_identity(plan.spec.socket_path),grant,business,end,total)
        config.update(thread_id=thread_id,initial_turn_id=None,lease_id=lease['lease_id'],owner_epoch=lease['owner_epoch'],go_binary=str(plan.go_binary),go_sha256=plan.go_sha256,
            task_dir=str(task),nonce=nonce,delivery_id='G0_SYNTHETIC_TOOL_RESULT_'+nonce,
            events=[{'event_id':'event_result','event_revision':1,'kind':'result','payload_hash':inspected['event_hash'],'action_slot':'ack_result'}])
        await g.base.send(helper,config,end);stage='helper-admission'
        _,admission=await g.admit_public_child(plan,prepared,store,helper,helper_peer,config,service_peer,end,lease);helper_live=True
        result['helper_admission']=admission
        async def drain_tui():
            while tui.driver is not None:tui.drain();await asyncio.sleep(.01)
        drain=asyncio.create_task(drain_tui())
        try:
            stage='helper-proof';history=await g.base.line(helper,business)
            checks={'event':history.get('event')=='history_proof','thread':history.get('thread_id')==thread_id,
                'lease':history.get('lease_id')==lease['lease_id'],'initial_history':bool(history.get('initial_history_sha256')),
                'grant':history.get('grant_sha256')==config['grant_sha256'],'manifest':history.get('manifest_sha256')==plan.manifest_sha256,
                'peer':history.get('public_peer')==admission['public_peer'],'events':history.get('matched_event_ids')==['event_result']}
            result['proof_checks']=checks;g.require(all(checks.values()),'helper proof differs')
            result['helper_proof']=history
            g.require(prepared.endpoint.snapshot()['accepted_requests']==2,'synthetic count differs')
            decision={'action':'decide','reads_sha256':history['reads_sha256'],'decisions':{'event_result':'handled'},'command_ids':{'event_result':'handled_'+nonce}}
            g.frozen(plan,prepared);g.write(plan.case/'controller-handled-decision.json',decision)
            stage='go-ack';await g.base.send(helper,decision,business);ack=await g.base.line(helper,business)
            g.require(ack.get('event')=='ack' and ack.get('business_ack_complete') and len(ack['records'])==1 and ack['records'][0].get('effect_count')==1,'Go ACK incomplete')
            result['ack']=ack;result['business_ack_verified']=True
            result['go_status']=await g.base.go_within(plan.go_binary,['batch-status','--dir',str(task),'--nonce',nonce],env,business)
            g.require(result['go_status']['send_allowed'] is False and result['go_status']['business_status']=='complete','Go final incomplete')
            stage='final-quit';end=min(total,g.now()+10);await tui.text('SYNTHETIC_COMPLETE',end);await tui.status(thread_id,end)
            await tui.quit(end);sent=tui.record['quit_sent_raw'];await tui.cleanup(end)
            eof=await g.base.line(helper,end);g.require(eof.get('event')=='helper_eof' and eof['eof_raw']>=sent,'helper EOF invalid')
            result['normal_close']={'quit_sent_raw':sent,'helper_eof_raw':eof['eof_raw']}
        finally:drain.cancel();await asyncio.gather(drain,return_exceptions=True)
        result['status']='passed';result['ordinary_tui_verified']=True
    except Exception as exc:
        result['failure']={'stage':stage,'type':type(exc).__name__,'message_sha256':hashlib.sha256(str(exc).encode()).hexdigest()}
        if isinstance(exc,g.base.SafeClientFailure):result['failure']['child_error']=exc.child_error
    finally:
        end=min(total,g.now()+10) if total else g.now()+10;cleanup={'all_backends_stopped':False,'service_stopped':False,'restart_allowed':False}
        for tui in sessions:
            try:await tui.cleanup(end)
            except Exception as exc:result['status']='unknown';cleanup['tui_failure']=type(exc).__name__
        result['tui_sessions']=[t.record for t in sessions];cleanup['tui_exits']=[t.record.get('exit_code') for t in sessions]
        try:
            cleanup['helper_exit']=await g.base.stop_client(helper,end,helper_peer)
            if result['status']=='passed':g.require(cleanup['tui_exits']==[0,0] and cleanup['helper_exit']==0,'client exit mismatch')
        except Exception:result['status']='unknown';cleanup['helper_stop_unproven']=True
        try:
            if lease is not None:
                final,digest=await g.receipt(store,f"backend-{lease['lease_id']}.json",end)
                closed.append(g.backend_projection(final,lease,sessions[-1].peer,helper_peer,require_helper=helper_live))
            if service_peer is not None:
                stopped=await g.stop_service(service_peer,store,plan,end,lease,sessions[-1].peer if sessions else None,helper_peer,helper_live)
                if stopped:result['final_service'],result['final_service_sha256']=stopped
            cleanup.update(service_stopped=service_peer is None or g.process_gone(service_peer),backend_count=len(closed),all_backends_stopped=len(closed)==2)
        except Exception as exc:result['status']='unknown';cleanup['service_failure']=type(exc).__name__
        try:result['revocation']=g.transaction_phase(plan,'revoke',plan.transaction.revoke,g.REVOKE_SECONDS)
        except Exception as exc:result['status']='unknown';cleanup['revoke_failure']=type(exc).__name__
        result['transaction']=dict(plan.transaction.receipt);cleanup['public_socket_removed']=not os.path.lexists(plan.spec.socket_path)
        if result['transaction']['state']!='REVOKED' or not cleanup['public_socket_removed']:result['status']='unknown'
        try:
            if alias is not None:session.probe.remove_default_socket_alias(prepared.context,plan.spec.socket_path,alias_identity)
            g.frozen(plan,prepared);result['postflight']={'fixture_valid':True,'source_pins_valid':True}
        except Exception:result['status']='unknown';result['postflight']={'verified':False}
        result['synthetic_endpoint']=prepared.endpoint.snapshot()
        try:prepared.endpoint.close()
        except Exception:result['status']='unknown';cleanup['endpoint_close_unproven']=True
        try:result['client_output_digests']=await asyncio.wait_for(asyncio.gather(*outputs),1)
        except Exception:result['status']='unknown';cleanup['client_output_unproven']=True
        for item in stores:item.close()
        result.update(cleanup=cleanup,closed_backends=closed,finished_raw=g.now())
        try:g.base.transport._write(case_fd,'result.json',result)
        finally:os.close(case_fd)
    return result
