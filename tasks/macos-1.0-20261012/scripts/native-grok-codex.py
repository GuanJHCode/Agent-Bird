#!/usr/bin/env python3
"""Real Grok -> project MCP -> fixed Codex app-server read task."""
import json,os,subprocess,tempfile
from pathlib import Path
root=Path(__file__).resolve().parents[3]
evidence=root/'tasks/macos-1.0-20261012/data'
fixture=Path(tempfile.mkdtemp(prefix='agent-bird-grok-codex-',dir='/private/tmp'))
project,locks,state=(fixture/n for n in ('project','locks','state'))
for p in (project,locks,state):p.mkdir(mode=0o700)
(project/'README.md').write_text('The acceptance marker is BIRD-CODEX-1012.\n')
bootstrap=root/'tasks/macos-1.0-20261012/tmp/agent-bird-native3'
source=root/'tasks/macos-1.0-20261012/tmp/package-native2/plugin'
r=subprocess.run([str(bootstrap),'plugin-run','--plugin-root',str(source),'--install-root',str(fixture/'packages'),'--state-dir',str(state),'check'],check=True,capture_output=True,text=True)
binary=Path(json.loads(r.stdout)['binary'])
p=locks/'codex.json';p.write_text(json.dumps(json.loads((evidence/'codex-probe.json').read_text())['lock']));p.chmod(0o600)
(project/'.grok').mkdir(mode=0o700)
config='[mcp_servers.agent_bird]\ncommand = '+json.dumps(str(binary))+'\nargs = '+json.dumps(['mcp','--state-dir',str(state),'--provider-locks',str(locks)])+'\ncwd = '+json.dumps(str(project))+'\nenabled = true\ntool_timeout_sec = 120\n'
p=project/'.grok/config.toml';p.write_text(config);p.chmod(0o600)
prompt='Use the agent_bird__agent_bird_call MCP tool exactly once with provider=codex, directory='+str(project)+', task="Read README.md with agent_bird.read_file and return its acceptance marker. Do not modify files or delegate.", timeout_ms=90000. Return the actual tool result. Do not use other tools or delegate again.'
args=[str(binary),'controller','start','--provider','grok','--state-dir',str(state),'--skill-path',str(root/'plugins/codex-orchestrator/skills/orchestrate/SKILL.md'),'--','-p',prompt,'--output-format','streaming-json','--cwd',str(project),'--no-subagents','--disable-web-search','--allow','MCPTool(agent_bird__agent_bird_call)','--permission-mode','plan']
(evidence/'grok-codex-argv.json').write_text(json.dumps(args,indent=2));(evidence/'grok-codex-fixture.txt').write_text(str(fixture)+'\n')
env=os.environ.copy();env['TMPDIR']=str(root/'tasks/macos-1.0-20261012/tmp')
with (evidence/'grok-codex-source.jsonl').open('wb') as out,(evidence/'grok-codex-source.stderr').open('wb') as err:
 child=subprocess.Popen(args,cwd=project,env=env,stdin=subprocess.DEVNULL,stdout=out,stderr=err)
 try:code=child.wait(timeout=150)
 except subprocess.TimeoutExpired:
  child.terminate()
  try:code=child.wait(timeout=15)
  except subprocess.TimeoutExpired:child.kill();code=child.wait(timeout=10)
  print('source_timeout',flush=True)
 print(json.dumps({'source_exit_code':code}),flush=True)
