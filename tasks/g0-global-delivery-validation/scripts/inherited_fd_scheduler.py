"""Private preflight scheduler: real inherited FD/process, never launchctl.

The in-memory job inventory is explicitly a simulation. It supports the actual
ActivationTransaction file lifecycle and the unchanged service --listener-fd
entry point. Backend native admission remains the root caller's responsibility.
"""
from __future__ import annotations
import hashlib,os,socket,subprocess,threading
from pathlib import Path
from proxy_transport import _process_metadata
from receipt_store import directory


class InheritedFdScheduler:
    def __init__(self,spec,now):
        if not spec.home.is_relative_to(Path('/private/tmp')) or not spec.socket_path.is_relative_to(spec.home):
            raise ValueError('inherited FD requires private activation home')
        self._home_identity=directory(spec.home);self._parent_identity=directory(spec.socket_path.parent)
        self._frozen_argv=tuple(spec.program_arguments);self._identity=None
        self.spec=spec;self.now=now;self.phase_end=None;self.process=None;self.listener=None;self._job=False
        self.digests={};self._threads=[];self.actual_argv=None
    def _remaining(self):
        if self.phase_end is None or self.phase_end<=self.now():raise TimeoutError('scheduler phase expired')
        return self.phase_end-self.now()
    def verify(self):
        if directory(self.spec.home)!=self._home_identity or directory(self.spec.socket_path.parent)!=self._parent_identity or tuple(self.spec.program_arguments)!=self._frozen_argv:
            raise ValueError('inherited scheduler identity changed')
    def _stop_created(self):
        if self.process is None:return True
        if self.process.poll() is None and self._identity is not None and all(self._identity):
            if _process_metadata(self.process.pid)!=self._identity:raise ValueError('service stop identity changed')
            self._remaining()
            try:self.process.terminate()
            except ProcessLookupError:pass
        self.process.wait(timeout=min(5,self._remaining()))
        return True
    def query(self,target):
        self._remaining()
        if not self._job:return 'absent',None
        return 'present',{'label':self.spec.label,'path':str(self.spec.plist_path),'program_arguments':list(self.spec.program_arguments),
            **({'pid':self.process.pid} if self.process.poll() is None else {})}
    def bootstrap(self,domain,path):
        from native_delivery_case import remove_public_socket
        from synthetic_native_transport import _socket_identity,_dir_identity
        self._remaining();self.verify()
        if self._job or self.process is not None:raise ValueError('one bootstrap only')
        self.listener=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM);socket_identity=None
        parent_identity=_dir_identity(self.spec.socket_path.parent)
        try:
            self.listener.bind(str(self.spec.socket_path));self.listener.listen(4);os.chmod(self.spec.socket_path,0o600)
            socket_identity=_socket_identity(self.spec.socket_path)
            self.actual_argv=(*self._frozen_argv,'--listener-fd',str(self.listener.fileno()))
            self._remaining()
            self.process=subprocess.Popen(self.actual_argv,pass_fds=(self.listener.fileno(),),stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE,stderr=subprocess.PIPE,env={'PATH':'/usr/bin:/bin','LANG':'C'})
            self._job=True;self._identity=_process_metadata(self.process.pid)
            if not all(self._identity):raise ValueError('service creation identity unavailable')
        except BaseException as exc:
            self.startup_failure={'failure_type':type(exc).__name__,'child_stopped':False}
            self.listener.close()
            try:
                self.startup_failure['child_stopped']=self._stop_created();self._job=False
                if self.process is not None:
                    self.process.stdout.close();self.process.stderr.close()
            except Exception:self.startup_failure['cleanup_unproven']=True
            if socket_identity is not None:
                try:remove_public_socket(self.spec.socket_path,socket_identity,parent_identity,self.startup_failure)
                except Exception:self.startup_failure['socket_cleanup_unproven']=True
            raise
        def digest(name,stream):
            value=hashlib.sha256();count=0
            while block:=stream.read(65536):count+=len(block);value.update(block)
            self.digests[name]={'bytes':count,'sha256':value.hexdigest()};stream.close()
        for name,stream in (('stdout',self.process.stdout),('stderr',self.process.stderr)):
            t=threading.Thread(target=digest,args=(name,stream),daemon=True);t.start();self._threads.append(t)
        t=threading.Thread(target=self.process.wait,daemon=True);t.start();self._threads.append(t)
        return 0
    def activate_successor(self,activation_id,final_sha256):
        """Private-only one successor after a fully stopped prior service."""
        from activation_service import _private_json,native_tui_policy,_original_process_gone
        from receipt_store import ReceiptStore
        self._remaining();self.verify()
        manifest,digest=_private_json(self.spec.manifest_path)
        if digest!=self.spec.manifest_sha256 or native_tui_policy(manifest.get('native_tui_policy')) is None:
            raise ValueError('successor is not in the frozen TUI policy')
        if getattr(self,'successor_started',False) or not self._job or self.process is None or self.process.poll() is None:
            raise ValueError('prior service is not stopped')
        if not _original_process_gone(self.process.pid,*self._identity):raise ValueError('prior service identity remains')
        store=ReceiptStore(Path(manifest['state_dir']),activation_id=activation_id)
        try:
            captured=store.read('activation.json')
            if captured is None or captured[1]!=final_sha256:raise ValueError('prior service final missing')
            final=captured[0];rows=final.get('backend_records',[])
            if final.get('manifest_sha256')!=digest or len(rows)!=1:raise ValueError('prior service final differs')
            for row in rows:
                if (row.get('state')!='closed' or row.get('process_stopped') is not True or row.get('socket_removed') is not True
                    or row.get('cleanup_failure') or row.get('guardian',{}).get('child_reaped') is not True
                    or not _original_process_gone(row['pid'],row['birth'],row['executable']) or os.path.lexists(row['private_socket'])):
                    raise ValueError('prior backend cleanup unproven')
        finally:store.close()
        self.previous_service={'pid':self.process.pid,'identity':self._identity,'final_sha256':final_sha256}
        self.successor_started=True;self._remaining()
        self.process=subprocess.Popen(self.actual_argv,pass_fds=(self.listener.fileno(),),stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,stderr=subprocess.PIPE,env={'PATH':'/usr/bin:/bin','LANG':'C'})
        self._identity=_process_metadata(self.process.pid)
        if not all(self._identity):raise ValueError('successor identity unavailable')
        def digest_stream(name,stream):
            value=hashlib.sha256();count=0
            while block:=stream.read(65536):value.update(block);count+=len(block)
            self.digests['successor_'+name]={'bytes':count,'sha256':value.hexdigest()};stream.close()
        for name,stream in (('stdout',self.process.stdout),('stderr',self.process.stderr)):
            thread=threading.Thread(target=digest_stream,args=(name,stream),daemon=True);thread.start();self._threads.append(thread)
        thread=threading.Thread(target=self.process.wait,daemon=True);thread.start();self._threads.append(thread)
        return self.process.pid

    def bootout(self,domain,path):
        self._remaining()
        if not self._job:return 0
        self.verify();self._stop_created()
        self.listener.close();self._job=False
        for thread in self._threads:thread.join(timeout=min(1,self._remaining()))
        return 0
