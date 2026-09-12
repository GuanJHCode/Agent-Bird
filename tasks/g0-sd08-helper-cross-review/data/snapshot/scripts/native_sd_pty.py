"""Local PTY transport for one native qualification case; no RPC or model logic."""
import datetime as dt
import fcntl
import hashlib
import os
import pty
import select
import struct
import termios
import time
from native_sd_collect import ui_status
from native_sd_clock import CLOCK_IMPL, continuous_ns, clock_pair


def stamp():
    return dt.datetime.now(dt.timezone.utc).isoformat()


class InputSequence:
    def __init__(self,prompt):
        if type(prompt) is not str or not 1 <= len(prompt) <= 16000 or any(x in prompt for x in ('\n','\r','\x1b')):
            raise ValueError('one fixed single-line initial prompt required')
        self.prompt = prompt
        self.used = []

    def take(self,action):
        if action == 'quit-paste' and self.used == ['initial-argv']:
            data = b'\x1b[200~/quit\x1b[201~'
        elif action == 'quit-submit' and self.used == ['initial-argv','quit-paste']:
            data = b'\r'
        else:
            raise ValueError('unregistered or repeated TUI input')
        self.used.append(action)
        return data

    def take_argv(self):
        if self.used: raise ValueError('initial argv already registered or input sequence changed')
        self.used.append('initial-argv')
        return ['codex',self.prompt]


class NativePTY:
    def __init__(self,argv,cwd,*,initial_input=None,launch_deadline_ns=None):
        if initial_input is not None:
            if (type(argv) is not list or len(argv)!=2 or argv[0]!='codex' or type(argv[1]) is not str
                    or type(initial_input) is not dict
                    or set(initial_input)!={'action','at','clock','bytes','sha256'}
                    or initial_input['action']!='initial-argv'
                    or initial_input['bytes']!=len(argv[1].encode())
                    or initial_input['sha256']!=hashlib.sha256(argv[1].encode()).hexdigest()
                    or type(initial_input['clock']) is not dict
                    or set(initial_input['clock'])!={'clock_impl','mono_ns','wall_ns'}
                    or initial_input['clock']['clock_impl']!=CLOCK_IMPL
                    or type(initial_input['clock']['mono_ns']) is not int
                    or type(initial_input['clock']['wall_ns']) is not int
                    or type(launch_deadline_ns) is not int
                    or not 0<=initial_input['clock']['mono_ns']<launch_deadline_ns):
                raise ValueError('fixed argv input metadata required before launch')
        if launch_deadline_ns is not None and (type(launch_deadline_ns) is not int or continuous_ns()>=launch_deadline_ns):
            raise TimeoutError('original native launch deadline ended')
        self.started_at = stamp()
        if launch_deadline_ns is not None and continuous_ns()>=launch_deadline_ns:
            raise TimeoutError('original native launch deadline ended before fork')
        self.pid,self.fd = pty.fork()
        if self.pid == 0:
            os.chdir(cwd)
            os.environ['TERM'] = 'xterm-256color'
            os.execvp(argv[0],argv)
        fcntl.ioctl(self.fd,termios.TIOCSWINSZ,struct.pack('HHHH',60,200,0,0))
        self.digest = hashlib.sha256()
        self.byte_count = 0
        self.buffer = b''
        self.status_roots = set()
        self.status_capture = False
        self.status_buffer = b''
        self.status_capture_start_byte = None
        self.cursor_replies = 0
        self.inputs = [dict(initial_input,clock=dict(initial_input['clock']))] if initial_input is not None else []
        self.exit_code = None
        self.eof = False

    def _poll(self):
        if self.exit_code is None:
            pid,status = os.waitpid(self.pid,os.WNOHANG)
            if pid:
                self.exit_code = os.waitstatus_to_exitcode(status)
        return self.exit_code

    def read(self,seconds):
        if not 0 < seconds <= 5: raise ValueError('PTY read must be bounded to five seconds')
        end = continuous_ns()+int(seconds*1000000000)
        while continuous_ns() < end and not self.eof:
            ready,_,_ = select.select([self.fd],[],[],min(0.1,max(0,end-continuous_ns())/1000000000))
            if ready:
                try: chunk = os.read(self.fd,16384)
                except OSError:
                    self.eof = True; break
                if not chunk:
                    self.eof = True; break
                self.digest.update(chunk); self.byte_count += len(chunk)
                self.buffer = (self.buffer+chunk)[-262144:]
                # Standard terminal query response, not a user or model turn.
                count = self.buffer.count(b'\x1b[6n')
                if count:
                    for _ in range(count): os.write(self.fd,b'\x1b[1;1R')
                    self.cursor_replies += count
                    self.buffer = self.buffer.replace(b'\x1b[6n',b'')
                if self.status_capture:
                    self.status_buffer = (self.status_buffer+chunk)[-262144:]
                    status = ui_status(self.status_buffer.decode('utf-8','replace'))
                    if status: self.status_roots.add(status['status_root'])
            self._poll()
        self._poll()
        return self.evidence()

    def send(self,sequence,action):
        if self._poll() is not None: raise ValueError('TUI already exited')
        data = sequence.take(action)
        if action == 'status-paste':
            self.read(.02)
            self.status_buffer = b''
            self.status_roots.clear()
            self.status_capture_start_byte = self.byte_count
            self.status_capture = True
        elif action != 'status-submit':
            self.status_capture = False
            self.status_buffer = b''
        position = 0
        while position < len(data):
            position += os.write(self.fd,data[position:])
        self.inputs.append({'at':stamp(),'clock':clock_pair(),'action':action,
                            'bytes':len(data),'sha256':hashlib.sha256(data).hexdigest()})

    def wait(self,seconds):
        if not 0 < seconds <= 10: raise ValueError('bounded local exit observation required')
        end = continuous_ns()+int(seconds*1000000000)
        while self._poll() is None and continuous_ns()<end:
            self.read(min(0.2,max(0.001,(end-continuous_ns())/1000000000)))
        return self._poll()

    def evidence(self):
        out = {'pid':self.pid,'started_at':self.started_at,'bytes':self.byte_count,
               'sha256':self.digest.hexdigest(),'cursor_position_replies':self.cursor_replies,
               'inputs':list(self.inputs),'exit_code':self.exit_code}
        if len(self.status_roots)==1: out['status_root'] = next(iter(self.status_roots))
        elif len(self.status_roots)>1: out['ambiguous_status_root'] = True
        if self.status_capture_start_byte is not None:
            out['status_capture_start_byte'] = self.status_capture_start_byte
        return out

    def close(self):
        if self._poll() is None: raise ValueError('refuse implicit shutdown of a live native PTY')
        os.close(self.fd)
