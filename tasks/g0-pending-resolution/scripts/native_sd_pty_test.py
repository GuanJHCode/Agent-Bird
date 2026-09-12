"""Exercise local PTY I/O only; never launches Codex or a model."""
import json
import os
import signal
import sys
import unittest
from native_sd_pty import NativePTY, InputSequence

ROOT = '01a08e0b-bd0d-7400-b069-d50978939cdd'
CANARY = 'CANARY_PRIVATE_TEXT'


class PTYTests(unittest.TestCase):
    def test_direct_initial_is_once_without_status_or_followup_enter(self):
        sequence=InputSequence('fixed synthetic input')
        self.assertEqual(sequence.take('initial-paste'),b'\x1b[200~fixed synthetic input\x1b[201~')
        with self.assertRaises(ValueError): sequence.take('quit-paste')
        self.assertEqual(sequence.take('initial-submit'),b'\r')
        for extra in ('initial-paste','initial-submit','status-paste','status-submit','continue'):
            with self.assertRaises(ValueError): sequence.take(extra)

    def test_input_sequence_rejects_extra_prompt_or_navigation(self):
        sequence = InputSequence('fixed synthetic input')
        for invalid in ('status','status-submit','quit','quit-submit'):
            with self.assertRaises(ValueError): sequence.take(invalid)
        self.assertEqual(sequence.take('status-paste'), b'\x1b[200~/status\x1b[201~')
        with self.assertRaises(ValueError): sequence.take('quit-paste')
        self.assertEqual(sequence.take('status-submit'), b'\r')
        try: framed=sequence.take('initial-paste')
        except ValueError: framed=None
        self.assertEqual(framed,b'\x1b[200~fixed synthetic input\x1b[201~')
        self.assertEqual(sequence.take('initial-submit'), b'\r')
        for extra in ('initial','initial-paste','initial-submit','status','status-paste','status-submit','new','resume','continue'):
            with self.assertRaises(ValueError): sequence.take(extra)
        self.assertEqual(sequence.take('quit-paste'), b'\x1b[200~/quit\x1b[201~')
        self.assertEqual(sequence.take('quit-submit'), b'\r')
        for extra in ('quit','quit-paste','quit-submit','status-paste'):
            with self.assertRaises(ValueError): sequence.take(extra)

    def test_real_pty_keeps_only_session_identity_and_digest(self):
        command = ("import os,sys,tty\ntty.setraw(0)\nprint('FIXTURE_READY',flush=True)\n"
                   "def read_exact(n):\n data=b''\n while len(data)<n: data+=os.read(0,n-len(data))\n return data\n"
                   "assert read_exact(19)==b'\\x1b[200~/status\\x1b[201~'\nassert read_exact(1)==b'\\r'\n"
                   "print('Session: "+ROOT+"\\r\\n"+CANARY+"',flush=True)\n"
                   "assert read_exact(17)==b'\\x1b[200~/quit\\x1b[201~'\nassert read_exact(1)==b'\\r'\n")
        pty = NativePTY([sys.executable,'-c',command],'.')
        try:
            pty.read(.2); sequence=InputSequence('fixed synthetic input')
            pty.send(sequence,'status-paste')
            self.assertNotIn('status_root',pty.read(.02))
            pty.send(sequence,'status-submit')
            summary = pty.read(.2)
            self.assertEqual(summary.get('status_root'),ROOT)
            self.assertGreater(summary.get('bytes',0),36)
            self.assertNotIn(CANARY,json.dumps(summary))
            self.assertNotIn(CANARY,json.dumps(pty.evidence()))
            pty.send(sequence,'quit-paste'); pty.send(sequence,'quit-submit')
            self.assertEqual(pty.wait(2),0)
        finally:
            if pty._poll() is None: os.kill(pty.pid,signal.SIGTERM); pty.wait(2)
            pty.close()

    def test_status_ignores_old_buffer_and_transcript_with_embedded_label(self):
        command = "import sys; print('Session: "+ROOT+"',flush=True); sys.stdin.readline(); print('model transcript: Session:"+ROOT+"',flush=True)"
        pty = NativePTY([sys.executable,'-c',command],'.')
        try:
            before = pty.read(.2)
            self.assertNotIn('status_root',before)
            sequence=InputSequence('fixed synthetic input')
            pty.send(sequence,'status-paste'); pty.send(sequence,'status-submit')
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
