"""The one fixed pagination notice is read-only; other bodies remain refused."""
import sys
from pathlib import Path

import pytest

sys.path.insert(0,str(Path(__file__).resolve().parents[3]/'tasks/g0-completion/scripts'))
from owner_helper import NativeWebSocketGate, OwnerHelperAdmissionError, validate_helper_rpc, validate_helper_server_message

SUMMARY='Full-history hydration is deprecated for paginated threads; omit `includeTurns` or set it to `false`, then page with `thread/turns/list` and `thread/items/list`.'


def test_exact_notice_allows_empty_details_but_no_other_payload_or_request():
    for params in ({'summary':SUMMARY},{'summary':SUMMARY,'details':None}):
        message={'method':'deprecationNotice','params':params,'emittedAtMs':3}
        validate_helper_server_message(message,'owner')
        with pytest.raises(OwnerHelperAdmissionError):validate_helper_rpc(message,'owner')
        with pytest.raises(OwnerHelperAdmissionError):validate_helper_server_message(dict(message,id=9),'owner')
    for params in ({'summary':'another notice'},{'summary':SUMMARY,'details':'body'},
                   {'summary':SUMMARY,'extra':None},{'summary':SUMMARY,'details':''}):
        with pytest.raises(OwnerHelperAdmissionError,match='deprecation_notice_envelope'):
            validate_helper_server_message({'method':'deprecationNotice','params':params},'owner')


def test_frame_limit_still_rejects_before_body_and_records_safe_header():
    rows=[]
    gate=NativeWebSocketGate('owner',server=True,on_reject=rows.append)
    gate.feed(b'HTTP/1.1 101 Switching Protocols\r\n\r\n')
    with pytest.raises(OwnerHelperAdmissionError,match='websocket_frame_too_large'):
        gate.feed(b'\x81\x7f'+(65536).to_bytes(8,'big'))
    assert rows[0]['reason']=='websocket_frame_too_large'
    assert rows[0]['frame_payload_bytes']==65536 and rows[0]['frame_opcode']==1
    assert rows[0]['method_sha256'] is None
    assert set(rows[0])=={'stage','reason','failure_type','message_sha256','frame_opcode',
                          'frame_payload_bytes','method_sha256','method_length'}
