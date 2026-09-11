#!/usr/bin/env python3
import json
import datetime
import unittest
from native_retained_collect import APPROVED_BINARY, APPROVED_NONCES, filter_events, safe_input, valid_binary, valid_window
from native_retained_data_test import ROOT, NONCE, SNAPSHOT

BINARY = APPROVED_BINARY
DIRECTORY = "/fixed/private/retained-task"
COMMAND = f'exec {BINARY} retained-start --dir {DIRECTORY} --nonce {NONCE} --controller-thread "$CODEX_THREAD_ID" --steps 12 --interval 4s'


class RetainedCollectTest(unittest.TestCase):
    def test_only_fixed_authorized_replacement_nonce_is_added(self):
        for nonce, allowed in [('rr-retained-quit-04',True),('rr-retained-quit-99',False)]:
            with self.subTest(nonce=nonce):
                self.assertEqual(nonce in APPROVED_NONCES,allowed)
                source=self.source(COMMAND.replace(NONCE,nonce))
                self.assertEqual('commands' in safe_input(source,ROOT,nonce,BINARY,DIRECTORY),allowed)

    def test_reviewed_binary_only(self):
        self.assertFalse(valid_binary('/tmp/CANARY_PRIVATE_TEXT'))
        source = self.source(COMMAND.replace(BINARY, '/tmp/CANARY_PRIVATE_TEXT'))
        self.assertNotIn('commands', safe_input(source, ROOT, NONCE, '/tmp/CANARY_PRIVATE_TEXT', DIRECTORY))

    def test_window_is_explicit_utc_elapsed_and_at_most_120_seconds(self):
        now = datetime.datetime(2026, 9, 11, 1, tzinfo=datetime.timezone.utc)
        valid_window('2026-09-11T00:00:00Z', '2026-09-11T00:02:00Z', now)
        for since, through in [('2026-09-11T00:00:00Z','2026-09-11T00:02:01Z'),
                               ('2026-09-11T00:00:00','2026-09-11T00:01:00'),
                               ('2026-09-11T00:00:00+08:00','2026-09-11T00:01:00+08:00'),
                               ('2026-09-11T01:00:00Z','2026-09-11T01:01:00Z')]:
            with self.subTest(since=since, through=through):
                with self.assertRaises(ValueError): valid_window(since, through, now)

    def source(self, command):
        return 'text(await tools.exec_command({cmd:' + json.dumps(command) + ',yield_time_ms:1000,max_output_tokens:5000}));'

    def test_expected_literal_exec_and_environment_binding(self):
        parsed = safe_input(self.source(COMMAND), ROOT, NONCE, BINARY, DIRECTORY)
        self.assertEqual(parsed["commands"][0]["verb"], "retained-start")
        self.assertTrue(parsed["commands"][0]["controller_from_native_env"])
        for wrong in ("CODEX_THREAD_ID=fake " + COMMAND, COMMAND.replace(DIRECTORY, "/CANARY_PRIVATE_TEXT")):
            result = safe_input(self.source(wrong), ROOT, NONCE, BINARY, DIRECTORY)
            self.assertNotIn("commands", result)
            self.assertNotIn("CANARY_PRIVATE_TEXT", json.dumps(result))

    def test_snapshot_requires_associated_retained_tool_call(self):
        output = {"timestamp": "2026-09-11T00:00:01Z", "type": "response_item", "payload": {
            "type": "custom_tool_call_output", "call_id": "call_test", "output": json.dumps({"output": json.dumps(SNAPSHOT), "session_id": 99})}}
        unknown = filter_events([output], ROOT, NONCE, BINARY, DIRECTORY)
        self.assertNotIn("snapshots", unknown[0]["output_summary"])
        call = {"timestamp": "2026-09-11T00:00:00Z", "type": "response_item", "payload": {
            "type": "custom_tool_call", "name": "exec", "call_id": "call_test", "input": self.source(COMMAND)}}
        known = filter_events([call, output], ROOT, NONCE, BINARY, DIRECTORY)
        self.assertEqual(known[1]["output_summary"]["snapshots"], [SNAPSHOT])
        join = {"timestamp": "2026-09-11T00:00:02Z", "type": "response_item", "payload": {
            "type": "custom_tool_call", "name": "exec", "call_id": "call_join",
            "input": 'text(await tools.write_stdin({session_id:99,chars:"",yield_time_ms:1000,max_output_tokens:5000}));'}}
        joined_output = dict(output, timestamp="2026-09-11T00:00:03Z", payload=dict(output['payload'], call_id="call_join"))
        joined = filter_events([call, output, join, joined_output], ROOT, NONCE, BINARY, DIRECTORY)
        self.assertEqual(joined[-1]["retained_source"], "known_retained_process_join")
        self.assertEqual(joined[-1]["output_summary"]["snapshots"], [SNAPSHOT])

    def test_unknown_reasoning_message_and_tool_text_never_export(self):
        text = "CANARY_PRIVATE_TEXT"
        values = [{"timestamp": "2026-09-11T00:00:00Z", "type": "response_item", "payload": p} for p in (
            {"type": "reasoning", "text": text},
            {"type": "message", "role": "user", "content": [{"type": "input_text", "text": text}]},
            {"type": "custom_tool_call", "name": "exec", "call_id": "call_private", "input": text})]
        result = filter_events(values, ROOT, NONCE, BINARY, DIRECTORY)
        self.assertEqual(len(result), 2)
        self.assertNotIn(text, json.dumps(result))


if __name__ == "__main__":
    unittest.main()
