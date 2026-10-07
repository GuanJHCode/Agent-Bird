#!/usr/bin/env python3
"""Run only this task's Linux selector and clean up only its exact container."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[3]
TASK = ROOT / "tasks/linux-profile-admission"
MODULE = ROOT / "tools/orchestrator"
GO = Path("/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go")
IMAGE = "sha256:6ab0b6e7381779332f97b8ca76193e45b0756f38d4c0dcda72dbb3c32061ab99"
SELECTOR = "^TestLinuxProfileAdmissionRejectsBeforeHistory$"
STAGE = sys.argv[1]
if STAGE not in ("red", "green"):
    raise SystemExit("stage must be red or green")
EVIDENCE = TASK / "data" / STAGE
BINDIR = TASK / "tmp" / STAGE
EVIDENCE.mkdir(parents=True, exist_ok=True)
BINDIR.mkdir(parents=True, exist_ok=True)
BINDIR.chmod(0o755)
ENV = dict(os.environ, TMPDIR="/private/tmp", GOTOOLCHAIN="local", GOPROXY="off", GOSUMDB="off")
commands = []


def run(name, args, cwd=MODULE, env=ENV, stdin=None):
    result = subprocess.run([str(arg) for arg in args], cwd=cwd, env=env,
                            input=stdin, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    (EVIDENCE / (name + ".log")).write_bytes(result.stdout)
    commands.append({"name": name, "argv": [str(arg) for arg in args],
                     "cwd": str(cwd), "exit_code": result.returncode})
    return result


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


metadata = {"stage": STAGE, "baseline": "92e4b49ab724916445816a84d4f28bc576bf671e",
            "selector": SELECTOR, "image": IMAGE,
            "source_sha256": {str(path.relative_to(ROOT)): sha(path) for path in (
                MODULE / "internal/host/profile.go",
                MODULE / "internal/host/profile_linux_admission_test.go")}}
cid = None
try:
    version = run("go-version", [GO, "version"])
    if version.returncode != 0 or b"go1.26.8" not in version.stdout:
        raise RuntimeError("pinned Go 1.26.8 unavailable")
    image = run("image-inspect", ["docker", "image", "inspect", IMAGE])
    if image.returncode:
        raise RuntimeError("fixed local image unavailable")
    binary = BINDIR / "host.test"
    build = run("build", [GO, "test", "-c", "-o", binary, "./internal/host"],
                env=dict(ENV, GOOS="linux", GOARCH="arm64", CGO_ENABLED="0"))
    if build.returncode:
        raise RuntimeError("standard host package cross-compile failed")
    binary.chmod(0o755)
    metadata["binary_sha256"] = sha(binary)
    name = "root-linux-profile-" + STAGE + "-" + metadata["binary_sha256"][:12]
    create = run("create", ["docker", "create", "--name", name, "--pull=never",
                           "--network", "none", "--read-only", "--cap-drop", "ALL",
                           "--security-opt", "no-new-privileges", "--user", "65534:65534",
                           "--init", "--tmpfs", "/tmp:rw,nosuid,nodev,mode=1777,size=268435456",
                           "--env", "TMPDIR=/tmp", "--env", "HOME=/tmp",
                           "--mount", "type=bind,src=" + str(BINDIR) + ",dst=/tests,readonly",
                           "--entrypoint", "/tests/host.test", IMAGE,
                           "-test.run", SELECTOR, "-test.v", "-test.count=1", "-test.timeout=30s"])
    if create.returncode:
        raise RuntimeError("container create failed")
    created_cid = create.stdout.decode().strip()
    if len(created_cid) != 64 or any(ch not in "0123456789abcdef" for ch in created_cid):
        raise RuntimeError("unexpected create CID")
    cid = created_cid
    metadata["container_name"], metadata["container_id"] = name, cid
    (EVIDENCE / "create.cid").write_text(cid + "\n")
    run("start", ["docker", "start", "--attach", cid])
    logs = run("raw", ["docker", "logs", cid])
    inspect = run("inspect", ["docker", "inspect", cid])
    if inspect.returncode:
        raise RuntimeError("container state unavailable")
    state = json.loads(inspect.stdout)[0]["State"]
    metadata["state"] = state
    if state["Running"] or state["OOMKilled"] or state["Error"]:
        raise RuntimeError("container run did not finish cleanly")
    converted = run("test2json", [GO, "tool", "test2json", "-p", "internal/host"], stdin=logs.stdout)
    (EVIDENCE / "tests.jsonl").write_bytes(converted.stdout)
    events = [json.loads(line) for line in converted.stdout.splitlines()]
    counts = {"named": {}, "top_level": {}, "package": {}}
    for action in ("run", "pass", "fail", "skip"):
        counts["named"][action] = sum(e.get("Action") == action and bool(e.get("Test")) for e in events)
        counts["top_level"][action] = sum(e.get("Action") == action and bool(e.get("Test")) and "/" not in e["Test"] for e in events)
        counts["package"][action] = sum(e.get("Action") == action and not e.get("Test") for e in events)
    metadata["counts"] = counts
    want_exit, want_fail, want_pass = (1, 3, 0) if STAGE == "red" else (0, 0, 3)
    if (state["ExitCode"] != want_exit or counts["named"]["fail"] != want_fail
            or counts["named"]["pass"] != want_pass or counts["named"]["skip"]):
        raise RuntimeError("unexpected behavior test outcome")
    metadata["expected_outcome_observed"] = True
finally:
    if cid:
        removed = run("remove", ["docker", "rm", cid])
        absent = run("inspect-absent", ["docker", "inspect", cid])
        metadata["cleanup"] = {"remove_exit_code": removed.returncode,
                               "inspect_absent_exit_code": absent.returncode,
                               "absent": absent.returncode != 0 and b"No such object" in absent.stdout}
        if removed.returncode or not metadata["cleanup"]["absent"]:
            metadata["cleanup_error"] = True
    metadata["commands"] = commands
    (EVIDENCE / "result.json").write_text(json.dumps(metadata, indent=2) + "\n")
if metadata.get("cleanup_error"):
    raise SystemExit("exact container cleanup failed")
print(json.dumps({key: metadata[key] for key in ("stage", "counts", "state", "cleanup")}, indent=2))
