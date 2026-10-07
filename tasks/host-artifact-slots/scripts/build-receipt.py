#!/usr/bin/env python3
"""Summarize terminal ROOT evidence without treating failed runs as passing."""
import collections
import hashlib
import json
import pathlib
import subprocess

root = pathlib.Path(__file__).resolve().parents[3]
data = root / 'tasks/host-artifact-slots/data'
selected = {
    'store-contract-full-v3': {'store': (399,1132,0), 'contract': (39,268,0)},
    'host-full-v3': {'host': (401,1548,16)},
    'host-coordinator-ipc-full-v2': {'coordinator': (84,176,0), 'ipc': (9,21,0)},
    'store-contract-race-v1': {'store': (19,50,0), 'contract': (4,19,0)},
    'native-host-cli-race-v1': {'host': (10,19,0), 'orchestrator': (3,3,0)},
}
receipt = {'base': subprocess.check_output(['git','rev-parse','HEAD'],cwd=root,text=True).strip(),
           'scope': 'Host artifact dispatch authority; physical writers/full M3 remain open',
           'runs': {}, 'linux': {}, 'source_sha256': {},
           'static_checks': {'go_build': 0, 'go_vet': 0, 'git_diff_check': 0},
           'review': 'Independent production, history, fixtures and document review PASS; ROOT owns final tests',
           'worker_fixture_commit': '1837b8471b1cea7d128cd30bcf12fe3c1f5ec34f'}
for label, expected in selected.items():
    path = data / (label + '.jsonl')
    counts = collections.defaultdict(collections.Counter)
    terminal, skipped, failed = {}, collections.defaultdict(list), collections.defaultdict(list)
    for line in path.read_text().splitlines():
        row = json.loads(line)
        package, action, test = row['Package'].rsplit('/',1)[-1], row['Action'], row.get('Test')
        if not test and action in ('pass','fail'): terminal[package] = {'action':action,'elapsed':row.get('Elapsed')}
        if test and action in ('pass','fail','skip'):
            counts[package][action+'_named'] += 1
            if '/' not in test: counts[package][action+'_top'] += 1
            if action == 'skip': skipped[package].append(test)
            if action == 'fail': failed[package].append(test)
    for package, (top,named,skip) in expected.items():
        c = counts[package]
        assert terminal[package]['action']=='pass', (label, package, terminal)
        assert (c['pass_top'],c['pass_named'],c['skip_named'],c['fail_named']) == (top,named,skip,0), (label,package,c)
    receipt['runs'][label] = {'selected_packages': list(expected), 'terminal': terminal,
        'counts': counts, 'skipped_tests': skipped, 'failed_tests':failed,
        'log_sha256':hashlib.sha256(path.read_bytes()).hexdigest()}
for path in sorted((data/'linux').glob('*-receipt.json')):
    row = json.loads(path.read_text())
    assert not row['state']['Running'] and not row['state']['OOMKilled']
    assert row['remove_exit']==0 and row['post_remove_inspect_exit']!=0
    check = subprocess.run(['docker','inspect',row['cid']],capture_output=True,text=True)
    assert check.returncode!=0 and 'No such object' in check.stderr, row['cid']
    receipt['linux'][row['label']] = {'receipt_sha256':hashlib.sha256(path.read_bytes()).hexdigest(),
        'cid':row['cid'], 'exit':row['state']['ExitCode'], 'pass_top':sum('/' not in name for name in row['pass_tests']),
        'pass_named':len(row['pass_tests']), 'fail_named':len(row['fail_tests']), 'skip_named':len(row['skip_tests']),
        'binary_sha256':row['binary_sha256'],'root_rechecked_absent':True}
assert receipt['linux']['store-v1']['exit']==0 and receipt['linux']['contract-v2']['exit']==0
names = subprocess.check_output(['git','diff','--name-only','HEAD'],cwd=root,text=True).splitlines()
names += subprocess.check_output(['git','ls-files','--others','--exclude-standard'],cwd=root,text=True).splitlines()
names += ['tools/orchestrator/go.mod','tools/orchestrator/go.sum']
for name in sorted(set(names)):
    if name.endswith('.go') or name in ('tools/orchestrator/go.mod','tools/orchestrator/go.sum'):
        receipt['source_sha256'][name] = hashlib.sha256((root/name).read_bytes()).hexdigest()
output = data/'root-final-receipt.json'
assert not output.exists(), 'refuse to replace evidence'
output.write_text(json.dumps(receipt,ensure_ascii=False,indent=2)+'\n')
print(json.dumps({'source_files':len(receipt['source_sha256']),'receipt_sha256':hashlib.sha256(output.read_bytes()).hexdigest()}))
