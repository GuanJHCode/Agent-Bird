from pathlib import Path
import os,sys
import pytest
ROOT=Path(__file__).resolve().parents[3]
sys.path.insert(0,str(ROOT/'tasks/g0-completion/scripts'))
import native_delivery_case as base
import native_delivery_client as client


def test_real_parent_is_distinct_from_service():
    parent=base.peer_for_process(os.getppid())
    client.validate_controller_parent({'controller_peer':parent,'proxy_peer':{'pid':parent['pid']+1000000}})


def test_wrong_parent_birth_is_rejected():
    parent=base.peer_for_process(os.getppid())
    with pytest.raises(ValueError):client.validate_controller_parent({'controller_peer':{**parent,'birth':'wrong'},'proxy_peer':parent})
