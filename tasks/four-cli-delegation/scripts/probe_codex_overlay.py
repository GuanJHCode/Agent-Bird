"""Non-generating experiment: private runtime home, original inputs read-only.

Not a production admission source. Prints only protocol/config booleans and keeps
all native stdout/stderr in private memory. No thread/turn methods are sent.
"""
import hashlib
import json
import os
from pathlib import Path
import selectors
import signal
import subprocess
import tempfile
import time

binary = Path.home() / '.local/bin/codex'
binary = binary.resolve(strict=True)
assert hashlib.sha256(binary.read_bytes()).hexdigest() == '4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc'
source = Path(os.environ.get('CODEX_HOME', str(Path.home()/'.codex'))).resolve(strict=True)
work = Path(tempfile.mkdtemp(prefix='bird-codex-overlay-', dir='/private/tmp'))
home = work/'home'
home.mkdir(mode=0o700)
runtime = work/'runtime'
runtime.mkdir(mode=0o700)
for name in ['tmp', 'sqlite', 'log']:
 (runtime/name).mkdir(mode=0o700)
# Deliberately do not copy or link sessions/history/databases/memories.
for name in ['config.toml', 'auth.json', 'AGENTS.md', 'rules', 'requirements.toml']:
 if (source/name).exists(): (home/name).symlink_to(source/name)
(home/'tmp').symlink_to(runtime/'tmp')
(home/'installation_id').touch(mode=0o600)
q=json.dumps
rules=['(version 1)','(allow default)','(deny file-write*)','(allow file-write* (literal "/dev/null"))',f'(allow file-write* (subpath {q(str(runtime))}))',f'(allow file-write* (literal {q(str(home/"installation_id"))}))']
flags=['--disable','multi_agent','--disable','hooks','--disable','plugins','--disable','apps','--disable','shell_snapshot','--disable','memories','-c','agents.enabled=false','-c','notify=[]','-c','sqlite_home='+q(str(runtime/'sqlite')),'-c','log_dir='+q(str(runtime/'log'))]
env=dict(os.environ)
assert 'CODEX_REFRESH_TOKEN_URL_OVERRIDE' not in env
env.update(CODEX_HOME=str(home),TMPDIR=str(runtime/'tmp'),CODEX_REFRESH_TOKEN_URL_OVERRIDE='invalid://refresh-blocked')
proc=subprocess.Popen(['/usr/bin/sandbox-exec','-p','\n'.join(rules),str(binary),*flags,'app-server','--listen','stdio://'],cwd=str(work),env=env,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,start_new_session=True)
sel=selectors.DefaultSelector()
sel.register(proc.stdout,selectors.EVENT_READ)
sel.register(proc.stderr,selectors.EVENT_READ)
buf=b''
def call(number,method,params):
 global buf
 proc.stdin.write(json.dumps({'id':number,'method':method,'params':params}).encode()+b'\n');proc.stdin.flush()
 until=time.monotonic()+15
 while time.monotonic()<until:
  for key,_ in sel.select(.2):
   chunk=os.read(key.fd,65536)
   if not chunk:
    sel.unregister(key.fileobj)
    if key.fileobj==proc.stdout: raise RuntimeError('native_protocol_closed')
    continue
   if key.fileobj==proc.stderr: continue
   buf+=chunk
   assert len(buf)<1048576
   while b'\n' in buf:
    line,buf=buf.split(b'\n',1)
    msg=json.loads(line)
    if msg.get('id')==number:
     if 'error' in msg:raise RuntimeError('native_rpc_rejected')
     return msg['result']
 raise RuntimeError('native_rpc_timeout')
result={'work':str(work),'model_called':False,'production_admission':False}
try:
 init=call(1,'initialize',{'clientInfo':{'name':'bird-overlay-probe','version':'1'},'capabilities':{'experimentalApi':True}})
 proc.stdin.write(b'{"method":"initialized","params":{}}\n');proc.stdin.flush()
 cfg=call(2,'config/read',{'includeLayers':False,'cwd':str(work)})['config']
 result.update(initialized=True,home_is_overlay=init.get('codexHome')==str(home),agents_disabled=cfg.get('agents',{}).get('enabled') is False,store_is_file=cfg.get('cli_auth_credentials_store')=='file')
except Exception as e: result['error']=type(e).__name__+':'+str(e)
finally:
 proc.stdin.close()
 try:proc.wait(timeout=2)
 except subprocess.TimeoutExpired:
  os.killpg(proc.pid,signal.SIGTERM)
  try:proc.wait(timeout=2)
  except subprocess.TimeoutExpired:os.killpg(proc.pid,signal.SIGKILL);proc.wait(timeout=2)
 result['exit']=proc.returncode
 for label, auth_home in [('source', source), ('overlay', home)]:
  auth_env=dict(env, CODEX_HOME=str(auth_home))
  if any(k in auth_env for k in ['CODEX_API_KEY','OPENAI_API_KEY','CODEX_ACCESS_TOKEN','OPENAI_FEDERATION_RULE_ID','OPENAI_IDENTITY_TOKEN_FILE']):
   result[label+'_login']='environment_auth_unverified'
   continue
  status=subprocess.Popen(['/usr/bin/sandbox-exec','-p','\n'.join(rules),str(binary),'login','status'],env=auth_env,cwd=str(work),stdout=subprocess.PIPE,stderr=subprocess.PIPE,start_new_session=True)
  try:
   out,err=status.communicate(timeout=10)
   lines=(out+b'\n'+err).decode(errors='replace').splitlines()
   result[label+'_login']='managed_chatgpt' if status.returncode==0 and lines.count('Logged in using ChatGPT')==1 else 'unverified'
  except subprocess.TimeoutExpired:
   os.killpg(status.pid,signal.SIGKILL);status.communicate(timeout=2)
   result[label+'_login']='timeout' 
 for name in ['config.toml', 'auth.json', 'AGENTS.md', 'rules', 'requirements.toml']:
  link=home/name
  if link.is_symlink(): link.unlink()
 result['source_links_detached']=True
 (work/'summary.json').write_text(json.dumps(result)+'\n');(work/'summary.json').chmod(0o600)
 print(json.dumps(result))
