"""Local guards for the single owned native case; no Codex/model invocations."""
import unittest
import json
import copy
from contextlib import ExitStack
import datetime as dt
import functools
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import time
from unittest.mock import patch
from types import SimpleNamespace
import native_sd_control as control
from native_sd_control import require_owned_pair


class ControlTests(unittest.TestCase):
    def test_only_sd03_is_registered_for_the_new_model_case(self):
        self.assertEqual(control.c.NONCE,'sd-qual-03')
        self.assertEqual(control.CASE.name,'sd-qual-03')
        self.assertEqual(control.JOB.name,'sd-qual-03')
        self.assertIn('SD_QUAL_03_READY',control.fixed_prompt())
        self.assertNotIn('sd-qual-02',control.fixed_prompt())

    def test_discovery_waits_for_one_owned_service_hook_and_rejects_ambiguity(self):
        root='01a08e0b-bd0d-7400-b069-d50978939cdd';turn='01a08e0b-bd0d-7400-b069-d50978939cde'
        since='2026-09-11T10:00:00Z';through='2026-09-11T10:02:00Z'
        with tempfile.TemporaryDirectory() as name,patch.object(control.c,'HOOKS',Path(name)):
            self.assertIsNone(control.discover_prompt_hook(123,since,through))
            value={'session_id':root,'turn_id':turn,'hook_event_name':'UserPromptSubmit','pid':124,'ppid':123,
                   'recorded_at':'2026-09-11T10:00:01Z','extra':'CANARY_PRIVATE_TEXT'}
            for filename,item in [('owned.json',value),('other.json',dict(value,ppid=999))]:
                path=Path(name)/filename;path.write_text(json.dumps(item));os.utime(path,ns=(1789120801000000000,1789120801000000000))
            got=control.discover_prompt_hook(123,since,through)
            self.assertEqual(got['session_id'],root);self.assertEqual(got['turn_id'],turn)
            self.assertNotIn('CANARY_PRIVATE_TEXT',json.dumps(got))
            path=Path(name)/'owned.json';missing=dict(value);missing.pop('turn_id')
            path.write_text(json.dumps(missing));os.utime(path,ns=(1789120801000000000,1789120801000000000))
            self.assertIsNone(control.discover_prompt_hook(123,since,through))
            path.write_text(json.dumps(value));os.utime(path,ns=(1789120801000000000,1789120801000000000))
            path=Path(name)/'second.json';path.write_text(json.dumps(dict(value,session_id=turn)))
            os.utime(path,ns=(1789120801000000000,1789120801000000000))
            with self.assertRaises(ValueError): control.discover_prompt_hook(123,since,through)

    def test_candidate_waits_for_job_then_requires_exact_owner_nonce_revision(self):
        root='01a08e0b-bd0d-7400-b069-d50978939cdd'
        with tempfile.TemporaryDirectory() as name,patch.object(control,'JOB',Path(name)):
            self.assertIsNone(control.read_job_owner(root))
            value={'version':1,'status':'started','nonce':'sd-qual-03','controller_thread':root,'task_revision':1,
                   'worker_pid':123,'created_at':'2026-09-11T10:00:01Z','job_file':'job.json','result_file':'result.json'}
            path=Path(name)/'job.json';path.write_text(json.dumps(value));path.chmod(0o600)
            got=control.read_job_owner(root)
            self.assertEqual(got['controller_thread'],root);self.assertEqual(got['task_revision'],1)
            for change in ({'controller_thread':'01a08e0b-bd0d-7400-b069-d50978939cde'},{'nonce':'sd-qual-02'},
                           {'task_revision':2},{'worker_pid':True},{'created_at':'CANARY_PRIVATE_TEXT'},
                           {'extra':'CANARY_PRIVATE_TEXT'}):
                path.write_text(json.dumps(dict(value,**change)))
                with self.assertRaises(ValueError): control.read_job_owner(root)

    def test_before_helper_refuses_extra_or_unclassified_recorded_clients(self):
        row={'fields':{'transport':'unix_socket','client_name':'codex-tui','client_version':'0.153.4','connection_id':0}}
        got=control.require_single_tui_connection([row])
        self.assertEqual(got['tui_connection_id'],0)
        for extra in ({'client_name':'codex-tui','connection_id':1},{'client_name':'g0-native-standalone','connection_id':1},
                      {'unrecognized_client_name':True,'connection_id':1},{'connection_id':1,'method':'thread/start'}):
            with self.assertRaises(ValueError): control.require_single_tui_connection([row,{'fields':extra}])

    def test_helper_rpc_error_projection_is_typed_and_drops_raw_content(self):
        error={'method':'thread/read','request_id':4,'code':-32601,'message_bytes':31,'message_sha256':'a'*64}
        envelope={'version':1,'status':'rejected','reason':'native_rpc_error','source_service_verified':False,
                  'resend_authorized':False,'absence_proof':False,'rpc_error':error}
        self.assertEqual(control.helper_rpc_error_fields(json.dumps(envelope).encode()),error)
        envelope['status']='uncertain'
        self.assertEqual(control.helper_rpc_error_fields(json.dumps(envelope).encode()),error)
        for change in ({'method':'CANARY_PRIVATE_TEXT'},{'request_id':True},{'request_id':10001},
                       {'code':True},{'code':2**63},{'message_bytes':True},{'message_bytes':65537},
                       {'message_sha256':'CANARY_PRIVATE_TEXT'},{'message':'CANARY_PRIVATE_TEXT'}):
            raw=json.dumps(dict(envelope,rpc_error=dict(error,**change))).encode()
            self.assertIsNone(control.helper_rpc_error_fields(raw))
        self.assertNotIn('CANARY_PRIVATE_TEXT',json.dumps(control.helper_rpc_error_fields(raw)))

    def test_helper_rpc_error_projection_rejects_ambiguous_or_unbound_envelopes(self):
        error={'method':'thread/read','request_id':4,'code':-32601,'message_bytes':31,'message_sha256':'a'*64}
        envelope={'version':1,'status':'rejected','reason':'native_rpc_error','source_service_verified':False,
                  'resend_authorized':False,'rpc_error':error}
        for change in ({'version':True},{'status':'prepared'},{'reason':'invalid_data'},
                       {'source_service_verified':True},{'resend_authorized':True},{'absence_proof':True},
                       {'extra':'CANARY_PRIVATE_TEXT'}):
            self.assertIsNone(control.helper_rpc_error_fields(json.dumps(dict(envelope,**change)).encode()))
        raw=json.dumps(envelope).encode().replace(b'"code": -32601',b'"code": 1, "code": -32601')
        self.assertIsNone(control.helper_rpc_error_fields(raw))
        self.assertIsNone(control.helper_rpc_error_fields(b'{"CANARY_PRIVATE_TEXT":'))
        self.assertIsNone(control.helper_rpc_error_fields(b' '*65537))

    def test_quit_actions_and_io_share_original_ten_second_window(self):
        for costs,read_cost,normal,actions in (([6000000000,3000000000],500000000,True,['quit-paste','quit-submit']),
                                              ([10000000001,0],0,False,['quit-paste']),
                                              ([4000000000,4000000000],3000000000,False,['quit-paste','quit-submit'])):
            with self.subTest(costs=costs,read_cost=read_cost):
                raw=[0]; sent=[]; exit_code=[None]
                def send(sequence,action):
                    sequence.take(action);raw[0]+=costs[len(sent)];sent.append(action)
                def read(seconds): raw[0]+=read_cost;exit_code[0]=0
                tui=SimpleNamespace(send=send,read=read,_poll=lambda:exit_code[0])
                with patch.object(control,'continuous_ns',side_effect=lambda:raw[0]):
                    result=control.quit_with_deadline(tui,control.InputSequence('fixed'))
                self.assertEqual(sent,actions)
                self.assertEqual(result['deadline_mono_ns'],10000000000)
                self.assertEqual(result['normal_exit_within_window'],normal)

    def test_guard_requires_executable_path_for_each_native_role(self):
        original=json.loads((control.DOCS/'native-sd-qual-02-controller.json').read_text())['origin']
        for role in ('service','tui'):
            with self.subTest(role=role):
                with self.assertRaises(ValueError): control.validate_process_identity(original[role])

    def test_sd02_comm_binding_uses_actual_collector_path_and_requires_it(self):
        # Clone the frozen actual sd02 identities. Only libproc/ps are fixture substitutes;
        # no new path is written into either original record or the binding by this test.
        captured=json.loads((control.DOCS/'native-sd-qual-02-controller.json').read_text())['origin']
        observed_path=str(control.c.CLI.resolve())
        def proc_pidpath(pid,buffer,size):
            buffer.value=observed_path.encode(); return len(buffer.value)
        for role in ('service','tui'):
            original=captured[role]
            row=f"{original['pid']} {original['ppid']} {original['uid']} {original['started_at_local']} {original['comm']}\n"
            def ps(args,**kwargs):
                return SimpleNamespace(returncode=0,stdout=original['comm']+'\n' if args[-1]=='comm=' else row)
            with self.subTest(role=role),patch.object(control.c.ctypes,'CDLL',return_value=SimpleNamespace(proc_pidpath=proc_pidpath)),patch.object(control.c.subprocess,'run',side_effect=ps):
                current=control.c.process(original['pid'])
                result=control.binding_process(current)
                self.assertEqual(result['comm'],'codex')
                self.assertEqual(result.get('executable_path'),observed_path)
                for bad in (dict(current,executable_path='/unrelated/codex'),dict(current,executable_path=None)):
                    with self.assertRaises(ValueError): control.binding_process(bad)
                missing=dict(current);missing.pop('executable_path',None)
                with self.assertRaises(ValueError): control.binding_process(missing)

    def test_service_probe_crossing_original_deadline_cannot_start_tui(self):
        identity={'pid':101,'ppid':100,'uid':os.getuid(),'comm':'codex','native_executable_matches':True,
                  'started_at_local':'Fri Sep 11 18:00:00 2026'}
        socket={'path':str(control.c.SOCKET),'exists':True,'dev':1,'ino':2,'uid':os.getuid(),
                'mode':'0600','service_holds_path':True}
        service=SimpleNamespace(pid=101,read=lambda seconds:None,_poll=lambda:None,
                                evidence=lambda:{'pid':101,'exit_code':None})
        for delayed_probe in ('process','socket'):
            with self.subTest(delayed_probe=delayed_probe),tempfile.TemporaryDirectory() as name,ExitStack() as patches:
                base=Path(name); executable=base/'fixture'; executable.write_text('fixture')
                docs=base/'docs'; docs.mkdir(); raw=[0]
                def process(pid):
                    if delayed_probe=='process': raw[0]=10000000000
                    return dict(identity,executable_path=str(executable.resolve()))
                def snapshot(pid=None):
                    if pid is None: return {'exists':False}
                    if delayed_probe=='socket': raw[0]=10000000000
                    return socket
                for attr,value in (('CASE',base/'case'),('JOB',base/'job'),('DOCS',docs),
                                   ('HELPER',executable),('AUDIT',executable)):
                    patches.enter_context(patch.object(control,attr,value))
                for attr,value in (('CLI',executable),('GO',executable),('CLI_SHA','0'*64),('GO_SHA','0'*64)):
                    patches.enter_context(patch.object(control.c,attr,value))
                patches.enter_context(patch.object(control.c,'sha',return_value='0'*64))
                patches.enter_context(patch.object(control.c,'protected_files',return_value={}))
                patches.enter_context(patch.object(control.c,'process',side_effect=process))
                patches.enter_context(patch.object(control.c,'snapshot_socket',side_effect=snapshot))
                patches.enter_context(patch.object(control,'continuous_ns',side_effect=lambda:raw[0]))
                patches.enter_context(patch.object(control,'OwnedService',return_value=service))
                tui=patches.enter_context(patch.object(control,'NativePTY',side_effect=ValueError('unexpected TUI start')))
                patches.enter_context(patch.object(control.subprocess,'run',return_value=subprocess.CompletedProcess(
                    [],0,stdout='01a08e0b-bd0d-7400-b069-d50978939cde\n')))
                patches.enter_context(patch('builtins.print'))
                self.assertEqual(control.run('0'*64,'0'*64),2)
                tui.assert_not_called()
                result=json.loads((base/'case/controller-result.json').read_text())
                self.assertEqual(result['failure']['code'],'continuous_deadline_exceeded')
                self.assertEqual(result['failure']['substage'],'service_wait_socket')

    def test_failure_record_keeps_fixed_code_and_clock_without_exception_text(self):
        try:
            control.c.window('2026-09-11T10:00:00Z','2026-09-11T10:02:01Z',dt.datetime(2026,9,11,10,3,tzinfo=dt.timezone.utc))
        except ValueError as error:
            got=control.failure_fields(error,'initial_hook_scan')
        self.assertEqual(got.get('code'),'epoch_correlation_inconclusive')
        self.assertEqual(got.get('substage'),'initial_hook_scan')
        other=control.failure_fields(ValueError('CANARY_PRIVATE_TEXT'),'CANARY_PRIVATE_TEXT')
        self.assertNotIn('CANARY_PRIVATE_TEXT',json.dumps(other))

    def test_continuous_attempt_ignores_wall_jump_but_rejects_legacy_clock(self):
        root='01a08e0b-bd0d-7400-b069-d50978939cdd'; boot='01a08e0b-bd0d-7400-b069-d50978939cde'
        raw=time.clock_gettime_ns(time.CLOCK_MONOTONIC_RAW)
        value={'version':1,'status':'send_intent','nonce':control.c.NONCE,'controller_thread_id':root,
               'binding_sha256':'0'*64,'payload_sha256':'1'*64,'boot_id':boot,
               'clock_impl':'clock_gettime_ns(CLOCK_MONOTONIC_RAW)','mono_ns':raw-1000000000,
               'wall_ns':time.time_ns()+1000000000000000}
        with tempfile.TemporaryDirectory() as directory,patch.object(control,'CASE',Path(directory)):
            path=Path(directory)/'send-attempt.json'; path.write_text(json.dumps(value)); path.chmod(0o600)
            try: result=control.read_send_attempt(root,boot,'0'*64,raw-2000000000)
            except ValueError: result=None
            self.assertIsNotNone(result)
            self.assertEqual(result['deadline_mono_ns'],raw+119000000000)
            value['clock_impl']='mach_absolute_time()'; path.write_text(json.dumps(value))
            with self.assertRaises(ValueError): control.read_send_attempt(root,boot,'0'*64,raw-2000000000)

    def test_zero_exit_without_same_case_send_attempt_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory,patch.object(control,'CASE',Path(directory)):
            completed=subprocess.run([sys.executable,'-c','pass'],capture_output=True)
            self.assertEqual(completed.returncode,0)
            with self.assertRaises(ValueError):
                control.require_helper_success('send-once',completed.returncode,
                    '01a08e0b-bd0d-7400-b069-d50978939cdd','01a08e0b-bd0d-7400-b069-d50978939cde','0'*64,time.monotonic_ns())

    def test_lab_read_envelope_is_fixed_nonce_and_does_not_export_extensions(self):
        expected={'version':1,'status':'pending','nonce':'sd-qual-03','count':0}
        self.assertEqual(control.lab_read_fields(json.dumps(expected)),expected)
        for value in [dict(expected,nonce='other'),dict(expected,extra='CANARY_PRIVATE_TEXT'),
                      dict(expected,count=True),dict(expected,status='completed'),dict(expected,version=True)]:
            with self.assertRaises(ValueError): control.lab_read_fields(json.dumps(value))

    def test_pair_guard_rejects_dead_reused_or_replaced_ownership(self):
        service = {'pid':101,'ppid':100,'uid':os.getuid(),'comm':'codex','native_executable_matches':True,
                   'started_at_local':'Fri Sep 11 18:00:00 2026','executable_path':str(control.c.CLI.resolve())}
        tui = dict(service,pid=102,started_at_local='Fri Sep 11 18:00:01 2026')
        socket = {'path':str(control.c.SOCKET),'exists':True,'dev':1,'ino':2,'uid':os.getuid(),'mode':'0600','service_holds_path':True}
        original = {'service':service,'tui':tui,'socket':socket}
        self.assertTrue(require_owned_pair(service,tui,socket,original))
        cases = [(None,tui,socket),(service,None,socket),
                 (dict(service,started_at_local='Fri Sep 11 18:01:00 2026'),tui,socket),
                 (service,dict(tui,pid=103),socket),
                 (service,tui,dict(socket,ino=3)),
                 (service,tui,dict(socket,service_holds_path=False)),
                 (service,tui,{'exists':False})]
        for values in cases:
            with self.assertRaises(ValueError): require_owned_pair(*values,original)
        self.assertTrue(require_owned_pair(dict(service,ppid=1),dict(tui,ppid=1),socket,original))

    def test_pair_guard_cannot_match_missing_fields_or_wrong_types_on_both_sides(self):
        process={'pid':101,'ppid':100,'uid':os.getuid(),'comm':'codex','native_executable_matches':True,
                 'started_at_local':'Fri Sep 11 18:00:00 2026','executable_path':str(control.c.CLI.resolve())}
        original={'service':process,'tui':dict(process,pid=102),
                  'socket':{'path':str(control.c.SOCKET),'exists':True,'dev':1,'ino':2,'uid':os.getuid(),
                            'mode':'0600','service_holds_path':True}}
        for section in ('service','tui','socket'):
            keys=list(original[section])
            if section != 'socket': keys.remove('started_at_local'); keys.insert(0,'started_at_local')
            for key in keys:
                current=copy.deepcopy(original); old=copy.deepcopy(original)
                del current[section][key]; del old[section][key]
                with self.assertRaises(ValueError,msg=f'{section}.{key}'):
                    require_owned_pair(current['service'],current['tui'],current['socket'],old)
        for section,key,value in [('service','pid',True),('tui','uid',str(os.getuid())),
                                  ('service','started_at_local','CANARY_PRIVATE_TEXT'),('socket','ino',True),
                                  ('socket','mode','0777'),('tui','native_executable_matches',1)]:
            current=copy.deepcopy(original); old=copy.deepcopy(original)
            current[section][key]=old[section][key]=value
            with self.assertRaises(ValueError):
                require_owned_pair(current['service'],current['tui'],current['socket'],old)

    def test_completion_reads_only_exact_owned_native_marker_in_original_window(self):
        root='01a08e0b-bd0d-7400-b069-d50978939cdd'
        turn='01a08e0b-bd0d-7400-b069-d50978939cde'
        with tempfile.TemporaryDirectory() as name:
            home=Path(name); sessions=home/'sessions'; sessions.mkdir()
            rollout=sessions/'synthetic.jsonl'
            secret={'timestamp':'2026-09-11T10:00:01Z','type':'response_item',
                    'payload':{'type':'reasoning','summary':'CANARY_PRIVATE_TEXT'}}
            complete={'timestamp':'2026-09-11T10:00:02Z','type':'event_msg',
                      'payload':{'type':'task_complete','turn_id':turn,'last_agent_message':'SD_QUAL_03_READY',
                                 'unknown':'CANARY_PRIVATE_TEXT'}}
            rollout.write_text(json.dumps(secret)+'\n'+json.dumps(complete)+'\n')
            db=sqlite3.connect(home/'state_5.sqlite')
            db.execute('CREATE TABLE threads(id,source,cwd,cli_version,rollout_path)')
            db.execute('CREATE TABLE thread_spawn_edges(parent_thread_id,child_thread_id,status)')
            db.execute('INSERT INTO threads VALUES(?,?,?,?,?)',
                       (root,'vscode',str(control.c.REPO),'0.153.4',str(rollout)))
            db.commit(); db.close()
            with patch.object(control.c,'CODEX_HOME',home):
                read=functools.partial(control.native_ready,now=dt.datetime(2026,9,11,10,2,tzinfo=dt.timezone.utc))
                result=read(root,turn,'2026-09-11T10:00:00Z','2026-09-11T10:02:00Z')
                self.assertEqual(result,{'at':'2026-09-11T10:00:02Z','turn_id':turn,'type':'task_complete',
                                         'last_agent_message':'SD_QUAL_03_READY'})
                self.assertNotIn('CANARY_PRIVATE_TEXT',json.dumps(result))
                self.assertIsNone(read(root,root,'2026-09-11T10:00:00Z','2026-09-11T10:02:00Z'))
                complete['timestamp']='2026-09-11T10:02:00.000000001Z'
                rollout.write_text(json.dumps(complete)+'\n')
                self.assertIsNone(read(root,turn,'2026-09-11T10:00:00Z','2026-09-11T10:02:00Z'))


if __name__ == '__main__': unittest.main()
