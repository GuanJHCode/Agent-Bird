#!/usr/bin/env python3
"""Same-candidate offline pending delivery acceptance; no real provider/account."""
from __future__ import annotations
import argparse
import hashlib
import importlib.util
import json
import os
import pathlib
import select
import signal
import sqlite3
import stat
import subprocess
import sys
import time
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[1]
CONTROLLER = ROOT / 'scripts/controller.py'
SOAK_SOURCE = ROOT.parent / 'resource-soak/scripts/resource_soak.py'
SOAK_THRESHOLDS = ROOT.parent / 'resource-soak/data/thresholds.json'
THRESHOLDS = json.loads((ROOT / 'data/thresholds.json').read_text())


class Failure(RuntimeError):
    pass


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def private_copy(source, target, expected, mode=0o500):
    fd = os.open(source, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        before = os.fstat(fd)
        if not stat.S_ISREG(before.st_mode):
            raise Failure('frozen source is not regular')
        digest = hashlib.sha256()
        with os.fdopen(os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, mode), 'wb') as out:
            while block := os.read(fd, 1024 * 1024):
                digest.update(block)
                out.write(block)
            out.flush()
            os.fsync(out.fileno())
        after = os.fstat(fd)
        fields = ('st_dev', 'st_ino', 'st_size', 'st_mtime_ns', 'st_ctime_ns')
        if any(getattr(before, f) != getattr(after, f) for f in fields) or digest.hexdigest() != expected:
            raise Failure('source changed or did not match frozen bytes')
    finally:
        os.close(fd)
    directory = os.open(target.parent, os.O_RDONLY)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


def private_output(path):
    return os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), 'wb')


def process_table():
    result = subprocess.run(['/bin/ps', '-axo', 'pid=,ppid=,rss=,time=,stat=,command='],
                            capture_output=True, text=True, check=True, timeout=2)
    rows = {}
    for line in result.stdout.splitlines():
        fields = line.strip().split(None, 5)
        if len(fields) == 6 and all(value.isdigit() for value in fields[:3]):
            rows[int(fields[0])] = dict(pid=int(fields[0]), ppid=int(fields[1]), rss_kib=int(fields[2]),
                                       cpu=fields[3], stat=fields[4], argv=fields[5])
    if not rows:
        raise Failure('empty process table')
    return rows


def observed_birth(pid):
    p = subprocess.run(['/bin/ps', '-o', 'lstart=', '-p', str(pid)], capture_output=True, text=True, timeout=2)
    if p.returncode == 1 and not p.stdout.strip() and not p.stderr.strip():
        return None
    if p.returncode or not p.stdout.strip() or len(p.stdout.strip().splitlines()) != 1:
        raise Failure('process birth UNKNOWN')
    return p.stdout.strip()


def identity_present(identity, rows, require_argv=True):
    current = observed_birth(identity['pid'])
    if current is None or current != identity['birth']:
        return False
    row = rows.get(identity['pid'])
    if row is None:
        raise Failure('process table/birth observation disagree')
    if require_argv and row['argv'] != identity['argv']:
        raise Failure('owned process argv drift')
    return True  # A same-identity zombie has not yet been reaped.


def remember(ctx, pid, expected, role):
    rows = process_table()
    row = rows.get(pid)
    if not expected or not row or row['stat'].startswith('Z') or observed_birth(pid) != expected:
        raise Failure(role + ' identity not live')
    identity = {'pid': pid, 'birth': expected, 'argv': row['argv'], 'role': role}
    ctx['identities'][str(pid)] = identity
    return identity


