import json
from pathlib import Path
import subprocess
import sys

import pytest

SCRIPT = Path(__file__).resolve().parents[1] / 'scripts/finish-live.py'


@pytest.mark.parametrize('mode,reason', [
    ('wall', 'direction_wall_exceeded'),
    ('fresh', 'parent_postflight_changed'),
    ('empty_host', 'host_closure_missing'),
])
def test_optimized_python_cannot_bypass_closure(tmp_path, mode, reason):
    # Public fixtures only. Fake commands cannot execute Git, CLI or models.
    code = r'''
import hashlib,json,runpy,sys,types
from pathlib import Path
script,root,mode=sys.argv[1:];root=Path(root)
evidence=root/'validation-public.json';evidence.write_text('PUBLIC')
post={'status':'POSTFLIGHT_PASS','integrated':False,'validation_evidence_sha256':hashlib.sha256(evidence.read_bytes()).hexdigest(),'candidate_oid':'oid','tree_oid':'tree'}
calls=[];writes=[]
def require(ok,reason):
 if not ok:raise ValueError(reason)
def read(path):
 if path.name=='setup.json':return {'pins':[],'version':'PUBLIC','identity_sha256':'PUBLIC'}
 if path.name=='scenario-result.json':return {'wall_seconds':181 if mode=='wall' else 1,'postflight':post}
 return {'scratch_dir':str(root),'state_dir':str(root),'dispatch':{'directory':str(root)}}
def command(args,env):
 args=list(map(str,args));calls.append(args)
 if 'finish' in args:
  result=dict(post)
  if '--integrate-private' in args:result['integrated']=True
  elif mode=='fresh':result['candidate_oid']='different'
  return json.dumps(result).encode()
 if args[-1]=='HEAD':return b'oid'
 if args[-1]=='HEAD^{tree}':return b'tree'
 if args[-1]=='--porcelain':return b''
 raise RuntimeError('unexpected_command')
launcher=types.SimpleNamespace(require=require,check=lambda:None,verify_pins=lambda p:None,environment=lambda r:{},command=command,PY='PUBLIC',write=lambda p,v:writes.append(str(p)))
directions=('codex-to-claude','claude-to-codex')
sys.modules['final_trial']=launcher
sys.modules['live_sequence']=types.SimpleNamespace(DIRECTIONS=directions,read_json=read,validate_result=lambda r:r['postflight'])
for direction in directions:
 area=root/direction/'live-run';area.mkdir(parents=True);(area/'scenario-result.json').write_text('{}')
loaded=runpy.run_path(script);main=loaded['main'];main.__globals__['ROOT']=root
class DB:
 def __enter__(self):return self
 def __exit__(self,*args):pass
 def execute(self,query):
  rows=[('completed',1)] if 'FROM tasks ' in query else [('exited',1)] if 'FROM segments ' in query else [] if 'FROM runtime_hosts ' in query else [(90000,70000,'settled')]
  return types.SimpleNamespace(fetchall=lambda:rows)
main.__globals__['sqlite3']=types.SimpleNamespace(connect=lambda *a,**k:DB())
try:main()
except ValueError as error:reason=str(error)
else:reason='incorrect_success'
print(json.dumps({'reason':reason,'integrations':sum('--integrate-private' in a for a in calls),'final_pass_written':any(p.endswith('final-result.json') for p in writes)}))
'''
    result = subprocess.run([sys.executable, '-O', '-B', '-c', code, str(SCRIPT), str(tmp_path), mode],
                            capture_output=True, text=True, check=True)
    observed = json.loads(result.stdout)
    assert observed['reason'] == reason
    assert observed['final_pass_written'] is False
    if mode != 'empty_host':
        assert observed['integrations'] == 0
