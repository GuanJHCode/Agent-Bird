#!/usr/bin/env python3
"""Frozen F12 four-source-Host acceptance runner; static preparation only."""
import argparse,hashlib,json,os,pathlib,stat,sys
ROOT=pathlib.Path(__file__).resolve().parents[1];TH=json.loads((ROOT/'data/thresholds.json').read_text());CANDIDATE=pathlib.Path('/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/capability-closure-20261006/tmp/integration/tasks/resource-soak/tmp/native-build/candidate')
class Failure(RuntimeError):pass
def sha(p):
 with p.open('rb') as f:return hashlib.file_digest(f,'sha256').hexdigest()
def pin(p):
 s=p.stat(follow_symlinks=False);return {'dev':s.st_dev,'ino':s.st_ino,'mode':s.st_mode&511,'size':s.st_size,'sha256':sha(p)}
def private_copy(src,dst,want):
 fd=os.open(src,os.O_RDONLY|os.O_NOFOLLOW)
 try:
  b=os.fstat(fd);h=hashlib.sha256()
  with os.fdopen(os.open(dst,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o500),'wb') as o:
   while x:=os.read(fd,1048576):h.update(x);o.write(x)
   o.flush();os.fsync(o.fileno())
  a=os.fstat(fd)
  if (a.st_dev,a.st_ino,a.st_size,a.st_mtime_ns)!=(b.st_dev,b.st_ino,b.st_size,b.st_mtime_ns) or h.hexdigest()!=want:raise Failure('freeze drift')
 finally:os.close(fd)
def main():
 p=argparse.ArgumentParser();p.add_argument('--freeze-manifest',type=pathlib.Path,required=True);p.add_argument('--out',type=pathlib.Path,required=True);a=p.parse_args()
 if sys.platform!='darwin' or a.out.exists() or not a.out.is_absolute():raise SystemExit('Darwin/new absolute output required')
 f=json.loads(a.freeze_manifest.read_text());src={'candidate_sha256':CANDIDATE,'runner_sha256':pathlib.Path(__file__),'controller_sha256':ROOT/'scripts/controller.py','thresholds_sha256':ROOT/'data/thresholds.json'}
 if f.get('candidate_path')!=str(CANDIDATE) or f.get('thresholds')!=TH or any(f.get(k)!=sha(v) for k,v in src.items()):raise SystemExit('freeze mismatch')
 a.out.mkdir(mode=0o700);(a.out/'bin').mkdir(mode=0o700)
 for k,n in [('candidate_sha256','candidate'),('runner_sha256','runner.py'),('controller_sha256','controller.py'),('thresholds_sha256','thresholds.json')]:private_copy(src[k],a.out/'bin'/n,f[k])
 raise SystemExit('prepared only: run path requires ROOT-reviewed four-origin admission implementation')
if __name__=='__main__':main()