def pending_facts(db, run_id, task_id):
    rows = db.execute('''SELECT t.status AS task_status,a.id AS attempt_id,a.attempt_no,s.segment_id,
        s.status AS segment_status,s.host_id,tr.host_launch_id
        FROM tasks t JOIN task_runtime tr ON tr.task_id=t.id JOIN attempts a ON a.task_id=t.id
        JOIN segment_runtime s ON s.attempt_id=a.id WHERE t.run_id=? AND t.id=?''', (run_id, task_id)).fetchall()
    if len(rows) != 1 or rows[0]['attempt_no'] != 1:
        return None
    row = dict(rows[0])
    if row['task_status'] != 'result_ready' or row['segment_status'] != 'exited':
        return None
    events = db.execute('''SELECT event_id,body_json,delivery_status FROM runtime_events
        WHERE task_id=? AND attempt_id=? AND segment_id=? AND producer_id=?
        AND json_extract(body_json,'$.kind') IN ('result','running','exited') ORDER BY sequence''',
        (task_id, row['attempt_id'], row['segment_id'], row['host_id'])).fetchall()
    by_kind = {}
    for event in events:
        body = json.loads(event['body_json'])
        kind = body['kind']
        if kind in by_kind:
            raise Failure('duplicate lifecycle event')
        by_kind[kind] = {'event_id': event['event_id'], 'body': body,
                         'body_sha256': hashlib.sha256(event['body_json'].encode()).hexdigest(),
                         'delivery_status': event['delivery_status']}
    if set(by_kind) != {'running', 'result', 'exited'}:
        return None
    started, exited = by_kind['running']['body'], by_kind['exited']['body']
    process = exited.get('process')
    if not process or process != started.get('process') or process.get('pid', 0) <= 1 or not process.get('birth'):
        raise Failure('worker process tuple is not bound to running/exited')
    if exited.get('exit_code') != 0 or by_kind['result']['delivery_status'] != 'pending':
        raise Failure('original worker did not exit0 with pending result')
    if db.execute('SELECT COUNT(*) FROM tasks WHERE run_id=?', (run_id,)).fetchone()[0] != 1:
        raise Failure('unexpected run task')
    if db.execute("SELECT COUNT(*) FROM segment_runtime WHERE status NOT IN ('exited','not_started')").fetchone()[0]:
        raise Failure('execution still active')
    return {'row': row, 'events': by_kind, 'worker': process}


def owners(ctx, db, facts, origin):
    rows = db.execute('''SELECT owner_pid,owner_birth,current_host_pid,current_host_birth,intent_json,original_binding_json
        FROM execution_owners WHERE run_id=? AND launch_id=? AND is_current=1 AND phase='bound' ''',
        (ctx['run_id'], facts['row']['host_launch_id'])).fetchall()
    if len(rows) != 1:
        raise Failure('one bound supervised Owner required')
    row = rows[0]
    scope = json.loads(row['intent_json'])['scope']
    if scope['original_origin_pid'] != origin['origin_pid'] or scope['original_origin_birth'] != origin['origin_birth']:
        raise Failure('Owner origin mismatch')
    for prefix, role in [('owner', 'owner'), ('current_host', 'host')]:
        remember(ctx, row[prefix + '_pid'], row[prefix + '_birth'], role)


def fork_watch(pid):
    watcher = select.kqueue()
    try:
        event = select.kevent(pid, filter=select.KQ_FILTER_PROC, flags=select.KQ_EV_ADD | select.KQ_EV_ENABLE,
                              fflags=select.KQ_NOTE_FORK | select.KQ_NOTE_EXIT)
        watcher.control([event], 0, 0)
        return watcher
    except BaseException:
        watcher.close()
        raise


def drain_forks(ctx, watcher):
    events = [{'ident': e.ident, 'filter': e.filter, 'flags': e.flags, 'fflags': e.fflags, 'data': e.data}
              for e in watcher.control([], 64, 0)]
    ctx['fork_events'].extend(events)
    if events:
        ctx['soak'].atomic(ctx['out'] / 'kqueue-events.json', ctx['fork_events'])
        raise Failure('Coordinator fork/exit or kqueue error during offline window')


def measure(ctx, db, baseline):
    watcher = fork_watch(ctx['server'].pid)
    start = time.monotonic()
    first_cpu = last_cpu = None
    peak = 0
    try:
        with private_output(ctx['out'] / 'samples.jsonl') as output:
            # Include a final sample AND kqueue drain at/after the deadline.
            for tick in range(THRESHOLDS['offline_seconds'] + 1):
                delay = start + tick - time.monotonic()
                if delay > 0:
                    time.sleep(delay)
                if ctx['server'].poll() is not None:
                    raise Failure('original Coordinator exited early')
                rows = process_table()
                server = ctx['identities'][str(ctx['server'].pid)]
                if not identity_present(server, rows):
                    raise Failure('Coordinator identity changed')
                product = [r for r in rows.values() if r['argv'] == str(ctx['candidate']) or r['argv'].startswith(str(ctx['candidate']) + ' ')]
                if {r['pid'] for r in product} != {server['pid']} or any(r['ppid'] == server['pid'] for r in rows.values()):
                    raise Failure('offline state has unexpected product actor')
                if pending_facts(db, ctx['run_id'], ctx['task_id']) != baseline:
                    raise Failure('same pending event or exited segment changed')
                usage = ctx['soak'].cpu_seconds(rows[server['pid']]['cpu'])
                first_cpu = usage if first_cpu is None else first_cpu
                last_cpu = usage
                rss = sum(r['rss_kib'] for r in product)
                peak = max(peak, rss)
                sample = {'tick': tick, 'elapsed': time.monotonic() - start, 'rss_kib': rss, 'cpu_seconds': usage,
                          'pending_event_id': baseline['events']['result']['event_id'], 'coordinator_pid': server['pid']}
                output.write((json.dumps(sample, separators=(',', ':')) + '\n').encode())
                output.flush()
                if tick % 5 == 0:
                    os.fsync(output.fileno())
                drain_forks(ctx, watcher)
                if peak > THRESHOLDS['rss_limit_mib'] * 1024:
                    raise Failure('offline RSS exceeds frozen limit')
            elapsed = time.monotonic() - start
            percent = 100 * (last_cpu - first_cpu) / THRESHOLDS['offline_seconds']
            if percent < 0 or percent > THRESHOLDS['cpu_percent_one_core']:
                raise Failure('offline CPU exceeds frozen limit')
            os.fsync(output.fileno())
        ctx['soak'].atomic(ctx['out'] / 'kqueue-events.json', ctx['fork_events'])
        return {'samples': THRESHOLDS['offline_seconds'] + 1, 'elapsed': elapsed,
                'peak_rss_mib': peak / 1024, 'cpu_percent_one_core': percent}
    finally:
        watcher.close()


