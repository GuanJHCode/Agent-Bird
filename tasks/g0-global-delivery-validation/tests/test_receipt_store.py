from pathlib import Path
import json,os,sys
import pytest
ROOT=Path(__file__).resolve().parents[3]
sys.path.insert(0,str(ROOT/'tasks/g0-global-delivery-validation/scripts'))

def store():
    import receipt_store
    return receipt_store


def put(path,value):
    path.write_text(json.dumps(value));path.chmod(0o600)


def test_stable_receipt_and_replacement_refusal(tmp_path):
    module=store();state=tmp_path/'state';state.mkdir(mode=0o700)
    receipts=module.ReceiptStore(state)
    active=state/('a'*32);active.mkdir(mode=0o700);path=active/'service-start.json';put(path,{'version':1})
    assert receipts.read('service-start.json')[0]=={'version':1}
    path.unlink();put(path,{'version':2})
    with pytest.raises(ValueError):receipts.read('service-start.json')
    receipts.close()


@pytest.mark.parametrize('change',['hardlink','symlink','directory'])
def test_rejects_receipt_aliases_and_parent_replacement(tmp_path,change):
    module=store();state=tmp_path/'state';state.mkdir(mode=0o700)
    receipts=module.ReceiptStore(state);active=state/('a'*32);active.mkdir(mode=0o700)
    path=active/'service-start.json';put(path,{'version':1});receipts.discover()
    if change=='hardlink':os.link(path,active/'alias')
    elif change=='symlink':path.rename(active/'real');path.symlink_to(active/'real')
    else:active.rename(state/('b'*32));active.mkdir(mode=0o700);put(path,{'version':1})
    with pytest.raises((ValueError,OSError)):receipts.read('service-start.json')
    receipts.close()


def test_disappeared_bound_activation_is_unknown(tmp_path):
    module=store();state=tmp_path/'state';state.mkdir(mode=0o700);active=state/('a'*32);active.mkdir(mode=0o700)
    receipts=module.ReceiptStore(state);assert receipts.discover();active.rmdir()
    with pytest.raises(ValueError):receipts.discover()
    receipts.close()


@pytest.mark.parametrize('raw',['{"key":1,"key":2}','{"key":NaN}','{"key":Infinity}','{"key":-Infinity}','{"key":1e309}'])
def test_duplicate_json_keys_and_nonfinite_numbers_rejected(tmp_path,raw):
    module=store();state=tmp_path/'state';state.mkdir(mode=0o700);active=state/('a'*32);active.mkdir(mode=0o700)
    path=active/'service-start.json';path.write_text(raw);path.chmod(0o600)
    receipts=module.ReceiptStore(state)
    with pytest.raises(ValueError):receipts.read('service-start.json')
    receipts.close()
