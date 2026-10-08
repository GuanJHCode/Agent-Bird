import hashlib
import json
import importlib.util
import pathlib
import tempfile
import sqlite3
import time
import datetime
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('resource_soak', pathlib.Path(__file__).with_name('resource_soak.py'))
soak = importlib.util.module_from_spec(spec)
spec.loader.exec_module(soak)

class ResourceAcceptanceGuards(unittest.TestCase):
    def test_one_worker_exiting_or_identity_drift_cannot_pass(self):
        class Server:
            def poll(self):
                return None
        workers = {task: {'pid': i, 'birth': 'original', 'argv': f'/private/worker {task}', 'role': task}
                   for i, task in enumerate(soak.TASKS, 101)}
        ctx = {'server': Server(), 'workers': workers, 'identities': {str(i['pid']): i for i in workers.values()}}
        rows = {i['pid']: {'argv': i['argv'], 'stat': 'S'} for i in workers.values()}
        running = {task: {'segment_status': 'running'} for task in soak.TASKS}
        with patch.object(soak, 'birth', return_value='original'):
            soak.validate_running(ctx, running, rows)
            broken = {**running, 'worker-a': {'segment_status': 'exited'}}
            with self.assertRaises(soak.Failure):
                soak.validate_running(ctx, broken, rows)
        with patch.object(soak, 'birth', return_value='reused'):
            with self.assertRaises(soak.Failure):
                soak.validate_running(ctx, running, rows)

    def test_pending_tail_rejects_replaced_event_and_missing_completion(self):
        db = sqlite3.connect(':memory:')
        self.addCleanup(db.close)
        db.row_factory = sqlite3.Row
        db.executescript("""CREATE TABLE tasks(id,status); CREATE TABLE attempts(id,task_id);
            CREATE TABLE segment_runtime(segment_id,attempt_id,status);
            CREATE TABLE runtime_events(event_id,producer_id,body_json,delivery_status,created_at,task_id,attempt_id,segment_id,sequence);""")
        class Server:
            def poll(self):
                return None
        workers = {task: {'pid': i, 'birth': 'fixed', 'argv': '/fixture', 'role': task}
                   for i, task in enumerate(soak.TASKS, 101)}
        ctx = {'server': Server(), 'workers': workers, 'identities': {}}
        stats = [{'status': 'completed', 'reports': 354, 'phases': [{'seconds': 1200}, {'seconds': 1200}, {'seconds': 1140}]} for _ in soak.TASKS]
        for task in soak.TASKS:
            db.execute('INSERT INTO tasks VALUES(?,?)', (task, 'result_ready'))
            db.execute('INSERT INTO attempts VALUES(?,?)', (task+'-attempt', task))
            db.execute('INSERT INTO segment_runtime VALUES(?,?,?)', (task+'-segment',task+'-attempt','exited'))
            for seq, kind in enumerate(('running','result','exited')):
                body = {'kind':kind,'process':{'pid':workers[task]['pid'],'birth':'fixed'},'exit_code':0}
                db.execute('INSERT INTO runtime_events VALUES(?,?,?,?,?,?,?,?,?)', (task+kind,'host',json.dumps(body),'pending' if kind=='result' else 'internal', datetime.datetime.now(datetime.timezone.utc).isoformat(),task,task+'-attempt',task+'-segment',seq))
        with patch.object(soak, 'identity_reaped', return_value=True):
            soak.validate_pending_tail(ctx, db, stats, {}, time.time_ns()-1, 3540, 10)
            db.execute("UPDATE runtime_events SET event_id='replacement' WHERE event_id='worker-aresult'")
            with self.assertRaisesRegex(soak.Failure, 'pending tail'):
                soak.validate_pending_tail(ctx, db, stats, {}, time.time_ns()-1, 3540, 10)
            ctx['tail_baseline'] = {}
            stats[0]['status'] = 'stopped'
            with self.assertRaisesRegex(soak.Failure, 'completion'):
                soak.validate_pending_tail(ctx, db, stats, {}, time.time_ns()-6_000_000_000, 3540, 10)

    def test_late_helper_maximum_recharges_early_product_peak(self):
        with tempfile.TemporaryDirectory(prefix='resource-soak-rss-', dir='/private/tmp') as tmp:
            path = pathlib.Path(tmp) / 'samples.jsonl'
            path.write_text(''.join(json.dumps({'tick': tick, 'observed_product_rss_kib': value * 1024,
                                              'helper_rss_allowance_bytes': 1024 * 1024}) + '\n'
                                    for tick, value in enumerate((120, 80))))
            with self.assertRaises(soak.Failure):
                soak.finalize_rss(path, [{'helper_max_rss_bytes': 12 * 1024 * 1024}], True)
            result = soak.finalize_rss(path, [{'helper_max_rss_bytes': 8 * 1024 * 1024}], True)
            self.assertEqual(result['peak_charged_rss_mib'], 128)
            self.assertEqual(result['five_minute_median_kib'][0], 108 * 1024)

    def test_private_copy_requires_frozen_bytes(self):
        with tempfile.TemporaryDirectory(prefix='resource-soak-pin-', dir='/private/tmp') as tmp:
            root = pathlib.Path(tmp)
            source, target = root / 'source', root / 'copy'
            source.write_bytes(b'candidate-v1')
            frozen = hashlib.sha256(source.read_bytes()).hexdigest()
            identity = soak.copy_pinned(source, target, frozen)
            source.write_bytes(b'changed-source')
            self.assertEqual(target.read_bytes(), b'candidate-v1')
            self.assertEqual(soak.pin(target), identity)
            with self.assertRaises(soak.Failure):
                soak.copy_pinned(source, root / 'bad-copy', frozen)

if __name__ == '__main__':
    unittest.main()
