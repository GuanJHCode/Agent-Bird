#!/usr/bin/env python3
"""Task-private resource acceptance; never builds or invokes a real provider."""
from __future__ import annotations
import argparse
import datetime
import hashlib
import json
import os
import pathlib
import signal
import sqlite3
import statistics
import subprocess
import sys
import time
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[1]
THRESHOLDS = json.loads((ROOT / 'data/thresholds.json').read_text())
TASKS = ('worker-a', 'worker-b')

class Failure(RuntimeError):
    pass

def sync_dir(path):
    fd = os.open(path, os.O_RDONLY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)

def atomic(path, data):
    raw = data if isinstance(data, bytes) else (json.dumps(data, sort_keys=True, indent=2) + '\n').encode()
    tmp = path.with_name(path.name + '.next')
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'wb') as out:
        out.write(raw)
        out.flush()
        os.fsync(out.fileno())
    os.replace(tmp, path)
    sync_dir(path.parent)

def sha(path):
    with path.open('rb') as f:
        return hashlib.file_digest(f, 'sha256').hexdigest()

def pin(path):
    st = path.stat(follow_symlinks=False)
    return {'dev': st.st_dev, 'ino': st.st_ino, 'size': st.st_size, 'mode': st.st_mode & 0o777, 'sha256': sha(path)}

def copy_pinned(source, target, expected):
    fd = os.open(source, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        before = os.fstat(fd)
        digest = hashlib.sha256()
        with os.fdopen(os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o500), 'wb') as out:
            while block := os.read(fd, 1024 * 1024):
                digest.update(block)
                out.write(block)
            out.flush()
            os.fsync(out.fileno())
        after = os.fstat(fd)
        if (before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns) != (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns) or digest.hexdigest() != expected:
            raise Failure('source changed or did not match frozen digest')
    finally:
        os.close(fd)
    sync_dir(target.parent)
    return pin(target)

def birth(pid):
    return subprocess.run(['/bin/ps', '-o', 'lstart=', '-p', str(pid)], capture_output=True, text=True, check=False).stdout.strip()

def process_table():
    rows = {}
    result = subprocess.run(['/bin/ps', '-axo', 'pid=,ppid=,rss=,time=,stat=,command='], capture_output=True, text=True, check=True)
    for line in result.stdout.splitlines():
        f = line.strip().split(None, 5)
        if len(f) == 6 and all(x.isdigit() for x in f[:3]):
            rows[int(f[0])] = dict(pid=int(f[0]), ppid=int(f[1]), rss_kib=int(f[2]), cpu=f[3], stat=f[4], argv=f[5])
    return rows

def cpu_seconds(value):
    parts = value.split(':')
    return sum(float(v) * 60 ** i for i, v in enumerate(reversed(parts)))

def load(path):
    return json.loads(path.read_text())

def call(ctx, *args, timeout=20):
    # Every short-lived control child has this subprocess.run's original Wait.
    proc = subprocess.run([str(ctx['candidate']), *args], env=ctx['env'], stdin=subprocess.DEVNULL, capture_output=True, timeout=timeout)
    number = ctx['calls']
    ctx['calls'] += 1
    atomic(ctx['raw'] / f'{number:04d}-{args[0]}.stdout', proc.stdout)
    atomic(ctx['raw'] / f'{number:04d}-{args[0]}.stderr', proc.stderr)
    atomic(ctx['raw'] / f'{number:04d}-{args[0]}.wait.json', {'argv': list(args), 'original_wait': proc.returncode})
    if proc.returncode:
        raise Failure(f'{args[0]} exited {proc.returncode}: {proc.stderr[-500:]!r}')
    return json.loads(proc.stdout)

def connect(ctx):
    db = sqlite3.connect(ctx['state'].joinpath('state.db').as_uri() + '?mode=ro', uri=True, timeout=2, isolation_level=None)
    db.row_factory = sqlite3.Row
    db.execute('PRAGMA query_only=ON')
    return db

