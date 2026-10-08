#!/usr/bin/env python3
"""One owned origin; retain each public CLI's original wait4 and rusage."""
import argparse
import importlib.util
import json
import os
import pathlib
import signal
import stat
import subprocess
import sys
import time


def main():
    parser = argparse.ArgumentParser()
    for name in ('candidate', 'state', 'work', 'ready', 'helper', 'raw', 'worker'):
        parser.add_argument('--' + name, type=pathlib.Path, required=True)
    for name in ('run-id', 'task-id', 'label'):
        parser.add_argument('--' + name, required=True)
    parser.add_argument('--fifth', action='store_true')
    args = parser.parse_args()
    spec = importlib.util.spec_from_file_location('soak', args.helper)
    soak = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(soak)
    env = {'PATH': '/usr/bin:/bin:/usr/sbin:/sbin', 'LANG': 'C', 'LC_ALL': 'C',
           'TMPDIR': str(args.work), 'ORCHESTRATOR_ENABLE_TEST_FAKE': '1'}
    call_number = 0
    children = {}

    def birth(pid):
        return subprocess.run(['/bin/ps', '-o', 'lstart=', '-p', str(pid)],
            capture_output=True, text=True, check=True, timeout=2).stdout.strip()

    def spawn(argv, stopped=False):
        nonlocal call_number
        prefix = args.raw / f'{call_number:04d}-{argv[0]}'
        call_number += 1
        out = os.open(str(prefix) + '.stdout', os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        err = os.open(str(prefix) + '.stderr', os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        command = [str(args.candidate), *argv]
        try:
            if stopped:
                pid = os.fork()
                if pid == 0:
                    try:
                        os.dup2(out, 1)
                        os.dup2(err, 2)
                        os.close(out)
                        os.close(err)
                        os.kill(os.getpid(), signal.SIGSTOP)
                        os.execve(command[0], command, env)
                    except BaseException:
                        os._exit(127)
                process = None
            else:
                process = subprocess.Popen(command, env=env, stdin=subprocess.DEVNULL, stdout=out, stderr=err)
                pid = process.pid
        finally:
            os.close(out)
            os.close(err)
        children[pid] = {'process': process, 'prefix': prefix, 'argv': command}
        if stopped:
            until = time.monotonic() + 5
            while True:
                got, status = os.waitpid(pid, os.WUNTRACED | os.WNOHANG)
                if got:
                    if not os.WIFSTOPPED(status) or os.WSTOPSIG(status) != signal.SIGSTOP:
                        raise RuntimeError('pre-exec helper failed to stop')
                    break
                if time.monotonic() > until:
                    raise RuntimeError('pre-exec stop timeout')
                time.sleep(.01)
        return pid

    def join(pid, timeout=25):
        child = children[pid]
        until = time.monotonic() + timeout
        forced = False
        while True:
            got, status, usage = os.wait4(pid, os.WNOHANG)
            if got:
                break
            if time.monotonic() > until:
                forced = True
                os.kill(pid, signal.SIGKILL)
                got, status, usage = os.wait4(pid, 0)
                break
            time.sleep(.01)
        code = os.waitstatus_to_exitcode(status)
        if child['process'] is not None:
            child['process'].returncode = code  # Original wait4 above is the sole terminal waiter.
        del children[pid]
        prefix = child['prefix']
        evidence = {'pid': pid, 'argv': child['argv'], 'original_wait': code,
                    'wait_status': status, 'forced': forced, 'max_rss_bytes': usage.ru_maxrss,
                    'cpu_seconds': usage.ru_utime + usage.ru_stime,
                    'stdout': pathlib.Path(str(prefix) + '.stdout').read_text(),
                    'stderr': pathlib.Path(str(prefix) + '.stderr').read_text()}
        soak.atomic(pathlib.Path(str(prefix) + '.wait.json'), evidence)
        if forced:
            raise RuntimeError('public CLI timeout required SIGKILL')
        return evidence

    def call(*argv):
        result = join(spawn(list(argv)))
        if result['original_wait'] != 0:
            raise RuntimeError('public CLI failed: ' + repr(result))
        return json.loads(result['stdout'])

    try:
        identity = {'origin_pid': os.getpid(), 'origin_birth': birth(os.getpid())}
        if not identity['origin_birth']:
            raise RuntimeError('unknown origin identity')
        owner_request = args.work / 'owner.json'
        soak.atomic(owner_request, {'controller_thread': 'capacity-' + args.label, **identity})
        owner = call('owner-bind', '--state-dir', str(args.state), '--request', str(owner_request))
        capability = pathlib.Path(owner['owner_capability'])
        for path, mode in ((capability, 0o600), (capability.parent, 0o700)):
            info = path.lstat()
            if info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != mode or stat.S_ISLNK(info.st_mode):
                raise RuntimeError('owner capability is not private')
        # Local capability is preserved as evidence, not presented as a native
        # bridge capability. This controlled fake path explicitly uses TEST_FAKE.
        request = {key: owner[key] for key in ('controller_thread', 'origin_pid', 'origin_birth', 'origin_context_id', 'host_generation')}
        request.update(run_id=args.run_id, plan_revision=1, owner_mode='native', delivery_mode='collect', tasks=[{
            'id': args.task_id, 'max_attempts': 1, 'max_active_ms': 120000, 'completion_policy': 'owner_review',
            'execution_policy': {'version': 1, 'group_max_attempts': 1, 'group_max_active_ms': 120000,
                                 'task_max_active_ms': 120000, 'segment_max_active_ms': 120000},
            'adapter': {'kind': 'fake', 'args': [sys.executable, str(args.worker), str(args.work / 'worker-release')],
                        'directory': str(args.work)}}])
        submit = args.work / 'submit.json'
        soak.atomic(submit, request)
        common = {'run_id': args.run_id, 'task_id': args.task_id, **identity}
        argv = ['submit', '--state-dir', str(args.state), '--request', str(submit)]
        if args.fifth:
            pid = spawn(argv, stopped=True)
            soak.atomic(args.ready, {**common, 'helper_pid': pid, 'helper_birth': birth(pid)})
            if sys.stdin.buffer.readline() != b'go\n':
                raise RuntimeError('missing pre-exec observer release')
            os.kill(pid, signal.SIGCONT)
            evidence = join(pid)
            soak.atomic(args.work / 'submitted.json', evidence)
        else:
            reply = call(*argv)
            if reply.get('status') != 'queued' or not reply.get('control_file'):
                raise RuntimeError('public submit did not queue')
            soak.atomic(args.ready, {**common, 'control_file': reply['control_file']})
        sys.stdin.buffer.read()
    finally:
        for pid in list(children):
            try:
                os.kill(pid, signal.SIGKILL)
                result = join(pid, 1)
                soak.atomic(args.work / f'forced-child-{pid}.json', result)
            except BaseException as error:
                soak.atomic(args.work / f'cleanup-error-{pid}.json', {'error': repr(error)})


if __name__ == '__main__':
    main()
