"""PUBLIC fixtures only: no real source, defaults, scenario finish, CLI or model."""
import copy
import hashlib
import json
import os
import sys
from pathlib import Path
from types import SimpleNamespace

import pytest
from test_final_trial import launcher, prepared, private_json, public_result
sys.path.insert(0, str(Path(__file__).resolve().parent.parent/'scripts'))
import live_sequence as sequence


def fake_scenario(launcher, root, monkeypatch, *, outcome=None, returncode=0):
    def run(args, **kwargs):
        assert 'timeout' not in kwargs
        area = root / ('codex-to-claude' if 'codex-to-claude' in args[args.index('--run-dir')+1] else 'claude-to-codex')
        (area/'live-run').mkdir(mode=0o700)
        private_json(area/'live-run/scenario-result.json', outcome or public_result())
        return SimpleNamespace(returncode=returncode, stdout=b'PUBLIC', stderr=b'')
    monkeypatch.setattr(launcher.subprocess, 'run', run)


def complete_first(launcher, prepared, monkeypatch, *, result=None):
    root, first, _, setup = prepared
    scratch = first/'scratch'
    scratch.mkdir(mode=0o700)
    evidence = scratch/'validation-public.json'
    private_json(evidence, {'fixture':'PUBLIC_VALIDATION'})
    validation_sha = hashlib.sha256(evidence.read_bytes()).hexdigest()
    outcome = result or public_result(validation_sha)
    bound = first/'bound-contract.json'
    private_json(bound, {'dispatch':{'request_id':'fake-only'},'scratch_dir':str(scratch),'state_dir':str(first/'state')})
    setup['pins'] = [launcher.pin(item['path']) for item in setup['pins']]
    private_json(root/'setup.json', setup)
    fake_scenario(launcher, root, monkeypatch, outcome=outcome)
    with pytest.raises(SystemExit):
        launcher.launch(root, sequence.DIRECTIONS[0])
    return root, first, setup, outcome, evidence


def forbid_source_and_processes(launcher, monkeypatch):
    monkeypatch.setattr(launcher, 'environment', lambda root: pytest.fail('sequence must reject before source read'))
    monkeypatch.setattr(launcher, 'command', lambda *a, **k: pytest.fail('no helper/defaults/finish'))
    monkeypatch.setattr(launcher.subprocess, 'run', lambda *a, **k: pytest.fail('no scenario/model'))


def test_second_direction_cannot_start_first(launcher, prepared, monkeypatch):
    root, _, _, _ = prepared
    forbid_source_and_processes(launcher, monkeypatch)
    with pytest.raises(ValueError, match='first_direction_completion_required'):
        launcher.launch(root, sequence.DIRECTIONS[1])
    assert not (root/sequence.DIRECTIONS[1]/sequence.STARTED).exists()


def test_blocked_first_cannot_open_second(launcher, prepared, monkeypatch):
    blocked = public_result(); blocked['status']='BLOCKED'
    root, *_ = complete_first(launcher, prepared, monkeypatch, result=blocked)
    forbid_source_and_processes(launcher, monkeypatch)
    with pytest.raises(ValueError, match='first_scenario_not_passed'):
        launcher.launch(root, sequence.DIRECTIONS[1])


@pytest.mark.parametrize('field,value', [
    ('timed_out',True),('exit_code',1),('output_channels_closed',False),
    ('remaining_owned_pids',[12]),('unverified_owned_pids',[13]),('discovery_errors',['PUBLIC_UNKNOWN']),
    ('postflight.status','BLOCKED'),('postflight.parent_accept',False),('postflight.transport_ack',False),
    ('postflight.active',1),('postflight.unknown',1),('postflight.reserved_ms',1),
    ('postflight.actionable_pending',1),('postflight.used_ms',90001),('postflight.used_ms',-1),
    ('postflight.attempts',0),('postflight.attempts',3),('postflight.attempts',True),
    ('postflight.integrated',True),('postflight.run_id',''),('postflight.task_id',''),
    ('postflight.candidate_oid',''),('postflight.cli_handle',''),('postflight.tree_oid',''),
    ('postflight.validation_evidence_sha256',''),
])
def test_bad_closure_rejects_second_before_source(launcher, prepared, monkeypatch, field, value):
    result = public_result()
    if '.' in field:
        _, key = field.split('.')
        result['postflight'][key]=value
    else:
        result[field]=value
    root, *_ = complete_first(launcher, prepared, monkeypatch, result=result)
    forbid_source_and_processes(launcher, monkeypatch)
    with pytest.raises(ValueError):
        launcher.launch(root, sequence.DIRECTIONS[1])


