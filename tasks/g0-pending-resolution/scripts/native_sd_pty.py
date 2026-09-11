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


def stamp():
    return dt.datetime.now(dt.timezone.utc).isoformat()


class InputSequence:
    def __init__(self,prompt):
        if type(prompt) is not str or not 1 <= len(prompt) <= 16000 or any(x in prompt for x in ('\n','\r','\x1b')):
            raise ValueError('one fixed single-line initial prompt required')
        self.prompt = prompt
        self.used = []

    def take(self,action):
        if action == 'status' and not self.used:
            data = b'/status\r'
        elif action == 'initial' and self.used == ['status']:
            data = self.prompt.encode()+b'\r'
        elif action == 'quit' and 'quit' not in self.used:
            data = b'/quit\r'
        else:
            raise ValueError('unregistered or repeated TUI input')
        self.used.append(action)
        return data


class NativePTY:
    def __init__(self,argv,cwd):
        self.started_at = stamp()
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
        self.inputs = []
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
        end = time.monotonic()+seconds
        while time.monotonic() < end and not self.eof:
            ready,_,_ = select.select([self.fd],[],[],min(0.1,max(0,end-time.monotonic())))
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
        if action == 'status':
            self.read(.02)
            self.status_buffer = b''
            self.status_roots.clear()
            self.status_capture_start_byte = self.byte_count
            self.status_capture = True
        else:
            self.status_capture = False
            self.status_buffer = b''
        position = 0
        while position < len(data):
            position += os.write(self.fd,data[position:])
        self.inputs.append({'at':stamp(),'monotonic':time.monotonic(),'action':action,
                            'bytes':len(data),'sha256':hashlib.sha256(data).hexdigest()})

    def wait(self,seconds):
        if not 0 < seconds <= 10: raise ValueError('bounded local exit observation required')
        end = time.monotonic()+seconds
        while self._poll() is None and time.monotonic()<end:
            self.read(min(0.2,max(0.001,end-time.monotonic())))
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
