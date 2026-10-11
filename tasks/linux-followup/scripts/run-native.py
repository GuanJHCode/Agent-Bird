#!/usr/bin/env python3
"""One offline Linux installed-binary acceptance; reuse collect-only packaging."""
import hashlib
import json
import pathlib
import subprocess
import tarfile

base = pathlib.Path(__file__).resolve().parents[1] / "data"
image = "sha256:6ab0b6e7381779332f97b8ca76193e45b0756f38d4c0dcda72dbb3c32061ab99"
archive = base / "package.tar"
receipt = base / "native-receipt.json"
log = base / "native.log"
if any(p.exists() for p in (archive, receipt, log)):
    raise SystemExit("refuse_to_replace_evidence")
with tarfile.open(archive, "x") as bundle:
    plugin = base / "package/plugin"
    for path in sorted(plugin.rglob("*")):
        if path.is_symlink():
            raise SystemExit("fixture_symlink")
        if path.is_dir():
            continue
        info = bundle.gettarinfo(str(path), arcname=path.relative_to(plugin).as_posix())
        info.uid = info.gid = 65534
        info.uname = info.gname = ""
        info.mtime = 0
        with path.open("rb") as source:
            bundle.addfile(info, source)
archive.chmod(0o644)
create = ["docker", "create", "--pull=never", "--init", "--name", "linux-followup-20261012",
          "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
          "--user", "65534:65534", "--pids-limit", "96", "--memory", "512m", "--cpus", "2",
          # Installed binary must execute in private /tmp, unlike check-only acceptance.
          "--tmpfs", "/tmp:rw,exec,nosuid,nodev,size=256m,mode=1777",
          "--mount", "type=bind,source=" + str(base) + ",target=/test,readonly",
          "--mount", "type=bind,source=" + str(archive) + ",target=/input/package.tar,readonly",
          "--env", "AGENT_BIRD_LINUX_FOLLOWUP_NATIVE=1", "--entrypoint", "/test/native-followup", image, "-test.run=^TestLinuxPortableFollowupNative$", "-test.v", "-test.timeout=30s"]
cid = subprocess.run(create, check=True, capture_output=True, text=True, timeout=20).stdout.strip()
result = {"create_command": create, "cid": cid,
          "sha256": {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in
                     (base / "codex-orchestrator", base / "native-followup", archive)},
          "portable_manifest": json.loads((base / "package/plugin/portable.json").read_text())}
receipt.write_text(json.dumps(result, indent=2) + "\n")
try:
    with log.open("wb") as output:
        run = subprocess.run(["docker", "start", "--attach", cid], stdout=output,
                             stderr=subprocess.STDOUT, timeout=60)
    result["start_exit"] = run.returncode
except subprocess.TimeoutExpired:
    subprocess.run(["docker", "stop", "--time", "5", cid], capture_output=True, timeout=20)
    result["start_exit"] = 124
finally:
    meta = json.loads(subprocess.run(["docker", "inspect", cid], check=True,
                                    capture_output=True, text=True, timeout=20).stdout)[0]
    result.update(state=meta["State"], image=meta["Image"])
    removed = subprocess.run(["docker", "rm", cid], capture_output=True, timeout=20)
    absent = subprocess.run(["docker", "inspect", cid], capture_output=True, timeout=20)
    result.update(remove_exit=removed.returncode, post_remove_inspect_exit=absent.returncode)
    raw = log.read_bytes()
    result["log_sha256"] = hashlib.sha256(raw).hexdigest()
    if result["start_exit"] == 0:
        result["summary"] = {"status": "passed" if "--- PASS: TestLinuxPortableFollowupNative" in raw.decode() and "--- FAIL:" not in raw.decode() else "failed", "test": "TestLinuxPortableFollowupNative"}
    receipt.write_text(json.dumps(result, indent=2) + "\n")
print(json.dumps(result.get("summary", result["state"])))
raise SystemExit(0 if result["start_exit"] == 0 and meta["State"]["ExitCode"] == 0
                 and not meta["State"]["OOMKilled"] and removed.returncode == 0
                 and absent.returncode != 0 and result["summary"]["status"] == "passed" else 1)
