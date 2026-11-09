"""Root acceptance: verify both genuine parent outcomes, then private fixture FF."""
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys

BASE = Path(__file__).resolve().parents[1]
TASK = BASE.parent
HARNESS = TASK / 'revisions/r8/scripts'
ROOT = Path('/private/tmp/ab8-1109-r8')
sys.path.insert(0, str(HARNESS))
import final_trial as launcher
import live_sequence

require = launcher.require


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    os.umask(0o077)
    launcher.check()
    setup = live_sequence.read_json(ROOT / 'setup.json')
    launcher.verify_pins(setup['pins'])
    env = launcher.environment(ROOT)
    rows = []
    # Both directions must be genuine closed passes before any FF operation.
    for direction in live_sequence.DIRECTIONS:
        area = ROOT / direction
        outcome = area / 'live-run/scenario-result.json'
        result = live_sequence.read_json(outcome)
        post = live_sequence.validate_result(result)
        require(result['wall_seconds'] <= 180, 'direction_wall_exceeded')
        contract = live_sequence.read_json(area / 'bound-contract.json')
        matches = [p for p in Path(contract['scratch_dir']).glob('validation-*.json')
                   if sha(p) == post['validation_evidence_sha256']]
        require(len(matches) == 1, 'validation_evidence_not_unique')
        args = [launcher.PY, '-B', HARNESS / 'scenario.py', 'finish', '--contract', area / 'bound-contract.json',
                '--validation-evidence', matches[0], '--validation-sha256', post['validation_evidence_sha256']]
        fresh = json.loads(launcher.command(args, env))
        require(fresh == post, 'parent_postflight_changed')
        rows.append(dict(direction=direction, area=area, contract=contract, args=args,
                         scenario=result, scenario_result_sha256=sha(outcome), evidence=matches[0]))
    require(sum(row['scenario']['wall_seconds'] for row in rows) <= 360, 'trial_wall_exceeded')
    for row in rows:
        final = json.loads(launcher.command([*row['args'], '--integrate-private'], env))
        require(final['status'] == 'POSTFLIGHT_PASS' and final['integrated'], 'private_ff_incomplete')
        repo = row['contract']['dispatch']['directory']
        head = launcher.command(['/usr/bin/git', '-C', repo, 'rev-parse', 'HEAD'], env).decode().strip()
        tree = launcher.command(['/usr/bin/git', '-C', repo, 'rev-parse', 'HEAD^{tree}'], env).decode().strip()
        status = launcher.command(['/usr/bin/git', '-C', repo, 'status', '--porcelain'], env).decode().strip()
        require(head == final['candidate_oid'] and tree == final['tree_oid'] and status == '', 'private_ff_git_postcondition')
        row['final'] = final
        launcher.write(row['area'] / 'final-postflight.json', final)
    report = {'status': 'BIDIRECTIONAL_LIVE_AND_PRIVATE_FF_PASS', 'version': setup['version'],
              'root': str(ROOT), 'identity_sha256': setup['identity_sha256'],
              'pins_verified': len(setup['pins']), 'source_credentials_persisted_by_closure': False,
              'total_scenario_wall_seconds': sum(r['scenario']['wall_seconds'] for r in rows), 'directions': []}
    for row in rows:
        state = Path(row['contract']['state_dir'])
        with sqlite3.connect((state / 'state.db').as_uri() + '?mode=ro', uri=True) as db:
            tasks = db.execute('SELECT status,count(*) FROM tasks GROUP BY status').fetchall()
            segments = db.execute('SELECT status,count(*) FROM segments GROUP BY status').fetchall()
            hosts = db.execute('SELECT status,count(*) FROM runtime_hosts GROUP BY status').fetchall()
            budgets = db.execute('SELECT granted_ms,used_ms,status FROM budget_runtime').fetchall()
            require(tasks == [('completed', 1)], 'task_not_closed')
            require(len(segments) == 1 and segments[0][0] == 'exited' and 1 <= segments[0][1] <= 2, 'segments_not_closed')
            require(hosts == [('offline', 1)], 'host_closure_missing')
            require(bool(budgets) and all(status == 'settled' and 0 <= used <= grant <= 90000 for grant, used, status in budgets), 'budget_not_settled')
            require(sum(used for _, used, _ in budgets) <= 90000, 'worker_total_exceeded')
        report['directions'].append({'direction': row['direction'], 'wall_seconds': row['scenario']['wall_seconds'],
            'scenario_result_sha256': row['scenario_result_sha256'],
            'validation_evidence': str(row['evidence']), 'postflight': row['final'],
            'tasks': tasks, 'segments': segments, 'hosts': hosts, 'budgets': budgets,
            'owned_cleanup': {name: row['scenario'][name] for name in
                ('remaining_owned_pids', 'unverified_owned_pids', 'discovery_errors', 'output_channels_closed')}})
    launcher.write(BASE / 'data/final-result.json', report)
    print(json.dumps({'status': report['status'], 'version': report['version'],
                      'total_scenario_wall_seconds': report['total_scenario_wall_seconds'],
                      'directions': [{'direction': r['direction'], 'used_ms': r['postflight']['used_ms'],
                                      'parent_accept': r['postflight']['parent_accept'],
                                      'transport_ack': r['postflight']['transport_ack'],
                                      'integrated': r['postflight']['integrated']} for r in report['directions']]}))


if __name__ == '__main__':
    main()
