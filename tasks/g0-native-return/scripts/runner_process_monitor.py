#!/usr/bin/env python3
"""Finite observer for the owned CLI/Host exit experiment; never signals them."""

import argparse
import datetime
import json
import pathlib
import subprocess
import time


def identity(pid):
    run = subprocess.run(["ps", "-p", str(pid), "-o", "pid=,ppid=,pgid=,lstart=,comm="],
                         text=True, capture_output=True)
    return run.stdout.strip() if run.returncode == 0 else None


parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--native-pid", required=True, type=int)
parser.add_argument("--dir", required=True, type=pathlib.Path)
parser.add_argument("--nonce", required=True)
parser.add_argument("--output", required=True, type=pathlib.Path)
parser.add_argument("--duration", type=int, default=90)
args = parser.parse_args()
if not 1 <= args.duration <= 120:
    parser.error("duration must be 1..120 seconds")
initial_native = identity(args.native_pid)
if initial_native is None:
    raise SystemExit("owned native PID must be live before monitoring")
job = json.loads((args.dir / "job.json").read_text())
if job.get("nonce") != args.nonce or not isinstance(job.get("worker_pid"), int):
    raise SystemExit("job identity mismatch")
worker_pid = job["worker_pid"]
initial_worker = identity(worker_pid)
if initial_worker is None:
    raise SystemExit("owned worker must be live before monitoring")
initial = time.monotonic()
previous = None
args.output.parent.mkdir(parents=True, exist_ok=True)
with args.output.open("x") as output:
    while time.monotonic() - initial <= args.duration:
        native = identity(args.native_pid)
        worker = identity(worker_pid)
        result_path = args.dir / "result.json"
        present = result_path.exists()
        state = (native, worker, present)
        if state != previous:
            record = {"at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
                      "elapsed_seconds": round(time.monotonic() - initial, 4),
                      "native_identity": native, "worker_identity": worker,
                      "result_present": present}
            if present:
                result = json.loads(result_path.read_text())
                if (set(result) != {"version", "status", "nonce", "count"}
                        or result != {"version": 1, "status": "completed", "nonce": args.nonce, "count": 1}):
                    raise SystemExit("unexpected synthetic result")
                record["result"] = result
                record["result_mtime"] = datetime.datetime.fromtimestamp(
                    result_path.stat().st_mtime, datetime.timezone.utc).isoformat()
            output.write(json.dumps(record) + "\n")
            output.flush()
            previous = state
        if native is None and worker is None and present:
            break
        time.sleep(0.1)
    output.write(json.dumps({"monitor_finished_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
                             "elapsed_seconds": round(time.monotonic() - initial, 4)}) + "\n")
    output.flush()
print(json.dumps({"output": str(args.output), "native_pid": args.native_pid, "worker_pid": worker_pid}))
