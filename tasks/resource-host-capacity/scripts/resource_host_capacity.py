#!/usr/bin/env python3
"""Four supervised source Hosts, two slots, exact fifth rejection and RSS."""
import argparse
import hashlib
import importlib.util
import json
import os
import pathlib
import select
import signal
import sqlite3
import subprocess
import sys
import time
import uuid

sys.dont_write_bytecode = True
ROOT = pathlib.Path(__file__).resolve().parents[1]
TH = json.loads((ROOT / 'data/thresholds.json').read_text())
TABLES = ('runs', 'host_launches', 'runtime_hosts', 'execution_owners', 'tasks',
          'attempts', 'segments', 'segment_runtime', 'task_runtime', 'runtime_meta', 'owner_grants')


class Failure(RuntimeError):
    pass


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


def inventory(db):
    result = {}
    db.execute('BEGIN')
    try:
        for table in TABLES:
            rows = db.execute('SELECT * FROM ' + table + ' ORDER BY 1').fetchall()
            if len(rows) > 256:
                raise Failure('unexpected unbounded inventory')
            raw = json.dumps([dict(row) for row in rows], sort_keys=True, separators=(',', ':')).encode()
            result[table] = {'count': len(rows), 'sha256': hashlib.sha256(raw).hexdigest()}
        result['next_ready_sequence'] = db.execute('SELECT next_ready_sequence FROM runtime_meta').fetchone()[0]
    finally:
        db.execute('ROLLBACK')
    return result


def private_entries(state):
    result = {}
    for directory in ('control', 'host-bootstrap', 'owners'):
        parent = state / directory
        if not parent.exists():
            continue
        values = []
        for path in sorted(parent.rglob('*')):
            info = path.lstat()
            values.append({'path': str(path.relative_to(state)), 'mode': info.st_mode,
                           'uid': info.st_uid, 'nlink': info.st_nlink, 'dev': info.st_dev, 'ino': info.st_ino})
        result[directory] = values
    return result


def wait_file(ctx, path, child, seconds=30):
    until = time.monotonic() + seconds
    while not path.exists():
        if child.poll() is not None or time.monotonic() > until:
            raise Failure('owned controller did not produce ' + path.name)
        time.sleep(.02)
    return json.loads(path.read_text())


def launch_origin(ctx, number, fifth=False):
    work = ctx['work'] / ('origin-' + str(number))
    work.mkdir(mode=0o700)
    raw = ctx['out'] / 'raw' / ('origin-' + str(number))
    raw.mkdir(mode=0o700)
    ready = work / 'ready.json'
    run_id, task_id = 'capacity-' + ctx['id'] + '-' + str(number), 'capacity-task-' + str(number)
    argv = [sys.executable, str(ctx['controller_path']), '--candidate', str(ctx['candidate']),
            '--state', str(ctx['state']), '--work', str(work), '--ready', str(ready),
            '--helper', str(ctx['helper']), '--raw', str(raw), '--worker', str(ctx['worker']),
            '--run-id', run_id, '--task-id', task_id, '--label', str(number)]
    if fifth:
        argv.append('--fifth')
    process = subprocess.Popen(argv, env=ctx['env'], stdin=subprocess.PIPE,
        stdout=ctx['idle'].private_output(raw / 'controller.stdout'),
        stderr=ctx['idle'].private_output(raw / 'controller.stderr'))
    item = {'process': process, 'work': work, 'run_id': run_id, 'task_id': task_id}
    ctx['origins'].append(item)  # Cleanup owns the original handle even if ready fails.
    identity = ctx['idle'].remember(ctx, process.pid, ctx['idle'].observed_birth(process.pid), 'controller-' + str(number))
    reply = wait_file(ctx, ready, process)
    if reply['origin_pid'] != process.pid or reply['origin_birth'] != identity['birth'] or reply['run_id'] != run_id or reply['task_id'] != task_id:
        raise Failure('origin reply changed original identity')
    item.update(reply=reply, identity=identity)
    return item