def task_states(db):
    result = {}
    for task in TASKS:
        row = db.execute("""SELECT t.status AS task_status,s.status AS segment_status,s.segment_id,
          (SELECT body_json FROM runtime_events e WHERE e.segment_id=s.segment_id AND json_extract(e.body_json,'$.kind')='running' ORDER BY e.sequence DESC LIMIT 1) AS running,
          (SELECT body_json FROM runtime_events e WHERE e.segment_id=s.segment_id AND json_extract(e.body_json,'$.kind')='exited' ORDER BY e.sequence DESC LIMIT 1) AS exited
          FROM tasks t JOIN attempts a ON a.task_id=t.id JOIN segment_runtime s ON s.attempt_id=a.id
          WHERE t.id=? ORDER BY a.attempt_no DESC,s.created_at DESC LIMIT 1""", (task,)).fetchone()
        if row:
            result[task] = dict(row)
    return result

def remember(ctx, pid, expected_birth, role, rows):
    row = rows.get(pid)
    if not row or row['stat'].startswith('Z') or birth(pid) != expected_birth:
        raise Failure(f'{role} identity not live: {pid}')
    key = str(pid)
    identity = {'pid': pid, 'birth': expected_birth, 'argv': row['argv'], 'role': role}
    if key in ctx['identities'] and ctx['identities'][key] != identity:
        raise Failure(f'identity drift: {pid}')
    ctx['identities'][key] = identity
    return identity

def identity_alive(identity, rows):
    row = rows.get(identity['pid'])
    return bool(row and not row['stat'].startswith('Z') and row['argv'] == identity['argv'] and birth(identity['pid']) == identity['birth'])

def establish(ctx, db, stats_paths):
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        states = task_states(db)
        if len(states) == 2 and all(v['segment_status'] == 'running' and v['running'] for v in states.values()) and all(p.exists() for p in stats_paths):
            break
        if ctx['server'].poll() is not None:
            raise Failure('coordinator exited during admission')
        time.sleep(.05)
    else:
        raise Failure('both worker processes did not reach running')
    rows = process_table()
    for task, path in zip(TASKS, stats_paths):
        actual = json.loads(states[task]['running'])['process']
        stats = load(path)
        if stats['pid'] != actual['pid'] or stats['status'] != 'waiting':
            raise Failure('worker marker differs from signed runtime identity')
        identity = remember(ctx, actual['pid'], actual['birth'], task, rows)
        if not identity['argv'].startswith(str(ctx['worker']) + ' '):
            raise Failure('worker executable mismatch')
        ctx['workers'][task] = identity
    owners = db.execute('SELECT owner_pid,owner_birth,current_host_pid,current_host_birth FROM execution_owners WHERE is_current=1 AND phase=\'bound\'').fetchall()
    if len(owners) != 1:
        raise Failure('resource run did not use one supervised Owner')
    for owner in owners:
        for prefix, role in [('owner', 'owner'), ('current_host', 'host')]:
            remember(ctx, owner[prefix + '_pid'], owner[prefix + '_birth'], role, rows)
    atomic(ctx['out'] / 'identities.json', ctx['identities'])

def product_sample(ctx, stats, rows):
    candidate = str(ctx['candidate'])
    workers = {v['pid'] for v in ctx['workers'].values()}
    products = {pid for pid, row in rows.items() if row['argv'] == candidate or row['argv'].startswith(candidate + ' ')}
    for _ in range(len(rows)):
        more = {pid for pid, row in rows.items() if row['ppid'] in products and pid not in workers}
        if more <= products:
            break
        products |= more
    selected = [rows[pid] for pid in sorted(products - workers)]
    # Include a conservative simultaneous maximum for both report helpers,
    # even if a helper was already visible in this sample (double counting).
    helper_rss = sum(s['helper_max_rss_bytes'] for s in stats)
    return {'unix_ns': time.time_ns(), 'product': selected, 'worker_pids': sorted(workers),
            'observed_product_rss_kib': sum(p['rss_kib'] for p in selected),
            'helper_rss_allowance_bytes': helper_rss,
            'charged_product_rss_kib': sum(p['rss_kib'] for p in selected) + helper_rss / 1024,
            'helper_cpu_ns': sum(s['helper_cpu_ns'] for s in stats)}

def validate_running(ctx, states, rows):
    if ctx['server'].poll() is not None:
        raise Failure('original Coordinator exited early')
    for task in TASKS:
        if states.get(task, {}).get('segment_status') != 'running' or not identity_alive(ctx['workers'][task], rows):
            raise Failure(f'{task} stopped or changed identity before deadline')
    for identity in ctx['identities'].values():
        if identity['role'] in ('owner', 'host', 'coordinator') and not identity_alive(identity, rows):
            raise Failure(f'{identity["role"]} exited early')

