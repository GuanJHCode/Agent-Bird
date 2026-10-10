#!/usr/bin/env python3
"""Audit the terminal container and remove only this task's exact binary."""
import hashlib
import json
import os
import pathlib
import stat
import subprocess

root = pathlib.Path(__file__).resolve().parents[3]
task = root / 'tasks/git-filter-resource-linux'
data = task / 'data'
binary = task / 'tmp/linux-bin/store.test'


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def run(label, command):
    paths = {suffix: data / (label + '.' + suffix) for suffix in ('stdout', 'stderr', 'exit', 'command.json')}
    if any(path.exists() for path in paths.values()):
        raise SystemExit('refuse to overwrite ' + label)
    paths['command.json'].write_text(json.dumps({
        'command': command,
        'cwd': str(root),
        'head': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=root, text=True).strip(),
        'dirty': subprocess.check_output(['git', 'status', '--short'], cwd=root, text=True).strip(),
        'environment': {},
    }, indent=2) + '\n')
    with paths['stdout'].open('xb') as stdout, paths['stderr'].open('xb') as stderr:
        result = subprocess.run(command, cwd=root, stdout=stdout, stderr=stderr)
    paths['exit'].write_text(str(result.returncode) + '\n')
    return result.returncode


receipt = json.loads((data / 'linux/resource-final-receipt.json').read_text())
if not receipt.get('accepted') or receipt['state']['Running'] or receipt['state']['ExitCode'] != 0 or receipt['state']['OOMKilled']:
    raise SystemExit('unproven original container terminal')
second_exit = data / 'second-absence.exit'
second_stderr = data / 'second-absence.stderr'
if second_exit.exists() and second_stderr.exists():
    second_code = int(second_exit.read_text().strip())
else:
    second_code = run('second-absence', ['docker', 'inspect', receipt['cid']])
if second_code == 0:
    raise SystemExit('second exact CID absence failed')
if 'No such' not in second_stderr.read_text():
    raise SystemExit('second exact CID absence failed')
identity = json.loads((data / 'binary-before.json').read_text())
before = binary.lstat()
actual = {
    'path': str(binary), 'dev': before.st_dev, 'inode': before.st_ino, 'uid': before.st_uid,
    'nlink': before.st_nlink, 'mode': oct(stat.S_IMODE(before.st_mode)), 'size': before.st_size,
    'sha256': sha(binary),
}
if actual != identity or before.st_uid != os.getuid() or before.st_nlink != 1 or not stat.S_ISREG(before.st_mode):
    raise SystemExit('binary identity drift; preserve')
with binary.open('rb') as binary_file:
    os.fsync(binary_file.fileno())
directory_fd = os.open(binary.parent, os.O_RDONLY)
try:
    os.fsync(directory_fd)
    binary.unlink()
    os.fsync(directory_fd)
finally:
    os.close(directory_fd)
cleanup = {
    'original_attach_exit': receipt['start_exit'], 'original_state': receipt['state'], 'exact_cid': receipt['cid'],
    'runner_finally_absent': receipt['removed'], 'second_absent': True, 'binary_before': identity,
    'preunlink_file_fsync': True, 'preunlink_directory_fsync': True, 'postunlink_directory_fsync': True,
    'binary_removed': not binary.exists(), 'binary_directory_retained': str(binary.parent),
}
(data / 'cleanup.json').write_text(json.dumps(cleanup, indent=2) + '\n')
print(json.dumps({
    'accepted': receipt['accepted'], 'pass': len(receipt['pass_tests']), 'fail': len(receipt['fail_tests']),
    'skip': len(receipt['skip_tests']), 'log_sha256': receipt['log_sha256'], 'cid_absent_twice': True,
    'binary_removed': cleanup['binary_removed'],
}))