def test_concurrent_launch_rejected_before_inputs(launcher, prepared, monkeypatch):
    root, _, _, _ = prepared
    forbid_source_and_processes(launcher, monkeypatch)
    with sequence.hold(root):
        with pytest.raises(ValueError, match='live_sequence_busy'):
            launcher.launch(root, sequence.DIRECTIONS[0])
    lock = root/sequence.LOCK
    inode = lock.stat().st_ino
    with sequence.hold(root):
        assert lock.stat().st_ino == inode
    assert lock.exists() and lock.stat().st_mode & 0o777 == 0o600


def test_source_preflight_failure_still_consumes_direction(launcher, prepared, monkeypatch):
    root, area, _, _ = prepared
    monkeypatch.setattr(launcher, 'environment', lambda root: (_ for _ in ()).throw(ValueError('PUBLIC_PREFLIGHT_FAILED')))
    with pytest.raises(ValueError, match='PUBLIC_PREFLIGHT_FAILED'):
        launcher.launch(root, sequence.DIRECTIONS[0])
    assert (area/sequence.STARTED).exists()
    assert (area/sequence.COMPLETED).exists()
    forbid_source_and_processes(launcher, monkeypatch)
    with pytest.raises(ValueError, match='never_reset_consumed_direction'):
        launcher.launch(root, sequence.DIRECTIONS[0])


@pytest.mark.parametrize('missing', [sequence.STARTED, sequence.COMPLETED, 'scenario-result'])
def test_missing_first_marker_or_result_rejects(launcher, prepared, monkeypatch, missing):
    root, first, *_ = complete_first(launcher, prepared, monkeypatch)
    path = first/'live-run/scenario-result.json' if missing=='scenario-result' else first/missing
    path.unlink()
    forbid_source_and_processes(launcher, monkeypatch)
    with pytest.raises((ValueError, FileNotFoundError)):
        launcher.launch(root, sequence.DIRECTIONS[1])


@pytest.mark.parametrize('changed', [sequence.STARTED, sequence.COMPLETED, 'scenario-result'])
def test_changed_first_marker_or_result_rejects(launcher, prepared, monkeypatch, changed):
    root, first, *_ = complete_first(launcher, prepared, monkeypatch)
    path = first/'live-run/scenario-result.json' if changed=='scenario-result' else first/changed
    private_json(path, {'PUBLIC':'TAMPERED'})
    forbid_source_and_processes(launcher, monkeypatch)
    with pytest.raises(ValueError):
        launcher.launch(root, sequence.DIRECTIONS[1])


def setup_second(root, setup, launcher):
    second = root/sequence.DIRECTIONS[1]
    private_json(second/'bound-contract.json', {'dispatch':{'request_id':'PUBLIC_SECOND'},'state_dir':str(second/'state')})
    return second


def test_valid_first_and_fresh_identical_finish_allows_second(launcher, prepared, monkeypatch):
    root, first, setup, outcome, evidence = complete_first(launcher, prepared, monkeypatch)
    second = setup_second(root, setup, launcher)
    env = {'PUBLIC_ENV':'SAME_OBJECT'}
    monkeypatch.setattr(launcher, 'environment', lambda root: env)
    observations=[]
    def command(args, supplied_env):
        assert supplied_env is env
        observations.append(args)
        if args[0] == launcher.PY:
            assert args[:4] == [launcher.PY,'-B',launcher.HARNESS/'scenario.py','finish']
            assert args[args.index('--validation-evidence')+1] == evidence
            assert '--integrate-private' not in args
            return json.dumps(outcome['postflight']).encode()
        return b'{"PUBLIC":"proof"}'
    monkeypatch.setattr(launcher, 'command', command)
    fake_scenario(launcher, root, monkeypatch)
    with pytest.raises(SystemExit) as result:
        launcher.launch(root, sequence.DIRECTIONS[1])
    assert result.value.code == 0
    assert len(observations)==2  # fresh readonly finish, then unchanged source proof
    assert (second/sequence.STARTED).exists() and (second/sequence.COMPLETED).exists()
    assert sequence.read_json(second/sequence.COMPLETED)['scenario_exit_code']==0


