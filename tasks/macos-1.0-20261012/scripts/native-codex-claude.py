#!/usr/bin/env python3
"""One real Codex initiator -> Agent Bird MCP -> Claude task; no config install."""
import json
import argparse
import os
from pathlib import Path
import subprocess
import tempfile
import shutil

parser=argparse.ArgumentParser();parser.add_argument('--edit',action='store_true');parser.add_argument('--label',default='');parser.add_argument('--target',choices=['claude','grok'],default='claude');options=parser.parse_args()
stem='codex-claude-edit' if options.edit else 'codex-claude'
stem = stem.replace('claude', options.target) + options.label
root = Path(__file__).resolve().parents[3]
base = root / 'tasks/macos-1.0-20261012/tmp/native-claude'
binary = root / 'tasks/macos-1.0-20261012/tmp/agent-bird-native3'
evidence = root / 'tasks/macos-1.0-20261012/data'
state = Path(tempfile.mkdtemp(prefix='agent-bird-macos1-', dir='/private/tmp'))
(state / 'project').mkdir(mode=0o700)
shutil.copyfile(base / 'project/README.md', state / 'project/README.md')
(evidence / 'native-runtime-path.txt').write_text(str(state) + '\n')
project = state / 'project'
locks = state / 'locks'
locks.mkdir(mode=0o700)
pin=locks/(options.target+'.json');pin.write_text(json.dumps(json.loads((evidence/(options.target+'-probe.json')).read_text())['lock']));pin.chmod(0o600)
package_root = root / 'tasks/macos-1.0-20261012/tmp/package-native2/plugin'
installed = subprocess.run([str(binary), 'plugin-run', '--plugin-root', str(package_root), '--install-root', str(state / 'packages'), '--state-dir', str(state / 'state'), 'check'], check=True, capture_output=True, text=True)
binary = Path(json.loads(installed.stdout)['binary'])
state = state / 'state'
if options.edit:
    (project/'answer.txt').write_text('before\n')
    subprocess.run(['git','init','-q',str(project)],check=True)
    subprocess.run(['git','-C',str(project),'add','README.md','answer.txt'],check=True)
    subprocess.run(['git','-C',str(project),'-c','user.name=Agent Bird Acceptance','-c','user.email=acceptance@example.invalid','commit','-qm','fixture'],check=True)
args = [str(binary), 'controller', 'start', '--provider', 'codex', '--state-dir', str(state),
        '--skill-path', str(root / 'plugins/codex-orchestrator/skills/orchestrate/SKILL.md'), '--',
        'exec', '--skip-git-repo-check', '--sandbox', 'read-only', '--json', '-C', str(project),
        '-c', 'mcp_servers.agent_bird.command=' + json.dumps(str(binary)),
        '-c', 'mcp_servers.agent_bird.args=' + json.dumps(['mcp', '--state-dir', str(state), '--provider-locks', str(locks)]),
        '-c', 'mcp_servers.agent_bird.tool_timeout_sec=120',
        '-c', 'mcp_servers.agent_bird.env_vars=["AGENT_BIRD_OWNER_REQUEST"]',
        '-c', 'mcp_servers.agent_bird.tools.agent_bird_call.approval_mode="approve"',
        'Use the agent_bird_call MCP tool exactly once with provider=claude, directory=' + str(project) + ', '
        'task="Read README.md and return its answer marker. Do not modify files or delegate.", timeout_ms=60000. '
        'Return the actual tool result. Do not use shell commands, other tools, or delegate again.']
if options.edit:
    args[-1]='Use agent_bird_call exactly once with provider=claude, directory='+str(project)+', mode=workspace-write, paths=["answer.txt"], timeout_ms=90000, task="Change answer.txt to exactly CLAUDE_EDIT_1012 followed by a newline. Use only Write to change the file, then Read once to confirm. Do not use Bash or any other validation tools. If a tool fails do not retry; report it and finish immediately. Do not delegate." Return the actual tool result. Do not use other tools or shell commands.'
args[-1]=args[-1].replace('provider=claude','provider='+options.target)
if options.target=='grok':
    args[-1]=args[-1].replace('CLAUDE_EDIT_1012','GROK_EDIT_1012').replace('Use only Write to change the file, then Read once to confirm.','Use file editing tools to change the file, then read it once to confirm.')
(evidence / (stem+'-argv.json')).write_text(json.dumps(args, indent=2))
env = os.environ.copy()
env['TMPDIR'] = str(root / 'tasks/macos-1.0-20261012/tmp')
with (evidence / (stem+'-source.jsonl')).open('xb') as stdout, (evidence / (stem+'-source.stderr')).open('xb') as stderr:
    child = subprocess.Popen(args, cwd=project, env=env, stdin=subprocess.DEVNULL, stdout=stdout, stderr=stderr)
    try:
        code = child.wait(timeout=150)
    except subprocess.TimeoutExpired:
        child.terminate()
        try:
            code = child.wait(timeout=15)
        except subprocess.TimeoutExpired:
            child.kill()
            code = child.wait(timeout=10)
        print('source_timeout', flush=True)
    print(json.dumps({'source_exit_code': code}), flush=True)
