"""Actual HTTP/WebSocket frames, projected without preserving JSON bodies."""
import base64
import hashlib
import json
import os
from pathlib import Path
import struct
import sys
from types import SimpleNamespace as NS

import pytest

ROOT=Path(__file__).resolve().parents[4]
sys.path.insert(0,str(ROOT/'tasks/g0-proxy-continuation/cold-start/scripts'))
import activation_service as service

THREAD='01234567-89ab-4cde-8fab-0123456789ab'
HOME='/private/tmp/g0-auth-protocol/c'
WORKSPACE='/private/tmp/g0-auth-protocol/w'
CANARY='synthetic-auth-and-message-canary-do-not-persist'
CLIENT=(b'GET / HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n'
        b'Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n')
SERVER=(b'HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n'
        b'Sec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\n\r\n')


def frame(value,client=False,raw=False):
    payload=value if raw else json.dumps(value,separators=(',',':')).encode()
    size=len(payload); mask=b'\x01\x02\x03\x04'
    prefix=bytes([0x81,(0x80 if client else 0)|(size if size<126 else 126)])
    if size>=126: prefix+=struct.pack('>H',size)
    return prefix+mask+bytes(byte^mask[i%4] for i,byte in enumerate(payload)) if client else prefix+payload


def event(epoch=1):
    peer=NS(pid=123,uid=os.getuid(),birth='owned-birth',executable='/synthetic/python',complete=True,source='fixture')
    return NS(connection_id=f'c{epoch}',epoch=epoch,frontend_peer=peer,backend_peer=peer)


def setup(tmp_path,*,limits=None):
    published=[]
    def lease(connect):
        return {'lease_id':f'lease{connect.epoch}','profile_id':'synthetic','expected_home':HOME,'expected_cwd':WORKSPACE}
    def publish(row):
        path=tmp_path/f"ready-{row['lease_id']}.json"
        service._write_json(path,row); published.append(path)
    sink=service.MetadataSink(lease_for_connection=lease,publish_ready=publish,observer_limits=limits)
    return sink,published


def feed(sink,data,*,client=False,epoch=1):
    sink.on_data(NS(connection_id=f'c{epoch}',epoch=epoch,direction='frontend_to_backend' if client else 'backend_to_frontend',data=data))


def connect(sink,epoch=1):
    sink.on_connect(event(epoch)); feed(sink,CLIENT,client=True,epoch=epoch); feed(sink,SERVER,epoch=epoch)


def initialize(sink,epoch=1,home=HOME):
    feed(sink,frame({'id':1,'method':'initialize','params':{'secret':CANARY}},True),client=True,epoch=epoch)
    feed(sink,frame({'id':1,'result':{'codexHome':home,'auth':CANARY}}),epoch=epoch)
    feed(sink,frame({'method':'initialized'},True),client=True,epoch=epoch)


def start(sink,epoch=1,*,suffix=b''):
    feed(sink,frame({'id':2,'method':'thread/start','params':{'cwd':WORKSPACE,'prompt':CANARY}},True),client=True,epoch=epoch)
    feed(sink,frame({'id':2,'result':{'thread':{'id':THREAD,'cwd':WORKSPACE,'turns':[CANARY]},'secret':CANARY}}),epoch=epoch)
    feed(sink,frame({'method':'thread/started','params':{'thread':{'id':THREAD,'cwd':WORKSPACE,'turns':[CANARY]}}})+suffix,epoch=epoch)


def test_ready_receipt_precedes_shutdown_and_only_safe_fields_are_retained(tmp_path):
    sink,paths=setup(tmp_path); connect(sink); initialize(sink)
    assert not paths
    start(sink)
    assert len(paths)==1 and paths[0].stat().st_mode&0o777==0o600
    row=json.loads(paths[0].read_text())
    assert row['initialize']=={'request_id':1,'home_match':True,'home_sha256':hashlib.sha256(HOME.encode()).hexdigest()}
    assert row['thread']=={'start_request_id':2,'id':THREAD,'started_id':THREAD}
    assert row['turn_counts']=={'start':0,'steer':0}
    assert row['protocol_valid'] and row['milestone_only']
    assert not row['closed']
    encoded=json.dumps(sink.snapshot())+paths[0].read_text()
    assert CANARY not in encoded and HOME not in encoded
    assert '"params"' not in encoded and '"result"' not in encoded


def test_epochs_cannot_share_initialize_or_thread_evidence(tmp_path):
    sink,paths=setup(tmp_path); connect(sink,1); initialize(sink,1); connect(sink,2); start(sink,2)
    assert not paths


@pytest.mark.parametrize('home',[None,'',HOME+'-other',{'path':HOME}])
def test_initialize_requires_exact_scalar_registered_backend_home(tmp_path,home):
    sink,paths=setup(tmp_path); connect(sink); initialize(sink,home=home); start(sink)
    assert not paths


@pytest.mark.parametrize('suffix',[b'\x81\x01{',b'\x81\x7e\xff\xff'])
def test_malformed_or_overlarge_trailing_frame_cannot_publish_ready(tmp_path,suffix):
    sink,paths=setup(tmp_path,limits={'max_frame_bytes':4096}); connect(sink); initialize(sink); start(sink,suffix=suffix)
    assert not paths
    assert sink.snapshot()['protocol'][0]['protocol_valid'] is False


@pytest.mark.parametrize('method',['turn/start','turn/steer'])
def test_turn_request_prevents_ready_and_is_counted_even_when_opaque(tmp_path,method):
    sink,paths=setup(tmp_path); connect(sink); initialize(sink)
    feed(sink,frame({'id':9,'method':method,'params':{'input':[CANARY]}},True),client=True)
    start(sink)
    assert not paths
    assert sink.snapshot()['protocol'][0]['turn_counts'][method.split('/')[1]]==1


def test_ready_is_historical_and_later_turn_is_visible_in_final_protocol(tmp_path):
    sink,paths=setup(tmp_path); connect(sink); initialize(sink); start(sink)
    feed(sink,frame({'id':9,'method':'turn/start','params':{'input':[CANARY]}},True),client=True)
    sink.on_lifecycle(NS(connection_id='c1',epoch=1,kind='eof',direction='frontend_to_backend'))
    assert len(paths)==1
    final=sink.snapshot()['protocol'][0]
    assert final['closed'] and final['turn_counts']['start']==1 and not final['zero_turns']


def test_matching_request_ids_on_other_epoch_do_not_complete_a_thread(tmp_path):
    sink,paths=setup(tmp_path)
    for epoch in (1,2): connect(sink,epoch); initialize(sink,epoch)
    feed(sink,frame({'id':2,'method':'thread/start','params':{'cwd':WORKSPACE}},True),client=True,epoch=1)
    feed(sink,frame({'id':2,'result':{'thread':{'id':THREAD,'cwd':WORKSPACE}}}),epoch=2)
    feed(sink,frame({'method':'thread/started','params':{'thread':{'id':THREAD,'cwd':WORKSPACE}}}),epoch=2)
    assert not paths


def test_unregistered_connection_cannot_publish_a_ready_receipt(tmp_path):
    sink,paths=setup(tmp_path); sink._leases=lambda _:None
    connect(sink); initialize(sink); start(sink)
    assert not paths
