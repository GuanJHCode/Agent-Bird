#!/usr/bin/env python3
"""External controlled worker with a no-fork release gate."""
import pathlib
import sys
import time
release = pathlib.Path(sys.argv[1])
while not release.exists():
    time.sleep(.02)
sys.stdout.write('capacity-result\n')
