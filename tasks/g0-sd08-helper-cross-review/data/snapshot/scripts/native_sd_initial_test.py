"""Run the positional-prompt controller against offline process/clock boundaries."""
from contextlib import ExitStack
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import native_sd_control as control


ROOT='01a08e0b-bd0d-7400-b069-d50978939cdd'
TURN='01a08e0b-bd0d-7400-b069-d50978939cde'
BASE_TIME=dt.datetime(2026,9,11,10,tzinfo=dt.timezone.utc)


class InitialTests(unittest.TestCase):
    def run_initial(self,*,missing=None,wrong_root=False,duplicate=None,intent_cost=0,
                    spawn_cost=2000000000,identity_cost=0,scan_cost=0,tamper=None,service_changed=False,
                    result_at=0,prepare_cost=None):
        raw=[0];launches=[];sent=[];helper_attempts=[];lab_reads=[];exit_code=[None];inputs=[]
        session_scans=[]
        with tempfile.TemporaryDirectory() as directory,ExitStack() as patches:
            base=Path(directory);executable=base/'fixture';executable.write_text('fixture')
            case=base/'case';job=base/'job';docs=base/'docs';docs.mkdir();hooks=base/'hooks';hooks.mkdir()
            identity={'pid':101,'ppid':100,'uid':os.getuid(),'comm':'codex','native_executable_matches':True,
                      'executable_path':str(executable.resolve()),'started_at_local':'Fri Sep 11 18:00:00 2026'}
            socket={'path':str(control.c.SOCKET),'exists':True,'dev':1,'ino':2,'uid':os.getuid(),
                    'mode':'0600','service_holds_path':True}
            def stamp(): return (BASE_TIME+dt.timedelta(microseconds=raw[0]//1000)).isoformat()
            def clock(): return {'clock_impl':control.CLOCK_IMPL,'mono_ns':raw[0],'wall_ns':control.c.epoch_ns(stamp())}
            def process(pid):
                if pid==102:
                    raw[0]+=identity_cost
                    return dict(identity,pid=102,started_at_local='Fri Sep 11 18:00:01 2026')
                return dict(identity,started_at_local='Fri Sep 11 18:00:02 2026') if service_changed and launches else identity
            def snapshot(pid=None): return {'exists':False} if pid is None else socket
            def tui_read(seconds):
                raw[0]+=1000000000
                if len(lab_reads)>=2 and raw[0]>=result_at: (job/'result.json').write_text('{}')
            def send(sequence,action):
                data=sequence.take(action);sent.append(action)
                inputs.append({'action':action,'at':stamp(),'clock':clock(),'bytes':len(data),
                               'sha256':hashlib.sha256(data).hexdigest()})
                if action=='quit-submit': exit_code[0]=0
            tui=SimpleNamespace(pid=102,byte_count=64,buffer=b'FIXTURE_PTY_BYTES',read=tui_read,send=send,
                                _poll=lambda:exit_code[0],close=lambda:None,
                                evidence=lambda:{'pid':102,'bytes':64,'sha256':'0'*64,'inputs':list(inputs),
                                                 'exit_code':exit_code[0]})
            def native_tui(argv,cwd,**kwargs):
                launches.append({'argv':argv,'mono_ns':raw[0],'kwargs':kwargs})
                if kwargs.get('initial_input') is not None: inputs.append(kwargs['initial_input'])
                start=raw[0];raw[0]+=spawn_cost
                for index,event in enumerate(('SessionStart','UserPromptSubmit')):
                    if missing==event: continue
                    at=(BASE_TIME+dt.timedelta(microseconds=(start+100000000+index*100000000)//1000)).isoformat()
                    value={'session_id':TURN if wrong_root and event=='UserPromptSubmit' else ROOT,
                           'hook_event_name':event,'pid':124+index,'ppid':101,'recorded_at':at}
                    if event=='SessionStart': value['source']='startup'
                    else: value['turn_id']=TURN
                    path=hooks/(event+'.json');path.write_text(json.dumps(value))
                    os.utime(path,ns=(control.c.epoch_ns(at),control.c.epoch_ns(at)))
                    if duplicate==event:
                        second=hooks/(event+'-duplicate.json');second.write_text(json.dumps(value))
                        os.utime(second,ns=(control.c.epoch_ns(at),control.c.epoch_ns(at)))
                return tui
            real_private_json=control.private_json
            def private_json(path,value):
                real_private_json(path,value)
                if path.name=='launch-intent.json': raw[0]+=intent_cost
                if path.name=='native-origin.json' and tamper=='intent_after_origin':
                    with (case/'launch-intent.json').open('a') as stream: stream.write(' ')
            real_discover=control.discover_session_start
            def discover(service,since,through):
                session_scans.append({'mono_ns':raw[0],'since':since,'through':through})
                value=real_discover(service,since,through);raw[0]+=scan_cost
                return value
            def read_job():
                completed=len(lab_reads)>=2;lab_reads.append(completed)
                return {'before':stamp(),'after':stamp(),'elapsed_seconds':0,'binary_sha256':'0'*64,
                        'output':{'version':1,'status':'completed' if completed else 'pending',
                                  'nonce':control.c.NONCE,'count':1 if completed else 0},
                        'stdout_sha256':'0'*64,'exit_code':0}
            def local_evidence(*args):
                if tamper=='intent_before_prepare':
                    with (case/'launch-intent.json').open('a') as stream: stream.write(' ')
                if tamper=='origin_before_prepare':
                    with (case/'native-origin.json').open('a') as stream: stream.write(' ')
                if tamper=='session_before_prepare':
                    path=hooks/'SessionStart.json';value=json.loads(path.read_text());value['session_id']=TURN
                    path.write_text(json.dumps(value));at=control.c.epoch_ns(value['recorded_at'])
                    os.utime(path,ns=(at,at))
                return {'thread':{'id':ROOT},'family':{'spawn_edges_query_succeeded':True,'parent_edge_count':0},
                        'hooks':[],'native_protocol':[{'fields':{'transport':'unix_socket','client_name':'codex-tui',
                                                              'client_version':'0.154.0','connection_id':0}}]}
            def helper(argv,**kwargs):
                helper_attempts.append(argv[3])
                if argv[3]=='prepare' and prepare_cost is not None:
                    raw[0]+=prepare_cost
                    return SimpleNamespace(returncode=0,poll=lambda:0,communicate=lambda timeout:(b'{}',b''))
                raise OSError('offline helper boundary')
            service=SimpleNamespace(pid=101,read=lambda seconds:None,_poll=lambda:None,
                                    evidence=lambda:{'pid':101,'exit_code':None},close=lambda:None)
            for key,value in (('CASE',case),('JOB',job),('DOCS',docs),('HELPER',executable),('AUDIT',executable)):
                patches.enter_context(patch.object(control,key,value))
            for key,value in (('CLI',executable),('GO',executable),('CLI_SHA','0'*64),('GO_SHA','0'*64),('HOOKS',hooks)):
                patches.enter_context(patch.object(control.c,key,value))
            patches.enter_context(patch.object(control.c,'sha',side_effect=lambda p:'0'*64 if p.resolve()==executable.resolve() else hashlib.sha256(p.read_bytes()).hexdigest()))
            patches.enter_context(patch.object(control.c,'protected_files',return_value={}))
            patches.enter_context(patch.object(control.c,'process',side_effect=process))
            patches.enter_context(patch.object(control.c,'snapshot_socket',side_effect=snapshot))
            patches.enter_context(patch.object(control.c,'local_evidence',side_effect=local_evidence))
            patches.enter_context(patch.object(control,'continuous_ns',side_effect=lambda:raw[0]))
            patches.enter_context(patch.object(control,'clock_pair',side_effect=clock))
            patches.enter_context(patch.object(control,'stamp',side_effect=stamp))
            patches.enter_context(patch.object(control,'private_json',side_effect=private_json))
            patches.enter_context(patch.object(control,'discover_session_start',side_effect=discover))
            patches.enter_context(patch.object(control,'OwnedService',return_value=service))
            patches.enter_context(patch.object(control,'NativePTY',side_effect=native_tui))
            patches.enter_context(patch.object(control,'read_job',side_effect=read_job))
            patches.enter_context(patch.object(control,'read_job_owner',return_value={
                'version':1,'status':'started','nonce':control.c.NONCE,'controller_thread':ROOT,
                'task_revision':1,'worker_pid':123,'job_sha256':'0'*64}))
            patches.enter_context(patch.object(control,'native_ready',return_value={
                'at':'2026-09-11T10:00:03Z','turn_id':TURN,'type':'task_complete','last_agent_message':'SD_QUAL_08_READY'}))
            patches.enter_context(patch.object(control.subprocess,'run',return_value=subprocess.CompletedProcess([],0,stdout=TURN+'\n')))
            patches.enter_context(patch.object(control.subprocess,'Popen',side_effect=helper))
            patches.enter_context(patch('builtins.print'))
            code=control.run('0'*64,'0'*64)
            files={path.name:path.read_bytes() for path in case.iterdir()}
            record=json.loads(files['controller-result.json'])
            return {'code':code,'record':record,'files':files,'launches':launches,'sent':sent,
                    'helper_attempts':helper_attempts,'session_scans':session_scans}

    def test_early_double_hooks_survive_post_launch_origin_and_prepare_once(self):
        got=self.run_initial()
        self.assertEqual(len(got['launches']),1)
        self.assertEqual(len(got['launches'][0]['argv']),2)
        self.assertEqual(got['helper_attempts'],['prepare'])
        self.assertFalse({'initial-paste','initial-submit'} & set(got['sent']))
        intent=json.loads(got['files']['launch-intent.json']);origin=json.loads(got['files']['native-origin.json'])
        evidence=json.loads(got['files']['controller-evidence.json'])
        self.assertEqual(intent['initial_input']['action'],'initial-argv')
        self.assertEqual(intent['initial_window']['mono_ns'],0)
        self.assertEqual(intent['initial_window']['deadline_mono_ns'],120000000000)
        self.assertGreater(origin['clock']['mono_ns'],0)
        self.assertLess(control.c.epoch_ns(got['record']['session_start']['recorded_at']),control.c.epoch_ns(origin['captured_at']))
        intent_sha=hashlib.sha256(got['files']['launch-intent.json']).hexdigest()
        self.assertEqual(origin['launch_intent_sha256'],intent_sha)
        self.assertEqual(evidence['launch_intent_sha256'],intent_sha)
        self.assertEqual(evidence['origin_sha256'],hashlib.sha256(got['files']['native-origin.json']).hexdigest())
        self.assertEqual(evidence['session_start']['session_id'],evidence['candidate_hook']['session_id'])
        self.assertEqual(evidence['pty']['inputs'],[intent['initial_input']])
        self.assertNotIn(got['launches'][0]['argv'][1],json.dumps(intent))

    def test_missing_session_start_allows_the_one_argv_launch_but_never_helper(self):
        got=self.run_initial(missing='SessionStart')
        self.assertEqual(len(got['launches']),1)
        self.assertEqual(len(got['launches'][0]['argv']),2)
        self.assertEqual(got['helper_attempts'],[])
        self.assertEqual(got['sent'],[])
        self.assertEqual(got['record']['failure']['code'],'owned_session_start_missing')

    def test_missing_or_conflicting_post_launch_hooks_never_prepare(self):
        for kwargs in ({'missing':'UserPromptSubmit'},{'wrong_root':True},
                       {'duplicate':'SessionStart'},{'duplicate':'UserPromptSubmit'}):
            with self.subTest(kwargs=kwargs):
                got=self.run_initial(**kwargs)
                self.assertEqual(len(got['launches']),1)
                self.assertEqual(len(got['launches'][0]['argv']),2)
                self.assertEqual(got['helper_attempts'],[])
                self.assertFalse({'initial-paste','initial-submit'} & set(got['sent']))

    def test_expired_intent_persistence_cannot_launch_tui(self):
        for cost in (10000000000,120000000000,121000000000):
            with self.subTest(cost=cost):
                got=self.run_initial(intent_cost=cost)
                self.assertEqual(got['launches'],[])
                self.assertEqual(got['helper_attempts'],[])
                self.assertEqual(got['record']['initial_window']['deadline_mono_ns'],120000000000)
                self.assertEqual(got['record']['failure']['code'],'continuous_deadline_exceeded')

    def test_spawn_or_identity_crossing_deadline_cannot_reset_window_or_prepare(self):
        for kwargs in ({'spawn_cost':10000000000},{'spawn_cost':121000000000},{'identity_cost':120000000000}):
            with self.subTest(kwargs=kwargs):
                got=self.run_initial(**kwargs)
                self.assertEqual(len(got['launches']),1)
                self.assertEqual(got['helper_attempts'],[])
                self.assertEqual(got['record']['initial_window']['mono_ns'],0)
                self.assertEqual(got['record']['initial_window']['deadline_mono_ns'],120000000000)
                self.assertEqual(got['record']['failure']['code'],'continuous_deadline_exceeded')

    def test_hook_scan_returning_valid_evidence_after_original_window_cannot_prepare(self):
        got=self.run_initial(scan_cost=120000000000)
        self.assertEqual(len(got['launches'][0]['argv']),2)
        self.assertEqual(got['helper_attempts'],[])
        self.assertEqual(got['record']['failure']['code'],'continuous_deadline_exceeded')

    def test_changed_launch_records_or_service_identity_cannot_prepare(self):
        for kwargs in ({'tamper':'intent_after_origin'},{'tamper':'intent_before_prepare'},
                       {'tamper':'origin_before_prepare'},{'service_changed':True}):
            with self.subTest(kwargs=kwargs):
                got=self.run_initial(**kwargs)
                self.assertEqual(len(got['launches'][0]['argv']),2)
                self.assertEqual(got['helper_attempts'],[])
                self.assertFalse({'initial-paste','initial-submit'} & set(got['sent']))

    def test_session_start_changed_after_evidence_read_cannot_prepare(self):
        got=self.run_initial(tamper='session_before_prepare')
        self.assertEqual(got['helper_attempts'],[])
        self.assertEqual(got['record']['failure']['code'],'evidence_validation_rejected')

    def test_send_preflight_after_initial_window_uses_frozen_evidence_range(self):
        got=self.run_initial(result_at=115000000000,prepare_cost=8000000000)
        self.assertEqual(got['helper_attempts'],['prepare','send-once'])
        window=json.loads(got['files']['controller-evidence.json'])['initial_window']
        later=[scan for scan in got['session_scans'] if scan['mono_ns']>=120000000000]
        self.assertTrue(later)
        self.assertTrue(all(scan['since']==window['since'] and scan['through']==window['through'] for scan in later))
        self.assertEqual(got['record']['initial_window']['deadline_mono_ns'],120000000000)
        self.assertEqual(got['record']['helper_calls'][0]['elapsed_seconds'],8)


if __name__=='__main__': unittest.main()
