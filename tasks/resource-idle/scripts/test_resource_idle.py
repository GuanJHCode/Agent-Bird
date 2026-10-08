import importlib.util
import pathlib
import unittest
from unittest.mock import patch

source = pathlib.Path(__file__).with_name('resource_idle.py')
spec = importlib.util.spec_from_file_location('idle', source)
idle = importlib.util.module_from_spec(spec)
spec.loader.exec_module(idle)


class RetirementBoundary(unittest.TestCase):
    def test_query_completes_after_deadline(self):
        clock = [112.9]
        ctx = {'pending_observed_at': 100, 'identities': {'7': {'role': 'owner', 'pid': 7}}}
        def late_absence(*_):
            clock[0] = 113.1
            return False
        with patch.object(idle.time, 'monotonic', side_effect=lambda: clock[0]), patch.object(idle, 'identity_present', side_effect=late_absence):
            with self.assertRaisesRegex(idle.Failure, 'within retirement interval'):
                idle.observe_retirement(ctx, {})
        self.assertEqual(ctx['retirement']['owner']['first_gone'], 113.1)


if __name__ == '__main__':
    unittest.main()
