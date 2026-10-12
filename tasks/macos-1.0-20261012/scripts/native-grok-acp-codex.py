#!/usr/bin/env python3
import json,os,subprocess,tempfile,sys
from pathlib import Path
root=Path(__file__).resolve().parents[3];evidence=root/'tasks/macos-1.0-20261012/data'
label=sys.argv[1] if len(sys.argv)>1 else ''
fixture=Path(tempfile.mkdtemp(prefix='bird-grok-acp-',dir='/private/tmp'))
project,locks,state=(fixture/n for n in ('project','locks','state'))
for d in (project,locks,state):d.mkdir(mode=0o700)
(project/'README.md').write_text('The acceptance marker is BIRD-CODEX-1012.\n')
(project/'answer.txt').write_text('before\n')
for argv in (['git','init','-q'],['git','add','.'],['git','-c','user.name=Fixture','-c','user.email=fixture@example.invalid','-c','commit.gpgsign=false','commit','-qm','fixture']):
 subprocess.run(argv,cwd=project,check=True,capture_output=True)
bootstrap=root/'tasks/macos-1.0-20261012/tmp/agent-bird-native9';package=root/'tasks/macos-1.0-20261012/tmp/package-native8/plugin'
r=subprocess.run([str(bootstrap),'plugin-run','--plugin-root',str(package),'--install-root',str(fixture/'packages'),'--state-dir',str(state),'check'],check=True,capture_output=True,text=True)
binary=json.loads(r.stdout)['binary']
p=locks/'codex.json';p.write_text(json.dumps(json.loads((evidence/'codex-probe.json').read_text())['lock']));p.chmod(0o600)
prompt='Use the agent_bird__agent_bird_call MCP tool exactly once with provider=codex, directory='+str(project)+', mode=workspace-write, paths=["answer.txt"], task="Read README.md and answer.txt using agent_bird.read_file. Change answer.txt to exactly CODEX_EDIT_1012 followed by a newline using agent_bird.write_file with the observed SHA256. Read it back using agent_bird.read_file and return the marker and changed file. Do not use shell commands or delegate.", timeout_ms=120000. Return the actual tool result including workspace, changes_known, changed_files, process_exited and failure_reason. Do not use other tools or delegate again.'

args=['python3',str(root/'tasks/macos-1.0-20261012/scripts/grok-plugin-acp.py'),'--binary',binary,'--state-dir',str(state),'--provider-locks',str(locks),'--directory',str(project),'--prompt',prompt,'--timeout-seconds','200']
(evidence/('grok-acp-codex'+label+'-argv.json')).write_text(json.dumps(args,indent=2));(evidence/('grok-acp-codex'+label+'-fixture.txt')).write_text(str(fixture)+'\n')
with (evidence/('grok-acp-codex'+label+'-source.json')).open('x') as out,(evidence/('grok-acp-codex'+label+'-source.stderr')).open('x') as err:
 r=subprocess.run(args,cwd=project,stdout=out,stderr=err,timeout=230)
 print(json.dumps({'source_adapter_exit_code':r.returncode}))
