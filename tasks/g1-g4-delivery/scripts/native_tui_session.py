"""One owned pre-exec native PTY session, reusing the reviewed auth probe."""
from __future__ import annotations
# The sandboxed pre-exec branch intentionally imports only os/sys. Importing
# the controller's ctypes metadata stack would request denied uname sysctls.
import os,sys
if __name__=='__main__':
    if len(sys.argv)<5 or sys.argv[1]!='--gate-child' or sys.argv[4]!='--':raise SystemExit(91)
    fd=int(sys.argv[2])
    try:release=os.read(fd,1)
    finally:os.close(fd)
    if release!=b'1':raise SystemExit(91)
    os.execv(sys.argv[3],[sys.argv[3],*sys.argv[5:]])
    raise SystemExit(92)
import asyncio,hashlib,os,re,signal,sys,uuid
from pathlib import Path
ROOT=Path(__file__).resolve().parents[3]
for path in (ROOT/'tasks/g0-auth-preserving-activation/scripts',ROOT/'tasks/g0-global-delivery-validation/scripts',ROOT/'tasks/g0-tui-proxy/scripts'):
    sys.path.insert(0,str(path))
import native_activation_probe as probe
import proxy_native_runtime as pty_runtime
import global_delivery_case as g


class TuiSession:
    def __init__(self,plan,prepared):
        self.plan=plan;self.prepared=prepared;self.driver=None;self.peer=None;self.grant=None;self.policy_file=None;self.artifacts=[];self.record={}

    def start(self,end,*,resume_thread_id=None,resume_proof=None):
        g.frozen(self.plan,self.prepared);g.remaining(end)
        context=self.prepared.context
        if resume_thread_id is not None:resume_thread_id=pty_runtime.canonical_resume_thread_id(resume_thread_id)
        argv=[] if resume_thread_id is None else ['resume',resume_thread_id]
        self.record={'argv':[str(context.expected_executable),*argv],'started_raw':g.now(),'deadline_raw':end,'resumed':resume_thread_id is not None}
        read_fd,write_fd=os.pipe();master=slave=None
        try:
            os.set_inheritable(read_fd,True)
            master,slave,slave_path=probe._open_controlling_pty()
            self.policy_file=context.spec.task_root/f'tui-{uuid.uuid4().hex[:12]}.sbpl'
            data=probe.render_sandbox_profile(context.spec,slave_path).encode()
            identity=probe._write_exclusive(self.policy_file,data);self.artifacts.append((self.policy_file,identity))
            gate=['/usr/bin/sandbox-exec','-f',str(self.policy_file),str(self.plan.python),'-I','-B',str(Path(__file__).resolve()),
                '--gate-child',str(read_fd),str(context.expected_executable),'--',*argv]
            env=probe.build_clean_environment(context.spec);env['TERM']='xterm-256color'
            self.record.update(policy_sha256=hashlib.sha256(data).hexdigest(),environment_sha256=hashlib.sha256(g.base.transport.encoded(env)).hexdigest())
            g.remaining(end)
            pid,master=probe._spawn_pty_gate(gate,env,context.spec.workspace,master,slave);slave=None
            self.driver=probe.PTYDriver(pid,master);master=None
            birth,gate_exe=probe._process_identity(pid)
            self.peer={'pid':pid,'uid':os.getuid(),'birth':birth,'executable':str(context.expected_executable),'executable_sha256':context.expected_executable_sha256}
            self.grant=self.plan.grants_dir/f'{pid}.json'
            grant={'version':1,'pid':pid,'uid':os.getuid(),'birth':birth,'expected_executable':str(context.expected_executable),
                'executable_sha256':context.expected_executable_sha256,'profile_id':context.spec.profile_id}
            if resume_thread_id is not None:grant.update(expected_resume_thread_id=resume_thread_id,resume_owner_proof=resume_proof)
            identity=probe._write_exclusive(self.grant,g.base.transport.encoded(grant));self.artifacts.append((self.grant,identity))
            self.record.update(frontend=self.peer,grant_sha256=g.sha(self.grant),grant_before_release=True,gate_executable=gate_exe)
            g.frozen(self.plan,self.prepared);g.remaining(end)
            os.write(write_fd,b'1');self.record['released_raw']=g.now()
        finally:
            for fd in (read_fd,write_fd,master,slave):
                if fd is not None:
                    try:os.close(fd)
                    except OSError:pass
        return self

    def drain(self):
        if self.driver is not None:self.driver.read_available()

    async def until(self,predicate,end):
        while True:
            g.remaining(end);self.drain()
            if predicate():return
            g.require(self.driver.poll() is None,'native TUI exited before milestone')
            await asyncio.sleep(.01)

    async def ready(self,store,service_peer,end,*,expected_owner_epoch=1):
        found=None
        while found is None:
            g.remaining(end);self.drain()
            names=[n for n in store.children() if re.fullmatch(r'owner-connected-[0-9a-f]{12}-'+str(self.peer['pid'])+r'\.json',n)]
            g.require(len(names)<=1,'multiple TUI owner connections')
            if names:found=store.read(names[0])
            if found is None:
                g.require(self.driver.poll() is None,'TUI exited before accepted connection')
                await asyncio.sleep(.01)
        row,digest=found;g.common(row,store,self.plan)
        g.require(set(row)==g.CONNECTED_KEYS and row['grant_sha256']==self.record['grant_sha256'],'TUI connected grant mismatch')
        g.check_peer(row['frontend_peer'],self.peer);native=g.check_connected_backend(row,self.prepared,service_peer);g.check_peer(row['backend_peer'],native)
        self.record['connected_sha256']=digest;lease_id=row['lease_id']
        while True:
            g.remaining(end);self.drain();found=store.read(f'ready-{lease_id}.json')
            if found is not None:break
            g.require(self.driver.poll() is None,'TUI exited before ready');await asyncio.sleep(.01)
        ready,ready_sha=found;g.common(ready,store,self.plan)
        g.require(ready.get('ready_published') is True and ready.get('zero_turns') is True and ready.get('initialized') is True,'TUI initial ready invalid')
        g.check_peer(ready['frontend'],self.peer);lease,native=g.validate_lease(ready['owner_lease'],self.prepared,service_peer,expected_owner_epoch=expected_owner_epoch);g.check_peer(ready['backend'],native)
        g.require(all(lease[k]==row[k] for k in g.CONNECTED_LEASE_KEYS),'TUI lease changed between connected/ready')
        g.require(g.peer_matches(g.base.peer_for_process(self.peer['pid']),self.peer),'TUI executable changed')
        self.record.update(ready_sha256=ready_sha,thread_id=lease['owner_thread_id'],lease_id=lease_id)
        return lease,ready_sha,ready

    async def status(self,thread_id,end):
        self.drain();g.remaining(end);self.driver.command('/status')
        await self.until(lambda:bool(self.driver.status_ids),end)
        g.require(self.driver.status_ids=={thread_id},'fresh PTY status mismatched owner')
        self.driver._status_capture=False
        self.record.setdefault('status_ids',[]).append(thread_id)

    async def text(self,marker,end):
        # The submitted prompt contains G0_SYNTHETIC_READY, which cannot match
        # this standalone marker. History verification remains authoritative.
        pattern=re.compile(r'(?<![A-Za-z0-9_])'+re.escape(marker)+r'(?![A-Za-z0-9_])')
        await self.until(lambda:pattern.search(pty_runtime._strip_terminal_controls(self.driver.screen_tail.decode('utf-8','replace'))) is not None,end)

    async def quit(self,end):
        g.remaining(end);self.driver.command('/quit');self.record['quit_sent_raw']=g.now()
        while self.driver.poll() is None:
            g.remaining(end);self.drain();await asyncio.sleep(.01)
        self.drain();g.require(self.driver.exit_code==0,'TUI quit was not normal')
        self.record['normal_exit_raw']=g.now()

    async def cleanup(self,end):
        if self.driver is not None:
            if self.driver.poll() is None:
                g.require(probe._process_identity(self.driver.pid)[0]==self.peer['birth'],'TUI cleanup identity unknown')
                g.remaining(end)
                try:os.kill(self.driver.pid,signal.SIGTERM)
                except ProcessLookupError:pass
                while self.driver.poll() is None and g.now()<min(end,self.record.get('cleanup_term_raw',g.now())+1):
                    self.record.setdefault('cleanup_term_raw',g.now());self.drain();await asyncio.sleep(.01)
                if self.driver.poll() is None:
                    g.require(probe._process_identity(self.driver.pid)[0]==self.peer['birth'],'TUI identity changed before kill')
                    g.remaining(end)
                    try:os.kill(self.driver.pid,signal.SIGKILL)
                    except ProcessLookupError:pass
                while self.driver.poll() is None:g.remaining(end);await asyncio.sleep(.01)
            self.drain();self.record.update(exit_code=self.driver.exit_code,pty_sha256=self.driver.digest.hexdigest(),pty_bytes=self.driver.byte_count,inputs=list(self.driver.inputs))
            self.driver.close();self.driver=None
        for path,identity in self.artifacts:probe._unlink_owned(path,identity)
        self.artifacts=[]
        return self.record
