#!/usr/bin/env python3
"""Explicit owner-review phases; dispatch/review never execute candidate code.

After reading the saved diff/source, explicitly validate its candidate OID and
source SHA. Validation requires the macOS sandbox; no unrestricted fallback.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile


def digest(data):
    return hashlib.sha256(data).hexdigest()


def save(path, value):
    path.write_text(json.dumps(value, indent=2, ensure_ascii=False) + "\n")
    path.chmod(0o600)


def checkpoint(path, value):
    """Publish an immutable request/receipt before invoking a mutating command."""
    if path.exists():
        if json.loads(path.read_text()) != value:
            raise ValueError("checkpoint_binding_changed")
        return
    descriptor, temporary = tempfile.mkstemp(prefix=path.name + ".", dir=path.parent)
    try:
        with os.fdopen(descriptor, "w") as stream:
            json.dump(value, stream, indent=2, ensure_ascii=False)
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.link(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        os.unlink(temporary)


def collection_ack(page, task_id, event, command_id):
    # collect uses contract.Event.payload_hash; inspect uses event_hash.
    actionable = [item for item in page["events"] if item.get("action_slot") and item["kind"] != "progress"]
    bindings = [{"event_id": item["event_id"], "event_revision": item["event_revision"], "event_hash": item["payload_hash"], "action_slot": item["action_slot"]} for item in actionable]
    expected = {key: event[key] for key in ("event_id", "event_revision", "event_hash", "action_slot")}
    if bindings != [expected]:
        raise ValueError("exact_reviewed_collection_required")
    return {"version": 1, "task_id": task_id, "delivery_id": page["delivery_id"], "collection_proof_sha256": page["collection_proof_sha256"], "decisions": [{**bindings[0], "decision": "handled", "command_id": command_id}]}


# Only known typed CLI codes may become public diagnostics. Never forward arbitrary
# error strings, arguments, paths, stdout, or stderr from a failed child process.
SAFE_CLI_ERRORS = frozenset({
    "provider_control_invalid", "provider_disabled", "provider_stop_pending",
    "provider_lock_required", "provider_lock_invalid", "provider_binary_not_found",
    "coordinator_upgrade_required", "execution_profile_unsupported", "model_invalid",
    "owner_capability_invalid", "managed_workspace_required", "invalid_args",
    "codex_guard_package_untrusted", "codex_trial_guard_not_ready",
    "cli_version_unparsed", "cli_version_catalog_invalid",
})


class CommandFailure(RuntimeError):
    def __init__(self, failure):
        def output_bytes(value):
            return value if isinstance(value, bytes) else (value or "").encode("utf-8")
        stdout, stderr = output_bytes(failure.stdout), output_bytes(failure.stderr)
        code = "command_failed"
        try:
            envelope = json.loads(stdout)
        except (ValueError, UnicodeError):
            envelope = None
        if (isinstance(envelope, dict) and type(envelope.get("version")) is int
                and envelope["version"] == 1 and envelope.get("status") == "error"
                and isinstance(envelope.get("error"), str)
                and envelope["error"] in SAFE_CLI_ERRORS):
            code = envelope["error"]
        self.diagnostic = {
            "version": 1, "status": "error", "error": code,
            "exit_code": failure.returncode,
            "stdout": {"size_bytes": len(stdout), "sha256": digest(stdout)},
            "stderr": {"size_bytes": len(stderr), "sha256": digest(stderr)},
        }
        super().__init__(json.dumps(self.diagnostic))


def command(args, cwd=None):
    try:
        return subprocess.run(args, cwd=cwd, check=True, capture_output=True).stdout.decode("utf-8")
    except subprocess.CalledProcessError as failure:
        raise CommandFailure(failure) from None


def candidate_source(repo, oid):
    return subprocess.check_output(["/usr/bin/git", "show", oid + ":calc.py"], cwd=repo)


def sandbox_validate(source, revision, scratch_parent):
    if sys.platform != "darwin" or not Path("/usr/bin/sandbox-exec").is_file():
        raise ValueError("validation_sandbox_unavailable")
    import resource
    python = Path(sys.executable).resolve()
    runtime = Path(sys.base_prefix).resolve()
    with tempfile.TemporaryDirectory(prefix="validation-", dir=scratch_parent) as temporary:
        scratch = Path(temporary).resolve()
        os.chmod(scratch, 0o700)
        (scratch / "calc.py").write_bytes(source)
        test = '''import runpy
add = runpy.run_path("calc.py")["add"]
assert add(2, 3) == 5
assert add(-2, 3) == 1
assert add(0, 0) == 0
assert add(1.5, 2.25) == 3.75
'''
        if revision == 2:
            test += '''class IntSubclass(int): pass
class FloatSubclass(float): pass
for pair in ((True, 1), (1, False), ("a", "b"), ([], []), (None, 1), (IntSubclass(1), 2), (1, IntSubclass(2)), (FloatSubclass(1.5), 2), (1, FloatSubclass(2.5))):
    try:
        add(*pair)
    except TypeError:
        pass
    else:
        raise AssertionError("explicit numeric-only boundary required")
'''
        (scratch / "check.py").write_text(test)
        quote = lambda value: json.dumps(str(value))
        # All other caller files, process metadata, Mach services, subprocesses
        # and network are denied even when candidate code imports ctypes.
        policy = '''(version 1)
(allow default)
(deny file-read*)
(deny file-write*)
(deny network*)
(deny process-fork)
(deny process-exec)
(deny process-info*)
(deny sysctl-read)
(deny mach-lookup)
'''
        for path in ("/System", "/usr/lib", runtime, scratch):
            policy += "(allow file-read* (subpath " + quote(path) + "))\n"
        for path in (python, "/dev/null", "/dev/random", "/dev/urandom"):
            policy += "(allow file-read* (literal " + quote(path) + "))\n"
        policy += "(allow file-write* (subpath " + quote(scratch) + "))\n"
        policy += '(allow file-write* (literal "/dev/null"))\n'
        env = {"PATH": "/usr/bin:/bin", "LANG": "C", "HOME": str(scratch), "TMPDIR": str(scratch)}
        def limits():
            resource.setrlimit(resource.RLIMIT_CPU, (10, 10))
            resource.setrlimit(resource.RLIMIT_FSIZE, (1024 * 1024, 1024 * 1024))
        # Initialize the trusted interpreter/stdlib with a minimal environment,
        # then install Seatbelt before opening or executing any candidate. This
        # avoids granting candidate-time exec/fork or ancestor directory reads
        # just to accommodate Homebrew's launcher -> framework startup chain.
        bootstrap = r'''import ctypes, runpy, sys
library = ctypes.CDLL("/usr/lib/libsandbox.dylib", use_errno=True)
initialize = library.sandbox_init
initialize.argtypes = [ctypes.c_char_p, ctypes.c_uint64, ctypes.POINTER(ctypes.c_char_p)]
initialize.restype = ctypes.c_int
error = ctypes.c_char_p()
policy = open(sys.argv[1], "rb").read()
if initialize(policy, 0, ctypes.byref(error)) != 0:
    raise SystemExit("validation_sandbox_initialization_failed")
runpy.run_path(sys.argv[2], run_name="__main__")
'''
        (scratch / "policy.sb").write_text(policy)
        with (scratch / "stdout").open("wb") as stdout, (scratch / "stderr").open("wb") as stderr:
            try:
                result = subprocess.run([str(python), "-I", "-S", "-B", "-c", bootstrap, str(scratch / "policy.sb"), str(scratch / "check.py")], cwd=scratch, env=env, stdin=subprocess.DEVNULL, stdout=stdout, stderr=stderr, timeout=15, preexec_fn=limits)
                code = result.returncode
            except subprocess.TimeoutExpired:
                code = 124
        return {"exit_code": code, "stdout": (scratch / "stdout").read_bytes()[:65536].decode(errors="replace"), "stderr": (scratch / "stderr").read_bytes()[:65536].decode(errors="replace"), "sandbox": "macos-seatbelt", "source_sha256": digest(source)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("phase", choices=("prepare", "dispatch", "review", "validate", "request-changes", "accept"))
    parser.add_argument("--root", required=True)
    parser.add_argument("--wrapper", required=True)
    parser.add_argument("--provider", choices=("claude", "codex"))
    parser.add_argument("--provider-lock")
    parser.add_argument("--model")
    parser.add_argument("--candidate-oid")
    parser.add_argument("--source-sha256")
    parser.add_argument("--max-active-ms", type=int, default=120000)
    parser.add_argument("--max-attempts", type=int, choices=(1, 2, 3), default=2)
    args = parser.parse_args()
    root, wrapper = Path(args.root), Path(args.wrapper)
    if not root.is_absolute() or not wrapper.is_absolute():
        parser.error("root and wrapper must be absolute")
    def invoke(*argv):
        return json.loads(command([str(wrapper), *argv]))
    if args.phase == "prepare":
        if not args.provider or not args.provider_lock or not args.model:
            parser.error("prepare requires provider, confirmed provider-lock and explicit model")
        root.mkdir(mode=0o700, parents=True, exist_ok=False)
        root = root.resolve()
        repo = root / "repo"
        repo.mkdir(mode=0o700)
        for argv in (("init", "-q", "-b", "main"), ("config", "user.name", "Trial"), ("config", "user.email", "trial@example.invalid")):
            command(["git", *argv], repo)
        (repo / "calc.py").write_text("def add(a, b):\n    raise NotImplementedError\n")
        command(["git", "add", "calc.py"], repo)
        command(["git", "commit", "-qm", "trial input"], repo)
        identity = digest(str(root).encode())[:24]
        save(root / "trial.json", {"wrapper": str(wrapper), "repo": str(repo), "base_oid": command(["git", "rev-parse", "HEAD"], repo).strip(), "request_id": "coding-" + identity})
        save(root / "dispatch.json", {"version": 1, "request_id": "coding-" + identity, "provider": args.provider, "provider_lock": str(Path(args.provider_lock).resolve()), "model": args.model, "directory": str(repo), "prompt": "Implement add(a,b) in calc.py using only Python a+b semantics. Do not add input validation or special type handling in this first revision. Only edit calc.py. Preserve the original checkout and return a managed candidate. Acceptance: positive/negative/zero/float addition. Do not delegate.", "role": "implementer", "paths": ["calc.py"], "acceptance": ["Python a+b semantics, without input validation"], "max_attempts": args.max_attempts, "max_active_ms": args.max_active_ms})
        print(json.dumps({"status": "prepared", "root": str(root)}))
        return
    root = root.resolve()
    trial = json.loads((root / "trial.json").read_text())
    if trial["wrapper"] != str(wrapper):
        raise ValueError("original_runtime_binding_required")
    if args.phase == "dispatch":
        result = invoke("task", "dispatch", "--request", str(root / "dispatch.json"), "--state-dir", str(root / "state"))
        save(root / "dispatch-result.json", result)
        save(root / "wait.json", invoke("task", "wait", "--handle", result["handle"], "--timeout-ms", "30000"))
        print(json.dumps(result))
        return
    handle = json.loads((root / "dispatch-result.json").read_text())["handle"]
    def check_head():
        if command(["git", "rev-parse", "HEAD"], trial["repo"]).strip() != trial["base_oid"]:
            raise ValueError("original_HEAD_changed")

    def replay_ack(name):
        save(root / (name + "-result.json"), invoke("task", "ack", "--handle", handle, "--request", str(root / (name + ".json"))))

    def finish_accept():
        check_head()
        decision = json.loads((root / "accept.json").read_text())
        save(root / "accept-result.json", invoke("task", "accept", "--handle", handle, "--request", str(root / "accept.json")))
        if not (root / "ack.json").exists():
            if not (root / "accept-collect.json").exists():
                checkpoint(root / "accept-collect.json", invoke("task", "collect", "--handle", handle))
            page = json.loads((root / "accept-collect.json").read_text())
            checkpoint(root / "ack.json", collection_ack(page, decision["task_id"], decision, trial["request_id"] + "-ack-2"))
        replay_ack("ack")
        final = invoke("task", "inspect", "--handle", handle)
        save(root / "final-inspect.json", final)
        if len(final["tasks"]) != 1 or final["tasks"][0]["task_id"] != decision["task_id"] or final["tasks"][0]["status"] != "completed" or final["tasks"][0]["work_revision"] != decision["work_revision"]:
            raise ValueError("acceptance_not_completed")
        check_head()
        print(json.dumps({"status": "accepted_and_acknowledged", "handle": handle}))

    # Reuse exact saved commands even after completed summaries lose reviewable_result.
    if args.phase == "accept" and (root / "accept.json").exists():
        finish_accept()
        return
    if args.phase == "request-changes" and (root / "feedback.json").exists():
        check_head()
        replay_ack("review-1-ack")
        save(root / "feedback-result.json", invoke("task", "request-changes", "--handle", handle, "--request", str(root / "feedback.json")))
        return
    summary = invoke("task", "inspect", "--handle", handle)
    save(root / "inspect.json", summary)
    task = summary["tasks"][0]
    event = task.get("reviewable_result")
    if not event:
        raise ValueError("current_reviewable_result_required")
    revision = task["work_revision"]
    if args.phase == "review":
        page = invoke("task", "collect", "--handle", handle)
        checkpoint(root / f"collect-{revision}.json", page)
        save(root / "collect.json", page)
        matches = [item for item in page["events"] if item["event_id"] == event["event_id"]]
        if len(matches) != 1:
            raise ValueError("reviewable_event_not_collected")
        artifact = matches[0]["artifact"]
        content = Path(artifact["path"]).read_bytes()
        if digest(content) != artifact["sha256"]:
            raise ValueError("artifact_digest_changed")
        candidate = json.loads(content)["candidate"]
        if candidate["candidate_oid"] != event["candidate_oid"]:
            raise ValueError("candidate_binding_mismatch")
        source = candidate_source(trial["repo"], candidate["candidate_oid"])
        diff = command(["git", "diff", candidate["base_oid"], candidate["candidate_oid"], "--", "calc.py"], trial["repo"])
        save(root / f"candidate-{revision}.json", candidate)
        (root / f"candidate-{revision}.py").write_bytes(source)
        (root / f"candidate-{revision}.diff").write_text(diff)
        review = {"work_revision": revision, "candidate_oid": candidate["candidate_oid"], "source_sha256": digest(source), "artifact_sha256": artifact["sha256"], "diff_sha256": digest(diff.encode())}
        save(root / f"review-{revision}.json", review)
        print(json.dumps(review))
        return
    review = json.loads((root / f"review-{revision}.json").read_text())
    source = (root / f"candidate-{revision}.py").read_bytes()
    actual = candidate_source(trial["repo"], event["candidate_oid"])
    if review["candidate_oid"] != event["candidate_oid"] or digest(source) != review["source_sha256"] or digest(actual) != review["source_sha256"]:
        raise ValueError("review_source_binding_changed")
    if args.phase == "validate":
        if args.candidate_oid != review["candidate_oid"] or args.source_sha256 != review["source_sha256"]:
            raise ValueError("explicit_reviewed_candidate_and_source_required")
        result = sandbox_validate(source, revision, root)
        result.update(candidate_oid=review["candidate_oid"], work_revision=revision)
        save(root / f"validation-{revision}.json", result)
        print(json.dumps(result))
        return
    if command(["git", "rev-parse", "HEAD"], trial["repo"]).strip() != trial["base_oid"]:
        raise ValueError("original_HEAD_changed")
    if args.phase == "request-changes":
        if revision != 1:
            raise ValueError("expected_revision_one")
        brief = {"work_revision": 1, "candidate_oid": event["candidate_oid"], "feedback": "New requirement for revision 2: accept only int/float values, explicitly reject bool and all other types (including strings) with TypeError. Preserve positive, negative, zero and float addition. This is a new boundary requirement, not an assertion that the earlier model failed. Make the substantive implementation change in calc.py; do not delegate.", "acceptance": "type(a) and type(b) must each be exactly int or float; bool/string/list/None raise TypeError; add(2,3)==5, add(-2,3)==1, add(0,0)==0, add(1.5,2.25)==3.75", "command_id": trial["request_id"] + "-numeric-boundary"}
        page = json.loads((root / "collect-1.json").read_text())
        checkpoint(root / "review-1-ack.json", collection_ack(page, task["task_id"], event, trial["request_id"] + "-ack-1"))
        checkpoint(root / "feedback.json", brief)
        replay_ack("review-1-ack")
        save(root / "feedback-result.json", invoke("task", "request-changes", "--handle", handle, "--request", str(root / "feedback.json")))
        return
    if revision != 2:
        raise ValueError("expected_revision_two")
    first = json.loads((root / "candidate-1.json").read_text())
    second = json.loads((root / "candidate-2.json").read_text())
    first_review = json.loads((root / "review-1.json").read_text())
    validation = json.loads((root / "validation-2.json").read_text())
    if (validation["exit_code"] or validation["candidate_oid"] != review["candidate_oid"] or validation["source_sha256"] != review["source_sha256"] or validation["sandbox"] != "macos-seatbelt" or first["candidate_oid"] == second["candidate_oid"] or second["base_oid"] != first["candidate_oid"] or first_review["source_sha256"] == review["source_sha256"]):
        raise ValueError("candidate_lineage_or_validation_failed")
    decision = {key: event[key] for key in ("event_id", "event_revision", "event_hash", "action_slot")}
    decision.update(task_id=task["task_id"], work_revision=2, command_id=trial["request_id"] + "-accept-two", decision="accept")
    checkpoint(root / "accept.json", decision)
    finish_accept()


if __name__ == "__main__":
    try:
        main()
    except CommandFailure as failure:
        print(json.dumps(failure.diagnostic))
        sys.exit(2)
