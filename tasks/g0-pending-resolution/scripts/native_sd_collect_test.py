"""Privacy and identity failures that would invalidate the fixed native sample."""
import datetime as dt
import json
import os
from pathlib import Path
import sqlite3
import tempfile
import unittest
from types import SimpleNamespace
from unittest.mock import patch
import native_sd_collect as c

ROOT = '01a08e0b-bd0d-7400-b069-d50978939cdd'
OTHER = '01a08e0b-bd0d-7400-b069-d50978939cde'
START = '2026-09-11T10:00:00Z'
END = '2026-09-11T10:02:00Z'
NOW = dt.datetime(2026, 9, 11, 10, 2, tzinfo=dt.timezone.utc)
CANARY = 'CANARY_PRIVATE_TEXT'


class EvidenceTests(unittest.TestCase):
    def test_root_family_requires_successful_exact_child_edge_query(self):
        db=sqlite3.connect(':memory:')
        try:
            with self.assertRaises(sqlite3.OperationalError): c.require_root_family(db,ROOT)
            db.execute('CREATE TABLE thread_spawn_edges(parent_thread_id,child_thread_id,status)')
            db.execute('INSERT INTO thread_spawn_edges VALUES(?,?,?)',(CANARY,OTHER,'completed'))
            self.assertEqual(c.require_root_family(db,ROOT),{'spawn_edges_query_succeeded':True,'parent_edge_count':0})
            db.execute('INSERT INTO thread_spawn_edges VALUES(?,?,?)',(CANARY,ROOT,'completed'))
            with self.assertRaises(ValueError): c.require_root_family(db,ROOT)
        finally: db.close()

    def test_process_preserves_ps_comm_and_reads_executable_path_from_libproc(self):
        executable='/trusted/bin/codex'
        observed=[executable]; calls=[]
        def proc_pidpath(pid,buffer,size):
            calls.append(pid); buffer.value=observed[0].encode(); return len(buffer.value)
        with patch.object(c,'CLI',Path(executable)),patch.object(c.ctypes,'CDLL',return_value=SimpleNamespace(proc_pidpath=proc_pidpath)):
            for comm in ('codex',executable):
                row=f'123 100 {os.getuid()} Fri Sep 11 18:00:00 2026 {comm}\n'
                with patch.object(c.subprocess,'run',return_value=SimpleNamespace(returncode=0,stdout=row)):
                    got=c.process(123)
                    self.assertEqual(got.get('executable_path'),executable)
                    self.assertEqual(got['comm'],comm)
                    observed[0]='/unrelated/codex'
                    with self.assertRaises(ValueError): c.process(123)
                    observed[0]=''
                    with self.assertRaises(ValueError): c.process(123)
                    observed[0]=executable
            self.assertEqual(calls,[123]*6)

    def test_window_cannot_hide_a_timeout_or_use_local_or_future_time(self):
        self.assertEqual(c.window(START, END, NOW), (1789120800000000000, 1789120920000000000))
        for begin, end in [(START, '2026-09-11T10:02:01Z'),
                           (START, '2026-09-11T10:02:00.000000001Z'),
                           (END, START), (START[:-1], END),
                           (START, '2026-09-11T10:03:00Z')]:
            with self.assertRaises(ValueError):
                c.window(begin, end, NOW)

    def test_process_identity_rejects_wrong_pid_uid_or_executable(self):
        text = f'123 100 501 Fri Sep 11 18:00:00 2026 {c.CLI.resolve()}\n'
        got = c.process_fields(text, 123, 501)
        self.assertEqual(got, {'pid':123, 'ppid':100, 'uid':501,
                              'started_at_local':'Fri Sep 11 18:00:00 2026',
                              'comm':str(c.CLI.resolve())})
        for value, pid, uid in [(text,124,501),(text,123,502),
                                (text.replace('codex', CANARY),123,501)]:
            with self.assertRaises(ValueError): c.process_fields(value,pid,uid)

    def test_same_identity_survives_reparent_but_not_pid_reuse(self):
        first = {'pid':123,'started_at_local':'Fri Sep 11 18:00:00 2026','ppid':100}
        self.assertTrue(c.same_process(dict(first,ppid=1), first))
        self.assertFalse(c.same_process(dict(first,started_at_local='Fri Sep 11 18:00:01 2026'),first))
        self.assertFalse(c.same_process(None, first))

    def test_snapshot_identity_rejects_missing_service_or_tui(self):
        service = {'pid':101,'started_at_local':'Fri Sep 11 18:00:00 2026'}
        tui = {'pid':102,'started_at_local':'Fri Sep 11 18:00:01 2026'}
        for left,right in ((None,tui),(service,None),(None,None)):
            with self.assertRaises(ValueError): c.require_live_processes(left,right)

    def test_only_exact_owned_thread_metadata_is_accepted(self):
        row = (ROOT,'vscode',str(c.REPO),'0.153.4')
        try: actual=c.thread_fields(row,ROOT)
        except ValueError: actual=None
        self.assertEqual(actual,{'id':ROOT,'source':'vscode','experiment_cwd_matches':True,'cli_version':'0.153.4'})
        for bad in [(OTHER,*row[1:]),(ROOT,CANARY,*row[2:]),
                    (ROOT,'vscode','/unrelated',row[3]),(ROOT,'cli',*row[2:]),
                    (ROOT,'subAgent',*row[2:]),(ROOT,'unknown',*row[2:])]:
            with self.assertRaises(ValueError): c.thread_fields(bad,ROOT)

    def test_hooks_require_exact_root_service_time_and_drop_unknown_text(self):
        value = {'session_id':ROOT,'hook_event_name':'UserPromptSubmit','turn_id':OTHER,
                 'pid':124,'ppid':123,'recorded_at':'2026-09-11T10:00:01Z',
                 'source':CANARY,'extension':CANARY,'nonce':CANARY}
        got = c.hook_fields(value,ROOT,123,1789120800000000000,1789120920000000000)
        self.assertEqual(got,{'session_id':ROOT,'hook_event_name':'UserPromptSubmit','turn_id':OTHER,
                              'pid':124,'ppid':123,'recorded_at':'2026-09-11T10:00:01Z'})
        self.assertNotIn(CANARY,json.dumps(got))
        for changes in ({'session_id':OTHER},{'ppid':456},{'recorded_at':'2026-09-11T09:59:59Z'},
                        {'hook_event_name':CANARY},{'pid':True}):
            self.assertIsNone(c.hook_fields(dict(value,**changes),ROOT,123,1789120800000000000,1789120920000000000))

    def test_hook_one_nanosecond_after_original_window_is_rejected(self):
        lo,hi = c.window(START,END,NOW)
        value = {'session_id':ROOT,'hook_event_name':'UserPromptSubmit','turn_id':OTHER,
                 'pid':124,'ppid':123,'recorded_at':'2026-09-11T10:02:00.000000001Z'}
        self.assertIsNone(c.hook_fields(value,ROOT,123,lo,hi))

    def test_real_log_query_keeps_exact_end_but_rejects_end_plus_one_nanosecond(self):
        with tempfile.TemporaryDirectory() as name:
            home = Path(name); hooks = home/'hooks'; hooks.mkdir()
            state = sqlite3.connect(home/'state_5.sqlite')
            state.execute('CREATE TABLE threads(id,source,cwd,cli_version)')
            state.execute('CREATE TABLE thread_spawn_edges(parent_thread_id,child_thread_id,status)')
            state.execute('INSERT INTO threads VALUES(?,?,?,?)',(ROOT,'vscode',str(c.REPO),'0.153.4'))
            state.commit(); state.close()
            logs = sqlite3.connect(home/'logs_2.sqlite')
            logs.execute('CREATE TABLE logs(id INTEGER PRIMARY KEY,ts,ts_nanos,target,thread_id,process_uuid,feedback_log_body)')
            for index,nanos in enumerate((0,1),1):
                logs.execute('INSERT INTO logs VALUES(?,?,?,?,?,?,?)',
                    (index,1789120920,nanos,'codex_app_server::outgoing_message',ROOT,'pid:123:'+OTHER,
                     'transport="unix_socket" client_name="codex-tui" method="thread/start" connection_id=0'))
            logs.commit(); logs.close()
            lo,hi = c.window(START,END,NOW)
            with patch.object(c,'CODEX_HOME',home),patch.object(c,'HOOKS',hooks):
                result = c.local_evidence(ROOT,123,lo,hi)
            self.assertEqual([row['ts_nanos'] for row in result['native_protocol']],[0])

    def test_ui_status_does_not_guess_from_other_uuid_or_echo_raw(self):
        got = c.ui_status('\x1b[32mSession: '+ROOT+'\x1b[0m\nPrivate: '+CANARY)
        self.assertEqual(got, {'status_root':ROOT})
        self.assertIsNone(c.ui_status('model said '+ROOT+' '+CANARY))
        self.assertIsNone(c.ui_status('model transcript: Session:'+ROOT))
        self.assertIsNone(c.ui_status('Session: '+ROOT+'\nSession: '+OTHER))

    def test_native_log_only_keeps_fixed_protocol_fields(self):
        body = 'request{transport="unix_socket" client_name="codex-tui" client_version="0.153.4" method="thread/start" connection_id=0 api_version="v2"} '+CANARY
        got = c.log_fields(body)
        self.assertEqual(got, {'transport':'unix_socket','client_name':'codex-tui','client_version':'0.153.4',
                               'method':'thread/start','connection_id':0,'api_version':'v2'})
        self.assertNotIn(CANARY,json.dumps(got))
        self.assertNotIn('client_name',c.log_fields(body.replace('codex-tui',CANARY)))
        self.assertNotIn('method',c.log_fields(body.replace('thread/start',CANARY)))


if __name__ == '__main__': unittest.main()