def running(db, item):
    rows = db.execute('''SELECT a.id AS attempt_id,a.attempt_no,s.segment_id,s.host_id,tr.host_launch_id,e.body_json
        FROM tasks t JOIN task_runtime tr ON tr.task_id=t.id JOIN attempts a ON a.task_id=t.id
        JOIN segment_runtime s ON s.attempt_id=a.id JOIN runtime_events e
        ON e.task_id=t.id AND e.attempt_id=a.id AND e.segment_id=s.segment_id AND e.producer_id=s.host_id
        WHERE t.id=? AND t.run_id=? AND t.status='running' AND s.status='running'
        AND json_extract(e.body_json,'$.kind')='running' ''', (item['task_id'], item['run_id'])).fetchall()
    if not rows:
        return None
    if len(rows) != 1 or rows[0]['attempt_no'] != 1:
        raise Failure('running lifecycle is not one original attempt')
    value = dict(rows[0])
    process = json.loads(value.pop('body_json')).get('process', {})
    if process.get('pid', 0) <= 1 or not process.get('birth'):
        raise Failure('running event lacks durable process identity')
    value['process'] = process
    return value


def wait_running(ctx, db, item):
    until = time.monotonic() + 20
    while True:
        value = running(db, item)
        if value:
            ctx['idle'].remember(ctx, value['process']['pid'], value['process']['birth'], 'worker')
            item['running'] = value
            return value
        if item['process'].poll() is not None or time.monotonic() > until:
            raise Failure('durable running event did not arrive')
        time.sleep(.03)


def enroll_owners(ctx, db):
    for index, item in enumerate(ctx['origins'][:4]):
        rows = db.execute('''SELECT o.*,h.pid AS actual_host_pid,h.birth AS actual_host_birth
            FROM execution_owners o JOIN runtime_hosts h ON h.launch_id=o.launch_id
            WHERE o.run_id=? AND o.is_current=1 AND o.phase='bound' AND h.status='ready'
            AND o.applied_control_revision=o.control_revision AND o.control_revision>0''', (item['run_id'],)).fetchall()
        if len(rows) != 1:
            raise Failure('one ready authenticated Host and Applied Owner required per origin')
        row = rows[0]
        scope = json.loads(row['intent_json'])['scope']
        if scope['original_origin_pid'] != item['reply']['origin_pid'] or scope['original_origin_birth'] != item['reply']['origin_birth']:
            raise Failure('signed Owner scope differs from enrolled origin')
        if (row['actual_host_pid'], row['actual_host_birth']) != (row['current_host_pid'], row['current_host_birth']):
            raise Failure('current Host identity mismatch')
        for prefix, role in (('owner', 'owner'), ('current_host', 'host')):
            ctx['idle'].remember(ctx, row[prefix + '_pid'], row[prefix + '_birth'], role + '-' + str(index))
    if db.execute("SELECT COUNT(*) FROM host_launches WHERE status!='released'").fetchone()[0] != 4:
        raise Failure('four occupied launches required')
    states = dict(db.execute('SELECT status,COUNT(*) FROM tasks GROUP BY status').fetchall())
    if states != {'running': 2, 'ready': 2}:
        raise Failure('default two running and two ready invariant failed: ' + repr(states))
    for table in ('attempts', 'segments', 'segment_runtime'):
        if db.execute('SELECT COUNT(*) FROM ' + table).fetchone()[0] != 2:
            raise Failure('ready tasks acquired execution reservations')
    if db.execute('SELECT next_ready_sequence FROM runtime_meta').fetchone()[0] != 5:
        raise Failure('unexpected original ready sequence')


def kernel_watch(identities):
    watcher = select.kqueue()
    try:
        events = [select.kevent(identity['pid'], filter=select.KQ_FILTER_PROC,
            flags=select.KQ_EV_ADD | select.KQ_EV_ENABLE,
            fflags=select.KQ_NOTE_FORK | select.KQ_NOTE_EXIT | select.KQ_NOTE_EXEC) for identity in identities]
        watcher.control(events, 0, 0)
        return watcher
    except BaseException:
        watcher.close()
        raise


