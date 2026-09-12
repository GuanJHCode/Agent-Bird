"""Exercise probe activation through the real service, UDS/WS and controlling PTY.

The only execution adapter adds a dummy Python CLI script after the genuine
probe pre-exec gate. Identity admission, sandbox, service receipt writer,
readiness/final validators, PTY/status and cleanup are production code.
"""
from contextlib import ExitStack
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile

import pytest

ROOT=Path(__file__).resolve().parents[4]
for directory in (ROOT/'tasks/g0-tui-proxy/scripts',ROOT/'tasks/g0-auth-preserving-activation/scripts'):
    sys.path.insert(0,str(directory))
import auth_isolation
import native_activation_probe as probe_module
import proxy_transport
from test_activation_service import policy,private_json,digest

FIXTURES=Path(__file__).parent/'fixtures'
ENTRY=ROOT/'tasks/g0-proxy-continuation/cold-start/scripts/projectproxy_launchd_entrypoint.py'


@pytest.mark.parametrize('mode',['success','readiness-fail','final-fail'])
def test_dummy_activation_success_and_failures_use_real_service_receipts(monkeypatch,mode):
    with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw, ExitStack() as roots:
        supervisor=Path(raw); public=supervisor/'public.sock'
        binary=proxy_transport._process_metadata(os.getpid())[1]
        manifest,profiles,grants=policy(supervisor,public,binary,roots)
        value=json.loads(manifest.read_text())
        value['backend_argv']=[binary,'-B',str(FIXTURES/'probe_ws_backend.py'),'{socket_path}',mode]
        value['file_pins']={str(path):digest(path) for path in (
            FIXTURES/'probe_ws_backend.py',FIXTURES/'probe_dummy_tui.py',ENTRY,
            ENTRY.with_name('activation_service.py'),ENTRY.with_name('launch_activation.py'),
            ROOT/'tasks/g0-tui-proxy/scripts/proxy_transport.py',
            ROOT/'tasks/g0-tui-proxy/scripts/proxy_observer.py',
            ROOT/'tasks/g0-auth-preserving-activation/scripts/auth_isolation.py')}
        value['idle_seconds']=1.0
        private_json(manifest,value)
        context=auth_isolation.load_isolation_context(supervisor/'isolation.json','a')
        protected=supervisor/'protected'
        synthetic_credential=protected/'auth.json'
        private_json(synthetic_credential,{'synthetic_fixture':True})
        before=synthetic_credential.read_bytes()
        inventory=supervisor/'credential-paths.txt'
        inventory.write_text(str(synthetic_credential)+'\n'); inventory.chmod(0o600)
        report_path=context.spec.task_root/'native-probe-test-report.json'
        probe=probe_module.NativeActivationProbe(probe_module.ProbeSpec(
            context=context,supervisor_root=supervisor,grants_dir=grants,profile_id='a',
            approved_public_socket=public,real_home=protected,manifest_sha256=digest(manifest),
            mode='activation',credential_manifest=inventory,ready_receipt_dir=supervisor/'state',
            receipt_dir=supervisor/'state',report_path=report_path,use_sandbox=True,
            sandbox_executable_sha256=digest('/usr/bin/sandbox-exec'),timeout=2.5))
        original_spawn=probe_module._spawn_pty_gate
        launches=[]
        def dummy_cli_after_real_gate(argv,env,cwd,master,slave):
            assert argv[0]=='/usr/bin/sandbox-exec'
            assert '--gate-child' in argv and argv[-1]=='--'
            assert env['HOME']==str(context.spec.home) and env['CODEX_HOME']==str(context.spec.codex_home)
            assert not any(key in env for key in ('OPENAI_API_KEY','CODEX_API_KEY','PYTHONPATH'))
            launches.append(tuple(argv))
            return original_spawn([*argv,str(FIXTURES/'probe_dummy_tui.py'),mode],env,cwd,master,slave)
        monkeypatch.setattr(probe_module,'_spawn_pty_gate',dummy_cli_after_real_gate)
        listener=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM)
        listener.bind(str(public)); public.chmod(0o600); listener.listen(8)
        public_identity=(public.stat().st_dev,public.stat().st_ino)
        service=subprocess.Popen([binary,'-I','-B',str(ENTRY),'--manifest',str(manifest),
            '--listener-fd',str(listener.fileno())],pass_fds=(listener.fileno(),),
            env={'PATH':'/usr/bin:/bin','HOME':str(supervisor)},
            stdout=subprocess.PIPE,stderr=subprocess.PIPE)
        service_birth=proxy_transport._process_metadata(service.pid)[0]
        error=None; result=None
        try:
            try:
                result=probe.run(send_status=True,send_quit=True)
            except probe_module.ProbeError as exc:
                error=exc
            stdout,stderr=service.communicate(timeout=3)
            assert service.returncode==0, (stdout,stderr)
        finally:
            if service.poll() is None:
                if proxy_transport._process_metadata(service.pid)[0]==service_birth:
                    service.terminate()
                service.communicate(timeout=3)
            listener.close()
        assert len(launches)==1
        report=json.loads(report_path.read_text())
        finals=list((supervisor/'state').glob('*/activation.json'))
        assert len(finals)==1
        final=json.loads(finals[0].read_text())
        records=final['backend_records']
        assert records, report['pty']['excerpt']
        assert all(row['process_stopped'] and row['socket_removed'] for row in records)
        for row in records:
            assert proxy_transport._process_metadata(row['pid'])[0]!=row['creation_birth']
            assert not os.path.lexists(row['private_socket'])
        assert public.exists() and (public.stat().st_dev,public.stat().st_ino)==public_identity
        assert not list(grants.glob('*.json'))
        assert not os.path.lexists(auth_isolation.default_control_socket(context.spec.codex_home))
        assert synthetic_credential.read_bytes()==before and report['auth']['unchanged'] is True
        ready_files=list((supervisor/'state').glob('*/ready-*.json'))
        if mode=='success':
            assert error is None, str(error)
            assert result is not None and report['status_sent'] and report['quit_sent']
            assert len(ready_files)==1
            ready=json.loads(ready_files[0].read_text())
            assert report['status_session_ids']==[ready['thread']['id']]
            assert ready['frontend']['pid']!=service.pid
            assert report['grant_registered_before_release'] is True
            assert all(row['zero_turns'] and row['turn_counts']=={'start':0,'steer':0} for row in final['transport']['protocol'])
            assert report['cleanup_failures']==[] and report['child_exit']==0
        elif mode=='readiness-fail':
            assert error is not None and result is None
            assert not ready_files and not report['status_sent']
        else:
            assert error is not None and result is None
            assert len(ready_files)==1 and report['status_sent'] and report['quit_sent']
            assert any(row['turn_counts']['start']==1 for row in final['transport']['protocol'])
            assert 'final_receipt_timeout' in report['cleanup_failures']


