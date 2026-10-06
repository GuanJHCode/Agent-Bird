#!/usr/bin/env python3
import hashlib,json,pathlib,re,subprocess,time
root=pathlib.Path(__file__).resolve().parents[3]
task=root/'tasks/linux-group-native';out=task/'data';binary=task/'tmp/root-bin/process-linux-arm64.test';cidfile=out/'root-native.cid'
if cidfile.exists():raise SystemExit('refuse to overwrite original CID evidence')
names=['TestLinuxNativeGroupLeaderGoneMemberLive','TestLinuxNativeGroupZombieNeedsOriginalWait']
args=['/usr/local/bin/docker','run','--rm','--init','--cidfile',str(cidfile),'--network','none','--read-only','--cap-drop','ALL','--security-opt','no-new-privileges','--user','65534:65534','--pids-limit','64','--memory','256m','--cpus','1','--tmpfs','/tmp:rw,nosuid,nodev,size=128m','--mount','type=bind,src='+str(binary.parent)+',dst=/test,readonly','--entrypoint','/test/'+binary.name,'sha256:6ab0b6e7381779332f97b8ca76193e45b0756f38d4c0dcda72dbb3c32061ab99','-test.timeout=20s','-test.v','-test.run=^('+'|'.join(names)+')$']
start=time.monotonic();timed=False;cleanup=None
with (out/'root-native.log').open('wb') as log:
 try:r=subprocess.run(args,stdout=log,stderr=subprocess.STDOUT,timeout=30);exit_code=r.returncode
 except subprocess.TimeoutExpired:
  timed=True;exit_code=None
  cid=cidfile.read_text().strip() if cidfile.exists() else ''
  if re.fullmatch('[0-9a-f]{64}',cid):
   c=subprocess.run(['/usr/local/bin/docker','rm','-f',cid],capture_output=True,text=True,timeout=10);cleanup=dict(exit=c.returncode,output=c.stdout+c.stderr)
cid=cidfile.read_text().strip() if cidfile.exists() else ''
if not re.fullmatch('[0-9a-f]{64}',cid):raise SystemExit('no exact owned CID; retain failed evidence')
inspect=subprocess.run(['/usr/local/bin/docker','inspect',cid],capture_output=True,text=True,timeout=10)
raw=(out/'root-native.log').read_bytes();text=raw.decode();passed=re.findall(r'^--- PASS: (\S+)',text,re.M);failed=re.findall(r'^--- FAIL: (\S+)',text,re.M);skipped=re.findall(r'^--- SKIP: (\S+)',text,re.M)
receipt=dict(command=args,source_sha256=hashlib.sha256((root/'tools/orchestrator/internal/process/linux_group_native_test.go').read_bytes()).hexdigest(),binary_sha256=hashlib.sha256(binary.read_bytes()).hexdigest(),exit_code=exit_code,timed_out=timed,elapsed=time.monotonic()-start,cid=cid,post_inspect_exit=inspect.returncode,post_inspect_output=inspect.stdout+inspect.stderr,cleanup=cleanup,pass_tests=passed,fail_tests=failed,skip_tests=skipped,raw_sha256=hashlib.sha256(raw).hexdigest())
(out/'root-native-receipt.json').write_text(json.dumps(receipt,indent=2)+'\n');print(json.dumps(receipt))
if exit_code or timed or passed!=names or failed or skipped or inspect.returncode==0:raise SystemExit(1)
