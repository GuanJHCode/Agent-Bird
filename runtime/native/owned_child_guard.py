"""Per-child parent-death guard. No tree discovery, credential access or restart.

The guardian is the real child's parent and sole reaper. Signals target its
Popen child, never a PID supplied in a message. An unreaped child cannot have
its PID reused. The controller sees the actual child's PID, not the guardian.
"""
from __future__ import annotations

import argparse
import asyncio
import json
import os
from pathlib import Path
import select
import signal
import socket
import stat
import subprocess
import sys
import time
import uuid

MAX_MESSAGE=65536
STOP_SECONDS=9.0


class GuardianLostError(OSError):
    pass


def _private_dir(path):
    path=Path(path)
    info=path.lstat()
    if (not path.is_absolute() or path.resolve()!=path or not stat.S_ISDIR(info.st_mode)
        or info.st_uid!=os.getuid() or info.st_mode&0o077):
        raise ValueError('guardian receipt directory must be private and canonical')
    return info.st_dev,info.st_ino,info.st_uid,info.st_mode


def _directory_fd(path):
    expected=_private_dir(path)
    fd=os.open(path,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
    try:
        if _fd_identity(fd)!=expected: raise GuardianLostError('guardian directory changed before open')
        return fd
    except BaseException:
        os.close(fd);raise


def _fd_identity(fd):
    info=os.fstat(fd)
    if not stat.S_ISDIR(info.st_mode) or info.st_uid!=os.getuid() or info.st_mode&0o077:
        raise GuardianLostError('guardian directory identity unsafe')
    return info.st_dev,info.st_ino,info.st_uid,info.st_mode


def _write(fd,name,value):
    _fd_identity(fd)
    if name not in ('guardian-start.json','guardian-final.json'): raise ValueError('invalid receipt name')
    data=json.dumps(value,sort_keys=True,separators=(',',':')).encode()+b'\n'
    if len(data)>MAX_MESSAGE: raise ValueError('guardian receipt too large')
    target=os.open(name,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600,dir_fd=fd)
    with os.fdopen(target,'wb') as stream:
        stream.write(data);stream.flush();os.fsync(stream.fileno())
        info=os.fstat(stream.fileno())
        if info.st_nlink!=1 or info.st_uid!=os.getuid() or info.st_mode&0o077:
            raise GuardianLostError('guardian receipt changed while writing')
    os.fsync(fd)


def _read(fd,name):
    _fd_identity(fd)
    target=os.open(name,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK,dir_fd=fd)
    with os.fdopen(target,'rb') as stream:
        first=os.fstat(stream.fileno())
        if not stat.S_ISREG(first.st_mode) or first.st_uid!=os.getuid() or first.st_mode&0o077 or first.st_size>MAX_MESSAGE or first.st_nlink!=1:
            raise GuardianLostError('unsafe guardian receipt')
        data=stream.read(MAX_MESSAGE+1);last=os.fstat(stream.fileno())
    fields=('st_dev','st_ino','st_uid','st_mode','st_size','st_nlink','st_mtime_ns','st_ctime_ns')
    if len(data)!=first.st_size or any(getattr(first,k)!=getattr(last,k) for k in fields):
        raise GuardianLostError('guardian receipt changed')
    value=json.loads(data)
    if not isinstance(value,dict): raise GuardianLostError('invalid guardian receipt')
    return value


def _send(channel,value):
    data=json.dumps(value,separators=(',',':')).encode()+b'\n'
    if len(data)>MAX_MESSAGE: raise ValueError('guardian message too large')
    channel.sendall(data)


def _receive(channel):
    data=bytearray()
    while len(data)<=MAX_MESSAGE:
        chunk=channel.recv(1)
        if not chunk: raise EOFError('controller closed')
        if chunk==b'\n': return json.loads(data)
        data.extend(chunk)
    raise ValueError('guardian message too large')


def _birth(pid):
    try:
        result=subprocess.run(['/bin/ps','-p',str(pid),'-o','lstart='],capture_output=True,text=True,timeout=1)
    except (OSError,subprocess.SubprocessError):
        return None
    return result.stdout.strip() if result.returncode==0 else None


def _stop(child,force=False):
    facts={'term_requested':False,'kill_requested':False}
    if child.poll() is None:
        if not force:
            child.terminate();facts['term_requested']=True
            try: child.wait(timeout=1.0)
            except subprocess.TimeoutExpired: pass
        if child.poll() is None:
            child.kill();facts['kill_requested']=True
            try: child.wait(timeout=STOP_SECONDS-1.0)
            except subprocess.TimeoutExpired: pass
    facts.update(child_reaped=child.returncode is not None,child_returncode=child.returncode)
    return facts


def guardian_main(fd,receipt_fd,owner_pid):
    identity=_fd_identity(receipt_fd)
    os.set_inheritable(receipt_fd,False)
    channel=socket.socket(fileno=fd);channel.settimeout(10)
    os.set_inheritable(fd,False)
    child=None;initial={};reason='guardian_error';failed=False;force=False
    try:
        request=_receive(channel)
        if (os.getppid()!=owner_pid or request.get('owner_pid')!=owner_pid
            or not isinstance(request.get('nonce'),str) or len(request['nonce'])!=32):
            raise ValueError('guardian owner mismatch')
        argv=request['argv'];env=request['env'];cwd=request['cwd']
        if not argv or any(not isinstance(x,str) or not x for x in argv) or not os.path.isabs(argv[0]):
            raise ValueError('invalid child argv')
        if not isinstance(env,dict) or any(not isinstance(k,str) or not isinstance(v,str) for k,v in env.items()):
            raise ValueError('invalid child environment')
        child=subprocess.Popen(argv,cwd=cwd,env=env,close_fds=True,start_new_session=True)
        initial={'version':1,'nonce':request['nonce'],'owner_pid':owner_pid,'guardian_pid':os.getpid(),
            'child_pid':child.pid,'child_birth':_birth(child.pid),'uid':os.getuid(),
            'tree_stop_unproven':True,'restart_allowed':False}
        if _fd_identity(receipt_fd)!=identity: raise ValueError('guardian directory changed')
        initial['state']='running' if isinstance(initial['child_birth'],str) and initial['child_birth'] else 'unknown'
        _write(receipt_fd,'guardian-start.json',initial)
        _send(channel,initial)
        channel.settimeout(1)
        if initial['state']=='unknown':
            reason='child_identity_unknown';failed=True
        while not failed and child.poll() is None:
            readable,_,_=select.select([channel],[],[],.05)
            if not readable: continue
            try: command=_receive(channel)
            except EOFError:
                reason='controller_eof';break
            if (command.get('nonce')!=initial['nonce'] or command.get('child_pid')!=child.pid
                or command.get('action') not in ('terminate','kill')):
                reason='invalid_control_message';failed=True;break
            reason='controller_stop';force=command['action']=='kill';break
        else:
            if not failed: reason='child_exit'
    except EOFError:
        reason='controller_eof'
    except BaseException:
        reason='guardian_error';failed=True
    finally:
        outcome=_stop(child,force) if child is not None else {'child_reaped':True,'child_returncode':None,'term_requested':False,'kill_requested':False}
        if initial and _fd_identity(receipt_fd)==identity:
            _write(receipt_fd,'guardian-final.json',dict(initial,**outcome,reason=reason,
                state='stopped' if outcome['child_reaped'] and not failed else 'unknown'))
        channel.close();os.close(receipt_fd)
    return 0 if initial and outcome['child_reaped'] and not failed else 1


class GuardedChild:
    """Async Process-shaped handle, with stop decisions executed by its parent."""
    def __init__(self,supervisor,channel,directory,receipt_fd,initial):
        self._supervisor=supervisor;self._channel=channel;self._directory=Path(directory);self._initial=initial
        self._receipt_fd=receipt_fd;self._outcome_cache=None
        self.identity_known=initial.get('state')=='running' and bool(initial.get('child_birth'))
        self.pid=initial['child_pid'];self.guardian_pid=supervisor.pid
        self.stdout=supervisor.stdout;self.stderr=supervisor.stderr

    @classmethod
    async def spawn(cls,argv,*,cwd,env,receipt_dir,**stdio):
        directory=Path(receipt_dir);receipt_fd=_directory_fd(directory)
        parent,child=socket.socketpair();parent.settimeout(10)
        nonce=uuid.uuid4().hex;supervisor=None
        task=asyncio.create_task(asyncio.create_subprocess_exec(sys.executable,'-I','-B',str(Path(__file__).resolve()),
            '--guardian-fd',str(child.fileno()),'--receipt-fd',str(receipt_fd),'--owner-pid',str(os.getpid()),
            pass_fds=(child.fileno(),receipt_fd),env={'PATH':'/usr/bin:/bin','LANG':'C'},start_new_session=True,umask=0o077,**stdio))
        try:
            try: supervisor=await asyncio.shield(task)
            except asyncio.CancelledError:
                supervisor=await task;raise
            child.close()
            await asyncio.to_thread(_send,parent,{'owner_pid':os.getpid(),'nonce':nonce,'argv':list(argv),'cwd':str(cwd),'env':dict(env)})
            initial=await asyncio.to_thread(_receive,parent)
            if (initial.get('nonce')!=nonce or initial.get('owner_pid')!=os.getpid()
                or initial.get('guardian_pid')!=supervisor.pid or type(initial.get('child_pid')) is not int
                or initial['child_pid']<=0 or initial['child_pid'] in (supervisor.pid,os.getpid())
                or initial.get('uid')!=os.getuid() or initial.get('state') not in ('running','unknown')
                or (initial.get('state')=='running' and not isinstance(initial.get('child_birth'),str))):
                raise GuardianLostError('guardian launch identity mismatch')
            parent.settimeout(1)
            return cls(supervisor,parent,directory,receipt_fd,initial)
        except BaseException as exc:
            parent.close();os.close(receipt_fd)
            if supervisor is not None:
                end=time.monotonic()+10
                while supervisor.returncode is None and time.monotonic()<end:
                    await asyncio.sleep(.01)
            if isinstance(exc,EOFError):
                raise GuardianLostError('guardian launch identity unavailable') from exc
            raise
        finally: child.close()

    def outcome(self):
        if self._outcome_cache is not None: return dict(self._outcome_cache)
        try:
            value=_read(self._receipt_fd,'guardian-final.json')
            if any(value.get(k)!=self._initial[k] for k in ('nonce','owner_pid','guardian_pid','child_pid','child_birth','uid')):
                raise GuardianLostError('guardian final identity mismatch')
            try: location_unchanged=_private_dir(self._directory)==_fd_identity(self._receipt_fd)
            except (OSError,ValueError): location_unchanged=False
            value['receipt_location_unchanged']=location_unchanged
            if not location_unchanged: value['state']='unknown'
            return value
        except (OSError,ValueError):
            return dict(self._initial,state='unknown',reason='guardian_result_unavailable',child_reaped=False,
                child_returncode=None,tree_stop_unproven=True,restart_allowed=False)

    @property
    def returncode(self):
        if self._supervisor.returncode is None: return None
        outcome=self.outcome()
        if not outcome['child_reaped']: raise GuardianLostError('guardian exited without child reaping evidence')
        return outcome['child_returncode']

    def _command(self,action):
        if self._supervisor.returncode is not None:
            self.returncode
            return
        _send(self._channel,{'nonce':self._initial['nonce'],'child_pid':self.pid,'action':action})

    def terminate(self): self._command('terminate')
    def kill(self): self._command('kill')

    async def wait(self):
        # asyncio Process.wait can wait for inherited output pipes even after
        # the guardian died. Its returncode is set by the child watcher first.
        while self._supervisor.returncode is None:
            await asyncio.sleep(.01)
        return self.returncode

    async def stop(self,timeout=10):
        try:
            self.terminate()
            await asyncio.wait_for(self.wait(),max(0,timeout))
        except (OSError,TimeoutError): pass
        finally: self._channel.close()
        result=self.outcome()
        if self._receipt_fd is not None: os.close(self._receipt_fd);self._receipt_fd=None
        self._outcome_cache=result
        return dict(result)


if __name__=='__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('--guardian-fd',type=int,required=True)
    parser.add_argument('--receipt-fd',type=int,required=True)
    parser.add_argument('--owner-pid',type=int,required=True)
    args=parser.parse_args()
    raise SystemExit(guardian_main(args.guardian_fd,args.receipt_fd,args.owner_pid))
