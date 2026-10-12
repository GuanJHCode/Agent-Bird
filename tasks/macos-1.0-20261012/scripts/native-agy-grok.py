#!/usr/bin/env python3
"""Real AGY -> project MCP -> pinned Grok read task."""
import json,os,subprocess,tempfile
from pathlib import Path
root=Path(__file__).resolve().parents[3]
evidence=root/'tasks/macos-1.0-20261012/data'
fixture=Path(tempfile.mkdtemp(prefix='agent-bird-agy-grok-',dir='/private/tmp'))
project,locks,state=(fixture/n for n in ('project','locks','state'))
for p in (project,locks,state):p.mkdir(mode=0o700)
(project/'README.md').write_text('The acceptance marker is BIRD-GROK-1012.\n')
bootstrap=root/'tasks/macos-1.0-20261012/tmp/agent-bird-native3'
source=root/'tasks/macos-1.0-20261012/tmp/package-native2/plugin'
r=subprocess.run([str(bootstrap),'plugin-run','--plugin-root',str(source),'--install-root',str(fixture/'packages'),'--state-dir',str(state),'check'],check=True,capture_output=True,text=True)
binary=Path(json.loads(r.stdout)['binary'])
lock=locks/'grok.json';lock.write_text(json.dumps(json.loads((evidence/'grok-probe.json').read_text())['lock']));lock.chmod(0o600)
(project/'.agents').mkdir(mode=0o700)
config={'mcpServers':{'agent_bird':{'command':str(binary),'args':['mcp','--state-dir',str(state),'--provider-locks',str(locks)],'cwd':str(project)}}}
p=project/'.agents/mcp_config.json';p.write_text(json.dumps(config));p.chmod(0o600)
prompt='Use the Agent Bird MCP agent_bird_call tool exactly once with provider=grok, directory='+str(project)+', task="Read README.md and return its acceptance marker. Do not modify files or delegate.", timeout_ms=90000. Return the actual tool result. Do not use other tools or delegate again.'
args=[str(binary),'controller','start','--provider','agy','--state-dir',str(state),'--skill-path',str(root/'plugins/codex-orchestrator/skills/orchestrate/SKILL.md'),'--','--print=','--output-format','stream-json','--input-format','stream-json']
(evidence/'agy-grok-argv.json').write_text(json.dumps(args,indent=2));(evidence/'agy-grok-fixture.txt').write_text(str(fixture)+'\n')
env=os.environ.copy();env['TMPDIR']=str(root/'tasks/macos-1.0-20261012/tmp')
with (evidence/'agy-grok-source.jsonl').open('wb') as out,(evidence/'agy-grok-source.stderr').open('wb') as err:
 child=subprocess.Popen(args,cwd=project,env=env,stdin=subprocess.PIPE,stdout=out,stderr=err)
 message={'event':'user','message':{'content':prompt}}
 child.stdin.write((json.dumps(message)+'\n').encode());child.stdin.close()
 try:code=child.wait(timeout=150)
 except subprocess.TimeoutExpired:
  child.terminate()
  try:code=child.wait(timeout=15)
  except subprocess.TimeoutExpired:child.kill();code=child.wait(timeout=10)
  print('source_timeout',flush=True)
 print(json.dumps({'source_exit_code':code}),flush=True)
