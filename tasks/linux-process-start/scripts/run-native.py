#!/usr/bin/env python3
import hashlib
import json
import pathlib
import re
import subprocess
import sys
import time

if len(sys.argv) != 3:
    raise SystemExit("usage: run-native.py LABEL TEST-BINARY")

label, binary_arg = sys.argv[1:]
root = pathlib.Path(__file__).resolve().parents[3]
task = root / "tasks/linux-process-start"
data = task / "data"
binary = pathlib.Path(binary_arg).resolve()
cidfile = data / (label + ".cid")
log = data / (label + ".log")
receipt = data / (label + "-receipt.json")
if cidfile.exists() or log.exists() or receipt.exists():
    raise SystemExit("refuse to overwrite session evidence")

names = [
    "TestShortLivedChildRetainsBirthUntilRegistered",
    "TestOwnedProcessCapturesAndStopsRealChild",
    "TestStopEscalatesToSIGKILLWhenChildIgnoresTermination",
    "TestStopWaitsForBoundedGracefulCleanup",
    "TestStartBirthErrorRetainsOriginalHandle",
    "TestStartDuplexEchoOwnsOriginalHandleAndStdout",
    "TestStartDuplexDrainsAfterOriginalQuickWait",
    "TestDuplexEOFAndCloseCannotProveHeldDescendantExited",
    "TestDuplexBirthFailureClosesFDsAndRetainsOriginalHandle",
]
pattern = "^(" + "|".join(names) + ")$"
command = [
    "/usr/local/bin/docker", "run", "--rm", "--init", "--name",
    "f07-linux-start-" + label, "--cidfile", str(cidfile), "--network", "none",
    "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
    "--user", "65534:65534", "--pids-limit", "64", "--memory", "256m",
    "--cpus", "1", "--tmpfs", "/tmp:rw,nosuid,nodev,size=128m", "--mount",
    "type=bind,src=" + str(binary.parent) + ",dst=/test,readonly", "--entrypoint",
    "/test/" + binary.name,
    "sha256:6ab0b6e7381779332f97b8ca76193e45b0756f38d4c0dcda72dbb3c32061ab99",
    "-test.timeout=30s", "-test.v", "-test.run=" + pattern,
]
started = time.monotonic()
with log.open("wb") as output:
    completed = subprocess.run(command, stdout=output, stderr=subprocess.STDOUT, timeout=45)
cid = cidfile.read_text().strip() if cidfile.exists() else ""
if not re.fullmatch(r"[0-9a-f]{64}", cid):
    raise SystemExit("missing exact owned CID")
inspection = subprocess.run(["/usr/local/bin/docker", "inspect", cid], capture_output=True, text=True, timeout=10)
raw = log.read_bytes()
body = raw.decode(errors="replace")
passed = re.findall(r"^\s*--- PASS: (\S+)", body, re.M)
failed = re.findall(r"^\s*--- FAIL: (\S+)", body, re.M)
skipped = re.findall(r"^\s*--- SKIP: (\S+)", body, re.M)
record = {
    "command": command,
    "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
    "exit_code": completed.returncode,
    "elapsed_seconds": time.monotonic() - started,
    "cid": cid,
    "post_terminal_inspect_exit": inspection.returncode,
    "post_terminal_inspect": inspection.stdout + inspection.stderr,
    "named_expected": names,
    "pass_tests": passed,
    "fail_tests": failed,
    "skip_tests": skipped,
    "raw_sha256": hashlib.sha256(raw).hexdigest(),
}
receipt.write_text(json.dumps(record, indent=2) + "\n")
print(json.dumps(record, indent=2))
if completed.returncode or inspection.returncode == 0:
    raise SystemExit(1)
