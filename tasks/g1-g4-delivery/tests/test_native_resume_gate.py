"""Actual bounded WS decoder: resume requires a fresh explicit owner chain."""
from pathlib import Path
import sys,json
import pytest
ROOT=Path(__file__).resolve().parents[3]
sys.path.insert(0,str(ROOT/'tasks/g0-proxy-continuation/cold-start/tests'))
from test_activation_protocol import service,setup,connect,initialize,feed,frame,THREAD,WORKSPACE,CANARY

@pytest.mark.parametrize('fault',[None,'wrong-target','wrong-response','skills-before','turn-before','unregistered-resume'])
def test_resume_requires_owned_target_and_fresh_response_barrier(tmp_path,fault):
    sink,paths=setup(tmp_path);original=sink._leases
    def lease(event):
        row=original(event)
        if fault!='unregistered-resume':row['expected_resume_thread_id']=THREAD
        return row
    sink._leases=lease;connect(sink);initialize(sink)
    def skills():
        feed(sink,frame({'id':3,'method':'skills/list','params':{}},True),client=True)
        feed(sink,frame({'id':3,'result':{'data':[]}}))
    if fault=='skills-before':skills()
    feed(sink,frame({'id':2,'method':'thread/resume','params':{'threadId':THREAD if fault!='wrong-target' else 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'}},True),client=True)
    feed(sink,frame({'id':2,'result':{'thread':{'id':THREAD if fault!='wrong-response' else 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa','cwd':WORKSPACE,'turns':[CANARY]}}}))
    assert not paths
    if fault=='turn-before':feed(sink,frame({'id':4,'method':'turn/start','params':{'threadId':THREAD,'input':[]}},True),client=True)
    if fault!='skills-before':skills()
    assert bool(paths)==(fault is None)
    if paths:
        row=json.loads(paths[0].read_text());assert row['thread']['id']==THREAD and row['thread']['resume_request_id']==2
        assert row['zero_turns'] and CANARY not in paths[0].read_text()


@pytest.mark.parametrize('policy',[{}, {'version':True,'resume_owned_thread':True,'initial_history_discovery':True},
    {'version':1,'resume_owned_thread':1,'initial_history_discovery':True},
    {'version':1,'resume_owned_thread':True,'initial_history_discovery':True,'extra_source':'untrusted'}])
def test_native_tui_policy_rejects_unfrozen_variants(policy):
    with pytest.raises(ValueError):service.native_tui_policy(policy)
    assert service.native_tui_policy(None) is None



def test_reader_waits_for_publisher_link_cleanup_without_reading_body(tmp_path,monkeypatch):
    import threading
    sys.path.insert(0,str(ROOT/'tasks/g0-global-delivery-validation/scripts'))
    from receipt_store import ReceiptStore
    state=tmp_path/'state';state.mkdir(mode=0o700);directory=state/('a'*32);directory.mkdir(mode=0o700)
    linked=threading.Event();release=threading.Event();link=service.os.link
    def paused(*args,**kwargs):
        link(*args,**kwargs);linked.set();assert release.wait(2)
    monkeypatch.setattr(service.os,'link',paused)
    worker=threading.Thread(target=service._publish_receipt,args=(directory/'backend-controlled.json',{'version':1}))
    worker.start();assert linked.wait(2)
    strict=ReceiptStore(state);pending=ReceiptStore(state,pending_publication=True)
    try:
        with pytest.raises(ValueError,match='unsafe'):strict.read('backend-controlled.json')
        assert pending.read('backend-controlled.json') is None
        assert not pending.pins
        release.set();worker.join(2);assert not worker.is_alive()
        assert pending.read('backend-controlled.json')[0]=={'version':1}
    finally:release.set();worker.join(2);strict.close();pending.close()


@pytest.mark.parametrize('field,value',[('threadId','wrong-owner'),('itemsView','summary'),('sortDirection','desc'),('limit',True),('cursor','x'*257)])
def test_paged_helper_read_cannot_escape_owner_or_full_history(field,value):
    sys.path.insert(0,str(ROOT/'tasks/g0-completion/scripts'))
    from owner_helper import validate_helper_rpc,OwnerHelperAdmissionError
    params={'threadId':THREAD,'cursor':None,'limit':2,'sortDirection':'asc','itemsView':'full'}
    validate_helper_rpc({'id':2001,'method':'thread/turns/list','params':params},THREAD)
    with pytest.raises(OwnerHelperAdmissionError):validate_helper_rpc({'id':2001,'method':'thread/turns/list','params':{**params,field:value}},THREAD)



def test_resume_original_process_identity_distinguishes_reuse_from_unknown(monkeypatch):
    import os
    pid=os.getpid();birth,executable=service._process_metadata(pid)
    assert birth and executable
    assert service._original_process_gone(pid,birth,executable) is False
    assert service._original_process_gone(pid,'known-prior-birth',executable) is True
    monkeypatch.setattr(service,'_process_metadata',lambda _: (None,None))
    with pytest.raises(ValueError,match='unknown'):service._original_process_gone(pid,birth,executable)
    monkeypatch.setattr(service,'_process_metadata',lambda _: (birth,None))
    with pytest.raises(ValueError,match='unknown'):service._original_process_gone(pid,birth,executable)


def test_two_concurrent_post_resume_skills_responses_keep_owner_ready(tmp_path):
    sink,paths=setup(tmp_path);original=sink._leases
    sink._leases=lambda event:{**original(event),'expected_resume_thread_id':THREAD}
    connect(sink);initialize(sink)
    feed(sink,frame({'id':5,'method':'thread/resume','params':{'threadId':THREAD}},True),client=True)
    feed(sink,frame({'id':5,'result':{'thread':{'id':THREAD,'cwd':WORKSPACE}}}))
    requests=['startup-skills-list-controlled',9]
    feed(sink,b''.join(frame({'id':identifier,'method':'skills/list','params':{}},True) for identifier in requests),client=True)
    assert not paths
    feed(sink,b''.join(frame({'id':identifier,'result':{'data':[]}}) for identifier in requests))
    assert len(paths)==1
    state=sink.snapshot()['protocol'][0]
    assert state['protocol_valid'] and state['ready_published'] and state['thread']['id']==THREAD and state['zero_turns']
