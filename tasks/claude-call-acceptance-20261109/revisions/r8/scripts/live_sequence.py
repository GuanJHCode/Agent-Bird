"""Durable, owner-only, strictly serial two-direction live launch gate."""
from contextlib import contextmanager
import fcntl
import hashlib
import json
import os
from pathlib import Path
import stat

DIRECTIONS = ('codex-to-claude', 'claude-to-codex')
LOCK = 'live-sequence.lock'
STARTED = 'live-started.json'
COMPLETED = 'live-completed.json'


def require(ok, reason):
    if not ok:
        raise ValueError(reason)


def owned_directory(path):
    path = Path(path)
    info = path.lstat()
    require(path.is_absolute() and path.resolve() == path and stat.S_ISDIR(info.st_mode)
            and stat.S_IMODE(info.st_mode) == 0o700 and info.st_uid == os.getuid(),
            'sequence_private_directory_required')


def private_info(info):
    return (stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode) == 0o600
            and info.st_uid == os.getuid() and info.st_nlink == 1)


def read_bytes(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
    try:
        require(private_info(os.fstat(fd)), 'sequence_private_file_required')
        with os.fdopen(fd, 'rb', closefd=False) as stream:
            raw = stream.read(1024 * 1024 + 1)
        require(len(raw) <= 1024 * 1024, 'sequence_file_too_large')
        return raw
    finally:
        os.close(fd)


def read_json(path):
    return json.loads(read_bytes(path))


def sha(path):
    return hashlib.sha256(read_bytes(path)).hexdigest()


def exists(path):
    return os.path.lexists(path)


def exclusive_json(path, value):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
    try:
        require(private_info(os.fstat(fd)), 'sequence_private_file_required')
        raw = (json.dumps(value, sort_keys=True, indent=2) + '\n').encode()
        with os.fdopen(fd, 'wb', closefd=False) as stream:
            stream.write(raw)
            stream.flush()
            os.fsync(fd)
        parent = os.open(Path(path).parent, os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC)
        try:
            os.fsync(parent)
        finally:
            os.close(parent)
    finally:
        os.close(fd)


@contextmanager
def hold(root):
    owned_directory(root)
    path = Path(root) / LOCK
    fd = os.open(path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
    try:
        require(private_info(os.fstat(fd)), 'sequence_private_lock_required')
        observed = path.lstat()
        require((observed.st_dev, observed.st_ino) == (os.fstat(fd).st_dev, os.fstat(fd).st_ino),
                'sequence_lock_replaced')
        os.set_inheritable(fd, False)
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise ValueError('live_sequence_busy') from None
        try:
            yield
        finally:
            fcntl.flock(fd, fcntl.LOCK_UN)
    finally:
        os.close(fd)  # Never unlink: crash/retry must share this same lock inode.


def binding(root, setup, direction):
    require(direction in DIRECTIONS, 'unknown_live_direction')
    for name in ('identity_sha256', 'package_sha256'):
        value = setup.get(name)
        require(isinstance(value, str) and len(value) == 64
                and all(c in '0123456789abcdef' for c in value), 'sequence_setup_binding_required')
    return dict(schema_version=1, direction=direction, setup_sha256=sha(Path(root)/'setup.json'),
                identity_sha256=setup['identity_sha256'], package_sha256=setup['package_sha256'],
                scenario_result=str(Path(root)/direction/'live-run/scenario-result.json'))


def validate_result(result):
    require(isinstance(result, dict) and result.get('status') == 'SCENARIO_PASS', 'first_scenario_not_passed')
    require(type(result.get('exit_code')) is int and result['exit_code'] == 0
            and result.get('timed_out') is False and result.get('output_channels_closed') is True,
            'first_scenario_not_closed')
    for name in ('remaining_owned_pids', 'unverified_owned_pids', 'discovery_errors'):
        require(result.get(name) == [] and isinstance(result.get(name), list), 'first_scenario_not_closed')
    post = result.get('postflight')
    require(isinstance(post, dict) and post.get('status') == 'POSTFLIGHT_PASS', 'first_postflight_not_passed')
    require(post.get('parent_accept') is True and post.get('transport_ack') is True
            and post.get('integrated') is False, 'first_parent_closure_missing')
    for name in ('active', 'unknown', 'reserved_ms', 'actionable_pending'):
        require(type(post.get(name)) is int and post[name] == 0, 'first_ledger_not_converged')
    require(type(post.get('used_ms')) is int and 0 <= post['used_ms'] <= 90000
            and type(post.get('attempts')) is int and 1 <= post['attempts'] <= 2,
            'first_budget_not_converged')
    for name in ('cli_handle', 'run_id', 'task_id', 'candidate_oid', 'tree_oid'):
        require(isinstance(post.get(name), str) and bool(post[name].strip()), 'first_original_identity_missing')
    value = post.get('validation_evidence_sha256')
    require(isinstance(value, str) and len(value) == 64
            and all(c in '0123456789abcdef' for c in value), 'first_validation_hash_missing')
    return post


def before_start(root, setup, direction):
    """Structural gate only; no credentials, provider/helper or ledger subprocesses."""
    root = Path(root)
    expected = binding(root, setup, direction)
    for name in DIRECTIONS:
        owned_directory(root/name)
    area = root/direction
    require(not any(exists(area/name) for name in (STARTED, COMPLETED, 'live-run')),
            'never_reset_consumed_direction')
    if direction == DIRECTIONS[0]:
        other = root/DIRECTIONS[1]
        require(not any(exists(other/name) for name in (STARTED, COMPLETED, 'live-run')),
                'first_direction_requires_prepared')
        return expected, None
    first = root/DIRECTIONS[0]
    require(all(exists(first/name) for name in (STARTED, COMPLETED, 'live-run')),
            'first_direction_completion_required')
    first_binding = binding(root, setup, DIRECTIONS[0])
    started = read_json(first/STARTED)
    completed = read_json(first/COMPLETED)
    require(started == first_binding, 'first_started_binding_changed')
    require(all(completed.get(key) == value for key, value in first_binding.items())
            and completed.get('started_sha256') == sha(first/STARTED)
            and completed.get('scenario_exit_code') == 0
            and type(completed.get('scenario_exit_code')) is int,
            'first_completed_binding_changed')
    outcome = Path(first_binding['scenario_result'])
    owned_directory(outcome.parent)
    require(completed.get('scenario_result_sha256') == sha(outcome), 'first_result_binding_changed')
    result = read_json(outcome)
    validate_result(result)
    return expected, result


def start(root, direction, expected):
    exclusive_json(Path(root)/direction/STARTED, expected)


def complete(root, direction, expected, exit_code):
    area = Path(root)/direction
    outcome = Path(expected['scenario_result'])
    value = dict(expected, started_sha256=sha(area/STARTED), scenario_exit_code=exit_code,
                 scenario_result_sha256=sha(outcome) if exists(outcome) else None)
    exclusive_json(area/COMPLETED, value)


def fresh_first(root, previous, env, python, scenario, execute):
    post = validate_result(previous)
    first = Path(root)/DIRECTIONS[0]
    contract_path = first/'bound-contract.json'
    contract = read_json(contract_path)
    scratch = Path(contract['scratch_dir'])
    owned_directory(scratch)
    candidates = list(scratch.glob('validation*.json'))
    require(1 <= len(candidates) <= 4, 'first_validation_files_missing_or_excess')
    matching = [path for path in candidates if sha(path) == post['validation_evidence_sha256']]
    require(len(matching) == 1, 'first_unique_validation_hash_required')
    args = [python, '-B', scenario, 'finish', '--contract', contract_path,
            '--validation-evidence', matching[0], '--validation-sha256', post['validation_evidence_sha256']]
    refreshed = json.loads(execute(args, env))
    require(refreshed == post, 'first_fresh_postflight_changed')
    return refreshed
