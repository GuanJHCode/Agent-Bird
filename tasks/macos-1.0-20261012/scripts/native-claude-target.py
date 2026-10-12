#!/usr/bin/env python3
"""Real Claude source -> Agent Bird MCP -> selected target in a private fixture."""
import argparse,json,os,subprocess,tempfile
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('target',choices=['codex','grok']);p.add_argument('--label',default='');opts=p.parse_args()
root=Path(__file__).resolve().parents[3]; evidence=root/'tasks/macos-1.0-20261012/data'
fixture=Path(tempfile.mkdtemp(prefix='agent-bird-claude-'+opts.target+'-',dir='/private/tmp'))
project,locks,state=(fixture/n for n in ('project','locks','state'))
for d in (project,locks,state):d.mkdir(mode=0o700)
(project/'README.md').write_text('The acceptance marker is BIRD-'+opts.target.upper()+'-1012.\n')
bootstrap=root/'tasks/macos-1.0-20261012/tmp/agent-bird-native4';package=root/'tasks/macos-1.0-20261012/tmp/package-native3/plugin'
r=subprocess.run([str(bootstrap),'plugin-run','--plugin-root',str(package),'--install-root',str(fixture/'packages'),'--state-dir',str(state),'check'],check=True,capture_output=True,text=True)
binary=Path(json.loads(r.stdout)['binary'])
pin=locks/(opts.target+'.json');pin.write_text(json.dumps(json.loads((evidence/(opts.target+'-probe.json')).read_text())['lock']));pin.chmod(0o600)
config=fixture/'mcp.json';config.write_text(json.dumps({'mcpServers':{'agent_bird':{'command':str(binary),'args':['mcp','--state-dir',str(state),'--provider-locks',str(locks)]}}}));config.chmod(0o600)
prompt='Use mcp__agent_bird__agent_bird_call exactly once with provider='+opts.target+', directory='+str(project)+', task="Read README.md and return its acceptance marker. Do not modify files or delegate.", timeout_ms=90000. Return the actual tool result. No other tools or shell commands.'
args=[str(binary),'controller','start','--provider','claude','--state-dir',str(state),'--skill-path',str(root/'plugins/codex-orchestrator/skills/orchestrate/SKILL.md'),'--','-p',prompt,'--output-format','stream-json','--verbose','--strict-mcp-config','--mcp-config',str(config),'--allowedTools','mcp__agent_bird__agent_bird_call']
stem='claude-'+opts.target+opts.label
(evidence/(stem+'-argv.json')).write_text(json.dumps(args,indent=2));(evidence/(stem+'-fixture.txt')).write_text(str(fixture)+'\n')
env=os.environ.copy();env['TMPDIR']=str(root/'tasks/macos-1.0-20261012/tmp')
with (evidence/(stem+'-source.jsonl')).open('xb') as out,(evidence/(stem+'-source.stderr')).open('xb') as err:
 child=subprocess.Popen(args,cwd=project,env=env,stdin=subprocess.DEVNULL,stdout=out,stderr=err)
 try:code=child.wait(timeout=240)
 except subprocess.TimeoutExpired:
  child.terminate()
  try:code=child.wait(timeout=15)
  except subprocess.TimeoutExpired:child.kill();code=child.wait(timeout=10)
  print('source_timeout',flush=True)
 print(json.dumps({'source_exit_code':code}),flush=True)
