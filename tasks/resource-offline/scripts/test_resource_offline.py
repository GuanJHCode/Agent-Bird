import importlib.util
import json
import pathlib
import sqlite3
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('offline', pathlib.Path(__file__).with_name('resource_offline.py'))
offline = importlib.util.module_from_spec(spec)
spec.loader.exec_module(offline)


class OfflineGuards(unittest.TestCase):
    def test_pending_evidence_requires_same_segment_and_exit_zero(self):
        db = sqlite3.connect(':memory:')
        self.addCleanup(db.close)
        db.row_factory = sqlite3.Row
        db.executescript('''
            CREATE TABLE tasks(id,run_id,status);
            CREATE TABLE task_runtime(task_id,host_launch_id);
            CREATE TABLE attempts(id,task_id,attempt_no);
            CREATE TABLE segment_runtime(segment_id,attempt_id,status,host_id);
            CREATE TABLE runtime_events(event_id,body_json,delivery_status,task_id,attempt_id,segment_id,producer_id,sequence);
            INSERT INTO tasks VALUES('task','run','result_ready');
            INSERT INTO task_runtime VALUES('task','launch');
            INSERT INTO attempts VALUES('attempt','task',1);
            INSERT INTO segment_runtime VALUES('segment','attempt','exited','host');
        ''')
        process = {'pid': 1234, 'birth': 'fixed-birth', 'pgid': 1234}
        for seq, kind in enumerate(('running', 'result', 'exited')):
            body = {'kind': kind, 'process': process}
            if kind == 'exited':
                body['exit_code'] = 0
            db.execute('INSERT INTO runtime_events VALUES(?,?,?,?,?,?,?,?)',
                       (kind, json.dumps(body), 'pending' if kind == 'result' else 'internal', 'task', 'attempt', 'segment', 'host', seq))
        self.assertIsNotNone(offline.pending_facts(db, 'run', 'task'))
        db.execute("UPDATE runtime_events SET segment_id='unrelated' WHERE event_id='exited'")
        self.assertIsNone(offline.pending_facts(db, 'run', 'task'))
        db.execute("UPDATE runtime_events SET segment_id='segment',body_json=json_set(body_json,'$.exit_code',1) WHERE event_id='exited'")
        with self.assertRaisesRegex(offline.Failure, 'exit0'):
            offline.pending_facts(db, 'run', 'task')

    def test_broken_controller_cleanup_does_not_skip_coordinator_wait(self):
        class Broken:
            stdin = None
            def poll(self):
                raise OSError('injected observation failure')
            def wait(self, timeout):
                raise OSError('injected wait failure')
        class Server:
            waited = False
            def poll(self):
                return 0
            def wait(self, timeout):
                self.waited = True
                return 0
        server = Server()
        with patch.object(offline, 'process_table', return_value={}):
            result = offline.cleanup({'controller': Broken(), 'server': server, 'candidate': pathlib.Path('/private/fixture')})
        self.assertTrue(server.waited)
        self.assertEqual(result['coordinator_original_wait'], 0)
        self.assertTrue(result['errors'])
        self.assertEqual(result['remaining'], [])


if __name__ == '__main__':
    unittest.main()
