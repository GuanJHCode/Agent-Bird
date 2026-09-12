"""Bounded dummy-only check of the controlling /dev/tty alias boundary."""
from contextlib import ExitStack
import json
import os
from pathlib import Path
import pty
import sys
import tempfile

import pytest

ROOT=Path(__file__).resolve().parents[4]
for directory in (ROOT/'tasks/g0-tui-proxy/scripts',ROOT/'tasks/g0-auth-preserving-activation/scripts'):
    sys.path.insert(0,str(directory))
import auth_isolation as guard
import native_activation_probe as probe
import proxy_transport
from proxy_native_runtime import PTYDriver
from test_activation_service import policy


@pytest.mark.parametrize('variant',['slave-only-baseline','tty-alias','dev-null-write'])
def test_controlling_tty_alias_with_real_sandbox_and_other_pty_denial(variant):
    with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw, ExitStack() as roots:
        supervisor=Path(raw); binary=proxy_transport._process_metadata(os.getpid())[1]
        manifest,profiles,grants=policy(supervisor,supervisor/'p.sock',binary,roots)
        context=guard.load_isolation_context(supervisor/'isolation.json','a')
        master,slave=pty.openpty(); other_master,other_slave=pty.openpty()
        os.set_blocking(master,False); os.set_inheritable(other_slave,True)
        driver=None
        try:
            text=guard.render_sandbox_profile(context.spec,pty_slave=Path(os.ttyname(slave)))
            # Hold the denied baseline fixed even after the production guard adopts
            # the tested /dev/null exception; the two additions remain explicit.
            text='\n'.join(line for line in text.splitlines() if '(literal "/dev/null")' not in line)+'\n'
            if variant=='tty-alias':
                text+='(allow file-read* file-write* file-ioctl (literal "/dev/tty"))\n'
            if variant=='dev-null-write':
                text+='(allow file-write* (literal "/dev/null"))\n'
            sandbox=supervisor/'controlling-tty.sbpl'; sandbox.write_text(text); sandbox.chmod(0o600)
            script=Path(__file__).parent/'fixtures/probe_controlling_tty.py'
            argv=['/usr/bin/sandbox-exec','-f',str(sandbox),binary,'-B',str(script),str(other_slave)]
            pid,fd=probe._spawn_pty_gate(argv,guard.build_clean_environment(context.spec),context.spec.workspace,master,slave)
            slave=None; driver=PTYDriver(pid,fd)
            assert driver.wait(3)==0, driver.screen_tail.decode('utf-8','replace')
            driver.read_available()
            result=json.loads(driver.screen_tail.decode())
            print(json.dumps({'policy':variant,'result':result},sort_keys=True))
            assert result['stdin_isatty'] and result['stdout_stderr_same_tty'] and result['stdin_raw_restore']==0
            assert result['tty_read_winsize']==0
            if variant=='dev-null-write':
                assert result['dev_null_write_open']==0 and result['stderr_suppress_restore']==0
                assert result['tty_rdwr_raw_restore'] in (1,13)
            else:
                assert result['dev_null_write_open'] in (1,13) and result['stderr_suppress_restore'] in (1,13)
            assert result['other_pty_raw_restore'] in (1,13)
            assert result['new_ptmx_open'] in (1,13)
            if variant=='tty-alias':
                assert result['tty_read_winsize']==0 and result['tty_rdwr_raw_restore']==0
        finally:
            if driver is not None:
                if driver.poll() is None: driver.terminate(1)
                driver.close()
            else: os.close(master)
            for fd in (slave,other_master,other_slave):
                if fd is not None: os.close(fd)
