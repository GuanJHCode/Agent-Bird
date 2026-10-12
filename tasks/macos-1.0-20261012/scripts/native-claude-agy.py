#!/usr/bin/env python3
"""Real Claude -> MCP -> AGY edit in a new private fixture."""
import json
import os
from pathlib import Path
import subprocess
import tempfile

root = Path(__file__).resolve().parents[3]
evidence = root / 'tasks/macos-1.0-20261012/data'
binary = Path(json.loads((evidence / 'codex-claude-argv.json').read_text())[0])
fixture = Path(tempfile.mkdtemp(prefix='agent-bird-agy-edit-', dir='/private/tmp'))
project, locks, state = (fixture / name for name in ('project', 'locks', 'state'))
for directory in (project, locks, state):
    directory.mkdir(mode=0o700)
(project / 'answer.txt').write_text('before\n')
subprocess.run(['git', 'init', '-q', str(project)], check=True)
subprocess.run(['git', '-C', str(project), 'add', 'answer.txt'], check=True)
subprocess.run(['git', '-C', str(project), '-c', 'user.name=Agent Bird Acceptance', '-c', 'user.email=acceptance@example.invalid', 'commit', '-qm', 'fixture'], check=True)
pin = json.loads((evidence / 'agy-probe.json').read_text())['lock']
path = locks / 'agy.json'
path.write_text(json.dumps(pin));path.chmod(0o600)
config = {'mcpServers': {'agent_bird': {'command': str(binary), 'args': ['mcp', '--state-dir', str(state), '--provider-locks', str(locks)]}}}
config_path = fixture / 'mcp.json'
config_path.write_text(json.dumps(config));config_path.chmod(0o600)
prompt = ('Use mcp__agent_bird__agent_bird_call exactly once. provider=agy, directory=' + str(project) + ', '
          'mode=workspace-write, paths=["answer.txt"], timeout_ms=90000, '
          'task="Change answer.txt to exactly AGY_EDIT_1012 followed by a newline. Read it back, describe the modification, and do not delegate." '
          'Return the actual tool result; no other tools or shell commands.')
args = [str(binary), 'controller', 'start', '--provider', 'claude', '--state-dir', str(state), '--skill-path',
        str(root / 'plugins/codex-orchestrator/skills/orchestrate/SKILL.md'), '--', '-p', prompt,
        '--output-format', 'stream-json', '--verbose', '--strict-mcp-config', '--mcp-config', str(config_path),
        '--allowedTools', 'mcp__agent_bird__agent_bird_call']
(evidence / 'claude-agy-argv.json').write_text(json.dumps(args, indent=2))
(evidence / 'claude-agy-fixture.txt').write_text(str(fixture)+'\n')
env=os.environ.copy();env['TMPDIR']=str(root/'tasks/macos-1.0-20261012/tmp')
with (evidence / 'claude-agy-source.jsonl').open('wb') as stdout, (evidence / 'claude-agy-source.stderr').open('wb') as stderr:
    child=subprocess.Popen(args,cwd=project,env=env,stdin=subprocess.DEVNULL,stdout=stdout,stderr=stderr)
    try:
        code=child.wait(timeout=150)
    except subprocess.TimeoutExpired:
        child.terminate()
        try: code=child.wait(timeout=15)
        except subprocess.TimeoutExpired:
            child.kill();code=child.wait(timeout=10)
        print('source_timeout',flush=True)
    print(json.dumps({'source_exit_code':code}),flush=True)
