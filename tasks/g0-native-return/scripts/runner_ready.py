#!/usr/bin/env python3
"""Bounded Host/process readiness only. Never reads or qualifies native Goal state."""

import argparse
import datetime
import json
import pathlib
import re
import subprocess
import time
import uuid


PROJECT = pathlib.Path("/Users/guanjunhui/code/ai/codex-cli-orchestration-design")
TEST_REPO = PROJECT / "tasks/g0-macos/data/test-repo"
HOST = str(PROJECT / "tasks/g0-native-return/tmp/host-impl/tools/g0-return-lab/data/g0-return-lab")
WORKERS = {HOST, HOST + "-ack"}


def identity(pid):
    result = subprocess.run(["ps", "-p", str(pid), "-o", "pid=,ppid=,pgid=,lstart=,comm="],
                            text=True, capture_output=True)
    return result.stdout.strip() if result.returncode == 0 else None


def valid_identity(value, pid, executables):
    if not isinstance(value, str):
        return False
    parts = value.split(maxsplit=8)
    if len(parts) != 9 or parts[8] not in executables:
        return False
    try:
        if any(not x.isascii() or not x.isdecimal() for x in parts[:3]):
            return False
        if int(parts[0]) != pid or not all(0 < int(x) < 2**31 for x in parts[:3]):
            return False
        datetime.datetime.strptime(" ".join(parts[3:8]), "%a %b %d %H:%M:%S %Y")
    except ValueError:
        return False
    return True


def valid_job(value, nonce, root):
    base = {"version", "status", "nonce", "worker_pid", "created_at", "job_file", "result_file"}
    owned = {"controller_thread", "task_revision"}
    if not isinstance(value, dict) or set(value) not in (base, base | owned):
        return False
    if (type(value["version"]) is not int or value["version"] != 1 or value["status"] != "started"
            or value["nonce"] != nonce or type(value["worker_pid"]) is not int
            or not 0 < value["worker_pid"] < 2**31
            or value["job_file"] != "job.json" or value["result_file"] != "result.json"):
        return False
    stamp = value["created_at"]
    if not isinstance(stamp, str) or len(stamp) > 30 or not re.fullmatch(
            r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z", stamp, flags=re.ASCII):
        return False
    try:
        datetime.datetime.fromisoformat(stamp.replace("Z", "+00:00"))
    except ValueError:
        return False
    if owned <= set(value) and (value["controller_thread"] != root
            or type(value["task_revision"]) is not int or value["task_revision"] != 1):
        return False
    return True


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root-thread", required=True, help="Caller-declared ID; no Goal or thread attachment proof")
    parser.add_argument("--native-pid", required=True, type=int)
    parser.add_argument("--nonce", required=True)
    parser.add_argument("--dir", required=True, type=pathlib.Path)
    parser.add_argument("--output", required=True, type=pathlib.Path)
    parser.add_argument("--timeout", type=int, default=90)
    args = parser.parse_args()
    try:
        canonical_root = str(uuid.UUID(args.root_thread)) == args.root_thread
    except ValueError:
        canonical_root = False
    if not canonical_root or not 0 < args.native_pid < 2**31 or not 1 <= args.timeout <= 100:
        parser.error("explicit UUID, native PID and 1..100 second timeout required")
    nonce = re.fullmatch(r"nr-g0-goal-([0-9]{2})", args.nonce)
    expected = TEST_REPO / ("tasks/native-return/data/runner-host-goal-" + (nonce[1] if nonce else "invalid"))
    if nonce is None or args.dir.resolve() != expected or args.dir.stat().st_mode & 0o777 != 0o700:
        parser.error("directory must be the exact owned 0700 synthetic task directory")
    original_native = identity(args.native_pid)
    if not valid_identity(original_native, args.native_pid, {"codex"}):
        raise SystemExit("owned native CLI process identity invalid")
    deadline = time.monotonic() + args.timeout
    while time.monotonic() < deadline:
        if identity(args.native_pid) != original_native:
            raise SystemExit("native PID disappeared or creation identity changed")
        job_path = args.dir / "job.json"
        if job_path.exists():
            if (args.dir / "result.json").exists():
                raise SystemExit("result already ready; cannot count an in-flight sample")
            value = json.loads(job_path.read_text())
            if not valid_job(value, args.nonce, args.root_thread):
                raise SystemExit("job metadata invalid")
            worker = identity(value["worker_pid"])
            if not valid_identity(worker, value["worker_pid"], WORKERS):
                raise SystemExit("owned Host process identity invalid")
            keys = ("version", "status", "nonce", "worker_pid", "controller_thread", "task_revision")
            record = {"ready_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
                      "scope": "host_process_and_pending_result", "goal_checked": False,
                      "root_thread": args.root_thread, "root_thread_source": "caller_supplied",
                      "nonce": args.nonce, "native_identity": original_native, "worker_identity": worker,
                      "job": {key: value[key] for key in keys if key in value}, "result_present": False}
            args.output.parent.mkdir(parents=True, exist_ok=True)
            with args.output.open("x") as output:
                output.write(json.dumps(record, indent=2) + "\n")
            print(json.dumps(record), flush=True)
            return
        time.sleep(0.1)
    raise SystemExit("Host readiness not established within the fixed preparation window")


if __name__ == "__main__":
    main()