def drain_kernel(ctx, watcher, helper_pid):
    events = watcher.control([], 64, 0)
    for event in events:
        row = {'ident': event.ident, 'flags': event.flags, 'fflags': event.fflags,
               'data': event.data, 'monotonic': time.monotonic()}
        ctx['kernel_events'].append(row)
        if event.flags & select.KQ_EV_ERROR or event.ident != helper_pid or event.fflags & select.KQ_NOTE_FORK or event.fflags & ~(select.KQ_NOTE_EXEC | select.KQ_NOTE_EXIT):
            raise Failure('forbidden kernel fork/exit/exec or observer error')


def measure(ctx, db, fifth):
    idle, soak = ctx['idle'], ctx['soak']
    actors = [i for i in ctx['identities'].values() if i['role'] == 'coordinator' or i['role'].startswith(('owner-', 'host-'))]
    sources = [i['identity'] for i in ctx['origins']]
    if len(actors) != TH['product_actors'] or len(sources) != 5:
        raise Failure('missing product actor or live origin')
    helper = {'pid': fifth['reply']['helper_pid'], 'birth': fifth['reply']['helper_birth']}
    rows = idle.process_table()
    if helper['pid'] not in rows or 'T' not in rows[helper['pid']]['stat'] or not idle.identity_present(helper, rows, require_argv=False):
        raise Failure('fifth helper not stopped at owned pre-exec boundary')
    before, files_before = inventory(db), private_entries(ctx['state'])
    soak.atomic(ctx['out'] / 'inventory-before.json', before)
    watcher = kernel_watch([*actors, *sources, helper])
    samples = []
    started = time.monotonic()
    try:
        fifth['process'].stdin.write(b'go\n')
        fifth['process'].stdin.flush()
        with idle.private_output(ctx['out'] / 'samples.jsonl') as raw:
            for tick in range(TH['window_seconds'] + 1):
                delay = started + tick - time.monotonic()
                if delay > 0:
                    time.sleep(delay)
                rows = idle.process_table()
                for identity in [*actors, *sources]:
                    if not idle.identity_present(identity, rows) or rows[identity['pid']]['stat'].startswith('Z'):
                        raise Failure('enrolled actor/origin exited, changed or became zombie')
                known = {i['pid'] for i in actors} | {helper['pid']}
                unknown = [row for pid, row in rows.items() if (row['argv'] == str(ctx['candidate']) or row['argv'].startswith(str(ctx['candidate']) + ' ')) and pid not in known]
                if unknown:
                    raise Failure('unregistered private product process: ' + repr(unknown))
                drain_kernel(ctx, watcher, helper['pid'])
                if inventory(db) != before:
                    raise Failure('fifth rejection changed exact database inventory or queue')
                entry = {'tick': tick, 'elapsed_seconds': time.monotonic()-started,
                         'product_rss_bytes': sum(rows[i['pid']]['rss_kib'] * 1024 for i in actors),
                         'actors': [{'identity': i, 'rss_kib': rows[i['pid']]['rss_kib']} for i in actors]}
                samples.append(entry)
                raw.write((json.dumps(entry, sort_keys=True) + '\n').encode())
                raw.flush()
        drain_kernel(ctx, watcher, helper['pid'])
    finally:
        watcher.close()
        soak.atomic(ctx['out'] / 'kernel-events.json', ctx['kernel_events'])
    fifth_result = wait_file(ctx, fifth['work'] / 'submitted.json', fifth['process'], 2)
    soak.atomic(ctx['out'] / 'fifth-submit.json', fifth_result)
    if fifth_result['original_wait'] != 2 or fifth_result['forced'] or fifth_result['stdout'] != '' or fifth_result['stderr'] != '{"error":"admission_deferred","status":"error","version":1}\n':
        raise Failure('fifth public submit was not exact typed admission_deferred')
    if not any(e['ident'] == helper['pid'] and e['fflags'] & select.KQ_NOTE_EXIT for e in ctx['kernel_events']):
        raise Failure('fifth helper exit not observed by kernel')
    if inventory(db) != before or private_entries(ctx['state']) != files_before:
        raise Failure('fifth submit changed durable inventory or private capabilities')
    if samples[-1]['elapsed_seconds'] < TH['window_seconds']:
        raise Failure('missing final observation at frozen deadline')
    allowance = fifth_result['max_rss_bytes']
    if allowance <= 0:
        raise Failure('missing original helper maximum RSS')
    peak = max(s['product_rss_bytes'] + allowance for s in samples)
    return {'samples': len(samples), 'elapsed_seconds': samples[-1]['elapsed_seconds'],
            'peak_charged_rss_mib': peak / 1048576, 'helper_allowance_bytes': allowance,
            'rss_passed': peak <= TH['rss_limit_mib'] * 1048576,
            'inventory_unchanged': True, 'fifth_original_wait': 2}


