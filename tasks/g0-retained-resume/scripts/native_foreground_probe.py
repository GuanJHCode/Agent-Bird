#!/usr/bin/env python3
"""One-off native exec shutdown probe: foreground, no child and no detach."""
import argparse
import datetime
import json
import os
import pathlib
import re
import signal
import time
import uuid


parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--dir", type=pathlib.Path, required=True)
parser.add_argument("--nonce", required=True)
args = parser.parse_args()
match = re.fullmatch(r"rr-probe-(quit|kill)-([0-9]{2})", args.nonce)
base = pathlib.Path("/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/test-repo/tasks/retained-resume/data")
expected = base / ("native-probe-" + match[1] + "-" + match[2]) if match else None
owner = os.environ.get("CODEX_THREAD_ID", "")
if expected is None or args.dir.resolve() != expected or args.dir.stat().st_mode & 0o777 != 0o700:
    parser.error("expected unique private probe directory")
if str(uuid.UUID(owner)) != owner:
    parser.error("native thread identity missing")
metadata = {"version": 1, "nonce": args.nonce, "controller_thread": owner,
            "pid": os.getpid(), "ppid": os.getppid(), "pgid": os.getpgrp(), "duration_seconds": 90}
with (args.dir / "meta.json").open("x") as file:
    json.dump(metadata, file); file.write("\n"); file.flush(); os.fsync(file.fileno())
events = (args.dir / "events.jsonl").open("x")


def record(status, received_signal=None):
    value = {"version": 1, "nonce": args.nonce, "status": status,
             "at": datetime.datetime.now(datetime.timezone.utc).isoformat()}
    if received_signal is not None:
        value["signal"] = signal.Signals(received_signal).name
    events.write(json.dumps(value) + "\n"); events.flush(); os.fsync(events.fileno())
    print(json.dumps(value), flush=True)


def stopped(number, _frame):
    record("interrupted", number)
    raise SystemExit(128 + number)


for number in (signal.SIGTERM, signal.SIGHUP, signal.SIGINT):
    signal.signal(number, stopped)
record("running")
time.sleep(90)
record("completed")
