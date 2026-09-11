"""Local guards for the single owned native case; no Codex/model invocations."""
import unittest
import json
import copy
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
import native_sd_control as control
from native_sd_control import require_owned_pair


class ControlTests(unittest.TestCase):
    def test_zero_exit_without_same_case_send_attempt_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory,patch.object(control,'CASE',Path(directory)):
            completed=subprocess.run([sys.executable,'-c','pass'],capture_output=True)
            self.assertEqual(completed.returncode,0)
            with self.assertRaises(ValueError):
                control.require_helper_success('send-once',completed.returncode,
                    '01a08e0b-bd0d-7400-b069-d50978939cdd','01a08e0b-bd0d-7400-b069-d50978939cde','0'*64,time.monotonic_ns())

    def test_lab_read_envelope_is_fixed_nonce_and_does_not_export_extensions(self):
        expected={'version':1,'status':'pending','nonce':'sd-qual-01','count':0}
        self.assertEqual(control.lab_read_fields(json.dumps(expected)),expected)
        for value in [dict(expected,nonce='other'),dict(expected,extra='CANARY_PRIVATE_TEXT'),
                      dict(expected,count=True),dict(expected,status='completed'),dict(expected,version=True)]:
            with self.assertRaises(ValueError): control.lab_read_fields(json.dumps(value))

    def test_pair_guard_rejects_dead_reused_or_replaced_ownership(self):
        service = {'pid':101,'ppid':100,'uid':os.getuid(),'comm':'codex','native_executable_matches':True,
                   'started_at_local':'Fri Sep 11 18:00:00 2026'}
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
                 'started_at_local':'Fri Sep 11 18:00:00 2026'}
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
                      'payload':{'type':'task_complete','turn_id':turn,'last_agent_message':'SD_QUAL_01_READY',
                                 'unknown':'CANARY_PRIVATE_TEXT'}}
            rollout.write_text(json.dumps(secret)+'\n'+json.dumps(complete)+'\n')
            db=sqlite3.connect(home/'state_5.sqlite')
            db.execute('CREATE TABLE threads(id,source,cwd,cli_version,rollout_path)')
            db.execute('INSERT INTO threads VALUES(?,?,?,?,?)',
                       (root,'cli',str(control.c.REPO),'0.153.4',str(rollout)))
            db.commit(); db.close()
            with patch.object(control.c,'CODEX_HOME',home):
                read=functools.partial(control.native_ready,now=dt.datetime(2026,9,11,10,2,tzinfo=dt.timezone.utc))
                result=read(root,turn,'2026-09-11T10:00:00Z','2026-09-11T10:02:00Z')
                self.assertEqual(result,{'at':'2026-09-11T10:00:02Z','turn_id':turn,'type':'task_complete',
                                         'last_agent_message':'SD_QUAL_01_READY'})
                self.assertNotIn('CANARY_PRIVATE_TEXT',json.dumps(result))
                self.assertIsNone(read(root,root,'2026-09-11T10:00:00Z','2026-09-11T10:02:00Z'))
                complete['timestamp']='2026-09-11T10:02:00.000000001Z'
                rollout.write_text(json.dumps(complete)+'\n')
                self.assertIsNone(read(root,turn,'2026-09-11T10:00:00Z','2026-09-11T10:02:00Z'))


if __name__ == '__main__': unittest.main()
