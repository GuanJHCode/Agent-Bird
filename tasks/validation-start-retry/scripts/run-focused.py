#!/usr/bin/env python3
"""Retain fresh command stdout, stderr, exit and metadata without overwrites."""
import json, os, pathlib, subprocess, sys
root = pathlib.Path(__file__).resolve().parents[3]
name, *command = sys.argv[1:]
data = root / 'tasks/validation-start-retry/data'
paths = {suffix: data / f'{name}.{suffix}' for suffix in ('stdout.jsonl', 'stderr.txt', 'exit.txt', 'command.json')}
if any(p.exists() for p in paths.values()):
    raise SystemExit('refusing to overwrite existing evidence')
env = os.environ.copy()
env['TMPDIR'] = '/private/tmp'
env['PATH'] = '/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin:' + env['PATH']
metadata = {'command': command, 'cwd': str(root), 'head': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=root, text=True).strip(), 'dirty': subprocess.check_output(['git', 'status', '--short'], cwd=root, text=True), 'test_environment': {key: env[key] for key in ('NEW_BASE_NATIVE_RACE', 'GORACE') if key in env}, 'go_version': subprocess.check_output(['go', 'version'], env=env, text=True).strip()}
paths['command.json'].write_text(json.dumps(metadata, indent=2) + '\n')
with paths['stdout.jsonl'].open('x') as out, paths['stderr.txt'].open('x') as err:
    result = subprocess.run(command, cwd=root, env=env, stdout=out, stderr=err)
paths['exit.txt'].write_text(str(result.returncode) + '\n')
print(json.dumps({'name':name,'exit':result.returncode}))
raise SystemExit(result.returncode)
