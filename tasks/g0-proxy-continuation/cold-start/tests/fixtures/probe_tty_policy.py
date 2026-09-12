"""Operate only the two newly allocated test PTY descriptors passed by parent."""
import json
import os
import sys
import termios
import tty

result={}
for name,value in zip(('own','other'),sys.argv[1:]):
    fd=int(value)
    previous=termios.tcgetattr(fd)
    try:
        tty.setraw(fd,termios.TCSANOW)
        termios.tcsetattr(fd,termios.TCSANOW,previous)
        result[name]=0
    except (OSError,termios.error) as exc:
        result[name]=exc.args[0]
try:
    descriptor=os.open('/dev/ptmx',os.O_RDWR)
    os.close(descriptor)
    result['open_new_ptmx']=0
except OSError as exc:
    result['open_new_ptmx']=exc.errno
print(json.dumps(result,sort_keys=True))
