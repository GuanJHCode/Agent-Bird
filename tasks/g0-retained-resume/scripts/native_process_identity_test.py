#!/usr/bin/env python3
import unittest
from native_process_identity import same_process


class ProcessIdentityTest(unittest.TestCase):
    def test_reparented_process_is_still_alive(self):
        before = {"pid": 8198, "ppid": 7238, "pgid": 8198,
                  "started_at_local": "Fri Sep 11 13:59:16 2026", "executable_basename": "Python"}
        after = dict(before, ppid=1)
        self.assertTrue(same_process(after, before))

    def test_absence_and_reused_pid_are_not_original_process(self):
        before = {"pid": 8198, "started_at_local": "Fri Sep 11 13:59:16 2026"}
        self.assertFalse(same_process(None, before))
        self.assertFalse(same_process(dict(before, started_at_local="Fri Sep 11 14:00:16 2026"), before))


if __name__ == "__main__":
    unittest.main()
