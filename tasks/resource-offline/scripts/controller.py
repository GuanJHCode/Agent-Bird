#!/usr/bin/env python3
"""Owned origin that submits through the public CLI, then exits on stdin EOF."""
import argparse
import importlib.util
import os
import pathlib
import sys
import subprocess


def call(soak, ctx, *args):
    number = ctx['calls']
    ctx['calls'] += 1
    process = subprocess.Popen([str(ctx['candidate']), *args], env=ctx['env'], stdin=subprocess.DEVNULL,
                               stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    timed_out = False
    try:
        stdout, stderr = process.communicate(timeout=20)
    except subprocess.TimeoutExpired:
        timed_out = True
        process.kill()
        stdout, stderr = process.communicate(timeout=5)
    soak.atomic(ctx['raw'] / f'{number:04d}-{args[0]}.stdout', stdout)
    soak.atomic(ctx['raw'] / f'{number:04d}-{args[0]}.stderr', stderr)
    soak.atomic(ctx['raw'] / f'{number:04d}-{args[0]}.wait.json',
                {'argv': list(args), 'original_wait': process.returncode, 'timed_out': timed_out})
    if timed_out or process.returncode:
        raise RuntimeError('public CLI failed: ' + args[0])
    return __import__('json').loads(stdout)


def main():
    parser = argparse.ArgumentParser()
    for name in ('candidate', 'state', 'work', 'ready', 'helper', 'raw'):
        parser.add_argument('--' + name, type=pathlib.Path, required=True)
    parser.add_argument('--run-id', required=True)
    args = parser.parse_args()
    spec = importlib.util.spec_from_file_location('soak', args.helper)
    soak = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(soak)
    env = {'PATH': '/usr/bin:/bin:/usr/sbin:/sbin', 'LANG': 'C', 'LC_ALL': 'C',
           'TMPDIR': str(args.work), 'ORCHESTRATOR_ENABLE_TEST_FAKE': '1'}
    ctx = {'candidate': args.candidate, 'raw': args.raw, 'calls': 0, 'env': env}
    identity = {'origin_pid': os.getpid(), 'origin_birth': subprocess.run(['/bin/ps', '-o', 'lstart=', '-p', str(os.getpid())], capture_output=True, text=True, check=True, timeout=2).stdout.strip()}
    if not identity['origin_birth']:
        raise RuntimeError('origin birth unknown')
    owner_request = args.work / 'owner.json'
    soak.atomic(owner_request, {'controller_thread': 'resource-offline', **identity})
    owner = call(soak, ctx, 'owner-bind', '--state-dir', str(args.state), '--request', str(owner_request))
    request = {k: v for k, v in owner.items() if k not in ('version', 'owner_capability')}
    request.update(run_id=args.run_id, plan_revision=1, owner_mode='native', delivery_mode='collect', tasks=[{
        'id': 'offline-pending', 'max_attempts': 1, 'max_active_ms': 30000, 'completion_policy': 'owner_review',
        'adapter': {'kind': 'fake', 'args': ['/bin/sh', '-c', 'while [ ! -f "$1" ]; do /bin/sleep 0.02; done; printf offline-pending', 'offline-worker', str(args.work / 'worker-release')], 'directory': str(args.work)}}])
    submit = args.work / 'submit.json'
    soak.atomic(submit, request)
    reply = call(soak, ctx, 'submit', '--state-dir', str(args.state), '--request', str(submit))
    soak.atomic(args.ready, {'run_id': args.run_id, 'task_id': 'offline-pending',
                            'control_file': reply['control_file'], **identity})
    sys.stdin.buffer.read(1)


if __name__ == '__main__':
    main()
