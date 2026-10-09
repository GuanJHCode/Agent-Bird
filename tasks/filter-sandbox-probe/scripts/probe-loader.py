"""Bounded single-variable loader probe; no candidate or production changes."""
import hashlib
import json
import os
from pathlib import Path
import subprocess

root = Path(__file__).resolve().parents[1]
out = root / "data" / "loader-01"
out.mkdir(parents=True)
base = """(version 1)
(allow default)
(deny file-read*)
(deny file-write*)
(deny file-link)
(deny network*)
(deny process-fork)
(allow file-read* (subpath "/System") (subpath "/usr/lib"))
(allow file-read* (subpath "/private/preboot/Cryptexes/OS"))
(allow file-read-metadata)
(allow file-read* file-write* (literal "/dev/null"))
(allow file-read* (literal "/dev/random") (literal "/dev/urandom"))
(allow file-read* (literal "/usr/bin/true"))
"""
records = []
for name, extra in [("baseline", ""), ("root_directory", '(allow file-read-data (literal "/"))')]:
    policy = base + extra
    (out / (name + ".sb")).write_text(policy)
    cmd = ["/usr/bin/sandbox-exec", "-p", policy, "/usr/bin/true"]
    p = subprocess.Popen(cmd, env={"PATH": "/usr/bin:/bin", "HOME": str(out), "TMPDIR": str(out)}, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    try:
        stdout, stderr = p.communicate(timeout=5)
    except subprocess.TimeoutExpired:
        p.kill()  # original Popen handle only, then original Wait
        stdout, stderr = p.communicate(timeout=5)
        raise
    (out / (name + ".stdout")).write_bytes(stdout)
    (out / (name + ".stderr")).write_bytes(stderr)
    records.append({"name": name, "pid": p.pid, "original_wait_exit": p.returncode, "argv": cmd, "stdout": stdout.decode(), "stderr": stderr.decode()})
record = {"pins": {p: hashlib.sha256(Path(p).read_bytes()).hexdigest() for p in ["/usr/bin/true", "/usr/bin/sandbox-exec"]}, "observations": records}
(out / "receipt.json").write_text(json.dumps(record, indent=2) + "\n")
print(json.dumps(record, indent=2))
