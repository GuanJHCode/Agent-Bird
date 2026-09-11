#!/usr/bin/env python3
import datetime
import fcntl
import os
import pathlib
import tempfile
import unittest
from native_retained_watch import action_deadline, lock_state


class RetainedWatchTest(unittest.TestCase):
    def test_lock_probe_is_nonblocking_and_releases_its_own_lock(self):
        temporary = pathlib.Path(__file__).resolve().parents[1] / 'tmp'
        temporary.mkdir(exist_ok=True)
        with tempfile.TemporaryDirectory(prefix='native-lock-test-', dir=temporary) as name:
            path = pathlib.Path(name) / 'retained.lock'; path.touch(mode=0o600)
            self.assertTrue(lock_state(path)['available'])
            with path.open('rb') as owner:
                fcntl.flock(owner, fcntl.LOCK_EX | fcntl.LOCK_NB)
                self.assertFalse(lock_state(path)['available'])
            self.assertTrue(lock_state(path)['available'])

    def test_only_fresh_typed_action_starts_ten_second_window(self):
        begin = datetime.datetime(2026,9,11,0,0,tzinfo=datetime.timezone.utc)
        action = {'root_thread':'fixed-root','nonce':'rr-retained-kill-01','native_pid':123,'action':'SIGKILL','at':'2026-09-11T00:00:01+00:00'}
        end = action_deadline(action, 'fixed-root', 'rr-retained-kill-01', 123, begin, begin + datetime.timedelta(seconds=2))
        self.assertEqual(end, begin + datetime.timedelta(seconds=11))
        for bad in (dict(action, at='2026-09-10T23:59:59+00:00'), dict(action, native_pid=True), dict(action, private='CANARY_PRIVATE_TEXT')):
            with self.assertRaises(ValueError):
                action_deadline(bad,'fixed-root','rr-retained-kill-01',123,begin,begin+datetime.timedelta(seconds=2))


if __name__ == '__main__':
    unittest.main()
