#!/usr/bin/env python3
"""Run the Linux portable `plugin-run ... -- check` acceptance container.

The F07 root phase owns invocation. The container has no network, a read-only
root, no capabilities, and only /tmp is writable; the plugin input stays a
read-only bind mount.
"""
import hashlib
import json
import pathlib
import re
import subprocess
import sys
import tarfile
import os


label, binary_arg, plugin_arg = sys.argv[1:]
if not re.fullmatch(r"[a-z0-9-]+", label):
    raise SystemExit("invalid_label")
binary = pathlib.Path(binary_arg).resolve()
plugin = pathlib.Path(plugin_arg).resolve()
if not binary.is_file() or not plugin.is_dir():
    raise SystemExit("binary_or_plugin_missing")

base = pathlib.Path(__file__).resolve().parents[1] / "data" / "linux"
base.mkdir(mode=0o700, parents=True, exist_ok=True)
receipt = base / (label + "-receipt.json")
log = base / (label + ".log")
if receipt.exists() or log.exists():
    raise SystemExit("refuse_to_replace_evidence")
image = "sha256:6ab0b6e7381779332f97b8ca76193e45b0756f38d4c0dcda72dbb3c32061ab99"
# A read-only archive is world-readable only inside this owned task directory.
# The unprivileged acceptance actor extracts it into its own private /tmp tree.
archive = base / (label + "-package.tar")
if archive.exists():
    raise SystemExit("refuse_to_replace_evidence")
with tarfile.open(archive, "x") as bundle:
    for path in sorted(plugin.rglob("*")):
        if path.is_symlink():
            raise SystemExit("fixture_symlink")
        if path.is_dir():
            continue
        info = bundle.gettarinfo(str(path), arcname=path.relative_to(plugin).as_posix())
        if not info.isfile():
            raise SystemExit("fixture_not_regular")
        info.uid = info.gid = 65534
        info.uname = info.gname = ""
        info.mtime = 0
        with path.open("rb") as source:
            bundle.addfile(info, source)
archive.chmod(0o644)
harness = binary.parent / "orchestrator.test"
if not harness.is_file():
    raise SystemExit("native_harness_missing")
name = "root-linux-portable-check-" + label
create = [
    "docker", "create", "--pull=never", "--init", "--name", name,
    "--network", "none", "--read-only", "--cap-drop", "ALL",
    "--security-opt", "no-new-privileges", "--user", "65534:65534",
    "--pids-limit", "96", "--memory", "512m", "--cpus", "2",
    "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=256m,mode=1777",
    "--env", "AGENT_BIRD_LINUX_PORTABLE_NATIVE=1",
    "--mount", "type=bind,source=" + str(binary.parent) + ",target=/test,readonly",
    "--mount", "type=bind,source=" + str(archive) + ",target=/input/package.tar,readonly",
    "--entrypoint", "/test/orchestrator.test", image,
    "-test.run=^TestLinuxPortablePackageCheckNative$", "-test.v", "-test.count=1", "-test.timeout=120s",
]
created = subprocess.run(create, capture_output=True, text=True, check=True, timeout=20)
cid = created.stdout.strip()
if not re.fullmatch(r"[a-f0-9]{64}", cid):
    raise SystemExit("container_identity_invalid")
result = {
    "label": label, "create_command": create, "cid": cid,
    "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
    "harness_sha256": hashlib.sha256(harness.read_bytes()).hexdigest(),
    "archive_sha256": hashlib.sha256(archive.read_bytes()).hexdigest(), "state": "created",
}
receipt.write_text(json.dumps(result, indent=2) + "\n")
try:
    with log.open("wb") as out:
        run = subprocess.run(["docker", "start", "--attach", cid], stdout=out, stderr=subprocess.STDOUT, timeout=150)
except subprocess.TimeoutExpired:
    # Stop only this exact owned container; preserve inspection and cleanup.
    subprocess.run(["docker", "stop", "--time", "5", cid], capture_output=True, timeout=20)
    run = subprocess.CompletedProcess([], 124)
inspection = subprocess.run(["docker", "inspect", cid], capture_output=True, text=True, check=True, timeout=20)
meta = json.loads(inspection.stdout)[0]
raw = log.read_bytes()
body = raw.decode(errors="replace")
passes = re.findall(r"^--- PASS: (\S+)", body, re.M)
fails = re.findall(r"^\s*--- FAIL: (\S+)", body, re.M)
skips = re.findall(r"^\s*--- SKIP: (\S+)", body, re.M)
semantic_pass = passes == ["TestLinuxPortablePackageCheckNative"] and not fails and not skips and "original child Wait completed" in body
result.update(pass_tests=passes, fail_tests=fails, skip_tests=skips, semantic_pass=semantic_pass)
result.update(start_exit=run.returncode, state=meta["State"], image=meta["Image"], log_sha256=hashlib.sha256(raw).hexdigest())
removed = subprocess.run(["docker", "rm", cid], capture_output=True, text=True, timeout=20)
absent = subprocess.run(["docker", "inspect", cid], capture_output=True, text=True, timeout=20)
result.update(remove_exit=removed.returncode, post_remove_inspect_exit=absent.returncode)
receipt.write_text(json.dumps(result, indent=2) + "\n")
print(json.dumps({"label": label, "cid": cid, "exit": meta["State"]["ExitCode"], "removed": removed.returncode == 0 and absent.returncode != 0}))
raise SystemExit(0 if run.returncode == 0 and meta["State"]["ExitCode"] == 0 and not meta["State"]["OOMKilled"] and semantic_pass and removed.returncode == 0 and absent.returncode != 0 else 1)