def identity_reaped(identity, rows):
    observed = subprocess.run(['/bin/ps', '-o', 'lstart=', '-p', str(identity['pid'])], capture_output=True, text=True, timeout=2)
    if observed.returncode == 1 and not observed.stdout.strip() and not observed.stderr.strip():
        return True
    if observed.returncode or not observed.stdout.strip() or len(observed.stdout.strip().splitlines()) != 1:
        raise Failure('process death observation UNKNOWN')
    if observed.stdout.strip() != identity['birth']:
        return True
    row = rows.get(identity['pid'])
    if row is None or row['argv'] != identity['argv']:
        raise Failure('process identity observation disagrees')
    return False  # Same-identity zombie is not reaped.


def pending_tail_fact(ctx, db, task, stats, rows, worker_duration, report_every):
    entries = db.execute("""SELECT a.id AS attempt_id,s.segment_id,s.status,t.status AS task_status
        FROM tasks t JOIN attempts a ON a.task_id=t.id JOIN segment_runtime s ON s.attempt_id=a.id WHERE t.id=?""", (task,)).fetchall()
    if len(entries) != 1:
        raise Failure('resource tail changed attempt/segment count')
    entry = dict(entries[0])
    if entry['status'] != 'exited' or entry['task_status'] != 'result_ready' or stats['status'] != 'completed':
        return None
    if sum(phase['seconds'] for phase in stats['phases']) != worker_duration or stats['reports'] != worker_duration // report_every:
        raise Failure('worker did not complete frozen duration and report count')
    events = db.execute("""SELECT event_id,producer_id,body_json,delivery_status,created_at
        FROM runtime_events WHERE task_id=? AND attempt_id=? AND segment_id=?
        AND json_extract(body_json,'$.kind') IN ('running','result','exited') ORDER BY sequence""",
        (task, entry['attempt_id'], entry['segment_id'])).fetchall()
    facts = {}
    for event in events:
        value = dict(event)
        body = json.loads(value['body_json'])
        if body['kind'] in facts:
            raise Failure('duplicate tail lifecycle event')
        facts[body['kind']] = value
    if set(facts) != {'running', 'result', 'exited'}:
        return None
    started = json.loads(facts['running']['body_json'])
    exited = json.loads(facts['exited']['body_json'])
    identity = ctx['workers'][task]
    if len({e['producer_id'] for e in facts.values()}) != 1 or exited.get('exit_code') != 0 or exited.get('process') != started.get('process') or exited['process']['pid'] != identity['pid'] or exited['process']['birth'] != identity['birth'] or facts['result']['delivery_status'] != 'pending':
        raise Failure('tail lacks exact pending result and original worker Exited0')
    if not identity_reaped(identity, rows):
        return None
    return {'segment': entry, 'events': facts, 'worker_stats': stats}


def validate_pending_tail(ctx, db, stats, rows, worker_deadline_ns, worker_duration, report_every):
    if ctx['server'].poll() is not None:
        raise Failure('original Coordinator exited during tail')
    baseline = ctx.setdefault('tail_baseline', {})
    for task, worker_stats in zip(TASKS, stats):
        fact = pending_tail_fact(ctx, db, task, worker_stats, rows, worker_duration, report_every)
        if fact is None:
            if time.time_ns() > worker_deadline_ns + THRESHOLDS['worker_completion_grace_seconds'] * 1_000_000_000 or worker_stats['status'] not in ('running', 'completed'):
                raise Failure('worker completion exceeded bounded tail transition')
            continue
        if task in baseline and baseline[task] != fact:
            raise Failure('exact pending tail event or segment changed')
        baseline[task] = fact
    completed_at = None
    if len(baseline) == len(TASKS):
        completed_at = max(datetime.datetime.fromisoformat(f['events']['exited']['created_at']).timestamp() for f in baseline.values())
    for identity in ctx['identities'].values():
        if identity['role'] == 'coordinator':
            if not identity_alive(identity, rows):
                raise Failure('Coordinator identity changed during tail')
        elif identity['role'] in ('owner', 'host'):
            gone = identity_reaped(identity, rows)
            # Only a complete exact durable proof permits actor retirement.
            if completed_at is None and gone:
                raise Failure('Owner/Host retired before complete durable worker proof')
            if completed_at is not None and not gone and time.time() > completed_at + THRESHOLDS['owner_retirement_seconds']:
                raise Failure('Owner/Host did not naturally retire after completion')


