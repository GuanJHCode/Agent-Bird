"""Local guards for the single owned native case; no Codex/model invocations."""
import unittest
from native_sd_control import require_owned_pair


class ControlTests(unittest.TestCase):
    def test_pair_guard_rejects_dead_reused_or_replaced_ownership(self):
        service = {'pid':101,'started_at_local':'Fri Sep 11 18:00:00 2026'}
        tui = {'pid':102,'started_at_local':'Fri Sep 11 18:00:01 2026'}
        socket = {'exists':True,'dev':1,'ino':2,'uid':501,'mode':'0600','service_holds_path':True}
        original = {'service':service,'tui':tui,'socket':socket}
        self.assertTrue(require_owned_pair(service,tui,socket,original))
        cases = [(None,tui,socket),(service,None,socket),
                 (dict(service,started_at_local='Fri Sep 11 18:01:00 2026'),tui,socket),
                 (service,dict(tui,pid=103),socket),
                 (service,tui,dict(socket,ino=3)),
                 (service,tui,dict(socket,service_holds_path=False)),
                 (service,tui,{'exists':False})]
        for values in cases:
            with self.assertRaises(ValueError): require_owned_pair(*values,original)


if __name__ == '__main__': unittest.main()