@pytest.mark.parametrize('policy_variant',['current','without-ptmx','owned-only'])
def test_owned_pty_raw_and_restore_do_not_authorize_another_pty(policy_variant):
    import pty
    with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw, ExitStack() as roots:
        supervisor=Path(raw); public=supervisor/'p.sock'
        binary=proxy_transport._process_metadata(os.getpid())[1]
        manifest,profiles,grants=policy(supervisor,public,binary,roots)
        context=auth_isolation.load_isolation_context(supervisor/'isolation.json','a')
        own_master,own_slave=pty.openpty(); other_master,other_slave=pty.openpty()
        try:
            policy_text=auth_isolation.render_sandbox_profile(context.spec,pty_slave=Path(os.ttyname(own_slave)))
            if policy_variant in ('without-ptmx','owned-only'):
                policy_text='\n'.join(line for line in policy_text.splitlines() if '(literal "/dev/ptmx")' not in line)+'\n'
            if policy_variant=='owned-only':
                policy_text=policy_text.replace('(allow pseudo-tty)\n','')
            sandbox=supervisor/'tty-policy.sbpl'; sandbox.write_text(policy_text); sandbox.chmod(0o600)
            child=subprocess.run(['/usr/bin/sandbox-exec','-f',str(sandbox),binary,'-B',
                str(FIXTURES/'probe_tty_policy.py'),str(own_slave),str(other_slave)],
                pass_fds=(own_slave,other_slave),env=auth_isolation.build_clean_environment(context.spec),
                cwd=context.spec.workspace,capture_output=True,text=True,timeout=3)
            assert child.returncode==0, child.stderr
            result=json.loads(child.stdout)
            assert result['own']==0, result
            assert result['other'] in (1,13), result
            if policy_variant=='owned-only':
                assert result['open_new_ptmx'] in (1,13), result
        finally:
            for descriptor in (own_master,own_slave,other_master,other_slave): os.close(descriptor)
