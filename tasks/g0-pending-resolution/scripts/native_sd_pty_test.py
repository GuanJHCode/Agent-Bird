"""Exercise local PTY I/O only; never launches Codex or a model."""
import json
import os
import signal
import sys
import unittest
from unittest.mock import patch
from native_sd_pty import NativePTY, InputSequence

ROOT = '01a08e0b-bd0d-7400-b069-d50978939cdd'
CANARY = 'CANARY_PRIVATE_TEXT'


class HistoricalStatusSequence:
    """Explicit transport-only fixture; unavailable to the current controller."""
    def __init__(self,prompt): self.used=[]
    def take(self,action):
        expected=('status-paste','status-submit','quit-paste','quit-submit')[len(self.used)]
        if action!=expected: raise ValueError('historical status fixture order')
        self.used.append(action)
        return {'status-paste':b'\x1b[200~/status\x1b[201~','status-submit':b'\r',
                'quit-paste':b'\x1b[200~/quit\x1b[201~','quit-submit':b'\r'}[action]


class PTYTests(unittest.TestCase):
    def test_argv_initial_is_once_without_any_pty_initial_input(self):
        sequence=InputSequence('fixed synthetic input')
        for extra in ('initial-paste','initial-submit','initial-argv','status-paste','status-submit','quit-paste'):
            with self.assertRaises(ValueError): sequence.take(extra)
        self.assertEqual(sequence.take_argv(),['codex','fixed synthetic input'])
        with self.assertRaises(ValueError): sequence.take_argv()
        for extra in ('initial-paste','initial-submit','initial-argv','status-paste','status-submit','continue'):
            with self.assertRaises(ValueError): sequence.take(extra)

    def test_input_sequence_rejects_extra_prompt_or_navigation(self):
        sequence = InputSequence('fixed synthetic input')
        for invalid in ('status','status-paste','status-submit','quit','quit-submit'):
            with self.assertRaises(ValueError): sequence.take(invalid)
        self.assertEqual(sequence.take_argv(),['codex','fixed synthetic input'])
        for extra in ('initial','initial-paste','initial-submit','status','status-paste','status-submit','new','resume','continue'):
            with self.assertRaises(ValueError): sequence.take(extra)
        self.assertEqual(sequence.take('quit-paste'), b'\x1b[200~/quit\x1b[201~')
        self.assertEqual(sequence.take('quit-submit'), b'\r')
        for extra in ('quit','quit-paste','quit-submit','status-paste'):
            with self.assertRaises(ValueError): sequence.take(extra)

    def test_expired_launch_deadline_cannot_fork(self):
        with patch('native_sd_pty.continuous_ns',return_value=121000000000),patch('native_sd_pty.pty.fork') as fork:
            for deadline in (120000000000,121000000000):
                with self.subTest(deadline=deadline),self.assertRaises(TimeoutError):
                    NativePTY(['codex','fixed'],'.',launch_deadline_ns=deadline)
            fork.assert_not_called()

    def test_real_pty_keeps_only_session_identity_and_digest(self):
        command = ("import os,sys,tty\ntty.setraw(0)\nprint('FIXTURE_READY',flush=True)\n"
                   "def read_exact(n):\n data=b''\n while len(data)<n: data+=os.read(0,n-len(data))\n return data\n"
                   "assert read_exact(19)==b'\\x1b[200~/status\\x1b[201~'\nassert read_exact(1)==b'\\r'\n"
                   "print('Session: "+ROOT+"\\r\\n"+CANARY+"',flush=True)\n"
                   "assert read_exact(17)==b'\\x1b[200~/quit\\x1b[201~'\nassert read_exact(1)==b'\\r'\n")
        pty = NativePTY([sys.executable,'-c',command],'.')
        try:
            pty.read(.2); sequence=HistoricalStatusSequence('fixed synthetic input')
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
            sequence=HistoricalStatusSequence('fixed synthetic input')
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