def complete(ctx, db):
    soak, idle = ctx['soak'], ctx['idle']
    for item in ctx['origins'][:2]:
        soak.atomic(item['work'] / 'worker-release', b'release\n')
    for item in ctx['origins'][2:4]:
        wait_running(ctx, db, item)
        soak.atomic(item['work'] / 'worker-release', b'release\n')
    until = time.monotonic() + 20
    while db.execute("SELECT COUNT(*) FROM segment_runtime WHERE status!='exited'").fetchone()[0]:
        if time.monotonic() > until:
            raise Failure('controlled workers did not complete')
        time.sleep(.03)
    facts = []
    for item in ctx['origins'][:4]:
        value = idle.pending_facts(db, item['run_id'], item['task_id'])
        if value is None or value['worker'] != item['running']['process'] or idle.identity_present(value['worker'], idle.process_table(), require_argv=False):
            raise Failure('pending result lost original worker tuple or reaping')
        facts.append(value)
        common = ('--state-dir', str(ctx['state']), '--task-id', item['task_id'], '--control-file', item['reply']['control_file'])
        collection = idle.public_call(ctx, 'collect', *common)
        events = [e for e in collection['events'] if e['kind'] == 'result']
        if len(events) != 1 or events[0]['event_id'] != value['events']['result']['event_id']:
            raise Failure('public collection differs from pending lifecycle')
        event = events[0]
        idle.public_call(ctx, 'accept', *common, '--work-revision', '1', '--event-id', event['event_id'],
            '--event-revision', str(event['event_revision']), '--event-hash', event['payload_hash'],
            '--action-slot', event['action_slot'], '--decision', 'accept', '--command-id', 'capacity-accept')
        request = item['work'] / 'delivery-ack.json'
        soak.atomic(request, {'version': 1, 'task_id': item['task_id'], 'control_file': item['reply']['control_file'],
            'delivery_id': collection['delivery_id'], 'collection_proof_sha256': collection['collection_proof_sha256'],
            'decisions': [{'event_id': event['event_id'], 'event_revision': event['event_revision'],
                'event_hash': event['payload_hash'], 'action_slot': event['action_slot'], 'decision': 'handled', 'command_id': 'capacity-ack'}]})
        reply = idle.public_call(ctx, 'ack', '--state-dir', str(ctx['state']), '--request', str(request))
        if reply.get('status') != 'acknowledged':
            raise Failure('completion delivery not acknowledged')
    soak.atomic(ctx['out'] / 'worker-exits.json', facts)
    if idle.idle_count(db) != 0:
        raise Failure('completed and acknowledged runs are not durably idle')
    until = time.monotonic() + TH['host_retire_seconds']
    actors = [i for i in ctx['identities'].values() if i['role'] == 'worker' or i['role'].startswith(('host-', 'owner-'))]
    while True:
        rows = idle.process_table()
        remaining = [i for i in actors if idle.identity_present(i, rows)]
        now = time.monotonic()
        if not remaining:
            if now > until:
                raise Failure('retirement proof completed after deadline')
            break
        if now > until:
            raise Failure('product actors did not retire naturally')
        time.sleep(.05)
    if ctx['server'].wait(timeout=35) != 0:
        raise Failure('Coordinator did not return natural original Wait0')