def cleanup_child(result, process, role, graceful_seconds, close_stdin=False):
    if process is None:
        return
    try:
        if close_stdin and process.stdin and not process.stdin.closed:
            process.stdin.close()
        if not close_stdin and process.poll() is None:
            process.terminate()
        result[role + '_original_wait'] = process.wait(timeout=graceful_seconds)
        return
    except BaseException as error:
        result['errors'].append(role + ' graceful wait: ' + repr(error))
    result['forced_' + role + '_cleanup'] = True
    # Each fallback is independent: failure to observe or signal one child must
    # not bypass the other original Popen handle or final residual inventory.
    for action in ('terminate', 'kill'):
        try:
            if process.poll() is None:
                getattr(process, action)()
            result[role + '_original_wait'] = process.wait(timeout=5)
            return
        except BaseException as error:
            result['errors'].append(role + ' ' + action + '/wait: ' + repr(error))


def cleanup(ctx):
    result = {'errors': [], 'identity_signals': [], 'remaining': []}
    captured = {}
    release = ctx.get('release')
    try:
        rows = process_table()
        roots = {int(pid) for pid, value in ctx.get('identities', {}).items() if value['role'] == 'worker'}
        if release:
            roots |= {pid for pid, row in rows.items() if str(release) in row['argv']}
        for _ in range(len(rows)):
            children = {pid for pid, row in rows.items() if row['ppid'] in roots}
            if children <= roots:
                break
            roots |= children
        for pid in roots:
            row = rows.get(pid)
            if row:
                value = {'pid': pid, 'birth': observed_birth(pid), 'argv': row['argv']}
                if value['birth']:
                    captured[pid] = value
    except BaseException as error:
        result['errors'].append('worker enrollment cleanup: ' + repr(error))
    if release:
        try:
            ctx['soak'].atomic(release, b'release\n')
        except BaseException as error:
            result['errors'].append('worker gate cleanup: ' + repr(error))
    cleanup_child(result, ctx.get('controller'), 'controller', 25, close_stdin=True)
    cleanup_child(result, ctx.get('server'), 'coordinator', 15)
    # Any remaining private executable is task-owned, but signal it only after
    # recording and rechecking its exact birth and argv. Such cleanup fails QA.
    until = time.monotonic() + 13
    while True:
        try:
            rows = process_table()
            remaining_ids = {pid for pid, row in rows.items() if row['argv'] == str(ctx['candidate']) or row['argv'].startswith(str(ctx['candidate']) + ' ') or (release and str(release) in row['argv'])}
            for pid, value in captured.items():
                if identity_present(value, rows):
                    remaining_ids.add(pid)
            for _ in range(len(rows)):
                children = {pid for pid, row in rows.items() if row['ppid'] in remaining_ids}
                if children <= remaining_ids:
                    break
                remaining_ids |= children
            remaining = [rows[pid] for pid in sorted(remaining_ids)]
            result['remaining'] = remaining
        except BaseException as error:
            result['errors'].append('residual inventory: ' + repr(error))
            break
        if not remaining or time.monotonic() >= until:
            break
        for row in remaining:
            try:
                identity = {'pid': row['pid'], 'birth': observed_birth(row['pid']), 'argv': row['argv']}
                if identity['birth'] and identity_present(identity, process_table()):
                    os.kill(identity['pid'], signal.SIGTERM)
                    result['identity_signals'].append(identity)
            except ProcessLookupError:
                pass
            except BaseException as error:
                result['errors'].append('residual cleanup: ' + repr(error))
        time.sleep(.1)
    return result