def measure(ctx, db, stats_paths, start_ns, duration, worker_duration, report_every, smoke):
    peak = 0
    cpu_first, cpu_last = {}, {}
    partial = ctx['out'] / 'samples.jsonl.partial'
    with os.fdopen(os.open(partial, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), 'w') as out:
        for tick in range(duration):
            delay = (start_ns + tick * 1_000_000_000 - time.time_ns()) / 1e9
            if delay > 0:
                time.sleep(delay)
            rows = process_table()
            stats = [load(p) for p in stats_paths]
            worker_deadline_ns = start_ns + worker_duration * 1_000_000_000
            if time.time_ns() < worker_deadline_ns:
                validate_running(ctx, task_states(db), rows)
                if any(s['status'] not in ('waiting', 'running') for s in stats):
                    raise Failure('worker fixture failed early')
            else:
                validate_pending_tail(ctx, db, stats, rows, worker_deadline_ns, worker_duration, report_every)
            sample = product_sample(ctx, stats, rows)
            sample['tick'] = tick
            peak = max(peak, sample['charged_product_rss_kib'])
            if peak > THRESHOLDS['product_rss_limit_mib'] * 1024:
                raise Failure('charged product RSS exceeded 128 MiB')
            for p in sample['product']:
                key = (p['pid'], p['argv'])
                value = cpu_seconds(p['cpu'])
                cpu_first.setdefault(key, value)
                cpu_last[key] = max(cpu_last.get(key, 0), value)
            out.write(json.dumps(sample, separators=(',', ':')) + '\n')
            out.flush()
            if tick % 5 == 4:
                os.fsync(out.fileno())
        os.fsync(out.fileno())
    remaining = (start_ns + duration * 1_000_000_000 - time.time_ns()) / 1e9
    if remaining > 0:
        time.sleep(remaining)
    validate_pending_tail(ctx, db, [load(p) for p in stats_paths], process_table(), start_ns + worker_duration * 1_000_000_000, worker_duration, report_every)
    if len(ctx.get('tail_baseline', {})) != len(TASKS):
        raise Failure('tail completion proof missing at measurement deadline')
    atomic(ctx['out'] / 'pending-tail.json', ctx['tail_baseline'])
    os.replace(partial, ctx['out'] / 'samples.jsonl')
    sync_dir(ctx['out'])
    return {'samples': duration,
            'sampled_cpu_seconds': sum(cpu_last[k] - cpu_first[k] for k in cpu_last)}

