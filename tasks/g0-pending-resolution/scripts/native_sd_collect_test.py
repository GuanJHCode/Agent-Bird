"""Privacy and identity failures that would invalidate the fixed native sample."""
import datetime as dt
import json
import unittest
import native_sd_collect as c

ROOT = '01a08e0b-bd0d-7400-b069-d50978939cdd'
OTHER = '01a08e0b-bd0d-7400-b069-d50978939cde'
START = '2026-09-11T10:00:00Z'
END = '2026-09-11T10:02:00Z'
NOW = dt.datetime(2026, 9, 11, 10, 2, tzinfo=dt.timezone.utc)
CANARY = 'CANARY_PRIVATE_TEXT'


class EvidenceTests(unittest.TestCase):
    def test_window_cannot_hide_a_timeout_or_use_local_or_future_time(self):
        self.assertEqual(c.window(START, END, NOW), (1789120800.0, 1789120920.0))
        for begin, end in [(START, '2026-09-11T10:02:01Z'),
                           (START, '2026-09-11T10:02:00.000000001Z'),
                           (END, START), (START[:-1], END),
                           (START, '2026-09-11T10:03:00Z')]:
            with self.assertRaises(ValueError):
                c.window(begin, end, NOW)

    def test_process_identity_rejects_wrong_pid_uid_or_executable(self):
        text = '123 100 501 Fri Sep 11 18:00:00 2026 /trusted/bin/codex\n'
        got = c.process_fields(text, 123, 501)
        self.assertEqual(got, {'pid':123, 'ppid':100, 'uid':501,
                              'started_at_local':'Fri Sep 11 18:00:00 2026',
                              'comm':'codex'})
        for value, pid, uid in [(text,124,501),(text,123,502),
                                (text.replace('codex', CANARY),123,501)]:
            with self.assertRaises(ValueError): c.process_fields(value,pid,uid)

    def test_same_identity_survives_reparent_but_not_pid_reuse(self):
        first = {'pid':123,'started_at_local':'Fri Sep 11 18:00:00 2026','ppid':100}
        self.assertTrue(c.same_process(dict(first,ppid=1), first))
        self.assertFalse(c.same_process(dict(first,started_at_local='Fri Sep 11 18:00:01 2026'),first))
        self.assertFalse(c.same_process(None, first))

    def test_only_exact_owned_thread_metadata_is_accepted(self):
        row = (ROOT,'cli',str(c.REPO),'0.153.4')
        self.assertEqual(c.thread_fields(row,ROOT),{'id':ROOT,'source':'cli','experiment_cwd_matches':True,'cli_version':'0.153.4'})
        for bad in [(OTHER,*row[1:]),(ROOT,CANARY,*row[2:]),
                    (ROOT,'cli','/unrelated',row[3])]:
            with self.assertRaises(ValueError): c.thread_fields(bad,ROOT)

    def test_hooks_require_exact_root_service_time_and_drop_unknown_text(self):
        value = {'session_id':ROOT,'hook_event_name':'UserPromptSubmit','turn_id':OTHER,
                 'pid':124,'ppid':123,'recorded_at':'2026-09-11T10:00:01Z',
                 'source':CANARY,'extension':CANARY,'nonce':CANARY}
        got = c.hook_fields(value,ROOT,123,1789120800,1789120920)
        self.assertEqual(got,{'session_id':ROOT,'hook_event_name':'UserPromptSubmit','turn_id':OTHER,
                              'pid':124,'ppid':123,'recorded_at':'2026-09-11T10:00:01Z'})
        self.assertNotIn(CANARY,json.dumps(got))
        for changes in ({'session_id':OTHER},{'ppid':456},{'recorded_at':'2026-09-11T09:59:59Z'},
                        {'hook_event_name':CANARY},{'pid':True}):
            self.assertIsNone(c.hook_fields(dict(value,**changes),ROOT,123,1789120800,1789120920))

    def test_ui_status_does_not_guess_from_other_uuid_or_echo_raw(self):
        got = c.ui_status('\x1b[32mSession: '+ROOT+'\x1b[0m\nPrivate: '+CANARY)
        self.assertEqual(got, {'status_root':ROOT})
        self.assertIsNone(c.ui_status('model said '+ROOT+' '+CANARY))
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