def main():
    parser = argparse.ArgumentParser()
    for name in ('candidate', 'freeze-manifest', 'out'):
        parser.add_argument('--' + name, type=pathlib.Path, required=True)
    args = parser.parse_args()
    if sys.platform != 'darwin' or not args.out.is_absolute() or args.out.exists() or not args.candidate.is_absolute():
        raise SystemExit('Darwin, absolute candidate and new absolute output required')
    frozen = json.loads(args.freeze_manifest.read_text())
    sources = {'candidate_sha256': args.candidate, 'runner_sha256': pathlib.Path(__file__),
               'controller_sha256': CONTROLLER, 'resource_soak_sha256': SOAK_SOURCE,
               'resource_soak_thresholds_sha256': SOAK_THRESHOLDS}
    if frozen.get('thresholds') != THRESHOLDS or any(frozen.get(k) != sha(p) for k, p in sources.items()):
        raise SystemExit('frozen manifest mismatch')
    args.out.mkdir(mode=0o700, parents=True)
    bindir = args.out / 'bin'
    bindir.mkdir(mode=0o700)
    library = args.out / 'lib/resource-soak'
    (library / 'scripts').mkdir(parents=True, mode=0o700)
    (library / 'data').mkdir(mode=0o700)
    candidate, controller_path, helper = bindir / 'candidate', bindir / 'controller.py', library / 'scripts/resource_soak.py'
    copies = {'candidate_sha256': candidate, 'controller_sha256': controller_path,
              'resource_soak_sha256': helper, 'resource_soak_thresholds_sha256': library / 'data/thresholds.json'}
    for name, destination in copies.items():
        private_copy(sources[name], destination, frozen[name])
    spec = importlib.util.spec_from_file_location('soak', helper)
    soak = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(soak)
    start_pins = {name: soak.pin(p) for name, p in copies.items()}
    raw = args.out / 'raw'
    raw.mkdir(mode=0o700)
    controller_raw = raw / 'controller'
    controller_raw.mkdir(mode=0o700)
    work = pathlib.Path('/private/tmp') / ('resource-offline-' + uuid.uuid4().hex[:12])
    work.mkdir(mode=0o700)
    state, ready = work / 'state', work / 'controller-ready.json'
    env = {'PATH': '/usr/bin:/bin:/usr/sbin:/sbin', 'LANG': 'C', 'LC_ALL': 'C',
           'TMPDIR': str(work), 'ORCHESTRATOR_ENABLE_TEST_FAKE': '1'}
    ctx = {'out': args.out, 'candidate': candidate, 'soak': soak, 'identities': {}, 'fork_events': [],
           'run_id': 'offline-' + uuid.uuid4().hex, 'task_id': 'offline-pending', 'release': work / 'worker-release'}
    summary = {'result': 'failed', 'frozen': frozen, 'pins_start': start_pins, 'state_preserved': str(state),
               'limitations': ['controlled fake worker, no real Provider/model', 'Owner/Host observed by exact identity; no new original-Wait claim for those actors', 'not idle30s or four-Host acceptance']}
    db = None
    try:
        ctx['server'] = subprocess.Popen([str(candidate), 'serve', '--state-dir', str(state)], env=env,
            stdin=subprocess.DEVNULL, stdout=private_output(raw / 'server.stdout'), stderr=private_output(raw / 'server.stderr'))
        remember(ctx, ctx['server'].pid, observed_birth(ctx['server'].pid), 'coordinator')
        end = time.monotonic() + 15
        while not (state / 'coordinator.sock').exists():
            if ctx['server'].poll() is not None or time.monotonic() > end:
                raise Failure('owned Coordinator startup failed')
            time.sleep(.05)
        ctx['controller'] = subprocess.Popen([sys.executable, str(controller_path), '--candidate', str(candidate),
            '--state', str(state), '--work', str(work), '--ready', str(ready), '--helper', str(helper),
            '--raw', str(controller_raw), '--run-id', ctx['run_id']], env=env, stdin=subprocess.PIPE,
            stdout=private_output(raw / 'origin.stdout'), stderr=private_output(raw / 'origin.stderr'))
        controller = ctx['controller']
        origin_identity = remember(ctx, controller.pid, observed_birth(controller.pid), 'controller')
        end = time.monotonic() + 45
        while not ready.exists():
            if controller.poll() is not None or time.monotonic() > end:
                raise Failure('public controller handshake failed')
            time.sleep(.05)
        origin = json.loads(ready.read_text())
        if origin['origin_pid'] != controller.pid or origin['origin_birth'] != origin_identity['birth'] or origin['run_id'] != ctx['run_id'] or origin['task_id'] != ctx['task_id']:
            raise Failure('controller response identity mismatch')
        db = sqlite3.connect((state / 'state.db').as_uri() + '?mode=ro', uri=True, timeout=2, isolation_level=None)
        db.row_factory = sqlite3.Row
        db.execute('PRAGMA query_only=ON')
        # Enroll exact Owner/Host while the controlled worker is still gated.
        # A completed worker may already let them retire before origin loss.
        end = time.monotonic() + 20
        running = None
        while time.monotonic() < end:
            rows = db.execute('''SELECT tr.host_launch_id,e.body_json FROM tasks t
                JOIN task_runtime tr ON tr.task_id=t.id JOIN attempts a ON a.task_id=t.id
                JOIN segment_runtime s ON s.attempt_id=a.id JOIN runtime_events e
                ON e.task_id=t.id AND e.attempt_id=a.id AND e.segment_id=s.segment_id AND e.producer_id=s.host_id
                WHERE t.run_id=? AND t.id=? AND s.status='running' AND json_extract(e.body_json,'$.kind')='running' ''',
                (ctx['run_id'], ctx['task_id'])).fetchall()
            if len(rows) == 1:
                running = rows[0]
                break
            time.sleep(.05)
        if running is None:
            raise Failure('gated worker running event missing')
        started_worker = json.loads(running['body_json'])['process']
        remember(ctx, started_worker['pid'], started_worker['birth'], 'worker')
        owners(ctx, db, {'row': dict(running)}, origin)
        soak.atomic(args.out / 'identities.json', ctx['identities'])
        soak.atomic(work / 'worker-release', b'release\n')
        end = time.monotonic() + 20
        facts = None
        while time.monotonic() < end:
            facts = pending_facts(db, ctx['run_id'], ctx['task_id'])
            if facts:
                break
            time.sleep(.05)
        if facts is None:
            raise Failure('durable pending result and original worker Exited0 not observed')
        if identity_present(facts['worker'], process_table(), require_argv=False):
            raise Failure('original worker not reaped')
        if facts['worker'] != started_worker:
            raise Failure('pending result is not the enrolled gated worker')
        soak.atomic(args.out / 'pending-baseline.json', facts)
        controller.stdin.close()
        if controller.wait(timeout=13) != 0:
            raise Failure('controller original Wait nonzero')
        end = time.monotonic() + THRESHOLDS['host_exit_seconds']
        while time.monotonic() < end:
            rows = process_table()
            if not any(identity_present(i, rows) for i in ctx['identities'].values() if i['role'] in ('owner', 'host')):
                break
            time.sleep(.1)
        else:
            raise Failure('Owner/Host did not naturally exit after origin loss')
        summary.update(measure(ctx, db, facts))
        summary['result'] = 'passed'
    except BaseException as e:
        summary['error'] = repr(e)
    finally:
        if db is not None:
            try:
                db.close()
            except BaseException as e:
                summary['database_close_error'] = repr(e)
                summary['result'] = 'failed'
        try:
            summary['cleanup'] = cleanup(ctx)
            c = summary['cleanup']
            if c['errors'] or c['remaining'] or c['identity_signals'] or c.get('controller_original_wait') != 0 or c.get('coordinator_original_wait') != 0:
                summary['result'] = 'failed'
        except BaseException as e:
            summary['cleanup_error'] = repr(e)
            summary['result'] = 'failed'
        try:
            summary['pins_end'] = {name: soak.pin(p) for name, p in copies.items()}
            summary['source_hashes_end'] = {name: sha(p) for name, p in sources.items()}
            if summary['pins_end'] != start_pins or any(summary['source_hashes_end'][k] != frozen[k] for k in sources):
                raise Failure('frozen source/copy changed')
        except BaseException as e:
            summary['pin_error'] = repr(e)
            summary['result'] = 'failed'
        soak.atomic(args.out / 'summary.json', summary)
    print(json.dumps({'result': summary['result'], 'error': summary.get('error'), 'output': str(args.out)}))
    return 0 if summary['result'] == 'passed' else 1


if __name__ == '__main__':
    raise SystemExit(main())