def finalize_rss(sample_path, stats, smoke):
    # A helper completing after the final sample can still establish a larger
    # maximum. Charge the final bound to EVERY sample, including early peaks.
    allowance = sum(s['helper_max_rss_bytes'] for s in stats) / 1024
    windows = {}
    peak = 0
    count = 0
    with sample_path.open() as samples:
        for line in samples:
            sample = json.loads(line)
            charged = sample['observed_product_rss_kib'] + allowance
            peak = max(peak, charged)
            windows.setdefault(sample['tick'] // 300, []).append(charged)
            count += 1
    medians = {k: statistics.median(v) for k, v in windows.items()}
    if not count or peak > THRESHOLDS['product_rss_limit_mib'] * 1024:
        raise Failure('final helper upper bound exceeded 128 MiB or samples absent')
    if not smoke:
        if count != THRESHOLDS['total_duration_seconds']:
            raise Failure('incomplete formal sample count')
        for phase in range(3):
            ids = [phase * 4 + i for i in (1, 2, 3)]
            if any(len(windows.get(i, [])) < 270 for i in ids):
                raise Failure('insufficient RSS window evidence')
            values = [medians[i] for i in ids]
            if max(values) - min(values) > THRESHOLDS['rss_window_range_limit_mib'] * 1024:
                raise Failure('RSS growth exceeded frozen window tolerance')
    return {'peak_charged_rss_mib': peak / 1024,
            'final_helper_allowance_bytes': int(allowance * 1024),
            'five_minute_median_kib': medians,
            'growth_check': 'not assessed in smoke' if smoke else 'passed'}

def finish_tasks(ctx, db, timeout=20):
    until = time.monotonic() + timeout
    while time.monotonic() < until:
        states = task_states(db)
        if len(states) == 2 and all(s['segment_status'] == 'exited' and s['exited'] for s in states.values()):
            break
        time.sleep(.1)
    else:
        raise Failure('worker original Exited receipts did not arrive')
    receipts = {}
    for task in TASKS:
        event = json.loads(states[task]['exited'])
        identity = ctx['workers'][task]
        if event.get('exit_code') != 0 or event['process']['pid'] != identity['pid'] or event['process']['birth'] != identity['birth'] or identity_alive(identity, process_table()):
            raise Failure('worker original Wait/exit was not clean and reaped')
        receipts[task] = event
        collected = call(ctx, 'collect', '--state-dir', str(ctx['state']), '--task-id', task, '--control-file', ctx['control'])
        results = [e for e in collected['events'] if e['kind'] == 'result']
        if len(results) != 1:
            raise Failure('one actionable result required for natural retirement')
        e = results[0]
        call(ctx, 'accept', '--state-dir', str(ctx['state']), '--task-id', task, '--control-file', ctx['control'], '--work-revision', '1', '--event-id', e['event_id'], '--event-revision', str(e['event_revision']), '--event-hash', e['payload_hash'], '--action-slot', e['action_slot'], '--decision', 'accept', '--command-id', 'resource-accept-' + task)
    atomic(ctx['out'] / 'worker-exited.json', receipts)
    end = time.monotonic() + 13
    while time.monotonic() < end:
        rows = process_table()
        if not any(identity_alive(i, rows) for i in ctx['identities'].values() if i['role'] in ('owner', 'host')):
            return
        time.sleep(.1)
    raise Failure('Owner/Host did not naturally retire after accept')

def cleanup(ctx, failed):
    result = {'worker_stop': [], 'identity_signals': []}
    if failed and ctx.get('control') and ctx.get('server') and ctx['server'].poll() is None:
        for task in TASKS:
            try:
                result['worker_stop'].append(call(ctx, 'stop', '--state-dir', str(ctx['state']), '--task-id', task, '--control-file', ctx['control'], '--work-revision', '1'))
            except Exception as e:
                result['worker_stop'].append({'error': repr(e)})
        if ctx.get('workers'):
            end = time.monotonic() + 13
            while time.monotonic() < end:
                if not any(identity_alive(i, process_table()) for i in ctx['workers'].values()):
                    break
                time.sleep(.1)
    server = ctx.get('server')
    if server is not None:
        if server.poll() is None:
            server.terminate()  # original unreaped child handle, never a pid-file reconstruction
        try:
            result['coordinator_original_wait'] = server.wait(timeout=15)
        except subprocess.TimeoutExpired:
            server.kill()
            result['coordinator_original_wait'] = server.wait(timeout=5)
            result['forced_coordinator_kill'] = True
    rows = process_table()
    # Only identities enrolled while live in this run may receive cleanup signals.
    for identity in ctx['identities'].values():
        if identity['role'] != 'coordinator' and identity_alive(identity, rows):
            os.kill(identity['pid'], signal.SIGTERM)
            result['identity_signals'].append(identity)
    end = time.monotonic() + 13
    while time.monotonic() < end:
        rows = process_table()
        if not any(identity_alive(i, rows) for i in ctx['identities'].values()):
            break
        time.sleep(.1)
    result['remaining'] = [i for i in ctx['identities'].values() if identity_alive(i, rows)]
    known = {i['pid'] for i in ctx['identities'].values()}
    result['unregistered_private_processes'] = [r for pid, r in rows.items() if pid not in known and any(r['argv'] == str(exe) or r['argv'].startswith(str(exe) + ' ') for exe in (ctx['candidate'], ctx['worker']))]
    return result

def verify_emissions(ctx, stats_paths, receipt_paths, worker_duration, phase_seconds, rate, giant_every, report_every):
    all_stats = []
    for stats_path, receipt_path in zip(stats_paths, receipt_paths):
        stats = load(stats_path)
        if stats['status'] != 'completed':
            raise Failure('worker did not finish full logical duration')
        receipts = [json.loads(line) for line in receipt_path.read_text().splitlines()]
        if len(receipts) != worker_duration // report_every or stats['reports'] != len(receipts):
            raise Failure('missing independent durable report ACKs')
        for phase, p in enumerate(stats['phases']):
            count = min(phase_seconds, max(0, worker_duration - phase * phase_seconds)) if phase < 2 else max(0, worker_duration - 2 * phase_seconds)
            giants = sum((i + 1) % giant_every == 0 for i in range(phase * phase_seconds, phase * phase_seconds + count)) if phase < 2 else 0
            expected = count * rate + giants * THRESHOLDS['giant_line_bytes'] if phase < 2 else 0
            if p['seconds'] != count or p['giants'] != giants or p['stdout_bytes'] + p['stderr_bytes'] != expected:
                raise Failure('emitted phase bytes/giant lines/duration mismatch')
        for n, receipt in enumerate(receipts, 1):
            if receipt['original_wait'] != 0 or receipt['ack']['status'] != 'durable' or receipt['ack']['acked_through'] != n:
                raise Failure('invalid report helper original Wait/durable receipt')
        all_stats.append(stats)
    atomic(ctx['out'] / 'emissions.json', all_stats)
    return all_stats

def main():
    ap = argparse.ArgumentParser()
    for name in ('candidate', 'worker', 'out'):
        ap.add_argument('--' + name, type=pathlib.Path, required=True)
    ap.add_argument('--freeze-manifest', type=pathlib.Path)
    ap.add_argument('--smoke', action='store_true')
    ap.add_argument('--offline', action='store_true')
    args = ap.parse_args()
    if sys.platform != 'darwin' or args.offline:
        raise SystemExit('macOS dual-worker mode only; offline requires a separate phase')
    if args.out.exists() or not args.out.is_absolute():
        raise SystemExit('new absolute output directory required')
    frozen = load(args.freeze_manifest) if args.freeze_manifest else None
    expected = {'candidate_sha256': sha(args.candidate), 'worker_sha256': sha(args.worker), 'runner_sha256': sha(pathlib.Path(__file__)), 'thresholds': THRESHOLDS}
    if not args.smoke and (frozen is None or any(frozen.get(k) != v for k, v in expected.items())):
        raise SystemExit('formal run requires exact candidate/worker/runner/threshold freeze')
    args.out.mkdir(mode=0o700, parents=True)
    bindir = args.out / 'bin'
    bindir.mkdir(mode=0o700)
    candidate, worker = bindir / 'candidate', bindir / 'resource-soak-worker-fixture'
    pins = {'candidate': copy_pinned(args.candidate, candidate, expected['candidate_sha256']), 'worker': copy_pinned(args.worker, worker, expected['worker_sha256'])}
    raw = args.out / 'raw'
    raw.mkdir(mode=0o700)
    tmp = pathlib.Path('/private/tmp') / ('resource-soak-' + uuid.uuid4().hex[:12])
    tmp.mkdir(mode=0o700)
    duration, worker_duration, phase_seconds, rate, giant_every, report_every = (20, 16, 6, 4096, 3, 2) if args.smoke else (3600, THRESHOLDS['worker_duration_seconds'], 1200, 1048576, 30, 10)
    ctx = {'out': args.out, 'raw': raw, 'candidate': candidate, 'worker': worker, 'state': tmp / 'state', 'calls': 0, 'workers': {}, 'identities': {}, 'env': {'PATH': '/usr/bin:/bin:/usr/sbin:/sbin', 'LANG': 'C', 'LC_ALL': 'C', 'TMPDIR': str(tmp), 'ORCHESTRATOR_ENABLE_TEST_FAKE': '1'}}
    summary = {'mode': 'smoke' if args.smoke else 'formal', 'result': 'failed', 'frozen': expected, 'pins_start': pins, 'state_root': str(tmp), 'limitations': ['fake adapter drain, no real Provider parser/model', 'Owner/Host retirement observed by exact identity; original handle evidence reused from native lifecycle acceptance', 'measurement ps/SQLite/Python scaffolding excluded; report helpers conservatively charged'], 'started_unix_ns': time.time_ns()}
    db = None
    try:
        server_out = (raw / 'server.stdout').open('xb')
        server_err = (raw / 'server.stderr').open('xb')
        ctx['server'] = subprocess.Popen([str(candidate), 'serve', '--state-dir', str(ctx['state'])], stdin=subprocess.DEVNULL, stdout=server_out, stderr=server_err, env=ctx['env'])
        pid = ctx['server'].pid
        remember(ctx, pid, birth(pid), 'coordinator', process_table())
        deadline = time.monotonic() + 15
        while not (ctx['state'] / 'coordinator.sock').exists():
            if ctx['server'].poll() is not None or time.monotonic() > deadline:
                raise Failure('owned Coordinator startup failed')
            time.sleep(.05)
        owner_request = tmp / 'owner.json'
        atomic(owner_request, {'controller_thread': 'resource-soak', 'origin_pid': os.getpid(), 'origin_birth': birth(os.getpid())})
        owner = call(ctx, 'owner-bind', '--state-dir', str(ctx['state']), '--request', str(owner_request))
        stats_paths = [tmp / (task + '.stats.json') for task in TASKS]
        receipt_paths = [tmp / (task + '.receipts.jsonl') for task in TASKS]
        start_path = tmp / 'shared-start.json'
        tasks = []
        for task, stats_path, receipt_path in zip(TASKS, stats_paths, receipt_paths):
            argv = [str(worker), '--id', task, '--seconds', str(worker_duration), '--phase-seconds', str(phase_seconds), '--bytes-per-second', str(rate), '--giant-line-bytes', str(THRESHOLDS['giant_line_bytes']), '--giant-line-every', str(giant_every), '--critical-every', str(report_every), '--stats-file', str(stats_path), '--start-file', str(start_path), '--receipt-file', str(receipt_path)]
            tasks.append({'id': task, 'max_attempts': 1, 'max_active_ms': (worker_duration + 60) * 1000, 'completion_policy': 'owner_review', 'adapter': {'kind': 'fake', 'args': argv, 'directory': str(tmp)}})
        request = {k: v for k, v in owner.items() if k not in ('version', 'owner_capability')}
        request.update(run_id='resource-' + uuid.uuid4().hex, plan_revision=1, owner_mode='native', delivery_mode='collect', tasks=tasks)
        request_path = tmp / 'submit.json'
        atomic(request_path, request)
        submitted = call(ctx, 'submit', '--state-dir', str(ctx['state']), '--request', str(request_path))
        ctx['control'] = submitted['control_file']
        db = connect(ctx)
        establish(ctx, db, stats_paths)
        start_ns = time.time_ns() + 500_000_000
        atomic(start_path, {'unix_ns': start_ns})
        summary.update(measure(ctx, db, stats_paths, start_ns, duration, worker_duration, report_every, args.smoke))
        finish_tasks(ctx, db)
        stats = verify_emissions(ctx, stats_paths, receipt_paths, worker_duration, phase_seconds, rate, giant_every, report_every)
        summary.update(finalize_rss(ctx['out'] / 'samples.jsonl', stats, args.smoke))
        summary['helper_cpu_seconds'] = sum(s['helper_cpu_ns'] for s in stats) / 1e9
        summary['result'] = 'passed'
    except BaseException as e:
        summary['error'] = repr(e)
    finally:
        if db is not None:
            db.close()
        summary['cleanup'] = cleanup(ctx, summary['result'] != 'passed')
        if summary['cleanup']['remaining'] or summary['cleanup']['unregistered_private_processes'] or summary['cleanup'].get('forced_coordinator_kill') or summary['cleanup'].get('coordinator_original_wait') != 0 or summary['cleanup']['identity_signals']:
            summary['result'] = 'failed'
        summary['pins_end'] = {'candidate': pin(candidate), 'worker': pin(worker)}
        if summary['pins_end'] != pins:
            summary['result'] = 'failed'
            summary['pin_error'] = 'private executable changed'
        summary['finished_unix_ns'] = time.time_ns()
        atomic(args.out / 'summary.json', summary)
        manifest = {}
        for path in sorted(args.out.rglob('*')):
            if path.is_file() and path.parent != bindir:
                manifest[str(path.relative_to(args.out))] = {'bytes': path.stat().st_size, 'sha256': sha(path)}
        for path in sorted(tmp.glob('*.json*')):
            manifest[str(path)] = {'bytes': path.stat().st_size, 'sha256': sha(path)}
        atomic(args.out / 'evidence-manifest.json', manifest)
    print(json.dumps({'result': summary['result'], 'mode': summary['mode'], 'error': summary.get('error'), 'peak_mib': summary.get('peak_charged_rss_mib'), 'output': str(args.out)}, sort_keys=True))
    return 0 if summary['result'] == 'passed' else 1

if __name__ == '__main__':
    raise SystemExit(main())
