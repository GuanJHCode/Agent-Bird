#!/usr/bin/env python3
"""One fixed source crossbuild and exact-CID Linux run; no production edits."""
import hashlib,json,os,pathlib,stat,subprocess,sys
os.umask(0o077)
root=pathlib.Path(__file__).resolve().parents[3]
task=root/'tasks/owner-grant-linux';data=task/'data';tmp=task/'tmp'
go='/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go'
source='e50bf47e9e3099b964d81f9c5af5a85d44f0995d'
def git(*args):return subprocess.check_output(['git',*args],cwd=root,text=True).strip()
def sha(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def run(label,command,env=None):
 paths={s:data/(label+'.'+s)for s in ['stdout','stderr','exit','command.json']}
 if any(p.exists()for p in paths.values()):raise SystemExit('refuse to overwrite '+label)
 paths['command.json'].write_text(json.dumps({'command':command,'cwd':str(root),'head':git('rev-parse','HEAD'),'dirty':git('status','--short'),'environment':{k:env[k]for k in ['TMPDIR','GOOS','GOARCH','CGO_ENABLED','GOPROXY','GOSUMDB','GOENV','GOWORK','GOTOOLCHAIN']if env and k in env}},indent=2)+'\n')
 with paths['stdout'].open('xb')as out,paths['stderr'].open('xb')as err:r=subprocess.run(command,cwd=root,env=env,stdout=out,stderr=err)
 paths['exit'].write_text(str(r.returncode)+'\n');return r.returncode
if git('status','--short'):raise SystemExit('require clean runner/source snapshot')
if git('rev-parse','HEAD:tools/orchestrator')!=git('rev-parse',source+':tools/orchestrator'):raise SystemExit('source tree changed')
files=git('ls-files','tools/orchestrator').splitlines()
provenance={'source':source,'source_tree':git('rev-parse',source+':tools/orchestrator'),'runner_head':git('rev-parse','HEAD'),'go':go,'go_version':subprocess.check_output([go,'version'],text=True).strip(),'file_sha256':{s:sha(root/s)for s in files if (root/s).is_file()}}
receipt=data/'source.json'
if receipt.exists():raise SystemExit('refuse to overwrite source receipt')
receipt.write_text(json.dumps(provenance,indent=2)+'\n')
bindir=tmp/'linux-bin';bindir.mkdir(mode=0o755);os.chmod(bindir,0o755);binary=bindir/'coordinator.test'
env=os.environ.copy();env.update(TMPDIR='/private/tmp',GOOS='linux',GOARCH='arm64',CGO_ENABLED='0',GOPROXY='off',GOSUMDB='off',GOENV='off',GOWORK='off',GOTOOLCHAIN='local')
code=run('crossbuild',[go,'-C','tools/orchestrator','test','-c','-o',str(binary),'./internal/coordinator'],env)
if code:raise SystemExit(code)
os.chmod(binary,0o555)
s=binary.lstat();identity={'path':str(binary),'dev':s.st_dev,'inode':s.st_ino,'uid':s.st_uid,'nlink':s.st_nlink,'mode':oct(stat.S_IMODE(s.st_mode)),'size':s.st_size,'sha256':sha(binary)}
(data/'binary-before.json').write_text(json.dumps(identity,indent=2)+'\n')
code=run('linux-run',[sys.executable,str(task/'scripts/run-linux.py'),'owner-grant',str(binary),'owner-grant',str(data/'expected-tests.json')])
if code:raise SystemExit(code)
container=json.loads((data/'linux/owner-grant-receipt.json').read_text())
if not container.get('accepted')or container['state']['Running']or container['state']['ExitCode']!=0 or container['state']['OOMKilled']:raise SystemExit('unproven original container terminal')
code=run('second-absence',['docker','inspect',container['cid']])
if code==0 or 'No such'not in(data/'second-absence.stderr').read_text():raise SystemExit('second exact CID absence failed')
s=binary.lstat();actual={'path':str(binary),'dev':s.st_dev,'inode':s.st_ino,'uid':s.st_uid,'nlink':s.st_nlink,'mode':oct(stat.S_IMODE(s.st_mode)),'size':s.st_size,'sha256':sha(binary)}
if actual!=identity or s.st_uid!=os.getuid()or s.st_nlink!=1 or not stat.S_ISREG(s.st_mode):raise SystemExit('binary identity drift; preserve')
binary.unlink()
cleanup={'original_attach_exit':container['start_exit'],'original_state':container['state'],'exact_cid':container['cid'],'runner_finally_absent':container['removed'],'second_absent':True,'binary_before':identity,'binary_removed':not binary.exists(),'binary_directory_retained':str(bindir)}
(data/'cleanup.json').write_text(json.dumps(cleanup,indent=2)+'\n')
print(json.dumps({'source':source,'accepted':container['accepted'],'pass':len(container['pass_tests']),'fail':len(container['fail_tests']),'skip':len(container['skip_tests']),'log_sha256':container['log_sha256'],'cid_absent_twice':True,'binary_removed':True}))
