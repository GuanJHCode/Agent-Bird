"""Inspect only this newly created controlling PTY and another new test PTY."""
import fcntl
import json
import os
import socket
import struct
import sys
import termios
import tty

result={'stdin_isatty':os.isatty(0),'stdout_stderr_same_tty':os.isatty(1) and os.isatty(2) and (os.fstat(1).st_dev,os.fstat(1).st_ino)==(os.fstat(2).st_dev,os.fstat(2).st_ino)}

def attempt(name,call):
    try:
        call(); result[name]=0
    except (OSError,termios.error) as exc:
        result[name]=exc.args[0]


def raw_restore(fd):
    previous=termios.tcgetattr(fd)
    tty.setraw(fd,termios.TCSANOW)
    termios.tcsetattr(fd,termios.TCSANOW,previous)


def opened(path,flags,call=lambda fd:None):
    fd=os.open(path,flags)
    try: call(fd)
    finally: os.close(fd)

attempt('stdin_raw_restore',lambda:raw_restore(0))
attempt('tty_read_open',lambda:opened('/dev/tty',os.O_RDONLY))
attempt('tty_read_winsize',lambda:opened('/dev/tty',os.O_RDONLY,lambda fd:fcntl.ioctl(fd,termios.TIOCGWINSZ,struct.pack('HHHH',0,0,0,0))))
attempt('tty_read_raw_restore',lambda:opened('/dev/tty',os.O_RDONLY,raw_restore))
attempt('tty_rdwr_raw_restore',lambda:opened('/dev/tty',os.O_RDWR,raw_restore))
attempt('other_pty_raw_restore',lambda:raw_restore(int(sys.argv[1])))
attempt('new_ptmx_open',lambda:opened('/dev/ptmx',os.O_RDWR))
attempt('dev_null_write_open',lambda:opened('/dev/null',os.O_WRONLY))
def stderr_suppress_restore():
    saved=os.dup(2)
    try:
        opened('/dev/null',os.O_WRONLY,lambda fd:os.dup2(fd,2))
    finally:
        os.dup2(saved,2); os.close(saved)
attempt('stderr_suppress_restore',stderr_suppress_restore)
def pair():
    left,right=socket.socketpair(socket.AF_UNIX,socket.SOCK_STREAM); left.close(); right.close()
attempt('unix_socketpair',pair)
def pipe():
    left,right=os.pipe(); os.close(left); os.close(right)
attempt('anonymous_pipe',pipe)
print(json.dumps(result,sort_keys=True),flush=True)
