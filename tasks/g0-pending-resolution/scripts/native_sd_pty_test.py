"""Exercise local PTY I/O only; never launches Codex or a model."""
import json
import sys
import unittest
from native_sd_pty import NativePTY, InputSequence

ROOT = '01a08e0b-bd0d-7400-b069-d50978939cdd'
CANARY = 'CANARY_PRIVATE_TEXT'


class PTYTests(unittest.TestCase):
    def test_input_sequence_rejects_extra_prompt_or_navigation(self):
        sequence = InputSequence('fixed synthetic input')
        self.assertEqual(sequence.take('status'), b'/status\r')
        self.assertEqual(sequence.take('initial'), b'fixed synthetic input\r')
        for extra in ('initial','status','new','resume','continue'):
            with self.assertRaises(ValueError): sequence.take(extra)
        self.assertEqual(sequence.take('quit'), b'/quit\r')
        with self.assertRaises(ValueError): sequence.take('quit')

    def test_real_pty_keeps_only_session_identity_and_digest(self):
        command = "import sys; sys.stdin.readline(); print('Session: "+ROOT+"'); print('"+CANARY+"'); sys.stdout.flush()"
        pty = NativePTY([sys.executable,'-c',command],'.')
        try:
            pty.send(InputSequence('fixed synthetic input'),'status')
            summary = pty.read(2)
            self.assertEqual(summary.get('status_root'),ROOT)
            self.assertGreater(summary.get('bytes',0),36)
            self.assertNotIn(CANARY,json.dumps(summary))
            self.assertNotIn(CANARY,json.dumps(pty.evidence()))
            self.assertEqual(pty.wait(2),0)
        finally: pty.close()

    def test_status_ignores_old_buffer_and_transcript_with_embedded_label(self):
        command = "import sys; print('Session: "+ROOT+"',flush=True); sys.stdin.readline(); print('model transcript: Session:"+ROOT+"',flush=True)"
        pty = NativePTY([sys.executable,'-c',command],'.')
        try:
            before = pty.read(.2)
            self.assertNotIn('status_root',before)
            pty.send(InputSequence('fixed synthetic input'),'status')
            after = pty.read(2)
            self.assertNotIn('status_root',after)
            self.assertGreater(after['status_capture_start_byte'],0)
            self.assertEqual(pty.wait(2),0)
        finally:
            # The fixture may still await its single input when a RED assertion fails.
            if pty.exit_code is None:
                import os
                os.write(pty.fd,b'\r'); pty.wait(2)
            pty.close()


if __name__ == '__main__': unittest.main()
