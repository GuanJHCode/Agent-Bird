#!/usr/bin/env python3
"""Diagnostic collection only: no resource-acceptance verdict or forced GC."""
import argparse
import concurrent.futures
import hashlib
import importlib.util
import json
import os
import pathlib
import signal
import subprocess
import sys
import time
import uuid

sys.dont_write_bytecode = True
ROOT = pathlib.Path(__file__).resolve().parents[1]
PLAN = {'duration_seconds': 1200, 'worker_seconds': 1140, 'phase_seconds': 300,
        'bytes_per_second': 1048576, 'giant_every': 30, 'report_every': 10,
        'memory_sample_seconds': 5, 'acceptance_verdict': 'not_applicable'}


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def read_module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


def observe_system(ctx, tick, boundary=False):
    """External observation helpers have separate original wait4 receipts."""
    soak = ctx['soak']
    commands = [] if boundary else [['/usr/bin/vm_stat'], ['/usr/sbin/sysctl', '-n', 'vm.swapusage']]
    if boundary:
        rows = soak.process_table()
        for identity in ctx['identities'].values():
            if identity['role'] in ('coordinator', 'host', 'owner') and soak.identity_alive(identity, rows):
                commands.append(['/usr/bin/vmmap', '-summary', str(identity['pid'])])
    for index, command in enumerate(commands):
        kind = 'vmmap' if boundary else 'system'
        prefix = ctx['out'] / 'system' / f'{tick:04d}-{kind}-{index}'
        out = os.open(str(prefix) + '.stdout', os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        err = os.open(str(prefix) + '.stderr', os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        started = time.time_ns()
        try:
            process = subprocess.Popen(command, stdin=subprocess.DEVNULL, stdout=out, stderr=err,
                                       env=ctx['env'])
        finally:
            os.close(out)
            os.close(err)
        deadline = time.monotonic() + 4
        forced = False
        while True:
            pid, status, usage = os.wait4(process.pid, os.WNOHANG)
            if pid:
                break
            if time.monotonic() > deadline:
                forced = True
                os.kill(process.pid, signal.SIGKILL)  # Original child is still unreaped.
                pid, status, usage = os.wait4(process.pid, 0)
                break
            time.sleep(.02)
        process.returncode = os.waitstatus_to_exitcode(status)
        soak.atomic(pathlib.Path(str(prefix) + '.wait.json'), {'argv': command, 'pid': pid,
            'started_unix_ns': started, 'finished_unix_ns': time.time_ns(),
            'original_wait': process.returncode, 'forced': forced,
            'max_rss_bytes': usage.ru_maxrss, 'cpu_seconds': usage.ru_utime + usage.ru_stime,
            'scope': 'excluded external diagnostic observer, not product helper'})


def verify_memory(ctx):
    roles = {'coordinator': 'serve', 'host': 'source-host', 'owner': 'original-owner'}
    found = {}
    for identity in ctx['identities'].values():
        if identity['role'] not in roles:
            continue
        path = ctx['trace'] / f"{roles[identity['role']]}-{identity['pid']}.jsonl"
        rows = [json.loads(line) for line in path.read_text().splitlines()]
        if not rows or any(r['pid'] != identity['pid'] or r['role'] != roles[identity['role']] for r in rows):
            raise RuntimeError('memory sample is not from the enrolled actor')
        found[identity['role']] = {'pid': identity['pid'], 'path': str(path), 'samples': len(rows)}
    if set(found) != set(roles):
        raise RuntimeError('per-actor memory instrumentation missing')
    return found


def main():
    parser = argparse.ArgumentParser()
    for name in ('candidate', 'worker', 'freeze-manifest', 'out'):
        parser.add_argument('--' + name, type=pathlib.Path, required=True)
    args = parser.parse_args()
    if sys.platform != 'darwin' or not args.out.is_absolute() or args.out.exists():
        raise SystemExit('Darwin and a new absolute output required')
    source = ROOT.parent / 'resource-soak/scripts/resource_soak.py'
    thresholds = ROOT.parent / 'resource-soak/data/thresholds.json'
    frozen = json.loads(args.freeze_manifest.read_text())
    sources = {'candidate': args.candidate, 'worker': args.worker, 'runner': pathlib.Path(__file__).resolve(),
               'helper': source, 'thresholds': thresholds}
    if frozen['plan'] != PLAN or frozen['source_sha256'] != {k: sha(v) for k, v in sources.items()}:
        raise SystemExit('diagnostic source or plan freeze mismatch')
    original = read_module('original_soak', source)
    args.out.mkdir(mode=0o700)
    for name in ('bin', 'raw', 'system', 'metrics', 'lib', 'lib/scripts', 'lib/data'):
        (args.out / name).mkdir(mode=0o700)
    copies = {'candidate': args.out / 'bin/resource-memory-diagnostic-cli-fixture',
        'worker': args.out / 'bin/resource-soak-worker-fixture', 'runner': args.out / 'bin/diagnose.py',
        'helper': args.out / 'lib/scripts/resource_soak.py', 'thresholds': args.out / 'lib/data/thresholds.json'}
    pins = {key: original.copy_pinned(sources[key], path, frozen['source_sha256'][key]) for key, path in copies.items()}
    soak = read_module('soak', copies['helper'])
    work = pathlib.Path('/private/tmp') / ('resource-memory-' + uuid.uuid4().hex[:12])
    work.mkdir(mode=0o700)
    trace = work / 'metrics'
    trace.mkdir(mode=0o700)
    ctx = {'out': args.out, 'raw': args.out / 'raw', 'candidate': copies['candidate'], 'worker': copies['worker'],
        'state': work / 'state', 'calls': 0, 'workers': {}, 'identities': {}, 'soak': soak, 'trace': trace,
        'env': {'PATH': '/usr/bin:/bin:/usr/sbin:/sbin', 'LANG': 'C', 'LC_ALL': 'C', 'TMPDIR': str(work),
                'ORCHESTRATOR_ENABLE_TEST_FAKE': '1', 'RESOURCE_MEMORY_TRACE_DIR': str(trace)}}
    summary = {'result': 'failed', 'acceptance_verdict': 'not_applicable', 'frozen': frozen,
               'pins_start': pins, 'private_state': str(ctx['state']), 'started_unix_ns': time.time_ns(),
               'limitations': ['diagnostic test binary with5s ReadMemStats+file sync; no formal resource qualification',
                   'vmmap/vm_stat observers may perturb timing; no forced GC or artificial memory pressure',
                   'controlled fake adapter, no Provider/model/account', 'shortened phase durations cannot exclude hour-only behavior']}
    db = None
    pool = concurrent.futures.ThreadPoolExecutor(max_workers=2)
    observers = {'system': None, 'vmmap': None}
    try:
        stdout = os.fdopen(os.open(ctx['raw'] / 'server.stdout', os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600), 'wb')
        stderr = os.fdopen(os.open(ctx['raw'] / 'server.stderr', os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600), 'wb')
        ctx['server'] = subprocess.Popen([str(ctx['candidate']), 'serve', '--state-dir', str(ctx['state'])],
            stdin=subprocess.DEVNULL, stdout=stdout, stderr=stderr, env=ctx['env'])
        stdout.close()
        stderr.close()
        soak.remember(ctx, ctx['server'].pid, soak.birth(ctx['server'].pid), 'coordinator', soak.process_table())
        deadline = time.monotonic() + 15
        while not (ctx['state'] / 'coordinator.sock').exists():
            if ctx['server'].poll() is not None or time.monotonic() > deadline:
                raise RuntimeError('diagnostic Coordinator not ready')
            time.sleep(.05)
        owner_request = work / 'owner.json'
        soak.atomic(owner_request, {'controller_thread': 'resource-memory', 'origin_pid': os.getpid(), 'origin_birth': soak.birth(os.getpid())})
        owner = soak.call(ctx, 'owner-bind', '--state-dir', str(ctx['state']), '--request', str(owner_request))
        stats_paths = [work / (task + '.stats.json') for task in soak.TASKS]
        receipts = [work / (task + '.receipts.jsonl') for task in soak.TASKS]
        start_path = work / 'shared-start.json'
        tasks = []
        for task, stats, receipt in zip(soak.TASKS, stats_paths, receipts):
            argv = [str(ctx['worker']), '--id', task, '--seconds', str(PLAN['worker_seconds']),
                '--phase-seconds', str(PLAN['phase_seconds']), '--bytes-per-second', str(PLAN['bytes_per_second']),
                '--giant-line-bytes', str(soak.THRESHOLDS['giant_line_bytes']), '--giant-line-every', str(PLAN['giant_every']),
                '--critical-every', str(PLAN['report_every']), '--stats-file', str(stats), '--start-file', str(start_path), '--receipt-file', str(receipt)]
            tasks.append({'id': task, 'max_attempts': 1, 'max_active_ms': (PLAN['worker_seconds'] + 60) * 1000,
                          'completion_policy': 'owner_review', 'adapter': {'kind': 'fake', 'args': argv, 'directory': str(work)}})
        request = {key: value for key, value in owner.items() if key not in ('version', 'owner_capability')}
        request.update(run_id='memory-' + uuid.uuid4().hex, plan_revision=1, owner_mode='native', delivery_mode='collect', tasks=tasks)
        request_path = work / 'submit.json'
        soak.atomic(request_path, request)
        submitted = soak.call(ctx, 'submit', '--state-dir', str(ctx['state']), '--request', str(request_path))
        ctx['control'] = submitted['control_file']
        db = soak.connect(ctx)
        soak.establish(ctx, db, stats_paths)
        summary['initial_metrics'] = verify_memory(ctx)  # Fail immediately, not after20m, if any role is silent.
        start_ns = time.time_ns() + 500_000_000
        soak.atomic(start_path, {'unix_ns': start_ns})
        samples = []
        with os.fdopen(os.open(args.out / 'samples.jsonl', os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), 'w') as raw:
            for tick in range(PLAN['duration_seconds']):
                remaining = (start_ns + tick * 1_000_000_000 - time.time_ns()) / 1e9
                if remaining > 0:
                    time.sleep(remaining)
                rows = soak.process_table()
                stats = [soak.load(path) for path in stats_paths]
                deadline_ns = start_ns + PLAN['worker_seconds'] * 1_000_000_000
                if time.time_ns() < deadline_ns:
                    soak.validate_running(ctx, soak.task_states(db), rows)
                    if any(s['status'] not in ('waiting', 'running') for s in stats):
                        raise RuntimeError('diagnostic worker exited before its frozen duration')
                else:
                    soak.validate_pending_tail(ctx, db, stats, rows, deadline_ns, PLAN['worker_seconds'], PLAN['report_every'])
                sample = soak.product_sample(ctx, stats, rows)
                sample.update(tick=tick, unix_nano=time.time_ns())
                samples.append(sample)
                raw.write(json.dumps(sample, separators=(',', ':')) + '\n')
                raw.flush()
                for kind, due in (('system', tick % 10 == 0), ('vmmap', tick in (0, 300, 600, 1100))):
                    if not due:
                        continue
                    previous = observers[kind]
                    if previous is not None:
                        if not previous.done():
                            raise RuntimeError(kind + ' observer exceeded its bounded sampling interval')
                        previous.result()
                    observers[kind] = pool.submit(observe_system, ctx, tick, kind == 'vmmap')
        remaining = (start_ns + PLAN['duration_seconds'] * 1_000_000_000 - time.time_ns()) / 1e9
        if remaining > 0:
            time.sleep(remaining)
        soak.validate_pending_tail(ctx, db, [soak.load(p) for p in stats_paths], soak.process_table(),
            start_ns + PLAN['worker_seconds'] * 1_000_000_000, PLAN['worker_seconds'], PLAN['report_every'])
        soak.atomic(args.out / 'pending-tail.json', ctx['tail_baseline'])
        soak.finish_tasks(ctx, db)
        emissions = soak.verify_emissions(ctx, stats_paths, receipts, PLAN['worker_seconds'], PLAN['phase_seconds'],
            PLAN['bytes_per_second'], PLAN['giant_every'], PLAN['report_every'])
        allowance = sum(s['helper_max_rss_bytes'] for s in emissions)
        summary.update(samples=len(samples), final_helper_allowance_bytes=allowance,
                       peak_charged_rss_mib=max(s['observed_product_rss_kib'] * 1024 + allowance for s in samples) / 1048576,
                       result='completed')
    except BaseException as error:
        summary['error'] = repr(error)
    finally:
        for kind, observer in observers.items():
            try:
                if observer is not None:
                    observer.result(timeout=15)
            except BaseException as error:
                summary.setdefault('observer_errors', {})[kind] = repr(error)
                summary['result'] = 'failed'
        pool.shutdown(wait=True)
        if db is not None:
            try:
                db.close()
            except BaseException as error:
                summary.update(result='failed', database_error=repr(error))
        try:
            summary['cleanup'] = soak.cleanup(ctx, summary['result'] != 'completed')
            c = summary['cleanup']
            if c['remaining'] or c['unregistered_private_processes'] or c.get('forced_coordinator_kill') or c.get('coordinator_original_wait') != 0 or c['identity_signals']:
                summary['result'] = 'failed'
        except BaseException as error:
            summary.update(result='failed', cleanup_error=repr(error))
        try:
            summary['final_metrics'] = verify_memory(ctx)
        except BaseException as error:
            summary.update(result='failed', metrics_error=repr(error))
        for path in sorted(trace.glob('*.jsonl')):
            soak.atomic(args.out / 'metrics' / path.name, path.read_bytes())
        summary['pins_end'] = {key: soak.pin(path) for key, path in copies.items()}
        summary['source_hashes_end'] = {key: sha(path) for key, path in sources.items()}
        if summary['pins_end'] != pins or summary['source_hashes_end'] != frozen['source_sha256']:
            summary.update(result='failed', pin_error='source/copy drift')
        summary['finished_unix_ns'] = time.time_ns()
        soak.atomic(args.out / 'summary.json', summary)
    print(json.dumps({'result': summary['result'], 'acceptance_verdict': 'not_applicable', 'error': summary.get('error')}))
    return 0 if summary['result'] == 'completed' else 1


if __name__ == '__main__':
    raise SystemExit(main())