def test_fresh_ledger_drift_consumes_second_without_scenario(launcher, prepared, monkeypatch):
    root, _, setup, outcome, _ = complete_first(launcher, prepared, monkeypatch)
    second = setup_second(root, setup, launcher)
    def command(args, env):
        changed=copy.deepcopy(outcome['postflight']);changed['used_ms']+=1
        return json.dumps(changed).encode()
    monkeypatch.setattr(launcher, 'command', command)
    monkeypatch.setattr(launcher.subprocess, 'run', lambda *a, **k: pytest.fail('no model with ledger drift'))
    with pytest.raises(ValueError, match='first_fresh_postflight_changed'):
        launcher.launch(root, sequence.DIRECTIONS[1])
    assert (second/sequence.STARTED).exists() and not (second/'live-run').exists()


@pytest.mark.parametrize('count', [0,2,5])
def test_validation_file_set_must_have_one_matching_hash_and_at_most_four(launcher, prepared, monkeypatch, count):
    root, first, _, outcome, evidence = complete_first(launcher, prepared, monkeypatch)
    if count==0:
        evidence.unlink()
    else:
        for index in range(count-1):
            private_json(evidence.parent/f'validation-copy-{index}.json', {'fixture':'PUBLIC_VALIDATION'})
    monkeypatch.setattr(launcher, 'command', lambda *a, **k: pytest.fail('no finish on invalid evidence set'))
    with pytest.raises(ValueError):
        sequence.fresh_first(root, outcome, {}, launcher.PY, launcher.HARNESS/'scenario.py', launcher.command)


@pytest.mark.parametrize('kind', ['symlink','hardlink','mode'])
def test_lock_file_must_be_private_single_link_regular(launcher, prepared, kind):
    root, *_ = prepared
    lock = root/sequence.LOCK
    other=root/'PUBLIC_LOCK_TARGET';other.write_text('PUBLIC');other.chmod(0o600)
    if kind=='symlink':lock.symlink_to(other)
    elif kind=='hardlink':os.link(other,lock)
    else:lock.write_text('PUBLIC');lock.chmod(0o644)
    with pytest.raises((ValueError,OSError)):
        with sequence.hold(root):pytest.fail('invalid lock admitted')


def test_first_requires_both_directions_unstarted(launcher, prepared, monkeypatch):
    root, *_ = prepared
    private_json(root/sequence.DIRECTIONS[1]/sequence.STARTED, {'PUBLIC':'consumed'})
    forbid_source_and_processes(launcher, monkeypatch)
    with pytest.raises(ValueError, match='first_direction_requires_prepared'):
        launcher.launch(root, sequence.DIRECTIONS[0])


def test_lock_covers_checks_environment_scenario_and_receipt(launcher, prepared, monkeypatch):
    root, area, _, _ = prepared
    stages=[]
    def locked(stage):
        with pytest.raises(ValueError, match='live_sequence_busy'):
            with sequence.hold(root):
                pytest.fail('outer launch lost lock')
        stages.append(stage)
    original_check = launcher.check
    monkeypatch.setattr(launcher, 'check', lambda: locked('check'))
    def environment(root):
        locked('environment')
        return {'PUBLIC':'ENV'}
    monkeypatch.setattr(launcher,'environment',environment)
    def run(args, **kwargs):
        locked('scenario')
        (area/'live-run').mkdir(mode=0o700)
        private_json(area/'live-run/scenario-result.json',public_result())
        return SimpleNamespace(returncode=0,stdout=b'',stderr=b'')
    monkeypatch.setattr(launcher.subprocess,'run',run)
    original_complete = sequence.complete
    def complete(*args):
        locked('receipt')
        return original_complete(*args)
    monkeypatch.setattr(sequence,'complete',complete)
    with pytest.raises(SystemExit) as result:
        launcher.launch(root,sequence.DIRECTIONS[0])
    assert result.value.code==0 and stages==['check','environment','scenario','receipt']


def test_lock_fd_is_noninheritable(launcher,prepared,monkeypatch):
    root,*_=prepared
    original = sequence.fcntl.flock
    seen=[]
    def observed(fd,operation):
        seen.append(os.get_inheritable(fd))
        return original(fd,operation)
    monkeypatch.setattr(sequence.fcntl,'flock',observed)
    with sequence.hold(root):
        pass
    assert seen==[False,False]