def cleanup(ctx):
    result = {'controller_waits': [], 'errors': []}
    # Capture private gated workers before releasing parents or controller handles.
    try:
        for pid, row in ctx['idle'].process_table().items():
            if ' ' + str(ctx['worker']) + ' ' in row['argv']:
                ctx['idle'].remember(ctx, pid, ctx['idle'].observed_birth(pid), 'worker')
    except BaseException as error:
        result['errors'].append('worker cleanup inventory: ' + repr(error))
    for item in ctx['origins']:
        try:
            ctx['soak'].atomic(item['work'] / 'worker-release', b'release\n')
        except BaseException as error:
            result['errors'].append(repr(error))
    for index, item in enumerate(ctx['origins']):
        record = {'errors': [], 'identity_signals': []}
        ctx['idle'].cleanup_child(record, item['process'], 'controller', 30, close_stdin=True)
        result['controller_waits'].append(record)
    result['product'] = ctx['idle'].cleanup(ctx)
    return result


def main():
    parser = argparse.ArgumentParser()
    for name in ('candidate', 'freeze-manifest', 'out'):
        parser.add_argument('--' + name, type=pathlib.Path, required=True)
    args = parser.parse_args()
    if sys.platform != 'darwin' or not args.out.is_absolute() or args.out.exists() or not args.candidate.is_absolute():
        raise SystemExit('Darwin, absolute candidate and new absolute output required')
    sources = {'candidate': args.candidate, 'runner': pathlib.Path(__file__).resolve(),
        'controller': ROOT / 'scripts/controller.py', 'worker': ROOT / 'scripts/worker.py',
        'thresholds': ROOT / 'data/thresholds.json', 'idle': ROOT.parent / 'resource-idle/scripts/resource_idle.py',
        'idle_thresholds': ROOT.parent / 'resource-idle/data/thresholds.json',
        'soak': ROOT.parent / 'resource-soak/scripts/resource_soak.py',
        'soak_thresholds': ROOT.parent / 'resource-soak/data/thresholds.json'}
    frozen = json.loads(args.freeze_manifest.read_text())
    if frozen['candidate_path'] != str(args.candidate) or frozen['thresholds'] != TH or frozen['source_sha256'] != {key: sha(path) for key, path in sources.items()}:
        raise SystemExit('frozen source or threshold mismatch')
    idle_source = module('original_idle', sources['idle'])
    args.out.mkdir(mode=0o700)
    for relative in ('bin', 'lib', 'lib/resource-idle', 'lib/resource-idle/scripts', 'lib/resource-idle/data',
                     'lib/resource-soak', 'lib/resource-soak/scripts', 'lib/resource-soak/data', 'raw'):
        (args.out / relative).mkdir(mode=0o700)
    copies = {'candidate': args.out / 'bin/candidate', 'runner': args.out / 'bin/runner.py',
        'controller': args.out / 'bin/controller.py', 'worker': args.out / 'bin/worker.py',
        'thresholds': args.out / 'bin/thresholds.json',
        'idle': args.out / 'lib/resource-idle/scripts/resource_idle.py',
        'idle_thresholds': args.out / 'lib/resource-idle/data/thresholds.json',
        'soak': args.out / 'lib/resource-soak/scripts/resource_soak.py',
        'soak_thresholds': args.out / 'lib/resource-soak/data/thresholds.json'}
    for key, target in copies.items():
        idle_source.private_copy(sources[key], target, frozen['source_sha256'][key])
    idle, soak = module('idle', copies['idle']), module('soak', copies['soak'])
    pins = {key: soak.pin(path) for key, path in copies.items()}
    work = pathlib.Path('/private/tmp') / ('resource-host-capacity-' + uuid.uuid4().hex[:12])
    work.mkdir(mode=0o700)
    env = {'PATH': '/usr/bin:/bin:/usr/sbin:/sbin', 'LANG': 'C', 'LC_ALL': 'C', 'TMPDIR': str(work),
           'ORCHESTRATOR_ENABLE_TEST_FAKE': '1', 'PYTHONDONTWRITEBYTECODE': '1'}
    ctx = {'id': uuid.uuid4().hex[:12], 'idle': idle, 'soak': soak, 'out': args.out, 'work': work,
           'state': work / 'state', 'candidate': copies['candidate'], 'helper': copies['soak'],
           'controller_path': copies['controller'], 'worker': copies['worker'], 'env': env,
           'origins': [], 'identities': {}, 'kernel_events': []}
    summary = {'result': 'failed', 'frozen': frozen, 'pins_start': pins, 'private_state': str(ctx['state']),
               'limitations': ['controlled TEST_FAKE public path, not native bridge capability or real Provider/model',
                              'bounded four-Host window, not formal60 flood qualification',
                              'Owner/Host exact retirement, original Wait evidence reused from native lifecycle tests']}
    db = None
    try:
        ctx['server'] = subprocess.Popen([str(ctx['candidate']), 'serve', '--state-dir', str(ctx['state'])],
            env=env, stdin=subprocess.DEVNULL, stdout=idle.private_output(args.out / 'raw/server.stdout'),
            stderr=idle.private_output(args.out / 'raw/server.stderr'))
        idle.remember(ctx, ctx['server'].pid, idle.observed_birth(ctx['server'].pid), 'coordinator')
        until = time.monotonic() + 15
        while not (ctx['state'] / 'coordinator.sock').exists():
            if ctx['server'].poll() is not None or time.monotonic() > until:
                raise Failure('Coordinator startup failed')
            time.sleep(.03)
        db = sqlite3.connect((ctx['state'] / 'state.db').as_uri() + '?mode=ro', uri=True, timeout=2, isolation_level=None)
        db.row_factory = sqlite3.Row
        db.execute('PRAGMA query_only=ON')
        for index in range(4):
            item = launch_origin(ctx, index)
            # owner-bind refuses any unproved active process; serialize only
            # this admission dependency, not the two worker executions.
            if index < 2:
                wait_running(ctx, db, item)
        enroll_owners(ctx, db)
        fifth = launch_origin(ctx, 4, fifth=True)
        soak.atomic(args.out / 'identities.json', ctx['identities'])
        summary.update(measure(ctx, db, fifth))
        complete(ctx, db)
        if not summary['rss_passed']:
            raise Failure('four-Host RSS exceeded frozen128MiB')
        summary['result'] = 'passed'
    except BaseException as error:
        summary['error'] = repr(error)
    finally:
        if db is not None:
            db.close()
        try:
            summary['cleanup'] = cleanup(ctx)
            c = summary['cleanup']
            if c['errors'] or any(w['errors'] or w['identity_signals'] or w.get('controller_original_wait') != 0 for w in c['controller_waits']) or c['product']['errors'] or c['product']['identity_signals'] or c['product']['remaining'] or c['product'].get('coordinator_original_wait') != 0:
                summary['result'] = 'failed'
        except BaseException as error:
            summary.update(result='failed', cleanup_error=repr(error))
        try:
            summary['pins_end'] = {key: soak.pin(path) for key, path in copies.items()}
            summary['source_hashes_end'] = {key: sha(path) for key, path in sources.items()}
            if summary['pins_end'] != pins or summary['source_hashes_end'] != frozen['source_sha256']:
                raise Failure('frozen source/copy drift')
        except BaseException as error:
            summary.update(result='failed', pin_error=repr(error))
        soak.atomic(args.out / 'summary.json', summary)
    print(json.dumps({'result': summary['result'], 'error': summary.get('error'), 'out': str(args.out)}))
    return 0 if summary['result'] == 'passed' else 1


if __name__ == '__main__':
    raise SystemExit(main())
